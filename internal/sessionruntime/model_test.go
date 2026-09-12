package sessionruntime

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// The reference model: a Runtime with no PTY, no emulator and no clock.
//
// It exists so the state machine can be exercised against adversarial arrival
// orders before anything is built, and so nocx-ygxjv.2 cannot choose the model
// by accident. It is in a _test.go file deliberately — see the package doc.

// rule names one rule of the contract. Every rule can be switched OFF, and the
// schedules in contract_test.go are run twice: once with every rule on, where
// they must pass, and once with exactly one rule off, where a NAMED assertion
// must fail. A rule that cannot be removed to make a test fail is not being
// tested by it.
type rule int

const (
	// ruleCancelOnRevoke: revoking or superseding a control epoch cancels every
	// intent admitted under it that has not executed.
	ruleCancelOnRevoke rule = iota
	// ruleExecutedIsIrreversible: an executed intent is never re-reported as
	// cancelled. Bytes on a PTY cannot be recalled.
	ruleExecutedIsIrreversible
	// ruleRevalidateAtExecution: incarnation, control epoch and precondition
	// are checked again at Execute, not only at Admit.
	ruleRevalidateAtExecution
	// ruleSightingAuthorisesNothing: a sighted fence never completes a
	// rendezvous on its own, whatever nonce it carries.
	ruleSightingAuthorisesNothing
	// rulePinSource: the content a fence was sighted over is pinned until the
	// rendezvous leaves its pending states, so later output cannot destroy it.
	rulePinSource
	// ruleAtomicGeometry: the COMMIT opens on both sides or on neither, and no
	// side is left at a size nobody committed. A resize has an effect outside
	// the two calls — the program receives SIGWINCH — so the rule cannot be,
	// and is not, "neither side was touched"; see GeometryCommit in contract.go.
	ruleAtomicGeometry
	// ruleSnapshotIsPassive: taking a snapshot mutates nothing.
	ruleSnapshotIsPassive
	// ruleObserverLossIsNotControlLoss: an observer going away cancels no
	// admitted input.
	ruleObserverLossIsNotControlLoss
	// ruleFailRevokes: a failed runtime becomes unavailable, cancels every
	// admitted intent and revokes control.
	ruleFailRevokes
	// ruleUnknownCompletenessRefusesWrites: while the runtime cannot say
	// whether it holds the whole stream, it executes nothing.
	ruleUnknownCompletenessRefusesWrites
	// ruleEncodeAgainstModes: a key is encoded against the modes the PROGRAM
	// set, at execution, by the runtime.
	ruleEncodeAgainstModes
	// ruleConsumerQueueIsBounded: one subscriber's queue holds at most
	// MaxPendingFrames payloads, and the runtime coalesces rather than growing.
	ruleConsumerQueueIsBounded
	// ruleConsumerLossIsReported: a payload the runtime dropped for a consumer
	// is COUNTED for that consumer and leaves it stale, rather than being
	// discovered by the consumer as a hole in what it holds.
	ruleConsumerLossIsReported
	// ruleLosslessIngestSurvivesASlowConsumer: a consumer that cannot keep up
	// never costs the stream itself. The lossless class is the emulator's, and
	// the way to slow a source down is to throttle it (AD-10's credit, the
	// carrier's job), never to discard output.
	ruleLosslessIngestSurvivesASlowConsumer
	// rulePerSessionAllowance: the allowance a queue draws on belongs to its
	// SESSION. One session reading slowly may not spend another's, which is
	// what per-session fairness means over frames.
	rulePerSessionAllowance
	// ruleDuplicateEffectIsSuppressed: an effect a consumer has already been
	// given is not delivered to it again, whatever carried it the second time.
	ruleDuplicateEffectIsSuppressed
	// ruleResendCarriesNoEffects: a full frame — a snapshot, a resend, a
	// re-attachment — carries cells and never an effect. A resend of state must
	// not resend a clipboard write or a notification (design §6.2).
	ruleResendCarriesNoEffects
	// ruleIngestIsBounded: the runtime's own ingest path is bounded three ways
	// — one call carries at most MaxIngestBytes and a larger one is refused,
	// an unterminated sequence is held to at most MaxPendingSequence bytes with
	// the excess dropped and reported, and the work it spends is linear in the
	// bytes it examined rather than in any count inside them.
	ruleIngestIsBounded
)

var ruleNames = map[rule]string{
	ruleCancelOnRevoke:                      "cancel-on-revoke",
	ruleExecutedIsIrreversible:              "executed-is-irreversible",
	ruleRevalidateAtExecution:               "revalidate-at-execution",
	ruleSightingAuthorisesNothing:           "sighting-authorises-nothing",
	rulePinSource:                           "pin-source",
	ruleAtomicGeometry:                      "atomic-geometry",
	ruleSnapshotIsPassive:                   "snapshot-is-passive",
	ruleObserverLossIsNotControlLoss:        "observer-loss-is-not-control-loss",
	ruleFailRevokes:                         "fail-revokes",
	ruleUnknownCompletenessRefusesWrites:    "unknown-completeness-refuses-writes",
	ruleEncodeAgainstModes:                  "encode-against-modes",
	ruleConsumerQueueIsBounded:              "consumer-queue-is-bounded",
	ruleConsumerLossIsReported:              "consumer-loss-is-reported",
	ruleLosslessIngestSurvivesASlowConsumer: "lossless-ingest-survives-a-slow-consumer",
	rulePerSessionAllowance:                 "per-session-allowance",
	ruleDuplicateEffectIsSuppressed:         "duplicate-effect-is-suppressed",
	ruleResendCarriesNoEffects:              "resend-carries-no-effects",
	ruleIngestIsBounded:                     "ingest-is-bounded",
}

type ruleSet map[rule]bool

func allRules() ruleSet {
	s := ruleSet{}
	for r := range ruleNames {
		s[r] = true
	}
	return s
}

// without returns the full rule set with one rule removed.
func without(r rule) ruleSet {
	s := allRules()
	s[r] = false
	return s
}

func (s ruleSet) on(r rule) bool { return s[r] }

// ---------------------------------------------------------------------------
// The model
// ---------------------------------------------------------------------------

type modelIntent struct {
	intent Intent
	state  IntentState
}

