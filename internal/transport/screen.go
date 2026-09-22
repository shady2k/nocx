package transport

// The screen plane's publish seam.
//
// The reserved metadata msg-type is the seat the data plane held for exactly
// this: a screen frame is neither PTY bytes (it must never ride MsgTypeData,
// whose pump, ring and credit accounting are the byte stream's — AD-6) nor a
// control-plane call (it is not JSON-RPC, AD-1). It rides the binary framing
// every other plane rides — version, msg-type, session-id, payload — with
// the payload being one whole session.frame document at one revision, as the
// helper's carrier delivered it. The revision is inside the document, where
// the renderer's generated type reads it.
//
// Publishing is per session and goes to the session's CURRENT subscriber —
// the same single slot the PTY pump serves, because one client owns a
// session (D8) and the renderer half of the plane consumes exactly what that
// client sees.
//
// A full outbound queue drops the frame, deliberately. The class permits it
// — every frame is a full snapshot, so the next revision supersedes what was
// lost — and the subscriber is not silently abandoned: the outbound queue's
// own stall policy is what the renderer sees, and the next revision it can
// drain repaints the whole screen. What is refused is the PTY path's answer
// (wait for room), because waiting would put the screen plane's backpressure
// on the byte stream's pump.
//
// Nobody attached is nothing lost: a screen published to nobody has no
// consumer, and the first frame a new subscriber is owed is its baseline —
// the runtime's per-client promise, delivered through the drain on attach.

import (
	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/session"
)

// PublishScreenFrame puts one published screen frame on the data plane for
// the session's current subscriber. It answers whether the frame was queued:
// false means nobody was attached or the subscriber's queue refused it, and
// both are visible decisions rather than silent ones — the drop is logged
// here, and the next revision the runtime publishes repaints whole.
func (s *WSServer) PublishScreenFrame(sid session.ID, revision uint64, doc []byte) bool {
	rx := s.getRx(sid)
	if rx == nil {
		return false
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return false
	}
	sidBytes, err := session.IDToBytes(sid)
	if err != nil {
		s.log.Warn("screen frame dropped: session id does not fit the frame header",
			"session_id", string(sid), "revision", revision, "error", err)
		return false
	}
	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeMetadata,
		SessionID: sidBytes,
		Payload:   doc,
	}
	if enqueueErr := wconn.out.TryEnqueue(websocket.BinaryMessage, f.Encode()); enqueueErr != nil {
		// The subscriber is behind and its stall policy is what it sees; the
		// next revision supersedes this one whole.
		s.log.Warn("screen frame dropped: the subscriber's outbound queue is full",
			"session_id", string(sid), "revision", revision, "error", enqueueErr)
		return false
	}
	return true
}
