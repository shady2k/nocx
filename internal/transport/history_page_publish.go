package transport

// The history page's publish seam (nocx-zg3k3.10.3): one carrier document —
// a session.historyPageRows payload — on the SAME reserved metadata msg-type
// the screen frame rides (ADR-0073: one carrier, the screen frame's own; a
// page is the frame's sibling cargo, not a third plane). The binary frame
// header keys it by the session, exactly as a frame's is; inside the payload
// the page id keys it to the session.historyPage result that named it.
//
// A document here is NOT superseded by the next revision the way a frame is
// — a frame a queue drops is repainted whole by the next revision, while a
// page dropped is a request that never answers. That difference is why this
// seam ANSWERS, and why the handler turns `false` into the caller's error
// rather than a log line: the drop is a visible decision, both when nobody
// is attached and when the subscriber's own queue refused it.

import (
	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/session"
)

// PublishHistoryPage puts one history page's rows on the data plane for the
// session's current subscriber. It answers whether the document was queued:
// false means nobody was attached or the subscriber's queue refused it, and
// the handler states that to the caller of the read that produced it.
func (s *WSServer) PublishHistoryPage(sid session.ID, doc []byte) bool {
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
		s.log.Warn("history page dropped: session id does not fit the frame header",
			"session_id", string(sid), "error", err)
		return false
	}
	f := Frame{
		Version:   FrameVersion,
		MsgType:   MsgTypeMetadata,
		SessionID: sidBytes,
		Payload:   doc,
	}
	if enqueueErr := wconn.out.TryEnqueueResponseFrame(websocket.BinaryMessage, f.Encode()); enqueueErr != nil {
		// The subscriber is behind and its stall policy is what it sees; the
		// page is not superseded by the next revision, so the refusal is the
		// handler's to answer with, not a line to log and move past.
		s.log.Warn("history page dropped: the subscriber's outbound queue is full",
			"session_id", string(sid), "error", enqueueErr)
		return false
	}
	return true
}
