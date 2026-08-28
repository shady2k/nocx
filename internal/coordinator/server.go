package coordinator

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

// The names inside the runtime directory. Short on purpose: sun_path is 104
// bytes on darwin and 108 on Linux, and the profile directory is already
// most of the budget on a Mac (/Users/<name>/Library/Application Support/…).
const (
	runtimeDirName = "run"
	socketName     = "srv.sock"
	lockName       = "srv.lock"

	// maxSocketPath is the smaller of the two platforms' sun_path limits,
	// less one for the terminating NUL. Checked before the bind so an
	// overlong path is refused by name instead of arriving as a bare
	// "invalid argument" from the kernel.
	maxSocketPath = 103

	// exchangeTimeout bounds one request/response exchange. A client that
	// connects and then says nothing must not hold a goroutine and a
	// descriptor for the life of a daemon that is measured in days.
	exchangeTimeout = 30 * time.Second
)

// The refusals, as sentinels rather than strings, because every one of them
// is a decision a caller has to be able to tell apart: a launcher does
// something different for "somebody else is already serving" than for
// "somebody has tampered with the path".
var (
	// ErrAlreadyRunning reports that another nocx-server holds this app
	// directory. It is the answer to the race two windows starting together
	// would otherwise win twice.
	ErrAlreadyRunning = errors.New("coordinator: another nocx-server already holds this app directory")

	// ErrSymlinkPath reports that the socket path is a symbolic link.
	// Binding through it would write wherever it points, so it is refused
	// and the target is left alone.
	ErrSymlinkPath = errors.New("coordinator: socket path is a symlink")

	// ErrOccupiedPath reports that something that is not a socket already
	// sits at the socket path.
	ErrOccupiedPath = errors.New("coordinator: socket path is occupied by something that is not a socket")

	// ErrForeignOwner reports that the runtime directory belongs to another
	// user, so nothing inside it can be trusted to be ours.
	ErrForeignOwner = errors.New("coordinator: runtime directory is not owned by this user")

	// ErrPathTooLong reports that the socket path does not fit in sun_path.
	ErrPathTooLong = errors.New("coordinator: socket path exceeds the platform's unix-socket limit")

	// ErrNotStarted reports an accessor used before Start.
	ErrNotStarted = errors.New("coordinator: server has not started")
)

// RuntimeDir returns the directory the discovery socket and its lock live
// in, for the profile THIS build owns.
//
// It derives from [storage.Paths] rather than naming a directory of its
// own, so `nocx` versus `nocx-dev` isolation by build tag and the test
// isolation seam both come free and there is no second way to find the app
// directory (design §4).
func RuntimeDir(p storage.Paths) string {
	return filepath.Join(p.DataDir(), runtimeDirName)
}

// Config is everything the server needs, supplied by the composition root.
// Nothing defaults to a real implementation here: a nil field is a
// configuration error, so no test can accidentally reach the kernel and no
// binary can accidentally reach a double (AD-8).
type Config struct {
	// Dir is the runtime directory — see [RuntimeDir]. Must be absolute.
	Dir string
	// Build identifies the binary answering the hello.
	Build Build
	// Backend is the running WS server this socket points clients at.
	Backend Backend
	// Peers answers who is on the other end of a connection.
	Peers PeerCredentials
	// Owner answers who owns the runtime directory.
	Owner PathOwner
	// SelfUID is the uid both answers are compared against.
	SelfUID uint32
	// Logger receives the lifecycle record. It never receives the token.
	Logger *slog.Logger
}

// Server owns the discovery socket for one daemon.
type Server struct {
	cfg    Config
	socket string
	lock   string

	mu       sync.Mutex
	listener *net.UnixListener
	flock    *fileLock
	closed   bool

	conns sync.WaitGroup
}

