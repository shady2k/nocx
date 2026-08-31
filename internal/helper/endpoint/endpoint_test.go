package endpoint_test

// The endpoint is the boundary (level-1 design §5): a private Unix socket,
// mode 0600, in a 0700 directory. These are its own failure paths — the
// directory with the wrong permissions, the stale socket a dead helper left,
// the live one that must not be stolen, and the dial that finds nothing.
//
// "A failure is never a verdict" (D5) is the rule the last of them obeys: a
// dial that finds no endpoint answers ErrNoEndpoint, which is an ANSWER about
// this generation's socket and never a claim about a session.

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
)

const gen = proto.GenerationID("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

// runDir is a short temp directory: a Unix socket path is bounded by sun_path
// (104 bytes on darwin), and a t.TempDir() under a long TMPDIR eats most of
// it. The bound is the platform's, so the test honours it rather than
// pretending it does not exist.
func runDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nocxep")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "run")
}

func TestListenCreatesAPrivateSocketInAPrivateDirectory(t *testing.T) {
	dir := runDir(t)
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// The boundary is a Unix socket and nothing else. A loopback TCP port is
	// reachable by any user of the machine, and the whole authorization model
	// is the Unix account (D12) — so the network is asserted, not assumed.
	if got := ln.Addr().Network(); got != "unix" {
		t.Fatalf("the endpoint listens on %q, want unix", got)
	}

	path, err := endpoint.Path(dir, gen)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	si, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat socket: %v", err)
	}
	if si.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("the endpoint is not a socket: %v", si.Mode())
	}
	if perm := si.Mode().Perm(); perm != endpoint.SocketMode {
		t.Errorf("socket mode %#o, want %#o", perm, endpoint.SocketMode)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != endpoint.DirMode {
		t.Errorf("directory mode %#o, want %#o", perm, endpoint.DirMode)
	}
}

func TestALooseDirectoryIsRepairedRatherThanUsedAsFound(t *testing.T) {
	dir := runDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a loose directory is the fixture: the repair is what is under test
		t.Fatalf("mkdir: %v", err)
	}
	// A directory anyone may enter is a directory anyone may reach the socket
	// through, and on a platform that does not enforce socket permissions
	// that is the whole boundary. What is true afterwards: the directory is
	// 0700 and the endpoint is serving. The next attempt needs no repair.
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen over a loose directory: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != endpoint.DirMode {
		t.Fatalf("the loose directory was used as found: mode %#o", perm)
	}
}

func TestALooseDirectoryIsRepairedBeforeADialToo(t *testing.T) {
	dir := runDir(t)
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if cerr := os.Chmod(dir, 0o755); cerr != nil { //nolint:gosec // loosening it under a live endpoint is the fixture
		t.Fatalf("chmod: %v", cerr)
	}
	// The coordinator reaches the same boundary as the helper, so it applies
	// the same rule: a directory that has been loosened under a live endpoint
	// is closed again on the way past it, not walked through.
	conn, err := endpoint.Dial(context.Background(), dir, gen)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != endpoint.DirMode {
		t.Fatalf("a dial walked through a loose directory: mode %#o", perm)
	}
}

func TestTheEndpointPathIsRefusedWhenItIsNotADirectory(t *testing.T) {
	dir := runDir(t)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := endpoint.Listen(dir, gen); err == nil {
		t.Fatal("Listen accepted a file where the endpoint directory belongs")
	}
}

// staleSocket leaves a socket file behind with nothing listening on it —
// exactly what a helper killed with SIGKILL leaves, since the file is
// unlinked by an orderly Close and by nothing else.
func staleSocket(t *testing.T, dir string) string {
	t.Helper()
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// SetUnlinkOnClose returns nothing to check; leaving the file behind is
	// exactly what a helper killed with SIGKILL does.
	ln.(*net.UnixListener).SetUnlinkOnClose(false) //nolint:errcheck // no error to check
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	path, err := endpoint.Path(dir, gen)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the stale socket was not left behind: %v", err)
	}
	return path
}

