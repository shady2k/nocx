package ssh

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
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
	// The authenticated lifecycle channel (ADR-0024 decision 2 "Over SSH"):
	// establish it before the start command is built, so the allocated
	// loopback port and the per-epoch capability are substituted into the
	// launch text. Refusal — the remote sshd will not forward — is
	// detectable synchronously and NOT distinguishable; the session then
	// opens as a conventional terminal with a visible native prompt, and
	// no diagnostic names a policy. Established only for enhanced sessions
	// with a launcher: without the channel the shell keeps its native
	// prompt (ADR-0024 decision 9).
	//
	// The desired mode is part of the gate for the same reason it gates
	// shellStartCommand (nocx-tr2n): raw and relay integrate nothing, so
	// their start command is a plain shell that will never dial the
	// forwarded port. Establishing anyway allocated a remote listener and
	// a domain that openShell closed unused on the next statement — free
	// while no ssh session was ever enhanced, and a round trip and a
	// remote listener per raw tab now that every session asks.
	var lc *lifecycleHandle
	if acq.cfg.Enhanced && modeAllowsIntegration(acq.cfg.DesiredMode) &&
		acq.cfg.RemoteLifecycle != nil && acq.cfg.RemoteLauncher != nil {
		launch, closer, lerr := acq.cfg.RemoteLifecycle.Establish(ctx, host, opts...)
		if lerr != nil {
			rc.log.Warn("ssh: lifecycle channel refused; session stays conventional",
				"host", host, "error", lerr)
		} else {
			lc = &lifecycleHandle{launch: launch, closer: closer}
		}
	}

	ch, err := rc.openShell(ctx, acq.client, acq.resolved, acq.cfg, func() { rc.pool.Release(acq.handle) }, lc)
	if err != nil {
		// Failed to open the shell — release our reference so the
		// connection can close if we were the only tab. Without this the
		// failed Connect path leaks a pooled ref (and a jump transport)
		// for the process life.
		lc.close()
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
//  3. In script mode the bundle is published over SFTP through the
//     RemoteInstaller, CONCURRENTLY with the loader (design §6.1 step 2,
//     §7). Its outcome is REPORTED AND NOT CONSULTED for which command is
//     emitted: the same carrier goes out whether the publish succeeded,
//     failed, or was never attempted. That is the carrier design's first
//     decision, and it is what the old shape got wrong — the command used to
//     begin with a far-side test of installation state, which on a first
//     contact fails while the publish that would have satisfied it is still
//     in flight. The far side stays the owner of "is this installation
//     valid"; that verification runs after the bootstrap settles, inside
//     stage-1. What the outcome DOES gate is the mint: §6.1 step 5 puts the
//     publish's terminal outcome ahead of the pair, to close the mutation
//     race where stage-1 verifies a manifest microseconds before an atomic
//     commit. It was sequential and inline until P5, which is why the
//     deadline arithmetic did not close: 3 + 3 + 10 is 16 against a 15 s
//     deadline, and concurrent it is 13.
//  4. The launcher builds the carrier: a bounded loader carrying no bundle
//     bytes and neither secret (shellintegration/carrier.go). A profile pin
//     (cfg.Shell) is still passed — a user who says "this host runs zsh"
//     knows something the detector cannot (nocx-6rj0) — though the carrier
//     is the same for every kind, because the far side dispatches after
//     stage-1 rather than in the command. A decline (or a
//     contract-violating result) falls back to a plain shell with the
//     reason: an ordinary, usable terminal with a visible native prompt is
//     absolute, no failure path may suppress it.
//  5. No launcher wired: a plain shell, reason none. There is deliberately
//     no installer-supplied command any more — a session either runs the
//     carrier or runs nothing at all.
func (rc *RealClient) shellStartCommand(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig, lc *lifecycleHandle) (string, RefusalReason, BootstrapRun) {
	if resolved.remoteCommand != "" {
		return resolved.remoteCommand, ReasonRemoteCommand, nil
	}

	// raw and relay publish nothing and integrate nothing (N1, §3.1; relay
	// is inert this epic). Unknown modes fail closed.
	if !modeAllowsIntegration(cfg.DesiredMode) {
		return "", ReasonNone, nil
	}

	// No launcher wired: the publish still runs — it is what puts a
	// generation on the far host — and the session opens a plain shell. The
	// installer supplies no command any more; a session either runs the
	// carrier or runs nothing at all.
	if cfg.RemoteLauncher == nil {
		rc.startPublish(ctx, gclient, resolved, cfg, nil)
		return "", ReasonNone, nil
	}

	shell := cfg.Shell
	if shell == "" {
		shell = ShellAuto
	}
	opts := LaunchOptions{
		SessionID: cfg.SessionID,
		Enhanced:  cfg.Enhanced,
	}
	// The lifecycle channel config addresses the carrier: lane, domain,
	// epoch and port are names and travel in the command. The
	// capability and the recovery fence are carried too, so the
	// launcher can prove it does not put them there — they reach the
	// far shell as a frame on the channel, never as command text. lc is
	// nil when establishment was refused; the launch then carries no
	// channel config and the shell stays conventional.
	if lc != nil {
		opts.Lane = lc.launch.Lane
		opts.Domain = lc.launch.Domain
		opts.Epoch = lc.launch.Epoch
		opts.LifecyclePort = lc.launch.Port
		opts.Capability = lc.launch.Capability
		opts.Recovery = lc.launch.Recovery
	}
	// The bootstrap is prepared first: the carrier commits to the
	// digest of the stage-1 frame, so the frame exists before the
	// command that names it does. Without a bootstrapper there is
	// nothing to feed the loader, and a loader with no sender blocks
	// on a frame that never arrives — so the session runs a plain
	// shell instead, with a named reason.
	digest, run, gate, prepared := cfg.RemoteLauncher.Prepare(shell, opts)
	if !prepared {
		rc.log.Warn("ssh: the shell bootstrap could not be prepared; the session runs a plain shell",
			"host", resolved.hostName, "shell", shell)
		// The publish still runs. It is what puts a generation on the
		// far host for the NEXT connection, and a session that cannot
		// bootstrap today is exactly the one that most needs the next
		// one to work.
		rc.startPublish(ctx, gclient, resolved, cfg, nil)
		return "", ReasonUnsupportedShell, nil
	}
	opts.StageDigest = digest

	// §6.1 step 2: the publish and the loader start CONCURRENTLY, and
	// §7 requires it — 3 + 3 + 10 is 16 against a 15 s integration
	// deadline, so a sequential publish cannot close. The publish runs
	// on its own auxiliary channel from here while the loader takes the
	// terminal and quarantines input; the mint waits for both through
	// the gate, and the longest path is 13 s rather than 16 — asserted on
	// the schedule graph rather than on a stopwatch, by
	// shellintegration's TestBootstrapSchedule_* assertions.
	//
	// Concurrent with the loader, and never with the CLAIM on the user's
	// session channel: openShell has already opened it, so this auxiliary
	// channel competes with nothing the user needs. See openShell.
	//
	// Step 4 is answered synchronously, because it already is: the
	// transport was established before the session was opened, so by
	// here the receiver either exists or never will.
	rc.startPublish(ctx, gclient, resolved, cfg, gate)
	if lc != nil {
		gate.ReceiverReady()
	} else {
		gate.ReceiverUnavailable(errors.New("ssh: no lifecycle channel for this session"))
	}
	cmd, reason, ok := cfg.RemoteLauncher.StartCommand(shell, opts)
	if ok && cmd != "" {
		// The bound, at the point of no return (nocx-e4ir3). A launcher is
		// a seam somebody else implements next, and the producer that put
		// 92 KiB and two bearers in a command was policing itself against
		// a cap beside it. Refused here, the command is never sent and the
		// session still fails open to a plain login shell — the reason is
		// named so the degrade is visible in the product, not only in a log.
		if len(cmd) >= MaxRemoteCommandLen {
			rc.log.Warn("ssh: the remote command is longer than the bound; the session runs a plain shell",
				"bytes", len(cmd), "bound", MaxRemoteCommandLen, "shell", string(shell))
			return "", ReasonCommandTooLong, nil
		}
		return cmd, ReasonNone, run
	}
	// Decline or degenerate result: fall back to a plain shell. Normalize
	// a missing reason so the degrade stays visible in the product
	// (AGENTS.md: a soft degrade must never be log-only). The lifecycle
	// channel is not used by a plain shell; openShell closes lc.
	if reason == "" {
		reason = ReasonUnsupportedShell
	}
	return "", reason, nil
}

// startPublish runs the SFTP publish on its own schedule and tells the gate
// when it has settled (design §6.1 step 5, §7).
//
// It used to run inline, ahead of everything, and that is what made the
// deadline arithmetic fail to close: the publish is bounded at T = 10 s and
// the receiver and the frame at 3 s each, so a sequential schedule costs 16 s
// against a 15 s deadline. Concurrent, the longest path is the publish plus
// the terminal outcome after frame 2 — 13 s.
//
// Its result stays a LOG and never an input to which command is emitted: the
// fail-open contract says a failed publish leaves the previous activation
// byte-identical and the session still starts, and the carrier is what starts
// it either way. What the result DOES decide is one thing — whether the gate
// opens with an error to name, so that a far side reporting
// generation-unavailable can be told that the reason there is no generation is
// that nocx could not write one.
//
// The context is deliberately not the caller's: Connect returns as soon as the
// session is started, and a cancelled connect context must not abort a publish
// that is already writing to the far host. The bound is the publisher's own T,
// which it enforces against its own clock; adding a second timer here would be
// a second, unsynchronised deadline for one budget.
func (rc *RealClient) startPublish(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig, gate BootstrapGate) {
	if cfg.RemoteInstaller == nil {
		// A terminal outcome all the same, and it must be: a gate waiting
		// for a fact nobody will ever supply is a session that never leaves
		// `starting`, which §7 forbids outright.
		if gate != nil {
			gate.PublishSettled(nil)
		}
		return
	}
	pctx := context.WithoutCancel(ctx)
	go func() {
		err := rc.publishBundle(pctx, gclient, resolved, cfg)
		if gate != nil {
			gate.PublishSettled(err)
		}
	}()
}

// publishBundle is the publish itself, separated so startPublish is about the
// schedule and this is about the work.
func (rc *RealClient) publishBundle(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig) error {
	remoteHome, err := cfg.RemoteInstaller.GetRemoteHome(gclient)
	if err != nil {
		rc.log.Warn("ssh: could not determine remote home for shell integration",
			"host", resolved.hostName, "error", err)
		return err
	}
	if err := cfg.RemoteInstaller.EnsureInstalledRemote(ctx, gclient, remoteHome); err != nil {
		rc.log.Warn("ssh: shell integration publish failed",
			"host", resolved.hostName, "error", err)
		return err
	}
	return nil
}

// openShell opens a session, requests a PTY, optionally requests agent
// forwarding, and starts a shell. releaseRef drops the caller's pooled
// reference when the remote session ends; it is assigned to the channel
// BEFORE the session watcher starts, so the watcher can never observe the
// field unset (a race the old assign-after-openShell shape had: a session
// dying in the window left Close reading a nil callback or racing the
// write). lc is the established lifecycle channel, or nil (refused or not
// wired); it is closed on every path that does not hand the shell a
// channel-using start command, and otherwise transferred to the channel.
//
// # The user's session channel is claimed FIRST, and that ordering is the
// product's promise rather than the scheduler's
//
// The session channel is opened before shellStartCommand runs, because
// shellStartCommand is what starts the publish, and the publish opens an
// auxiliary channel of its own. A server bounds the sessions it will grant one
// connection (OpenSSH's MaxSessions, and a subsystem counts), so with one slot
// those two are competing for it — and they were, in opposite directions:
// whichever asked first won, and when the publish won, gclient.NewSession()
// for the INTERACTIVE session was refused and Connect returned an error, so
// the user got no terminal at all. Measured before this line moved: 4 of 10
// attempts reached the working un-integrated prompt §0 promises and 6 reached
// nothing.
//
// ADR-0004 makes an ordinary usable terminal with a visible native prompt the
// one thing no failure path may suppress, and losing it to nocx's OWN
// auxiliary work is the single way to lose it that is nocx's fault. The typed
// path never had the defect — there the user's own `ssh` is the interactive
// session and it authenticates before nocx interposes — and one spelling of
// the rule is better than two (AD-8), so this is the saved path saying the
// same thing: the user's session exists before any channel of ours does.
//
// It does NOT make the publish sequential with the loader, and it must not:
// design §6.1 step 2 and §7's arithmetic need those concurrent (3 + 3 + 10 is
// 16 against a 15 s deadline; only the concurrent schedule closes at 13).
// Opening the session CHANNEL is not finishing the bootstrap — the loader has
// not been sent, the frames have not started, nothing has been minted. What
// happens between these two statements is one round trip for a channel and its
// pty; the publish then runs beside the loader on that same connection exactly
// as before.
func (rc *RealClient) openShell(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig, releaseRef func(), lc *lifecycleHandle) (*RealChannel, error) {
	sess, err := rc.openSessionWithPTY(gclient, resolved, cfg)
	if err != nil {
		lc.close()
		return nil, err
	}
	session, stdin, stdout := sess.session, sess.stdin, sess.stdout

	startCmd, reason, bootstrap := rc.shellStartCommand(ctx, gclient, resolved, cfg, lc)

	// A start command that does not use the lifecycle channel — the
	// launcher declined, the destination ran a configured remote command,
	// or no integration happened at all — leaves the channel unclaimed: a
	// plain shell (or a configured command) never connects to the
	// forwarded port, and holding the lease open would keep the pooled
	// connection (and its remote listener) alive for the process life.
	// Close it here, before the shell starts.
	if startCmd == "" || reason == ReasonRemoteCommand || !modeAllowsIntegration(cfg.DesiredMode) {
		lc.close()
	}

	// execAccepted records the REQUEST RESULT, which is what §6.4 says to
	// branch on. It is the discriminator for the sixth row: an accepted
	// request whose loader never announces itself is a substituted command,
	// not a refused one, and the two leave the user in opposite places.
	execAccepted := false
	if startCmd != "" {
		if startErr := session.Start(startCmd); startErr != nil {
			// §6.4 amendment A. The exec request was refused; whether the
			// channel survived it is a property of the server, so it is
			// OBSERVED rather than assumed — a shell on the same channel,
			// and a replacement session channel on the same connection if
			// that channel is gone. Neither costs a second authentication.
			recovered, rerr := rc.recoverFromRefusedExec(gclient, resolved, cfg, session, startErr)
			if rerr != nil {
				lc.close()
				_ = session.Close()
				return nil, rerr
			}
			session, stdin, stdout = recovered.session, recovered.stdin, recovered.stdout
			// What is on the far side now is a NATIVE login shell: our
			// loader never ran, so there is nothing to send frames to and
			// no channel for it to use. Sending them anyway would type them
			// into the user's shell.
			reason, bootstrap = ReasonExecRefused, nil
			lc.close()
		} else {
			execAccepted = true
		}
	} else {
		if err := session.Shell(); err != nil {
			lc.close()
			_ = session.Close()
			return nil, fmt.Errorf("shell: %w", err)
		}
	}

	// The input quarantine opens BEFORE the command is sent (design §5.3):
	// the session is bootstrapping and the user's keystrokes are refused,
	// not buffered — a buffered keystroke is a command the user did not
	// knowingly run, executed later. It closes at exactly one terminal
	// outcome, never at READY.
	//
	// The output side is a feed rather than the raw pipe for the same
	// interval: the bootstrap reads the far side's tokens, and the same
	// reader hands the remainder to the terminal afterwards, so no byte is
	// consumed by a wait that gave up.
	var out io.Reader = stdout
	gate := newInputGate(true)
	bootstrapDone := make(chan struct{})
	var feed *sessionFeed
	if bootstrap != nil {
		feed = newSessionFeed(stdout)
		out = feed
		gate = newInputGate(false)
	} else {
		close(bootstrapDone)
	}

	ch := &RealChannel{
		log:                    rc.log.With("remote", resolved.hostName),
		session:                session,
		stdin:                  stdin,
		stdout:                 out,
		done:                   make(chan struct{}),
		inputGate:              gate,
		bootstrapDone:          bootstrapDone,
		shellIntegrationReason: reason,
		closeCb: func() {
			_ = session.Close()
		},
		releasePoolRef: releaseRef,
		lifecycleClose: lc.close,
	}

	if bootstrap != nil {
		// The context is deliberately NOT the caller's: Connect returns
		// as soon as the session is started, and a cancelled connect
		// context must not abort a bootstrap that is already running on
		// a live session. The session's own end is what stops it, which
		// the stream reports as an error.
		bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		go func() {
			<-ch.done
			cancel()
		}()
		go func() {
			defer cancel()
			// Whatever the outcome, the gate opens: the interval
			// closes at the TERMINAL OUTCOME, and every one of them
			// leaves the far side on its way to a usable prompt.
			bootReason := bootstrap(bctx, bootstrapStream{sessionFeed: feed, w: stdin})
			if bootReason != ReasonNone {
				// §6.4's sixth row, named at the only moment it is
				// observable. The exec request was ACCEPTED, so the far
				// side agreed to run our loader; the loader never spoke
				// and the channel is already gone. Something else ran on
				// it, it reported its status, and no native prompt exists
				// on any channel of that connection — which is why this is
				// session-failed and not a refusal that a prompt survives.
				//
				// It does not claim to diagnose the server's
				// configuration, and it cannot: a far side that died
				// before the loader spoke reaches the same state. What it
				// names is the OUTCOME, which is the same either way — the
				// user has no prompt here.
				//
				// "The channel is already gone" is asked through
				// farSideEnded rather than of the feed alone: the far
				// side's end reaches this goroutine down two unordered
				// chains, and reading only the pump's reported the row
				// as ReasonUnknown whenever the watcher's chain won a
				// race the pump had not already finished.
				_, remoteSessionEnded := ch.WaitErr()
				if execAccepted && farSideEnded(feed, remoteSessionEnded) {
					bootReason = ReasonExecSubstituted
				}
				ch.setShellIntegrationReason(bootReason)
				ch.log.Warn("ssh: shell bootstrap did not integrate the session",
					"reason", bootReason)
				// THE HARD INVALIDATION of design §5.3's validity
				// interval, named where it does its work.
				//
				// The capability's validity opens at minting and is
				// closed hard by backend invalidation — a bootstrap
				// refusal or timeout among them — after which a frame
				// of that epoch is rejected. This is that close, and
				// it is what bounds the one exposure §6.1 admits it
				// cannot remove: a forged STAGE_READY that outruns an
				// honest refusal produces a bearer, and this is the
				// event that kills it. Nothing else on this path did
				// it before — the transport stayed live until the
				// session ended, so a refusal left a valid epoch
				// behind it for as long as the tab was open.
				//
				// It is also the ONLY thing that moves a refused
				// remote session out of `starting`: with no domain
				// ever established the kernel publishes nothing, so
				// without this the axis would wait for a fact that is
				// never coming (§7: `starting` can never be
				// permanent).
				//
				// Closing the handle is the whole invalidation: it
				// ends the domain through the kernel's TransportLost,
				// releases the tunnel lease and removes the remote
				// listener. It is idempotent, so the channel's own
				// Close still runs exactly once.
				lc.close()
			}
			gate.release()
			close(bootstrapDone)
		}()
	}

	// The watcher starts AFTER releasePoolRef is set (above), so a session
	// that ends during the assignment window cannot race the field.
	go func() {
		waitErr := session.Wait()
		// Record BEFORE Close: the exit monitor wakes on done (which Close
		// closes) and reads WaitErr to classify how the session ended — the
		// remote shell's own exit (authoritative, with a status, via nil or
		// *ExitError) versus a loss (nocx-ictcq). Close is idempotent
		// (closeOnce), so if the tab already called Close this is a no-op;
		// if not, it closes the session, drops the ref, and (for a
		// jump-backed conn) releases the bastion handle.
		ch.recordWait(waitErr)
		_ = ch.Close()
	}()

	return ch, nil
}

// ptySession is one session channel with a pty and its pipes: everything
// needed to carry an interactive shell.
type ptySession struct {
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// openSessionWithPTY opens a session channel on an EXISTING connection,
// requests a pty and (when asked) agent forwarding, and returns its pipes.
//
// It is a function rather than four inline statements because §6.4's fourth
// row needs it twice: a server that tears the channel down as it refuses the
// exec request leaves the CONNECTION intact, and a replacement channel on it
// reaches a prompt at the cost of a second session and no second
// authentication.
func (rc *RealClient) openSessionWithPTY(gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig) (ptySession, error) {
	session, err := gclient.NewSession()
	if err != nil {
		return ptySession{}, fmt.Errorf("new session: %w", err)
	}
	ptyReq := ptyReqMsg{
		Term:     "xterm-256color",
		Columns:  uint32(resolved.cols),
		Rows:     uint32(resolved.rows),
		Width:    uint32(resolved.xpixel),
		Height:   uint32(resolved.ypixel),
		Modelist: buildTerminalModes(),
	}
	if _, err = session.SendRequest("pty-req", true, gossh.Marshal(&ptyReq)); err != nil {
		_ = session.Close()
		return ptySession{}, fmt.Errorf("pty-req: %w", err)
	}
	if cfg.AgentForward {
		// Per-connection handler already registered in Connect (initAgentForward).
		// Per-session: request agent forwarding on this session so the remote
		// side can open auth-agent@openssh.com channels. agent.RequestAgentForwarding
		// uses wantReply=true, so a server refusal surfaces as an error.
		if reqErr := agent.RequestAgentForwarding(session); reqErr != nil {
			_ = session.Close()
			return ptySession{}, fmt.Errorf("agent-forward request: %w", reqErr)
		}
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return ptySession{}, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return ptySession{}, fmt.Errorf("stdout pipe: %w", err)
	}
	return ptySession{session: session, stdin: stdin, stdout: stdout}, nil
}

// recoverFromRefusedExec is §6.4's third and fourth rows, in code.
//
// The refusal is CONDITIONAL and the condition is observable at the moment it
// matters, so nothing here has to guess: a request refused with the channel
// intact leaves a pty already granted on it and a `shell` succeeds there; a
// request refused with the channel torn down does not, and the answer is a
// replacement session channel on the SAME connection. Neither opens a
// connection and neither costs a second authentication — the whole point of
// the row, and the difference between conventional(exec-refused) and
// session-failed(exec-refused).
//
// The recovery is attempted in that order rather than branched on the error
// value on purpose. The discriminator §6.4 names — (false, nil) against
// (false, io.EOF) — is the client's view of the REQUEST, and gossh's
// Session.Start folds the first into an error of its own while passing the
// second through; asking the channel whether it still works answers the same
// question at the seam an implementer actually holds, and it never reads an
// error's text.
func (rc *RealClient) recoverFromRefusedExec(gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig, refused *gossh.Session, startErr error) (ptySession, error) {
	rc.log.Warn("ssh: the server refused the exec request; recovering to a native prompt without a second authentication",
		"host", resolved.hostName, "error", startErr)

	// The channel may have survived the refusal with its pty. A Start whose
	// exec request was refused leaves the Session unstarted, so a Shell on
	// that same Session is a Shell on that same channel.
	stdin, inErr := refused.StdinPipe()
	stdout, outErr := refused.StdoutPipe()
	if inErr == nil && outErr == nil {
		if shellErr := refused.Shell(); shellErr == nil {
			rc.log.Info("ssh: the refused exec left the channel usable; the session is a native prompt on it",
				"host", resolved.hostName, "reason", string(ReasonExecRefused))
			return ptySession{session: refused, stdin: stdin, stdout: stdout}, nil
		}
	}

	// It did not. A replacement channel on the same connection is the whole
	// of the recovery — never a new connection, never a second
	// authentication.
	_ = refused.Close()
	replacement, err := rc.openSessionWithPTY(gclient, resolved, cfg)
	if err != nil {
		return ptySession{}, fmt.Errorf("shell start: %s: the exec request was refused and no replacement session channel could be opened on the same connection: %w",
			ReasonExecRefused, err)
	}
	if shellErr := replacement.session.Shell(); shellErr != nil {
		_ = replacement.session.Close()
		return ptySession{}, fmt.Errorf("shell start: %s: the exec request was refused and the replacement session channel reached no prompt: %w",
			ReasonExecRefused, shellErr)
	}
	rc.log.Info("ssh: the refused exec took the channel; a replacement session channel on the same connection reached a native prompt",
		"host", resolved.hostName, "reason", string(ReasonExecRefused))
	return replacement, nil
}

// isAuthError returns true if the error likely comes from a failed SSH authentication.
func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "ssh: handshake failed") ||
		strings.Contains(msg, "no common algorithms")
}
