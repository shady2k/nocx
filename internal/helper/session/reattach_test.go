package session_test

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// This is nocx-k6p18.3's acceptance criterion, and it is the clause that
// proves the FEATURE rather than the code: killing and restarting the
// coordinator leaves the session in the inventory and reattachable, and the
// process is proved to have RUN across the gap by a per-second marker COUNTED
// afterwards — not by a pane having rendered.
//
// It runs a real shell under a real PTY, over the real frame protocol, through
// the shipped host dispatcher. The only thing it fakes is the ssh carrier,
// which is a pipe here and a socket in nocx-k6p18.4: the helper's stdin and
// stdout ARE the connection, so closing them is exactly what a coordinator
// dying looks like from the helper's side.
//
// The shape is e2e/coordinator-reclaim.spec.ts's, applied one level down. That
// spec proves the coordinator daemon survives its client; this proves the
// SESSION survives its coordinator, which is the level-1 promise (D1).

var markerRe = regexp.MustCompile(`MARK(\d+)`)

// markers extracts every marker index in a byte range, in order. The command
// that produces them is echoed back by the terminal as `echo MARK$i`, which
// does not match — so what is counted is only what the shell actually RAN.
func markers(b []byte) []int {
	var out []int
	for _, m := range markerRe.FindAllSubmatch(b, -1) {
		n, err := strconv.Atoi(string(m[1]))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// helperConn is one coordinator↔helper connection: the host serving on one end
// of a pipe pair and a minimal frame client on the other. It is hand-rolled
// rather than borrowed from internal/helper/client because that client's job
// is launching a helper over ssh, and what is under test here is the wire.
type helperConn struct {
	t         *testing.T
	toHost    *io.PipeWriter
	serveDone chan error
	release   func()

	mu        sync.Mutex
	responses map[uint64]proto.Response
	notes     []proto.Notification
	stream    map[[16]byte][]byte
	arrived   chan struct{}

	seq uint64
}

func dialHelper(t *testing.T, svc *session.Service) *helperConn {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "content-hash", "instance", discardLog())
	h.Register(svc)

	c := &helperConn{
		t: t, toHost: inW,
		serveDone: make(chan error, 1),
		responses: make(map[uint64]proto.Response),
		stream:    make(map[[16]byte][]byte),
		arrived:   make(chan struct{}),
	}
	c.release = svc.Bind(h)
	go func() { c.serveDone <- h.Serve(context.Background()) }()

	// The hello goes first and the sentinel answers it: a version mismatch
	// must write NOTHING to stdout (D5), so there is no banner to read until
	// the version has been agreed.
	c.send(proto.TypeHello, mustJSON(t, proto.Hello{Version: proto.Version, Nonce: "n"}))

	// The sentinel line comes before any frame: it is what tells a caller it
	// reached OUR helper and not a login banner.
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := outR.Read(one)
		if err != nil {
			t.Fatalf("sentinel: %v", err)
		}
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			line = append(line, one[0])
		}
	}

	if want := "nocx-helper " + proto.Version + " ready"; string(line) != want {
		t.Fatalf("sentinel = %q, want %q", line, want)
	}

	go c.read(outR)
	return c
}

func (c *helperConn) read(r io.Reader) {
	dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
		c.mu.Lock()
		switch ty {
		case proto.TypeResponse:
			var resp proto.Response
			if err := json.Unmarshal(payload, &resp); err == nil {
				c.responses[resp.ID] = resp
			}
		case proto.TypeNotify:
			var n proto.Notification
			if err := json.Unmarshal(payload, &n); err == nil {
				c.notes = append(c.notes, n)
			}
		case proto.TypeSessionData:
			if f, err := proto.DecodeSessionFrame(payload); err == nil {
				c.stream[f.Subscriber] = append(c.stream[f.Subscriber], f.Payload...)
			}
		}
		close(c.arrived)
		c.arrived = make(chan struct{})
		c.mu.Unlock()
	}, nil)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_ = dec.Feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (c *helperConn) send(ty proto.FrameType, payload []byte) {
	if _, err := c.toHost.Write(proto.EncodeFrame(ty, 0, 0, payload)); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

func (c *helperConn) waiter() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.arrived
}

