// Package endpoint is the helper's boundary, and there is exactly one of it:
// a private Unix socket, mode 0600, in a 0700 directory (level-1 design §5).
//
// # One shape locally and remotely (D11)
//
// The helper runs on every machine, including yours. Locally the coordinator
// connects to this endpoint directly; remotely `nocx-helper bridge
// <generation>` runs over the pty-less ssh exec lane, calls the same Dial on
// the far side and copies bytes. There is no second mechanism and no code
// path that exists only for one of them: what differs is which side of the
// socket the process is on, never what it speaks or how it is reached.
//
// # Why not a loopback port
//
// A port on 127.0.0.1 is reachable by ANY user of that machine, and the whole
// authorization model is the Unix account (D12: same-UID trust, any nocx under
// that account may connect). A loopback listener would annul it. This package
// contains no TCP listener and none may be added to the path — there is a test
// that walks the source of every package on it and asserts so.
//
// # And no forwarding
//
// Nothing is forwarded remotely: the bridge is a process on the far side that
// connects to a socket on the far side. No port forward is configured and none
// is required. `direct-streamlocal@openssh.com` would be one fewer process per
// connection and is an optional carrier improvement, never the boundary.
//
// # The ssh carrier needs no authentication of its own
//
// Reaching this socket already required becoming the account: the directory is
// 0700 and the socket 0600, so an ssh session that got here authenticated as
// the owner, and there is nothing further for the carrier to prove. A non-ssh
// carrier must supply an authentication of its own; that is the carrier's
// problem, not this protocol's.
package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/helper/proto"
)

const (
	// DirMode is the endpoint directory: private to the account that owns it.
	// It is the boundary that holds on every platform, including the ones
	// that have historically not enforced permissions on the socket file
	// itself.
	DirMode os.FileMode = 0o700
	// SocketMode is the endpoint: readable and writable by its owner and
	// nobody else.
	SocketMode os.FileMode = 0o600
	// DirName is the endpoint directory's name under the account's home. It
	// is deliberately short: a Unix socket path is bounded by sun_path, which
	// is 104 bytes on darwin, and the install directory the helper binary
	// lives in (~/.nocx/helper/<version>-<goos>-<goarch>-<64 hex>) is far too
	// long to also hold the socket.
	DirName = "run"
	// ServeCommand and BridgeCommand are the helper subcommands this package
	// starts and the binary dispatches. They are named here because Ensure
	// spawns one of them: one owner for the vocabulary, so a rename cannot
	// leave a caller spelling it the old way.
	ServeCommand  = "serve"
	BridgeCommand = "bridge"
	// genPrefix is how much of the generation names the socket. The whole
	// content hash plus a directory does not fit in sun_path, and 64 bits of
	// it distinguishes every generation an account will ever hold at once.
	// The dialled endpoint's identity is confirmed by the handshake's content
	// hash regardless (D21), so this is a NAME and never a credential.
	genPrefix = 16
)

var (
	// ErrNoEndpoint is a dial that found nothing serving: no socket file, or
	// a stale one a dead helper left behind. It is an answer about this
	// generation's socket and never a verdict about a session (D5) — the
	// caller starts a helper or reports that none is running, and reconciles
	// nothing.
	ErrNoEndpoint = errors.New("endpoint: no helper is serving this generation")
	// ErrAlreadyServing is a Listen that found a LIVE endpoint. It is the
	// loser's answer in a start race, and it is deliberately not an error to
	// recover from by force: unlinking a socket somebody's sessions are
	// reachable through would end them.
	ErrAlreadyServing = errors.New("endpoint: a helper of this generation already holds the endpoint")
	// ErrForeignDir is an endpoint directory this account does not own. It is
	// never repaired and never used: a directory another account can write to
	// is a directory another account can put its own socket in, and dialling
	// it would hand our session to them.
	ErrForeignDir = errors.New("endpoint: the endpoint directory belongs to another account")
	// ErrBadGeneration is a generation that is not a content hash. The socket
	// name is derived from it, so it is validated where it is parsed.
	ErrBadGeneration = errors.New("endpoint: generation is not a content hash")
	// ErrPathTooLong is a socket path longer than the platform's sun_path
	// bound. It is refused with the number in it rather than being truncated
	// into a path two generations could share.
	ErrPathTooLong = errors.New("endpoint: the socket path exceeds the platform's limit")
)

// maxSocketPath is the shortest sun_path bound across the platforms we ship
// (darwin's 104, against Linux's 108), minus the trailing NUL. Bounding by
// the smallest means a path that works here works there: a developer on Linux
// cannot write a home directory that only fails on the Mac.
const maxSocketPath = 103

// Dir is the endpoint directory for an account's home. It is derived from the
// home and NOTHING else — deliberately, and this is the reason: the
// coordinator, the bridge and the daemon must agree on one path without
// sharing an environment, and a run directory taken from XDG_RUNTIME_DIR or
// TMPDIR would differ between a GUI launch, an ssh exec and a login shell.
// Two nocx processes under one account that disagreed about where the socket
// is would start two helpers and each hold half the sessions.
func Dir(home string) string { return filepath.Join(home, ".nocx", DirName) }

