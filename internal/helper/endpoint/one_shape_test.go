package endpoint_test

// THE CLAUSE THAT PROVES THE FEATURE (D11): the coordinator reaches the helper
// through the SAME endpoint locally and remotely, with no branch that exists
// only for the local case.
//
// It is proved the only way a claim like that can be: the same coordinator —
// the shipped internal/helper/client, its handshake and its Call — is driven
// twice against one running helper, once over a socket it dialled itself and
// once over `nocx-helper bridge` on the far side of a pipe pair standing in for
// the ssh exec lane. A session spawned through one is found and attached
// through the other, because there is one endpoint and both carriers reach it.
//
// The binary under test is the real one, built here: the subcommand dispatch,
// the generation derived from the binary's own content hash, and the bridge's
// byte copying are all in it, and a test that reimplemented any of them would
// be asserting about itself.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
)

var (
	buildOnce   sync.Once
	builtBinary string
	builtGen    proto.GenerationID
	buildErr    error
)

// helperBinary builds cmd/nocx-helper once for this package's tests and
// returns its path and its generation — which is the sha256 of the file,
// because a helper install is content-addressed and the generation IS the
// build (D10). The test computes it the same way the binary does, from the
// bytes on disk; if the two ever disagreed the handshake would refuse, which
// is the check being relied on here.
func helperBinary(t *testing.T) (string, proto.GenerationID) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nocxbin")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "nocx-helper")
		out, err := exec.Command("go", "build", "-o", bin, "../../../cmd/nocx-helper").CombinedOutput() //nolint:gosec // the arguments are this test's own constants
		if err != nil {
			buildErr = errors.New(string(out))
			return
		}
		data, err := os.ReadFile(bin) // #nosec G304 — the path is this test's own build output
		if err != nil {
			buildErr = err
			return
		}
		sum := sha256.Sum256(data)
		builtBinary, builtGen = bin, proto.GenerationID(hex.EncodeToString(sum[:]))
	})
	if buildErr != nil {
		t.Fatalf("building cmd/nocx-helper: %v", buildErr)
	}
	return builtBinary, builtGen
}

// helperHome is the account the helper under test runs as, as far as the
// endpoint is concerned: the run directory is derived from the home and from
// nothing else, so giving the subprocesses a home of their own is what keeps a
// test off the developer's real endpoint — and off any other test's.
func helperHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "nocxh")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

// runHelper starts `nocx-helper serve` and waits for the endpoint to answer.
// The wait is on the socket accepting — an observable state — and is bounded
// by the test's context, never by a duration.
func runHelper(t *testing.T, bin, home string, generation proto.GenerationID) {
	t.Helper()
	cmd := exec.Command(bin, endpoint.ServeCommand) //nolint:gosec // bin is this test's own build output
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	t.Cleanup(func() {
		// SIGTERM rather than SIGKILL: the helper's own shutdown closes every
		// PTY it holds, so the shells it spawned die with it instead of being
		// orphaned onto the machine running the tests.
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	})

	dir := endpoint.Dir(home)
	for {
		conn, err := endpoint.Dial(t.Context(), dir, generation)
		if err == nil {
			_ = conn.Close()
			return
		}
		if t.Context().Err() != nil {
			t.Fatalf("the helper never answered on its endpoint: %v", err)
		}
	}
}

// lane is the pty-less ssh exec lane, standing in for internal/ssh: it runs a
// command with pipes and no terminal, which is exactly what the real lane
// gives the helper. It is the SAME HelperConn the production client takes, so
// what is being exercised over it is the shipped Dial and nothing else.
type lane struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
	done   chan struct{}
	once   sync.Once
}

func newLane(t *testing.T, bin, home string, args ...string) *lane {
	t.Helper()
	cmd := exec.Command(bin, args...) // #nosec G204 — the binary is this test's own build output
	cmd.Env = append(os.Environ(), "HOME="+home)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	return &lane{cmd: cmd, stdin: in, stdout: out, stderr: errPipe, done: make(chan struct{})}
}

func (l *lane) Stdin() io.WriteCloser { return l.stdin }
func (l *lane) Stdout() io.Reader     { return l.stdout }
func (l *lane) Stderr() io.Reader     { return l.stderr }
func (l *lane) Start(string) error    { return l.cmd.Start() }

