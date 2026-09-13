package sessionruntime

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator/ghostty"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
)

// The epic's success criterion, executable (nocx-ygxjv.9): a PROGRAM runs on a
// real PTY, the runtime directs it, and afterwards what is extracted is the
// screen it drew, the mode it set and the reply it received — with NO CLIENT
// ATTACHED, which is the whole point of the runtime living beside the process.
//
// Nothing here is mocked below the contract. The terminal is internal/pty's
// real one, the emulator is libghostty-vt behind its port, and the program is a
// shell running a script. The bytes the runtime decides go through a real line
// discipline; what the program prints back is the only evidence used.

// hangLimit is how long a wait may last before the test calls it a hang. It is
// a limit and not a schedule: every wait ends when an observable state change
// arrives, and nothing here is asserted on how long that took.
const hangLimit = 20 * time.Second

// realProgram is the program: a shell that exercises the four things a session
// runtime owes a program and no client can see.
//
//   - It turns the line discipline raw, so the runtime's bytes arrive as bytes
//     rather than as a line the shell is editing.
//   - It reads THREE bytes and hex-encodes them, before and after enabling
//     application cursor keys — so the same intent is two different sequences
//     (ESC [ A, then ESC O A) and the difference is a mode THE PROGRAM SET.
//   - It asks where its cursor is (DSR) and hex-encodes the answer, so the
//     reply is evidence of the runtime's own state rather than of a constant.
//   - It waits for one more byte and then reports its terminal's size, which is
//     how a resize's EXTERNAL effect — the program learns the size it is
//     running on — becomes observable.
const realProgram = `
stty -icanon -echo min 1 time 0
printf 'READY\n'
first=$(dd bs=1 count=3 2>/dev/null | od -An -tx1 | tr -d ' \n')
printf 'KEY1:%s\n' "$first"
printf '\033[?1h'
printf 'KEYS-ON\n'
second=$(dd bs=1 count=3 2>/dev/null | od -An -tx1 | tr -d ' \n')
printf 'KEY2:%s\n' "$second"
printf '\033[1;1H\033[6n'
answer=$(dd bs=1 count=6 2>/dev/null | od -An -tx1 | tr -d ' \n')
printf 'REPLY:%s\n' "$answer"
dd bs=1 count=1 2>/dev/null >/dev/null
printf 'SIZE:%s\n' "$(stty size)"
`

// ptyTerminal is the composition root's adapter — internal/pty.Pty behind the
// contract's Terminal — written here because the root is another bead's file.
// It is the whole of the adapter: the bytes, and the size, and nothing else.
type ptyTerminal struct{ lp *pty.LocalPty }

var _ Terminal = (*ptyTerminal)(nil)

func (t *ptyTerminal) Write(p []byte) (int, error) { return t.lp.Write(p) }

func (t *ptyTerminal) Resize(g Geometry) error {
	cols, err := ptyDimension(g.Cols)
	if err != nil {
		return err
	}
	rows, err := ptyDimension(g.Rows)
	if err != nil {
		return err
	}
	width, err := ptyDimension(g.CellWidthPx * g.Cols)
	if err != nil {
		return err
	}
	height, err := ptyDimension(g.CellHeightPx * g.Rows)
	if err != nil {
		return err
	}
	return t.lp.Resize(context.Background(), cols, rows, width, height)
}

// ptySize is the cell grid and the pixel size a real pty is started at, each
// figure checked against the width the kernel's own ioctl carries.
func ptySize(g Geometry) (cols, rows, xPixels, yPixels uint16, err error) {
	if cols, err = ptyDimension(g.Cols); err != nil {
		return 0, 0, 0, 0, err
	}
	if rows, err = ptyDimension(g.Rows); err != nil {
		return 0, 0, 0, 0, err
	}
	if xPixels, err = ptyDimension(g.CellWidthPx * g.Cols); err != nil {
		return 0, 0, 0, 0, err
	}
	if yPixels, err = ptyDimension(g.CellHeightPx * g.Rows); err != nil {
		return 0, 0, 0, 0, err
	}
	return cols, rows, xPixels, yPixels, nil
}