func TestAStaleSocketFromADeadHelperIsReplaced(t *testing.T) {
	dir := runDir(t)
	staleSocket(t, dir)

	// What is true afterwards: the stale file is gone, a live endpoint is in
	// its place, and a coordinator reaches it. Recovery is the ordinary
	// start, not a repair tool somebody has to know about.
	ln, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := endpoint.Dial(context.Background(), dir, gen)
	if err != nil {
		t.Fatalf("Dial after replacing the stale socket: %v", err)
	}
	_ = conn.Close()
}

func TestDialingAStaleSocketIsNoEndpointRatherThanAnError(t *testing.T) {
	dir := runDir(t)
	staleSocket(t, dir)
	_, err := endpoint.Dial(context.Background(), dir, gen)
	if !errors.Is(err, endpoint.ErrNoEndpoint) {
		t.Fatalf("Dial over a stale socket = %v, want ErrNoEndpoint", err)
	}
}

func TestDialingAnEndpointThatWasNeverThereIsNoEndpoint(t *testing.T) {
	dir := runDir(t)
	_, err := endpoint.Dial(context.Background(), dir, gen)
	if !errors.Is(err, endpoint.ErrNoEndpoint) {
		t.Fatalf("Dial with no endpoint = %v, want ErrNoEndpoint", err)
	}
}

func TestALiveEndpointIsNeverStolenByASecondHelper(t *testing.T) {
	dir := runDir(t)
	first, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	// The loser of a start race must not unlink a socket somebody's sessions
	// are reachable through. It answers ErrAlreadyServing and exits; what is
	// true afterwards is that the FIRST helper still holds the endpoint.
	if _, lerr := endpoint.Listen(dir, gen); !errors.Is(lerr, endpoint.ErrAlreadyServing) {
		t.Fatalf("a second Listen = %v, want ErrAlreadyServing", lerr)
	}
	conn, err := endpoint.Dial(context.Background(), dir, gen)
	if err != nil {
		t.Fatalf("the live endpoint stopped answering: %v", err)
	}
	_ = conn.Close()
}

func TestTwoGenerationsCoexistOnOneMachine(t *testing.T) {
	// D4: installs are content-addressed and immutable, so two generations
	// are resident at once. Their endpoints are two sockets in one directory.
	dir := runDir(t)
	other := proto.GenerationID("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")

	a, err := endpoint.Listen(dir, gen)
	if err != nil {
		t.Fatalf("Listen a: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := endpoint.Listen(dir, other)
	if err != nil {
		t.Fatalf("Listen b: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	if a.Addr().String() == b.Addr().String() {
		t.Fatalf("two generations share one endpoint: %s", a.Addr())
	}
}

func TestAGenerationThatIsNotAContentHashIsRefused(t *testing.T) {
	// The socket name is derived from the generation, so a generation
	// carrying a path separator would name a file outside the endpoint
	// directory. It is refused where it is parsed, once.
	dir := runDir(t)
	for _, bad := range []proto.GenerationID{"", "../../etc/passwd", "not hex", "abc"} {
		if _, err := endpoint.Path(dir, bad); err == nil {
			t.Errorf("Path accepted generation %q", bad)
		}
	}
}

func TestTheLoserOfABindRaceIsToldSomebodyIsServingRatherThanAnErrno(t *testing.T) {
	// The stale check and the bind are two steps, and a helper starting at the
	// same moment can win between them: the loser's Lstat finds nothing and its
	// bind then fails with "address already in use". That is the same fact
	// ErrAlreadyServing names, and naming it differently would turn an ordinary
	// double start into a failure a user has to read an errno to understand.
	dir := runDir(t)
	path, err := endpoint.Path(dir, gen)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if merr := os.MkdirAll(dir, endpoint.DirMode); merr != nil {
		t.Fatalf("mkdir: %v", merr)
	}
	// A live listener the stale check cannot have seen, because it appeared
	// after that check — which is exactly the window under test.
	winner, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = winner.Close() })

	if _, lerr := endpoint.Listen(dir, gen); !errors.Is(lerr, endpoint.ErrAlreadyServing) {
		t.Fatalf("the loser of the bind race got %v, want ErrAlreadyServing", lerr)
	}
}
