package ssh

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// dialer encapsulates SSH connection logic. It builds the per-target
// gossh.ClientConfig and performs the real network dial (direct or via a
// jump host). The pool owns the resulting connection; dialer only produces it.
type dialer struct {
	client *RealClient
}

// poolKeyFor builds the pool key for a Connect. The key carries the resolved
// host/port/user, the credential identity, and the resolved jump route (see
// poolKey). Two Connects that resolve to the same key share one connection.
//
// identity: a stored credential is the principal (its SecretID isolates it);
// an inline private key is the principal (keyed by its public-key fingerprint,
// not its file path).
// Agent-only or prompt-password auth leaves identity empty by explicit design:
// the agent authenticates each channel independently (every shell/session request
// goes through the agent socket), so sharing the transport does not share the
// authentication — each tab still proves its identity through the agent for its
// own channel. If the agent's loaded keys change (e.g. ssh-add -D / ssh-add),
// new transports dialed after the change use the updated key set, but existing
// transports are already authenticated and remain valid. Pooling by host+user+port
// for agent auth means two tabs sharing one transport is correct: there is no
// credential principal to isolate, and the agent's channel-level authentication
// independently gates each session. Connect to a new agent socket epoch (e.g.
// SSH_AUTH_SOCK pointing at a different agent) would require a different host
// (different socket path), so the pool key already separates them.
// The identity string is what the binding check already keys on, so widening or
// narrowing it here is exactly widening/narrowing the authorization boundary.
func (rc *RealClient) poolKeyFor(ctx context.Context, resolved *resolvedConfig, cfg *ConnectConfig) poolKey {
	// identity: a stored credential is the principal (its SecretID isolates
	// it). An inline private key is the principal — keyed by its public-key
	// fingerprint (SHA256 of the public key), NOT its file path, so replacing
	// the file contents at the same path changes the pool identity. Agent-only
	// or prompt-password auth has no credential principal, so identity is empty
	// and such connections share — there is no second principal to isolate.
	identity := string(cfg.SecretID)
	if identity == "" {
		keyPath := cfg.KeyFile
		if keyPath == "" {
			keyPath = resolved.identityFile
		}
		if keyPath != "" {
			identity = publicKeyFingerprint(keyPath)
		}
	}

	jumpRoute := ""
	if cfg.JumpHost != "" {
		jumpRoute = rc.jumpRouteKey(ctx, cfg)
	}

	return poolKey{
		host:      resolved.hostName,
		port:      resolved.port,
		user:      resolved.user,
		identity:  identity,
		jumpRoute: jumpRoute,
	}
}

// publicKeyFingerprint computes the SHA256 fingerprint of the public key
// from a private key file. For encrypted keys, falls back to a SHA256 of
// the file content so the identity still changes when the file is replaced.
// Returns empty string on I/O error (caller retains the empty identity,
// which means agent/prompt sharing by host+user+port).
func publicKeyFingerprint(keyPath string) string {
	// #nosec G304 -- keyPath comes from the profile or ~/.ssh/config IdentityFile,
	// the same file the SSH client is about to read to authenticate. Refusing to
	// hash it here would not stop it being used, only stop the pool telling two
	// keys apart.
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return ""
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err == nil {
		return gossh.FingerprintSHA256(signer.PublicKey())
	}
	// Encrypted or otherwise unparseable — use content hash so file rotation
	// still changes the pool key. Prefix with "content-" to distinguish from
	// real SSH fingerprints.
	h := sha256.Sum256(data)
	return fmt.Sprintf("SHA256:content-%x", h[:8])
}