func (l *lane) Wait() (int, error) {
	err := l.cmd.Wait()
	l.once.Do(func() { close(l.done) })
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (l *lane) Done() <-chan struct{} { return make(chan struct{}) }
func (l *lane) LostErr() error        { return nil }
func (l *lane) Close() error          { _ = l.stdin.Close(); return l.cmd.Process.Kill() }

func TestOneShapeLocallyAndRemotely(t *testing.T) {
	bin, generation := helperBinary(t)
	home := helperHome(t)
	runHelper(t, bin, home, generation)
	ctx := t.Context()

	// --- the local coordinator: it connects to the endpoint directly -------
	conn, err := endpoint.Dial(ctx, endpoint.Dir(home), generation)
	if err != nil {
		t.Fatalf("local dial: %v", err)
	}
	socket := client.NewSocketConn(conn)
	local, err := client.Dial(ctx, client.Config{Exec: socket, ExpectHash: string(generation)})
	if err != nil {
		t.Fatalf("the local coordinator could not reach the helper: %v", err)
	}
	t.Cleanup(func() { _ = local.Close() })

	// --- the remote coordinator: the same client, over the bridge ----------
	// No port forwarding is configured here, and none is required: the bridge
	// is a process on the helper's own machine that connects to the helper's
	// own socket. What crosses the lane is the frame protocol, unchanged.
	remoteLane := newLane(t, bin, home, endpoint.BridgeCommand, string(generation))
	remote, err := client.Dial(ctx, client.Config{
		Exec:       remoteLane,
		Command:    bin + " " + endpoint.BridgeCommand + " " + string(generation),
		ExpectHash: string(generation),
	})
	if err != nil {
		t.Fatalf("the bridged coordinator could not reach the helper: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })

	// --- and they are on the same helper -----------------------------------
	var spawned proto.SpawnResult
	if err := local.Call(ctx, proto.ServiceSession, proto.OpSpawn,
		proto.SpawnParams{Cols: 80, Rows: 24}, &spawned); err != nil {
		t.Fatalf("spawn through the local carrier: %v", err)
	}
	handle := spawned.Entry.Session
	if handle.Generation != generation {
		t.Fatalf("the session names generation %q, want %q", handle.Generation, generation)
	}

	var inv proto.SessionsResult
	if err := remote.Call(ctx, proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &inv); err != nil {
		t.Fatalf("inventory through the bridge: %v", err)
	}
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session != handle {
		t.Fatalf("the bridged coordinator does not see the session the local one opened: %+v", inv.Sessions)
	}

	// The bridged coordinator attaches to it, as its own subscriber. There is
	// nothing here that the local one does differently: same op, same params,
	// same result shape, one endpoint.
	var attached proto.AttachResult
	if err := remote.Call(ctx, proto.ServiceSession, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("fedcba9876543210fedcba9876543210"),
		Session:    handle, Offset: 0, Fresh: true,
	}, &attached); err != nil {
		t.Fatalf("attach through the bridge: %v", err)
	}

	// And the local one is still there, still answering, still holding the
	// same session: neither carrier displaces the other (D12).
	var still proto.SessionsResult
	if err := local.Call(ctx, proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &still); err != nil {
		t.Fatalf("inventory through the local carrier: %v", err)
	}
	if len(still.Sessions) != 1 || still.Sessions[0].Session != handle {
		t.Fatalf("the local coordinator lost its session to the bridged one: %+v", still.Sessions)
	}
}

func TestTheBridgeRefusesWhenTheFarSideEndpointIsNotThere(t *testing.T) {
	bin, _ := helperBinary(t)
	home := helperHome(t)

	// A generation this binary is not, with nothing serving it. Starting a
	// helper for it would publish another generation's name over our
	// sessions, so the bridge refuses — and it refuses with its own exit code,
	// because "no helper is running there" is a different sentence for the
	// user than "the host refused the exec".
	other := proto.GenerationID("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
	cmd := exec.Command(bin, endpoint.BridgeCommand, string(other)) // #nosec G204 — this test's own build output
	cmd.Env = append(os.Environ(), "HOME="+home)
	err := cmd.Run()

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("the bridge did not refuse: %v", err)
	}
	if ee.ExitCode() != endpoint.ExitNoEndpoint {
		t.Fatalf("the bridge exited %d, want %d", ee.ExitCode(), endpoint.ExitNoEndpoint)
	}
	// And it changed nothing on the way past: no socket for a generation
	// nobody is serving.
	path, perr := endpoint.Path(endpoint.Dir(home), other)
	if perr != nil {
		t.Fatalf("Path: %v", perr)
	}
	if _, serr := os.Lstat(path); serr == nil {
		t.Fatalf("the refusing bridge left a socket at %s", path)
	}
}

func TestStartingASecondHelperForAGenerationChangesNothing(t *testing.T) {
	bin, generation := helperBinary(t)
	home := helperHome(t)
	runHelper(t, bin, home, generation)

	// Two coordinators reaching for one generation at the same time both try
	// to start it. The second start is not an error to anybody: it answers by
	// exiting 0, having changed nothing, and the FIRST helper still holds the
	// endpoint — which the dial afterwards is what proves.
	cmd := exec.Command(bin, endpoint.ServeCommand) // #nosec G204 — this test's own build output
	cmd.Env = append(os.Environ(), "HOME="+home)
	if err := cmd.Run(); err != nil {
		t.Fatalf("a second helper for a generation already served exited %v, want 0", err)
	}
	conn, err := endpoint.Dial(t.Context(), endpoint.Dir(home), generation)
	if err != nil {
		t.Fatalf("the first helper stopped serving after a second one started: %v", err)
	}
	_ = conn.Close()
}
