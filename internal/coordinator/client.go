package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"
)

// The client half of the discovery socket: what a launcher uses to learn
// which daemon is running and how to reach its WebSocket.
//
// It lives beside the server rather than in the launcher's own package
// because it is the same protocol, and a protocol with two owners drifts
// (AGENTS.md, "look for the existing answer"). The request and response
// shapes in handshake.go are shared verbatim; nothing here restates them.

var (
	// ErrNoCoordinator reports that nothing is serving the discovery
	// socket — the path is absent, or a socket file survives a daemon that
	// died and connect(2) refuses it. It is the launcher's cue to spawn,
	// and it is deliberately distinguishable from every other failure: a
	// dial that failed for want of descriptors must not be read as "no
	// daemon" and answered by starting a second one.
	ErrNoCoordinator = errors.New("coordinator: no daemon is serving the discovery socket")

	// ErrRefused reports that the daemon answered, and answered no. The
	// reason it gave travels in the wrapped message.
	ErrRefused = errors.New("coordinator: the daemon refused the request")
)

// exchangeDeadline bounds one client-side request/response. The server
// applies its own 30s bound per exchange; this is the other half, so a
// daemon that accepted a connection and then stopped answering cannot hold
// a window's startup open indefinitely.
const exchangeDeadline = 10 * time.Second

// Dialer opens the discovery socket.
//
// An interface for the same reason [PeerCredentials] is one: the failure
// paths that matter here — a dial that fails for a reason which is NOT
// absence, a daemon that writes half an answer — cannot be arranged out of
// real sockets on a developer's machine, and they are exactly the paths a
// launcher must not confuse with "nothing is running" (AD-8).
type Dialer interface {
	// Dial connects to the unix socket at path.
	Dial(ctx context.Context, path string) (net.Conn, error)
}

// SystemDialer is the real answer: a unix connect, with absence reported as
// [ErrNoCoordinator] and everything else as itself.
type SystemDialer struct{}

// Dial connects to the discovery socket.
//
// ENOENT and ECONNREFUSED are the two spellings of "nobody is there":
// the first is a path that was never created or was unlinked at shutdown,
// the second is a socket file whose daemon died without unlinking it.
// Both are absence; neither is an error a person needs to read.
func (SystemDialer) Dial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err == nil {
		return conn, nil
	}
	if isAbsence(err) {
		return nil, fmt.Errorf("%w: %s", ErrNoCoordinator, path)
	}
	return nil, fmt.Errorf("coordinator: dial %s: %w", path, err)
}

func isAbsence(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOTDIR)
}

// Sighting is one exchange's worth of knowledge about the running daemon:
// what it said, and which process said it.
//
// The pid is not part of the wire shape and deliberately never will be: a
// daemon that STATED its pid could state anybody's. The kernel stamps this
// one on the socket at connect(2) time, which is the same reason the
// server's uid check is trustworthy (peer.go). It is 0 where the platform
// cannot answer, and a stopper that is handed a 0 refuses rather than
// signalling something arbitrary.
type Sighting struct {
	Hello Hello
	PID   int
}

// Discoverer asks the discovery socket who is serving it. [Client] is the
// real implementation; the launcher takes the interface so that its own
// decisions — spawn, wait, refuse, replace — are testable without a daemon.
type Discoverer interface {
	Hello(ctx context.Context) (Sighting, error)
}

// ClientConfig is everything the client needs, from the composition root.
type ClientConfig struct {
	// Socket is the path to the discovery socket — [SocketPathIn].
	Socket string
	// Self is this build's identity, stated on every hello.
	Self ClientIdentity
	// Dialer opens the socket.
	Dialer Dialer
	// Logger receives the exchange record. It never receives the token.
	Logger *slog.Logger
}

// Client is the launcher's end of the discovery socket.
type Client struct {
	cfg ClientConfig
}

