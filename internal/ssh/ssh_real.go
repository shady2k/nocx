package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	agent "golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// RealClientOption configures the RealClient constructor.
type RealClientOption func(*RealClient)

// WithKnownHostsFile sets an explicit known_hosts path.
func WithKnownHostsFile(path string) RealClientOption {
	return func(rc *RealClient) { rc.knownHostsFile = path }
}

// WithConfigResolver sets the SSH config resolver for the RealClient.
// When set, ~/.ssh/config HostName resolution and config merging use the
// injected resolver instead of running ssh -G internally. Tests use a
// stub resolver; production uses NewSSHConfigResolver.
func WithConfigResolver(resolver ConfigResolver) RealClientOption {
	return func(rc *RealClient) { rc.configResolver = resolver }
}

// RealClient is a production SSH client that connects to remote hosts
// via golang.org/x/crypto/ssh. Connections are pooled and ref-counted
// (AD-4): tabs targeting the same host+identity+route share one
// authenticated ssh.Client, and the connection (including any jump
// transport) closes when the last referencing tab closes.
type RealClient struct {
	log            log.Logger
	knownHostsFile string
	configResolver ConfigResolver

	pool *ConnPool
}

// NewReal creates a RealClient with the given options.
func NewReal(logger log.Logger, opts ...RealClientOption) (*RealClient, error) {
	rc := &RealClient{
		log: logger.With("module", "ssh"),
	}
	home, _ := os.UserHomeDir()
	rc.knownHostsFile = filepath.Join(home, ".ssh", "known_hosts")

	for _, o := range opts {
		o(rc)
	}

	// If no resolver was injected, create a default ssh -G resolver pointing
	// at ~/.ssh/config with an empty sshPath (look up "ssh" on PATH).
	if rc.configResolver == nil {
		sshConfigPath := filepath.Join(home, ".ssh", "config")
		rc.configResolver = NewSSHConfigResolver(rc.log, sshConfigPath, "")
	}

	rc.pool = NewConnPool(logger)
	return rc, nil
}

// acquiredPooled bundles the result of resolving and acquiring a pooled
// connection: the raw client, the pooled wrapper (for per-connection
// features like agent forwarding), the handle owning this reference, and the
// caller's effective config. Connect and TunnelConn share this path so a
// forward is authorized and keyed exactly like a tab (AD-4) — a tunnel to a
// host the credential is not bound to is the same authorization violation as
// a tab to it.
type acquiredPooled struct {
	handle   *poolHandle
	pconn    *pooledSSHConn
	client   *gossh.Client
	resolved *resolvedConfig
	cfg      *ConnectConfig
}

// acquirePooled resolves the connection config, enforces credential
// authorization, and acquires a pooled connection. On success the caller
// owns the returned handle and MUST release it exactly once.
//
// Authorization is enforced BEFORE any dial. Only a linked credential
// (Secrets != nil) carries an authorized endpoint to check; inline auth has
// no stored secret to redirect.
//
// Resolve the authorized endpoint through ~/.ssh/config separately from the
// dial target. The resolver stores the canonical (resolved) hostname, and
// this pass re-resolves it through the current SSH config. If ~/.ssh/config
// changed since the resolver ran (drift), the two resolutions yield different
// results and the check fails. For binding tests that bypass the resolver,
// this pass also resolves aliases set directly as AuthorizedEndpoint.
//
// The host parameter and cfg.AuthorizedEndpoint are separate inputs, so
// comparing their resolved forms is not self-authorizing.
func (rc *RealClient) acquirePooled(ctx context.Context, host string, opts []ConnectOption) (*acquiredPooled, error) {
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}

	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve config for %s: %w", host, err)
	}
	if cfg.Secrets != nil {
		resolvedAuthz := rc.resolveAuthzEndpoint(ctx, cfg.AuthorizedEndpoint)
		if authErr := checkAuthorization(resolvedAuthz, resolved, string(cfg.SecretID), false); authErr != nil {
			return nil, authErr
		}
	}
	key := rc.poolKeyFor(ctx, resolved, cfg)
	handle, err := rc.pool.AcquireDial(ctx, key, rc.dialForConnect(ctx, host, resolved, cfg))
	if err != nil {
		return nil, err
	}

	pconn, ok := handle.conn.(*pooledSSHConn)
	if !ok {
		// dialForConnect always returns a *pooledSSHConn; a different type
		// means the pool's dial factory was overridden (tests). Release and bail.
		rc.pool.Release(handle)
		return nil, fmt.Errorf("internal: pooled connection is not a *pooledSSHConn (%T)", handle.conn)
	}
	gclient, ok := pconn.client.(*gossh.Client)
	if !ok {
		rc.pool.Release(handle)
		return nil, fmt.Errorf("internal: pooled client is not *gossh.Client (%T)", pconn.client)
	}
	return &acquiredPooled{handle: handle, pconn: pconn, client: gclient, resolved: resolved, cfg: cfg}, nil
}

