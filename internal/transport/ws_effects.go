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
	params := sessionEffectParams{SessionID: string(sid), Generation: strconv.FormatUint(effect.Generation, 10), EffectID: strconv.FormatUint(effect.EffectID, 10), Kind: kind, Body: string(effect.Body)}
	if err := wconn.TryNotify("session.effect", mustMarshal(params)); err != nil {
		s.log.Debug("session.effect dropped", "session", string(sid), "effect_id", effect.EffectID, "error", err)
		return false
	}
	return true
}
