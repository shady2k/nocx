package app

// The capture round's acceptance over the REAL composition root and the
// real socket (the brief's words): submit a command carrying a key, get a
// capture id back, save it, and read the history row as a reference — then
// then leave a second offer unanswered while other commands run, and
// answer it afterwards — the offer waits.
// The vault is set up with a passphrase (no keystore on this host), so the
// save path runs against the real file provider and the real encrypted
// content store.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage/storagetest"

	"github.com/gorilla/websocket"
)

func TestCapture_SaveNowAndSaveLaterOverTheRealSocket(t *testing.T) {
	storagetest.Isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	defer a.Shutdown(ctx)

	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	// The vault needs to be unsealed before a save can create a secret:
	// passphrase setup (the no-keystore host has no OS key).
	setup := callAppWS(t, conn, "vault.setup", map[string]any{"passphrase": "correct horse battery staple"}, 1)
	if setup.Error != nil {
		t.Fatalf("vault.setup: %+v", setup.Error)
	}

	// ── leg 1: submit a key, save it, read the row as a reference ────────
	record := callAppWS(t, conn, "history.record", map[string]any{
		"command": `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://openrouter.ai/api`,
		"cwd":     "/srv", "host": "", "status": "success", "exitCode": 0, "author": "shell",
		// The capture scope is (connection, tab): a pending capture belongs to
		// the tab that submitted it and dies with that tab (nocx-tsajw).
		"paneId":    "pane-acceptance",
		"startedAt": int64(1_750_000_000_000), "endedAt": int64(1_750_000_000_100), "trusted": true,
	}, 2)
	if record.Error != nil {
		t.Fatalf("history.record: %+v", record.Error)
	}
	var ack struct {
		EntryID     string `json:"entryId"`
		MaskedCount int    `json:"maskedCount"`
		Redactions  []struct {
			Kind  string `json:"kind"`
			Start int    `json:"start"`
			End   int    `json:"end"`
		} `json:"redactions"`
		Captures []struct {
			ID            string `json:"id"`
			SuggestedName string `json:"suggestedName"`
		} `json:"captures"`
	}
	if err := json.Unmarshal(record.Result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.MaskedCount != 1 || len(ack.Captures) != 1 {
		t.Fatalf("ack = %+v, want one mask and one offer", ack)
	}
	if ack.Captures[0].SuggestedName != "openrouter.ai" {
		t.Errorf("suggestedName = %q, want the host openrouter.ai", ack.Captures[0].SuggestedName)
	}
	if ack.EntryID == "" {
		t.Fatal("entryId is empty")
	}

	save := callAppWS(t, conn, "secrets.captureSave", map[string]any{"captureId": ack.Captures[0].ID}, 3)
	if save.Error != nil {
		t.Fatalf("secrets.captureSave: %+v", save.Error)
	}
	var saved struct {
		Name    string `json:"name"`
		Partial bool   `json:"partial"`
	}
	if err := json.Unmarshal(save.Result, &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if saved.Name != "openrouter.ai" {
		t.Errorf("saved name = %q, want the real name", saved.Name)
	}
	if saved.Partial {
		t.Error("save reported partial, want the full success")
	}

	// Read the row back: it is a reference now, and the raw key is nowhere.
	q := callAppWS(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 50,
	}, 4)
	if q.Error != nil {
		t.Fatalf("history.query: %+v", q.Error)
	}
	var page struct {
		Entries []struct {
			Command    string `json:"command"`
			Redactions []struct {
				Kind string `json:"kind"`
			} `json:"redactions"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(q.Result, &page); err != nil {
		t.Fatalf("decode query: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %+v, want the one recorded row", page.Entries)
	}
	if !strings.Contains(page.Entries[0].Command, "{{secret:openrouter.ai}}") {
		t.Errorf("row command = %q, want the vault reference", page.Entries[0].Command)
	}
	if len(page.Entries[0].Redactions) != 0 {
		t.Errorf("row redactions = %+v, want the saved segment gone", page.Entries[0].Redactions)
	}

	// ── leg 2: an offer left unanswered while work carries on ────────────
	// The offer waits. It used to die at the next submission and, before
	// that, on a 30-second timer; both cost the decision, because deciding
	// about a key is rarely the next thing anyone does. Two more commands
	// run here — one carrying its own key, one carrying none — and the
	// first offer is still answerable afterwards.
	record2 := callAppWS(t, conn, "history.record", map[string]any{
		"command": "TOKEN=abcdefghijklmnopqrstuvwxyz123456 ./run.sh",
		"cwd":     "/srv", "host": "", "status": "success", "exitCode": 0, "author": "shell",
		// The capture scope is (connection, tab): a pending capture belongs to
		// the tab that submitted it and dies with that tab (nocx-tsajw).
		"paneId":    "pane-acceptance",
		"startedAt": int64(1_750_000_000_200), "endedAt": int64(1_750_000_000_300), "trusted": true,
	}, 5)
	if record2.Error != nil {
		t.Fatalf("history.record (leg 2): %+v", record2.Error)
	}
	var ack2 struct {
		Captures []struct {
			ID string `json:"id"`
		} `json:"captures"`
		Redactions []struct {
			Kind   string `json:"kind"`
			Prefix string `json:"prefix"`
			Suffix string `json:"suffix"`
		} `json:"redactions"`
	}
	if err := json.Unmarshal(record2.Result, &ack2); err != nil {
		t.Fatalf("decode ack2: %v", err)
	}
	if len(ack2.Captures) != 1 || len(ack2.Redactions) != 1 {
		t.Fatalf("ack2 = %+v, want one capture and one structured redaction", ack2)
	}

	// Ordinary work carries on in the same tab.
	record3 := callAppWS(t, conn, "history.record", map[string]any{
		"command": "echo done",
		"cwd":     "/srv", "host": "", "status": "success", "exitCode": 0, "author": "shell",
		// The capture scope is (connection, tab): a pending capture belongs to
		// the tab that submitted it and dies with that tab (nocx-tsajw).
		"paneId":    "pane-acceptance",
		"startedAt": int64(1_750_000_000_400), "endedAt": int64(1_750_000_000_500), "trusted": true,
	}, 8)
	if record3.Error != nil {
		t.Fatalf("history.record (leg 3): %+v", record3.Error)
	}

	still := callAppWS(t, conn, "secrets.captureSave", map[string]any{"captureId": ack2.Captures[0].ID}, 6)
	if still.Error != nil {
		t.Fatalf("save after later commands = %+v, want the offer still answerable", still.Error)
	}

	// And the row that was saved through the offer now reads as a
	// reference, with the structured redaction gone.
	q2 := callAppWS(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv", "host": "", "limit": 50,
	}, 7)
	if q2.Error != nil {
		t.Fatalf("history.query (leg 2): %+v", q2.Error)
	}
	var page2 struct {
		Entries []struct {
			Command     string `json:"command"`
			MaskedCount int    `json:"maskedCount"`
			Redactions  []struct {
				Kind   string `json:"kind"`
				Prefix string `json:"prefix"`
				Suffix string `json:"suffix"`
			} `json:"redactions"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(q2.Result, &page2); err != nil {
		t.Fatalf("decode query2: %v", err)
	}
	var lateRow *struct {
		Command     string `json:"command"`
		MaskedCount int    `json:"maskedCount"`
		Redactions  []struct {
			Kind   string `json:"kind"`
			Prefix string `json:"prefix"`
			Suffix string `json:"suffix"`
		} `json:"redactions"`
	}
	for i := range page2.Entries {
		if strings.HasPrefix(page2.Entries[i].Command, "TOKEN=") {
			lateRow = &page2.Entries[i]
			break
		}
	}
	if lateRow == nil {
		t.Fatal("the leg-2 row is missing from history")
	}
	// Answered late, and the answer landed: the row reads as a reference and
	// its structured redaction is gone, exactly as leg 1's did — two
	// unrelated commands having run in between changes nothing.
	if !strings.Contains(lateRow.Command, "{{secret:") {
		t.Errorf("leg-2 command = %q, want the vault reference after the late save", lateRow.Command)
	}
	if strings.Contains(lateRow.Command, "abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("leg-2 command carries the raw value: %q", lateRow.Command)
	}
	if len(lateRow.Redactions) != 0 {
		t.Errorf("leg-2 redactions = %+v, want the saved segment gone", lateRow.Redactions)
	}
}

// wsRPCResult, dialAppWS and callAppWS are shared with the history
// acceptance tests in history_acceptance_test.go.
var _ = websocket.TextMessage
