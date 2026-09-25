package sessionruntime

import "testing"

// SealEnvironmentEntry (nocx-2v80t.3.21): an authenticated environment
// entry — the coordinator's kernel accepting a confirmed environment change
// while a local command's interval is still open — ends that interval
// exactly as its own end marker would. Before this method existed, every
// other freeze path ended in an authenticated fence and BlockIntervalEnded;
// entering ssh had no event of its own, so the local block's rows artifact
// stayed empty (nocx-2v80t.3.20).
//
// The test drives the real emulator directly, the way every other schedule
// in this package does, and reads back what the row stream's own end marker
// carries — the closing screen actually appended to the block — never
// Session.ObservationFor's raw, untrimmed screen.

func TestSealEnvironmentEntrySealsTheOpenIntervalAtItsOwnScreen(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	if err := s.Ingest([]byte("ssh host\r\n")); err != nil {
		t.Fatalf("ingest the ssh command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the output mark: %v", err)
	}
	if err := s.Ingest([]byte("Welcome to host\r\npassword:")); err != nil {
		t.Fatalf("ingest the banner and password prompt: %v", err)
	}

	s.SealEnvironmentEntry(s.Incarnation(), "dom-child")

	var end *rowEvent
	for _, e := range rs.snapshot() {
		if e.kind == "end" && e.nonce == (FenceNonce{}) {
			ev := e
			end = &ev
		}
	}
	if end == nil {
		t.Fatal("the environment entry never sealed an interval")
	}
	texts := make([]string, len(end.closing))
	for i, r := range end.closing {
		texts[i] = streamRowText(r)
	}
	if containsClosingText(texts, "ssh host") {
		t.Fatalf("the entry's closing screen holds its own echoed command line: %v", texts)
	}
	if !containsClosingText(texts, "Welcome to host") {
		t.Fatalf("the entry's closing screen is missing the banner: %v", texts)
	}
	if !containsClosingText(texts, "password:") {
		t.Fatalf("the entry's closing screen is missing the password prompt: %v", texts)
	}

	// Paired with an ordinary end marker: a command run once the entry has
	// sealed the interval before it still seals on its OWN fence, and its
	// closing screen carries none of the entry's own rows.
	if err := s.Ingest([]byte("echo after\r\n")); err != nil {
		t.Fatalf("ingest the next command's echo: %v", err)
	}
	if err := s.Ingest([]byte(outputMarkerFixed)); err != nil {
		t.Fatalf("ingest the next output mark: %v", err)
	}
	if err := s.Ingest([]byte("after\r\n")); err != nil {
		t.Fatalf("ingest the next command's output: %v", err)
	}
	nonce := obsNonce(0x07)
	s.Completed(s.Incarnation(), nonce, 0)
	if err := s.Ingest([]byte(fenceFor(0x07))); err != nil {
		t.Fatalf("ingest the next fence: %v", err)
	}
	got := closingTexts(t, rs, nonce)
	if containsClosingText(got, "Welcome to host") || containsClosingText(got, "password:") {
		t.Fatalf("the ordinary interval after the entry holds the entry's own closing rows: %v", got)
	}
	if !containsClosingText(got, "after") {
		t.Fatalf("the ordinary interval after the entry lost its own output: %v", got)
	}
}

// A stale incarnation, or a session that is not available, is refused
// exactly as Session.Completed refuses one — the same guard, because a
// replaced coordinator asking a runtime to seal an incarnation it left
// behind is what it exists for.
func TestSealEnvironmentEntryRefusesAStaleIncarnation(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))

	if err := s.Ingest([]byte("ssh host\r\n")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	stale := s.Incarnation()
	stale.Generation++
	s.SealEnvironmentEntry(stale, "dom-child")

	for _, e := range rs.snapshot() {
		if e.kind == "end" {
			t.Fatalf("a stale incarnation's environment entry sealed an interval anyway: %+v", e)
		}
	}
}

// entryEnds counts the fence-less end markers the row stream carried: one per
// interval an environment entry sealed.
func entryEnds(rs *recordingRowStream) int {
	n := 0
	for _, e := range rs.snapshot() {
		if e.kind == "end" && e.nonce == (FenceNonce{}) {
			n++
		}
	}
	return n
}

// enterAndPrint drives a local `ssh` to the point an entry seals it, then the
// remote side printing something of its own, so a second seal has an interval
// with content to (wrongly) close.
func enterAndPrint(t *testing.T, s *Session) {
	t.Helper()
	for _, b := range []string{"ssh host\r\n", outputMarkerFixed, "Welcome to host\r\n"} {
		if err := s.Ingest([]byte(b)); err != nil {
			t.Fatalf("ingest %q: %v", b, err)
		}
	}
}

// The same environment entry delivered twice (nocx-2v80t.3.28) — the
// completion downlink retries a send that timed out, and the attempt that
// timed out may already have landed — seals ONE interval. The second copy
// arrives while the remote session is printing; sealing it would end an
// interval nobody ended and hand the remote rows a phantom block.
func TestTheSameEnvironmentEntryDeliveredTwiceSealsOneInterval(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	enterAndPrint(t, s)

	s.SealEnvironmentEntry(s.Incarnation(), "dom-child")
	if err := s.Ingest([]byte("remote$ ls\r\nfile-a\r\n")); err != nil {
		t.Fatalf("ingest the remote output: %v", err)
	}
	s.SealEnvironmentEntry(s.Incarnation(), "dom-child")

	if got := entryEnds(rs); got != 1 {
		t.Fatalf("one environment entry delivered twice sealed %d intervals, want 1", got)
	}
}

// Paired with the duplicate: two DISTINCT entries — a nested ssh from inside
// the first, a second child domain taking the lane — are two boundaries and
// seal two intervals.
func TestTwoDistinctEnvironmentEntriesSealTwoIntervals(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(80, 24))
	enterAndPrint(t, s)

	s.SealEnvironmentEntry(s.Incarnation(), "dom-child")
	if err := s.Ingest([]byte("remote$ ssh inner\r\nWelcome to inner\r\n")); err != nil {
		t.Fatalf("ingest the nested ssh: %v", err)
	}
	s.SealEnvironmentEntry(s.Incarnation(), "dom-grandchild")

	if got := entryEnds(rs); got != 2 {
		t.Fatalf("two distinct environment entries sealed %d intervals, want 2", got)
	}
}
