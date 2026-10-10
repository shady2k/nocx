package transport

// nocx-zg3k3.5.11 Round 8, the 19/20 loaded residual's arm: the end marker
// resolved by its completion's fence while resent rows were still owed — the
// close found the artifact's cursor short of the end's boundary (cursor <
// endRow), sealed at the short cursor (196/200 of 277 across the loaded
// runs), and every still-owed row the helper had already sent was dropped
// against the settled block, unconfirmed. The captured helper log is
// decisive: `walk mark=0 stop=0 departed=277 ends=0` → `sent spans_end=0
// rows=277 short=0` — the helper sent everything; the coordinator sealed
// early.
//
// The seam drives the exact order: rows land to 177, the end marker arrives
// BEFORE its fence (parked in bs.ends — ADR-0024 decision 7's either-order),
// the completion publishes the fence, and only then do the owed rows
// [177..278) arrive. The invariant: the close must not seal while the
// source is attached and rows in [cursor, endRow) are still owed — it waits
// (parked, without spending the attempt bound), each delivery re-drives it
// at the truer cursor, and the block seals with every row stored.

import (
	"context"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

func rowsOfOwed(t *testing.T, from, n int) []emulator.Row {
	t.Helper()
	rows := make([]emulator.Row, 0, n)
	for i := from; i < from+n; i++ {
		rows = append(rows, aStreamRow("owed"+strconv.Itoa(i)))
	}
	return rows
}

func blockSealedState(t *testing.T, db content.ContentDB, attempt string) (bool, string) {
	t.Helper()
	row, rowErr := db.Ledger().Entry(context.Background(), attempt)
	if rowErr != nil || row == nil {
		return false, "entry unreadable"
	}
	for _, ex := range row.Executions {
		for _, a := range ex.Artifacts {
			if a.MediaType != content.MediaBlockRows {
				continue
			}
			art, artErr := db.Ledger().Artifact(context.Background(), a.ID)
			if artErr != nil || art == nil {
				continue
			}
			return art.State == content.ArtifactSealed, string(art.State)
		}
	}
	return false, "no block artifact"
}

func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

func TestACloseWithRowsStillOwedWaitsForThemInsteadOfSealing(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sidStr, _ := newLifecycleLedgerEnvWithStore(t, db)
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")

	// The interval streams [0..177) and the coordinator stores it all.
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, rowsOfOwed(t, 0, 177), ""); !confirm || up != 177 {
		t.Fatalf("the head delivery: up=%d confirm=%v, want 177 true", up, confirm)
	}

	// THE END MARKER ARRIVES BEFORE ITS FENCE: it parks in bs.ends (the
	// either-order of ADR-0024 decision 7), unresolved.
	var nonce [32]byte
	for i := range nonce {
		nonce[i] = byte(9)
	}
	e.ws.BlockIntervalEnded(sid, nonce, 278, nil, false)

	// THE COMPLETION PUBLISHES THE FENCE: the parked end resolves and the
	// close runs — with rows [177..278) still owed on the ordered channel.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3,
		lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, lifecycleFence(9))))

	// THE INVARIANT UNDER TEST: the close must NOT seal while the source is
	// attached and [cursor, endRow) is still owed. Red, it sealed at 177
	// here and every later row dropped against the settled block.
	if sealed, state := blockSealedState(t, db, attempt); sealed {
		t.Fatalf("the close sealed while rows [177, 278) were still owed (artifact %s) — "+
			"it must wait behind the owed rows, each delivery re-driving it", state)
	}

	// THE OWED ROWS ARRIVE, in the walk's batches; each re-drives the close
	// at the truer cursor.
	if up, confirm := e.ws.BlockRowsArrived(sid, 177, 0, rowsOfOwed(t, 177, 53), ""); !confirm {
		t.Fatalf("the first owed batch did not store: up=%d confirm=%v", up, confirm)
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 230, 0, rowsOfOwed(t, 230, 48), ""); !confirm {
		t.Fatalf("the second owed batch did not store: up=%d confirm=%v", up, confirm)
	}

	// The retried close seals with every row.
	waittest.WaitForDetail(t, "the block to seal with every row of the command", func() string {
		_, state := blockSealedState(t, db, attempt)
		return "artifact state " + state
	}, func() bool {
		sealed, _ := blockSealedState(t, db, attempt)
		return sealed
	})
	body := blockRowsBody(t, db, attempt)
	got := len(splitBlockLines(trimTrailingNewline(body)))
	if got != 278 {
		t.Fatalf("the sealed block holds %d rows, want the command's whole 278", got)
	}
}
