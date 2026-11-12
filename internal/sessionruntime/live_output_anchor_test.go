package sessionruntime

import "testing"

func TestRunningOutputAnchorFollowsRepaintWithoutMovingStreamStart(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(40, 8))
	ingest := func(text string) {
		t.Helper()
		if err := s.Ingest([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	latest := func() rowEvent {
		t.Helper()
		events := rs.snapshot()
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].kind == "output-start" {
				return events[i]
			}
		}
		t.Fatal("no output anchor")
		return rowEvent{}
	}
	ingest("old history\r\n$ top\r\n\x1b]133;C\a")
	initial := latest()
	if initial.anchor == nil || initial.anchor.Row != 2 {
		t.Fatalf("initial anchor = %+v", initial.anchor)
	}
	ingest("first output")
	if got := latest(); got.anchor.Row != initial.anchor.Row {
		t.Fatalf("ordinary output changed prefix: %+v", got.anchor)
	}
	for _, repaint := range []string{"\x1b[Htop summary\x1b[2;1Htasks", "\x1b[Hnew summary\x1b[2;1Htasks"} {
		ingest(repaint)
		got := latest()
		snap := s.Snapshot()
		if got.anchor == nil || got.anchor.Row != 0 || got.from != initial.from || got.anchor.Revision > snap.Revision || got.anchor.Revision <= initial.anchor.Revision {
			t.Fatalf("repaint anchor = %+v, from %d, want row 0/from %d/revision %d", got.anchor, got.from, initial.from, snap.Revision)
		}
	}
	for _, e := range rs.snapshot() {
		if e.kind == "end" {
			t.Fatal("repaint ended interval")
		}
	}
	obsSeal(t, s, obsNonce(1))
	events := rs.snapshot()
	end := events[len(events)-1]
	if end.kind != "end" || len(end.closing) == 0 || streamRowText(end.closing[0]) != "new summary" {
		t.Fatalf("closing screen lost rewritten first row: %+v", end)
	}
}

func TestRunningOutputAnchorKeepsUntouchedHistoryAbovePartialRepaint(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(40, 8))
	for _, text := range []string{"old history\r\n$ top\r\n\x1b]133;C\a", "\x1b[2;1Hrewritten prompt\x1b[H"} {
		if err := s.Ingest([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	events := rs.snapshot()
	got := events[len(events)-1]
	if got.anchor == nil || got.anchor.Row != 1 {
		t.Fatalf("partial repaint anchor = %+v, want row 1", got.anchor)
	}
}

func TestRunningOutputAnchorDoesNotFollowDepartingRows(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(40, 8))
	if err := s.Ingest([]byte("old history\r\n$ program\r\n\x1b]133;C\a")); err != nil {
		t.Fatal(err)
	}
	events := rs.snapshot()
	initial := events[len(events)-1]
	obsFeed(t, s, 0, 150)
	for _, geometry := range []Geometry{harnessGeometry(40, 12), harnessGeometry(60, 12)} {
		if _, err := s.CommitGeometry(geometry); err != nil {
			t.Fatal(err)
		}
		events = rs.snapshot()
		latest := events[len(events)-1]
		if latest.anchor == nil || latest.anchor.Row != initial.anchor.Row || latest.from != initial.from {
			t.Fatalf("resize moved original anchor from %+v to %+v", initial.anchor, latest.anchor)
		}
	}
	events = rs.snapshot()
	var latest rowEvent
	for _, event := range events {
		if event.kind == "output-start" {
			latest = event
		}
	}
	if latest.anchor.Row != initial.anchor.Row || latest.from != initial.from {
		t.Fatalf("scroll moved original anchor from %+v to %+v", initial.anchor, latest.anchor)
	}
}

func TestRunningOutputAnchorFollowsWrappedPrefixOnWidthShrink(t *testing.T) {
	s, rs := streamSession(t, harnessGeometry(40, 12))
	if err := s.Ingest([]byte("$ 123456789012345678901234567890\r\n\x1b]133;C\aoutput")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitGeometry(harnessGeometry(10, 12)); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	want := -1
	for y, row := range snap.Rows {
		if streamRowText(row) == "output" {
			want = y
			break
		}
	}
	events := rs.snapshot()
	latest := events[len(events)-1]
	if want < 0 || latest.anchor == nil || latest.anchor.Row != snap.BaseRow+uint64(want) {
		t.Fatalf("wrapped prefix anchor %+v want row %d at base %d", latest.anchor, want, snap.BaseRow)
	}
}
