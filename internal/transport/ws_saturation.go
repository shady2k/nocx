// The wire form of a refused control request.
//
// The bounded executor (internal/transport/control) admits or refuses control
// work instead of queueing it unboundedly. Refusal is a NEW outcome for the
// user: today an operation always eventually runs, because everything waits.
// This file gives the refusal a stable wire shape so the renderer can make
// it visible — a JSON-RPC error with a machine-readable data payload for a
// request that carried an id, and the control.saturated notification params
// for one that did not (no id means no response to carry the error in).
//
// The payload is fixed vocabulary. Reason is normalized to the literal
// "control-saturated" — the renderer's discriminator — never the rejection's
// free text. Scope passes through as the admission's resource name, which is
// server vocabulary by construction: it is set at the composition root from
// a closed set (control.go: Name identifies the resource), and a request can
// never influence it. The mapper additionally takes the registered method
// name so the response seam can diagnose an in-handler refusal. Both inputs
// are server vocabulary; request params remain structurally unreachable.
package transport

import (
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/transport/control"
)

// SaturationErrorCode is the JSON-RPC error code for a refused control
// request, in the server-error range (-32000..-32099).
//
// NOT -32001: that code already means vault-sealed (ws_vault.go), and two
// error classes may not share one code. -32004 is the next free application
// code in the range, after the vault's -32000..-32003.
const SaturationErrorCode = -32004

// SaturationMessage is the message of the refusal error. Short and fixed:
// the machine-readable payload, not the prose, is what the renderer acts on.
const SaturationMessage = "Control plane busy"

// saturationReason is the fixed wire vocabulary for a refusal. The renderer
// matches on this literal (frontend/src/dispatcher.ts); a new refusal class
// would extend the vocabulary deliberately, schema and frontend together.
const saturationReason = "control-saturated"

// saturationData is the data payload of the refusal error. Every field is
// contracted in contracts/control.saturated.schema.json: exact key set
// (additionalProperties: false) and explicit required list.
type saturationData struct {
	Reason       string `json:"reason"`
	Scope        string `json:"scope"`
	Retryable    bool   `json:"retryable"`
	RetryAfterMs int64  `json:"retryAfterMs"`
}

// saturationError is the JSON-RPC error envelope for a refused control
// request. Data is not omitempty: the payload is the point of the error.
type saturationError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    saturationData `json:"data"`
}

// saturatedNotificationParams is the params of the control.saturated
// server-to-client notification: refused work with no id has no response to
// carry the error, so the server emits this instead, rate-limited. methodClass
// is the server's coarse class of the refused method (e.g. "ssh", "session") —
// never the raw method name — and scope is the same admission vocabulary as
// the error payload. Contract: contracts/control.saturated.notification.schema.json.
type saturatedNotificationParams struct {
	MethodClass string `json:"methodClass"`
	Scope       string `json:"scope"`
}

// saturationErrorFor maps a control.Rejection to the stable wire error.
//
// r MUST be non-nil — a nil *Rejection means success (control.go) and must
// never reach this function. Only RetryAfter influences the payload: reason
// is the fixed literal, and scope is the rejection's own admission name, so
// an error built here can never carry request text. A negative RetryAfter
// (the struct has no invariant) is clamped to 0 — the schema pins
// retryAfterMs at minimum 0, and a negative hint is meaningless.
func saturationErrorFor(r *control.Rejection) saturationError {
	retryAfterMs := r.RetryAfter.Milliseconds()
	if retryAfterMs < 0 {
		retryAfterMs = 0
	}
	return saturationError{
		Code:    SaturationErrorCode,
		Message: SaturationMessage,
		Data: saturationData{
			Reason:       saturationReason,
			Scope:        r.Scope,
			Retryable:    true, // saturation is transient; capacity frees
			RetryAfterMs: retryAfterMs,
		},
	}
}

// saturatedNotificationParamsFor builds the params of the control.saturated
// notification. methodClass and scope are both server vocabulary — the
// method's coarse class and the saturated admission's name — so the builder
// takes no request data and none can leak.
func saturatedNotificationParamsFor(methodClass, scope string) saturatedNotificationParams {
	return saturatedNotificationParams{
		MethodClass: methodClass,
		Scope:       scope,
	}
}

// saturationRPCError maps a control.Rejection to the RPCError a refused
// control handler answers with. Method is internal diagnostic metadata: the
// Responder does not serialize it, but uses it to name in-handler refusals
// that bypass dispatch admission.
func saturationRPCError(method string, r *control.Rejection) RPCError {
	sat := saturationErrorFor(r)
	return RPCError{Code: sat.Code, Message: sat.Message, Data: sat.Data, method: method}
}

// logSaturationRefusal emits only registered server vocabulary. Request
// params, frame bytes, and the rejection's free-text reason are absent from
// the signature, so this diagnostic cannot disclose them.
func logSaturationRefusal(logger log.Logger, method, disposition string, data saturationData) {
	logger.Debug("control action refused",
		"method", method,
		"methodClass", methodClassFor(method),
		"scope", data.Scope,
		"disposition", disposition,
		"retryAfterMs", data.RetryAfterMs)
}
