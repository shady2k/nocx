package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

type sessionRecoveryStatusParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
}

type sessionRecoveryStatusResult struct {
	SessionID string        `json:"sessionId"`
	Produced  uint64        `json:"produced"`
	Gaps      []content.Gap `json:"gaps"`
}

type sessionRecoveryStatusHandlers struct {
	ops      *capability.SessionOperations
	store    SessionOutputRecorder
	instance session.InstanceID
	r        Responder
}

func (h sessionRecoveryStatusHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	var params sessionRecoveryStatusParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId is required"})
		return
	}
	sid := session.ID(params.SessionID)
	if foreignInstance(h.instance, params.InstanceID) {
		_ = h.r.TryError(req.ID, foreignInstanceRefusal())
		return
	}
	op, err := h.ops.ForSession(sid)
	if err != nil {
		_ = h.r.TryError(req.ID, refuseClaim(reasonUnknownSession, "Invalid params: unknown sessionId"))
		return
	}
	var refusal *RPCError
	runErr := op.Run(ctx, func(_ context.Context, svc capability.SessionService) error {
		sess, getErr := svc.Get(sid)
		if getErr != nil {
			sess = nil
		}
		if r := judgeClaim(h.instance, params.InstanceID, params.SessionEpoch, sess); r != nil {
			refusal = r
		}
		return nil
	})
	if runErr != nil {
		answerOperationRefusal(h.r, req, runErr)
		return
	}
	if refusal != nil {
		_ = h.r.TryError(req.ID, *refusal)
		return
	}
	status, err := h.store.RecoveryStatus(ctx, string(sid))
	if err != nil {
		log.From(ctx).Warn("session recovery status could not be read", "session_id", string(sid), "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprint(err)})
		return
	}
	if status.Gaps == nil {
		status.Gaps = []content.Gap{}
	}
	if msg := unsafeSessionOffset("produced", status.Produced); msg != "" {
		log.From(ctx).Error("session recovery status exceeds the JSON-safe byte-offset boundary", "session_id", string(sid), "error", msg)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: msg})
		return
	}
	for i, gap := range status.Gaps {
		if gap.Start < 0 || gap.End < 0 {
			msg := fmt.Sprintf("gaps[%d] has a negative byte offset", i)
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: msg})
			return
		}
		if msg := unsafeSessionOffset(fmt.Sprintf("gaps[%d].start", i), uint64(gap.Start)); msg != "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: msg})
			return
		}
		if msg := unsafeSessionOffset(fmt.Sprintf("gaps[%d].end", i), uint64(gap.End)); msg != "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: msg})
			return
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(sessionRecoveryStatusResult{
		SessionID: string(sid), Produced: status.Produced, Gaps: status.Gaps,
	}))
}

func validateSessionRecoveryStatusRaw(raw json.RawMessage) string {
	var params sessionRecoveryStatusParams
	if msg := decodeObject(raw, &params, "sessionId", "instanceId", "sessionEpoch"); msg != "" {
		return msg
	}
	if msg := validateSessionIDShape(params.SessionID); msg != "" {
		return "sessionId " + msg
	}
	if msg := validateSessionIDShape(params.InstanceID); msg != "" {
		return "instanceId " + msg
	}
	if params.SessionEpoch == 0 {
		return "sessionEpoch starts at 1"
	}
	return ""
}
