package ssh

import (
	"context"
	"io"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

type Channel interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	// Done returns a channel closed when the remote shell exits or the
	// underlying connection is lost — the Disconnected signal from AD-7.
	Done() <-chan struct{}
}

// RemoteInstaller installs shell integration scripts on a remote host via
// SSH/SFTP and returns the start command for the shell. Defined here (not
// in shellintegration) to avoid a cyclic import.
type RemoteInstaller interface {
	EnsureInstalledRemote(ctx context.Context, sshClient *gossh.Client, remoteHome string) error
	GetRemoteHome(sshClient *gossh.Client) (string, error)
	RemoteStartCommand() string
}

type SSH interface {
	Connect(ctx context.Context, host string, opts ...ConnectOption) (Channel, error)
	Close() error
}

type ConnectOption func(*ConnectConfig)

type ConnectConfig struct {
	User            string
	Port            int
	KeyFile         string
	UseAgent        bool
	Cols            uint16
	Rows            uint16
	XPixel          uint16
	YPixel          uint16
	AuthMethods     []gossh.AuthMethod
	KeyExchanges    []string
	RemoteInstaller RemoteInstaller

	// AuthMode controls which auth buckets are tried (null=Auto with full
	// fallback-chain; a specific value restricts which buckets are attempted).
	// Mirrors Tabby's profile.options.auth enum.
	AuthMode string

	// JumpHost is the profile name or ID of the jump server to use.
	JumpHost string
	// JumpPort is the port of the jump server (0 means use default 22).
	JumpPort int
	// Jump host credentials — loaded from jump server's profile.
	JumpUser     string
	JumpKeyFile  string
	JumpAuthMode string

	// BoundHost/BoundPort carry the host a linked credential is bound to
	// (from profile.Credential), set by the resolver. internal/ssh enforces
	// them after resolveConfig against the *resolved* hostname and effective
	// port — never the alias the renderer chose. Binding on the alias is
	// unsound: ~/.ssh/config can map "Host myserver" to "HostName
	// evil.example.com", so a binding satisfiable by a name the attacker
	// chooses is not a binding (nocx-mon/PR11-T5). An empty BoundHost means
	// the credential is unbound and is REFUSED at connect time — "any host"
	// is exactly the credential-redirection hole. An unset BoundPort (0)
	// means "this host, any port": host is the load-bearing identity; making
	// port mandatory would break every existing host-only credential harder
	// than the hole it would close. Stated exception, not a silent gap.
	BoundHost string
	BoundPort int

	// JumpBoundHost/JumpBoundPort are the jump credential's binding, enforced
	// against the jump host's resolved name and effective port independently
	// of the target — a target-bound credential must not satisfy the jump
	// binding and vice versa.
	JumpBoundHost string
	JumpBoundPort int

	// JumpSecrets, when set, enables late-bind of the jump host's
	// password from the SecretStore. Separate from the target's Secrets
	// so each hop resolves independently.
	JumpSecrets  credential.SecretStore
	JumpSecretID credential.SecretID
	// JumpPassphraseSecretID is the opaque reference to the jump host's key
	// passphrase in the SecretStore.
	JumpPassphraseSecretID credential.SecretID

	// Secrets, when set, enables late-bind of stored passwords from the
	// SecretStore by SecretID. The store is the seam between the profile
	// manager (clear data) and the secret store — never call it directly
	// from frontend code.
	Secrets  credential.SecretStore
	SecretID credential.SecretID
	// PassphraseSecretID is the opaque reference to the stored key
	// passphrase in the SecretStore.
	PassphraseSecretID credential.SecretID
	
	// AuthorizationRevision is set by connection.Resolver after grant check (ADR-0013).
	// Used by poolKey to invalidate pooled connections when grants change or secrets rotate.
	AuthorizationRevision string
}

func WithUser(user string) ConnectOption {
	return func(c *ConnectConfig) { c.User = user }
}

func WithPort(port int) ConnectOption {
	return func(c *ConnectConfig) { c.Port = port }
}

// WithKeyFile sets an explicit private key path for authentication.
func WithKeyFile(path string) ConnectOption {
	return func(c *ConnectConfig) { c.KeyFile = path }
}

