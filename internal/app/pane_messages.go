package app

// session.message — a FIFO queue per descendant pane, delivered through the
// SAME PaneKeys/PaneReader step path session.keys already spends targets
// through, never a second write mechanism (design §8, Task 10).
//
// Mirrors Task 8/9's layering: assistant.PaneMessages is the interface the
// executor consumes (internal/assistant/execute_session_message.go);
// paneMessages here is its one production implementation, wrapping the SAME
// paneKeysReader (its Read for fresh targets, its Record for a "when=now"
// caller's own prior mint) and the SAME PaneKeys/authority hub Tasks 8/9
// already built — never a second, independent answer to "which helper holds
// this pane" or "does this chain still hold".

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// messagePollInterval is how often a queued ("free") message re-checks
// whether the pane is free enough to attempt delivery, and how often
// deliverLoop re-polls the echo step. It is a scheduling yield the loop's
// exit condition never depends on for correctness (the exit is always "the
// box shows the echo", "the message was cancelled", or "authority no longer
// holds") — the same role optionPollInterval already plays for the option
// loop (session_keys.go).
const messagePollInterval = 20 * time.Millisecond

// defaultEchoWait bounds how long a delivery waits for the pasted text to
// echo into the input box before giving up and reporting partial (design
// §8.2 step 2, "wait (bounded)"), and how long confirmSubmission waits for
// step 4's own close. It is a paneMessages FIELD (echoWait below), not a
// bare constant, so a test asserting the "never appears" path is not made
// to sleep out a production-sized bound for a fact it can settle in
// milliseconds — a watchdog on progress, not a duration, applied to this
// package's own hard-coded timeout the same way AGENTS.md already asks of
// a test's own waits.
const defaultEchoWait = 3 * time.Second

// authorityInterval is MessageKey's own comparable projection of
// pane_access.go's Authority (design §8.4: "endpoint -> the admission epoch
// ...; kernel -> the run id"). It is a distinct, small, comparable struct
// rather than reusing Authority directly because MessageKey must be usable
// as a map key across BOTH concrete Authority variants without either
// caller importing the other's type, and because EnqueueTask's internal
// namespace ("nocx", Task 11) has no caller-bound Authority at all — a third
// kind, "internal", that a caller can never produce (Send/Cancel always
// derive kind from access.Authority(), never from a parameter).
type authorityInterval struct {
	kind  string // "endpoint" | "kernel" | "internal"
	value string // AdmissionEpoch (decimal) | RunID | ""
}

func authorityIntervalOf(a Authority) authorityInterval {
	if ep, ok := a.(EndpointAuthority); ok {
		return authorityInterval{kind: "endpoint", value: strconv.FormatUint(uint64(ep.AdmissionEpoch), 10)}
	}
	if k, ok := a.(KernelAuthority); ok {
		return authorityInterval{kind: "kernel", value: k.RunID}
	}
	return authorityInterval{kind: "internal"}
}

// MessageKey is one message's idempotency key (design §8.4): every part
// server-derived except ID, which the caller chooses. It is comparable
// (every field is), so it is used directly as a map key rather than hashed
// into a string — the same reasoning session.Identity and workers.Liveness
// already rely on for their own SameIncarnation comparisons.
type MessageKey struct {
	Caller      string // "endpoint" | "kernel" | "internal"
	Controller  string
	Identity    session.Identity
	Authority   authorityInterval
	Participant workers.ParticipantID
	Liveness    workers.Liveness
	Namespace   string // "caller" | "nocx"
	ID          string
}

// payloadHashDoc is PayloadHashV1's canonical JSON v1 document, built as a
// map so encoding/json's own guarantee — map[string]T marshals with its keys
// sorted lexicographically — gives "keys sorted" for free rather than a
// hand-rolled canonicaliser.
func PayloadHashV1(text, when string, targetKind sessionruntime.TargetKind) [32]byte {
	doc := map[string]any{"v": 1, "text": text, "when": when, "targetKind": string(targetKind)}
	// A map[string]any of these value kinds (string, int) never fails to
	// marshal; the error is checked anyway because ignoring it silently
	// would hash an empty document on a future field that does.
	b, err := json.Marshal(doc)
	if err != nil {
		b = []byte(fmt.Sprintf(`{"v":1,"text":%q,"when":%q,"targetKind":%q,"marshalError":%q}`, text, when, targetKind, err))
	}
	return sha256.Sum256(b)
}

