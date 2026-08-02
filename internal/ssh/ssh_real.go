package ssh

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // known_hosts hashes host names with HMAC-SHA1; the file format fixes the algorithm.
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// Connect implements SSH.Connect.
func (rc *RealClient) Connect(ctx context.Context, host string, opts ...ConnectOption) (Channel, error) {
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}

	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve config for %s: %w", host, err)
	}
	// Enforce computed authorization BEFORE any dial. Only a linked credential
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

	// Agent forwarding: register the per-connection channel handler once
	// (initAgentForward is guarded by agentForwardOnce), then request it
	// per-session inside openShell. Fail early if the user asked for
	// forwarding but no agent is reachable.
	if cfg.AgentForward {
		if !rc.agentAvailable() {
			rc.pool.Release(handle)
			return nil, fmt.Errorf("agent forwarding requested but no SSH agent available (SSH_AUTH_SOCK not set)")
		}
		if fwdErr := pconn.initAgentForward(gclient, os.Getenv("SSH_AUTH_SOCK")); fwdErr != nil {
			rc.pool.Release(handle)
			return nil, fmt.Errorf("agent-forward setup: %w", fwdErr)
		}
	}

	ch, err := rc.openShell(ctx, gclient, resolved, cfg)
	if err != nil {
		// Failed to open the shell — release our reference so the
		// connection can close if we were the only tab. Without this the
		// failed Connect path leaks a pooled ref (and a jump transport)
		// for the process life.
		rc.pool.Release(handle)
		return nil, err
	}

	// Wire the channel's close to release our pool reference. RealChannel.Close
	// runs closeCb exactly once (sync.Once), so the handle is released exactly
	// once even if the session errors and the tab then closes. Releasing the
	// handle drops the target refcount; when it hits zero the pooledSSHConn
	// closes the gossh.Client AND releases the jump handle, which closes the
	// bastion when its own refcount hits zero. One Close per channel, one
	// Release per handle, no leak.
	ch.releasePoolRef = func() { rc.pool.Release(handle) }
	return ch, nil
}

// Probe attempts SSH authentication with only the supplied auth method,
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

// hostKeyCallback builds a HostKeyCallback from known_hosts.
func (rc *RealClient) hostKeyCallback() (gossh.HostKeyCallback, error) {
	cb, err := knownhosts.New(rc.knownHostsFile)
	if err != nil {
		return func(addr string, remote net.Addr, key gossh.PublicKey) error {
			return &ErrUnknownHostKey{
				Addr:        addr,
				KeyAlgo:     key.Type(),
				Fingerprint: gossh.FingerprintSHA256(key),
				Key:         key.Marshal(),
			}
		}, nil
	}

	return func(addr string, remote net.Addr, key gossh.PublicKey) error {
		err := cb(addr, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) == 0 {
				return &ErrUnknownHostKey{
					Addr:        addr,
					KeyAlgo:     key.Type(),
					Fingerprint: gossh.FingerprintSHA256(key),
					Key:         key.Marshal(),
				}
			}
			var expected []string
			for _, k := range keyErr.Want {
				expected = append(expected, gossh.FingerprintSHA256(k.Key))
			}
			return &ErrHostKeyMismatch{
				Addr:        addr,
				KeyAlgo:     key.Type(),
				Fingerprint: gossh.FingerprintSHA256(key),
				Expected:    strings.Join(expected, ","),
				Key:         key.Marshal(),
			}
		}
		return err
	}, nil
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
func (rc *RealClient) probeHostKeyCallback() (gossh.HostKeyCallback, *string, error) {
	var captured string
	cb, err := knownhosts.New(rc.knownHostsFile)
	if err != nil {
		// known_hosts file missing or unreadable — treat every host as unknown.
		return func(addr string, remote net.Addr, key gossh.PublicKey) error {
			captured = gossh.FingerprintSHA256(key)
			return &ErrUnknownHostKey{
				Addr:        addr,
				KeyAlgo:     key.Type(),
				Fingerprint: captured,
				Key:         key.Marshal(),
			}
		}, &captured, nil
	}

	return func(addr string, remote net.Addr, key gossh.PublicKey) error {
		captured = gossh.FingerprintSHA256(key)
		err := cb(addr, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) == 0 {
				return &ErrUnknownHostKey{
					Addr:        addr,
					KeyAlgo:     key.Type(),
					Fingerprint: captured,
					Key:         key.Marshal(),
				}
			}
			var expected []string
			for _, k := range keyErr.Want {
				expected = append(expected, gossh.FingerprintSHA256(k.Key))
			}
			return &ErrHostKeyMismatch{
				Addr:        addr,
				KeyAlgo:     key.Type(),
				Fingerprint: captured,
				Expected:    strings.Join(expected, ","),
				Key:         key.Marshal(),
			}
		}
		return err
	}, &captured, nil
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
// key the user has just rejected would still pass. Every line naming this host
// with the SAME key algorithm is dropped first; lines for the host's other
// algorithms are left alone, since a changed ed25519 key says nothing about
// its RSA one. Hashed entries (|1|salt|hash, what OpenSSH writes with
// HashKnownHosts on) are matched by recomputing the HMAC, the same way
// ssh-keygen -R finds them.
//
// A file with nothing to replace is appended to and never rewritten. A rewrite
// goes through a temp file in the same directory and a rename, so an interrupt
// cannot leave a half-written known_hosts. A missing file is created 0600; an
// existing file keeps its own mode, which is the user's to choose.
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

	existing, readErr := os.ReadFile(rc.knownHostsFile)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("trust host key: read %s: %w", rc.knownHostsFile, readErr)
	}

	kept, replaced := dropHostKeyLines(string(existing), addr, key.Type())
	if !replaced {
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

	if err := rc.rewriteKnownHosts(kept + line); err != nil {
		return "", err
	}
	return gossh.FingerprintSHA256(key), nil
}

