package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// claudeTurnStartingChrome is the idle chrome with the status row Claude
// Code 2.1.266 draws in the first seconds of a turn: the spinner and its verb,
// no elapsed timer yet, and the interrupt hint in the mode line (nocx-ys9jd).
func claudeTurnStartingChrome(cols int) string {
	return claudeIdleChrome(cols) +
		"\x1b[6;1H✻ Burrowing…" +
		"\x1b[12;1H  ⏸ manual mode on · esc to interrupt" +
		"\x1b[9;3H"
}

// THE SEAM A PERSON REACHES (nocx-nru89). Nobody calls Explain: a person sees a
// pane's state because the watcher classifies the grid and the transport sends
// session.observationChanged. So the readings the rule fixes are proved here,
// through the real socket, the real watcher and the shipped rule, in the order
// a turn produces them.
func TestAnObservedPaneReportsEachStateItsScreenShows(t *testing.T) {
	ws, store, watch, term := newObservedWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	if err := store.Enrol(sid, 80, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	watch.Watch(sid, "claude")

	steps := []struct {
		name   string
		chrome string
		want   agentdriver.State
	}{
		{"a turn before its elapsed timer", claudeTurnStartingChrome(80), agentdriver.StateWorking},
		{"the turn finished", claudeIdleChrome(80), agentdriver.StateFreeText},
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
