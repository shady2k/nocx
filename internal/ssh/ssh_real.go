package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// RealClient — a production SSH client implementing the SSH interface.
//
// Connection pool seam (nocx-9le.2 will add ref-counted pooling):
// RealClient currently holds a single *gossh.Client per Connect. When
// nocx-9le.2 lands, the pool will wrap RealClient behind the same SSH
// interface by owning a map[key]*poolEntry, each entry holding a
// *gossh.Client and a ref count. RealClient.Connect will become the
// inner dial+channel logic that the pool delegates to on cache-miss.
// ---------------------------------------------------------------------------

// RealClientOption configures the RealClient constructor.
type RealClientOption func(*RealClient)

// WithKnownHostsFile sets an explicit known_hosts path. Default is
// ~/.ssh/known_hosts.
func WithKnownHostsFile(path string) RealClientOption {
	return func(rc *RealClient) { rc.knownHostsFile = path }
}

// WithSSHConfigPath sets an explicit SSH config path. Default is
// ~/.ssh/config. For testing.
func WithSSHConfigPath(path string) RealClientOption {
	return func(rc *RealClient) { rc.sshConfigPath = path }
}

// RealClient is a production SSH client that connects to remote hosts
// via golang.org/x/crypto/ssh, honours ~/.ssh/config, authenticates with
// public keys (and the ssh-agent), and verifies host keys against
// known_hosts.
type RealClient struct {
	log            log.Logger
	knownHostsFile string
	sshConfigPath  string

	// mu guards the client map (for future connection pool integration).
	mu      sync.Mutex
	clients []*gossh.Client
}

// NewReal creates a RealClient with the given options. If known_hosts is
// not set it defaults to ~/.ssh/known_hosts; if the file does not exist
// every host is treated as unknown (ErrUnknownHostKey).
func NewReal(logger log.Logger, opts ...RealClientOption) (*RealClient, error) {
	rc := &RealClient{
		log: logger.With("module", "ssh"),
	}
	// Defaults.
	home, _ := os.UserHomeDir()
	rc.knownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
	rc.sshConfigPath = filepath.Join(home, ".ssh", "config")

	for _, o := range opts {
		o(rc)
	}
	return rc, nil
}

// Connect implements SSH.Connect.
func (rc *RealClient) Connect(ctx context.Context, host string, opts ...ConnectOption) (Channel, error) {
	// 1. Apply options to get a ConnectConfig.
	cfg := &ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}

	// 2. Resolve SSH config.
	resolved, err := rc.resolveConfig(host, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve config for %s: %w", host, err)
	}

	// 3. Build host key callback.
	hostKeyCB, err := rc.hostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("host key callback: %w", err)
	}

	// 4. Build auth methods.
	auths, err := rc.buildAuthMethods(resolved, cfg)
	if err != nil {
		return nil, err
	}

	// 5. Dial.
	addr := net.JoinHostPort(resolved.hostName, strconv.Itoa(resolved.port))
	gcfg := &gossh.ClientConfig{
		User:            resolved.user,
		Auth:            auths,
		HostKeyCallback: hostKeyCB,
		Timeout:         30 * time.Second,
	}

	// Support context cancellation during dial.
	type dialResult struct {
		client *gossh.Client
		err    error
	}
	dialCh := make(chan dialResult, 1)
	go func() {
		cl, dialErr := gossh.Dial("tcp", addr, gcfg)
		dialCh <- dialResult{cl, dialErr}
	}()

	var gclient *gossh.Client
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-dialCh:
		if res.err != nil {
			// Detect auth failures: x/crypto/ssh v0.51.0 does not export a
			// dedicated client-side auth error type, so we match on the
			// message pattern.
			if isAuthError(res.err) {
				return nil, &ErrAuthFailed{
					User: resolved.user,
					Host: host,
					Err:  res.err,
				}
			}
			return nil, res.err
		}
		gclient = res.client
	}

	rc.mu.Lock()
	rc.clients = append(rc.clients, gclient)
	rc.mu.Unlock()

	// 6. Open session, request PTY + shell.
	ch, err := rc.openShell(gclient, resolved)
	if err != nil {
		_ = gclient.Close()
		return nil, err
	}

	return ch, nil
}

