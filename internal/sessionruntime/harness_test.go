package sessionruntime

import (
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// The harness (nocx-ygxjv.9).
//
// The contract's schedules judge a runtime by being constructed over
// INSTRUMENTS (contract.go, TerminalInstrument and EmulatorInstrument), and
// they must judge the real runtime the same way they judge the model — that is
// the whole point of nocx-ygxjv.6. So the runtime these tests build is the
// REAL one: the emulator is libghostty-vt behind its port, the terminal is a
// terminal this test can read and make fail, and both are wrapped so that the
// three things a schedule has to see — the bytes that reached the program,
// whether a resize failed, and the size each side is running at — are read at
// the boundary rather than from the runtime's own account of itself.
//
// The record belongs to the WRAPPER and never to the product: no shipped
// adapter keeps a log of the keys it forwards, and a real tty does not refuse a
// size because a test said so.

// errHarnessResize and errHarnessWrite are what the harness's refusals are. A
// real side's refusal is its own error — internal/pty's, the emulator's — and
// the runtime propagates whichever it is: the refusal is the side's to name and
// not this package's to invent.
var (
	errHarnessResize = errors.New("sessionruntime harness: the size was refused")
	errHarnessWrite  = errors.New("sessionruntime harness: the write failed")
)

// harnessGeometry is the size every terminal in these tests is built and
// resized at: 80x24 cells of 10x20 pixels, which is the size the model's own
// instruments start at and the size a PTY reports on an ordinary machine.
func harnessGeometry(cols, rows int) Geometry {
	return Geometry{Cols: cols, Rows: rows, CellWidthPx: 10, CellHeightPx: 20}
}

// harnessTerminal is the terminal side of the boundary: it records what it was
// handed, it runs at a size it reports, and it can be told to refuse — both a
// resize and a write, because the write is a failure path the runtime owes
// handling for and a real tty performs it whenever the program is gone.
//
// It is the contract's [TerminalInstrument] BY IMPLEMENTATION (Written, Size,
// RefuseResize, AcceptResize) and not by declaration alone: the schedules
// assert the capability at the boundary they read it through.
type harnessTerminal struct {
	mu sync.Mutex

	writes [][]byte
	size   Geometry
	refuse bool

	// failWrites makes the next N writes fail with errHarnessWrite. It is how
	// a REPLY write — the emulator's own answer to a program's size query — is
	// made to fail on its own, without touching the resize that produced it.
	failWrites int
	// writeErr makes every write fail with this error, which is how a terminal
	// that is gone behaves.
	writeErr error
	// shortBy makes the next write take this many bytes fewer than it was
	// handed: the part-way failure nobody may report as delivered.
	shortBy int
}

var (
	_ Terminal           = (*harnessTerminal)(nil)
	_ TerminalInstrument = (*harnessTerminal)(nil)
)

func newHarnessTerminal(g Geometry) *harnessTerminal {
	return &harnessTerminal{size: g}
}

func (h *harnessTerminal) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failWrites > 0 {
		h.failWrites--
		return 0, errHarnessWrite
	}
	if h.writeErr != nil {
		return 0, h.writeErr
	}
	if h.shortBy > 0 {
		taken := len(p) - h.shortBy
		if taken < 0 {
			taken = 0
		}
		h.shortBy = 0
		h.writes = append(h.writes, append([]byte(nil), p[:taken]...))
		return taken, nil
	}
	h.writes = append(h.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (h *harnessTerminal) Resize(g Geometry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refuse {
		return errHarnessResize
	}
	h.size = g
	return nil
}

// Written is what reached the terminal, in order, as a copy.
func (h *harnessTerminal) Written() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]byte, 0, len(h.writes))
	for _, w := range h.writes {
		out = append(out, append([]byte(nil), w...))
	}
	return out
}

// Size is the size the terminal is running at, which is a different fact from
// the commit in force and the one a refusal is judged against.
func (h *harnessTerminal) Size() Geometry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.size
}

func (h *harnessTerminal) RefuseResize() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuse = true
}

func (h *harnessTerminal) AcceptResize() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuse = false
}

// failNextWrites makes the next n writes fail, which is how a REPLY write is
// made to fail on its own — without touching the resize that produced it, and
// without a terminal that is gone for every other purpose.
func (h *harnessTerminal) failNextWrites(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failWrites = n
}

// acceptWrites ends every failure the two toggles above began.
func (h *harnessTerminal) acceptWrites() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failWrites = 0
	h.writeErr = nil
	h.shortBy = 0
}

// shortNextWrite makes the next write take one byte fewer than it was handed.
func (h *harnessTerminal) shortNextWrite(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shortBy = n
}

// harnessEmulator is the screen side: the real emulator, plus the refusal and
// the size the schedules judge a geometry commit by. It embeds the port, so
// the schedule-facing reads (Row, Cell, Screen, Ingest, EncodeKey, Effects,
// Paste, Mouse, Focus) are the REAL adapter's and nothing here stands between
// a schedule and the screen it is judging.
type harnessEmulator struct {
	emulator.Terminal

	mu      sync.Mutex
	refuse  bool
	replies []byte
}

var (
	_ emulator.Terminal  = (*harnessEmulator)(nil)
	_ EmulatorInstrument = (*harnessEmulator)(nil)
)

func (h *harnessEmulator) Resize(g emulator.Geometry) ([]byte, error) {
	h.mu.Lock()
	refuse := h.refuse
	h.mu.Unlock()
	if refuse {
		return nil, errHarnessResize
	}
	replies, err := h.Terminal.Resize(g)
	h.mu.Lock()
	scripted := h.replies
	h.mu.Unlock()
	if scripted != nil {
		return scripted, err
	}
	return replies, err
}

// Size is the size the emulator is running at, read from the emulator itself
// rather than kept beside it: one fact, one owner.
func (h *harnessEmulator) Size() Geometry {
	g, err := h.Terminal.Geometry()
	if err != nil {
		return Geometry{}
	}
	return g
}

func (h *harnessEmulator) RefuseResize() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuse = true
}

func (h *harnessEmulator) AcceptResize() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuse = false
}

// answerResizeWith is what stands in for a report the emulator produces and the
// runtime must deliver: the in-band size report a program that enabled mode
// 2048 is owed when its terminal is resized. It is set here because the report
// is the PORT's answer and what this runtime owes is what it does with one.
func (h *harnessEmulator) answerResizeWith(replies []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replies = replies
}

// realRuntime builds the runtime the schedules judge: the contract's ports,
// with a REAL emulator behind the screen and a scripted terminal behind the
// write boundary, both wrapped in the instruments a schedule reads the
// boundary through.
//
// The incarnation and the completeness are stated rather than assumed: a
// runtime is told which session it is (the composition root mints that) and
// what the caller has established about the stream, and the value that means
// "nothing yet" — CompletenessUnknown — is the zero one, which is why an
// ordinary test says [CompletenessComplete] out loud.
func realRuntime(t *testing.T, opts ...func(*Config)) (*Session, *harnessTerminal, *harnessEmulator) {
	t.Helper()
	g := harnessGeometry(80, 24)
	term := newHarnessTerminal(g)
	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the emulator this runtime is constructed over: %v", err)
	}
	t.Cleanup(screen.Close)
	emu := &harnessEmulator{Terminal: screen}

	cfg := Config{
		Incarnation:  Incarnation{Session: "S", Generation: 1},
		Geometry:     g,
		Terminal:     term,
		Emulator:     emu,
		Completeness: CompletenessComplete,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("build the runtime under test: %v", err)
	}
	return s, term, emu
}