// NewServer validates the configuration and computes the paths. It touches
// no filesystem: everything that can fail against the disk fails in Start,
// where a caller is already prepared for it.
func NewServer(cfg Config) (*Server, error) {
	switch {
	case cfg.Dir == "":
		return nil, errors.New("coordinator: no runtime directory")
	case !filepath.IsAbs(cfg.Dir):
		return nil, fmt.Errorf("coordinator: runtime directory %q is not absolute", cfg.Dir)
	case cfg.Backend == nil:
		return nil, errors.New("coordinator: no backend")
	case cfg.Peers == nil:
		return nil, errors.New("coordinator: no peer credentials")
	case cfg.Owner == nil:
		return nil, errors.New("coordinator: no path owner")
	case cfg.Logger == nil:
		return nil, errors.New("coordinator: no logger")
	}
	return &Server{
		cfg:    cfg,
		socket: filepath.Join(cfg.Dir, socketName),
		lock:   filepath.Join(cfg.Dir, lockName),
	}, nil
}

// SocketPath is where the discovery socket is, or will be. It is valid
// before Start so that a launcher can look for it without asking a daemon
// that may not exist.
func (s *Server) SocketPath() string { return s.socket }

// Start prepares the runtime directory, takes the single-daemon lock, binds
// the socket atomically and begins serving.
//
// The order is the security argument, so it is worth reading as one:
// the directory exists and is 0700 and ours BEFORE anything is created
// inside it; the lock is taken BEFORE the path is inspected, so no second
// process can be inspecting the same path concurrently; and the bind lands
// on a temporary name and is renamed into place, so the moment the socket
// appears it is already listening. A caller that finds the socket therefore
// finds a live daemon, never a half-built one.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("coordinator: server is closed")
	}
	if s.listener != nil {
		return errors.New("coordinator: server is already started")
	}

	if len(s.socket) > maxSocketPath {
		return fmt.Errorf("%w: %d bytes at %s", ErrPathTooLong, len(s.socket), s.socket)
	}
	if err := s.prepareDir(); err != nil {
		return err
	}

	lock, err := acquireLock(s.lock)
	if err != nil {
		return err
	}
	// From here every failure path releases the lock. The alternative — a
	// lock still held by a process that decided not to serve — is a
	// directory no daemon can ever claim again until a reboot.
	release := func(cause error) error {
		if relErr := lock.release(); relErr != nil {
			s.cfg.Logger.Warn("coordinator: releasing lock after a failed start", "error", relErr)
		}
		return cause
	}

	if pathErr := s.checkSocketPath(); pathErr != nil {
		return release(pathErr)
	}
	listener, err := s.bind()
	if err != nil {
		return release(err)
	}

	s.listener = listener
	s.flock = lock
	s.cfg.Logger.Info("coordinator: discovery socket listening",
		"socket", s.socket,
		"protocol", ProtocolVersion,
		"version", s.cfg.Build.Version,
		"commit", s.cfg.Build.Commit,
	)
	go s.accept(listener)
	return nil
}

// prepareDir makes the runtime directory, forces 0700 on it and refuses it
// if it is not ours.
//
// The chmod is not redundant with the MkdirAll mode: MkdirAll applies the
// umask, and a directory left over from an earlier version — or from
// somebody's tar — carries whatever mode it was created with. The mode is
// asserted rather than assumed on every start.
func (s *Server) prepareDir() error {
	if err := os.MkdirAll(s.cfg.Dir, 0o700); err != nil {
		return fmt.Errorf("coordinator: create runtime dir %s: %w", s.cfg.Dir, err)
	}
	//nolint:gosec // 0700 IS the mode this directory must carry: a directory
	// needs its execute bit to be entered at all, and 0600 would make the
	// socket inside unreachable to its own owner.
	if err := os.Chmod(s.cfg.Dir, 0o700); err != nil {
		return fmt.Errorf("coordinator: set mode on runtime dir %s: %w", s.cfg.Dir, err)
	}
	owner, err := s.cfg.Owner.OwnerUID(s.cfg.Dir)
	if err != nil {
		return err
	}
	if owner != s.cfg.SelfUID {
		return fmt.Errorf("%w: %s is owned by uid %d, we are uid %d",
			ErrForeignOwner, s.cfg.Dir, owner, s.cfg.SelfUID)
	}
	return nil
}

