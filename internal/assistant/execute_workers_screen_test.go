package assistant

// workers.screen's executor (nocx-f545a.6): it names the participant the model
// named, shows what the record's screen read returned, and passes a refusal
// through unchanged so the endpoint's ownership and delegation sentences apply.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/workers"
)

func TestWorkerScreenShowsTheHeldWorkersRows(t *testing.T) {
	rec := &fakeWorkerRecord{screen: workers.PaneScreen{
		Readable: true, State: "permission_choice",
		Rows: []string{"Quick safety check", "", "❯ No, exit", "  Yes, I trust this folder"},
	}}
	out, err := executeWorkerScreen(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"worker":"p-7"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("executeWorkerScreen: %v", err)
	}
	if len(rec.screenedFor) != 1 || rec.screenedFor[0] != "p-7" {
		t.Fatalf("screen asked for %v, want exactly p-7", rec.screenedFor)
	}
	var got struct {
		Worker   string   `json:"worker"`
		Readable bool     `json:"readable"`
		State    string   `json:"state"`
		Rows     []string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	if got.Worker != "p-7" || !got.Readable || got.State != "permission_choice" || len(got.Rows) != 4 {
		t.Fatalf("result = %+v", got)
	}
}

// A pane with no reading still carries rows, empty, so the model never has to
// tell an absent field from an empty screen.
func TestWorkerScreenOfAnUnreadablePaneCarriesEmptyRows(t *testing.T) {
	rec := &fakeWorkerRecord{}
	out, err := executeWorkerScreen(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"worker":"p-7"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("executeWorkerScreen: %v", err)
	}
	if !strings.Contains(out, `"rows":[]`) || !strings.Contains(out, `"readable":false`) {
		t.Fatalf("result = %s, want readable false and an empty rows array", out)
	}
}

func TestWorkerScreenPassesTheRecordsRefusalThrough(t *testing.T) {
	rec := &fakeWorkerRecord{screenErr: workers.ErrNotHeld}
	_, err := executeWorkerScreen(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"worker":"p-7"}`), workerSeams(rec))
	if !errors.Is(err, workers.ErrNotHeld) {
		t.Fatalf("err = %v, want the record's ErrNotHeld unchanged", err)
	}
}

func TestWorkerScreenNeedsAWorker(t *testing.T) {
	rec := &fakeWorkerRecord{}
	if _, err := executeWorkerScreen(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{}`), workerSeams(rec)); err == nil {
		t.Fatal("a screen with no worker named was answered")
	}
	if len(rec.screenedFor) != 0 {
		t.Fatalf("a refused call still read a pane: %v", rec.screenedFor)
	}
}