type model struct {
	rules ruleSet

	inc   Incarnation
	avail Availability
	rev   Revision

	control Control

	queue   []IntentID
	intents map[IntentID]*modelIntent
	nextID  IntentID

	// pty and emulator are the INSTRUMENTS this model was constructed over.
	// They are held as the contract's ports and not as their concrete types,
	// because that is what a real runtime does with the real ones: input is
	// written to a terminal and a commit is asked of both sides, and the model
	// has no private copy of either. What reached the PTY is therefore the
	// terminal's record and not a field here (contract.go, Terminal).
	pty      Terminal
	emulator Emulator

	geom GeometryCommit
	// reportedGeom is the last thing a client said it could show. The runtime
	// decides; this is only evidence for that decision.
	reportedGeom Geometry

	screen []byte
	// applicationCursorKeys is the one mode the model carries. It is enough to
	// make "input is intent" concrete: the same IntentKindKey produces
	// different bytes depending on what the PROGRAM set, and a client could not
	// have known which.
	applicationCursorKeys bool

	rendezvous   Rendezvous
	completeness Completeness

	// --- delivery: the classes, and the bounds that keep them bounded ------

	// budget is the allowance every consumer queue draws on. It is per model
	// unless a schedule hands two models ONE budget, which is how "a session
	// cannot spend another session's" is a difference a schedule can see
	// rather than a sentence in a comment.
	budget *deliveryBudget
	// consumers are the subscribers attached to this session, in attach order.
	// The schedules that matter hold one WEDGED: attached, and never read.
	consumers []*consumer
	// nextEffect mints the identity an effect's duplicate policy is stated
	// over. The runtime mints it, never the consumer.
	nextEffect EffectID
	// lastEffect is what the stream produced last, which is what a resend must
	// NOT repeat.
	lastEffect *Effect

	// pending is the part of an escape sequence the runtime is still holding
	// because it has not terminated. It is the runtime's own memory for a
	// hostile capture, and MaxPendingSequence is its bound.
	pending []byte
	// ingestWork counts the work units the runtime's OWN ingest path spent:
	// one per byte examined. It exists so "a hostile capture costs bounded
	// time" is a number a schedule can read, and so that a runtime which did
	// the EMULATOR's expansion of a repeat count would be caught doing it.
	ingestWork uint64
	// ingestLost counts output the ingest path discarded: the oldest bytes of
	// an unterminated sequence past its bound. It belongs to the LOSSLESS class
	// and it is the number the schedule for a slow consumer asserts is zero.
	ingestLost uint64
}

var (
	_ Runtime             = (*model)(nil)
	_ Consumers           = (*model)(nil)
	_ AuthenticatedEvents = (*model)(nil)
	_ Consumer            = (*consumer)(nil)
	_ Terminal            = (*ptySink)(nil)
	_ TerminalInstrument  = (*ptySink)(nil)
	_ Emulator            = (*emulatorSink)(nil)
	_ EmulatorInstrument  = (*emulatorSink)(nil)
)

func newModel(rules ruleSet) *model {
	return newModelWithBudget(rules, newDeliveryBudget())
}

// newUnestablishedModel is the model as a real runtime BEGINS: completeness
// UNKNOWN, where the write gate refuses every write until something establishes
// that the runtime holds the whole stream. The ordinary constructor has no
// attach step to establish anything from, and the producer of that state is a
// real runtime's (nocx-ygxjv.2) — so the state a runtime STARTS in is built
// here rather than switched on by a schedule through the contract, which would
// have been an instrumentation of the model wearing the contract's name.
func newUnestablishedModel(rules ruleSet) *model {
	m := newModel(rules)
	m.completeness = CompletenessUnknown
	return m
}

// newModelWithBudget is newModel with the delivery allowance SUPPLIED, which is
// how a schedule attaches two sessions to one budget: whether the allowance is
// per session is otherwise a difference nothing can observe.
func newModelWithBudget(rules ruleSet, budget *deliveryBudget) *model {
	return &model{
		rules:        rules,
		inc:          Incarnation{Session: "S", Generation: 1},
		avail:        AvailabilityAvailable,
		rev:          1,
		intents:      map[IntentID]*modelIntent{},
		geom:         GeometryCommit{Geometry: Geometry{Cols: 80, Rows: 24}, Revision: 1},
		completeness: CompletenessComplete,
		budget:       budget,
		// The instruments the test owns. A real runtime is constructed over a
		// real terminal and a real emulator at the composition root; this is
		// the same arrangement with nothing behind either port, which is what
		// makes "no I/O" true of the model rather than merely claimed.
		pty:      newPtySink(),
		emulator: newEmulatorSink(),
	}
}

// newSessionModel is newModelWithBudget for a session other than the model's
// default one, which is how a schedule attaches two SESSIONS to a single
// budget: the allowance is per session, and two sessions is the least that can
// show it.
func newSessionModel(rules ruleSet, budget *deliveryBudget, id SessionID) *model {
	m := newModelWithBudget(rules, budget)
	m.inc = Incarnation{Session: id, Generation: 1}
	return m
}

func (m *model) tick() Revision { m.rev++; return m.rev }

func (m *model) Incarnation() Incarnation   { return m.inc }
func (m *model) Availability() Availability { return m.avail }
func (m *model) Revision() Revision         { return m.rev }
func (m *model) Control() Control           { return m.control }
func (m *model) Geometry() GeometryCommit   { return m.geom }
func (m *model) Rendezvous() Rendezvous     { return m.rendezvous }
func (m *model) Completeness() Completeness { return m.completeness }
func (m *model) Terminal() Terminal         { return m.pty }
func (m *model) Emulator() Emulator         { return m.emulator }

// AuthenticatedEvents is the model itself: it collects the completion and does
// nothing else with it, which is all the runtime is allowed to do (ADR-0024
// decision 1 — a sighted marker LOCATES an authenticated event and never
// authorises one).
func (m *model) AuthenticatedEvents() AuthenticatedEvents { return m }

// Consumers is the model ITSELF, which is the least that can be said: the
// delivery side has four methods and the model is already the thing that holds
// the queues, the budget and the last effect. The interface is what keeps a
// schedule from reaching any of that except through these four (contract.go).
func (m *model) Consumers() Consumers { return m }