// ptyDimension converts a geometry figure to the width the kernel's own ioctl
// carries. The bound is checked rather than assumed: [Geometry.Valid] allows a
// pixel size a uint16 cannot hold, and a silent wrap would resize the terminal
// to a number nobody asked for.
func ptyDimension(n int) (uint16, error) {
	if n < 0 || n > math.MaxUint16 {
		return 0, fmt.Errorf("pty: %d does not fit the kernel's uint16", n)
	}
	return uint16(n), nil
}

// This test carries two things the six verification cases in
// realpty_cases_test.go do not: a key arriving encoded against the mode the
// PROGRAM set (steps 2 and 4) and the reply the program actually asked for
// (step 5). Each is written the way those six are — the handling it turns on,
// and what removing that handling does — and every removal below was run by
// hand against this test before it was written down:
//
//   - The key encoded against the mode the program set. Handling:
//     Session.encode's IntentKindKey branch, which asks the emulator to encode
//     the key from the terminal's own state. Removal (encode ESC [ A here
//     instead of asking): step 4's KEY2 reads 1b5b41 where the program is owed
//     1b4f41, and the wait for it never sees the value.
//   - The reply the program actually asked for. Handling: the write of the
//     emulator's replies on the ordered path in Session.Ingest. Removal (write
//     replies[:0]): the program's DSR is never answered, its six-byte read
//     never returns, and the wait never sees REPLY:1b5b313b3152.
//
// Step 6's resize is not one of the six: what it proves is the commit's
// EXTERNAL effect — the program learns the size it is running at — which is the
// part of a geometry commit that no rollback can take back.
func TestAProgramRunsOnARealPTYDirectedByTheRuntimeWithNoClient(t *testing.T) {
	prog := startProgram(t, realProgram, harnessGeometry(80, 24))
	s, pump, ctrl := prog.s, prog.done, prog.ctrl

	// 1. The program is up and has turned its terminal raw.
	waitForScreen(t, s, prog.changed, "READY")

	// 2. A key, before the program set any mode. The intent carries the KEY
	// ("Up"); what it becomes is the runtime's decision, and here the program
	// reads back the legacy sequence.
	press(t, s, ctrl, "Up")
	waitForScreen(t, s, prog.changed, "KEY1:1b5b41")

	// 3. The program enables application cursor keys and says so, which is the
	// observable state the next step waits on.
	waitForScreen(t, s, prog.changed, "KEYS-ON")

	// 4. The SAME intent, and the program reads back the other sequence: the
	// mode the program set is what decided the bytes, and no client was
	// involved in the decision (ADR-0066, AD-1 as amended).
	press(t, s, ctrl, "Up")
	waitForScreen(t, s, prog.changed, "KEY2:1b4f41")

	// 5. The program asked where its cursor was and got the runtime's answer:
	// ESC [ 1 ; 1 R, hex-encoded by the program itself. The cursor was put at
	// the home position by the program, so the answer is the runtime's STATE
	// and not a constant.
	waitForScreen(t, s, prog.changed, "REPLY:1b5b313b3152")

	// 6. A size the client reports and the runtime decides. The commit's
	// external effect is that the PROGRAM learns it, which is what cannot be
	// rolled back: the PTY is resized and the program is signalled.
	if reportErr := s.ReportGeometry(harnessGeometry(100, 30)); reportErr != nil {
		t.Fatalf("the client reports the size it can show: %v", reportErr)
	}
	commit, err := s.CommitGeometry(harnessGeometry(100, 30))
	if err != nil {
		t.Fatalf("commit the size the client reported: %v", err)
	}
	if commit.Geometry != harnessGeometry(100, 30) {
		t.Fatalf("the commit in force is %+v, want 100x30", commit.Geometry)
	}
	// The one byte the program is waiting for, sent as an intent like any other.
	run(t, s, ctrl, IntentKindText, []byte("g"))
	waitForScreen(t, s, prog.changed, "SIZE:30 100")

	// 7. And no client was ever attached: everything above was the runtime's,
	// with nobody watching.
	if got := s.Consumers().Attached(); len(got) != 0 {
		t.Fatalf("%d consumers were attached to a session driven with no client", len(got))
	}

	// The pump closes its report channel when the program's output ends, so
	// this waits on that and reads the outcome it left behind: a clock here
	// would be a duration where an event exists.
	for range prog.changed {
	}
	if err := <-pump; err != nil {
		t.Fatalf("feeding the program's output into the runtime failed: %v", err)
	}
}

