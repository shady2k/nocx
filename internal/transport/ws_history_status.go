package transport

// history.status / history.statusChanged — the ONE way the product says that
// durable command history is not running, and why (nocx-rtg0.15).
//
// The bug this exists for: failing soft was right — a terminal that refuses
// to start because its history key would not open is worse than one that
// starts without history — but the soft failure was a slog.Warn and nothing
// else, while the Settings screen went on offering a keep-history toggle, a
// retention age and a two-number budget that governed nothing. A silent
// degrade the UI contradicts is how a feature that does not exist survives a
// release (AGENTS.md).
//
// ── Why one mechanism, and why it is not named after startup ──────────────
//
// There are TWO unavailabilities and they must never become two
// vocabularies: the store never opened (this bead — no content key, an
// unusable budget, a failed Open), and the store is open but writes are
// failing or the outbox overflowed at runtime (nocx-rtg0.10, whose policy
// already says it raises its notice through THIS surface, once per degrade
// episode rather than once per lost command, with the interval closed when
// the queue drains). So the shape here is raise/clear, not one-shot, and the
// name says what is true rather than when it became true. A runtime failure
// adds a member to HistoryDegradeReason and calls Raise; it does not get a
// second status. If you are about to add one, this comment is the reason not
// to.
//
// ── Why a status and not an error code ────────────────────────────────────
//
// The transport already knows when the content store is absent: the ledger
// handlers answer -32601 "method not found: content store not wired"
// (ws_ledger.go). That is a fact a UI could read, and it is the wrong one to
// reason about. An error code answers "your call failed", which is a
// property of the call; a Settings screen is not making a call, it is
// describing a capability, and a screen that has to provoke a failure to
// learn whether a feature works can only ever report the failure it
// provoked. -32601 also carries no reason, and the reason is the half of the
// message that lets somebody act. So the degrade is an explicit status with
// a closed reason code, in the house style of git.status's envState/
// envReason and files.watch's degradedReason — and the error codes stay
// exactly what they were, for callers.

import (
	"context"
	"sync"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/transport/control"
)

// HistoryDegradeReason is the closed set of reasons durable history is not
// running. Closed on purpose: the renderer picks its own sentence from the
// code rather than parsing prose, so a backend rewording never changes what
// a user reads. contracts/history.status.schema.json holds the same enum.
type HistoryDegradeReason string

const (
	// HistoryDegradeNoKey — the content key could not be read, so there is
	// nothing to decrypt the store with.
	HistoryDegradeNoKey HistoryDegradeReason = "noKey"
	// HistoryDegradeInvalidBudget — the History size settings do not make a
	// usable budget, so the store would have no bound to evict against.
	HistoryDegradeInvalidBudget HistoryDegradeReason = "invalidBudget"
	// HistoryDegradeOpenFailed — the history database itself would not open.
	HistoryDegradeOpenFailed HistoryDegradeReason = "openFailed"
	// HistoryDegradeWriteFailed is the RUNTIME one: the store opened and is
	// refusing writes (nocx-rtg0.10). It is a degrade like the three above —
	// commands are not being kept — and it is the reason this type was built
	// with a raise/clear interval rather than a one-shot flag, because it is
	// the only one that can end without a restart.
	HistoryDegradeWriteFailed HistoryDegradeReason = "writeFailed"
)

// historyStatusResponse is the wire shape of history.status, and — byte for
// byte — the params of history.statusChanged. Both pointers are null exactly
// as the schema says: reason is null if and only if Available is true;
// detail may be null either way, because a reason without words for it is
// still a complete answer.
type historyStatusResponse struct {
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
	Detail    *string `json:"detail"`
	// Discarded is how many commands the store threw away when it opened,
	// because the file was written by a different schema — null when nothing
	// was, which is every ordinary start (nocx-rtg0.19).
	//
	// IT IS NOT A DEGRADE, and that is why it rides beside `available`
	// rather than as a reason for it: history IS running, and it is empty
	// because the format changed under it. Saying that with `available:
	// false` would claim the feature is off; saying it only in a log is the
	// silent degrade AGENTS.md forbids, and the one that hurts most, because
	// the honest symptom — an empty history — is indistinguishable from a
	// fresh install.
	Discarded *int `json:"discarded"`
	// DetachedOutput is what happens to a session's output while no client
	// is attached (nocx-22k1c.1). It rides here, beside `available`, for the
	// reason `discarded` does: it is not a degrade OF durable history, it is
	// a CONSEQUENCE of the History switches that the settings below do not
	// otherwise state.
	//
	// And it has to be stated. With no recorder there is no consumer for the
	// replay ring, so a session whose window is closed throttles once the
	// ring fills — its output stops until somebody attaches. That is an
	// accepted degrade and a surprising one, and a degrade that reaches only
	// a slog.Warn is how a feature that does not exist survives a release
	// (AGENTS.md). It is said where the switches that cause it live.
	DetachedOutput detachedOutputStatus `json:"detachedOutput"`
}

