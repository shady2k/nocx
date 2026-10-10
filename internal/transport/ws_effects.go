package transport

import (
	"strconv"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
)

type sessionEffectParams struct {
	SessionID  string `json:"sessionId"`
	Generation string `json:"generation"`
	EffectID   string `json:"effectId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Body       string `json:"body"`
}

func effectKindName(kind proto.EffectKind) (string, bool) {
	switch kind {
	case proto.EffectBell:
		return "bell", true
	case proto.EffectNotification:
		return "notification", true
	case proto.EffectClipboard:
		return "clipboard", true
	case proto.EffectTitle:
		return "title", true
	case proto.EffectCwdReport:
		return "cwd", true
	case proto.EffectPromptBoundary:
		return "promptBoundary", true
	default:
		return "", false
	}
}

// PublishSessionEffect sends one runtime effect to the current session owner.
// It is a control-plane event, never part of a replayable full screen frame.
func (s *WSServer) PublishSessionEffect(sid session.ID, effect proto.EffectFrame) bool {
	kind, ok := effectKindName(effect.Kind)
	if !ok || effect.EffectID == 0 || effect.Generation == 0 {
		return false
	}
	rx := s.getRx(sid)
	if rx == nil {
		return false
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return false
	}
	params := sessionEffectParams{SessionID: string(sid), Generation: strconv.FormatUint(effect.Generation, 10), EffectID: strconv.FormatUint(effect.EffectID, 10), Kind: kind, Title: string(effect.Title), Body: string(effect.Body)}
	waitForBytes := effect.Kind == proto.EffectPromptBoundary && effect.StreamOffset > 0
	offset := effect.StreamOffset
	accepted, coalesced := rx.queueEffect(wconn, offset, waitForBytes, "session.effect", mustMarshal(params))
	if !accepted {
		s.log.Debug("session.effect dropped: pending delivery budget full or subscriber changed", "session", string(sid), "effect_id", effect.EffectID)
		return false
	}
	if coalesced {
		s.log.Debug("coalesced an older pending prompt boundary", "session", string(sid), "effect_id", effect.EffectID)
	}
	if cursor, ok := rx.deliveryCursor(wconn); ok {
		if !rx.enqueueDueEffects(wconn, cursor) {
			// The item remains at the head of the bounded queue. ringToConn
			// retries it through its cancellable outbound-room wait.
			s.log.Debug("session.effect queued behind outbound backpressure", "session", string(sid), "effect_id", effect.EffectID)
		}
	}
	return true
}
