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

func TestSharedRowPoolChargesTransientResendAndFIFOCopies(t *testing.T) {
	markerBytes := emissionBytes(rowEmission{incomplete: true})
	row := []emulator.Row{textRow("retained")}
	retainedBytes := emissionBytes(rowEmission{rows: row})
	hs := &hostSession{rowBufferBytes: markerBytes + int64(maxResendEnds)*int64(unsafe.Sizeof(droppedEnd{})) + 2*retainedBytes + 1024}
	charged, ok := hs.chargeRetainedRows(row)
	if !ok || charged != retainedBytes {
		t.Fatalf("retained charge = (%d,%v), want (%d,true)", charged, ok, retainedBytes)
	}
	hs.enqueueRowEmission(rowEmission{from: 0, rows: row})
	if got, max := poolBytes(t, hs), hs.rowBufferBytes; got > max {
		t.Fatalf("simultaneous resend + FIFO owners charged %d > budget %d", got, max)
	}
	if got := hs.rowPool.ownerBytes(rowOwnerResend); got != retainedBytes {
		t.Fatalf("resend owner charged %d, want %d", got, retainedBytes)
	}
	if got := hs.rowQueuedBytes; got != emissionBytes(rowEmission{from: 0, rows: row}) {
		t.Fatalf("FIFO charge = %d", got)
	}
	hs.releaseRetainedRows(charged)
	if got := poolBytes(t, hs); got != hs.rowQueuedBytes {
		t.Fatalf("after resend release total=%d, FIFO=%d", got, hs.rowQueuedBytes)
	}
}

func TestSharedRowPoolOverflowMarksRowAndResumesAfterEnd(t *testing.T) {
	marker := rowEmission{incomplete: true}
	markerBytes := emissionBytes(marker)
	row := []emulator.Row{textRow("row")}
	rowBytes := emissionBytes(rowEmission{rows: row})
	hs := &hostSession{rowBufferBytes: markerBytes + int64(maxResendEnds)*int64(unsafe.Sizeof(droppedEnd{})) + rowBytes, log: slog.Default()}
	// The retained window owns the normal capacity. The next row cannot be
	// admitted, but the carved-out marker charge still fits the same pool.
	retainedBytes, ok := hs.chargeRetainedRows(row)
	if !ok {
		t.Fatal("could not charge retained row")
	}
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
	hs.releaseRetainedRows(retainedBytes)
	if em, ok := hs.dequeueRowEmission(); !ok || !em.incomplete || em.from != 17 {
		t.Fatalf("marker dequeue=(%+v,%v)", em, ok)
	}
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
