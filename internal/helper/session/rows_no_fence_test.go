package session

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/sessionruntime"
)

// endNoFence reads the noFence the end marker's own payload carries.
func endNoFence(t *testing.T, payload []byte) bool {
	t.Helper()
	var doc struct {
		NoFence *bool `json:"noFence"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("decode the end marker's payload: %v", err)
	}
	if doc.NoFence == nil {
		t.Fatalf("the end marker's payload names no noFence: %s", payload)
	}
	return *doc.NoFence
}

// An interval the runtime settled without its fence (ADR-0074 decision 3,
// amended by nocx-n5ent) reaches the wire saying so, over the real runtime and
// the real bridge (nocx-2v80t.3.29): the coordinator stores the block from this
// marker, and a marker that says nothing is a block that reads whole with its
// closing screen missing. The payload satisfies its contract.
//
// The amendment moved only WHEN the settle is taken. The second completion
// arrives on the authenticated channel, which the command's own bytes are not
// ordered against, so it defers: the interval keeps its own rows until the byte
// stream reaches its next boundary. The later command's fence then flushes the
// parked interval as a no-fence end and closes the later interval with a fenced
// end. Both markers must reach the bridge in that order.
func TestTheBridgeSaysAnIntervalWasSettledWithoutItsFence(t *testing.T) {
	_, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 30)
	var parked, later sessionruntime.FenceNonce
	for i := range parked {
		parked[i], later[i] = 0xA1, 0xA2
	}
	rt.Completed(rt.Incarnation(), parked, 0)
	rt.Completed(rt.Incarnation(), later, 0) // the next event: parked's fence will not come
	// The byte stream reaches its next boundary: the later command's own fence.
	if err := rt.SightFence(later, []byte("$ ")); err != nil {
		t.Fatalf("sight the next interval's fence: %v", err)
	}
	sink.waitFor(1, 2, 0)

	ends := sink.endFrames()
	if len(ends) != 2 {
		t.Fatalf("the pump sent %d end markers, want the parked and later intervals", len(ends))
	}
	if !endNoFence(t, ends[0].Payload) {
		t.Fatal("the parked interval's end marker says its fence arrived")
	}
	if endNoFence(t, ends[1].Payload) {
		t.Fatal("the later fenced interval's end marker says its fence never arrived")
	}
	schema := loadRowSchema(t, "session.interval-end.schema.json")
	validateRowSchema(t, schema, ends[0].Payload)
	validateRowSchema(t, schema, ends[1].Payload)
}

// Paired: an ordinary fenced boundary's marker claims no missing fence.
func TestTheBridgeSaysAFencedIntervalIsWhole(t *testing.T) {
	_, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 30)
	var nonce sessionruntime.FenceNonce
	for i := range nonce {
		nonce[i] = 0xA3
	}
	rt.Completed(rt.Incarnation(), nonce, 0)
	if err := rt.SightFence(nonce, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}
	sink.waitFor(1, 1, 0)

	ends := sink.endFrames()
	if len(ends) != 1 {
		t.Fatalf("the pump sent %d end markers, want 1", len(ends))
	}
	if endNoFence(t, ends[0].Payload) {
		t.Fatal("a fenced interval's end marker says its fence never arrived")
	}
	validateRowSchema(t, loadRowSchema(t, "session.interval-end.schema.json"), ends[0].Payload)
}