// Path is the socket path for one generation inside dir.
func Path(dir string, gen proto.GenerationID) (string, error) {
	name, err := socketName(gen)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if len(p) > maxSocketPath {
		return "", fmt.Errorf("%w: %d bytes, the limit is %d: %s", ErrPathTooLong, len(p), maxSocketPath, p)
	}
	return p, nil
}

// socketName names the socket for a generation: the protocol version, so two
// incompatible helpers can never collide on one path, and the generation's
// first 64 bits.
func socketName(gen proto.GenerationID) (string, error) {
	s := string(gen)
	if len(s) < genPrefix {
		return "", fmt.Errorf("%w: %q is %d characters, want at least %d", ErrBadGeneration, s, len(s), genPrefix)
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", fmt.Errorf("%w: %q is not hex", ErrBadGeneration, s)
		}
	}
	return proto.Version + "-" + s[:genPrefix] + ".sock", nil
}

// Listen binds the endpoint for one generation: it makes the directory
// private, replaces a socket a dead helper left behind, binds and narrows the
// socket to 0600.
//
// The three states a socket path can be in are distinguished by ASKING, never
// by inferring from an error (D4's rule that liveness is a fact): nothing
// there is bound directly; a path that ACCEPTS a connection is a live helper
// and answers ErrAlreadyServing; a path that refuses one is stale and is
// unlinked. What is true after a stale replacement is that the old file is
// gone and this process holds the endpoint; what is true after
// ErrAlreadyServing is that the FIRST helper still holds it and this one has
// changed nothing.
func Listen(dir string, gen proto.GenerationID) (net.Listener, error) {
	path, err := prepare(dir, gen)
	if err != nil {
		return nil, err
	}
	if serr := clearStale(path); serr != nil {
		return nil, serr
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		// The bind can lose a race the stale check just won: two helpers
		// starting together both find no socket, and one binds first. That is
		// the same fact ErrAlreadyServing names, so it is named the same way —
		// and it is CONFIRMED by dialling rather than inferred from the errno,
		// because an address already in use and a socket somebody is serving
		// are only the same thing when something answers.
		if conn, derr := net.Dial("unix", path); derr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: %s", ErrAlreadyServing, path)
		}
		return nil, fmt.Errorf("endpoint: listen on %s: %w", path, err)
	}
	// Narrowed after the bind rather than through the umask, which is
	// process-wide and inherited from whoever started us. There is a window
	// between bind and chmod in which the socket carries the umask's mode;
	// the 0700 directory is what closes it, and is why the directory is the
	// boundary rather than a belt beside it.
	if err := os.Chmod(path, SocketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("endpoint: narrow %s: %w", path, err)
	}
	return ln, nil
}

// Dial connects to the endpoint for one generation. A missing or stale socket
// is ErrNoEndpoint — an answer, not a failure to interpret.
func Dial(ctx context.Context, dir string, gen proto.GenerationID) (net.Conn, error) {
	path, err := prepare(dir, gen)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrNoEndpoint, path, err)
	}
	return conn, nil
}

// prepare makes the endpoint directory exist, private and ours, and returns
// the socket path inside it. Both sides of the boundary run it — the helper
// before it binds and the coordinator before it dials — because a directory
// loosened after the bind is exactly as reachable as one that was always
// loose, and only the process passing through it can notice.
func prepare(dir string, gen proto.GenerationID) (string, error) {
	path, err := Path(dir, gen)
	if err != nil {
		return "", err
	}
	if err := ensureDir(dir); err != nil {
		return "", err
	}
	return path, nil
}

// ensureDir creates the endpoint directory 0700 and, if it already exists,
// checks the two things that make it the boundary: it is ours, and only we
// may enter it. Ours is REFUSED when it is not — a directory another account
// owns can be made to hold their socket, and no repair we could apply would
// change who owns it. Loose is REPAIRED, because a directory we own we can
// close, and refusing would strand a user behind a mode they cannot see.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("endpoint: create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("endpoint: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("endpoint: %s is not a directory", dir)
	}
	if !ownedByThisAccount(info) {
		return fmt.Errorf("%w: %s", ErrForeignDir, dir)
	}
	if info.Mode().Perm() != DirMode {
		if err := os.Chmod(dir, DirMode); err != nil {
			return fmt.Errorf("endpoint: narrow %s: %w", dir, err)
		}
	}
	return nil
}

// clearStale removes a socket file nothing is listening on, and refuses to
// touch one that answers. The probe is a real connection: "does anything
// accept here" is a fact, where "the last helper's pid file says it died" is
// an inference.
func clearStale(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("endpoint: stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		// Not a socket at all: something else owns this name. Removing it
		// would be destroying a file we cannot account for.
		return fmt.Errorf("endpoint: %s exists and is not a socket", path)
	}
	if conn, derr := net.Dial("unix", path); derr == nil {
		_ = conn.Close()
		return fmt.Errorf("%w: %s", ErrAlreadyServing, path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("endpoint: remove the stale socket %s: %w", path, err)
	}
	return nil
}

// ExitNoEndpoint is `nocx-helper bridge`'s exit code for a generation nothing
// is serving and that this binary may not serve. It is its own code because
// it is its own sentence for the user — "no helper is running there" is not
// "the host refused the exec" and not "that is not our helper" — and because
// the coordinator's classification of a peer that ended before the sentinel
// has nothing else to go on.
const ExitNoEndpoint = 43