// detachedOutputStatus is the shape of that consequence: whether it is being
// kept, and which switch says otherwise.
type detachedOutputStatus struct {
	Recorded bool    `json:"recorded"`
	Reason   *string `json:"reason"`
}

// HistoryStatus is the raise/clear state of durable command history: the one
// place that knows whether it is running, shared between the composition
// root that opens the store and the transport that answers for it.
//
// The interval this type states has both ends. A degrade episode OPENS at
// the first Raise with a given reason and CLOSES at Clear (or at the Raise
// of a different reason, which is a different episode). Listeners fire on
// those two events and on nothing in between — so a thousand lost commands
// inside one episode are one notice, and the notice goes away because
// something named made it go away, not because it faded.
type HistoryStatus struct {
	mu        sync.RWMutex
	available bool
	reason    HistoryDegradeReason
	detail    string
	// discarded is a ONE-SHOT FACT, not an episode: the store rebuilt itself
	// at open and this many commands went with the old shape. It has no
	// closing event because there is nothing to close — it happened once, on
	// this start, and the next start either repeats it or does not.
	discarded *int
	listeners []func()
}

// NewHistoryStatus returns a status reporting durable history as running.
// Available is the default rather than "unknown" because the renderer has
// one status to read and no third state to draw: the composition root is
// what says otherwise, on each of its degrade paths, before the transport
// ever starts.
func NewHistoryStatus() *HistoryStatus {
	return &HistoryStatus{available: true}
}

// Discarded records that the store rebuilt itself at open and how many
// commands that cost. It was called once, from the composition root, before
// the transport starts — so no listener has to fire and no episode opens.
//
// A count of -1 means the file held nothing this build could count, which is
// still a discard worth stating: something was there and is not now.
//
// NOTHING CALLS IT ANY MORE, and the sentence it feeds can no longer appear
// (nocx-lmb6v.1). The store does not rebuild a file for a version difference
// at all: it migrates one it has a step for and refuses one it does not, and
// a refusal speaks through HistoryDegradeOpenFailed like every other reason
// Open can fail. This method, the `discarded` field on the wire, and the
// renderer's historyDiscardSentence are the whole of a surface with no
// producer left; removing them is a wire-contract change and is deliberately
// not folded into the migration bead. The deadcode ratchet cannot see it —
// RTA treats a method on a type a live interface can hold as reachable
// (AGENTS.md) — so it is written down here instead.
func (h *HistoryStatus) Discarded(rows int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.discarded = &rows
}

// ClearReason closes an episode ONLY if it is the one named, and returns
// whether it closed anything.
//
// It exists because a runtime success does not disprove a startup degrade.
// The write path calls this after a record lands, and clearing
// unconditionally would let one successful write erase "the content key could
// not be read" — a sentence that is still true and that nothing else would
// ever say again. An episode is closed by the event that ends IT, which is
// the interval rule stated as a method.
func (h *HistoryStatus) ClearReason(reason HistoryDegradeReason) bool {
	h.mu.Lock()
	if h.available || h.reason != reason {
		h.mu.Unlock()
		return false
	}
	h.available = true
	h.reason = ""
	h.detail = ""
	listeners := append([]func(){}, h.listeners...)
	h.mu.Unlock()
	notifyHistoryStatusListeners(listeners)
	return true
}

// Raise opens a degrade episode: durable history is not running, for this
// reason, with this detail (which may be empty when there is no underlying
// error to quote).
//
// Idempotent within an episode. Raising the same reason again — which is
// what a per-write failure path does — changes the detail but announces
// nothing, so the surface raises once per episode rather than once per loss.
// A DIFFERENT reason is a different degrade and does announce, because the
// sentence on the user's screen changes with it.
func (h *HistoryStatus) Raise(reason HistoryDegradeReason, detail string) {
	h.mu.Lock()
	announce := h.available || h.reason != reason
	h.available = false
	h.reason = reason
	h.detail = detail
	listeners := append([]func(){}, h.listeners...)
	h.mu.Unlock()
	if !announce {
		return
	}
	notifyHistoryStatusListeners(listeners)
}

// Restate announces the CURRENT status again, without changing it.
//
// It exists because history.status carries a fact this type does not own:
// whether a detached session's output is being recorded, which follows the
// History switches live. Those switches move without any episode opening or
// closing, so nothing here would fire — and the renderer would go on
// displaying a sentence that stopped being true when the person flipped the
// toggle in front of it.
//
// The caller is the composition root, which updates the policy and then says
// so, in that order. Deliberately not a second notifier registered against
// the settings registry: notifier order would then decide whether the
// announcement carried the new policy or the old one.
func (h *HistoryStatus) Restate() {
	h.mu.RLock()
	listeners := append([]func(){}, h.listeners...)
	h.mu.RUnlock()
	notifyHistoryStatusListeners(listeners)
}

