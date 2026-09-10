package assistant

// What workers.spawn tells a coordinator about the task (nocx-f545a.3).
//
// A worker whose pane stopped on a question before it could take its task is
// live and untold, and the result has to say both. Before this there was no
// third answer: the call either returned a live worker (task typed) or an
// error, and a pane asking "do you trust this folder" was the error.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/workers"
)

func spawnResultWith(t *testing.T, delivery workers.TaskDelivery) map[string]any {
	t.Helper()
	rec := &fakeWorkerRecord{delivery: delivery}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("executeWorkerSpawn: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	return got
}

func TestAWorkerWhosePaneAsksIsLiveAndSaysItsTaskWasNotTyped(t *testing.T) {
	got := spawnResultWith(t, workers.TaskDelivery{WaitingOn: "permission_choice"})
	if got["state"] != "live" {
		t.Fatalf("state = %v, want live: a worker waiting on a question still exists", got["state"])
	}
	if got["taskTyped"] != false {
		t.Fatalf("taskTyped = %v, want false", got["taskTyped"])
	}
	if got["waitingOn"] != "permission_choice" {
		t.Fatalf("waitingOn = %v, want permission_choice", got["waitingOn"])
	}
}

// The ordinary case says it too, explicitly — an absent field would leave a
// coordinator to infer "typed" from silence — and names no wait.
func TestATypedTaskSaysSoAndNamesNoWait(t *testing.T) {
	got := spawnResultWith(t, workers.TaskDelivery{Typed: true})
	if got["taskTyped"] != true {
		t.Fatalf("taskTyped = %v, want true", got["taskTyped"])
	}
	if _, present := got["waitingOn"]; present {
		t.Fatalf("a typed task named a wait: %v", got)
	}
}
