package ssh

import (
	"context"
	"io"

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
	User             string
	Port             int
	KeyFile          string
	Password         string
	UseAgent         bool
	Cols             uint16
	Rows             uint16
	XPixel           uint16
	YPixel           uint16
	AuthMethods      []gossh.AuthMethod
	KeyExchanges     []string
	RemoteInstaller  RemoteInstaller
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

// WithPassword sets password authentication.
func WithPassword(password string) ConnectOption {
	return func(c *ConnectConfig) { c.Password = password }
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