// Clear closes the open degrade episode: durable history is running again.
// Idempotent — clearing an already-clear status announces nothing.
func (h *HistoryStatus) Clear() {
	h.mu.Lock()
	announce := !h.available
	h.available = true
	h.reason = ""
	h.detail = ""
	listeners := append([]func(){}, h.listeners...)
	h.mu.Unlock()
	if !announce {
		return
	}
	notifyHistoryStatusListeners(listeners)
}

// Available reports whether durable command history is running. The read
// path uses it to answer honestly rather than as though a store had looked.
func (h *HistoryStatus) Available() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.available
}

// AddListener registers a callback fired once per episode boundary. The
// callback runs on the goroutine that called Raise or Clear, outside this
// type's lock, so a listener may read the status without deadlocking.
func (h *HistoryStatus) AddListener(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners = append(h.listeners, fn)
}

// snapshot renders the current state as the wire shape.
func (h *HistoryStatus) snapshot() historyStatusResponse {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := historyStatusResponse{Available: h.available}
	if !h.available && h.reason != "" {
		reason := string(h.reason)
		out.Reason = &reason
	}
	if h.detail != "" {
		detail := h.detail
		out.Detail = &detail
	}
	if h.discarded != nil {
		n := *h.discarded
		out.Discarded = &n
	}
	return out
}

// notifyHistoryStatusListeners runs the callbacks in order. Separated from
// the two callers so the "outside the lock" rule is stated once.
func notifyHistoryStatusListeners(listeners []func()) {
	for _, fn := range listeners {
		fn()
	}
}

// WithHistoryStatus attaches the durable-history degrade status. When
// absent, history.status answers "running": a server nobody told otherwise
// has nothing to report, and the method must still answer, because a
// renderer reads it on every render of the History settings and a method
// that vanishes is a worse answer than a boolean.
func WithHistoryStatus(st *HistoryStatus) WSServerOption {
	return func(s *WSServer) {
		s.historyStatus = st
		st.AddListener(func() { s.broadcastHistoryStatusChanged() })
	}
}

// historyStatusSnapshot is the server's view of the status, defaulting to
// "running" when no status was wired. One reader for both the method and the
// broadcast, so the two can never disagree.
func (s *WSServer) historyStatusSnapshot() historyStatusResponse {
	out := historyStatusResponse{Available: true}
	if s.historyStatus != nil {
		out = s.historyStatus.snapshot()
	}
	// Merged HERE and not inside HistoryStatus, because the two facts have
	// different owners: the degrade episode is that type's, and whether a
	// detached session is being recorded is the recorder's, read live off
	// the History policy. One reader for the method and the broadcast, so
	// the two can never disagree.
	out.DetachedOutput = detachedOutputOf(s.sessionRecordingStance())
	return out
}

// detachedOutputOf turns the recorder's stance into the wire shape. The
// stance codes travel unchanged — the renderer picks its own sentence from a
// closed code, so a backend rewording never changes what a person reads.
func detachedOutputOf(stance content.SessionOutputStance) detachedOutputStatus {
	if stance == content.SessionOutputKept {
		return detachedOutputStatus{Recorded: true}
	}
	reason := string(stance)
	return detachedOutputStatus{Reason: &reason}
}

// historyDurableAvailable reports whether durable history is running, for
// the read path. A server with no status wired is not making a claim, so it
// answers as the store itself does.
func (s *WSServer) historyDurableAvailable() bool {
	if s.historyStatus == nil {
		return true
	}
	return s.historyStatus.Available()
}

// broadcastHistoryStatusChanged sends history.statusChanged to every
// connected client. Best-effort and non-blocking, exactly like
// broadcastSettingsChanged: each notification is one enqueue into the
// connection's outbound queue, so a slow renderer delays its own connection
// only and never the store's write path, which is where a runtime raise
// (nocx-rtg0.10) will come from.
func (s *WSServer) broadcastHistoryStatusChanged() {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	params := mustMarshal(s.historyStatusSnapshot())
	for _, wc := range conns {
		_ = wc.TryNotify("history.statusChanged", params)
	}
}

// historyStatusHandlers answers history.status.
type historyStatusHandlers struct {
	snapshot func() historyStatusResponse
	r        Responder
}

func (h historyStatusHandlers) handleHistoryStatus(_ context.Context, req jsonrpcRequest) {
	_ = h.r.TryResult(req.ID, mustMarshal(h.snapshot()))
}

// historyStatusSpecs declares history.status. It runs on the ordinary lane
// and takes no params: the answer is a mutex read of in-memory state, with
// no store, no vault and no socket behind it.
//
// Deliberately NOT gated with whenAvailable. A method that answers "method
// not found" while the capability it describes is down would make the one
// question the renderer needs to ask unanswerable exactly when the answer
// matters.
func (s *WSServer) historyStatusSpecs(sub control.Submission) []methodSpec {
	return []methodSpec{
		regResponder(sub, "history.status", noParams(), func(r Responder) handlerFunc {
			h := historyStatusHandlers{snapshot: s.historyStatusSnapshot, r: r}
			return h.handleHistoryStatus
		}),
	}
}
