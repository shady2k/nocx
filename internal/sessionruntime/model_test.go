package sessionruntime

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
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
	// ruleAtomicGeometry: a commit that fails on either the PTY or the emulator
	// commits on neither, and the previous commit stands.
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
)

var ruleNames = map[rule]string{
	ruleCancelOnRevoke:                   "cancel-on-revoke",
	ruleExecutedIsIrreversible:           "executed-is-irreversible",
	ruleRevalidateAtExecution:            "revalidate-at-execution",
	ruleSightingAuthorisesNothing:        "sighting-authorises-nothing",
	rulePinSource:                        "pin-source",
	ruleAtomicGeometry:                   "atomic-geometry",
	ruleSnapshotIsPassive:                "snapshot-is-passive",
	ruleObserverLossIsNotControlLoss:     "observer-loss-is-not-control-loss",
	ruleFailRevokes:                      "fail-revokes",
	ruleUnknownCompletenessRefusesWrites: "unknown-completeness-refuses-writes",
	ruleEncodeAgainstModes:               "encode-against-modes",
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

	queue    []IntentID
	intents  map[IntentID]*modelIntent
	nextID   IntentID
	executed [][]byte // what reached the "PTY", in order

	geom GeometryCommit
	// reportedGeom is the last thing a client said it could show. The runtime
	// decides; this is only evidence for that decision.
	reportedGeom Geometry
	// ptyRefusesResize / emulatorRefusesResize are the injectable halves of a
	// geometry commit. A real runtime has a channel and an emulator; the model
	// has two booleans, which is enough to state the atomicity rule.
	ptyRefusesResize      bool
	emulatorRefusesResize bool

	screen []byte
	// applicationCursorKeys is the one mode the model carries. It is enough to
	// make "input is intent" concrete: the same IntentKindKey produces
	// different bytes depending on what the PROGRAM set, and a client could not
	// have known which.
	applicationCursorKeys bool

	rendezvous   Rendezvous
	completeness Completeness
}

var _ Runtime = (*model)(nil)

func newModel(rules ruleSet) *model {
	return &model{
		rules:        rules,
		inc:          Incarnation{Session: "S", Generation: 1},
		avail:        AvailabilityAvailable,
		rev:          1,
		intents:      map[IntentID]*modelIntent{},
		geom:         GeometryCommit{Geometry: Geometry{Cols: 80, Rows: 24}, Revision: 1},
		completeness: CompletenessComplete,
	}
}

func (m *model) tick() Revision { m.rev++; return m.rev }

func (m *model) Incarnation() Incarnation   { return m.inc }
func (m *model) Availability() Availability { return m.avail }
func (m *model) Revision() Revision         { return m.rev }
func (m *model) Control() Control           { return m.control }
func (m *model) Geometry() GeometryCommit   { return m.geom }
func (m *model) Rendezvous() Rendezvous     { return m.rendezvous }
func (m *model) Completeness() Completeness { return m.completeness }

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
	kept := m.queue[:0]
	for _, id := range m.queue {
		mi := m.intents[id]
		if mi.intent.Under != m.control.Epoch {
			// ruleExecutedIsIrreversible: only an ADMITTED intent can be
			// cancelled. An executed one is not in the queue at all, and the
			// guard below is what keeps a broken implementation from putting
			// it back.
			if m.rules.on(ruleExecutedIsIrreversible) && mi.state == IntentStateExecuted {
				continue
			}
			mi.state = IntentStateCancelled
			continue
		}
		kept = append(kept, id)
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

	m.executed = append(m.executed, m.encode(mi.intent))
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
		// Both sides are asked BEFORE either is recorded. The interval this
		// field describes opens when both accepted and closes when the next
		// commit replaces it, so a refusal leaves the session describing what
		// it is still running at.
		if m.ptyRefusesResize || m.emulatorRefusesResize {
			return m.geom, errors.New("sessionruntime: resize refused")
		}
	} else {
		// The defect: the PTY is changed, then the emulator, and a failure in
		// between leaves two sizes in the system.
		if m.ptyRefusesResize {
			return m.geom, errors.New("sessionruntime: resize refused")
		}
		m.geom = GeometryCommit{Geometry: g, Revision: m.tick()}
		if m.emulatorRefusesResize {
			return m.geom, errors.New("sessionruntime: resize refused")
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
	// Enough of an emulator to make the mode real: the program turns
	// application cursor keys on and off, and the runtime's encoder follows.
	if bytes.Contains(b, []byte("\x1b[?1h")) {
		m.applicationCursorKeys = true
	}
	if bytes.Contains(b, []byte("\x1b[?1l")) {
		m.applicationCursorKeys = false
	}
	printable := bytes.ReplaceAll(b, []byte("\x1b[?1h"), nil)
	printable = bytes.ReplaceAll(printable, []byte("\x1b[?1l"), nil)
	// A trim: later output can destroy the rows a fence was seen on, which is
	// exactly why a rendezvous pins content rather than a row number.
	m.screen = append(m.screen, printable...)
	if len(m.screen) > 64 {
		m.screen = m.screen[len(m.screen)-64:]
	}
	m.tick()
	return nil
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

// establishNothing puts the runtime in the state a real one starts in, before
// it has established anything: completeness UNKNOWN, where the write gate
// (ruleUnknownCompletenessRefusesWrites) refuses everything. The model is
// CONSTRUCTED complete because it has no attach step to establish anything
// from — a real runtime's producer is nocx-ygxjv.2's — so this is that missing
// producer, named here rather than written as a bare field by whichever
// schedule happened to need it.
func (m *model) establishNothing() {
	m.completeness = CompletenessUnknown
}

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