// Close implements SSH.Close.
func (rc *RealClient) Close() error {
	rc.mu.Lock()
	clients := rc.clients
	rc.clients = nil
	rc.mu.Unlock()

	var lastErr error
	for _, cl := range clients {
		if err := cl.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

type resolvedConfig struct {
	hostName     string
	user         string
	port         int
	identityFile string
	keyAlgos     []string
	cols         uint16
	rows         uint16
	xpixel       uint16
	ypixel       uint16
}

// resolveConfig merges ~/.ssh/config values with explicit ConnectOptions.
// Precedence: explicit option > config file > default.
func (rc *RealClient) resolveConfig(host string, cfg *ConnectConfig) (*resolvedConfig, error) {
	// If the host already contains a port, extract it and use the host part.
	resolvedHost, resolvedPort := host, 22
	if h, p, err := net.SplitHostPort(host); err == nil {
		resolvedHost = h
		if port, err := strconv.Atoi(p); err == nil {
			resolvedPort = port
		}
	}

	resolved := &resolvedConfig{
		hostName: resolvedHost,
		user:     currentUser(),
		port:     resolvedPort,
		cols:     80,
		rows:     24,
	}

	// Try to parse ~/.ssh/config.
	sshCfg, err := rc.openSSHConfig()
	if err == nil && sshCfg != nil {
		if hn, _ := sshCfg.Get(host, "HostName"); hn != "" {
			resolved.hostName = hn
		}
		if u, _ := sshCfg.Get(host, "User"); u != "" {
			resolved.user = u
		}
		if p, _ := sshCfg.Get(host, "Port"); p != "" {
			if port, err := strconv.Atoi(p); err == nil {
				resolved.port = port
			}
		}
		if idf, _ := sshCfg.Get(host, "IdentityFile"); idf != "" {
			resolved.identityFile = expandPath(idf)
		}
	}

	// Explicit options override config.
	if cfg.User != "" {
		resolved.user = cfg.User
	}
	if cfg.Port > 0 {
		resolved.port = cfg.Port
	}
	if cfg.KeyFile != "" {
		resolved.identityFile = cfg.KeyFile
	}
	if cfg.Cols > 0 {
		resolved.cols = cfg.Cols
	}
	if cfg.Rows > 0 {
		resolved.rows = cfg.Rows
	}
	if cfg.XPixel > 0 {
		resolved.xpixel = cfg.XPixel
	}
	if cfg.YPixel > 0 {
		resolved.ypixel = cfg.YPixel
	}
	if len(cfg.KeyExchanges) > 0 {
		resolved.keyAlgos = cfg.KeyExchanges
	}

	return resolved, nil
}

func (rc *RealClient) openSSHConfig() (*ssh_config.Config, error) {
	f, err := os.Open(rc.sshConfigPath)
	if err != nil {
		// Config file doesn't exist — not an error, use defaults.
		return nil, nil
	}
	defer func() { _ = f.Close() }()
	return ssh_config.Decode(f)
}

// hostKeyCallback builds a HostKeyCallback from known_hosts.
// It distinguishes "host not found" (ErrUnknownHostKey) from
// "host key mismatch" (ErrHostKeyMismatch) so the UI can act.
func (rc *RealClient) hostKeyCallback() (gossh.HostKeyCallback, error) {
	cb, err := knownhosts.New(rc.knownHostsFile)
	if err != nil {
		// If the file doesn't exist or is unreadable, treat every host
		// as unknown (safe default for a brand-new install).
		return func(addr string, remote net.Addr, key gossh.PublicKey) error {
			return &ErrUnknownHostKey{
				Addr:        addr,
				KeyAlgo:     key.Type(),
				Fingerprint: gossh.FingerprintSHA256(key),
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
				// Host not in known_hosts.
				return &ErrUnknownHostKey{
					Addr:        addr,
					KeyAlgo:     key.Type(),
					Fingerprint: gossh.FingerprintSHA256(key),
				}
			}
			// Host found but key doesn't match.
			var expected []string
			for _, k := range keyErr.Want {
				expected = append(expected, gossh.FingerprintSHA256(k.Key))
			}
			return &ErrHostKeyMismatch{
				Addr:        addr,
				Fingerprint: gossh.FingerprintSHA256(key),
				Expected:    strings.Join(expected, ","),
			}
		}
		return err
	}, nil
}

// buildAuthMethods determines the auth methods to try, in order.
// Priority: explicit key > explicit password > ssh-agent > default key files.
func (rc *RealClient) buildAuthMethods(resolved *resolvedConfig, cfg *ConnectConfig) ([]gossh.AuthMethod, error) {
	// 0. Explicit auth methods override everything.
	if len(cfg.AuthMethods) > 0 {
		return cfg.AuthMethods, nil
	}

	var methods []gossh.AuthMethod

	// 1. If a specific key file was given, use it.
	if resolved.identityFile != "" {
		signer, err := rc.loadKey(resolved.identityFile)
		if err != nil {
			return nil, err
		}
		methods = append(methods, gossh.PublicKeys(signer))
		return methods, nil
	}

	// 2. If password was given, use it.
	if cfg.Password != "" {
		methods = append(methods, gossh.Password(cfg.Password))
		return methods, nil
	}

	// 3. Try the ssh-agent.
	if agentMethods := rc.agentMethods(); len(agentMethods) > 0 {
		methods = append(methods, agentMethods...)
	}

	// 4. Fall back to default key files.
	defaultKeys := []string{
		filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519"),
		filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa"),
		filepath.Join(os.Getenv("HOME"), ".ssh", "id_ecdsa"),
	}

	for _, path := range defaultKeys {
		signer, err := rc.loadKey(path)
		if err != nil {
			// Silently skip unreadable keys.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			// Skip encrypted keys with a log.
			var encKeyErr *ErrEncryptedKey
			if errors.As(err, &encKeyErr) {
				rc.log.Debug("skipping encrypted key", "path", path)
				continue
			}
			return nil, err
		}
		methods = append(methods, gossh.PublicKeys(signer))
	}

	if len(methods) == 0 {
		return nil, errNoAuthMethods
	}

	return methods, nil
}

func (rc *RealClient) loadKey(path string) (gossh.Signer, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from config or option
	if err != nil {
		return nil, err
	}

	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		var passErr *gossh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, &ErrEncryptedKey{Path: path}
		}
		return nil, fmt.Errorf("parse key %s: %w", path, err)
	}
	return signer, nil
}

