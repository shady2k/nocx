package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// ProbeConfig performs a forced-fresh credential probe against a resolved
// profile configuration. It verifies the host key, authenticates with
// exactly one auth method (from buildAuthChain — no fallback chain), and
// closes immediately without launching a shell or running a command.
//
// cfg is the ConnectConfig from the profile resolver — the probe uses the
// same user, port, timeout and secret references that Connect would.
// The host parameter is the resolved dial target hostname.
//
// Errors are distinguishable via errors.As:
//   - *ErrAuthFailed for rejected credentials
//   - *ErrUnknownHostKey / *ErrHostKeyMismatch for host key issues
//   - *ErrEncryptedKey when a passphrase or keyboard-interactive user input is needed
//   - *net.OpError (or context deadline/exceeded) for unreachable targets
func (rc *RealClient) ProbeConfig(ctx context.Context, host string, cfg *ConnectConfig) error {
	_, err := rc.probeConfig(ctx, host, cfg)
	return err
}

// ProbeConfigWithResult is identical to ProbeConfig but also returns the
// host-key fingerprint observed during the handshake. The fingerprint is
// empty when the handshake fails before host key verification (e.g.
// unreachable host).
func (rc *RealClient) ProbeConfigWithResult(ctx context.Context, host string, cfg *ConnectConfig) (fingerprint string, err error) {
	return rc.probeConfig(ctx, host, cfg)
}

// probeConfig is the shared implementation for ProbeConfig and
// ProbeConfigWithResult.
func (rc *RealClient) probeConfig(ctx context.Context, host string, cfg *ConnectConfig) (fingerprint string, err error) {
	resolved, err := rc.resolveConfig(ctx, host, cfg)
	if err != nil {
		return "", fmt.Errorf("probe config: %w", err)
	}

	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		return "", fmt.Errorf("probe config: %w", err)
	}

	auth, err := firstAuthMethod(chain)
	if err != nil {
		// The connection names an auth method it has no material for. That is
		// a configuration answer, and the probe must give the same one the
		// connect path gives — a test that reports something different from
		// what connecting does is worse than no test.
		if errors.Is(err, errNoUsableAuth) {
			return "", &ErrNoAuthMethod{User: resolved.user, Host: resolved.hostName, Mode: cfg.AuthMode}
		}
		return "", fmt.Errorf("probe config: %w", err)
	}

	hostKeyCB, fp, err := rc.probeHostKeyCallback()
	if err != nil {
		return "", fmt.Errorf("probe config: %w", err)
	}

	addr := net.JoinHostPort(resolved.hostName, fmt.Sprintf("%d", resolved.port))
	timeout := cfg.ReadyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	gcfg := &gossh.ClientConfig{
		User:            resolved.user,
		Auth:            []gossh.AuthMethod{auth},
		HostKeyCallback: hostKeyCB,
		Timeout:         timeout,
	}

	d := &dialer{client: rc}
	gclient, err := d.dialDirect(ctx, addr, gcfg, host, resolved.user)
	if err != nil {
		return *fp, fmt.Errorf("probe config: %w", err)
	}
	_ = gclient.Close()
	return *fp, nil
}

// errNoUsableAuth marks a chain with nothing to send. Callers turn it into an
// ErrNoAuthMethod carrying the user and host, which they know and this does not.
//
// One sentinel, not two: errors.go carried an errNoAuthMethods that nothing in
// the package ever returned — a test asserted on it in a branch production
// could not reach, which is how a dead sentinel survives a review.
var errNoUsableAuth = errors.New("no usable auth methods found")

// firstAuthMethod extracts the first usable gossh.AuthMethod from the auth
// chain built by buildAuthChain. It skips entries that do not carry a method
// (kindNone, kindHostbased) and returns ErrEncryptedKey for entries that
// require user interaction at probe time (kindPromptPassword,
// kindKeyboardInteractive without a stored secret).
//
// For kindKeyboardInteractive with a stored secret, it constructs a
// keyboard-interactive challenge handler that responds with the stored
// password — distinct from a plain password method.
//
// Exactly one method is returned. The probe never sends a chain: a second
// method against one host is indistinguishable from password spraying and
// MaxAuthTries is finite.
func firstAuthMethod(chain []authChainEntry) (gossh.AuthMethod, error) {
	for _, entry := range chain {
		switch entry.kind {
		case kindPublicKey, kindAgent, kindSavedPassword:
			if entry.method != nil {
				return entry.method, nil
			}
		case kindKeyboardInteractive:
			if !entry.secret.IsEmpty() {
				// Build a keyboard-interactive challenge handler from the
				// stored password — not a plain Password method.
				return gossh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
					if len(questions) == 0 {
						return nil, nil
					}
					var pw string
					if err := entry.secret.Use(func(b []byte) error { pw = string(b); return nil }); err != nil {
						return nil, err
					}
					return []string{pw}, nil
				}), nil
			}
			// No stored secret for keyboard-interactive → needs user input.
			return nil, &ErrEncryptedKey{Path: "keyboard-interactive"}
		case kindPromptPassword:
			return nil, &ErrEncryptedKey{Path: "prompt-password"}
		}
		// kindNone, kindHostbased → skip to next entry.
	}
	return nil, errNoUsableAuth
}