// Connect implements SSH.Connect.
func (rc *RealClient) Connect(ctx context.Context, host string, opts ...ConnectOption) (Channel, error) {
	acq, err := rc.acquirePooled(ctx, host, opts)
	if err != nil {
		return nil, err
	}

	// Agent forwarding: register the per-connection channel handler once
	// (initAgentForward is guarded by agentForwardOnce), then request it
	// per-session inside openShell. Fail early if the user asked for
	// forwarding but no agent is reachable.
	if acq.cfg.AgentForward {
		if !rc.agentAvailable() {
			rc.pool.Release(acq.handle)
			return nil, fmt.Errorf("agent forwarding requested but no SSH agent available (SSH_AUTH_SOCK not set)")
		}
		if fwdErr := acq.pconn.initAgentForward(acq.client, os.Getenv("SSH_AUTH_SOCK")); fwdErr != nil {
			rc.pool.Release(acq.handle)
			return nil, fmt.Errorf("agent-forward setup: %w", fwdErr)
		}
	}

	ch, err := rc.openShell(ctx, acq.client, acq.resolved, acq.cfg, func() { rc.pool.Release(acq.handle) })
	if err != nil {
		// Failed to open the shell — release our reference so the
		// connection can close if we were the only tab. Without this the
		// failed Connect path leaks a pooled ref (and a jump transport)
		// for the process life.
		rc.pool.Release(acq.handle)
		return nil, err
	}

	// openShell wired the channel's close to release our pool reference.
	// RealChannel.Close runs closeCb exactly once (sync.Once), so the handle
	// is released exactly once even if the session errors and the tab then
	// closes. Releasing the handle drops the target refcount; when it hits
	// zero the pooledSSHConn closes the gossh.Client AND releases the jump
	// handle, which closes the bastion when its own refcount hits zero. One
	// Close per channel, one Release per handle, no leak.
	return ch, nil
}

// BYPASSING the connection pool. It verifies the host key, authenticates
// (sending exactly one auth method — the caller's responsibility to restrict),
// and closes immediately without launching a shell or running a command.
//
// This is the primitive for credential validation where MaxAuthTries is
// finite: sending several passwords against one host causes account lockouts
// and is indistinguishable from password spraying. The caller MUST supply
// exactly ONE auth method per Probe call.
//
// Host key verification runs before authentication, via the standard
// known_hosts callback. If the host is unknown or the key has changed,
// Probe returns the appropriate error (ErrUnknownHostKey or
// ErrHostKeyMismatch) without attempting authentication.
func (rc *RealClient) Probe(ctx context.Context, host string, authMethod gossh.AuthMethod, opts ...ConnectOption) error {
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}

	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return fmt.Errorf("probe: resolve config: %w", err)
	}

	hostKeyCB, err := rc.hostKeyCallback()
	if err != nil {
		return fmt.Errorf("probe: host key callback: %w", err)
	}

	addr := net.JoinHostPort(resolved.hostName, fmt.Sprintf("%d", resolved.port))
	timeout := cfg.ReadyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// Only one auth method — the caller decides which single method to send.
	gcfg := &gossh.ClientConfig{
		User:            resolved.user,
		Auth:            []gossh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCB,
		Timeout:         timeout,
	}

	d := &dialer{client: rc}
	// Use dialDirect (bypasses pool) to establish and authenticate.
	gclient, err := d.dialDirect(ctx, addr, gcfg, host, resolved.user)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	// Authenticated successfully — close immediately, no shell.
	_ = gclient.Close()
	return nil
}