// pendingMessage is the coordinator's own record of one queued or delivered
// message (design §8.3, §8.4).
type pendingMessage struct {
	key         MessageKey
	payloadHash [32]byte
	text        string
	when        string

	// access/chain are the authority this message was accepted under.
	// Re-checked (StillHolds) before every delivery step, never assumed
	// still valid at enqueue time alone (design §8.6).
	access *DescendantPaneAccess
	chain  workers.Chain

	mu           sync.Mutex
	phase        assistant.MessagePhase
	bytesWritten int
	boxContents  string
	// generation is bumped by claim() and by cancel(); a step's own commit
	// (§8.5) only applies if the generation it captured at claim time still
	// matches, which is what makes a claim cancel's own linearisation point.
	generation uint64
	cancelled  bool
}

func (pm *pendingMessage) view() assistant.MessageView {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return assistant.MessageView{
		ID: pm.key.ID, Namespace: pm.key.Namespace, Phase: pm.phase,
		BytesWritten: pm.bytesWritten, BoxContents: pm.boxContents,
	}
}

// claim is design §8.5's queue-mutex step: "refuse if the record is
// cancelled, else claim the step (record phase + generation) — the claim IS
// cancel's linearisation point — and release." ok is false when the record
// is already cancelled or already terminal (nothing left to claim).
func (pm *pendingMessage) claim(next assistant.MessagePhase) (generation uint64, ok bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.cancelled || isTerminalPhase(pm.phase) {
		return 0, false
	}
	pm.phase = next
	pm.generation++
	return pm.generation, true
}

// commit applies a step's outcome only if generation still matches the one
// claim() handed out — design §8.5's "commit the phase only if the record's
// generation still matches." A stale commit (generation moved under it,
// because a concurrent cancel or a later step's claim already advanced the
// record) is silently dropped: the record already reflects something newer.
func (pm *pendingMessage) commit(generation uint64, phase assistant.MessagePhase, bytesWritten int, boxContents string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.generation != generation {
		return
	}
	pm.phase = phase
	if bytesWritten > 0 {
		pm.bytesWritten = bytesWritten
	}
	if boxContents != "" {
		pm.boxContents = boxContents
	}
}

// cancel is design §8.6's cancel form, linearised on the SAME claim: it
// bumps the generation itself (so any step already in flight, holding an
// older generation, can no longer commit) and reports the exact response
// shape. It is idempotent: a message already cancelled answers the same way
// again rather than double-bumping.
func (pm *pendingMessage) cancel() (result string, phase assistant.MessagePhase) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.cancelled {
		return "cancelled", assistant.PhaseCancelled
	}
	if pm.phase == assistant.PhaseQueued {
		pm.cancelled = true
		pm.phase = assistant.PhaseCancelled
		pm.generation++
		return "cancelled", assistant.PhaseCancelled
	}
	// Any other phase: the paste step has already been claimed (pasting) or
	// gone further. "A cancel never reports cancelled for a message whose
	// paste can still be written" — too_late, and the paste proceeds; it is
	// NOT marked cancelled, because the delivery may still legitimately
	// reach submitted.
	return "too_late", pm.phase
}

func (pm *pendingMessage) currentPhase() assistant.MessagePhase {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.phase
}

func (pm *pendingMessage) isCancelled() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.cancelled
}

func isTerminalPhase(p assistant.MessagePhase) bool {
	switch p {
	case assistant.PhaseRefused, assistant.PhaseFailedPartial, assistant.PhaseDeliveryUnknown,
		assistant.PhasePartial, assistant.PhaseWritten, assistant.PhaseSubmitted,
		assistant.PhaseCancelled, assistant.PhaseIndeterminate:
		return true
	default:
		return false
	}
}

// paneQueue is one descendant pane's FIFO queue: one delivery at a time,
// arrival order, in coordinator memory only (design §8.1, §8.6 "the queue is
// in memory").
type paneQueue struct {
	mu         sync.Mutex
	order      []*pendingMessage
	byKey      map[MessageKey]*pendingMessage
	delivering bool
}