// WithAgent enables ssh-agent authentication (default when no key or password
// is specified).
func WithAgent() ConnectOption {
	return func(c *ConnectConfig) { c.UseAgent = true }
}

// WithPTYSize sets the initial PTY dimensions for the shell channel.
// Per AD-1/AD-7 the channel is created at this size, never spawned-then-resized.
func WithPTYSize(cols, rows, xpixel, ypixel uint16) ConnectOption {
	return func(c *ConnectConfig) {
		c.Cols = cols
		c.Rows = rows
		c.XPixel = xpixel
		c.YPixel = ypixel
	}
}

// WithAuthMethods injects explicit ssh.AuthMethod values, bypassing the
// default key-discovery logic. Used primarily in tests.
func WithAuthMethods(auths []gossh.AuthMethod) ConnectOption {
	return func(c *ConnectConfig) { c.AuthMethods = auths }
}

// WithRemoteInstaller injects a shell integration installer for the remote
// session. When set, openShell installs scripts via SFTP and starts the
// shell with the integration activated.
func WithRemoteInstaller(ri RemoteInstaller) ConnectOption {
	return func(c *ConnectConfig) { c.RemoteInstaller = ri }
}

// WithAuthMode sets the auth-method filter for the connection (null=Auto).
// A specific value ("password"/"publicKey"/"agent"/"keyboardInteractive")
// restricts which auth buckets are attempted in the fallback chain.
func WithAuthMode(mode string) ConnectOption {
	return func(c *ConnectConfig) { c.AuthMode = mode }
}

// WithJumpHost sets the jump host configuration for SSH connection.
// Password authentication for the jump host comes from JumpCredentials
// (late-bound via the credential store), never as plaintext.
func WithJumpHost(host string, port int, user, authMode string) ConnectOption {
	return func(c *ConnectConfig) {
		c.JumpHost = host
		c.JumpPort = port
		c.JumpUser = user
		c.JumpAuthMode = authMode
	}
}

// WithJumpCredentials injects a SecretStore for late-bind of the jump
// host's password by SecretID. Mirrors WithCredentials but for the jump hop.
func WithJumpCredentials(store credential.SecretStore, id credential.SecretID) ConnectOption {
	return func(c *ConnectConfig) {
		c.JumpSecrets = store
		c.JumpSecretID = id
	}
}

// WithCredentials injects a SecretStore for late-bind of stored
// passwords by SecretID. The store is the seam between the profile manager
// and the secret store.
func WithCredentials(store credential.SecretStore, id credential.SecretID) ConnectOption {
	return func(c *ConnectConfig) {
		c.Secrets = store
		c.SecretID = id
	}
}

type Stub struct {
	log log.Logger
}

func NewStub(logger log.Logger) *Stub {
	return &Stub{log: logger}
}

func (s *Stub) Connect(ctx context.Context, host string, opts ...ConnectOption) (Channel, error) {
	s.log.Info("ssh stub: Connect called (not implemented)", "host", host)
	return NewStubChannel(s.log), nil
}

func (s *Stub) Close() error {
	s.log.Debug("ssh stub: Close called")
	return nil
}

type StubChannel struct {
	log  log.Logger
	done chan struct{}
}

func NewStubChannel(logger log.Logger) *StubChannel {
	return &StubChannel{log: logger, done: make(chan struct{})}
}

func (c *StubChannel) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (c *StubChannel) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *StubChannel) Close() error {
	c.onceClose()
	return nil
}

func (c *StubChannel) onceClose() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func (c *StubChannel) Done() <-chan struct{} {
	return c.done
}

func (c *StubChannel) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	return nil
}

// WithBinding sets the credential binding for ADR-0013 grant enforcement.
// This is set by the resolver after grant check and passed to SSH layer.
func WithBinding(host string, port int) ConnectOption {
	return func(c *ConnectConfig) {
		c.BoundHost = host
		c.BoundPort = port
	}
}

// WithJumpBinding sets the jump credential binding for ADR-0013 grant enforcement.
func WithJumpBinding(host string, port int) ConnectOption {
	return func(c *ConnectConfig) {
		c.JumpBoundHost = host
		c.JumpBoundPort = port
	}
}