// await blocks until want() holds, waking on each frame. Never on a clock: a
// condition that never becomes true is reported by the test's own timeout.
func (c *helperConn) await(what string, want func() bool) {
	c.t.Helper()
	for {
		next := c.waiter()
		if want() {
			return
		}
		select {
		case <-next:
		case <-c.t.Context().Done():
			c.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// request sends one request and decodes its answer.
func (c *helperConn) request(op string, params any, out any) {
	c.t.Helper()
	c.mu.Lock()
	c.seq++
	id := c.seq
	c.mu.Unlock()

	c.send(proto.TypeRequest, mustJSON(c.t, proto.Request{
		ID: id, Service: proto.ServiceSession, Op: op, Params: mustJSON(c.t, params),
	}))

	var resp proto.Response
	c.await("a response to "+op, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		r, ok := c.responses[id]
		resp = r
		return ok
	})
	if resp.Error != nil {
		c.t.Fatalf("%s refused: %s (%s)", op, resp.Error.Message, resp.Error.Code)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			c.t.Fatalf("%s result: %v", op, err)
		}
	}
}

func (c *helperConn) received(sub [16]byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.stream[sub]...)
}

// kill is the coordinator dying: the pipe carrying the connection is closed,
// and the helper's own bind is released the way its accept loop would.
func (c *helperConn) kill() {
	_ = c.toHost.Close()
	<-c.serveDone
	c.release()
}

