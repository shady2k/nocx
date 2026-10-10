package transport

// The post-bind live/resend handoff (nocx-zg3k3.5.11 Round 5, the 2/40
// loaded residual): a live delivery's store call runs outside bs.mu, and
// between its COMMIT and its return the in-memory cursor (block.rows) still
// names the previous delivery. An interval end that resolves in that window
// snapshots the stale cursor and writes its own closing append at rows the
// in-flight delivery owns — on the loaded run the two store transactions
// interleaved, both succeeded, and the sealed body lost them both (256 of
// 300 rows). The deferred-append path has always made its batch visible
// (flushing, set by takeForFlushLocked) so closeBlockRows parks the end
// behind it; the direct path must hold the same invariant: NO close-side
// store op may overlap a delivery that is still in the store.
//
// The store double commits the watched delivery first, THEN holds the call —
// exactly the production window — and records any overlapping close-side
// operation. Red, the close runs during the hold; green, it waits behind
// the flush tail like every deferred close.

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

type inFlightCloseWatchStore struct {
	closeFailureBlockStore
	mu         sync.Mutex
	inFlight   bool
	watching   bool // beginWatch armed the hold; nothing watches before it
	holdFrom   uint64
	overlapped []string // what ran while a delivery was still in the store
	entered    chan struct{}
	release    chan struct{}
}

func (s *inFlightCloseWatchStore) record(what string) {
	s.mu.Lock()
	if s.inFlight {
		s.overlapped = append(s.overlapped, what)
	}
	s.mu.Unlock()
}

func (s *inFlightCloseWatchStore) overlappedSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.overlapped...)
}

func (s *inFlightCloseWatchStore) beginWatch(from uint64) {
	s.mu.Lock()
	s.watching = true
	s.holdFrom = from
	s.mu.Unlock()
}

func (s *inFlightCloseWatchStore) AppendBlockRows(ctx context.Context, in content.AppendBlockRows) error {
	s.record("append at " + strconv.FormatUint(in.FromRow, 10))
	s.mu.Lock()
	watch := s.watching && !s.inFlight && in.FromRow >= s.holdFrom
	if watch {
		s.inFlight = true
	}
	s.mu.Unlock()
	if watch {
		s.entered <- struct{}{}
	}
	err := s.ledger.AppendBlockRows(ctx, in)
	if watch {
		// THE PRODUCTION WINDOW, held: the delivery has COMMITTED — the
		// store cursor already names these rows — and the call has not
		// returned, so block.rows still names the previous delivery.
		<-s.release
		s.mu.Lock()
		s.inFlight = false
		s.mu.Unlock()
	}
	return err
}

func (s *inFlightCloseWatchStore) CloseBlockRows(ctx context.Context, in content.CloseBlockRows) (content.BlockRowsSummary, error) {
	s.record("close")
	return s.ledger.CloseBlockRows(ctx, in)
}

func rowsOf(t *testing.T, from, n int) []emulator.Row {
	t.Helper()
	rows := make([]emulator.Row, 0, n)
	for i := from; i < from+n; i++ {
		rows = append(rows, aStreamRow("row"+strconv.Itoa(i)))
	}
	return rows
}

func TestACloseWaitsForADeliveryThatIsStillInTheStore(t *testing.T) {
	db := newLedgerStore(t)
	e, pub, lane, h, sidStr, _ := newLifecycleLedgerEnvWithStore(t, db)
	store := &inFlightCloseWatchStore{
		closeFailureBlockStore: closeFailureBlockStore{ledger: db.Ledger()},
		entered:                make(chan struct{}, 1),
		release:                make(chan struct{}),
	}
	e.ws.blockRowsStore = store
	sid := session.ID(sidStr)
	e.ws.AttachBlockRows(sid)
	attempt := startsACommand(t, e, pub, lane, h, 2, "make")

	// The command's streamed rows, in live deliveries — all but the last
	// one land unremarkably.
	if up, confirm := e.ws.BlockRowsArrived(sid, 0, 0, rowsOf(t, 0, 128), ""); !confirm || up != 128 {
		t.Fatalf("the first delivery: up=%d confirm=%v, want 128 true", up, confirm)
	}
	if up, confirm := e.ws.BlockRowsArrived(sid, 128, 0, rowsOf(t, 128, 128), ""); !confirm || up != 256 {
		t.Fatalf("the second delivery: up=%d confirm=%v, want 256 true", up, confirm)
	}

	// THE LAST LIVE DELIVERY COMMITS, THEN THE CALL HANGS IN THE STORE —
	// the loaded window: the store cursor is 277, block.rows still says 256.
	store.beginWatch(256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.ws.BlockRowsArrived(sid, 256, 0, rowsOf(t, 256, 21), "")
	}()
	<-store.entered

	// THE COMPLETION'S FENCE IS PUBLISHED FIRST, as the replayed window's
	// completion arrives before the block's end is processed: with the
	// fence registered, the end below RESOLVES and drives the close
	// directly — exactly the concurrent shape the loaded run lost rows to.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3,
		lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, lifecycleFence(9))))

	// THE INTERVAL ENDS INSIDE THAT WINDOW, the way the replayed window's
	// own close arrives while the live pump is still draining: endRow 277,
	// the 23 suffix rows as the closing screen.
	var nonce [32]byte
	for i := range nonce {
		nonce[i] = byte(9)
	}
	e.ws.BlockIntervalEnded(sid, nonce, 277, rowsOf(t, 277, 23), false)

	if leaked := store.overlappedSnapshot(); len(leaked) != 0 {
		t.Fatalf("the close ran while a delivery was still in the store: %v — "+
			"a close must wait behind the in-flight delivery, as the deferred "+
			"path's flushing already makes it", leaked)
	}

	// The delivery lands, and with it the whole handoff: the parked end (if
	// any) closes at the true cursor and the artifact seals with every row.
	close(store.release)
	<-done
	waittest.WaitForDetail(t, "the block to seal with every row of the command", func() string {
		row, rowErr := db.Ledger().Entry(context.Background(), attempt)
		if rowErr != nil || row == nil {
			return "entry unreadable: " + fmtOpenErr(rowErr)
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
				return "artifact state " + string(art.State)
			}
		}
		return "no block artifact on the entry yet"
	}, func() bool {
		row, rowErr := db.Ledger().Entry(context.Background(), attempt)
		if rowErr != nil || row == nil {
			return false
		}
		for _, ex := range row.Executions {
			for _, a := range ex.Artifacts {
				if a.MediaType != content.MediaBlockRows {
					continue
				}
				art, artErr := db.Ledger().Artifact(context.Background(), a.ID)
				if artErr != nil || art == nil || art.State != content.ArtifactSealed {
					return false
				}
				return true
			}
		}
		return false
	})
	body := blockRowsBody(t, db, attempt)
	// The stored rows are one newline-terminated line each, so the split's
	// trailing empty element is not a row (the settle test trims the same
	// way).
	got := len(strings.Split(strings.TrimSuffix(body, "\n"), "\n"))
	if got != 300 {
		t.Fatalf("the sealed block holds %d rows, want the command's whole 300", got)
	}
}
