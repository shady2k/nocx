package sessionruntime

import (
	"fmt"
	"strings"
	"testing"
)

// The output-start mark's join (nocx-2v80t.3.12): the FIRST OSC 133 C
// sighted inside an interval locates where that interval's own output
// begins. Everything above the cursor at that instant — a prompt, and an
// app-submitted command's own echoed line, wrapped or not — is this
// interval's own prefix and is never streamed or stored as output; a later
// C within the same interval carries no meaning at all (only the first
// counts); and with no C ever sighted, an interval behaves exactly as
// before this bead — everything that departs is output.
//
// Every test drives the real emulator directly through Session.Ingest, the
// way the shell's own bytes would arrive, and reads back what actually
// streamed (recordingRowStream) or what a sealed record's own closing
// screen holds — never what the code was written to do.

// collectStreamedText gathers every row's text ever handed to OutputRows,
// in the order it streamed.
func collectStreamedText(rs *recordingRowStream) []string {
	var out []string
	for _, e := range rs.snapshot() {
		if e.kind != "rows" {
			continue
		}
		for _, r := range e.rows {
			out = append(out, streamRowText(r))
		}
	}
	return out
}

func containsText(texts []string, want string) bool {
	for _, t := range texts {
		if t == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The first interval of a session has no prior fence to have installed a
// suppression window from at all — the gap this bead's C-sighting exists to
// close. The echoed command line wraps across two physical rows (a long
// app-submitted command on a narrow pane), terminated by the user's own
// Enter before the shell's preexec ever runs; the first C sighted locates
// the cursor's fresh row as where real output starts, protecting both
// wrapped rows above it.
// ---------------------------------------------------------------------------

func TestTheFirstIntervalsWrappedEchoIsNotStoredAsOutput(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(20, 24))

	echoed := "1234567890123456789012345" // 25 chars on a 20-col pane: two rows
	echoRow0, echoRow1 := echoed[:20], echoed[20:]
	if err := s.Ingest([]byte(echoed + "\r\n")); err != nil {
		t.Fatalf("ingest the echoed, wrapped command line: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the output mark: %v", err)
	}
	obsFeed(t, s, 0, 30) // the command's own real output; enough to scroll the echo off

	nonce := obsNonce(0x22)
	s.Completed(s.Incarnation(), nonce, 0)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s%s\x07", fenceMarkerPrefix, strings.Repeat("22", 32))
	if err := s.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}

	streamed := collectStreamedText(rs)
	if containsText(streamed, echoRow0) || containsText(streamed, echoRow1) {
		t.Fatalf("the wrapped echo (%q / %q) streamed as output: %v", echoRow0, echoRow1, streamed)
	}
	rec, ok := s.ObservationFor(nonce)
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	for _, row := range rec.Closing.Lines {
		if text := obsRowText(row); text == echoRow0 || text == echoRow1 {
			t.Fatalf("the closing screen holds the echo (%q): it was never suppressed", text)
		}
	}
	if !containsText(streamed, "L000000") {
		t.Fatalf("genuine output never streamed at all: %v", streamed)
	}
}

// ---------------------------------------------------------------------------
// A multi-line command — explicit newlines in the echoed text, not merely a
// wrap — is protected the same way: every one of its own lines sits above
// the cursor's fresh row when the first C is sighted.
// ---------------------------------------------------------------------------

func TestAMultiLineCommandsEchoIsNotStoredAsOutput(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	lines := []string{"for i in 1 2 3; do", "  echo \"line $i\"", "done"}
	if err := s.Ingest([]byte(strings.Join(lines, "\r\n") + "\r\n")); err != nil {
		t.Fatalf("ingest the multi-line echoed command: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the output mark: %v", err)
	}
	obsFeed(t, s, 0, 30)

	nonce := obsNonce(0x33)
	s.Completed(s.Incarnation(), nonce, 0)
	if err := s.Ingest([]byte(fenceMarkerPrefix + strings.Repeat("33", 32) + "\x07")); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}

	streamed := collectStreamedText(rs)
	for _, line := range lines {
		if containsText(streamed, line) {
			t.Fatalf("echoed line %q streamed as output: %v", line, streamed)
		}
	}
	rec, ok := s.ObservationFor(nonce)
	if !ok {
		t.Fatal("the interval sealed no record")
	}
	for _, row := range rec.Closing.Lines {
		text := obsRowText(row)
		for _, line := range lines {
			if text == line {
				t.Fatalf("the closing screen holds echoed line %q: it was never suppressed", line)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Only the FIRST C inside an interval counts. A later one — a nested
// command's own preexec, a shell function, anything already running inside
// the interval's own output — must not re-arm the window from whatever is
// on screen at that later instant: doing so would suppress genuine output
// the moment it departed.
// ---------------------------------------------------------------------------

func TestALaterOutputMarkWithinTheSameIntervalIsOrdinaryOutput(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(20, 5))

	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the first output mark: %v", err)
	}
	obsFeed(t, s, 0, 3) // genuine output; screen not yet full, nothing departs

	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the second output mark: %v", err)
	}
	obsFeed(t, s, 3, 20) // comfortably forces L000000..L000012 to scroll off and depart

	streamed := collectStreamedText(rs)
	for i := range 13 {
		want := fmt.Sprintf("L%06d", i)
		if !containsText(streamed, want) {
			t.Fatalf("row %q never streamed: a later output mark suppressed genuine output (%v)", want, streamed)
		}
	}
}

// ---------------------------------------------------------------------------
// With no C ever sighted — a shell with no preexec support, or one whose
// integration never activated — an interval behaves exactly as it did
// before this bead: everything that departs is output, echo-shaped or not.
// ---------------------------------------------------------------------------

func TestWithNoOutputMarkEverythingStaysOutput(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(20, 5))

	if err := s.Ingest([]byte("echoedliketext\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	obsFeed(t, s, 0, 10) // enough to scroll the first line off

	streamed := collectStreamedText(rs)
	if !containsText(streamed, "echoedliketext") {
		t.Fatalf("with no output mark sighted, a row was suppressed anyway: %v", streamed)
	}
}