// Close implements SSH.Close. It closes every pooled connection regardless
// of refcount — used during shutdown. Ordinary tab closure releases a
// single handle via the channel's closeCb and never reaches here.
func (rc *RealClient) Close() error {
	rc.pool.CloseAll()
	return nil
}

// hostKeyCallback builds a direct-route HostKeyCallback from known_hosts.
func (rc *RealClient) hostKeyCallback() (gossh.HostKeyCallback, error) {
	return rc.hostKeyCallbackFor("")
}

// hostKeyCallbackFor builds a HostKeyCallback whose lookup identity may differ
// from the dial address. A jump target uses a stable route identity so its key
// can coexist with the key presented by the same hostname on a direct route.
// The error still names the real target address; KnownHostsAddr is the opaque
// value the trust path must write back.
func (rc *RealClient) hostKeyCallbackFor(knownHostsAddr string) (gossh.HostKeyCallback, error) {
	cb, err := knownhosts.New(rc.knownHostsFile)
	return func(addr string, remote net.Addr, key gossh.PublicKey) error {
		lookupAddr := knownHostsAddr
		if lookupAddr == "" {
			lookupAddr = addr
		}
		if err != nil {
			return unknownHostKeyError(addr, lookupAddr, key)
		}
		if checkErr := cb(lookupAddr, remote, key); checkErr != nil {
			var keyErr *knownhosts.KeyError
			if !errors.As(checkErr, &keyErr) {
				return checkErr
			}
			if len(keyErr.Want) == 0 {
				return unknownHostKeyError(addr, lookupAddr, key)
			}
			expected := make([]string, 0, len(keyErr.Want))
			for _, k := range keyErr.Want {
				expected = append(expected, gossh.FingerprintSHA256(k.Key))
			}
			return &ErrHostKeyMismatch{
				Addr:           addr,
				KnownHostsAddr: lookupAddr,
				KeyAlgo:        key.Type(),
				Fingerprint:    gossh.FingerprintSHA256(key),
				Expected:       strings.Join(expected, ","),
				Key:            key.Marshal(),
			}
		}
		return nil
	}, nil
}

func unknownHostKeyError(addr, knownHostsAddr string, key gossh.PublicKey) error {
	return &ErrUnknownHostKey{
		Addr:           addr,
		KnownHostsAddr: knownHostsAddr,
		KeyAlgo:        key.Type(),
		Fingerprint:    gossh.FingerprintSHA256(key),
		Key:            key.Marshal(),
	}
}

// probeHostKeyCallback returns a HostKeyCallback that captures the observed
// host key fingerprint on every call (success or failure). The capture is set
// before the callback returns, so reading *capture after dialDirect returns
// gives the fingerprint that was presented by the server — even when the key
// was rejected (unknown host key, key mismatch). For unreachable hosts the
// callback is never invoked and *capture is empty.
//
// The closure captures a stack-allocated string by reference; escape analysis
// promotes it to the heap. The returned func owns the only reference, so
// *capture is safe to read after the dial completes and before the closure is
// collected.
func (rc *RealClient) probeHostKeyCallback(knownHostsAddr string) (gossh.HostKeyCallback, *string, error) {
	var captured string
	cb, err := rc.hostKeyCallbackFor(knownHostsAddr)
	if err != nil {
		return nil, nil, err
	}
	return func(addr string, remote net.Addr, key gossh.PublicKey) error {
		captured = gossh.FingerprintSHA256(key)
		return cb(addr, remote, key)
	}, &captured, nil
}

// knownHostsTargetAddr returns the storage identity for a target's host key.
// Direct routes keep the real OpenSSH-compatible address. Jump routes use a
// hostname-safe SHA-256 digest of the target endpoint and every hop endpoint.
// Authentication material and profile IDs are intentionally absent: changing
// a credential must not invalidate trust in the same network route.
func knownHostsTargetAddr(targetAddr string, cfg *ConnectConfig) string {
	if cfg == nil || (cfg.JumpHost == "" && cfg.JumpConfig == nil) {
		return targetAddr
	}

	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "nocx-known-hosts-route-v1\ntarget=%s\n", knownhosts.Normalize(targetAddr))
	for current := cfg; current != nil && (current.JumpHost != "" || current.JumpConfig != nil); {
		port := current.JumpPort
		if port <= 0 && current.JumpConfig != nil {
			port = current.JumpConfig.Port
		}
		if port <= 0 {
			port = 22
		}
		hopAddr := net.JoinHostPort(current.JumpHost, fmt.Sprintf("%d", port))
		_, _ = fmt.Fprintf(hash, "jump=%s\n", knownhosts.Normalize(hopAddr))
		current = current.JumpConfig
	}

	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash.Sum(nil))
	routeHost := "nocx-v1-" + strings.ToLower(encoded)
	return net.JoinHostPort(routeHost, "22")
}