func TestTheSessionSurvivesTheCoordinatorAndTheProcessRanAcrossTheGap(t *testing.T) {
	// A real shell, pinned rather than resolved, because the assertion is
	// about the session and not about which login shell this machine has. The
	// shell is a COMPOSITION-ROOT choice here exactly as it is in production:
	// no caller over the wire can name it (D3).
	svc := session.New(session.Options{
		Generation: "content-hash",
		Spawner:    session.NewLocalSpawner(discardLog(), session.Shell{Path: "/bin/sh", Args: []string{"-i"}}),
		Inspector:  session.NewProcFS(),
		Log:        discardLog(),
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)

	subscriber := proto.SubscriberID("0123456789abcdef0123456789abcdef")
	subRaw, err := proto.SessionBytes(string(subscriber))
	if err != nil {
		t.Fatalf("subscriber: %v", err)
	}

	// --- the first coordinator ---------------------------------------------
	first := dialHelper(t, svc)

	var spawned proto.SpawnResult
	first.request(proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &spawned)
	handle := spawned.Entry.Session
	if handle.Generation != "content-hash" {
		t.Fatalf("the handle names generation %q", handle.Generation)
	}
	if spawned.Entry.Launch.Pid == 0 {
		t.Fatal("no pid was recorded: the helper did not actually spawn anything")
	}

	var attached proto.AttachResult
	first.request(proto.OpAttach, proto.AttachParams{
		Subscriber: subscriber, Session: handle, Offset: 0, Fresh: true, RequestWrite: true,
	}, &attached)
	if !attached.Write.Granted {
		t.Fatalf("no write capability: %+v", attached.Write)
	}

	// A per-second marker, typed the way a person types it: down the DATA
	// plane, as raw bytes carrying the writer's lease (AD-1).
	first.send(proto.TypeSessionData, proto.EncodeSessionFrame(proto.SessionFrame{
		Session:    mustSessionBytes(t, handle.Session),
		Subscriber: subRaw,
		Epoch:      attached.Write.Epoch,
		Payload:    []byte("i=0; while :; do i=$((i+1)); echo MARK$i; sleep 1; done\n"),
	}))

	// Wait until the loop is demonstrably running — two markers, which is an
	// observable state and not an elapsed time.
	var seenBefore []int
	first.await("the marker loop to start", func() bool {
		seenBefore = markers(first.received(subRaw))
		return len(seenBefore) >= 2
	})
	lastBefore := seenBefore[len(seenBefore)-1]
	offsetBefore := proto.StreamOffset(len(first.received(subRaw)))

	// --- the coordinator dies ----------------------------------------------
	first.kill()

	// --- its replacement ---------------------------------------------------
	second := dialHelper(t, svc)

	// The session is still in the inventory, under the same durable handle,
	// still running. This is D1: the coordinator was replaced underneath it.
	var inv proto.SessionsResult
	second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
	if len(inv.Sessions) != 1 {
		t.Fatalf("the inventory holds %d sessions after the restart, want 1", len(inv.Sessions))
	}
	survivor := inv.Sessions[0]
	if survivor.Session != handle {
		t.Fatalf("the handle changed across the restart: %+v want %+v", survivor.Session, handle)
	}
	if survivor.Exit != nil {
		t.Fatalf("the session died with its coordinator: %+v", survivor.Exit)
	}
	if survivor.Writer != nil {
		t.Fatalf("the dead coordinator still holds the write capability: %v", survivor.Writer)
	}

	// Wait for the stream to grow far enough past where the first coordinator
	// stopped to contain markers, then FREEZE that offset: everything between
	// offsetBefore and it was produced while no coordinator was attached.
	const enoughForTwoMarkers = 3 * len("MARK123\r\n")
	var gapEnd proto.StreamOffset
	for {
		second.request(proto.OpSessions, proto.SessionsParams{}, &inv)
		gapEnd = inv.Sessions[0].Window.Written
		if gapEnd >= offsetBefore+proto.StreamOffset(enoughForTwoMarkers) {
			break
		}
		if t.Context().Err() != nil {
			t.Fatalf("the stream never grew past %d while nobody was attached", offsetBefore)
		}
	}

	// Reattach where the dead coordinator stopped. Nothing was lost: the
	// default window is far larger than a few seconds of markers, so this is
	// a resume and not a reset — which is the ordinary case D1 promises and
	// the paired positive for every reclaim assertion elsewhere.
	var again proto.AttachResult
	second.request(proto.OpAttach, proto.AttachParams{
		Subscriber: subscriber, Session: handle, Offset: offsetBefore, Fresh: false,
	}, &again)
	if again.Resume.Reset {
		t.Fatalf("reattaching lost data it did not have to: %+v", again.Resume)
	}
	if again.Resume.From != offsetBefore {
		t.Fatalf("resumed at %d, want %d", again.Resume.From, offsetBefore)
	}

	// Drain exactly the range that existed before this coordinator attached.
	gapBytes := int(gapEnd - offsetBefore) //nolint:gosec // a few seconds of markers, bounded by the loop above
	second.await("the replay of what ran while nobody was watching", func() bool {
		return len(second.received(subRaw)) >= gapBytes
	})
	gap := second.received(subRaw)[:gapBytes]

	// THE CLAUSE THAT PROVES THE FEATURE. Every marker in this range was
	// printed by a process that had no coordinator attached to it: the range
	// begins where the first one stopped receiving and ends where the window
	// stood before the second one attached.
	var ran []int
	for _, n := range markers(gap) {
		if n > lastBefore {
			ran = append(ran, n)
		}
	}
	if len(ran) < 2 {
		t.Fatalf("%d markers were produced across the gap, want at least 2 — the last one before the restart was %d and the replayed range was %q",
			len(ran), lastBefore, gap)
	}
	// Consecutive, and starting where the first coordinator left off: the
	// process did not restart, and nothing in between was invented.
	if ran[0] != lastBefore+1 {
		t.Errorf("the first marker across the gap is %d, want %d: the shell was restarted rather than resumed", ran[0], lastBefore+1)
	}
	for i := 1; i < len(ran); i++ {
		if ran[i] != ran[i-1]+1 {
			t.Errorf("markers %d and %d are not consecutive: the replayed range has a hole", ran[i-1], ran[i])
		}
	}

	second.kill()
}

func mustSessionBytes(t *testing.T, hex string) [16]byte {
	t.Helper()
	raw, err := proto.SessionBytes(hex)
	if err != nil {
		t.Fatalf("session id %q: %v", hex, err)
	}
	return raw
}