// checkSocketPath decides whether the path may be bound over.
//
// A socket left behind by a daemon that died is fine — we hold the lock, so
// nothing live owns it, and the rename below replaces it. A symlink is not:
// rename(2) replaces the link itself rather than following it, so nothing
// would be written through it, but a path somebody else has redirected is a
// path we have lost control of and the right answer is to stop. Anything
// else that is not a socket is refused for the same reason.
func (s *Server) checkSocketPath() error {
	fi, err := os.Lstat(s.socket)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("coordinator: inspect socket path %s: %w", s.socket, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlinkPath, s.socket)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %s is %s", ErrOccupiedPath, s.socket, fi.Mode())
	}
	return nil
}

// bind creates the listener on a temporary name in the same directory and
// renames it into place.
//
// Two processes racing must never leave a live daemon with no socket, which
// is what bind-then-unlink-then-rebind would do to whichever lost. rename(2)
// within one directory is atomic, so the path either names the previous
// socket or this one and never nothing. The lock makes the race
// theoretical; the atomic bind is what keeps it harmless if the lock is ever
// wrong.
//
// The temporary name carries the pid so a crashed start cannot collide with
// a live one, and it is removed on every failure path below.
func (s *Server) bind() (*net.UnixListener, error) {
	tmp := filepath.Join(s.cfg.Dir, "."+socketName+"."+strconv.Itoa(os.Getpid()))
	if len(tmp) > maxSocketPath {
		return nil, fmt.Errorf("%w: %d bytes at %s", ErrPathTooLong, len(tmp), tmp)
	}
	// A previous crash may have left this exact name behind; it is ours by
	// construction (our pid), so removing it cannot take anybody else's.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("coordinator: clear stale bind name %s: %w", tmp, err)
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("coordinator: bind %s: %w", tmp, err)
	}
	// The listener knows the temporary name, not the final one, so it must
	// not unlink on close — that would delete a name it no longer owns
	// while leaving the real socket behind. Close does the unlink.
	listener.SetUnlinkOnClose(false)
	// Bind applies the umask, so the mode is set explicitly. The window
	// between the two is not reachable by another user: the parent
	// directory is already 0700 and ours.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return nil, s.abandonBind(listener, tmp, fmt.Errorf("coordinator: set mode on socket: %w", err))
	}
	if err := os.Rename(tmp, s.socket); err != nil {
		return nil, s.abandonBind(listener, tmp, fmt.Errorf("coordinator: publish socket at %s: %w", s.socket, err))
	}
	return listener, nil
}

// abandonBind closes a listener that will never serve and removes the name
// it was bound to, so a failed start leaves the directory as it found it.
func (s *Server) abandonBind(l *net.UnixListener, tmp string, cause error) error {
	if err := l.Close(); err != nil {
		s.cfg.Logger.Warn("coordinator: closing an abandoned listener", "error", err)
	}
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		s.cfg.Logger.Warn("coordinator: removing an abandoned bind name", "path", tmp, "error", err)
	}
	return cause
}

// accept runs until the listener is closed.
func (s *Server) accept(l *net.UnixListener) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.cfg.Logger.Warn("coordinator: accept failed", "error", err)
			return
		}
		s.conns.Add(1)
		go func() {
			defer s.conns.Done()
			s.serve(conn)
		}()
	}
}