// paneMessages is assistant.PaneMessages' one production implementation.
type paneMessages struct {
	keys      assistant.PaneKeys
	reader    paneKeysReader
	hub       *paneAccessHub
	rules     *agentdriver.Registry
	startedAt time.Time
	// echoWait is defaultEchoWait, overridable by a test in this same
	// package (pane_messages_test.go sets it directly) — see
	// defaultEchoWait's own doc for why this is a field rather than a bare
	// constant.
	echoWait time.Duration

	mu     sync.Mutex
	queues map[string]*paneQueue
}

// newPaneMessages builds a PaneMessages over keys and reader (the SAME
// PaneKeys/PaneReader session.keys already spends targets through — never a
// second write mechanism), hub (the authority chain's StillHolds, exactly
// as PaneKeys.commitStep uses it), rules (unused directly today — kept for
// parity with the plan's own signature and because a future agent's own
// echo form belongs here, beside MenuDisplacesInputBox, rather than as a
// second per-agent lookup) and startedAt (this instance's own construction
// time, design §8.6's restart signal: DeliveryLost answers non-nil for any
// session this instance holds no queue for, which is every session right
// after a restart, since the queue is in-memory only).
func newPaneMessages(keys assistant.PaneKeys, reader paneKeysReader, hub *paneAccessHub, rules *agentdriver.Registry, startedAt time.Time) *paneMessages {
	return &paneMessages{
		keys: keys, reader: reader, hub: hub, rules: rules, startedAt: startedAt,
		echoWait: defaultEchoWait,
		queues:   make(map[string]*paneQueue),
	}
}

var _ assistant.PaneMessages = (*paneMessages)(nil)

func (m *paneMessages) queueFor(sessionID string) *paneQueue {
	m.mu.Lock()
	defer m.mu.Unlock()
	q, ok := m.queues[sessionID]
	if !ok {
		q = &paneQueue{byKey: make(map[MessageKey]*pendingMessage)}
		m.queues[sessionID] = q
	}
	return q
}

// Pending implements the paneMessagesSource seam session_targets.go's
// paneReader consumes.
func (m *paneMessages) Pending(sessionID string) []assistant.MessageView {
	m.mu.Lock()
	q, ok := m.queues[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]assistant.MessageView, 0, len(q.order))
	for _, pm := range q.order {
		out = append(out, pm.view())
	}
	return out
}

// DeliveryLost answers design §8.6's restart signal: non-nil, naming this
// instance's own startedAt, for any session it holds no queue record for —
// which is every session before its first Send in this incarnation, since
// the queue does not survive a restart and this instance has no other way
// to tell "never used" from "used and lost" apart. See newPaneMessages' own
// doc.
func (m *paneMessages) DeliveryLost(sessionID string) *time.Time {
	m.mu.Lock()
	_, ok := m.queues[sessionID]
	m.mu.Unlock()
	if ok {
		return nil
	}
	since := m.startedAt
	return &since
}

// buildKey derives a message's server-side MessageKey from da's own bound
// authority and the enrolment/chain Resolve just proved (design §8.4):
// caller/controller/identity/authority from da, participant/liveness from
// reach, namespace and id from the call.
func buildMessageKey(da *DescendantPaneAccess, reach workers.Reach, namespace, id string) MessageKey {
	caller := "kernel"
	if _, ok := da.AsEndpoint(); ok {
		caller = "endpoint"
	}
	return MessageKey{
		Caller: caller, Controller: da.Controller(), Identity: da.Identity(),
		Authority: authorityIntervalOf(da.Authority()), Participant: reach.Participant.ID,
		Liveness: reach.Participant.Liveness, Namespace: namespace, ID: id,
	}
}