// TrustHostKey records the offered public key for addr in known_hosts. It is
// the one write path this package owns for the host key store, and the only
// operation that ever touches it.
//
// It REPLACES rather than merely appends, and that distinction is the whole
// security value of the operation. A key that has changed is answered by
// pressing "trust the new key"; appending the new line and leaving the old one
// in place would keep the old key valid too, because knownhosts accepts a
// presented key that matches any line for the host — so the party holding the
// key the user has just rejected would still pass.
//
// # Which lines cover the host is knownhosts' question to answer, not ours
//
// The file format is OpenSSH's: host fields carry `*`/`?` wildcards, may list
// several comma-separated patterns, may negate one with `!`, and may be hashed.
// knownhosts implements all of that, and it is the same package that VERIFIES
// the key on the next connection (hostKeyCallbackFor). This function therefore
// asks it rather than deciding for itself: a second derivation of "does this
// line cover this host" is what nocx-9224 was — the two agreed everywhere
// anybody looked and disagreed on wildcards, so a `*.example.com` line survived
// the write and went on verifying the key the user had just rejected.
//
// The query is a probe with a key that cannot be in any file (knownHostsProbe),
// which makes knownhosts report EVERY covering line instead of stopping at the
// first that matches, with the line numbers to act on.
//
// # What happens to each covering line
//
//   - A line naming only this host (one exact pattern, or one hashed pattern —
//     a hash names exactly one host) is REMOVED.
//   - A wildcard, or a list naming other hosts too, is NARROWED: `!<addr>` is
//     appended to its pattern list, which stops the line covering this host and
//     leaves every sibling it was written for untouched. Deleting it instead
//     would silently revoke trust for hosts the user never asked about.
//   - `@cert-authority` is LEFT ALONE. It authorises host certificates, which is
//     a different trust mechanism from the raw key that was rejected.
//   - `@revoked` cannot appear: knownhosts indexes revocations by key, globally,
//     and never puts them among the host lines. That is load-bearing — deleting
//     one would re-enable a deliberately banned key for every host.
//
// Every algorithm goes, not just the presented one. A changed host identity that
// left the host's old RSA key valid would still admit whoever holds it, which is
// the attack the mismatch warning exists to report; `ssh-keygen -R` removes all
// of a host's entries for the same reason.
//
// # Refusing
//
// A file knownhosts cannot parse is one whose meaning we cannot establish, so
// the write refuses and names the file. Appending to it would report a trust
// that was not achieved — and the read path currently degrades such a file to
// "host unknown" on every connection, so the user is already not being verified.
//
// A missing file is not that case: it is an empty store, created 0600 under
// parents created 0700.
//
// # Atomicity
//
// A rewrite goes to a temp file in the same directory, is VALIDATED there, and
// only then renamed — validating after the rename would leave a bad file in
// place while returning an error. The postcondition is asserted, not assumed:
// exactly one raw-key line covers the host afterwards, it carries the accepted
// key, and the only other survivors are the same `@cert-authority` lines as
// before. An existing file keeps its own mode, which is the user's to choose.
//
// keyBlob is the wire-format marshalled public key (gossh.PublicKey.Marshal),
// as carried by ErrUnknownHostKey.Key / ErrHostKeyMismatch.Key. addr is the
// address exactly as the host key callback saw it, so the written line matches
// the next probe's known_hosts lookup (knownhosts.Line normalizes host:22 →
// host and host:2222 → [host]:2222 for us).
func (rc *RealClient) TrustHostKey(addr string, keyBlob []byte) (fingerprint string, err error) {
	key, parseErr := gossh.ParsePublicKey(keyBlob)
	if parseErr != nil {
		return "", fmt.Errorf("trust host key: invalid public key: %w", parseErr)
	}
	line := knownhosts.Line([]string{addr}, key) + "\n"

	dir := filepath.Dir(rc.knownHostsFile)
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return "", fmt.Errorf("trust host key: create directory %s: %w", dir, mkdirErr)
	}

	existing, readErr := os.ReadFile(rc.knownHostsFile)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("trust host key: read %s: %w", rc.knownHostsFile, readErr)
	}
	if errors.Is(readErr, os.ErrNotExist) || len(existing) == 0 {
		f, openErr := os.OpenFile(rc.knownHostsFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if openErr != nil {
			return "", fmt.Errorf("trust host key: open %s: %w", rc.knownHostsFile, openErr)
		}
		defer func() { _ = f.Close() }()
		if _, writeErr := f.WriteString(line); writeErr != nil {
			return "", fmt.Errorf("trust host key: append to %s: %w", rc.knownHostsFile, writeErr)
		}
		return gossh.FingerprintSHA256(key), nil
	}

	covering, coverErr := coveringLines(dir, string(existing), addr)
	if coverErr != nil {
		return "", fmt.Errorf("trust host key: %s: %w", rc.knownHostsFile, coverErr)
	}
	if len(covering) == 0 {
		f, openErr := os.OpenFile(rc.knownHostsFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if openErr != nil {
			return "", fmt.Errorf("trust host key: open %s: %w", rc.knownHostsFile, openErr)
		}
		defer func() { _ = f.Close() }()
		if _, writeErr := f.WriteString(line); writeErr != nil {
			return "", fmt.Errorf("trust host key: append to %s: %w", rc.knownHostsFile, writeErr)
		}
		return gossh.FingerprintSHA256(key), nil
	}

	rewritten, authorities := revokeCoveringLines(string(existing), covering, addr)
	if err := rc.replaceKnownHosts(rewritten+line, addr, key, authorities); err != nil {
		return "", err
	}
	return gossh.FingerprintSHA256(key), nil
}

// knownHostsProbe is a public key no known_hosts line can carry. knownhosts
// compares keys by their marshalled bytes and never verifies a signature on
// this path, and a parsed key always marshals to a length-prefixed type name —
// so a one-byte blob matches nothing that parsed. Probing with it makes the
// library enumerate every line covering a host instead of stopping at the first
// whose key matches, which is how this package asks "which lines cover this
// host" without owning a second answer to the question.
type knownHostsProbe struct{}

func (knownHostsProbe) Type() string    { return "nocx-known-hosts-probe" }
func (knownHostsProbe) Marshal() []byte { return []byte{0} }
func (knownHostsProbe) Verify([]byte, *gossh.Signature) error {
	return errors.New("nocx: the known_hosts probe key never verifies anything")
}

// coveringLines returns the 1-based physical line numbers of every entry in
// content that knownhosts would consult when verifying addr, in ascending
// order. The snapshot is written to a temp file beside the store and parsed
// from there, so the numbers describe the bytes this operation is about to
// rewrite rather than whatever the file says by the time we look again.
func coveringLines(dir, content, addr string) ([]int, error) {
	snapshot, createErr := os.CreateTemp(dir, ".known_hosts-probe-*")
	if createErr != nil {
		return nil, fmt.Errorf("create probe snapshot in %s: %w", dir, createErr)
	}
	name := snapshot.Name()
	defer func() { _ = os.Remove(name) }()
	if _, writeErr := snapshot.WriteString(content); writeErr != nil {
		_ = snapshot.Close()
		return nil, fmt.Errorf("write probe snapshot: %w", writeErr)
	}
	if closeErr := snapshot.Close(); closeErr != nil {
		return nil, fmt.Errorf("close probe snapshot: %w", closeErr)
	}
	return coveringLinesInFile(name, addr)
}

// coveringLinesInFile is coveringLines against a file that already exists.
func coveringLinesInFile(path, addr string) ([]int, error) {
	cb, newErr := knownhosts.New(path)
	if newErr != nil {
		// The verifier cannot read this file either. Say so rather than
		// appending to something whose meaning is unknown.
		return nil, fmt.Errorf("known_hosts does not parse: %w", newErr)
	}
	err := cb(addr, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, knownHostsProbe{})
	if err == nil {
		// Unreachable: the probe key cannot equal a parsed key. Treated as
		// "nothing covers the host" rather than trusted silently.
		return nil, nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return nil, fmt.Errorf("known_hosts lookup: %w", err)
	}
	lines := make([]int, 0, len(keyErr.Want))
	for _, want := range keyErr.Want {
		lines = append(lines, want.Line)
	}
	sort.Ints(lines)
	return lines, nil
}

// revokeCoveringLines rewrites the numbered lines so none of them covers addr,
// and reports the @cert-authority lines it deliberately left in place — the
// only entries allowed to still cover the host afterwards.
//
// Untouched lines are preserved byte for byte, including their line endings and
// whether the file ends in a newline, because this is the user's file.
func revokeCoveringLines(content string, covering []int, addr string) (rewritten string, authorities []string) {
	target := make(map[int]bool, len(covering))
	for _, n := range covering {
		target[n] = true
	}
	negation := "!" + knownhosts.Normalize(addr)

	var b strings.Builder
	for i, physical := range strings.SplitAfter(content, "\n") {
		lineNo := i + 1
		if !target[lineNo] {
			b.WriteString(physical)
			continue
		}
		body := strings.TrimSuffix(physical, "\n")
		if isCertAuthorityLine(body) {
			authorities = append(authorities, strings.TrimSpace(body))
			b.WriteString(physical)
			continue
		}
		narrowed, keep := narrowHostField(body, knownhosts.Normalize(addr), negation)
		if !keep {
			continue
		}
		b.WriteString(narrowed)
		if strings.HasSuffix(physical, "\n") {
			b.WriteString("\n")
		}
	}
	rewritten = b.String()
	if rewritten != "" && !strings.HasSuffix(rewritten, "\n") {
		rewritten += "\n"
	}
	return rewritten, authorities
}

// isCertAuthorityLine reports whether a line declares a host certificate
// authority rather than a raw host key.
func isCertAuthorityLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "@cert-authority")
}