// serve authorises one connection and then answers requests on it until the
// peer hangs up.
//
// The uid check is first and unconditional: everything past it can reach
// the token, so there is no request — not even a malformed one — that a
// foreign peer gets an answer to beyond the refusal itself.
func (s *Server) serve(conn *net.UnixConn) {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.cfg.Logger.Debug("coordinator: closing connection", "error", err)
		}
	}()

	uid, err := s.cfg.Peers.PeerUID(conn)
	if err != nil {
		s.cfg.Logger.Warn("coordinator: refusing a peer we cannot identify", "error", err)
		s.write(conn, Response{Error: "peer credentials unavailable"})
		return
	}
	if uid != s.cfg.SelfUID {
		s.cfg.Logger.Warn("coordinator: refusing a foreign peer", "peerUID", uid, "selfUID", s.cfg.SelfUID)
		s.write(conn, Response{Error: "peer uid " + strconv.FormatUint(uint64(uid), 10) + " is not permitted"})
		return
	}

	reader := bufio.NewReader(conn)
	for {
		if err := conn.SetDeadline(time.Now().Add(exchangeTimeout)); err != nil {
			s.cfg.Logger.Debug("coordinator: setting exchange deadline", "error", err)
			return
		}
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				s.cfg.Logger.Debug("coordinator: reading a request", "error", err)
			}
			return
		}
		var req Request
		if jsonErr := json.Unmarshal(line, &req); jsonErr != nil {
			// The offending bytes are deliberately NOT logged: a client
			// that got its framing wrong may have half a hello in there.
			s.cfg.Logger.Debug("coordinator: refusing an unparseable request")
			if !s.write(conn, Response{Error: "request is not newline-delimited JSON"}) {
				return
			}
		} else if !s.write(conn, s.answer(req)) {
			return
		}
		if err != nil {
			return
		}
	}
}

// answer maps one request to one response. The token is read here and
// nowhere else, and it is never given to the logger.
func (s *Server) answer(req Request) Response {
	if req.Type != RequestHello {
		s.cfg.Logger.Debug("coordinator: unknown request type", "type", req.Type)
		return Response{Error: "unknown request type " + strconv.Quote(req.Type)}
	}
	s.cfg.Logger.Debug("coordinator: hello",
		"clientProtocol", clientProtocol(req),
		"clientVersion", clientVersion(req),
	)
	return Response{Hello: &Hello{
		Build:     s.cfg.Build,
		Protocol:  ProtocolVersion,
		WSAddress: s.cfg.Backend.WSAddress(),
		WSToken:   s.cfg.Backend.WSToken(),
	}}
}

func clientProtocol(req Request) int {
	if req.Client == nil {
		return 0
	}
	return req.Client.Protocol
}

func clientVersion(req Request) string {
	if req.Client == nil {
		return "unstated"
	}
	return req.Client.Version
}

// write emits one response line and reports whether the connection is still
// usable. A failed write costs that connection and nothing else.
func (s *Server) write(conn *net.UnixConn, resp Response) bool {
	line, err := json.Marshal(resp)
	if err != nil {
		// Cannot happen for these shapes, and if it ever does the response
		// must not be half-written — say so and drop the connection.
		s.cfg.Logger.Error("coordinator: encoding a response", "error", err)
		return false
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		s.cfg.Logger.Debug("coordinator: writing a response", "error", err)
		return false
	}
	return true
}

// Close stops serving, unlinks the socket and releases the single-daemon
// lock. It is idempotent, because the signal path and a deferred cleanup
// both legitimately reach it.
//
// The socket is unlinked here rather than left for the next start to
// replace, so the window in which a launcher can find a socket no daemon is
// behind is the daemon's own shutdown and nothing longer.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	listener, lock := s.listener, s.flock
	s.listener, s.flock = nil, nil
	s.mu.Unlock()

	var errs []error
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("coordinator: close listener: %w", err))
		}
	}
	if err := os.Remove(s.socket); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("coordinator: unlink socket: %w", err))
	}
	s.conns.Wait()
	if err := lock.release(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	s.cfg.Logger.Info("coordinator: discovery socket closed", "socket", s.socket)
	return nil
}