// Intents is the admission record in the order ids were minted. Ids are minted
// sequentially, so this is deterministic without walking a map.
func (m *model) Intents() []IntentRecord {
	out := make([]IntentRecord, 0, len(m.intents))
	for id := IntentID(1); id <= m.nextID; id++ {
		if mi, ok := m.intents[id]; ok {
			out = append(out, IntentRecord{ID: id, State: mi.state})
		}
	}
	return out
}

func (m *model) ReportedGeometry() Geometry { return m.reportedGeom }

func (m *model) IngestState() IngestState {
	return IngestState{
		Pending: append([]byte(nil), m.pending...),
		Work:    m.ingestWork,
		Lost:    m.ingestLost,
	}
}

func (m *model) live() error {
	if m.avail != AvailabilityAvailable {
		return ErrUnavailable
	}
	return nil
}

// --- control ---------------------------------------------------------------

func (m *model) GrantControl(p Principal) (Control, error) {
	if err := m.live(); err != nil {
		return m.control, err
	}
	m.control = Control{Holder: p, Epoch: m.control.Epoch + 1}
	m.cancelSuperseded()
	m.tick()
	return m.control, nil
}

func (m *model) RevokeControl() (Control, error) {
	if err := m.live(); err != nil {
		return m.control, err
	}
	m.control = Control{Holder: Principal{}, Epoch: m.control.Epoch + 1}
	m.cancelSuperseded()
	m.tick()
	return m.control, nil
}

// cancelSuperseded is ruleCancelOnRevoke. An intent admitted under an epoch
// that is no longer current will never execute, and saying so at the moment
// authority changes is what stops it arriving after the handover.
func (m *model) cancelSuperseded() {
	if !m.rules.on(ruleCancelOnRevoke) {
		return
	}
	// Walked over every RECORD rather than over the pending queue, because the
	// records are what a real implementation has to keep: IntentState(id) must
	// answer long after an intent left the queue, so a revocation written
	// against the records is the ordinary shape — and the ordinary mistake.
	// Walking the queue instead would make the guard below unreachable and its
	// removal inert, which is what nocx-ygxjv.5 was filed about. Ids are minted
	// sequentially, so this is deterministic without a map walk.
	for id := IntentID(1); id <= m.nextID; id++ {
		mi, ok := m.intents[id]
		if !ok || mi.intent.Under == m.control.Epoch {
			continue
		}
		// ruleExecutedIsIrreversible. Bytes on a PTY cannot be recalled, so a
		// revocation may not report an executed intent as cancelled. Removing
		// this guard is exactly what a revocation that walks its records
		// without the check does.
		if m.rules.on(ruleExecutedIsIrreversible) && mi.state != IntentStateAdmitted {
			continue
		}
		mi.state = IntentStateCancelled
	}
	kept := m.queue[:0]
	for _, id := range m.queue {
		if m.intents[id].intent.Under == m.control.Epoch {
			kept = append(kept, id)
		}
	}
	m.queue = kept
}

// --- input -----------------------------------------------------------------

func (m *model) Admit(i Intent) (IntentID, error) {
	if err := m.live(); err != nil {
		return 0, err
	}
	if i.At != m.inc {
		return 0, ErrStaleIncarnation
	}
	if m.control.Holder.Kind == PrincipalNone {
		return 0, ErrNoController
	}
	if i.Under != m.control.Epoch {
		return 0, ErrStaleControlEpoch
	}
	m.nextID++
	i.ID = m.nextID
	m.intents[i.ID] = &modelIntent{intent: i, state: IntentStateAdmitted}
	m.queue = append(m.queue, i.ID)
	m.tick()
	return i.ID, nil
}

func (m *model) Execute() (IntentID, IntentState, error) {
	if err := m.live(); err != nil {
		return 0, IntentStateNone, err
	}
	if m.rules.on(ruleUnknownCompletenessRefusesWrites) && m.completeness == CompletenessUnknown {
		return 0, IntentStateNone, ErrCompletenessUnknown
	}
	if len(m.queue) == 0 {
		return 0, IntentStateNone, ErrNothingAdmitted
	}
	id := m.queue[0]
	m.queue = m.queue[1:]
	mi := m.intents[id]

	if m.rules.on(ruleRevalidateAtExecution) {
		// Revalidation at CONSUMPTION. Admission established these once;
		// between then and now the authority may have changed hands and the
		// screen may have moved underneath the caller. ADR-0064's rule is that
		// the identification authorising a write is the one taken immediately
		// before it.
		if mi.intent.At != m.inc {
			mi.state = IntentStateRefused
			return id, mi.state, ErrStaleIncarnation
		}
		if mi.intent.Under != m.control.Epoch {
			mi.state = IntentStateCancelled
			return id, mi.state, ErrStaleControlEpoch
		}
		if p := mi.intent.Precondition; p != nil {
			if p.Digest != sha256.Sum256(m.screen) {
				mi.state = IntentStateRefused
				return id, mi.state, ErrPreconditionStale
			}
		}
	}

	// The bytes reach the program through the terminal this runtime was
	// constructed over, and nowhere else: what a schedule reads as "what
	// reached the PTY" is that terminal's record, so nothing here keeps a
	// second copy of it. A write that fails is reported as FAILED and never as
	// executed — nobody may say the bytes landed — and never as cancelled
	// either, because a write can fail part-way and the bytes it did take are
	// beyond recall. That is the whole reason IntentStateFailed exists.
	if _, err := m.pty.Write(m.encode(mi.intent)); err != nil {
		mi.state = IntentStateFailed
		return id, mi.state, err
	}
	mi.state = IntentStateExecuted
	m.tick()
	return id, mi.state, nil
}

// encode is ruleEncodeAgainstModes: the bytes a key becomes are decided HERE,
// against the mode the program set, because a frame of cells conveys none of it
// and a client that guessed would send differently encoded input while the
// screen looked correct.
func (m *model) encode(i Intent) []byte {
	if i.Kind != IntentKindKey {
		return append([]byte(nil), i.Payload...)
	}
	if !m.rules.on(ruleEncodeAgainstModes) {
		// The defect, stated as code: the client's own bytes, passed through.
		return append([]byte(nil), i.Payload...)
	}
	if string(i.Payload) == "Up" {
		if m.applicationCursorKeys {
			return []byte("\x1bOA")
		}
		return []byte("\x1b[A")
	}
	return append([]byte(nil), i.Payload...)
}

