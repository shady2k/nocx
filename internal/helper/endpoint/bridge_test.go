package endpoint_test

// The bridge's own failure paths, and the one that matters most: the ssh
// channel dying mid-copy. The bridge is stateless and disposable — it holds no
// session, no window and no lock — so its death is one attachment ending (D2),
// and the session, its window and its process survive it. A new bridge
// reattaches to the same session afterwards.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// bridgeTo puts a real endpoint.Bridge between a coordinator and the daemon,
// over a pipe pair standing in for the pty-less ssh exec lane. The coordinator
// end is a plain stream, which is the whole point: it is the same stream the
// local carrier is, and the coordinator above it cannot tell.
func bridgeTo(t *testing.T, d *helperDaemon) io.ReadWriteCloser {
	t.Helper()
	laneIn, coordOut := net.Pipe() // coordinator -> the bridge's stdin
	coordIn, laneOut := net.Pipe() // the bridge's stdout -> coordinator
	conn, err := endpoint.Dial(t.Context(), d.dir, gen)
	if err != nil {
		t.Fatalf("the bridge could not reach the endpoint: %v", err)
	}
	go func() {
		_ = endpoint.Bridge(context.Background(), laneIn, laneOut, conn)
		_ = conn.Close()
		_ = laneIn.Close()
		_ = laneOut.Close()
	}()
	return duplex{r: coordIn, w: coordOut}
}

// duplex is the coordinator's end of the lane as one stream: a reader out of
// one pipe and a writer into the other, which is what an ssh exec channel's
// stdout and stdin add up to.
type duplex struct {
	r net.Conn
	w net.Conn
}

func (d duplex) Read(p []byte) (int, error)  { return d.r.Read(p) }
func (d duplex) Write(p []byte) (int, error) { return d.w.Write(p) }

// Close is the ssh channel dying: both directions at once, mid-copy, with no
// orderly half-close for the far side to read anything into.
func (d duplex) Close() error {
	_ = d.r.Close()
	return d.w.Close()
}

func TestABridgeDyingMidCopyEndsAnAttachmentAndNothingElse(t *testing.T) {
	d := startDaemon(t, session.Shell{Path: "/bin/sh", Args: []string{"-i"}})
	sub := proto.SubscriberID("0123456789abcdef0123456789abcdef")
	subRaw := subscriberBytes(t, sub)

	first := connectOver(t, bridgeTo(t, d))
	var spawned proto.SpawnResult
	first.request(proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &spawned)
	handle := spawned.Entry.Session

	var attached proto.AttachResult
	first.request(proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: handle, Offset: 0, Fresh: true, RequestWrite: true,
	}, &attached)
	first.write(proto.TypeSessionData, proto.EncodeSessionFrame(proto.SessionFrame{
		Session:    mustSession(t, handle.Session),
		Subscriber: subRaw,
		Epoch:      attached.Write.Epoch,
		Payload:    []byte("i=0; while :; do i=$((i+1)); echo MARK$i; sleep 1; done\n"),
	}))
	first.await("the marker loop to start", func() bool {
		return bytes.Count(first.received(subRaw), []byte("MARK")) >= 3
	})

	// The ssh channel dies mid-copy.
	first.close()

	// A NEW bridge, a new attachment, the same session — and the process is
	// still running, which the window growing is what proves.
	second := connectOver(t, bridgeTo(t, d))
	var inv proto.SessionsResult
	second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session != handle {
		t.Fatalf("the session did not survive the bridge: %+v", inv.Sessions)
	}
	if inv.Sessions[0].Exit != nil {
		t.Fatalf("the session died with the bridge: %+v", inv.Sessions[0].Exit)
	}
	was := inv.Sessions[0].Window.Written
	for {
		second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
		if inv.Sessions[0].Window.Written > was {
			break
		}
		if t.Context().Err() != nil {
			t.Fatalf("the process stopped when the bridge died: the window never grew past %d", was)
		}
	}
}

func TestEnsureStartsAHelperWhenNoneIsServing(t *testing.T) {
	// The other half of D11: nothing is serving until something starts it, and
	// the thing that reaches for the endpoint is the thing that starts it —
	// the same Ensure locally and inside the bridge remotely. This runs the
	// REAL helper, through a wrapper that records the pid, because a helper is
	// deliberately detached from whoever started it (that is D1) and a test
	// that could not stop one again would leave it running on the machine.
	bin, generation := helperBinary(t)
	home := helperHome(t)

	wrapper := filepath.Join(t.TempDir(), "start-helper")
	pidFile := filepath.Join(home, "helper.pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexport HOME=" + home + "\nexec " + bin + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // it is meant to be executable
		t.Fatalf("write wrapper: %v", err)
	}
	t.Cleanup(func() { stopRecorded(t, pidFile) })

	dir := endpoint.Dir(home)
	conn, err := endpoint.Ensure(t.Context(), dir, generation, generation, wrapper)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	_ = conn.Close()

	// What is true afterwards: a helper of this generation holds the endpoint,
	// and the next reach for it starts nothing — it dials what is there.
	again, err := endpoint.Dial(t.Context(), dir, generation)
	if err != nil {
		t.Fatalf("the helper Ensure started is not serving: %v", err)
	}
	_ = again.Close()
}

func TestEnsureRefusesToServeAGenerationThisBinaryIsNot(t *testing.T) {
	// A binary that is not that generation may not publish its name over
	// somebody's sessions. The refusal is ErrNoEndpoint, because that is what
	// is true: nothing is serving it, and this process is not the one that can
	// change that. Nothing was started, so nothing has to be cleaned up.
	dir := runDir(t)
	other := proto.GenerationID("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
	_, err := endpoint.Ensure(context.Background(), dir, other, gen, "/nonexistent/helper")
	if !errors.Is(err, endpoint.ErrNoEndpoint) {
		t.Fatalf("Ensure = %v, want ErrNoEndpoint", err)
	}
}

func TestEnsureReportsAHelperThatDiesBeforeItServes(t *testing.T) {
	dir := runDir(t)
	// A binary that exits immediately: the wait ends on the child's exit — an
	// observable fact — rather than on a duration, and what is true afterwards
	// is that nothing is serving and nothing was left behind for the next
	// attempt to trip over.
	_, err := endpoint.Ensure(context.Background(), dir, gen, gen, lookPath(t, "false"))
	if err == nil {
		t.Fatal("Ensure returned a connection to a helper that never served")
	}
	path, perr := endpoint.Path(dir, gen)
	if perr != nil {
		t.Fatalf("Path: %v", perr)
	}
	if _, serr := os.Lstat(path); serr == nil {
		t.Fatalf("a helper that never served left a socket at %s", path)
	}
}

// stopRecorded ends the helper whose pid the wrapper recorded. An interrupt
// rather than a kill: the helper's own shutdown closes every PTY it holds, so
// the shells it spawned die with it instead of being orphaned onto the machine
// running the tests.
func stopRecorded(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile) // #nosec G304 — this test's wrapper wrote it
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Signal(os.Interrupt)
}
