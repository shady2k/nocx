package session

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// The session runtime in the helper (ADR-0066, nocx-ygxjv.12).
//
// These are the acceptance checks of the wiring, and each one is written
// against an OBSERVABLE the product produces rather than against the shape of
// the code: the bytes the session writes BACK to a program that asked a
// question, the text the emulator holds after the delivery window discarded
// the bytes, the size both ends of a commit are running at, and what happens to
// the runtime when the session ends.
//
// Nothing here waits on a duration. Every wait is on a state change — a write
// arriving, the window's produced offset advancing — with a hang limit as the
// only clock, exactly as internal/sessionruntime's own tests do.

// hangLimit is how long a wait may last before this file calls it a hang. It is
// a limit and not a schedule: no assertion here is about how long anything
// took.
const hangLimit = 20 * time.Second

// scriptedProcess is the program side of a session with no PTY and no shell: it
// hands the pump exactly the bytes a program would have written, in the chunks
// the pump asks for, and records every byte the session writes BACK to it.
//
// The back-channel is the point of the fixture. A reply to a program's question
// is invisible in every other reading of this package — it is not output, not a
// frame and not an inventory field — and it is the one thing ADR-0066 says the
// backend owes a program with nobody attached.
type scriptedProcess struct {
	// script is what the program "prints", consumed by Read across as many
	// calls as it takes; empty means the program has said everything and is
	// now waiting, like a shell at a prompt.
	script []byte

	release  chan struct{}
	done     chan struct{}
	written  chan []byte
	closeOne sync.Once

	mu      sync.Mutex
	resizes [][2]uint16
}

func newScriptedProcess(script string) *scriptedProcess {
	return &scriptedProcess{
		script:  []byte(script),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		written: make(chan []byte, 64),
	}
}

// Read serves the script and then PARKS. It never returns EOF by itself: an EOF
// here would close the window and end the pump, so a test that asserts on
// anything after the script would be asserting on a session that had already
// stopped.
func (p *scriptedProcess) Read(b []byte) (int, error) {
	p.mu.Lock()
	if len(p.script) > 0 {
		n := copy(b, p.script)
		p.script = p.script[n:]
		p.mu.Unlock()
		return n, nil
	}
	p.mu.Unlock()
	<-p.release
	return 0, io.EOF
}

// Write is the PTY's write side: what the runtime decided, in order. It
// publishes each write on a channel rather than into a slice, because the only
// question a test asks is "has it been written yet" — a slice would be a second
// record of one fact, and one of the two would eventually be read at the wrong
// moment.
func (p *scriptedProcess) Write(b []byte) (int, error) {
	payload := append([]byte(nil), b...)
	select {
	case p.written <- payload:
	default:
	}
	return len(b), nil
}

func (p *scriptedProcess) Resize(_ context.Context, cols, rows, _, _ uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, [2]uint16{cols, rows})
	return nil
}

func (p *scriptedProcess) Close() error {
	p.closeOne.Do(func() {
		close(p.release)
		close(p.done)
	})
	return nil
}

func (p *scriptedProcess) Done() <-chan struct{} { return p.done }

func (p *scriptedProcess) WaitErr() (error, bool) { return nil, false }

func (p *scriptedProcess) Pid() int { return 5150 }

func (p *scriptedProcess) Shell() string { return "/bin/scripted" }

func (p *scriptedProcess) ForegroundProcessGroup() (int, error) { return 5150, nil }

// awaitWrite waits for the next thing the session wrote to the program. The
// wait ends on the write itself; the deadline is only what turns a session that
// never answers into a failure rather than a hung test.
func (p *scriptedProcess) awaitWrite(t *testing.T) []byte {
	t.Helper()
	select {
	case w := <-p.written:
		return w
	case <-time.After(hangLimit):
		t.Fatal("the session never wrote an answer back to the program")
		return nil
	}
}

// lastResize is the size the PTY was last asked to take.
func (p *scriptedProcess) lastResize() ([2]uint16, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.resizes) == 0 {
		return [2]uint16{}, false
	}
	return p.resizes[len(p.resizes)-1], true
}

// staticSpawner is the Spawner seam: every spawn gets the process the test
// scripted, and nothing is forked.
type staticSpawner struct{ proc Process }

func (s staticSpawner) Spawn(SpawnRequest) (Process, error) { return s.proc, nil }