// Send implements assistant.PaneMessages.
func (m *paneMessages) Send(ctx context.Context, access any, sessionID, text, when, id, tokenID string) (assistant.MessageView, error) {
	da, ok := access.(*DescendantPaneAccess)
	if !ok || da == nil {
		return assistant.MessageView{}, workers.ErrNotReachable
	}
	if when != "free" && when != "now" {
		return assistant.MessageView{}, fmt.Errorf("session.message: when must be %q or %q, got %q", "free", "now", when)
	}
	reach, err := da.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		return assistant.MessageView{}, err
	}
	// EnqueueTask's own doc (below) already found this once for namespace
	// "nocx": sessionID here is whatever the caller named the pane by
	// (workers.ParticipantID for an ordinary session.message call), and
	// every step below — the queue, the delivery, the mint the pane reader
	// spends — is keyed by the descendant's REAL backend session instead.
	// reach.SessionID is that real id, Resolve's own translation already
	// applied.
	sessionID = reach.SessionID

	targetKind := sessionruntime.TargetKind("")
	if when == "now" {
		if tokenID == "" {
			return assistant.MessageView{}, errors.New(`session.message: when "now" requires tokenId`)
		}
		rec, ok := m.reader.Record(tokenID)
		if !ok || rec.Access != da || (rec.View.Kind != sessionruntime.TargetInput && rec.View.Kind != sessionruntime.TargetWorking) {
			return assistant.MessageView{}, errors.New(`session.message: tokenId does not name a live target of kind input or working under this call's own authority`)
		}
		targetKind = rec.View.Kind
	}

	key := buildMessageKey(da, reach, "caller", id)
	hash := PayloadHashV1(text, when, targetKind)

	q := m.queueFor(sessionID)
	q.mu.Lock()
	if existing, dup := q.byKey[key]; dup {
		q.mu.Unlock()
		if existing.payloadHash != hash {
			return assistant.MessageView{}, fmt.Errorf("session.message: id %q was already used with a different payload", id)
		}
		// "Same key, same hash -> the recorded state" (design §8.4): a
		// repeated call with the identical payload is answered from the
		// record, never re-delivered.
		return existing.view(), nil
	}
	pm := &pendingMessage{
		key: key, payloadHash: hash, text: text, when: when,
		access: da, chain: reach.Chain, phase: assistant.PhaseQueued,
	}
	q.order = append(q.order, pm)
	q.byKey[key] = pm
	q.mu.Unlock()

	if when == "now" {
		// The delivery runs on a DETACHED background context, never ctx: a
		// delivery belongs to the pane's queue, not to this call, and a
		// caller disconnect (ctx cancelled) must not cancel a paste or
		// Enter already under way (design §8.6, "a caller disconnect does
		// not cancel"). This call still normally waits for it — "when=now
		// ... runs the delivery within the call" (§8.1) — but returns early
		// on ctx.Done() with whatever phase the record holds at that
		// moment, leaving the background delivery to finish on its own.
		done := make(chan struct{})
		go func() {
			defer close(done)
			m.deliverOne(context.Background(), sessionID, pm)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
		return pm.view(), nil
	}
	// "free": returns queued immediately; delivery happens in the
	// background, one at a time, whenever this pane's queue is not already
	// running a delivery.
	go m.runQueue(sessionID, q)
	return pm.view(), nil
}

// EnqueueTask is namespace "nocx"'s one message: the owed task a spawn
// leaves for its worker once it meets a question (design §9, Task 11).
// coordinatorSession is the delegation chain's own root above participant —
// the plan's own newPaneMessages/EnqueueTask sketch omitted it, but
// Resolve's own contract refuses controller==sessionID (they can never be
// the same session), so a real DescendantPaneAccess for delivering to
// participant's pane needs the ACTUAL controller above it, exactly as the
// registrar methods this replaced (Screen, Answer) already required it as a
// parameter.
//
// sessionID is participant.Liveness.SessionID, never string(participant.ID):
// ParticipantID is a backend-minted opaque name (internal/workers/ids.go's
// newParticipantID, random hex) and Resolve's own ParticipantBySession
// lookup keys on the pane's SESSION id (a session.ID, e.g. a UUID) — the two
// only coincide in a test double whose fake Spawner happens to default one
// from the other (worker_two_callers_test.go's workerTwoCallersSpawner).
// Task 11's own caller (workers.go's Registrar.Register, via TaskQueue) is
// what surfaced this: every real EnqueueTask call was resolving a session id
// that names no participant at all.
func (m *paneMessages) EnqueueTask(ctx context.Context, coordinatorSession string, participant workers.Participant, task string) error {
	if m.hub == nil || m.hub.registrar == nil {
		return errNoPaneRuntime
	}
	sessionID := participant.Liveness.SessionID
	if sessionID == "" {
		return workers.ErrNotReachable
	}
	da := m.hub.Bind(coordinatorSession, session.Identity{}, KernelAuthority{RunID: "internal:owed-task"})
	reach, err := da.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		return err
	}
	key := MessageKey{
		Caller: "internal", Controller: coordinatorSession,
		Participant: reach.Participant.ID, Liveness: reach.Participant.Liveness,
		Namespace: "nocx", ID: "task",
	}
	hash := PayloadHashV1(task, "free", "")

	q := m.queueFor(sessionID)
	q.mu.Lock()
	if existing, dup := q.byKey[key]; dup {
		q.mu.Unlock()
		if existing.payloadHash != hash {
			return errors.New(`session.message: namespace "nocx" id "task" was already used with a different payload for this incarnation`)
		}
		return nil
	}
	pm := &pendingMessage{
		key: key, payloadHash: hash, text: task, when: "free",
		access: da, chain: reach.Chain, phase: assistant.PhaseQueued,
	}
	q.order = append(q.order, pm)
	q.byKey[key] = pm
	q.mu.Unlock()

	go m.runQueue(sessionID, q)
	return nil
}