func (rc *RealClient) agentMethods() []gossh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	// Use the agent via dialing the socket — gossh provides
	// NewSSHAgentClient for extended operations, but for auth
	// we can use gossh.PublicKeysCallback.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	_ = conn.Close()

	return []gossh.AuthMethod{
		gossh.PublicKeysCallback(func() ([]gossh.Signer, error) {
			conn, err := net.Dial("unix", sock)
			if err != nil {
				return nil, err
			}
			defer func() { _ = conn.Close() }()
			return agent.NewClient(conn).Signers()
		}),
	}
}

// openShell opens a session, requests a PTY at the requested size, and
// starts a shell. Returns a RealChannel wrapping the underlying channel.
func (rc *RealClient) openShell(gclient *gossh.Client, resolved *resolvedConfig) (*RealChannel, error) {
	session, err := gclient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}

	// Request PTY at the requested size — per AD-1/AD-7 the channel is
	// created at this size, never spawned-then-resized.
	ptyErr := session.RequestPty("xterm-256color", int(resolved.rows), int(resolved.cols),
		gossh.TerminalModes{
			gossh.ECHO:          1,
			gossh.TTY_OP_ISPEED: 14400,
			gossh.TTY_OP_OSPEED: 14400,
		})
	if ptyErr != nil {
		_ = session.Close()
		return nil, fmt.Errorf("request pty: %w", ptyErr)
	}

	// StdinPipe / StdoutPipe give us the raw channel for io.ReadWriteCloser.
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

	// Start the shell.
	if err := session.Shell(); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("shell: %w", err)
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

	// Pump goroutine: wait for session to exit, then close done.
	go func() {
		_ = session.Wait()
		ch.closeOnce.Do(func() {
			close(ch.done)
		})
	}()

	return ch, nil
}

// ---------------------------------------------------------------------------
// RealChannel — wraps an SSH session channel as an ssh.Channel.
// ---------------------------------------------------------------------------

// RealChannel implements the Channel interface over an SSH session.
type RealChannel struct {
	log     log.Logger
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	done    chan struct{}

	closeOnce sync.Once
	closeCb   func()
}

func (c *RealChannel) Read(p []byte) (int, error) {
	return c.stdout.Read(p)
}

func (c *RealChannel) Write(p []byte) (int, error) {
	return c.stdin.Write(p)
}

func (c *RealChannel) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
	})
	c.closeCb()
	return nil
}

func (c *RealChannel) Done() <-chan struct{} {
	return c.done
}

// Resize sends a window-change request to the remote end.
func (c *RealChannel) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	return c.session.WindowChange(int(rows), int(cols))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func currentUser() string {
	u := os.Getenv("USER")
	if u == "" {
		u = os.Getenv("LOGNAME")
	}
	if u == "" {
		u = "root"
	}
	return u
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// isAuthError returns true if the error likely comes from a failed SSH
// authentication. x/crypto/ssh v0.51.0 does not export a dedicated
// client-side auth error type, so we match on message patterns.
func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "ssh: handshake failed") ||
		strings.Contains(msg, "no common algorithms")
}