// NewClient validates the configuration. It touches no filesystem: a client
// is constructed before anybody knows whether a daemon exists.
func NewClient(cfg ClientConfig) (*Client, error) {
	switch {
	case cfg.Socket == "":
		return nil, errors.New("coordinator: no socket path")
	case cfg.Dialer == nil:
		return nil, errors.New("coordinator: no dialer")
	case cfg.Logger == nil:
		return nil, errors.New("coordinator: no logger")
	}
	return &Client{cfg: cfg}, nil
}

// Hello performs one exchange: state who we are, read who they are.
//
// One connection per call rather than a kept one. The launcher's questions
// are minutes apart at most and there is exactly one of them per start, so
// a pooled connection would buy nothing and cost a reconnect path nobody
// exercises.
func (c *Client) Hello(ctx context.Context) (Sighting, error) {
	conn, err := c.cfg.Dialer.Dial(ctx, c.cfg.Socket)
	if err != nil {
		return Sighting{}, err
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			c.cfg.Logger.Debug("coordinator: closing the discovery connection", "error", cerr)
		}
	}()

	deadline := time.Now().Add(exchangeDeadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if dlErr := conn.SetDeadline(deadline); dlErr != nil {
		return Sighting{}, fmt.Errorf("coordinator: set deadline on %s: %w", c.cfg.Socket, dlErr)
	}

	// The pid is read BEFORE the exchange: a daemon that hangs up as soon
	// as it has answered would otherwise leave us with a hello and no way
	// to name the process that gave it.
	pid := peerPIDOf(conn)

	req := Request{Type: RequestHello, Client: &c.cfg.Self}
	line, err := json.Marshal(req)
	if err != nil {
		return Sighting{}, fmt.Errorf("coordinator: encoding the hello: %w", err)
	}
	if _, wErr := conn.Write(append(line, '\n')); wErr != nil {
		return Sighting{}, fmt.Errorf("coordinator: writing the hello to %s: %w", c.cfg.Socket, wErr)
	}

	answer, err := bufio.NewReader(conn).ReadBytes('\n')
	if len(answer) == 0 {
		if err == nil {
			err = errors.New("empty answer")
		}
		return Sighting{}, fmt.Errorf("coordinator: reading the answer from %s: %w", c.cfg.Socket, err)
	}
	var resp Response
	if err := json.Unmarshal(answer, &resp); err != nil {
		// The bytes are NOT logged: a daemon that got its framing wrong
		// may have put half a token in them.
		return Sighting{}, fmt.Errorf("coordinator: the answer from %s is not newline-delimited JSON: %w",
			c.cfg.Socket, err)
	}
	if resp.Error != "" {
		return Sighting{}, fmt.Errorf("%w: %s", ErrRefused, resp.Error)
	}
	if resp.Hello == nil {
		// Neither a payload nor a reason. Reading this as an empty hello
		// would hand the renderer an empty address and an empty token,
		// which fails later and somewhere else.
		return Sighting{}, fmt.Errorf("coordinator: the answer from %s carried neither a hello nor a reason",
			c.cfg.Socket)
	}
	c.cfg.Logger.Debug("coordinator: found a daemon",
		"socket", c.cfg.Socket,
		"pid", pid,
		"version", resp.Hello.Build.Version,
		"commit", resp.Hello.Build.Commit,
		"protocol", resp.Hello.Protocol,
		"wsAddress", resp.Hello.WSAddress,
	)
	return Sighting{Hello: *resp.Hello, PID: pid}, nil
}

// peerPIDOf reports the pid the kernel stamped on conn at connect time, or
// 0 when the connection is not a unix socket (a test double) or the
// platform cannot say.
//
// A best-effort read on purpose: a launcher that could not learn the pid
// still starts normally against a compatible daemon, and only the refusal
// path — which needs something to stop — is affected. That path says so
// itself rather than being silently skipped here.
func peerPIDOf(conn net.Conn) int {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0
	}
	var pid int
	var opErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		pid, opErr = peerPID(fd)
	}); ctrlErr != nil || opErr != nil {
		return 0
	}
	return pid
}