// Cancel implements assistant.PaneMessages (design §8.6). It resolves id to
// the caller's own MessageKey — the SAME derivation Send uses, so a cancel
// can only ever name a message that call's own caller/controller/identity/
// authority/participant produced, never another caller's or namespace
// "nocx"'s (Send/Cancel both hard-code namespace "caller"; "nocx" is never
// reachable from either parameter list).
func (m *paneMessages) Cancel(ctx context.Context, access any, sessionID, id string) (assistant.CancelResult, error) {
	da, ok := access.(*DescendantPaneAccess)
	if !ok || da == nil {
		return assistant.CancelResult{}, workers.ErrNotReachable
	}
	reach, err := da.Resolve(ctx, sessionID, workers.EffectSendInput)
	if err != nil {
		return assistant.CancelResult{}, err
	}
	// Send's own note applies here identically: the queue Cancel looks up
	// is keyed by the descendant's real session (reach.SessionID), not by
	// whatever id the caller named it with.
	sessionID = reach.SessionID
	key := buildMessageKey(da, reach, "caller", id)

	q := m.queueFor(sessionID)
	q.mu.Lock()
	pm, ok := q.byKey[key]
	q.mu.Unlock()
	if !ok {
		return assistant.CancelResult{Result: "no_such_message"}, nil
	}
	result, phase := pm.cancel()
	return assistant.CancelResult{Result: result, Phase: phase}, nil
}

// runQueue is one pane's delivery loop for "free" messages: while the queue
// is not already delivering and its head is still queued and not cancelled,
// deliver it, one at a time, in arrival order (design §8.1). Started as a
// goroutine from Send/EnqueueTask; a second start while one is already
// running for this pane is a no-op (the delivering flag), so a burst of
// enqueues never runs two deliveries on one pane concurrently.
func (m *paneMessages) runQueue(sessionID string, q *paneQueue) {
	for {
		q.mu.Lock()
		if q.delivering {
			q.mu.Unlock()
			return
		}
		var head *pendingMessage
		for _, pm := range q.order {
			if pm.currentPhase() == assistant.PhaseQueued && !pm.isCancelled() {
				head = pm
				break
			}
		}
		if head == nil {
			q.mu.Unlock()
			return
		}
		q.delivering = true
		q.mu.Unlock()

		m.deliverOne(context.Background(), sessionID, head)

		q.mu.Lock()
		q.delivering = false
		q.mu.Unlock()
	}
}

