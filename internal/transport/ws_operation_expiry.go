// The wire form of a call that reached a declared deadline.
//
// ADR-8eej3e66 removed the capacity answer entirely (stage 8): no admission
// refuses for capacity, so there is no -32004 and no control.saturated
// notification. What a person can still meet is a call that did not start —
// its turn ran out while it waited for a resource — or a call whose work
// outlived its max. Both are the catalogue's own answer (ADR-8eej3e66 §3):
// before any effect, "not-started"; after one, the entry's declared expired
// outcome.
//
// The code is -32005, which the git domain and the mutation-replay refusal
// already answer with: one code for one fact — the call's declared lifetime
// ran out — and the reason tells the three outcomes apart. The payload is
// fixed server vocabulary (contracts/operation.expired.schema.json): the
// reason is the catalogue's enum, and it is the only field.
package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// OperationExpiredCode is the JSON-RPC error code for a call that did not
// start or whose work reached its declared max, in the server-error range
// (-32000..-32099).
//
// NOT -32004: that code was the control plane's capacity refusal, and stage 8
// removed it with the lane. -32005 was already the code for a declared
// lifetime running out (the git domain, the mutation replay), and there is one
// fact here, not two.
const OperationExpiredCode = -32005

// operationExpiredMessage is the fixed message for a call that did not start.
// Short on purpose: the reason is machine-readable and the surface owns the
// sentence a person reads.
const operationExpiredMessage = "The call did not start"

// operationExpiredData is the data payload of the expiry error. Every field is
// contracted in contracts/operation.expired.schema.json: exact key set and an
// explicit required list.
type operationExpiredData struct {
	Reason string `json:"reason"`
}

// answerOperationExpiry answers a call whose declared lifetime ran out: the
// declaration's own expired outcome as the reason, and nothing else. It is the
// ONE place that shape is written, so the git domain, the filesystem's failure
// domains and a gate's turn all answer the same wire.
func answerOperationExpiry(r Responder, req jsonrpcRequest, reason catalog.Expired, message string) {
	if message == "" {
		message = operationExpiredMessage
	}
	_ = r.TryError(req.ID, RPCError{
		Code:    OperationExpiredCode,
		Message: message,
		Data:    operationExpiredData{Reason: string(reason)},
	})
}

// answerSubmissionRejection answers a request a Submission or a handler's
// admission would not run. A wait that ran out is the declaration's
// "not-started"; the caller's own cancellation is answered as a cancellation;
// anything else — a submission closed by shutdown, a quarantine — is the
// unavailable answer, which is a fact about the resource and not a retry hint.
//
// The scope and the holders travel into diagnostics, not onto the wire: the
// payload is closed vocabulary, and a request can never influence it.
func answerSubmissionRejection(ctx context.Context, r Responder, req jsonrpcRequest, rej *control.Rejection) {
	control.ReportRefusal(ctx, *rej)
	answerOperationRejection(r, req, rej)
}

// answerOperationRejection writes the answer without reporting it: a caller
// that already holds the diagnostic context reports there.
func answerOperationRejection(r Responder, req jsonrpcRequest, rej *control.Rejection) {
	switch rej.Kind {
	case control.RejectionCancelled:
		_ = r.TryError(req.ID, RPCError{Code: -32800, Message: "Request cancelled: it was withdrawn before it started"})
	case control.RejectionWaitExceeded:
		answerOperationExpiry(r, req, catalog.ExpiredNotStarted, "")
	case control.RejectionUnavailable:
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: rej.Reason})
	default:
		// No other kind exists: an admission answers a person with a wait, a
		// cancellation or an unavailable resource (control.RejectionKind).
		_ = r.TryError(req.ID, RPCError{Code: -32603, Message: rej.Reason})
	}
}