// newRuntimeService is the composition this file drives: the REAL service, the
// REAL spawn path (so the runtime is created where production creates it) and
// the REAL emulator, with a scripted process behind the PTY and nothing else
// replaced.
func newRuntimeService(t *testing.T, proc Process) *Service {
	t.Helper()
	svc := New(Options{
		Generation: "gen",
		Spawner:    staticSpawner{proc: proc},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(svc.Close)
	return svc
}

// spawnScripted spawns one session over the scripted process and answers the
// session row AND the host session behind it, so a test can read the runtime
// the spawn built.
//
// The call is made on a BARE context — no Bind, no attachment, no connection —
// which is the whole condition these tests exist for: a helper session's
// terminal is not created by a coordinator connecting, and it does not end when
// one leaves.
func spawnScripted(t *testing.T, proc *scriptedProcess, windowBytes int64) (*Service, *hostSession) {
	t.Helper()
	svc := newRuntimeService(t, proc)
	res := callOp[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{
		Cols:        80,
		Rows:        24,
		WindowBytes: windowBytes,
	})
	svc.mu.Lock()
	hs, ok := svc.sessions[res.Entry.Session.Session]
	svc.mu.Unlock()
	if !ok {
		t.Fatalf("spawn registered no session for %s", res.Entry.Session.Session)
	}
	return svc, hs
}

// callOp drives one operation through the seam the host dispatcher reaches, and
// decodes the result the way a caller would.
func callOp[T any](t *testing.T, svc *Service, op string, params any) T {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("%s params: %v", op, err)
	}
	res, err := svc.Call(context.Background(), op, raw)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("%s result: %v", op, err)
	}
	var decoded T
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("%s result decode: %v", op, err)
	}
	return decoded
}

// pumpRuntime builds the runtime a pump test drives, over a process that is not
// a PTY.
//
// The pump ingests before it delivers, so a fixture without a runtime would be
// measuring a pump the product never runs — and the mutex property those tests
// exist for (the pump takes no session mutex, even while the runtime is
// answering the program) is only real over a runtime that has somewhere to
// write a reply to.
func pumpRuntime(t *testing.T, proc Process) *sessionruntime.Session {
	t.Helper()
	rt, screen, err := newSessionRuntime(defaultScreen, proc, "00000000000000000000000000000000", 80, 24)
	if err != nil {
		t.Fatalf("build the session runtime: %v", err)
	}
	t.Cleanup(screen.Close)
	return rt
}

// TestAProgramIsAnsweredWithNoAttachmentAndNoClient is the epic's acceptance
// criterion, asserted at the helper: a program asks its terminal where the
// cursor is, the session answers, and NOBODY is attached — no subscriber, no
// writer lease, no connection at all (requirement 3 of nocx-ygxjv.12).
//
// The question is asked with the cursor moved to the second row, third column,
// so the answer is evidence of the RUNTIME's own state rather than of a
// constant a stub could have produced: `CSI 2;3H` then `CSI 6n`, answered
// `ESC [ 2 ; 3 R`. That answer travels the runtime's ordered write path —
// ptyTerminal.Write, which is the PTY here — and it reaches a process with no
// attachment holding the write capability, because a program's question is the
// terminal's obligation and not a user intent.
func TestAProgramIsAnsweredWithNoAttachmentAndNoClient(t *testing.T) {
	proc := newScriptedProcess("\x1b[2;3H\x1b[6n")
	_, hs := spawnScripted(t, proc, 0)

	if hs.runtime == nil {
		t.Fatal("spawn registered a session with no runtime: the session's terminal was not created with it")
	}
	if got := hs.runtime.Availability(); got != sessionruntime.AvailabilityAvailable {
		t.Fatalf("the session's runtime is %v at spawn", got)
	}

	got := string(proc.awaitWrite(t))
	if want := "\x1b[2;3R"; got != want {
		t.Fatalf("the program was answered %q, want %q", got, want)
	}

	// The write capability was never granted to anybody, which is what makes
	// the answer above the runtime's rather than an attachment's.
	hs.mu.Lock()
	writer := hs.writer
	attachments := len(hs.attachments)
	hs.mu.Unlock()
	if writer != nil || attachments != 0 {
		t.Fatalf("the test attached something: writer=%v attachments=%d", writer, attachments)
	}
}

// TestTheWindowDiscardsOutputTheRuntimeStillHolds asserts requirement 2: the
// emulator's ingest is LOSSLESS while the delivery window is the one lossy case
// (AD-10's hole, D8's capacity reclaim), so bytes the window has thrown away
// are bytes the session's own terminal state still holds.
//
// The window is spawned at the helper's floor (any smaller request is clamped
// up), and the program then prints more than the window can hold — with the
// marker on the FIRST row, where it stays because the flood overwrites the
// second row in place rather than scrolling. Nothing is attached, so every byte
// the window reclaims is genuinely gone from the delivery path: a reader
// standing at offset 0 is told the base instead of being served it.
//
// This is also the wiring's order asserted from the outside: an emulator fed
// from the delivery path (or from a subscriber's cursor) would have seen only
// what the window kept.
func TestTheWindowDiscardsOutputTheRuntimeStillHolds(t *testing.T) {
	const marker = "MARKER-ON-SCREEN"
	// Row 2, column 1, then a full row overwritten in place: no newline is ever
	// printed, so the screen never scrolls and the marker stays on row 1.
	const fillerRepeat = 4200
	script := marker + "\r\n\x1b[2;1H" + strings.Repeat(strings.Repeat("y", 78)+"\r", fillerRepeat)

	proc := newScriptedProcess(script)
	_, hs := spawnScripted(t, proc, 1)

	// Wait for the whole script to have been moved. The observable is the
	// window's produced offset — each write wakes a waiter — so nothing here
	// asserts how long the flood took.
	want := proto.StreamOffset(len(script))
	deadline := time.Now().Add(hangLimit)
	for {
		changed := hs.win.changed()
		_, written := hs.win.span()
		if written >= want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session moved %d of %d scripted bytes", written, want)
		}
		select {
		case <-changed:
		case <-time.After(hangLimit):
			t.Fatalf("the session stopped moving output at %d of %d bytes", written, want)
		}
	}

	// The window threw the beginning away...
	base, written := hs.win.span()
	if base <= proto.StreamOffset(len(marker)) {
		t.Fatalf("the window retained the whole stream (base %d of %d): this test proves nothing "+
			"about what the emulator holds when the window discards", base, written)
	}
	if _, resume := hs.win.read(0); !resume.Reset {
		t.Fatal("a reader at offset 0 was served instead of reset, so the window discarded nothing")
	}

	// ...and the session's own screen still has what the window discarded.
	snap := hs.runtime.Snapshot()
	if snap.Availability != sessionruntime.AvailabilityAvailable {
		t.Fatalf("the session's runtime is %v after ingesting the stream", snap.Availability)
	}
	if !strings.Contains(string(snap.Screen), marker) {
		t.Fatalf("the emulator does not hold %q after %d bytes (%d discarded by the window):\n%s",
			marker, written, base, snap.Screen)
	}
}

