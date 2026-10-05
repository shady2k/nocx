package client_test

// A COMPLETION IS NOT HELD BEHIND THE ROUND TRIP THAT ACKNOWLEDGES IT
// (nocx-uo8pf).
//
// `attachedLifecycle.Read` copied the bytes out of the attachment's queue and
// then, still inside the same call, made a synchronous `OpAck` request and
// waited for its answer with no deadline. The bridge that hands those bytes to
// the lifecycle channel writes them to the adapter only AFTER Read returns, so
// a completion the helper had already sent sat in that buffer for as long as
// the ack's answer was outstanding: the person's block stays open, and nothing
// in the product says why.
//
// The helper in ack_withholding_test.go answers the handshake and the attach as
// the real one does and then WITHHOLDS the ack, which is the condition driven
// directly rather than through a starved machine. Both halves are asserted: the
// bytes are the reader's while the ack is unanswered, AND the ack still carries
// the cursor those bytes end at. The shape it shares with the PTY reader, and
// why one sender owns it for both, is at `ackPump` in sessions.go.

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// completionBytes stands in for the frame the shell's own precmd writes for a
// finished command. What matters here is only that it is a payload the reader
// must hand over, so the test asserts those exact bytes.
var completionBytes = []byte("complete\n")

func TestTheLifecycleBytesReachTheReaderWhileTheHelpersAckGoesUnanswered(t *testing.T) {
	attached, svc := attachOverAWithheldAck(t, func(h *host.Host, session, subscriber [16]byte) error {
		return h.SendLifecycleData(proto.SessionFrame{
			Session: session, Subscriber: subscriber, Payload: completionBytes,
		})
	})
	lifecycle := attached.Lifecycle()
	t.Cleanup(func() { _ = lifecycle.Close() })

	assertBytesArriveWhileTheAckIsUnanswered(t, svc, lifecycle.Read, completionBytes, lifecycleCursor)
}

// lifecycleCursor is the offset the lifecycle reader's ack names: the lifecycle
// cursor, which rides beside the PTY position the wire requires (nocx-2v80t.3.44).
func lifecycleCursor(p proto.AckParams) (uint64, bool) {
	if p.LifecycleOffset == nil {
		return 0, false
	}
	return uint64(*p.LifecycleOffset), true
}