// narrowHostField stops a line covering one host. A line whose pattern list is
// only that host is dropped (keep=false) rather than left as a contradiction;
// anything else keeps its key and gains a negation, so the hosts it was written
// for keep theirs.
func narrowHostField(line, normalized, negation string) (narrowed string, keep bool) {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	rest := line[len(indent):]
	// Fields are separated by a space OR a tab, the same way knownhosts splits
	// them. Cutting on the space alone would leave a tab-separated line's key
	// inside the host field and write a pattern that matches nothing.
	sep := strings.IndexAny(rest, " \t")
	if sep < 0 {
		// No key on the line; knownhosts would not have parsed it, so this is
		// unreachable. Leave it alone rather than mangle it.
		return line, true
	}
	field, tail := rest[:sep], rest[sep+1:]
	separator := rest[sep : sep+1]
	patterns := strings.Split(field, ",")
	if len(patterns) == 1 && (patterns[0] == normalized || strings.HasPrefix(patterns[0], "|")) {
		return "", false
	}
	return indent + field + "," + negation + separator + tail, true
}

// replaceKnownHosts installs content atomically and only if it means what the
// caller intended. The candidate is written beside the store, VALIDATED there,
// and renamed last: validating after the rename would leave a file we know to
// be wrong in place while returning an error, which is the opposite of what an
// atomic write is for. An interrupt therefore leaves either the old file or the
// new one, never a truncated or a wrong one. The file keeps the mode it had.
//
// The postcondition is the security statement of the whole operation, so it is
// checked rather than believed: after the write, the accepted key verifies for
// addr, exactly one raw-key entry covers addr, and the only other entries that
// cover it are the same @cert-authority lines that covered it before.
func (rc *RealClient) replaceKnownHosts(content, addr string, key gossh.PublicKey, authorities []string) error {
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(rc.knownHostsFile); statErr == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(rc.knownHostsFile)
	tmp, createErr := os.CreateTemp(dir, ".known_hosts-*")
	if createErr != nil {
		return fmt.Errorf("trust host key: create temp in %s: %w", dir, createErr)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, writeErr := tmp.WriteString(content); writeErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("trust host key: write %s: %w", tmpName, writeErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return fmt.Errorf("trust host key: close %s: %w", tmpName, closeErr)
	}
	if chmodErr := os.Chmod(tmpName, mode); chmodErr != nil {
		return fmt.Errorf("trust host key: chmod %s: %w", tmpName, chmodErr)
	}
	if err := validateTrustWrite(tmpName, content, addr, key, authorities); err != nil {
		return fmt.Errorf("trust host key: refusing to install %s: %w", rc.knownHostsFile, err)
	}
	if renameErr := os.Rename(tmpName, rc.knownHostsFile); renameErr != nil {
		return fmt.Errorf("trust host key: rename onto %s: %w", rc.knownHostsFile, renameErr)
	}
	return nil
}

// validateTrustWrite asks knownhosts what the candidate file means for addr and
// refuses anything but the intended outcome.
func validateTrustWrite(path, content, addr string, key gossh.PublicKey, authorities []string) error {
	cb, newErr := knownhosts.New(path)
	if newErr != nil {
		return fmt.Errorf("the rewritten file does not parse: %w", newErr)
	}
	remote := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
	if err := cb(addr, remote, key); err != nil {
		return fmt.Errorf("the accepted key would not verify: %w", err)
	}

	covering, coverErr := coveringLinesInFile(path, addr)
	if coverErr != nil {
		return coverErr
	}
	physical := strings.SplitAfter(content, "\n")
	rawKeyLines := 0
	survivingAuthorities := 0
	for _, lineNo := range covering {
		if lineNo < 1 || lineNo > len(physical) {
			return fmt.Errorf("line %d is outside the rewritten file", lineNo)
		}
		if isCertAuthorityLine(strings.TrimSuffix(physical[lineNo-1], "\n")) {
			survivingAuthorities++
			continue
		}
		rawKeyLines++
	}
	if rawKeyLines != 1 {
		return fmt.Errorf("%d raw host-key entries would still cover %s, want exactly the accepted one", rawKeyLines, addr)
	}
	if survivingAuthorities != len(authorities) {
		return fmt.Errorf("%d certificate-authority entries survived, want %d", survivingAuthorities, len(authorities))
	}
	return nil
}

// shellStartCommand decides what the remote session runs and whether shell
// integration happened. Precedence:
//
//  1. A RemoteCommand configured for the destination wins outright: OpenSSH
//     refuses a command-line remote command alongside it ("Cannot execute
//     command-line and remote command"), so the configured command runs
//     as-is and no launcher or installer is consulted (spec §4.2).
//  2. The desired mode (nocx-mlm7) gates everything else: raw publishes
//     nothing and opens a plain shell; relay behaves as raw in this epic;
//     script (or empty — the direct-host default) publishes and
//     integrates.
//  3. In script mode a saved connection publishes the bundle over SFTP
//     first, through the RemoteInstaller (P8's carrier): the publisher's
//     fail-open contract means the session still starts — transient-
//     integrated via the launcher, or raw — when the publish fails, and
//     the previous activation stays byte-identical.
//  4. The launcher (nocx-xs1d) then builds the integrated start command. A
//     profile pin (cfg.Shell) wins outright — a user who says "this host
//     runs zsh" knows something the detector cannot (nocx-6rj0). Unpinned,
//     the launcher receives ShellAuto and emits a strictly-POSIX
//     dispatcher that detects the far login shell at runtime — the only
//     layer that knows which shell is at the far end — and execs the
//     matching tier: bash → bash, zsh → zsh, anything else (dash, ash, ksh,
//     csh, …) → the minimal tier. The dispatcher never guesses bash: an
//     undetectable shell degrades to the minimal tier, whose fail-open
//     starts an ordinary plain login shell (ADR-0004:60). A decline (or a
//     contract-violating result) falls back to a plain shell with the
//     reason: an ordinary, usable terminal with a visible native prompt is
//     absolute, no failure path may suppress it.
//  5. No launcher wired: the installer's own start command (the §3.3
//     far-side guard) when one is, else a plain shell, reason none.
func (rc *RealClient) shellStartCommand(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig) (string, RefusalReason) {
	if resolved.remoteCommand != "" {
		return resolved.remoteCommand, ReasonRemoteCommand
	}

	// raw and relay publish nothing and integrate nothing (N1, §3.1; relay
	// is inert this epic). Unknown modes fail closed.
	if !modeAllowsIntegration(cfg.DesiredMode) {
		return "", ReasonNone
	}

	// A saved connection publishes the bundle over SFTP before the session
	// starts (design §4: the SFTP carrier hands the same descriptor to the
	// same Publish). Best-effort: a publish failure is logged and the
	// session still starts — fail-open (design §4.1).
	if cfg.RemoteInstaller != nil {
		remoteHome, err := cfg.RemoteInstaller.GetRemoteHome(gclient)
		if err != nil {
			rc.log.Warn("ssh: could not determine remote home for shell integration",
				"host", resolved.hostName, "error", err)
		} else if err := cfg.RemoteInstaller.EnsureInstalledRemote(ctx, gclient, remoteHome); err != nil {
			rc.log.Warn("ssh: shell integration publish failed",
				"host", resolved.hostName, "error", err)
		}
	}

	if cfg.RemoteLauncher != nil {
		shell := cfg.Shell
		if shell == "" {
			shell = ShellAuto
		}
		cmd, reason, ok := cfg.RemoteLauncher.StartCommand(shell, LaunchOptions{
			SessionID: cfg.SessionID,
			Enhanced:  cfg.Enhanced,
		})
		if ok && cmd != "" {
			return cmd, ReasonNone
		}
		// Decline or degenerate result: fall back to a plain shell. Normalize
		// a missing reason so the degrade stays visible in the product
		// (AGENTS.md: a soft degrade must never be log-only).
		if reason == "" {
			reason = ReasonUnsupportedShell
		}
		return "", reason
	}

	if cfg.RemoteInstaller != nil {
		return cfg.RemoteInstaller.RemoteStartCommand(), ReasonNone
	}

	return "", ReasonNone
}

// openShell opens a session, requests a PTY, optionally requests agent
// forwarding, and starts a shell. releaseRef drops the caller's pooled
// reference when the remote session ends; it is assigned to the channel
// BEFORE the session watcher starts, so the watcher can never observe the
// field unset (a race the old assign-after-openShell shape had: a session
// dying in the window left Close reading a nil callback or racing the
// write).
func (rc *RealClient) openShell(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig, releaseRef func()) (*RealChannel, error) {
	startCmd, reason := rc.shellStartCommand(ctx, gclient, resolved, cfg)

	session, err := gclient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}

	ptyReq := ptyReqMsg{
		Term:     "xterm-256color",
		Columns:  uint32(resolved.cols),
		Rows:     uint32(resolved.rows),
		Width:    uint32(resolved.xpixel),
		Height:   uint32(resolved.ypixel),
		Modelist: buildTerminalModes(),
	}
	_, err = session.SendRequest("pty-req", true, gossh.Marshal(&ptyReq))
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("pty-req: %w", err)
	}

	if cfg.AgentForward {
		// Per-connection handler already registered in Connect (initAgentForward).
		// Per-session: request agent forwarding on this session so the remote
		// side can open auth-agent@openssh.com channels. agent.RequestAgentForwarding
		// uses wantReply=true, so a server refusal surfaces as an error.
		if reqErr := agent.RequestAgentForwarding(session); reqErr != nil {
			_ = session.Close()
			return nil, fmt.Errorf("agent-forward request: %w", reqErr)
		}
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if startCmd != "" {
		if err := session.Start(startCmd); err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("shell start: %w", err)
		}
	} else {
		if err := session.Shell(); err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("shell: %w", err)
		}
	}

	ch := &RealChannel{
		log:                    rc.log.With("remote", resolved.hostName),
		session:                session,
		stdin:                  stdin,
		stdout:                 stdout,
		done:                   make(chan struct{}),
		shellIntegrationReason: reason,
		closeCb: func() {
			_ = session.Close()
		},
		releasePoolRef: releaseRef,
	}

	// The watcher starts AFTER releasePoolRef is set (above), so a session
	// that ends during the assignment window cannot race the field.
	go func() {
		_ = session.Wait()
		// Remote session ended — release the pool reference. Close is
		// idempotent (closeOnce), so if the tab already called Close this
		// is a no-op; if not, it closes the session, drops the ref, and
		// (for a jump-backed conn) releases the bastion handle.
		_ = ch.Close()
	}()

	return ch, nil
}

// isAuthError returns true if the error likely comes from a failed SSH authentication.
func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "ssh: handshake failed") ||
		strings.Contains(msg, "no common algorithms")
}