// TestResizeReachesThePtyAndTheScreenTogether asserts requirement 4: one
// geometry commit moves BOTH ends, so what the program is running on and what
// the session describes cannot disagree (the defect that leaves every cell
// after a column wrong).
//
// All three readings are taken after the operation returns, and each is a
// different fact: the size the PTY took (the program's SIGWINCH), the size the
// emulator is running at, and the commit in force.
func TestResizeReachesThePtyAndTheScreenTogether(t *testing.T) {
	proc := newScriptedProcess("")
	svc, hs := spawnScripted(t, proc, 0)

	callOp[proto.ResizeResult](t, svc, proto.OpResize, proto.ResizeParams{
		Session: hs.id,
		Cols:    100,
		Rows:    30,
	})

	size, ok := proc.lastResize()
	if !ok {
		t.Fatal("the geometry commit never reached the PTY")
	}
	if size != [2]uint16{100, 30} {
		t.Fatalf("the PTY took %dx%d, want 100x30", size[0], size[1])
	}

	screenSize, err := hs.screen.Geometry()
	if err != nil {
		t.Fatalf("read the screen's geometry: %v", err)
	}
	if screenSize.Cols != 100 || screenSize.Rows != 30 {
		t.Fatalf("the screen runs at %dx%d, want 100x30", screenSize.Cols, screenSize.Rows)
	}

	if commit := hs.runtime.Geometry(); commit.Geometry.Cols != 100 || commit.Geometry.Rows != 30 {
		t.Fatalf("the commit in force is %+v, want 100x30", commit.Geometry)
	}

	// A size no terminal can have is REFUSED rather than applied to one end:
	// the commit publishes nothing and both ends stay at 100x30.
	raw, err := json.Marshal(proto.ResizeParams{Session: hs.id, Cols: 0, Rows: 30})
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if _, err := svc.Call(context.Background(), proto.OpResize, raw); err == nil {
		t.Fatal("a zero-column resize was accepted")
	}
	if size, _ := proc.lastResize(); size != [2]uint16{100, 30} {
		t.Fatalf("the refused resize reached the PTY: %v", size)
	}
	if commit := hs.runtime.Geometry(); commit.Geometry.Cols != 100 {
		t.Fatalf("a refused commit moved the size to %+v", commit.Geometry)
	}
}

// TestTheRuntimeAndItsScreenEndWithTheSession asserts requirement 5's closing
// end: the runtime and its emulator live as long as the session and are
// destroyed with it — and, at the opening end, that they were created before a
// single byte was read (the script was answered from the runtime's state, which
// is only possible if the runtime existed when the pump started).
//
// The destruction is asserted by USING the pieces afterwards rather than by
// inspecting a flag: a closed screen refuses a read, and a failed runtime
// refuses an ingest, which are the two things a leaked emulator would still do.
func TestTheRuntimeAndItsScreenEndWithTheSession(t *testing.T) {
	proc := newScriptedProcess("")
	svc, hs := spawnScripted(t, proc, 0)

	callOp[proto.CloseSessionResult](t, svc, proto.OpCloseSession, proto.CloseSessionParams{Session: hs.id})

	if _, err := hs.screen.Geometry(); err == nil {
		t.Fatal("the emulator outlived the session it belonged to")
	}
	if got := hs.runtime.Availability(); got != sessionruntime.AvailabilityUnavailable {
		t.Fatalf("the runtime is %v after the session ended, want %v", got, sessionruntime.AvailabilityUnavailable)
	}
	if err := hs.runtime.Ingest([]byte("x")); err == nil {
		t.Fatal("the runtime accepted bytes after the session it belonged to ended")
	}
	svc.mu.Lock()
	_, still := svc.sessions[hs.id.Session]
	svc.mu.Unlock()
	if still {
		t.Fatal("the session row outlived close-session")
	}
}
