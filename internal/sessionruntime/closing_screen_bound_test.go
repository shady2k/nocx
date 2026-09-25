package sessionruntime

import (
	"strings"
	"testing"
)

// The closing screen's own start bound (nocx-2v80t.3.12, reopened): the
// output-start mark's join already protects a DEPARTING row from being
// re-streamed (output_mark_test.go), but a command that never scrolls never
// departs, and its closing screen — built straight from the current screen,
// unbounded — held every row still sitting above its own output: the row(s)
// the interval before it left, never claimed by anything else because they
// never departed either.
//
// Every test drives the real emulator directly through Session.Ingest and
// reads back what the row stream's own end marker carries (end.closing) —
// the block's actual stored rows — never Session.ObservationFor's raw
// screen, which is untrimmed by design (observation.go).

// fenceFor is the fence bytes ingested to seal the nonce obsNonce(k) names —
// obsNonce fills every byte with k, and the fence's own wire spelling is
// each byte's hex pair repeated 32 times.
func fenceFor(k byte) string {
	pair := strings.ToLower(string("0123456789abcdef"[k>>4])) + strings.ToLower(string("0123456789abcdef"[k&0xf]))
	return fenceMarkerPrefix + strings.Repeat(pair, 32) + "\x07"
}

// closingTexts reads one interval end's closing rows as text, in order.
func closingTexts(t *testing.T, rs *recordingRowStream, nonce FenceNonce) []string {
	t.Helper()
	for _, e := range rs.snapshot() {
		if e.kind == "end" && e.nonce == nonce {
			out := make([]string, len(e.closing))
			for i, r := range e.closing {
				out[i] = streamRowText(r)
			}
			return out
		}
	}
	t.Fatalf("no end marker for nonce %v ever streamed", nonce)
	return nil
}

func containsClosingText(texts []string, want string) bool {
	for _, t := range texts {
		if t == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The reported shape exactly: after "true #t9-ok" (no output), the block of
// "echo failing; false #t9-fail" must hold only "failing" — never the first
// command's own row, never its own echoed command line, both of which are
// still sitting on the screen because nothing ever scrolled.
// ---------------------------------------------------------------------------

func TestASecondShortCommandsClosingScreenHoldsOnlyItsOwnOutput(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	if err := s.Ingest([]byte("true #t9-ok\r\n")); err != nil {
		t.Fatalf("ingest the first command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the first output mark: %v", err)
	}
	nonce1 := obsNonce(0x01)
	s.Completed(s.Incarnation(), nonce1, 0)
	if err := s.Ingest([]byte(fenceFor(0x01))); err != nil {
		t.Fatalf("ingest the first fence: %v", err)
	}
	if got := closingTexts(t, rs, nonce1); len(got) != 0 {
		t.Fatalf("a command with no output closed with a non-empty screen: %v", got)
	}

	if err := s.Ingest([]byte("echo failing; false #t9-fail\r\n")); err != nil {
		t.Fatalf("ingest the second command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the second output mark: %v", err)
	}
	if err := s.Ingest([]byte("failing\r\n")); err != nil {
		t.Fatalf("ingest the second command's output: %v", err)
	}
	nonce2 := obsNonce(0x02)
	s.Completed(s.Incarnation(), nonce2, 0)
	if err := s.Ingest([]byte(fenceFor(0x02))); err != nil {
		t.Fatalf("ingest the second fence: %v", err)
	}

	got := closingTexts(t, rs, nonce2)
	if containsClosingText(got, "true #t9-ok") {
		t.Fatalf("the second interval's closing screen holds the first interval's own row: %v", got)
	}
	if containsClosingText(got, "echo failing; false #t9-fail") {
		t.Fatalf("the second interval's closing screen holds its own echoed command line: %v", got)
	}
	if !containsClosingText(got, "failing") {
		t.Fatalf("the second interval's closing screen never holds its own output: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("the second interval's closing screen holds %d rows, want exactly 1 (\"failing\"): %v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Paired: a second short command with NO output of its own must close with
// an EMPTY screen too — not the first command's row, and not its own echo.
// ---------------------------------------------------------------------------

func TestASecondShortCommandWithNoOutputClosesEmpty(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	if err := s.Ingest([]byte("true #t9-ok\r\n")); err != nil {
		t.Fatalf("ingest the first command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the first output mark: %v", err)
	}
	nonce1 := obsNonce(0x03)
	s.Completed(s.Incarnation(), nonce1, 0)
	if err := s.Ingest([]byte(fenceFor(0x03))); err != nil {
		t.Fatalf("ingest the first fence: %v", err)
	}

	if err := s.Ingest([]byte("false #t9-fail\r\n")); err != nil {
		t.Fatalf("ingest the second command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the second output mark: %v", err)
	}
	nonce2 := obsNonce(0x04)
	s.Completed(s.Incarnation(), nonce2, 0)
	if err := s.Ingest([]byte(fenceFor(0x04))); err != nil {
		t.Fatalf("ingest the second fence: %v", err)
	}

	if got := closingTexts(t, rs, nonce2); len(got) != 0 {
		t.Fatalf("a second command with no output of its own closed with %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Paired: a command that SCROLLS keeps the ordinary, unbounded closing
// screen — every foreign row already left through the departure stream (the
// suppression window this bead's earlier fix already protects), so nothing
// is left for this bound to cut, and the fix must not cut into the command's
// own output regardless.
// ---------------------------------------------------------------------------

func TestAScrollingSecondCommandsClosingScreenIsUnbounded(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(20, 5))

	if err := s.Ingest([]byte("true #a\r\n")); err != nil {
		t.Fatalf("ingest the first command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the first output mark: %v", err)
	}
	nonce1 := obsNonce(0x05)
	s.Completed(s.Incarnation(), nonce1, 0)
	if err := s.Ingest([]byte(fenceFor(0x05))); err != nil {
		t.Fatalf("ingest the first fence: %v", err)
	}

	if err := s.Ingest([]byte("echo x\r\n")); err != nil {
		t.Fatalf("ingest the second command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the second output mark: %v", err)
	}
	obsFeed(t, s, 0, 20) // comfortably scrolls a 5-row screen many times over
	nonce2 := obsNonce(0x06)
	s.Completed(s.Incarnation(), nonce2, 0)
	if err := s.Ingest([]byte(fenceFor(0x06))); err != nil {
		t.Fatalf("ingest the second fence: %v", err)
	}

	got := closingTexts(t, rs, nonce2)
	if containsClosingText(got, "true #a") {
		t.Fatalf("the scrolling interval's closing screen holds the first interval's own row: %v", got)
	}
	if containsClosingText(got, "echo x") {
		t.Fatalf("the scrolling interval's closing screen holds its own echoed command line: %v", got)
	}
	if !containsClosingText(got, "L000019") {
		t.Fatalf("the scrolling interval's closing screen lost its own tail output: %v", got)
	}
}