// programSession is one program on a real pty with the runtime that directs
// it: the session, the authority a client's intents are admitted under, and the
// end of the pump that feeds the program's output in. Every test in this file
// and in realpty_cases_test.go is built from it, so a program is started one
// way and the differences between the tests are the things they are about.
type programSession struct {
	t    *testing.T
	s    *Session
	ctrl Control
	done <-chan error
	// changed reports the runtime having taken in more of the program's
	// output, and closes when the program's output ends. A wait selects on it
	// rather than reading the screen on a timer: every assertion here is about
	// a state the runtime reached, so the thing to wait for is the state
	// having been reached, and the screen is checked after each report.
	changed <-chan struct{}
}

// startProgram starts `sh -c script` on a real pty of the given size, builds
// the runtime over it and the real emulator, and begins feeding the program's
// output in. The script is expected to begin with rawPreamble (or do the
// equivalent), because each of these programs is driven with bytes.
func startProgram(t *testing.T, script string, g Geometry) *programSession {
	t.Helper()
	cols, rows, xPixels, yPixels, err := ptySize(g)
	if err != nil {
		t.Fatalf("the size a real pty is started at: %v", err)
	}
	lp, err := pty.NewLocal(log.NewSlogAdapter(nil), pty.Config{
		Command: "/bin/sh",
		Args:    []string{"-c", script},
		Cols:    cols,
		Rows:    rows,
		XPixel:  xPixels,
		YPixel:  yPixels,
	})
	if err != nil {
		t.Fatalf("start a real shell on a real pty: %v", err)
	}
	t.Cleanup(func() { _ = lp.Close() })

	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the emulator the runtime is constructed over: %v", err)
	}
	t.Cleanup(screen.Close)

	s, err := New(Config{
		Incarnation:  Incarnation{Session: "a-real-session", Generation: 1},
		Geometry:     g,
		Terminal:     &ptyTerminal{lp: lp},
		Emulator:     screen,
		Completeness: CompletenessComplete,
	})
	if err != nil {
		t.Fatalf("build the runtime over the real pair: %v", err)
	}
	done, changed := feedFrom(t, lp, s)
	return &programSession{t: t, s: s, ctrl: sessionControl(t, s), done: done, changed: changed}
}

func (p *programSession) screen() string { return string(p.s.Snapshot().Screen) }

// wait blocks until the screen holds want, or fails at the hang limit.
func (p *programSession) wait(want string) { waitForScreen(p.t, p.s, p.changed, want) }

// send admits and executes one intent and answers where it got to, WITHOUT
// failing the test: the mouse and paste cases both need an intent that is
// REFUSED (a mouse event no program asked for), which is a result and not a
// failure.
func (p *programSession) send(kind IntentKind, payload []byte) (IntentState, error) {
	p.t.Helper()
	id := admitted(p.t, p.s, p.ctrl, kind, payload)
	gotID, state, err := p.s.Execute()
	if gotID != id {
		p.t.Fatalf("executing the admitted intent answered for id %d, want %d", gotID, id)
	}
	return state, err
}