func (m *model) IntentState(id IntentID) IntentState {
	mi, ok := m.intents[id]
	if !ok {
		return IntentStateNone
	}
	return mi.state
}

// --- geometry --------------------------------------------------------------

func (m *model) ReportGeometry(g Geometry) error {
	if err := m.live(); err != nil {
		return err
	}
	if g.Cols == 0 || g.Rows == 0 {
		return ErrGeometryInvalid
	}
	// A report is not a commit, and this is the whole of what it does. Under
	// AD-1 as amended the client reports and the runtime decides; the previous
	// arrangement had the client resize itself and tell the backend afterwards.
	m.reportedGeom = g
	return nil
}

func (m *model) CommitGeometry(g Geometry) (GeometryCommit, error) {
	if err := m.live(); err != nil {
		return m.geom, err
	}
	if g.Cols == 0 || g.Rows == 0 {
		return m.geom, ErrGeometryInvalid
	}
	if m.rules.on(ruleAtomicGeometry) {
		// Both sides are asked BEFORE the commit opens, and the rule is about
		// the COMMIT and not about the two calls: a resize has an effect
		// outside them — the terminal is resized and the program receives
		// SIGWINCH — and a signal already delivered cannot be recalled. What a
		// terminal can honour, and all it can, is that no side is left at a
		// size nobody committed: the commit in force stands, and the side that
		// took the refused size is put back to it.
		if err := m.pty.Resize(g); err != nil {
			return m.geom, err
		}
		if err := m.emulator.Resize(g); err != nil {
			// The terminal already took a size that is not going to be
			// committed. Put it back, so what is running and what the session
			// describes are one size and not two.
			if restoreErr := m.pty.Resize(m.geom.Geometry); restoreErr != nil {
				return m.geom, errors.Join(err, restoreErr)
			}
			return m.geom, err
		}
	} else {
		// The defect, stated as code: the terminal is resized and the commit
		// recorded, and only then is the emulator asked. A refusal in between
		// leaves the two running at different sizes with nothing that puts
		// them back, while the session describes the size one of them took.
		if err := m.pty.Resize(g); err != nil {
			return m.geom, err
		}
		m.geom = GeometryCommit{Geometry: g, Revision: m.tick()}
		if err := m.emulator.Resize(g); err != nil {
			return m.geom, err
		}
		return m.geom, nil
	}
	m.geom = GeometryCommit{Geometry: g, Revision: m.tick()}
	return m.geom, nil
}

// --- output, fence, completeness -------------------------------------------

func (m *model) Ingest(b []byte) error {
	if err := m.live(); err != nil {
		return err
	}
	if m.rules.on(ruleIngestIsBounded) && len(b) > MaxIngestBytes {
		// One call carries at most the ingest bound, and a larger one is
		// REFUSED rather than truncated: nothing is ingested, no work is
		// charged, and the caller knows (the carrier's own reads are bounded by
		// the credit AD-10 gives it).
		return ErrIngestTooLarge
	}
	// A consumer that cannot keep up must not cost the STREAM. This is the
	// lossless class's own rule, and the defect it refuses to be is the obvious
	// wrong one: discard output to keep a slow subscriber happy, instead of
	// throttling the source that produced it.
	if !m.rules.on(ruleLosslessIngestSurvivesASlowConsumer) && m.anyConsumerExhausted() {
		// The defect, stated as code: the output is dropped, silently, because
		// nobody is reading.
		m.ingestLost += uint64(len(b))
		return nil
	}

	// The runtime's OWN cost for this call, in work units: the bytes it examines
	// and hands on. It is linear in the BYTES of the call and never in a count
	// inside them. `CSI 1000000000 b` is the reason that sentence exists: its
	// cost is the emulator's (ADR-0065 measured x/vt at ~180 s and ~10⁹
	// allocations and libghostty-vt clamping REP at 65535; which emulator nocx
	// ships, and therefore that clamp, is nocx-ygxjv.2's), and the runtime's
	// share of the answer is to hand it over rather than to expand it.
	m.ingestWork += uint64(len(b))
	if !m.rules.on(ruleIngestIsBounded) {
		// The defect, stated as code: the runtime does the emulator's expansion,
		// so a sixteen-byte sequence costs it a billion units of work.
		m.ingestWork += repeatCount(b)
	}

	// The sequence this call leaves open, joined to whatever was already open.
	// Only the trailing OPEN sequence is held; everything before it has either
	// been drawn or is an escape sequence that completed.
	joined := append(append([]byte(nil), m.pending...), b...)
	printable, effects, open := m.scanOutput(joined)
	m.pending = open
	if m.rules.on(ruleIngestIsBounded) {
		held, kept := uint64(len(m.pending)), uint64(MaxPendingSequence)
		if held > kept {
			// The oldest bytes of the sequence go. The loss is STATED — the
			// completeness claim and the counter — because an emulator fed a
			// partial sequence is not authoritative (design §6.7) and a silent
			// trim is how a screen that looks whole is served from one that is
			// not.
			m.ingestLost += held - kept
			m.pending = append([]byte(nil), m.pending[held-kept:]...)
			m.completeness = CompletenessLostIngest
		}
	}

	if err := m.deliver(losslessPayload(printable)); err != nil {
		return err
	}
	rev := m.tick()
	for i := range effects {
		m.nextEffect++
		e := Effect{
			ID:   m.nextEffect,
			At:   m.inc,
			Kind: effects[i].kind,
			Body: append([]byte(nil), effects[i].body...),
		}
		m.lastEffect = &e
		if err := m.deliver(effectPayload(e)); err != nil {
			return err
		}
	}
	// One frame per ingest is the model's stand-in for the frame protocol: it
	// is what makes a consumer's queue fill, and its body is a snapshot of the
	// screen. The real frame format is epic nocx-zg3k3's and is not declared
	// here.
	return m.deliver(framePayload(rev, m.screen))
}

// streamEffect is what the program's output asked for, before the runtime mints
// the identity it will be delivered under.
type streamEffect struct {
	kind EffectKind
	body []byte
}

// The two control bytes the model's stand-in parser knows by name.
const (
	esc = 0x1b
	bel = 0x07
)

