package sessionruntime

import (
	"encoding/json"
	"testing"
)

func TestDecodeCwdReportMatchesRendererURIShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want CwdReport
		ok   bool
	}{
		{name: "encoded host and path", body: "file://build%20host/tmp/one%20two", want: CwdReport{Host: "build host", Path: "/tmp/one two"}, ok: true},
		{name: "empty host", body: "file:///tmp", want: CwdReport{Host: "", Path: "/tmp"}, ok: true},
		{name: "wrong scheme", body: "https://host/tmp"},
		{name: "missing path", body: "file://host"},
		{name: "malformed escape", body: "file://host/tmp%ZZ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decodeCwdReport([]byte(tt.body))
			if ok != tt.ok || got != tt.want {
				t.Fatalf("decodeCwdReport(%q) = (%+v, %v), want (%+v, %v)", tt.body, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestRuntimeSnapshotRetainsLatestTitleAndCwdState(t *testing.T) {
	s := obsSession(t, harnessGeometry(80, 24))
	consumer := s.Consumers().Attach()
	if err := s.Ingest([]byte("\x1b]0;first title\x07\x1b]7;file://buildhost/tmp/one%20two\x07")); err != nil {
		t.Fatalf("ingest title and cwd: %v", err)
	}
	snap := s.Snapshot()
	if snap.Title == nil || *snap.Title != "first title" {
		t.Fatalf("snapshot title = %v, want first title", snap.Title)
	}
	wantCwd := CwdReport{Host: "buildhost", Path: "/tmp/one two"}
	if snap.Cwd == nil || *snap.Cwd != wantCwd {
		t.Fatalf("snapshot cwd = %v, want %+v", snap.Cwd, wantCwd)
	}
	raw, err := EncodeFrame(snap)
	if err != nil {
		t.Fatalf("encode latest state frame: %v", err)
	}
	var frame struct {
		State struct {
			Title *string    `json:"title"`
			Cwd   *CwdReport `json:"cwd"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if frame.State.Title == nil || *frame.State.Title != "first title" || frame.State.Cwd == nil || *frame.State.Cwd != wantCwd {
		t.Fatalf("frame state = %+v, want title and cwd from one snapshot", frame.State)
	}

	// A title clear is state (the empty string), while an invalid cwd report
	// does not erase the last valid directory. Neither is inferred from a
	// replayed renderer event.
	if err := s.Ingest([]byte("\x1b]2;\x07\x1b]7;https://elsewhere/tmp\x07")); err != nil {
		t.Fatalf("ingest clear and invalid cwd: %v", err)
	}
	snap = s.Snapshot()
	if snap.Title == nil || *snap.Title != "" {
		t.Fatalf("cleared snapshot title = %v, want explicit empty title", snap.Title)
	}
	if snap.Cwd == nil || *snap.Cwd != wantCwd {
		t.Fatalf("invalid cwd replaced the last valid report: got %v, want %+v", snap.Cwd, wantCwd)
	}

	effects := consumer.Effects()
	if len(effects) != 4 {
		t.Fatalf("at-most-once title/cwd effects = %d, want 4; state must not add a second effect path", len(effects))
	}
}
