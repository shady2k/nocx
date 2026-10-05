package session

import (
	"encoding/json"
	"log/slog"
	"testing"
	"unsafe"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

func TestSharedRowPoolProductionEnqueueAndPrefixReclaim(t *testing.T) {
	markerBytes := emissionBytes(rowEmission{incomplete: true})
	row := []emulator.Row{textRow("retained")}
	rowBytes := emissionBytes(rowEmission{rows: row})
	endReserve := int64(maxResendEnds) * int64(unsafe.Sizeof(droppedEnd{}))
	// The queue and resend window refer to the same backing rows. Count cell
	// data once, plus the FIFO's emission record. The marker and dropped-end
	// reserves stay carved out of that same budget.
	hs := &hostSession{rowBufferBytes: markerBytes + endReserve + rowBytes + int64(unsafe.Sizeof(rowEmission{})), log: slog.Default()}
	hs.enqueueRowEmission(rowEmission{from: 0, rows: row})
	if len(hs.rowQueue) != 1 || hs.rowQueue[0].from != 0 {
		t.Fatalf("production enqueue did not queue rows: %+v", hs.rowQueue)
	}
	if got, want := poolBytes(t, hs), rowBytes+int64(unsafe.Sizeof(rowEmission{})); got != want {
		t.Fatalf("enqueue pool charge=%d, want shared rows plus FIFO record=%d", got, want)
	}
	em, ok := hs.dequeueRowEmission()
	if !ok {
		t.Fatal("queued row emission could not be dequeued")
	}
	hs.releaseRowEmission(em)
	if got, want := poolBytes(t, hs), rowBytes; got != want {
		t.Fatalf("after delivery pool charge=%d, want retained window=%d", got, want)
	}
	if got := hs.reclaimRetainedPrefix(1); got != 1 {
		t.Fatalf("reclaimed prefix=%d, want 1", got)
	}
	if got := poolBytes(t, hs); got != 0 {
		t.Fatalf("prefix reclaim left %d bytes charged", got)
	}
	hs.enqueueRowEmission(rowEmission{from: 1, rows: row})
	if len(hs.rowQueue) != 1 || hs.rowQueue[0].from != 1 || hs.rowQueue[0].incomplete {
		t.Fatalf("enqueue after prefix reclaim did not retain rows: %+v", hs.rowQueue)
	}
}

func TestSharedRowPoolOverflowMarksRowAndResumesAfterEnd(t *testing.T) {
	marker := rowEmission{incomplete: true}
	markerBytes := emissionBytes(marker)
	row := []emulator.Row{textRow("row")}
	rowBytes := emissionBytes(rowEmission{rows: row})
	endReserve := int64(maxResendEnds) * int64(unsafe.Sizeof(droppedEnd{}))
	// The shared row data cannot fit in the budget. The bridge emits its
	// marker without retaining either a FIFO or resend copy.
	hs := &hostSession{rowBufferBytes: markerBytes + endReserve + rowBytes/2, log: slog.Default()}
	hs.enqueueRowEmission(rowEmission{from: 17, rows: row})
	if len(hs.rowQueue) != 1 || !hs.rowQueue[0].incomplete || hs.rowQueue[0].from != 17 {
		t.Fatalf("queue does not contain the named incomplete marker: %+v", hs.rowQueue)
	}
	if got := poolBytes(t, hs); got > hs.rowBufferBytes {
		t.Fatalf("pool total %d exceeds budget %d", got, hs.rowBufferBytes)
	}
	if got := hs.rowBufferOverflows.Load(); got != 1 {
		t.Fatalf("overflow counter=%d, want 1", got)
	}
	em, ok := hs.dequeueRowEmission()
	if !ok || !em.incomplete || em.from != 17 {
		t.Fatalf("marker dequeue=(%+v,%v)", em, ok)
	}
	hs.releaseRowEmission(em)
	// Overflow drops through the next end fence; afterward the following
	// command is accepted by the same FIFO and the same pool.
	hs.enqueueRowEmission(rowEmission{from: 18, rows: row})
	if len(hs.rowQueue) != 0 {
		t.Fatal("rows inside the damaged command were recorded")
	}
	nonce := sessionruntime.FenceNonce{1}
	hs.enqueueRowEmission(rowEmission{end: true, from: 19, nonce: nonce})
	if end, ok := hs.dequeueRowEmission(); !ok || !end.end || end.nonce != nonce {
		t.Fatalf("next end marker=(%+v,%v), want its own fence", end, ok)
	}
	hs.enqueueRowEmission(rowEmission{from: 19, rows: row})
	if len(hs.rowQueue) != 1 || hs.rowQueue[0].from != 19 {
		t.Fatalf("recording did not resume after boundary: %+v", hs.rowQueue)
	}
}

func TestRetentionLossAndHelperOverflowHaveDistinctEmissionWordsAndCounters(t *testing.T) {
	retention, err := json.Marshal(proto.OutputRowsDoc{FromRow: 8, LostRows: 3, LostCause: proto.LostCauseCoordinatorUnavailable, Rows: json.RawMessage("[]")})
	if err != nil {
		t.Fatal(err)
	}
	overflow, err := json.Marshal(proto.OutputRowsDoc{FromRow: 8, Rows: json.RawMessage("[]"), Incomplete: true})
	if err != nil {
		t.Fatal(err)
	}
	var retentionDoc, overflowDoc map[string]any
	if err := json.Unmarshal(retention, &retentionDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(overflow, &overflowDoc); err != nil {
		t.Fatal(err)
	}
	if retentionDoc["lostCause"] != proto.LostCauseCoordinatorUnavailable || overflowDoc["incomplete"] != true || overflowDoc["lostCause"] != nil {
		t.Fatalf("losses collapsed together: retention=%s overflow=%s", retention, overflow)
	}
	hs := &hostSession{}
	hs.rowBufferOverflows.Add(2)
	hs.rowsLiveRetentionLost.Add(3)
	if hs.rowBufferOverflows.Load() != 2 || hs.rowsLiveRetentionLost.Load() != 3 {
		t.Fatal("the two loss counters are not independent")
	}
}

func poolBytes(t *testing.T, hs *hostSession) int64 {
	t.Helper()
	hs.rowMu.Lock()
	defer hs.rowMu.Unlock()
	return hs.rowPool.total
}