// scanOutput is the model's stand-in for the emulator's parser, and it is
// deliberately NOT a terminal: it finds the units a chunk of output is made of
// — the text that reaches the screen, the non-visual effects the program asked
// for, and the trailing sequence that has not terminated — and nothing else.
// The DECCKM set and clear it applies is the one mode this model carries, which
// is what makes "input is intent" concrete.
//
// A real parser's cost, its mode table and its own bounds are the emulator's
// (ADR-0065, nocx-ygxjv.2). What this returns is only ever used to assert the
// RUNTIME's obligations.
func (m *model) scanOutput(b []byte) (printable []byte, effects []streamEffect, open []byte) {
	for i := 0; i < len(b); {
		if b[i] == bel {
			effects = append(effects, streamEffect{kind: EffectBell})
			i++
			continue
		}
		if b[i] != esc {
			printable = append(printable, b[i])
			i++
			continue
		}
		if i+1 >= len(b) {
			return printable, effects, b[i:]
		}
		switch b[i+1] {
		case ']': // OSC, terminated by BEL or ST
			body, next, ok := oscBody(b, i+2)
			if !ok {
				return printable, effects, b[i:]
			}
			if kind, isEffect := effectOfOSC(body); isEffect {
				effects = append(effects, streamEffect{kind: kind, body: body})
			}
			i = next
		case 'P': // DCS, terminated by ST. Nothing in it is drawn.
			next, ok := stTerminated(b, i+2)
			if !ok {
				return printable, effects, b[i:]
			}
			i = next
		case '[': // CSI, terminated by a final byte
			end, ok := csiEnd(b, i+2)
			if !ok {
				return printable, effects, b[i:]
			}
			applyCSI(b[i+2:end], m)
			i = end
		default: // ESC <byte>: two bytes and it is done, nothing drawn
			i += 2
		}
	}
	return printable, effects, nil
}

// oscBody reads an OSC body from b starting at start, which is just past
// `ESC ]`. The returned body excludes the terminator and next is the offset
// just past it; ok is false when the sequence does not terminate within b —
// which is the hostile-capture case rather than an edge of it.
func oscBody(b []byte, start int) (body []byte, next int, ok bool) {
	for i := start; i < len(b); i++ {
		if b[i] == bel {
			return b[start:i], i + 1, true
		}
		if b[i] == esc && i+1 < len(b) && b[i+1] == '\\' {
			return b[start:i], i + 2, true
		}
	}
	return nil, len(b), false
}

// stTerminated reads a string terminated by ST from b starting at start, which
// is just past `ESC P`.
func stTerminated(b []byte, start int) (next int, ok bool) {
	for i := start; i < len(b); i++ {
		if b[i] == esc && i+1 < len(b) && b[i+1] == '\\' {
			return i + 2, true
		}
	}
	return len(b), false
}

// csiEnd reads a CSI from b starting at start, which is just past `ESC [`, and
// returns the offset just past its final byte (0x40–0x7e). A sequence whose
// final byte has not arrived is open.
func csiEnd(b []byte, start int) (next int, ok bool) {
	for i := start; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return i + 1, true
		}
	}
	return len(b), false
}

// effectOfOSC maps the OSC numbers design §6.2 names onto their effects. The
// numbers that carry none carry none here: a fence (133, 1337) has its own
// vocabulary above, and `9;4;<state>;<percent>` is ConEmu's progress hint,
// which the renderer's own parser returns null for so that it stays available
// to whatever renders progress (frontend/src/renderers/xterm.ts, onNotification)
// — an OSC arriving is not an effect arriving. The payload is not parsed
// further: decoding it is the surface's, and delivering it once is this
// contract's.
func effectOfOSC(body []byte) (EffectKind, bool) {
	num, rest, _ := bytes.Cut(body, []byte(";"))
	switch string(num) {
	case "0", "2":
		return EffectTitle, true
	case "7":
		return EffectCwdReport, true
	case "9":
		if bytes.HasPrefix(rest, []byte("4;")) {
			return EffectNone, false
		}
		return EffectNotification, true
	case "777":
		return EffectNotification, true
	case "52":
		return EffectClipboard, true
	default:
		return EffectNone, false
	}
}

// applyCSI applies the one mode this model carries. DECCKM is enough to make
// "input is intent" concrete; the mode table is the emulator's (nocx-ygxjv.2).
func applyCSI(params []byte, m *model) {
	switch string(params) {
	case "?1h":
		m.applicationCursorKeys = true
	case "?1l":
		m.applicationCursorKeys = false
	}
}

