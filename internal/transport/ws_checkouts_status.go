package transport

// checkouts.status — the ONE way the product says whether the automatic
// sweep of nocx-made worker checkouts can actually run, and why not
// (nocx-xn63t.1.6).
//
// The failure this exists for is the one AGENTS.md names: with the content
// store unavailable the durable checkout record is not wired, so the sweep
// can judge nothing and remove nothing — while the Settings screen goes on
// offering "Remove unused worker checkouts after" governing nothing. A
// silent degrade the UI contradicts is how a feature that does not exist
// survives a release.
//
// ── Why a second status and not HistoryStatus ────────────────────────────
//
// HistoryStatus says whether durable COMMAND HISTORY is running, in that
// surface's own closed words; raising a checkout failure there would put
// history's sentence on the checkout's notice and give one fact two owners.
// This surface is to the checkout sweep what that one is to history, and no
// more than that.
//
// ── Why no statusChanged notification ────────────────────────────────────
//
// The degrade is decided once, by the composition root, before the
// transport starts, and the store never un-opens at runtime: the answer
// cannot change under a connected renderer. So the shape is a plain status
// read the renderer makes when the section that cares renders — the
// raise/clear episode machinery of history.status has no episode here to
// announce.
//
// The reason set is closed in the house style of history.status's: the
// renderer picks its own sentence from the code rather than parsing prose,
// so a backend rewording never changes what a person reads.
// contracts/checkouts.status.schema.json holds the same enum.

import (
	"context"
	"sync"

	"github.com/shady2k/nocx/internal/transport/control"
)

// CheckoutSweepDegradeReason is the closed set of reasons the checkout
// sweep cannot run.
type CheckoutSweepDegradeReason string

// CheckoutSweepDegradeNoRecord — the durable checkout record is not wired,
// which is what the content store failing to open IS: nothing can be
// judged, so nothing may be removed. Raised by the composition root before
// the transport starts.
//
// CheckoutSweepDegradeRecordWrites — the record refused a WRITE at runtime
// (a creation row, or a last-used stamp): the stamps it holds may all be
// stale, so the sweep trusts none and ages nothing. Raised by the record
// service the first time a write fails, and sticky for the life of the
// process — there is still no Clear, because the safe direction is never
// to remove on a stamp that may be a lie. A client attached when the write
// failed reads the degrade on its next read of this method; there is
// deliberately no push notification, the same as for the composition-time
// raise.
const (
	CheckoutSweepDegradeNoRecord     CheckoutSweepDegradeReason = "noRecord"
	CheckoutSweepDegradeRecordWrites CheckoutSweepDegradeReason = "recordWrites"
)

// checkoutsStatusResponse is the wire shape of checkouts.status, pinned by
// the DTO contract test. The pointers are null exactly as the schema says:
// reason and detail are null if and only if Available is true — a status
// without a why is a dead end for the person reading it.
type checkoutsStatusResponse struct {
	Available bool                        `json:"available"`
	Reason    *CheckoutSweepDegradeReason `json:"reason"`
	Detail    *string                     `json:"detail"`
}

// CheckoutSweepStatus is the composition root's answer about whether the
// sweep can run: raised on the store-failure path, beside the history
// status raise it sits beside, before the transport ever starts.
type CheckoutSweepStatus struct {
	mu        sync.Mutex
	available bool
	reason    CheckoutSweepDegradeReason
	detail    string
}

func NewCheckoutSweepStatus() *CheckoutSweepStatus {
	return &CheckoutSweepStatus{available: true}
}

// RaiseUnavailable records that the sweep cannot run, for this reason, with
// this detail (the underlying error's own words). Called before the
// transport starts; there is no Clear, because the store never un-opens.
func (s *CheckoutSweepStatus) RaiseUnavailable(reason CheckoutSweepDegradeReason, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.available = false
	s.reason = reason
	s.detail = detail
}

func (s *CheckoutSweepStatus) snapshot() checkoutsStatusResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := checkoutsStatusResponse{Available: s.available}
	if !s.available {
		reason := s.reason
		resp.Reason = &reason
		if s.detail != "" {
			detail := s.detail
			resp.Detail = &detail
		}
	}
	return resp
}

// WithCheckoutSweepStatus attaches the checkout sweep's status. When absent,
// checkouts.status answers "able to run": a server nobody told otherwise has
// nothing to report, and the method must still answer, because a renderer
// reads it on every render of the Worktrees settings and a method that
// vanishes is a worse answer than a boolean.
func WithCheckoutSweepStatus(st *CheckoutSweepStatus) WSServerOption {
	return func(s *WSServer) { s.checkoutSweepStatus = st }
}

// checkoutsStatusSnapshot is the server's view of the status, defaulting to
// "able to run" when no status was wired.
func (s *WSServer) checkoutsStatusSnapshot() checkoutsStatusResponse {
	if s.checkoutSweepStatus == nil {
		return checkoutsStatusResponse{Available: true}
	}
	return s.checkoutSweepStatus.snapshot()
}

// checkoutsStatusHandlers answers checkouts.status.
type checkoutsStatusHandlers struct {
	snapshot func() checkoutsStatusResponse
	r        Responder
}

func (h checkoutsStatusHandlers) handleCheckoutsStatus(_ context.Context, req jsonrpcRequest) {
	_ = h.r.TryResult(req.ID, mustMarshal(h.snapshot()))
}

// checkoutsStatusSpecs declares checkouts.status. It runs on the ordinary
// lane and takes no params: the answer is a mutex read of in-memory state,
// with no store, no vault and no socket behind it.
//
// Deliberately NOT gated with whenAvailable, for history.status's reason: a
// method that answers "method not found" while the capability it describes
// is down would make the one question the renderer needs to ask
// unanswerable exactly when the answer matters.
func (s *WSServer) checkoutsStatusSpecs(sub control.Submission) []methodSpec {
	return []methodSpec{
		regResponder(sub, "checkouts.status", noParams(), func(r Responder) handlerFunc {
			h := checkoutsStatusHandlers{snapshot: s.checkoutsStatusSnapshot, r: r}
			return h.handleCheckoutsStatus
		}),
	}
}
