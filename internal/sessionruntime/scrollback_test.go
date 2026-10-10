package sessionruntime

// The budget path is judged at this layer the way a session reaches it: the
// runtime is constructed over the REAL emulator (the tests hold the screen
// they handed in, which is how the retained history is read back), and what
// is asserted is what a caller of the runtime can see — the budget applied,
// the emulator's own report clean after it, and the named refusals.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/emulator/ghostty"
)

// realScreenRuntime builds a runtime over an UNWRAPPED real emulator, the
// production shape: the helper's newSessionRuntime passes the bare screen,
// and it is the bare screen that carries the scrollback capability. (The
// schedules' realRuntime wraps the screen in an instrument, which is honest
// for what it measures and carries no budget — the refusal test below uses
// exactly that.)
func realScreenRuntime(t *testing.T) (*Session, emulator.Terminal) {
	t.Helper()
	g := harnessGeometry(80, 24)
	term := newHarnessTerminal(g)
	screen, err := ghostty.New(g)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	t.Cleanup(screen.Close)
	rt, err := New(Config{
		Incarnation:  Incarnation{Session: "S", Generation: 1},
		Geometry:     g,
		Terminal:     term,
		Emulator:     screen,
		Completeness: CompletenessComplete,
		Replies:      directReplySink{term: term},
	})
	if err != nil {
		t.Fatalf("build the runtime: %v", err)
	}
	return rt, screen
}

func numberedLines(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "L%05d\r\n", i)
	}
	return sb.String()
}

// The budget lands: the emulator retains the budget's promise after output
// far past it, and the next feed reports cleanly — the application re-based
// the emulator's own measurement, so the prune it took is not read as a
// loss.
func TestApplyScrollbackAppliesThroughTheRealEmulator(t *testing.T) {
	rt, screen := realScreenRuntime(t)

	if err := rt.Ingest([]byte(numberedLines(3000))); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := screen.DepartedRows(); err != nil {
		t.Fatalf("precondition: the first feed reported a loss: %v", err)
	}
	page, err := screen.HistoryRows(0, 0)
	if err != nil {
		t.Fatalf("read the retained total: %v", err)
	}
	if page.Total < 2900 {
		t.Fatalf("precondition: only %d rows retained before the budget", page.Total)
	}

	if err = rt.ApplyScrollback(1000); err != nil {
		t.Fatalf("apply the budget: %v", err)
	}
	page, err = screen.HistoryRows(0, 0)
	if err != nil {
		t.Fatalf("read the retained total: %v", err)
	}
	// At least the ask (the adapter applies it one page wide) and within
	// one page of it — the promise the setting's screen makes, at this
	// geometry one page is 512 rows.
	if page.Total < 1000 || page.Total > 1000+512 {
		t.Fatalf("after the budget the emulator retains %d rows, want within one page of 1000", page.Total)
	}

	if err := rt.Ingest([]byte(numberedLines(50))); err != nil {
		t.Fatalf("ingest after the budget: %v", err)
	}
	if _, err := screen.DepartedRows(); err != nil {
		t.Fatalf("the feed after the budget reported a loss: %v", err)
	}
}

// An emulator that does not carry the capability refuses by name: the
// schedules' instrumented screen is exactly such a value, and a caller
// reading a silent success from it would believe a budget is in force that
// nothing is keeping.
func TestApplyScrollbackRefusesAnEmulatorWithoutTheCapability(t *testing.T) {
	rt, _, _ := realRuntime(t)
	if err := rt.ApplyScrollback(1000); !errors.Is(err, emulator.ErrUnsupported) {
		t.Fatalf("a screen with no budget capability answered %v, want ErrUnsupported", err)
	}
}

// An ended session has no terminal to configure.
func TestApplyScrollbackRefusesAnEndedSession(t *testing.T) {
	rt, _ := realScreenRuntime(t)
	if err := rt.Fail("test over"); err != nil {
		t.Fatalf("end the session: %v", err)
	}
	if err := rt.ApplyScrollback(1000); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an ended session answered %v, want ErrUnavailable", err)
	}
}
