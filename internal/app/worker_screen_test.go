package app

// The composition root's half of workers.screen (nocx-f545a.6), through the
// real grid and the real watcher, off the real capture of the question that
// started this epic. What is asserted is what a coordinator would read: the
// question in its own words, the options as drawn, and what nocx reads the pane
// as — and, for a pane nocx no longer watches, no reading rather than an error.

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/workers"
)

func TestAWorkersScreenShowsTheQuestionInItsOwnWords(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: participantCols, Rows: participantRows,
	})
	if err != nil {
		t.Fatalf("open the worker's session: %v", err)
	}
	sid := sess.ID()
	stand.enrolWorkerPane(t, sid)
	stand.feedCapture(t, sid, "claude-trust", 11000)
	stand.watch.Sweep()

	screener := &workerScreener{grid: stand.grid, watch: stand.watch}
	got, err := screener.ReadScreen(context.Background(), workers.Participant{
		ID: "p-screen", Liveness: workers.Liveness{SessionID: string(sid)},
	})
	if err != nil {
		t.Fatalf("ReadScreen: %v", err)
	}
	if !got.Readable {
		t.Fatal("a watched pane was not readable")
	}
	if got.State != string(agentdriver.StatePermissionChoice) {
		t.Fatalf("state = %q, want %q", got.State, agentdriver.StatePermissionChoice)
	}
	if len(got.Rows) != participantRows {
		t.Fatalf("%d rows, want the whole %d-row screen", len(got.Rows), participantRows)
	}
	all := strings.Join(got.Rows, "\n")
	for _, want := range []string{
		"Quick safety check: Is this a project you created or one you trust?",
		"❯ No, exit",
		"Yes, I trust this folder",
		"Enter to confirm",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the screen does not carry %q:\n%s", want, all)
		}
	}
	for i, row := range got.Rows {
		if strings.HasSuffix(row, " ") {
			t.Fatalf("row %d keeps trailing spaces: %q", i, row)
		}
	}
}

// A pane nocx is not watching — its observation closed, or never opened — is
// no reading, answered as such.
func TestAPaneNocxIsNotWatchingHasNoReading(t *testing.T) {
	stand := newTaskDeliveryStand(t)
	screener := &workerScreener{grid: stand.grid, watch: stand.watch}
	got, err := screener.ReadScreen(context.Background(), workers.Participant{
		ID: "p-gone", Liveness: workers.Liveness{SessionID: "session-nobody-enrolled"},
	})
	if err != nil {
		t.Fatalf("ReadScreen of an unwatched pane: %v", err)
	}
	if got.Readable || len(got.Rows) != 0 || got.State != "" {
		t.Fatalf("screen = %+v, want no reading", got)
	}
}