// deliverOne runs design §8.2's whole sequence for one message: paste, wait
// for the echo, Enter, confirm submission — each step its own freshly
// minted target (§8.2), claimed and committed under §8.5's lock order.
//
// For when=="now" a precondition failure (box not empty, or the input box
// is not currently identifiable at all — Task 10's own measurement,
// nocx-6q1uh.10: a Claude menu always displaces the input box, so "not
// identifiable" IS "a menu is up") is terminal: refused. For when=="free" it
// is not terminal — the message stays queued and runQueue tries again after
// messagePollInterval, up to the message being cancelled or its authority
// no longer holding.
func (m *paneMessages) deliverOne(ctx context.Context, sessionID string, pm *pendingMessage) {
	for {
		if pm.isCancelled() {
			return
		}
		if !m.hub.registrar.StillHolds(ctx, pm.chain) {
			m.terminate(pm, assistant.PhaseRefused, 0, "")
			return
		}
		if !m.pasteReady(ctx, pm.access, sessionID) {
			if pm.when == "now" {
				m.terminate(pm, assistant.PhaseRefused, 0, "")
				return
			}
			// when=="free": whether the box holds someone else's typing or
			// the box could not be identified at all (a menu up), the
			// answer is the same — try again shortly rather than fighting
			// for it or giving up.
			select {
			case <-ctx.Done():
				return
			case <-time.After(messagePollInterval):
			}
			continue
		}
		break
	}

	gen, ok := pm.claim(assistant.PhasePasting)
	if !ok {
		return // already cancelled or terminal — nothing left to deliver
	}
	pasteResult, pasteErr := m.pasteStep(ctx, pm.access, sessionID, pm.text)
	if pasteErr != nil {
		pm.commit(gen, assistant.PhaseIndeterminate, 0, "")
		return
	}
	switch pasteResult.State {
	case "executed":
		pm.commit(gen, assistant.PhaseAwaitingEcho, pasteResult.BytesWritten, "")
	case "failed_partial":
		pm.commit(gen, assistant.PhaseFailedPartial, pasteResult.BytesWritten, "")
		return
	case "delivery_unknown":
		pm.commit(gen, assistant.PhaseDeliveryUnknown, pasteResult.BytesWritten, "")
		return
	default:
		// refused, cancelled, indeterminate, in_progress: nothing written,
		// and there is no further step to take on this attempt.
		pm.commit(gen, assistant.PhaseRefused, 0, "")
		return
	}

	echoed, boxNow := m.waitForEcho(ctx, pm.access, sessionID, pm.text)
	if pm.isCancelled() {
		return
	}
	if !echoed {
		pm.commit(gen, assistant.PhasePartial, pasteResult.BytesWritten, boxNow)
		return
	}

	if !m.hub.registrar.StillHolds(ctx, pm.chain) {
		pm.commit(gen, assistant.PhasePartial, pasteResult.BytesWritten, boxNow)
		return
	}
	entering, ok := pm.claim(assistant.PhaseEntering)
	if !ok {
		return
	}
	enterResult, enterErr := m.enterStep(ctx, pm.access, sessionID)
	if enterErr != nil {
		pm.commit(entering, assistant.PhaseIndeterminate, 0, boxNow)
		return
	}
	switch enterResult.State {
	case "executed":
		pm.commit(entering, assistant.PhaseWritten, pasteResult.BytesWritten, "")
	default:
		pm.commit(entering, assistant.PhasePartial, pasteResult.BytesWritten, boxNow)
		return
	}

	if m.confirmSubmission(ctx, pm.access, sessionID) {
		pm.commit(entering, assistant.PhaseSubmitted, pasteResult.BytesWritten, "")
	}
	// A submission this check could not confirm within its own bounded wait
	// stays "written" rather than being escalated to indeterminate: the
	// Enter step's own outcome IS known (executed), only the agent's own
	// reaction to it was not observed in time.
}

// terminate is deliverOne's shared "claim straight to a terminal phase"
// path, for the two preconditions that end a delivery before any bytes are
// written (revoked authority, or — when=="now" — the box not being ready).
func (m *paneMessages) terminate(pm *pendingMessage, phase assistant.MessagePhase, bytesWritten int, box string) {
	if gen, ok := pm.claim(phase); ok {
		pm.commit(gen, phase, bytesWritten, box)
	}
}

// agentAwareReader is the optional half a paneKeysReader may implement —
// *paneReader does (session_targets.go's AgentFor) — to name which agent a
// session runs, so deliveryTargetKind can consult
// agentdriver.Registry.MenuDisplacesInputBox per agent rather than
// hardcoding one answer for every agent this build ever ships a driver for.
type agentAwareReader interface {
	AgentFor(sessionID string) string
}