// jumpRouteKey renders the jump host's resolved identity into the route
// component of the target's pool key. The bastion is pooled under its own
// poolKey (see dialPool's jump path); this string keeps a target-via-bastion-A
// entry separate from the same target via bastion-B, and from the same target
// dialed directly.
//
// For multi-hop routes (cfg.JumpConfig != nil), the function recursively
// walks the JumpConfig chain, so a target reached through bastion A then
// bastion B produces a different route key than the same target through
// bastion A alone, and from the same target through bastion B then C.
func (rc *RealClient) jumpRouteKey(ctx context.Context, cfg *ConnectConfig) string {
	// Prefer JumpConfig (multi-hop) over flat fields.
	jumpCfg := cfg.JumpConfig
	if jumpCfg == nil {
		jumpCfg = &ConnectConfig{
			User:               cfg.JumpUser,
			Port:               cfg.JumpPort,
			KeyFile:            cfg.JumpKeyFile,
			AuthMode:           cfg.JumpAuthMode,
			Secrets:            cfg.JumpSecrets,
			SecretID:           cfg.JumpSecretID,
			PassphraseSecretID: cfg.JumpPassphraseSecretID,
		}
	}

	jumpResolved, err := rc.resolveConfig(ctx, cfg.JumpHost, jumpCfg)
	if err != nil {
		return "unresolved:" + cfg.JumpHost
	}
	secretID := string(cfg.JumpSecretID)
	if secretID == "" {
		secretID = string(jumpCfg.SecretID)
	}
	jumpKey := poolKey{
		host:     jumpResolved.hostName,
		port:     jumpResolved.port,
		user:     jumpResolved.user,
		identity: secretID,
	}
	if jumpKey.identity == "" {
		keyFile := cfg.JumpKeyFile
		if keyFile == "" {
			keyFile = jumpCfg.KeyFile
		}
		if keyFile != "" {
			jumpKey.identity = keyFile
		} else if jumpResolved.identityFile != "" {
			jumpKey.identity = jumpResolved.identityFile
		}
	}

	base := jumpKey.jumpRouteKey()

	// Recursively append the next hop in a multi-hop chain.
	if jumpCfg.JumpConfig != nil || jumpCfg.JumpHost != "" {
		next := rc.jumpRouteKey(ctx, jumpCfg)
		if next != "" {
			base += ">" + next
		}
	}

	return base
}

// dialForConnect is the per-Connect dial factory passed to the pool. It
// builds the gossh.ClientConfig from the caller's resolved config and auth
// chain (so stored-credential late-bind and inline key paths resolve
// exactly as a non-pooled Connect would), dials the target — directly or
// via a jump host acquired from this same pool — and returns a
// *pooledSSHConn wrapping the gossh.Client. The pool stores it under the
// key Connect computed; later Connects with the same key reuse it without
// re-dialing. The factory is invoked only on a cache miss.
func (rc *RealClient) dialForConnect(ctx context.Context, host string, resolved *resolvedConfig, cfg *ConnectConfig) func(poolKey) (sshClientConn, error) {
	return func(_ poolKey) (sshClientConn, error) {
		hostKeyCB, err := rc.hostKeyCallback()
		if err != nil {
			return nil, fmt.Errorf("host key callback: %w", err)
		}

		chain, err := rc.buildAuthChain(ctx, resolved, cfg)
		if err != nil {
			return nil, err
		}
		auths := authMethodsFromChain(chain)
		// Nothing to offer. The chain always carries `none`, which carries no
		// method, so an empty list means every real method fell out: the mode
		// names something the connection has no material for. Say that instead
		// of dialing and letting the handshake report it as
		// "attempted methods [none], no supported methods remain" — the
		// server is not the problem and the user should not be sent to look
		// at it. `none` is not attempted on its own: a server that accepts it
		// accepts it as the opening of a real chain, and offering it alone
		// is how this failure disguised itself as an auth rejection.
		if len(auths) == 0 {
			return nil, &ErrNoAuthMethod{User: resolved.user, Host: resolved.hostName, Mode: cfg.AuthMode}
		}

		addr := net.JoinHostPort(resolved.hostName, strconv.Itoa(resolved.port))
		timeout := cfg.ReadyTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		gcfg := &gossh.ClientConfig{
			User:            resolved.user,
			Auth:            auths,
			HostKeyCallback: hostKeyCB,
			Timeout:         timeout,
		}

		d := &dialer{client: rc}
		if cfg.JumpHost != "" || cfg.JumpConfig != nil {
			return d.dialViaJumpHost(ctx, cfg, resolved, gcfg, addr)
		}
		gclient, err := d.dialDirect(ctx, addr, gcfg, host, resolved.user)
		if err != nil {
			return nil, err
		}
		stopKA, _ := startKeepalive(gclient, cfg.KeepaliveInterval, cfg.KeepaliveCountMax)
		return &pooledSSHConn{
			client:        gclient,
			stopKeepalive: stopKA,
		}, nil
	}
}