// rewriteKnownHosts replaces the file's contents atomically, keeping the mode
// the file already had — a temp file beside it and a rename, so an interrupt
// leaves either the old file or the new one and never a truncated one.
func (rc *RealClient) rewriteKnownHosts(content string) error {
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
	if renameErr := os.Rename(tmpName, rc.knownHostsFile); renameErr != nil {
		return fmt.Errorf("trust host key: rename onto %s: %w", rc.knownHostsFile, renameErr)
	}
	return nil
}

// dropHostKeyLines returns the file with every line that names addr under the
// given key algorithm removed, and whether anything was removed. Comments and
// blank lines survive untouched: this is the user's file.
func dropHostKeyLines(content, addr, keyAlgo string) (kept string, dropped bool) {
	var b strings.Builder
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") &&
			lineKeyAlgo(trimmed) == keyAlgo && lineNamesHost(trimmed, addr) {
			dropped = true
			continue
		}
		b.WriteString(line)
	}
	kept = b.String()
	if kept != "" && !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	return kept, dropped
}

// knownHostsFields strips a leading @cert-authority / @revoked marker and
// returns the line's fields: host patterns, key algorithm, key.
func knownHostsFields(line string) []string {
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	return fields
}

// lineKeyAlgo is the key algorithm a known_hosts line carries, or "".
func lineKeyAlgo(line string) string {
	fields := knownHostsFields(line)
	if len(fields) < 3 {
		return ""
	}
	return fields[1]
}

// lineNamesHost reports whether a known_hosts line's host field names addr,
// plain or hashed. Negations (!host) are treated as naming it — a line that
// mentions the host at all is one this operation must not silently leave
// contradicting the key it just wrote.
func lineNamesHost(line, addr string) bool {
	fields := knownHostsFields(line)
	if len(fields) < 3 {
		return false
	}
	normalized := knownhosts.Normalize(addr)
	for _, pattern := range strings.Split(fields[0], ",") {
		pattern = strings.TrimPrefix(pattern, "!")
		if strings.HasPrefix(pattern, "|1|") {
			if hashedPatternMatches(pattern, normalized) {
				return true
			}
			continue
		}
		if pattern == normalized {
			return true
		}
	}
	return false
}

// hashedPatternMatches answers the |1|salt|hash form OpenSSH writes when
// HashKnownHosts is on. The construction is fixed by the file format —
// HMAC-SHA1 over the normalized host with the line's own salt — so SHA-1 here
// is reading somebody else's format, not choosing a hash.
func hashedPatternMatches(pattern, normalizedHost string) bool {
	parts := strings.Split(pattern, "|")
	if len(parts) != 4 {
		return false
	}
	salt, saltErr := base64.StdEncoding.DecodeString(parts[2])
	if saltErr != nil {
		return false
	}
	want, hashErr := base64.StdEncoding.DecodeString(parts[3])
	if hashErr != nil {
		return false
	}
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(normalizedHost))
	return hmac.Equal(mac.Sum(nil), want)
}

// openShell opens a session, requests a PTY, optionally requests agent
// forwarding, and starts a shell.
func (rc *RealClient) openShell(ctx context.Context, gclient *gossh.Client, resolved *resolvedConfig, cfg *ConnectConfig) (*RealChannel, error) {
	if cfg.RemoteInstaller != nil {
		remoteHome, err := cfg.RemoteInstaller.GetRemoteHome(gclient)
		if err != nil {
			rc.log.Warn("ssh: could not determine remote home for shell integration",
				"host", resolved.hostName, "error", err)
		} else if err := cfg.RemoteInstaller.EnsureInstalledRemote(ctx, gclient, remoteHome); err != nil {
			rc.log.Warn("ssh: shell integration install failed",
				"host", resolved.hostName, "error", err)
		}
	}

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

	if cfg.RemoteInstaller != nil {
		if err := session.Start(cfg.RemoteInstaller.RemoteStartCommand()); err != nil {
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
		log:     rc.log.With("remote", resolved.hostName),
		session: session,
		stdin:   stdin,
		stdout:  stdout,
		done:    make(chan struct{}),
		closeCb: func() {
			_ = session.Close()
		},
	}

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