// deliveryTargetKind answers design §8.2's own open question — bind the
// paste and Enter steps (and the step-4 "or working" submission check) to
// the input box + cursor, or fall back to the wider menu zone — per agent,
// from the declared, measured fact claude.rule.json now carries
// (nocx-6q1uh.10, agentdriver.Document.MenuDisplacesInputBox): TargetInput
// when the agent declares it (a menu always displaces its input box, so a
// target minted from the box alone already catches a menu appearing), the
// wider TargetWorking otherwise — the safe default for an agent nobody has
// measured this property for yet.
func (m *paneMessages) deliveryTargetKind(sessionID string) sessionruntime.TargetKind {
	aware, ok := m.reader.(agentAwareReader)
	if !ok || m.rules == nil {
		return sessionruntime.TargetWorking
	}
	agent := aware.AgentFor(sessionID)
	if agent == "" || !m.rules.MenuDisplacesInputBox(agent) {
		return sessionruntime.TargetWorking
	}
	return sessionruntime.TargetInput
}

// agentFor answers which agent sessionID runs, through the SAME enrolment-
// cache lookup deliveryTargetKind already keys MenuDisplacesInputBox on
// (agentAwareReader.AgentFor) — never a second derivation of "what agent runs
// here" (AGENTS.md, "look for the existing answer before you write a second
// one"). Empty when this reader carries no such lookup (a test double, most
// tests in this package) or the session names no enrolled agent.
func (m *paneMessages) agentFor(sessionID string) string {
	aware, ok := m.reader.(agentAwareReader)
	if !ok {
		return ""
	}
	return aware.AgentFor(sessionID)
}

// inputText re-evaluates f — the SAME frame a Read this call already made
// just handed back — under sessionID's own agent rule, and answers what the
// RULE says is actually typed in the input box (agentdriver.Observation.
// InputText, nocx-6q1uh.18). This is a second, cheap, pure evaluation of a
// frame this package already has in hand, never a second read of the pane:
// Observe's own contract is a function of the frame alone, so calling it
// again here is exactly as safe as the paneReader's own first call was.
//
// Before this, every caller below answered "is the box empty" and "did it
// echo" by trimming strings.TrimSpace over regionText's raw cell-join of the
// whole minted target span — which for an agent like claude is
// Document.InputBox, rule rows included (deliberately: the target must stay
// that wide so a menu displacing the box still makes it refuse). A row of
// nothing but the rule's own full-width glyph is never whitespace, so that
// trim was never empty and a queued message could neither be pasted nor have
// its own submission confirmed (claude.go's own note on this has the full
// account, and the corpus evidence). ok is false when this build has no rule
// for the agent, or the rule's own extractor could not read the box on this
// exact frame (a menu has displaced it, or nothing is enrolled here at all)
// — the same fail-closed direction pasteReady's caller already takes for
// "the box is not currently identifiable".
func (m *paneMessages) inputText(sessionID string, f paneview.Frame) (string, bool) {
	if m.rules == nil {
		return "", false
	}
	agent := m.agentFor(sessionID)
	if agent == "" {
		return "", false
	}
	return m.rules.Observe(agent, f).InputText()
}

// pasteReady mints a fresh target of deliveryTargetKind's own answer and
// reports whether the paste precondition holds (design §8.2 step 1: "the
// input box empty"). False either because the box currently holds someone
// else's text, or because the box could not be identified at all right now
// — for an agent whose menu displaces its input box (nocx-6q1uh.10), that
// IS "a menu is up". Both reasons are treated identically by every caller
// (when=="now" refuses either way; when=="free" retries either way), so
// this reports only the one bool a caller acts on.
//
// The target mint is still spent for its own sake — it is what makes "a
// menu is up" refuse (a target of a kind other than want, or none at all) —
// and "empty" is answered by the RULE's own inputText reading of the same
// frame (nocx-6q1uh.18), never by trimming the wider span the target itself
// covers.
func (m *paneMessages) pasteReady(ctx context.Context, da *DescendantPaneAccess, sessionID string) bool {
	want := m.deliveryTargetKind(sessionID)
	read, err := m.reader.Read(ctx, da, sessionID, &want, nil)
	if err != nil || read.Target == nil || read.Target.Kind != want {
		return false
	}
	text, ok := m.inputText(sessionID, read.Frame)
	return ok && text == ""
}

// pasteResult carries what commitPasteOrEnter needs without exposing
// session_keys.go's own KeysResult naming to this file's callers directly —
// it IS assistant.KeysResult; the alias exists only to keep this file's own
// signatures readable.
type pasteResultT = assistant.KeysResult

