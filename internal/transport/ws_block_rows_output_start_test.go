package transport

// nocx-4y82c.1.11 (review 4): the block's output start is RECORDED on its
// artifact when the helper's ordered output mark applies, and it survives
// both ways a block ends. Readers drop the rows below it — the command's own
// echo — so a frozen body shows the echo once whether the mark landed before
// or after the rows streamed.

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

// recordedOutputStart is the artifact payload's outputStartRow, or nil when
// the block records none.
func recordedOutputStart(t *testing.T, db content.ContentDB, entryID string) *uint64 {
	t.Helper()
	art := blockArtifact(t, db, entryID)
	var payload struct {
		OutputStartRow *uint64 `json:"outputStartRow"`
	}
	if art.Payload != "" {
		if err := json.Unmarshal([]byte(art.Payload), &payload); err != nil {
			t.Fatalf("artifact payload is not a JSON object: %v\npayload: %s", err, art.Payload)
		}
	}
	return payload.OutputStartRow
}

func TestTheRecordedOutputStartSurvivesSealAndSessionEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(e *lifecycleTestEnv, sid session.ID, fence lifecycle.FenceNonce)
		rows int
		body string
	}{
		{
			name: "its own end marker seals it",
			end: func(e *lifecycleTestEnv, sid session.ID, fence lifecycle.FenceNonce) {
				e.ws.BlockIntervalEnded(sid, fence, 3, []emulator.Row{aStreamRow("$ ")}, false)
			},
			rows: 4,
			body: "output\n$",
		},
		{
			name: "the session end settles it",
			end: func(e *lifecycleTestEnv, sid session.ID, fence lifecycle.FenceNonce) {
				e.ws.HelperSessionEnded(sid)
			},
			rows: 3,
			body: "output",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pub, lane, h, sidStr, db := newLifecycleLedgerEnv(t, true)
			sid := session.ID(sidStr)
			e.ws.AttachBlockRows(sid)
			e.ws.BlockOutputStartPlaneAttached(sid)
			attempt := startsACommand(t, e, pub, lane, h, 2, "printf output")

			// The command's echo streams into the artifact exactly as it does
			// on main: nothing is held.
			if _, confirm := e.ws.BlockRowsArrived(sid, 0, 0, []emulator.Row{
				aStreamRow("$ printf output"), aStreamRow("output"),
			}, ""); !confirm {
				t.Fatal("the echo rows were not stored")
			}
			// The mark names row 2 as the block's first output row.
			e.ws.BlockOutputStartRow(sid, 2)
			if got := recordedOutputStart(t, db, attempt); got == nil || *got != 2 {
				t.Fatalf("recorded output start = %v, want 2", got)
			}
			if _, confirm := e.ws.BlockRowsArrived(sid, 2, 0, []emulator.Row{aStreamRow("output")}, ""); !confirm {
				t.Fatal("the output row was not stored")
			}

			fence := lifecycleFence(0x91)
			mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
			tc.end(e, sid, fence)

			// The record survives the end: the block is frozen and the read
			// still knows where its output began.
			if got := recordedOutputStart(t, db, attempt); got == nil || *got != 2 {
				t.Fatalf("recorded output start after the end = %v, want 2", got)
			}
			assertBlockSealed(t, db, attempt)
			// The rows themselves are kept, as on main: only the READ is
			// delimited, so a resend, a selection or a future reader still
			// has them.
			if rows := streamRows(t, db, attempt); len(rows) != tc.rows {
				t.Fatalf("artifact holds %d rows, want all %d streamed rows kept: %+v", len(rows), tc.rows, rows)
			}
			// And the body read a tool takes drops the echo.
			art := blockArtifact(t, db, attempt)
			body, err := content.BlockRowsTextAfter(art.Chunks, content.BlockRowsOutputStart(art.Payload))
			if err != nil {
				t.Fatalf("BlockRowsTextAfter: %v", err)
			}
			if body != tc.body {
				t.Fatalf("body read = %q, want %q", body, tc.body)
			}
		})
	}
}
