package client_test

// THE PTY READER'S BYTES ARE NOT HELD BEHIND THE ROUND TRIP THAT ACKNOWLEDGES
// THEM EITHER (nocx-da7sd).
//
// `AttachedSession.Read` is the session's PTY read: the ring's source, the
// grid's source, and the reader whose cursor is the helper's PTY credit. It
// made the same synchronous, deadline-less `OpAck` call inside the read that
// the lifecycle reader made, so an ack the helper was slow or stalled on
// delayed bytes that were already the coordinator's — the pane's output, not
// just a completion marker — and the sink behind it (the session's read pump,
// `ring.write`, AD-10's own throttle) saw nothing until the helper answered.
//
// NOT THE SAME AS THE LIFECYCLE CURSOR, and the difference is why this is its
// own test rather than a second case of the first one: the offset this reader
// acks is exactly the credit that reopens the helper's PTY pump
// (`sent - acked` against creditLimit in the helper's session), so the last
// cursor must be sent even if the reader never reads again, or a source the
// helper throttled is never reopened. That is the paired half of the assertion
// below. What does NOT differ is the design the parent of this fix asked to
// have checked: the coordinator's ring credit floor is the FRONTEND's ack
// (`outputRing.creditFloor` takes `acked`, `base` and the pump's own `since`),
// a different cursor with a different owner, and the helper's window reclaim
// does not consult the ack at all (`window.write` reclaims by capacity, D8) —
// so moving this ack off the read path weakens no bound: the helper still
// refuses to send past creditLimit, and the ring still blocks `ring.write`
// when the browser is behind.

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// outputBytes stands in for PTY output — the bytes the ring and the grid are
// fed from. The test asserts those exact bytes.
var outputBytes = []byte("rows\n")

func TestThePtyBytesReachTheReaderWhileTheHelpersAckGoesUnanswered(t *testing.T) {
	attached, svc := attachOverAWithheldAck(t, func(h *host.Host, session, subscriber [16]byte) error {
		return h.SendSessionData(proto.SessionFrame{
			Session: session, Subscriber: subscriber, Payload: outputBytes,
		})
	})

	assertBytesArriveWhileTheAckIsUnanswered(t, svc, attached.Read, outputBytes, ptyCursor)
}

// ptyCursor is the offset the PTY reader's ack names: its own stream position,
// and the helper's credit for this subscriber.
func ptyCursor(p proto.AckParams) (uint64, bool) { return uint64(p.Offset), true }
