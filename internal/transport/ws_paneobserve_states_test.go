package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentcapture/replaylocal"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/paneobserve"
)

// recordedChrome replays a committed internal/agentdriver capture up to atMs
// and paints the resulting frame back into the bytes that reproduce it
// (internal/agentcapture.Paint), so the seam test below drives its pane from
// real Claude Code byte streams rather than hand-built chrome. A fixture
// written by the person writing the driver encodes that person's model of the
// TUI, including the parts that are wrong (AGENTS.md's testing rule 1); a
// capture is real bytes off a real PTY, produced and marked the way
// internal/agentdriver's own manifest is (nocx-nru89.8).
func recordedChrome(t *testing.T, capture string, atMs int64) string {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", capture+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", capture, err)
	}
	moments, err := agentcapture.Frames(context.Background(), replaylocal.Replayer{}, header, chunks, []int64{atMs})
	if err != nil {
		t.Fatalf("replay %s to %dms: %v", capture, atMs, err)
	}
	f := moments[0].Frame
	return string(agentcapture.Paint(f))
}

// THE SEAM A PERSON REACHES (nocx-nru89). Nobody calls Explain: a person sees a
// pane's state because the watcher classifies the grid and the transport sends
// session.observationChanged. So the readings the rule fixes are proved here,
// through the real socket, the real watcher and the shipped rule, in the order
// a turn produces them — and, since nocx-nru89.8, on the same recorded bytes
// the manifest itself is checked against, not on chrome hand-built to match
// what the rule currently reads.
//
// The pane is enrolled at 120x40 because that is the geometry every capture
// used here was recorded at (record.sh's -rows 40, cols 120): the frame a
// driver classifies in production comes out of a panegrid Store fed from byte
// zero at a real geometry, and replaying at a different one would answer
// about a screen the product never produces (the same reason
// internal/agentcapture's package doc gives for going through panegrid at
// all).
//
// The API-wait frame is sourced from claude-lmstudio-turn, the nocx-nru89.2
// recording, rather than one of the nocx-nru89.8 captures: this corpus's only
// evidence of Claude's "Waiting for API response · will retry in" chrome is
// that capture (see manifest.json's api-waiting entry — the owner's decision,
// 2026-09-12, because a never-answering listener does not reproduce this
// chrome within record.sh's capture window; nocx-nru89.9). The capture itself
// remains a valid, committed nocx-nru89.2 recording of Claude Code 2.1.266;
// nocx-nru89.9 is what gave the manifest entry the "api-waiting" moment name.
func TestAnObservedPaneReportsEachStateItsScreenShows(t *testing.T) {
	ws, store, watch, term := newObservedWS(t, paneobserve.Config{})
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	if err := store.Watch(sid, 120, 40); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	watch.Watch(sid, "claude")

	steps := []struct {
		name   string
		chrome string
		want   agentdriver.State
	}{
		{"a turn before its elapsed timer", recordedChrome(t, "claude-2.1.266-turn", 49000), agentdriver.StateWorking},
		{"the API waiting for a response", recordedChrome(t, "claude-lmstudio-turn", 72000), agentdriver.StateError},
		{"the turn finished", recordedChrome(t, "claude-2.1.266-subagent-finished", 126000), agentdriver.StateFreeText},
	}
	for _, step := range steps {
		term.emit(t, step.chrome)
		raw := readNotification(t, conn, "session.observationChanged", wantWithin)
		var got observationChangedParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", step.name, err)
		}
		if got.State != string(step.want) {
			t.Fatalf("%s: observed %q, want %q", step.name, got.State, step.want)
		}
	}
}