// repeatCount reads the count out of a REP sequence (`CSI <n> b`). It exists
// ONLY so the defect branch can charge the runtime the emulator's work: nothing
// in the runtime's own path expands a repeat, and when the rule is on nothing
// calls this.
func repeatCount(b []byte) uint64 {
	_, rest, ok := bytes.Cut(b, []byte("\x1b["))
	if !ok {
		return 0
	}
	digits, _, ok := bytes.Cut(rest, []byte("b"))
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(string(digits), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (m *model) ReportHole(lost uint64) error {
	if err := m.live(); err != nil {
		return err
	}
	if lost == 0 {
		return nil
	}
	// An emulator fed only the surviving suffix is not authoritative, and the
	// runtime must say so rather than serve a screen that looks whole.
	m.completeness = CompletenessLostIngest
	m.tick()
	return nil
}

// ---------------------------------------------------------------------------
// The instruments the model is built over.
//
// These are the TEST's, and the model reaches them only through the contract's
// ports (contract.go, Terminal and Emulator). They exist because three things a
// schedule must see are invisible from the runtime's own account — the bytes
// that reached the program, whether a resize failed, and (for a consumer) what
// it was told — and reading them here is reading them where they are true
// rather than reading the model's private copy of them.
//
// They implement the contract's TerminalInstrument and EmulatorInstrument
// (contract.go) — the reading half a schedule judges the boundary through, and
// the refusal a real tty only performs when it actually refuses a size. In a
// real runtime's tests the same two capabilities arrive as a wrapper in front
// of the real tty and the real emulator, which is why the interfaces and not
// these structs are what a schedule names.
// ---------------------------------------------------------------------------

// ptySink is the terminal the model is constructed over: it records what it was
// handed and it can be told to refuse a resize, which is the only way to make
// one side of a geometry commit fail without a real terminal.
type ptySink struct {
	writes [][]byte
	size   Geometry
	refuse bool
}

func newPtySink() *ptySink {
	return &ptySink{size: Geometry{Cols: 80, Rows: 24}}
}

func (p *ptySink) Write(b []byte) (int, error) {
	p.writes = append(p.writes, append([]byte(nil), b...))
	return len(b), nil
}

func (p *ptySink) Resize(g Geometry) error {
	if p.refuse {
		return errResizeRefused
	}
	p.size = g
	return nil
}

// Written is what reached the terminal, in order, as a copy. It is the
// instrument's and deliberately not the contract's: a runtime that kept its own
// log of what it sent would be spending memory on an observation this test gets
// for free, and no port could ask a real tty for it.
func (p *ptySink) Written() [][]byte { return slices.Clone(p.writes) }

// Size is the size the terminal is running at, which is a different fact from
// the commit in force and the one a refusal has to be judged against.
func (p *ptySink) Size() Geometry { return p.size }

// RefuseResize makes every Resize up to AcceptResize fail, leaving the size
// where it is: a terminal that refuses has not been resized.
func (p *ptySink) RefuseResize() { p.refuse = true }

// AcceptResize ends the refusal RefuseResize began.
func (p *ptySink) AcceptResize() { p.refuse = false }

// emulatorSink is the screen the model is constructed over: nothing is fed to
// it (the model's parser is a stand-in, and the real emulator is nocx-ygxjv.2's),
// and it is here for the other half of a geometry commit.
type emulatorSink struct {
	size   Geometry
	refuse bool
}

func newEmulatorSink() *emulatorSink {
	return &emulatorSink{size: Geometry{Cols: 80, Rows: 24}}
}

func (e *emulatorSink) Resize(g Geometry) error {
	if e.refuse {
		return errResizeRefused
	}
	e.size = g
	return nil
}

// Size is the size the emulator is running at. A commit that opened on both
// sides leaves this and the terminal's at the same size; a refusal leaves both
// where the commit in force says they are.
func (e *emulatorSink) Size() Geometry { return e.size }

// RefuseResize makes every Resize up to AcceptResize fail, leaving the size
// where it is: an emulator that refuses has not been resized.
func (e *emulatorSink) RefuseResize() { e.refuse = true }

// AcceptResize ends the refusal RefuseResize began.
func (e *emulatorSink) AcceptResize() { e.refuse = false }

// errResizeRefused is what either instrument answers once a schedule has told it
// to. A real side's refusal is its own error — internal/pty's, the emulator's —
// and the runtime propagates whichever it is: the refusal is the side's to name
// and not this package's to invent.
var errResizeRefused = errors.New("sessionruntime: the size was refused")

func (m *model) Completed(at Incarnation, nonce FenceNonce, _ int) {
	if m.avail != AvailabilityAvailable || at != m.inc {
		return
	}
	switch m.rendezvous.State {
	case RendezvousAwaitingAuthenticated:
		if m.rendezvous.Nonce != nonce {
			return
		}
		m.rendezvous.State = RendezvousComplete
		m.tick()
	default:
		m.rendezvous = Rendezvous{State: RendezvousAwaitingSighting, Nonce: nonce, At: at}
		m.tick()
	}
}

func (m *model) SightFence(nonce FenceNonce, source []byte) error {
	if err := m.live(); err != nil {
		return err
	}
	switch m.rendezvous.State {
	case RendezvousAwaitingSighting:
		if m.rendezvous.Nonce != nonce {
			return ErrNonceMismatch
		}
		m.rendezvous.State = RendezvousComplete
		m.rendezvous.SightedAt = m.rev
		m.rendezvous.PinnedSource = append([]byte(nil), source...)
		m.tick()
		return nil
	default:
		// ruleSightingAuthorisesNothing. A sighted marker may only LOCATE an
		// already-authenticated event (ADR-0024 decision 1), so arriving first
		// it parks and grants nothing — a program printing a forged fence must
		// not be able to close a block or choose a capture endpoint.
		st := RendezvousAwaitingAuthenticated
		if !m.rules.on(ruleSightingAuthorisesNothing) {
			st = RendezvousComplete // the defect, stated as code
		}
		m.rendezvous = Rendezvous{State: st, Nonce: nonce, At: m.inc, SightedAt: m.rev}
		if m.rules.on(rulePinSource) {
			m.rendezvous.PinnedSource = append([]byte(nil), source...)
		}
		m.tick()
		return nil
	}
}

func (m *model) ExpireRendezvous() error {
	if err := m.live(); err != nil {
		return err
	}
	switch m.rendezvous.State {
	case RendezvousAwaitingSighting, RendezvousAwaitingAuthenticated:
		m.rendezvous.State = RendezvousExpired
		m.rendezvous.PinnedSource = nil
		// The body may still be worth keeping; it may not be described as the
		// command's complete output.
		if m.completeness == CompletenessComplete {
			m.completeness = CompletenessNoFence
		}
		m.tick()
		return nil
	default:
		return ErrNoRendezvous
	}
}

// ---------------------------------------------------------------------------
// Delivery: what a consumer is, what its queue may hold, and what the runtime
// owes when it cannot hold any more.
//
// The classes and the bounds are the CONTRACT's (contract.go, DeliveryClass).
// What is here is the model's delivery path, so that a schedule can wedge a
// consumer and read what the runtime did about it.
//
// A consumer arriving or leaving is not a transition of the TERMINAL, which is
// why it is not on Runtime as a transition: attaching, resending, offering an
// effect and losing a consumer are the delivery side's, and they reach a
// schedule through Consumers() — a port, so that a schedule can hold a wedged
// consumer and a real runtime can hand out real ones (contract.go, Consumers).
// ---------------------------------------------------------------------------

// modelScreenBytes is how much of the screen the model keeps: a window on the
// emulator's state, not the stream.
const modelScreenBytes = 64

// payload is one thing the runtime hands a consumer, as the model's union types
// it. Its CLASS decides where it goes, and the producer decides the class —
// never the consumer, which is what makes "every payload belongs to exactly
// one" checkable rather than a habit.
type payload struct {
	class  DeliveryClass
	rev    Revision // coalescable: the revision of the visual state
	bytes  []byte   // lossless: the output ingested, or a frame's cells
	effect Effect   // at-most-once
}

func losslessPayload(b []byte) payload {
	return payload{class: DeliveryLossless, bytes: append([]byte(nil), b...)}
}

func framePayload(rev Revision, screen []byte) payload {
	return payload{class: DeliveryCoalescable, rev: rev, bytes: append([]byte(nil), screen...)}
}

func effectPayload(e Effect) payload {
	return payload{class: classOfEffect(e.Kind), effect: e}
}

// classOfEffect is the ONE place an effect's class is decided, and it is
// exhaustive by construction: every kind the vocabulary declares is named here,
// and a kind added to contract.go without being named falls to
// DeliveryUnclassified and is REFUSED rather than delivered under a guess.
func classOfEffect(k EffectKind) DeliveryClass {
	switch k {
	case EffectBell, EffectNotification, EffectClipboard, EffectTitle, EffectCwdReport:
		return DeliveryAtMostOnce
	default:
		return DeliveryUnclassified
	}
}

// consumer is one subscriber: a queue nobody reads in the schedules that hold
// it wedged, and the counts that tell it what it lost. It implements the
// contract's Consumer — the reads below are what a schedule sees of it, and the
// queue itself is nobody's but the runtime's.
type consumer struct {
	queue []payload
	// coalesced and effectsLost are what the runtime DROPPED for this consumer,
	// by class. They are the report: a consumer is TOLD what it lost, and that
	// what it still holds is stale, rather than discovering a hole in it.
	coalesced   uint64
	effectsLost uint64
	stale       bool
}

func (c *consumer) Pending() int { return len(c.queue) }

// HeldBytes is the memory the runtime spends on this consumer: the payloads it
// is holding for it.
func (c *consumer) HeldBytes() int {
	held := 0
	for _, p := range c.queue {
		held += len(p.bytes) + len(p.effect.Body)
	}
	return held
}

func (c *consumer) Coalesced() uint64   { return c.coalesced }
func (c *consumer) EffectsLost() uint64 { return c.effectsLost }
func (c *consumer) Stale() bool         { return c.stale }

// Effects is the at-most-once payloads the consumer holds, oldest first. It is
// the queue's effects and nothing else: a payload that changed no cell is the
// only thing the duplicate policy is stated over.
func (c *consumer) Effects() []Effect {
	held := make([]Effect, 0, len(c.queue))
	for _, p := range c.queue {
		if p.class == DeliveryAtMostOnce {
			held = append(held, p.effect)
		}
	}
	return held
}

// hasEffect reports whether the consumer already holds THIS effect. Identity is
// the pair, not the number alone: an EffectID identifies an effect for as long
// as its INCARNATION lives (contract.go), so a later incarnation minting the
// same number again is a different effect and comparing numbers alone would
// swallow a bell.
func (c *consumer) hasEffect(e Effect) bool {
	for _, p := range c.queue {
		if p.class == DeliveryAtMostOnce && p.effect.At == e.At && p.effect.ID == e.ID {
			return true
		}
	}
	return false
}

// deliveryBudget is the allowance one consumer's queue draws on. The schedules
// that attach TWO sessions to one budget are asserting the policy stated in
// MaxPendingFrames: the allowance is per session, so one session reading slowly
// may not spend another's.
type deliveryBudget struct {
	remaining map[SessionID]int
}

func newDeliveryBudget() *deliveryBudget {
	return &deliveryBudget{remaining: map[SessionID]int{}}
}

// take spends one payload's worth of the account's allowance, opening the
// account at MaxPendingFrames the first time it is drawn on.
func (b *deliveryBudget) take(account SessionID) bool {
	if _, ok := b.remaining[account]; !ok {
		b.remaining[account] = MaxPendingFrames
	}
	if b.remaining[account] <= 0 {
		return false
	}
	b.remaining[account]--
	return true
}

// account is the budget account this session's queues spend from, and WHICH
// account that is, is rulePerSessionAllowance.
func (m *model) account() SessionID {
	if !m.rules.on(rulePerSessionAllowance) {
		// The defect, stated as code: one account every session spends from, so
		// the first wedged consumer starves the others.
		return ""
	}
	return m.inc.Session
}

// --- the Consumers port, which the model satisfies itself ------------------
//
// Four methods, and each is deliberately NOT a transition of the terminal: a
// consumer joining, going away, being handed a full frame again, or being
// offered an effect the producer already minted, all leave the session's state
// exactly as it was (contract.go, Consumers).

// Attach joins a consumer and hands it back for a schedule to hold wedged.
func (m *model) Attach() Consumer { return m.attach() }

// Attached is every joined consumer, in attach order.
func (m *model) Attached() []Consumer {
	attached := make([]Consumer, 0, len(m.consumers))
	for _, c := range m.consumers {
		attached = append(attached, c)
	}
	return attached
}

// Offer is the producer's side of the duplicate policy: handing a consumer an
// effect it has already been given delivers nothing.
func (m *model) Offer(e Effect) error { return m.deliverEffect(e) }

// Resend is a full frame — a snapshot, a re-attachment, a resync. It carries
// cells, and ruleResendCarriesNoEffects is what keeps it from carrying an
// effect with them.
func (m *model) Resend() error { return m.resendState() }

// Lost is a consumer going away. It is here rather than on Runtime for the
// reason the header above gives, and it is not nothing: a disconnect treated as
// a loss of authority throws away input a person already committed to, which is
// what ruleObserverLossIsNotControlLoss refuses.
func (m *model) Lost() { m.observerLost() }

// attach adds a consumer to this session.
func (m *model) attach() *consumer {
	c := &consumer{}
	m.consumers = append(m.consumers, c)
	return c
}

// anyConsumerExhausted reports whether some consumer has spent the allowance,
// which is the state the lossless-ingest defect reacts to.
func (m *model) anyConsumerExhausted() bool {
	for _, c := range m.consumers {
		if len(c.queue) >= MaxPendingFrames {
			return true
		}
	}
	return false
}

// deliver routes one payload to whatever its class says may hold it. It is the
// ONE place a class decides behaviour, so no payload is treated as coalescable
// by accident.
func (m *model) deliver(p payload) error {
	switch p.class {
	case DeliveryLossless:
		return m.feedEmulator(p.bytes)
	case DeliveryCoalescable, DeliveryAtMostOnce:
		for _, c := range m.consumers {
			m.enqueue(c, p)
		}
		return nil
	default:
		return ErrUnclassifiedDelivery
	}
}

// feedEmulator is where a lossless payload lands. Nothing here may drop any of
// it: the SCREEN is a window, and the stream the class covers is not.
//
// The trim is what makes a rendezvous pin CONTENT rather than a row number:
// later output can destroy the rows a fence was seen on.
func (m *model) feedEmulator(b []byte) error {
	m.screen = append(m.screen, b...)
	if len(m.screen) > modelScreenBytes {
		m.screen = m.screen[len(m.screen)-modelScreenBytes:]
	}
	return nil
}

// enqueue is the bounded path one payload takes to one consumer.
func (m *model) enqueue(c *consumer, p payload) {
	if p.class == DeliveryAtMostOnce && m.rules.on(ruleDuplicateEffectIsSuppressed) && c.hasEffect(p.effect) {
		// The duplicate policy: the bell is the same bell, and applying it
		// twice rings twice. Nothing was lost by not delivering it, so nothing
		// is reported either.
		return
	}
	if !m.rules.on(ruleConsumerQueueIsBounded) {
		// The defect, stated as code: the queue grows without bound, which is
		// what one unwatched session does to the process holding it.
		c.queue = append(c.queue, p)
		return
	}
	if !m.budget.take(m.account()) {
		m.shed(c, p)
		return
	}
	c.queue = append(c.queue, p)
}

// shed makes room in a queue whose session has spent its allowance. The OLDEST
// coalescable payload goes first, because a later frame supersedes it; if the
// queue holds only effects, the oldest EFFECT goes, which at-most-once permits
// — zero deliveries is inside that class — as long as the loss is reported.
func (m *model) shed(c *consumer, p payload) {
	for i, queued := range c.queue {
		if queued.class != DeliveryCoalescable {
			continue
		}
		c.queue = append(c.queue[:i], c.queue[i+1:]...)
		m.reportLoss(c, queued)
		c.queue = append(c.queue, p)
		return
	}
	if len(c.queue) == 0 {
		// Nothing to shed: the payload that just arrived is what this consumer
		// does not get.
		m.reportLoss(c, p)
		return
	}
	oldest := c.queue[0]
	c.queue = c.queue[1:]
	m.reportLoss(c, oldest)
	c.queue = append(c.queue, p)
}

// reportLoss is ruleConsumerLossIsReported: the consumer is told. Without it
// the payload is gone and nothing the consumer holds says so — which is how a
// client paints a screen it believes is current.
func (m *model) reportLoss(c *consumer, p payload) {
	if !m.rules.on(ruleConsumerLossIsReported) {
		return
	}
	if p.class == DeliveryAtMostOnce {
		c.effectsLost++
	} else {
		c.coalesced++
	}
	c.stale = true
}

// deliverEffect is the producer's side of the duplicate policy: handing a
// consumer an effect it has already been given delivers nothing.
func (m *model) deliverEffect(e Effect) error {
	return m.deliver(effectPayload(e))
}

// resendState is a full frame: a snapshot, a re-attachment, a resync. It
// carries cells, and ruleResendCarriesNoEffects is what keeps it from carrying
// an effect — a resend of state must not re-ring a bell or re-write a clipboard
// (design §6.2).
func (m *model) resendState() error {
	if err := m.deliver(framePayload(m.rev, m.screen)); err != nil {
		return err
	}
	if m.rules.on(ruleResendCarriesNoEffects) || m.lastEffect == nil {
		return nil
	}
	// The defect, stated as code: the resend replays the effects it has already
	// delivered. It appends directly on purpose — the hazard is a resend that
	// carries effects at all, however the resend is built.
	for _, c := range m.consumers {
		c.queue = append(c.queue, effectPayload(*m.lastEffect))
	}
	return nil
}

// --- failure and observation -----------------------------------------------

func (m *model) Fail(string) error {
	if m.avail == AvailabilityUnavailable {
		return ErrUnavailable
	}
	m.avail = AvailabilityUnavailable
	if m.rules.on(ruleFailRevokes) {
		for _, id := range m.queue {
			mi := m.intents[id]
			if mi.state == IntentStateAdmitted {
				mi.state = IntentStateCancelled
			}
		}
		m.queue = nil
		m.control = Control{Holder: Principal{}, Epoch: m.control.Epoch + 1}
	}
	m.tick()
	return nil
}

// observerLost is not part of Runtime on purpose: an observer coming and going
// is not a transition of the terminal. It is here so a schedule can exercise
// the distinction that matters — losing a WATCHER is not losing CONTROL.
func (m *model) observerLost() {
	if m.rules.on(ruleObserverLossIsNotControlLoss) {
		return
	}
	// The defect: a disconnect treated as a loss of authority, which throws
	// away input a person already committed to.
	for _, id := range m.queue {
		m.intents[id].state = IntentStateCancelled
	}
	m.queue = nil
}

func (m *model) Snapshot() Snapshot {
	s := Snapshot{
		Revision:     m.rev,
		At:           m.inc,
		Availability: m.avail,
		Control:      m.control,
		Geometry:     m.geom,
		Screen:       append([]byte(nil), m.screen...),
		Rendezvous:   m.rendezvous.State,
		Completeness: m.completeness,
	}
	if !m.rules.on(ruleSnapshotIsPassive) {
		// The defect: a read that moves the clock, so two observers
		// resynchronising produce a revision neither of them can reason about.
		m.tick()
	}
	return s
}

// ---------------------------------------------------------------------------
// Assertion helper: a schedule reports a NAMED assertion rather than calling
// t.Errorf, so the same schedule can be run against a model with a rule removed
// and its failure inspected.
// ---------------------------------------------------------------------------

type assertionError struct {
	Assertion string
	Detail    string
}

func (e *assertionError) Error() string {
	return fmt.Sprintf("assertion %q failed: %s", e.Assertion, e.Detail)
}

func failed(assertion, format string, args ...any) error {
	return &assertionError{Assertion: assertion, Detail: fmt.Sprintf(format, args...)}
}
