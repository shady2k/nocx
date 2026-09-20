package transport

// Every command that arrives through the authenticated shell channel gets an
// entry — the owner's decision of 2026-09-19. Until then a ledger row existed
// only for a command submitted from nocx's own input line
// (lifecycle.submitAttempt opens it), so a command typed straight into the
// shell, or run in a session with no window attached, left no entry at all.
//
// These tests drive the shell's own path: a NAMED authenticated start — the
// shell mints its own attempt id because no outbound envelope ever carries
// the app's (protocol §8) — ingested exactly as a lifecycle adapter would,
// with the store's own policy deciding the output half.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
)

const (
	shellLedgerPane      = "01930000-0000-7000-8000-0000000000a1"
	shellLedgerArtifact1 = "00000000-0000-7000-8000-0000000000b01"
	shellLedgerArtifact2 = "00000000-0000-7000-8000-0000000000b02"
)

// ledgerQueryPaneIDs drives ledger.query over the real socket for one pane —
// the read the pane's restore is made of — and returns the page's entry ids.
func ledgerQueryPaneIDs(t *testing.T, conn *websocket.Conn, paneID string, id int) []string {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "ledger.query", map[string]any{
		"scope":  "everywhere",
		"paneId": paneID,
	}, id)
	var envelope struct {
		Result struct {
			Entries []struct {
				ID string `json:"id"`
			} `json:"entries"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("ledger.query: unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("ledger.query: %+v", envelope.Error)
	}
	ids := make([]string, 0, len(envelope.Result.Entries))
	for _, entry := range envelope.Result.Entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

// shellStartComplete drives one whole shell-owned command: the authenticated
// start with the shell's own id, then the authenticated completion.
func shellStartComplete(t *testing.T, pub *lifecyclepub.Publisher, lane lifecycle.LaneID, h lifecycle.DomainHandle, seq uint64, id lifecycle.AttemptID, command string, code int) {
	t.Helper()
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, seq, lifecycleStartEvt(&id, command)))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, seq+1, lifecycleCompleteEvt(id, code, lifecycleFence(0x44))))
}

// Criterion 1: a command typed directly into a shell-integrated pane, with NO
// window attached, produces one entry with its start and finish, readable
// through ledger.query for that pane.
func TestShellOriginatedStart_EntryWithNoWindowAttached(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	// The window goes away: the session stays in the registry and the lane
	// stays registered, but nobody is subscribed — the backend-opened /
	// detached-pane shape the feature exists for.
	e.ws.removeRx(session.ID(sid))

	shellID := lifecycle.AttemptID("att-shell-1")
	// THE BINDING ROW the OPEN path writes for a helper-hosted session — this
	// harness opens its session with no helper behind it, so the test writes
	// what the open would have (the subject here is the entry WRITER, not
	// the opener; the same thing TestLifecycleSubmitAttemptNamesTheSession
	// TheCommandRanIn does for the submit path).
	if err := db.Ledger().CreateSession(context.Background(), content.Session{
		ID: sid, WorkspaceID: "ws-lifecycle",
	}); err != nil {
		t.Fatalf("CreateSession(%s): %v", sid, err)
	}

	shellStartComplete(t, pub, lane, h, 2, shellID, "git push", 0)

	row := mustEntry(t, db, string(shellID))
	if row.Phase != content.PhaseClosed || row.Status != content.EntrySuccess {
		t.Fatalf("completed row = phase=%q status=%q, want closed/success", row.Phase, row.Status)
	}
	if row.StartedAt == nil || row.EndedAt == nil {
		t.Fatalf("completed row times = started=%v ended=%v, want both", row.StartedAt, row.EndedAt)
	}
	if row.PaneID == nil || *row.PaneID != shellLedgerPane {
		t.Fatalf("shell row pane = %v, want the lifecycle pane", row.PaneID)
	}
	if row.SessionID == nil || *row.SessionID != sid {
		t.Fatalf("shell row session = %v, want %q", row.SessionID, sid)
	}
	if row.Intent != "git push" {
		t.Fatalf("shell row intent = %q, want the shell's own line", row.Intent)
	}

	ids := ledgerQueryPaneIDs(t, e.conn, shellLedgerPane, 90)
	found := false
	for _, id := range ids {
		if id == string(shellID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("ledger.query for the pane = %v, want the shell entry %q", ids, string(shellID))
	}
}

// Criterion 2: a command submitted from nocx's input line still produces
// EXACTLY ONE entry, not two. The shell's authenticated start attaches to the
// app attempt as an alias — the ordinary production shape has the shell mint
// its OWN id — and the completion names the APP id, because the alias is a
// start-side name only (applyComplete reads the attempt map; the alias is
// never a key).
func TestSubmitAttempt_StartAttachingProducesExactlyOneEntry(t *testing.T) {
	e, pub, lane, h, _, db := newLifecycleLedgerEnv(t, true)
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt", lifecycleSubmitParams(string(h.Domain), "make"), 41))

	shellID := lifecycle.AttemptID("att-shell-9")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(&shellID, "make")))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(got.ID), 0, lifecycleFence(0x44))))

	row := mustEntry(t, db, got.ID)
	if row.Phase != content.PhaseClosed || row.Status != content.EntrySuccess {
		t.Fatalf("app row = phase=%q status=%q, want closed/success", row.Phase, row.Status)
	}
	if n := entryCount(t, db); n != 1 {
		t.Fatalf("entry count after submit + shell start + completion = %d, want exactly one", n)
	}
	aliasRow, err := db.Ledger().Entry(context.Background(), string(shellID))
	if err != nil {
		t.Fatalf("Entry(shell alias id): %v", err)
	}
	if aliasRow != nil {
		t.Fatalf("the shell's own id opened a second row: %+v", aliasRow)
	}
}

// Criterion 3: a shell-originated command containing a secret is stored
// masked — the same backend masking pass history.record and the submit path
// apply, with the same receipt on entries.payload.
func TestShellOriginatedStart_MasksTheCommandLikeHistoryRecord(t *testing.T) {
	_, pub, lane, h, _, db := newLifecycleLedgerEnv(t, true)
	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ" //nolint:gosec // synthetic detector fixture
	shellID := lifecycle.AttemptID("att-shell-2")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(&shellID, "deploy --token="+secret)))

	row := mustEntry(t, db, string(shellID))
	if strings.Contains(row.Intent, secret) || !strings.Contains(row.Intent, "sk-a...GHIJ") {
		t.Fatalf("stored intent = %q, want masked secret", row.Intent)
	}
	masking, err := content.EntryMaskingOf(row.Payload)
	if err != nil {
		t.Fatalf("EntryMaskingOf: %v", err)
	}
	if masking.MaskedCount != 1 || len(masking.Redactions) != 1 {
		t.Fatalf("stored masking receipt = %+v, want one redaction", masking)
	}
}

// Criterion 4, the pair: with output retention off the shell-originated entry
// is still recorded and no output is kept; with retention on the output is
// kept as today. CaptureOutput's refusals are the store's own — the entry's
// existence is what this change adds.
func TestShellOriginatedEntry_OutputRetentionPair(t *testing.T) {
	t.Run("retention off: entry recorded, no output kept", func(t *testing.T) {
		policy := content.NewPolicy()
		policy.SetOutputEnabled(false)
		db := newLedgerStoreWithPolicy(t, policy)
		_, pub, lane, h, _, _ := newLifecycleLedgerEnvWithStore(t, db)

		shellID := lifecycle.AttemptID("att-shell-3")
		shellStartComplete(t, pub, lane, h, 2, shellID, "cat secrets.txt", 0)
		if row := mustEntry(t, db, string(shellID)); row.Phase != content.PhaseClosed {
			t.Fatalf("row with retention off = phase=%q, want closed — the entry itself is kept", row.Phase)
		}

		kept, err := db.Ledger().CaptureOutput(context.Background(), content.CaptureOutput{
			EntryID:        string(shellID),
			ArtifactID:     shellLedgerArtifact1,
			MediaType:      content.MediaText,
			CaptureMethod:  content.CaptureTerminalCells,
			CaptureVersion: 1,
			Seq:            1,
			Body:           []byte("the output"),
		})
		if err != nil {
			t.Fatalf("CaptureOutput with retention off: %v, want nil (a refusal, not a failure)", err)
		}
		if kept {
			t.Fatal("output kept while retention is off")
		}
	})

	t.Run("retention on: output kept as today", func(t *testing.T) {
		db := newLedgerStore(t)
		_, pub, lane, h, _, _ := newLifecycleLedgerEnvWithStore(t, db)

		shellID := lifecycle.AttemptID("att-shell-4")
		shellStartComplete(t, pub, lane, h, 2, shellID, "cat report.txt", 0)

		kept, err := db.Ledger().CaptureOutput(context.Background(), content.CaptureOutput{
			EntryID:        string(shellID),
			ArtifactID:     shellLedgerArtifact2,
			MediaType:      content.MediaText,
			CaptureMethod:  content.CaptureTerminalCells,
			CaptureVersion: 1,
			Seq:            1,
			Body:           []byte("the output"),
		})
		if err != nil {
			t.Fatalf("CaptureOutput with retention on: %v", err)
		}
		if !kept {
			t.Fatal("output not kept while retention is on")
		}
		art, err := db.Ledger().Artifact(context.Background(), shellLedgerArtifact2)
		if err != nil {
			t.Fatalf("Artifact: %v", err)
		}
		if art == nil || len(art.Chunks) == 0 || string(art.Chunks[0]) != "the output" {
			t.Fatalf("stored artifact = %+v, want the body readable", art)
		}
	})
}

// A shell start naming no command is a bare newline, not an execution — the
// submit path's own rule. It opens no entry.
func TestShellOriginatedStart_EmptyCommandOpensNoEntry(t *testing.T) {
	_, pub, lane, h, _, db := newLifecycleLedgerEnv(t, true)
	shellID := lifecycle.AttemptID("att-shell-5")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycleStartEvt(&shellID, "")))

	row, err := db.Ledger().Entry(context.Background(), string(shellID))
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if row != nil {
		t.Fatalf("an empty command opened an entry: %+v", row)
	}
}