// mustSend is send for the intent that has to reach the program.
func (p *programSession) mustSend(kind IntentKind, payload []byte) {
	p.t.Helper()
	if state, err := p.send(kind, payload); err != nil || state != IntentStateExecuted {
		p.t.Fatalf("sending a %d intent with payload %q: state=%s err=%v, want executed",
			kind, payload, intentStateName(state), err)
	}
}

// typed is one Text intent, which is how these tests send the single byte a
// program waits on before it goes on.
func (p *programSession) typed(text string) { p.t.Helper(); p.mustSend(IntentKindText, []byte(text)) }

// feedFrom pumps the program's output into the runtime, which is what a
// carrier does in the product: the runtime never reads the PTY itself, because
// the bytes it decides and the bytes it ingests are the same ordered path.
//
// It answers two channels and both are about the same event. done carries the
// end of the pump — nil when the program simply exited, an error when the
// runtime refused what it was handed. changed reports each read that reached
// the runtime and is CLOSED when the pump ends, so a test waits on the
// runtime's state changing rather than on a clock, and a wait that can no
// longer be satisfied is told so instead of timing out.
//
// The report is a non-blocking send into a one-slot channel, which is exact
// rather than lossy: the screen is checked after every report and it is the
// CURRENT screen, so a report that arrives while one is already pending
// carries nothing the pending one does not already cause to be re-read.
func feedFrom(t *testing.T, lp *pty.LocalPty, s *Session) (<-chan error, <-chan struct{}) {
	t.Helper()
	done := make(chan error, 1)
	changed := make(chan struct{}, 1)
	go func() {
		defer close(changed)
		buf := make([]byte, 32<<10)
		for {
			n, err := lp.Read(buf)
			if n > 0 {
				if ingestErr := s.Ingest(append([]byte(nil), buf[:n]...)); ingestErr != nil {
					done <- ingestErr
					return
				}
				select {
				case changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				// The program is gone: that is the end of this pump and not a
				// failure of it.
				done <- nil
				return
			}
		}
	}()
	return done, changed
}

// press admits and executes one key, and fails the test if it did not reach
// the program.
func press(t *testing.T, s *Session, ctrl Control, key string) {
	t.Helper()
	run(t, s, ctrl, IntentKindKey, []byte(key))
}

// run admits and executes one intent, and fails the test if it did not reach
// the program. It is the only way these tests send input, so an intent that is
// refused or fails is a test failure rather than something the test could
// describe around.
func run(t *testing.T, s *Session, ctrl Control, kind IntentKind, payload []byte) {
	t.Helper()
	_, state, err := admittedThenExecuted(t, s, ctrl, kind, payload)
	if err != nil || state != IntentStateExecuted {
		t.Fatalf("sending a %d intent with payload %q: state=%s err=%v, want executed",
			kind, payload, intentStateName(state), err)
	}
}

// waitForScreen waits until the screen holds want, and fails the test at the
// hang limit. The condition is an observable state change — the screen the
// runtime hands out, re-read after each report that it took more of the
// program's output in — and nothing here says how long anything took: the hang
// limit only ends a wait that can no longer be satisfied, and a pump that has
// ended says so rather than leaving the wait to expire.
func waitForScreen(t *testing.T, s *Session, changed <-chan struct{}, want string) {
	t.Helper()
	deadline := time.NewTimer(hangLimit)
	defer deadline.Stop()
	ended := false
	for {
		if strings.Contains(string(s.Snapshot().Screen), want) {
			return
		}
		if ended {
			t.Fatalf("the program's output ended and the screen never came to hold %q; it reads:\n%s", want, s.Snapshot().Screen)
		}
		select {
		case _, ok := <-changed:
			if !ok {
				ended = true
			}
		case <-deadline.C:
			t.Fatalf("the screen never came to hold %q within %s; it reads:\n%s", want, hangLimit, s.Snapshot().Screen)
		}
	}
}