// dialJumpForConnect is the per-Connect dial factory for the bastion hop,
// passed to the pool when acquiring the jump connection. It dials the
// bastion directly (a bastion is not itself jumped) and returns a bare
// *pooledSSHConn (no further release hook — the bastion's lifetime is
// governed by its own pool entry's refcount).
func (rc *RealClient) dialJumpForConnect(ctx context.Context, host string, resolved *resolvedConfig, cfg *ConnectConfig) func(poolKey) (sshClientConn, error) {
	return func(_ poolKey) (sshClientConn, error) {
		hostKeyCB, err := rc.hostKeyCallback()
		if err != nil {
			return nil, fmt.Errorf("jump host key callback: %w", err)
		}
		chain, err := rc.buildAuthChain(ctx, resolved, cfg)
		if err != nil {
			return nil, fmt.Errorf("build jump host auth: %w", err)
		}
		jumpAuths := authMethodsFromChain(chain)
		timeout := cfg.ReadyTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		jumpClientCfg := &gossh.ClientConfig{
			User:            resolved.user,
			Auth:            jumpAuths,
			HostKeyCallback: hostKeyCB,
			Timeout:         timeout,
		}
		d := &dialer{client: rc}
		jumpAddr := net.JoinHostPort(resolved.hostName, strconv.Itoa(resolved.port))

		// Multi-hop: if this jump host itself has a jump, use dialViaJumpHost
		// to acquire the next hop through the pool, then extract the *gossh.Client
		// from the resulting pooledSSHConn for onward proxying. The release chain
		// ensures the next hop's handle is released when this connection closes.
		var gclient *gossh.Client
		if cfg.JumpConfig != nil {
			pconn, viaErr := d.dialViaJumpHost(ctx, cfg, resolved, jumpClientCfg, jumpAddr)
			if viaErr != nil {
				return nil, viaErr
			}
			psconn, ok := pconn.(*pooledSSHConn)
			if !ok {
				_ = pconn.Close()
				return nil, fmt.Errorf("internal: multi-hop jump returned %T, want *pooledSSHConn", pconn)
			}
			gclient, ok = psconn.client.(*gossh.Client)
			if !ok {
				_ = pconn.Close()
				return nil, fmt.Errorf("internal: multi-hop jump client is %T, want *gossh.Client", psconn.client)
			}
			// Return a pooledSSHConn that chains close through the via-hop's
			// pooled connection, so the next hop's handle is released when
			// this connection is released from the pool.
			return &pooledSSHConn{
				client:  gclient,
				release: func() { _ = pconn.Close() },
			}, nil
		}

		gclient, err = d.dialDirect(ctx, jumpAddr, jumpClientCfg, host, resolved.user)
		if err != nil {
			return nil, err
		}
		return &pooledSSHConn{client: gclient}, nil
	}
}

// dialDirect establishes a direct SSH connection with context support.
//
// The TCP dial uses net.Dialer.DialContext, which respects ctx cancellation.
// gossh.NewClientConn has no context-aware form (v0.54.0), so it runs in a
// goroutine with a watchdog on ctx.Done(): closing the underlying net.Conn
// unblocks the handshake. The goroutine is drain-safe because the buffered
// channel (size 1) ensures the send always succeeds. The caller sees
// ctx.Err(), not an incidental "use of closed network connection" error.
func (d *dialer) dialDirect(ctx context.Context, addr string, cfg *gossh.ClientConfig, host, user string) (*gossh.Client, error) {
	d.client.log.Info("Dialing directly", "addr", addr, "user", cfg.User)

	netConn, err := d.dialWithCtx(ctx, "tcp", addr, cfg.Timeout)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	type hsResult struct {
		conn  gossh.Conn
		chans <-chan gossh.NewChannel
		reqs  <-chan *gossh.Request
		err   error
	}
	ch := make(chan hsResult, 1)
	go func() {
		conn, chans, reqs, err := gossh.NewClientConn(netConn, addr, cfg)
		ch <- hsResult{conn, chans, reqs, err}
	}()

	select {
	case <-ctx.Done():
		_ = netConn.Close() // unblocks NewClientConn
		<-ch                // drain goroutine
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			_ = netConn.Close()
			if isAuthError(r.err) {
				return nil, &ErrAuthFailed{User: user, Host: host, Err: r.err}
			}
			return nil, r.err
		}
		return gossh.NewClient(r.conn, r.chans, r.reqs), nil
	}
}

// dialWithCtx dials a network address with context cancellation support.
func (d *dialer) dialWithCtx(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	netDialer := &net.Dialer{Timeout: timeout}
	return netDialer.DialContext(ctx, network, addr)
}