// pasteStep mints a fresh target of deliveryTargetKind's own answer and
// spends it as a text atom, through PaneKeys' own step path (Task 9) —
// never a second write mechanism.
func (m *paneMessages) pasteStep(ctx context.Context, da *DescendantPaneAccess, sessionID, text string) (pasteResultT, error) {
	want := m.deliveryTargetKind(sessionID)
	read, err := m.reader.Read(ctx, da, sessionID, &want, nil)
	if err != nil {
		return assistant.KeysResult{}, err
	}
	if read.Target == nil || read.Target.Kind != want {
		return assistant.KeysResult{State: "refused"}, nil
	}
	return m.keys.Send(ctx, da, assistant.KeysRequest{SessionID: sessionID, TokenID: read.Target.TokenID, Text: &text})
}

// enterStep mints a fresh target covering the box the echo was just
// confirmed on and spends it as the Enter key (design §8.2 step 3).
func (m *paneMessages) enterStep(ctx context.Context, da *DescendantPaneAccess, sessionID string) (pasteResultT, error) {
	want := m.deliveryTargetKind(sessionID)
	read, err := m.reader.Read(ctx, da, sessionID, &want, nil)
	if err != nil {
		return assistant.KeysResult{}, err
	}
	if read.Target == nil || read.Target.Kind != want {
		// The box moved or a menu appeared between echo and Enter (design
		// §8.2's own named race, TestAMenuBetweenPasteAndEnterRefusesTheEnter):
		// refused, no Enter sent.
		return assistant.KeysResult{State: "refused"}, nil
	}
	enter := assistant.KeyName("Enter")
	return m.keys.Send(ctx, da, assistant.KeysRequest{SessionID: sessionID, TokenID: read.Target.TokenID, Key: &enter})
}

// waitForEcho polls (bounded by echoWait) for the pasted text to appear in
// the input box (design §8.2 step 2). It accepts either the text verbatim
// (a single-line paste) or Claude's own bracket echo form for a multi-line
// paste ("[Pasted text #N +M lines]") — matched loosely, on the "+M lines]"
// suffix alone, since #N is a counter this side of the call cannot predict.
func (m *paneMessages) waitForEcho(ctx context.Context, da *DescendantPaneAccess, sessionID, text string) (echoed bool, boxNow string) {
	deadline := time.Now().Add(m.echoWait)
	want := m.deliveryTargetKind(sessionID)
	for {
		read, err := m.reader.Read(ctx, da, sessionID, &want, nil)
		if err == nil && read.Target != nil && read.Target.Kind == want {
			if box, ok := m.inputText(sessionID, read.Frame); ok {
				boxNow = box
				if boxContainsEcho(box, text) {
					return true, box
				}
			}
		}
		if time.Now().After(deadline) {
			return false, boxNow
		}
		select {
		case <-ctx.Done():
			return false, boxNow
		case <-time.After(messagePollInterval):
		}
	}
}

// boxContainsEcho is waitForEcho's own predicate: the pasted text verbatim,
// or (for a multi-line paste) Claude's bracket placeholder naming the same
// number of extra lines.
func boxContainsEcho(box, text string) bool {
	if strings.Contains(box, text) {
		return true
	}
	lines := strings.Count(text, "\n") + 1
	if lines <= 1 {
		return false
	}
	return strings.Contains(box, fmt.Sprintf("+%d lines]", lines-1))
}

// confirmSubmission waits (bounded) for design §8.2 step 4's own close: the
// box cleared, or the pane now classified working. It does not itself
// distinguish "the agent is thinking" from "the box happens to be empty for
// another reason" — that ambiguity is why a submission it cannot confirm
// stays "written" rather than becoming a stronger claim.
func (m *paneMessages) confirmSubmission(ctx context.Context, da *DescendantPaneAccess, sessionID string) bool {
	deadline := time.Now().Add(m.echoWait)
	for {
		read, err := m.reader.Read(ctx, da, sessionID, nil, nil)
		if err == nil {
			if read.Classification == agentdriver.StateWorking {
				return true
			}
			// read.Frame is already this exact snapshot's own frame (Read
			// fills it whether or not a target was asked for), so the box's
			// own text is read straight off it — no second mint needed now
			// that the check is the RULE's inputText rather than a trim over
			// a minted target's span (nocx-6q1uh.18).
			if text, ok := m.inputText(sessionID, read.Frame); ok && text == "" {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(messagePollInterval):
		}
	}
}
