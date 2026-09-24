package transport

import (
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// TestCloseSession_ClearsPendingHistoryReceipts is nocx-2v80t.3.23: a receipt
// publishClosedAttemptHistory stashes (ws_lifecycle.go's pendingHistoryReceipts)
// waits for PublishLifecycle to send the fact naming the same attempt done
// before it is ever taken back out (nocx-2v80t.3.22). When the session ends
// in that window — its lane unregistered, so the fact that would have
// flushed the receipt can never arrive — nothing removed the entry, and it
// sat there, with its masked command, for the rest of the server's life.
//
// The test ends a session between a stash and the fact that would have
// flushed it (the fact is simply never sent, which is the leak's own
// condition — a deterministic ordering, not a timing race) and asserts
// nothing is left. It is paired with the ordinary flush: a second attempt,
// stashed for a session that stays open, is still there afterwards and still
// comes back through the ordinary takeStashedHistoryRecorded path — closing
// one session must not reach into another's receipts.
func TestCloseSession_ClearsPendingHistoryReceipts(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	s := NewWSServer(logger, newRegWithStub(logger))

	closing := session.ID("00000000000000000000000000000010")
	staying := session.ID("00000000000000000000000000000011")
	s.getOrCreateRx(closing)
	s.getOrCreateRx(staying)

	const lost = lifecycle.AttemptID("attempt-lost")
	const kept = lifecycle.AttemptID("attempt-kept")
	s.stashHistoryRecorded(lost, historyRecordedData{SessionID: closing, AttemptID: string(lost)})
	s.stashHistoryRecorded(kept, historyRecordedData{SessionID: staying, AttemptID: string(kept)})

	// The session ends before its own fact ever reaches PublishLifecycle to
	// flush the stash — exactly the case where nothing else ever would.
	s.closeSession(closing, nil)

	if _, ok := s.takeStashedHistoryRecorded(lost); ok {
		t.Fatal("closeSession left a stashed history receipt behind for the session that just ended")
	}

	// The ordinary flush: an unrelated session's still-pending receipt is
	// untouched by another session's teardown, and the take that would
	// ordinarily follow its own fact still finds it.
	data, ok := s.takeStashedHistoryRecorded(kept)
	if !ok {
		t.Fatal("closeSession of one session dropped a receipt stashed for a different, still-open session")
	}
	if data.SessionID != staying {
		t.Fatalf("takeStashedHistoryRecorded(%q).SessionID = %q, want %q", kept, data.SessionID, staying)
	}
}