// dialViaJumpHost connects to the target host through a jump server. The
// jump client is ACQUIRED FROM THE SAME POOL as the target (AD-4): the bastion
// is itself a ref-counted connection, so two tabs jumping through one bastion
// share its transport, and the bastion closes when the last target through it
// closes. The returned *pooledSSHConn's Close releases the bastion handle.
//
// Jump-target dialing uses gossh.Client.DialContext (available since
// golang.org/x/crypto v0.54.0), which respects ctx cancellation. The
// subsequent gossh.NewClientConn handshake has no context-aware form;
// a watchdog goroutine closes the dialed connection on ctx.Done() so
// the handshake fails promptly.
func (d *dialer) dialViaJumpHost(ctx context.Context, cfg *ConnectConfig, resolved *resolvedConfig, targetCfg *gossh.ClientConfig, targetAddr string) (sshClientConn, error) {
	d.client.log.Info("Connecting via jump host", "jump", cfg.JumpHost, "target", targetAddr)

	jumpHandle, jumpClient, err := d.acquireJumpHost(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// DialContext respects ctx cancellation — no watchdog needed for this step.
	conn, err := jumpClient.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		d.client.pool.Release(jumpHandle)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("dial target %s through jump: %w", targetAddr, err)
	}

	// gossh.NewClientConn has no context-aware form. Same watchdog pattern
	// as dialDirect: close conn on ctx.Done() to unblock the handshake.
	type hsResult struct {
		clientConn gossh.Conn
		chans      <-chan gossh.NewChannel
		reqs       <-chan *gossh.Request
		err        error
	}
	ch := make(chan hsResult, 1)
	go func() {
		cc, chans, reqs, err := gossh.NewClientConn(conn, targetAddr, targetCfg)
		ch <- hsResult{cc, chans, reqs, err}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close() // unblocks NewClientConn
		<-ch             // drain goroutine
		d.client.pool.Release(jumpHandle)
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			_ = conn.Close()
			d.client.pool.Release(jumpHandle)
			return nil, fmt.Errorf("ssh client conn through jump: %w", r.err)
		}
		target := gossh.NewClient(r.clientConn, r.chans, r.reqs)
		// The target's Close closes the gossh.Client AND releases the bastion
		// handle. When the last target through this bastion closes, the bastion's
		// refcount drops to zero and the bastion connection closes. The bastion
		// handle is released exactly once because pooledSSHConn.Close is guarded
		// by its own sync.Once.
		stop, _ := startKeepalive(target, cfg.KeepaliveInterval, cfg.KeepaliveCountMax)
		return &pooledSSHConn{
			client:        target,
			release:       func() { d.client.pool.Release(jumpHandle) },
			stopKeepalive: stop,
		}, nil
	}
}

// acquireJumpHost resolves the jump host's config, enforces the jump
// credential's binding, and Acquires the bastion from the pool — so the
// bastion is shared across tabs and released with the last target. Returns
// the pool handle (to release when the target closes) and the gossh.Client
// (to dial the target through).
//
// When cfg.JumpConfig is set (multi-hop), it carries the full recursive
// jump configuration including the bastion's own JumpConfig for the next
// hop. The bastion's own dial factory (dialJumpForConnect) checks for
// nested JumpConfig and dials through the next hop when present.
func (d *dialer) acquireJumpHost(ctx context.Context, cfg *ConnectConfig) (*poolHandle, *gossh.Client, error) {
	// Prefer JumpConfig (set by the resolver for multi-hop) over flat fields.
	jumpCfg := cfg.JumpConfig
	if jumpCfg == nil {
		jumpCfg = &ConnectConfig{
			User:               cfg.JumpUser,
			Port:               cfg.JumpPort,
			KeyFile:            cfg.JumpKeyFile,
			AuthMode:           cfg.JumpAuthMode,
			JumpHost:           "",
			Secrets:            cfg.JumpSecrets,
			SecretID:           cfg.JumpSecretID,
			PassphraseSecretID: cfg.JumpPassphraseSecretID,
		}
	}

	jumpResolved, err := d.client.resolveConfig(ctx, cfg.JumpHost, jumpCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve jump host config: %w", err)
	}
	// Enforce the jump credential's authorization against the jump host's
	// resolved name/effective port, independently of the target.
	secrets := jumpCfg.Secrets
	if secrets == nil && cfg.JumpSecrets != nil {
		secrets = cfg.JumpSecrets
	}
	secretID := jumpCfg.SecretID
	if secretID == "" && cfg.JumpSecretID != "" {
		secretID = cfg.JumpSecretID
	}
	if secrets != nil {
		resolvedJumpAuthz := d.client.resolveAuthzEndpoint(ctx, cfg.JumpAuthorizedEndpoint)
		if authErr := checkAuthorization(resolvedJumpAuthz, jumpResolved, string(secretID), true); authErr != nil {
			return nil, nil, authErr
		}
	}

	jumpKey := d.client.poolKeyFor(ctx, jumpResolved, jumpCfg)
	handle, err := d.client.pool.AcquireDial(ctx, jumpKey, d.client.dialJumpForConnect(ctx, cfg.JumpHost, jumpResolved, jumpCfg))
	if err != nil {
		return nil, nil, err
	}
	pconn, ok := handle.conn.(*pooledSSHConn)
	if !ok {
		d.client.pool.Release(handle)
		return nil, nil, fmt.Errorf("internal: jump pool entry is not *pooledSSHConn (%T)", handle.conn)
	}
	jumpClient, ok := pconn.client.(*gossh.Client)
	if !ok {
		d.client.pool.Release(handle)
		return nil, nil, fmt.Errorf("internal: jump client is not *gossh.Client (%T)", pconn.client)
	}
	return handle, jumpClient, nil
}
