package sessionruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

// A PROGRAM'S TITLE ENDS WITH ITS RUN (nocx-ouru5.1.1). Every case drives the
// real emulator through Ingest, the way the shell's own bytes arrive, and reads
// back what a consumer would be handed: the sticky title a frame carries. The
// window rule itself is title.go's; what these pin is the stream ORDER it is
// read from — a program's report between the 133 C output mark and the render
// fence, the shell's after it — because that order is the whole of why a
// prompt-time title is not swept away with the program that ran before it.

// fenceBytes is one render fence as the shell writes it: a nonce the runtime
// has never seen, so the sighting parks rather than joining anything.
func fenceBytes(nonce string) string {
	return "\x1b]1337;NOCX_FENCE;" + nonce + "\x07"
}

const titleTestNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// frameTitle is the title a full frame carries, decoded the way the renderer
// reads it: null means no title was ever reported, "" is an explicit clear.
func frameTitle(t *testing.T, s *Session) *string {
	t.Helper()
	raw, err := EncodeFrame(s.Snapshot())
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	var frame struct {
		State struct {
			Title *string `json:"title"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return frame.State.Title
}

func ingestTitleTest(t *testing.T, s *Session, bytes string) {
	t.Helper()
	if err := s.Ingest([]byte(bytes)); err != nil {
		t.Fatalf("ingest %q: %v", bytes, err)
	}
}

func TestAProgramsTitleGoesWhenItsCommandEnds(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	// A shell title drawn at the prompt: no run is in flight.
	ingestTitleTest(t, s, "\x1b]0;shell title\x07")
	if got := frameTitle(t, s); got == nil || *got != "shell title" {
		t.Fatalf("prompt title = %v, want the shell's own", got)
	}

	// The command runs: its output mark, then the program naming itself.
	ingestTitleTest(t, s, "\x1b]133;C\x07")
	ingestTitleTest(t, s, "\x1b]0;\u03c0 > tmp\x07")
	ingestTitleTest(t, s, "the program's output\r\n")
	if got := frameTitle(t, s); got == nil || *got != "\u03c0 > tmp" {
		t.Fatalf("the running program's title = %v, want it taken", got)
	}

	// The command ends. The tab goes back to what the shell shows — an
	// EXPLICIT clear, which is the wire's way of saying so.
	ingestTitleTest(t, s, fenceBytes(titleTestNonce)+"\x1b]133;D;0\x07\x1b]133;A\x07")
	got := frameTitle(t, s)
	if got == nil {
		t.Fatal("a title the program set was reported as 'never reported' rather than as a clear")
	}
	if *got != "" {
		t.Fatalf("title after the command ended = %q, want an explicit clear", *got)
	}

	// And the same program may set its title again on the next run: the state
	// has moved through a clear, so a consumer sees a CHANGE rather than the
	// same value twice.
	ingestTitleTest(t, s, "\x1b]133;C\x07\x1b]0;\u03c0 > tmp\x07")
	if got := frameTitle(t, s); got == nil || *got != "\u03c0 > tmp" {
		t.Fatalf("the second run's title = %v, want it taken again", got)
	}
}

func TestAShellThatSetsItsOwnTitleKeepsIt(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))

	// The program's title, inside the run.
	ingestTitleTest(t, s, "\x1b]133;C\x07\x1b]0;a program\x07run output\r\n")
	// The run ends, and the shell draws its own title at the prompt — the
	// user's prompt_command, written AFTER the fence, which is where every
	// real shell puts it.
	ingestTitleTest(t, s, fenceBytes(titleTestNonce))
	ingestTitleTest(t, s, "\x1b]0;shell title\x07\x1b]133;D;0\x07\x1b]133;A\x07")
	if got := frameTitle(t, s); got == nil || *got != "shell title" {
		t.Fatalf("title after the prompt returned = %v, want the shell's own", got)
	}

	// AND THE NEXT COMMAND DOES NOT SWEEP IT AWAY: it was set outside the run
	// window, so the fence that closes a run with no title of its own leaves
	// it alone.
	ingestTitleTest(t, s, "\x1b]133;C\x07plain output\r\n"+fenceBytes(strings.Repeat("ab", 32)))
	if got := frameTitle(t, s); got == nil || *got != "shell title" {
		t.Fatalf("title after a command that set none = %v, want the shell's own kept", got)
	}
}

func TestAFenceWithNoRunWindowInFlightKeepsTheTitle(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	ingestTitleTest(t, s, "\x1b]0;drawn at the prompt\x07")
	// A fence with nothing running behind it — a program printing one itself,
	// or a stream that carried no output mark. It locates a boundary the
	// runtime never opened, so it ends no run and clears no title.
	ingestTitleTest(t, s, fenceBytes(titleTestNonce))
	if got := frameTitle(t, s); got == nil || *got != "drawn at the prompt" {
		t.Fatalf("title after a fence with no run in flight = %v, want it kept", got)
	}
}
