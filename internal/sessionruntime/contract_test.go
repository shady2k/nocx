package sessionruntime

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The contract as an executable document (bead nocx-ygxjv.1, ADR-0066).
//
// Every schedule below is a REAL arrival order, not a hypothesis: the order the
// halves of a rendezvous arrive in, the order a handover and the input queued
// under the old authority arrive in, the order a resize and the output it
// interrupts arrive in. Each is a function returning an error rather than a
// test calling t.Errorf, so the same schedule can be run twice — once against a
// model obeying every rule, where it must pass, and once against a model with
// exactly ONE rule removed, where a NAMED assertion must fail. A rule that
// cannot be removed to make a named assertion fail is not being tested by the
// schedule paired with it.
//
// The model these run against is in model_test.go, which is not this file's to
// rewrite: it is the reference implementation the real runtime (nocx-ygxjv.2)
// will have to satisfy the same way. Nothing here sleeps, nothing here is
// concurrent, and nothing here reads a clock — ExpireRendezvous exists as a
// CALL for exactly that reason.

// ---------------------------------------------------------------------------
// Fixtures. Every schedule is a real arrival order; these are the parts of one
// that are not its subject.
// ---------------------------------------------------------------------------

// person and agent are the two subjects the vocabulary separates. Telling two
// people, or two agents, apart is Principal.ID's job and not the runtime's.
func person() Principal { return Principal{Kind: PrincipalPerson, ID: "person-at-a-client"} }

func agent() Principal { return Principal{Kind: PrincipalAgent, ID: "agent-under-delegation"} }

// nonceOf mints a fence nonce. The runtime never mints one and never
// authenticates one (contract.go, FenceNonce); the schedules distinguish the
// two halves of a rendezvous by it and nothing more.
func nonceOf(b byte) FenceNonce {
	var n FenceNonce
	for i := range n {
		n[i] = b
	}
	return n
}

// grant and admitKey are setup rather than subject matter: no schedule in this
// file is about the grant itself or about the shape of an intent, and a failure
// here names itself as setup so it cannot be mistaken for the schedule's finding.
func grant(m *model, p Principal) (Control, error) {
	c, err := m.GrantControl(p)
	if err != nil {
		return c, failed("setup/grant-control", "granting control to %+v: %v", p, err)
	}
	return c, nil
}

func admitKey(m *model, ctrl Control, payload []byte) (IntentID, error) {
	id, err := m.Admit(Intent{
		At:      m.Incarnation(),
		Under:   ctrl.Epoch,
		By:      ctrl.Holder,
		Kind:    IntentKindKey,
		Payload: payload,
	})
	if err != nil {
		return 0, failed("setup/admit", "admitting a key intent under epoch %d: %v", ctrl.Epoch, err)
	}
	return id, nil
}

// lastExecuted is what reached the PTY last, which is the only place a schedule
// can see the bytes the runtime decided on.
func lastExecuted(m *model) []byte {
	if len(m.executed) == 0 {
		return nil
	}
	return m.executed[len(m.executed)-1]
}

func intentStateName(s IntentState) string {
	switch s {
	case IntentStateNone:
		return "none"
	case IntentStateAdmitted:
		return "admitted"
	case IntentStateExecuted:
		return "executed"
	case IntentStateCancelled:
		return "cancelled"
	case IntentStateRefused:
		return "refused"
	case IntentStateFailed:
		return "failed"
	default:
		return fmt.Sprintf("IntentState(%d)", int(s))
	}
}

func rendezvousName(s RendezvousState) string {
	switch s {
	case RendezvousIdle:
		return "idle"
	case RendezvousAwaitingSighting:
		return "awaiting-sighting"
	case RendezvousAwaitingAuthenticated:
		return "awaiting-authenticated"
	case RendezvousComplete:
		return "complete"
	case RendezvousExpired:
		return "expired"
	default:
		return fmt.Sprintf("RendezvousState(%d)", int(s))
	}
}

func completenessName(c Completeness) string {
	switch c {
	case CompletenessUnknown:
		return "unknown"
	case CompletenessComplete:
		return "complete"
	case CompletenessLostIngest:
		return "lost-ingest"
	case CompletenessNoFence:
		return "no-fence"
	case CompletenessEvicted:
		return "evicted"
	default:
		return fmt.Sprintf("Completeness(%d)", int(c))
	}
}

func availabilityName(a Availability) string {
	switch a {
	case AvailabilityUnknown:
		return "unknown"
	case AvailabilityAvailable:
		return "available"
	case AvailabilityUnavailable:
		return "unavailable"
	default:
		return fmt.Sprintf("Availability(%d)", int(a))
	}
}

func principalName(k PrincipalKind) string {
	switch k {
	case PrincipalNone:
		return "none"
	case PrincipalPerson:
		return "person"
	case PrincipalAgent:
		return "agent"
	default:
		return fmt.Sprintf("PrincipalKind(%d)", int(k))
	}
}

// ---------------------------------------------------------------------------
// Reachability bookkeeping.
//
// The schedules below record every enumerated state they pass through, so the
// walk at the end of this file can say of each constant either that a schedule
// reaches it or, in words, why nothing does. It is package state because a
// schedule's signature is a model and an error and nothing else; the walk
// RESETS it and drives every schedule itself, so its answer does not depend on
// which test ran first.
// ---------------------------------------------------------------------------

var observedStates = map[string]bool{}

const (
	kindIntent       = "intent-state"
	kindRendezvous   = "rendezvous-state"
	kindCompleteness = "completeness"
	kindAvailability = "availability"
	kindPrincipal    = "principal-kind"
)

func observe(kind string, value int) { observedStates[fmt.Sprintf("%s/%d", kind, value)] = true }

func wasObserved(kind string, value int) bool {
	return observedStates[fmt.Sprintf("%s/%d", kind, value)]
}

// ---------------------------------------------------------------------------
// "An invalid event changes nothing" — the property that makes invalid events
// testable at all, and the reason a runtime's refusals can be asserted rather
// than hoped for. fingerprint is everything a caller can observe of the model:
// a comparison is then a statement about the runtime rather than about the one
// field somebody remembered to check.
// ---------------------------------------------------------------------------

type fingerprint struct {
	Snapshot

	NextID       IntentID
	Admitted     []IntentID
	IntentStates map[IntentID]IntentState
	Executed     [][]byte
	Reported     Geometry
}

// fingerprintOf reads the model. It takes a snapshot, so it is passive only
// while ruleSnapshotIsPassive is on — every use in this file is on a model
// built by allRules(), where it is.
func fingerprintOf(m *model) fingerprint {
	f := fingerprint{
		Snapshot:     m.Snapshot(),
		NextID:       m.nextID,
		Admitted:     append([]IntentID(nil), m.queue...),
		Executed:     append([][]byte(nil), m.executed...),
		Reported:     m.reportedGeom,
		IntentStates: make(map[IntentID]IntentState, len(m.intents)),
	}
	for id, mi := range m.intents {
		f.IntentStates[id] = mi.state
	}
	return f
}

// diff names the fields that differ rather than dumping the struct: a failure
// message has to be deterministic, and a map printed with %v is not.
func (f fingerprint) diff(g fingerprint) string {
	var changed []string
	add := func(field string, differ bool) {
		if differ {
			changed = append(changed, field)
		}
	}
	add("revision", f.Revision != g.Revision)
	add("incarnation", f.At != g.At)
	add("availability", f.Availability != g.Availability)
	add("control", f.Control != g.Control)
	add("geometry", f.Geometry != g.Geometry)
	add("reported geometry", f.Reported != g.Reported)
	add("screen", !bytes.Equal(f.Screen, g.Screen))
	add("rendezvous", f.Rendezvous != g.Rendezvous)
	add("completeness", f.Completeness != g.Completeness)
	add("next intent id", f.NextID != g.NextID)
	add("admitted order", !slices.Equal(f.Admitted, g.Admitted))
	add("executed bytes", !reflect.DeepEqual(f.Executed, g.Executed))
	add("intent states", !reflect.DeepEqual(f.IntentStates, g.IntentStates))
	if len(changed) == 0 {
		return ""
	}
	return "changed fields: " + strings.Join(changed, ", ")
}

func mustBeUnchanged(m *model, before fingerprint, assertion string) error {
	if d := before.diff(fingerprintOf(m)); d != "" {
		return failed(assertion,
			"an event the vocabulary refuses changed the runtime (%s); an invalid event must change no state", d)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. The authenticated completion arrives BEFORE the output bytes it describes.
//
// ADR-0024 decision 7 records that SSH orders the two channels independently,
// so this is ordinary and not the error case. Paired with
// ruleSightingAuthorisesNothing.
// ---------------------------------------------------------------------------

func scheduleFenceAuthenticatedFirst(m *model) error {
	observe(kindRendezvous, int(m.Rendezvous().State)) // idle: nothing is in flight yet

	inc := m.Incarnation()
	nonce := nonceOf(0x11)
	source := []byte("$ \x1b]133;D;0\x07")

	m.Completed(inc, nonce, 0)
	if got := m.Rendezvous().State; got != RendezvousAwaitingSighting {
		return failed("fence/authenticated-first-parks",
			"the authenticated half left the rendezvous %s, want awaiting-sighting and therefore not complete", rendezvousName(got))
	}
	observe(kindRendezvous, int(RendezvousAwaitingSighting))

	// A sighting carrying a different nonce is a different event.
	if err := m.SightFence(nonceOf(0x22), source); !errors.Is(err, ErrNonceMismatch) {
		return failed("fence/foreign-nonce-refused",
			"a sighting carrying a nonce the completion did not carry returned %v, want %v", err, ErrNonceMismatch)
	}
	if got := m.Rendezvous().State; got != RendezvousAwaitingSighting {
		return failed("fence/refused-sighting-leaves-the-state-alone",
			"a refused sighting moved the rendezvous to %s", rendezvousName(got))
	}

	// The matching sighting is the half that was missing.
	if err := m.SightFence(nonce, source); err != nil {
		return failed("fence/matching-sighting-completes", "sighting the awaited nonce: %v", err)
	}
	if got := m.Rendezvous().State; got != RendezvousComplete {
		return failed("fence/authenticated-first-completes",
			"both halves arrived and the rendezvous is %s, want complete", rendezvousName(got))
	}
	if got := m.Rendezvous().PinnedSource; !bytes.Equal(got, source) {
		return failed("fence/completed-rendezvous-pins-the-content", "the rendezvous pinned %q, want %q", got, source)
	}
	observe(kindRendezvous, int(RendezvousComplete))

	// Then the rule this schedule is paired with, which is only visible in the
	// OTHER branch of a sighting: a fence sighted with no authenticated event
	// waiting for it authorises nothing. A marker the program printed itself
	// must not be able to close a block or choose a capture endpoint
	// (ADR-0024 decision 1), so a completed rendezvous does not turn a later,
	// foreign fence into a second close — the sighting parks instead.
	if err := m.SightFence(nonceOf(0x33), []byte("a fence nobody authenticated")); err != nil {
		return failed("fence/unmatched-sighting-parks", "sighting with nothing authenticated in flight: %v", err)
	}
	if got := m.Rendezvous().State; got != RendezvousAwaitingAuthenticated {
		return failed("fence/sighting-authorises-nothing",
			"a sighted fence with no authenticated event waiting for it left the rendezvous %s, want awaiting-authenticated", rendezvousName(got))
	}
	observe(kindRendezvous, int(m.Rendezvous().State))
	return nil
}

// ---------------------------------------------------------------------------
// 2. The emulator sees the fence FIRST. Paired with
// ruleSightingAuthorisesNothing.
// ---------------------------------------------------------------------------

func scheduleFenceSightedFirst(m *model) error {
	nonce := nonceOf(0x44)
	source := []byte("$ \x1b]133;D;0\x07")

	if err := m.SightFence(nonce, source); err != nil {
		return failed("fence/sighted-first-parks", "sighting the fence before its authenticated event: %v", err)
	}
	if got := m.Rendezvous().State; got != RendezvousAwaitingAuthenticated {
		return failed("fence/sighting-authorises-nothing",
			"a fence sighted first left the rendezvous %s, want awaiting-authenticated", rendezvousName(got))
	}
	observe(kindRendezvous, int(RendezvousAwaitingAuthenticated))

	// It authorises nothing: a completion carrying another nonce is another
	// event, and must not close what the sighting parked.
	m.Completed(m.Incarnation(), nonceOf(0x55), 0)
	if got := m.Rendezvous().State; got != RendezvousAwaitingAuthenticated {
		return failed("fence/parked-sighting-closes-nothing",
			"a completion with a foreign nonce closed the parked rendezvous: %s", rendezvousName(got))
	}

	m.Completed(m.Incarnation(), nonce, 0)
	if got := m.Rendezvous().State; got != RendezvousComplete {
		return failed("fence/sighted-first-completes",
			"the matching completion left the rendezvous %s, want complete", rendezvousName(got))
	}
	observe(kindRendezvous, int(RendezvousComplete))
	return nil
}

// ---------------------------------------------------------------------------
// 3. The second half of the sighted-first order, and the part it exists for:
// the screen moves underneath a sighting while the authenticated half is still
// in flight. Paired with rulePinSource.
// ---------------------------------------------------------------------------

func scheduleFenceSightedFirstSurvivesScreenTrim(m *model) error {
	nonce := nonceOf(0x66)
	source := []byte("the row the fence was drawn over")

	if err := m.SightFence(nonce, source); err != nil {
		return failed("fence/trim-sighting-parks", "sighting the fence before its authenticated event: %v", err)
	}
	if got := m.Rendezvous().PinnedSource; !bytes.Equal(got, source) {
		return failed("fence/sighted-first-pins-source",
			"the sighting pinned %q, want the content it was seen over, %q", got, source)
	}
	observe(kindRendezvous, int(RendezvousAwaitingAuthenticated))

	// Output keeps flowing while the authenticated half is in flight, and the
	// model's screen keeps only its last 64 bytes: the rows the fence was drawn
	// over are gone by the time it arrives. A row number would have been
	// destroyed here, which is why a rendezvous pins CONTENT — the capture
	// source has to outlive the interval it was taken in.
	if err := m.Ingest(bytes.Repeat([]byte("x"), 96)); err != nil {
		return failed("fence/trim-ingest", "output after the sighting: %v", err)
	}
	if bytes.Contains(m.screen, source) {
		return failed("fence/trim-must-actually-happen",
			"the screen still holds the sighted content, so this schedule would prove nothing")
	}
	if got := m.Rendezvous().PinnedSource; !bytes.Equal(got, source) {
		return failed("fence/pin-outlives-the-trim",
			"the pin is %q after the screen trimmed past it, want %q", got, source)
	}

	m.Completed(m.Incarnation(), nonce, 0)
	if got := m.Rendezvous().State; got != RendezvousComplete {
		return failed("fence/pinned-sighting-completes",
			"the matching completion left the rendezvous %s, want complete", rendezvousName(got))
	}
	observe(kindRendezvous, int(RendezvousComplete))
	return nil
}

// ---------------------------------------------------------------------------
// 4. An explicit takeover with input queued under the authority being replaced.
// Paired with ruleCancelOnRevoke.
// ---------------------------------------------------------------------------

func scheduleTakeoverWithInputQueued(m *model) error {
	ctrl, err := grant(m, agent())
	if err != nil {
		return err
	}
	observe(kindPrincipal, int(ctrl.Holder.Kind))

	// Three intents under the agent's epoch: one reaches the PTY, two are still
	// admitted when authority changes hands.
	now, err := admitKey(m, ctrl, []byte("l"))
	if err != nil {
		return err
	}
	queuedA, err := admitKey(m, ctrl, []byte("s"))
	if err != nil {
		return err
	}
	queuedB, err := admitKey(m, ctrl, []byte("Up"))
	if err != nil {
		return err
	}
	observe(kindIntent, int(IntentStateAdmitted))

	if id, state, execErr := m.Execute(); execErr != nil || id != now || state != IntentStateExecuted {
		return failed("takeover/one-intent-executes",
			"executing the first admitted intent: id=%d state=%s err=%v", id, intentStateName(state), execErr)
	}
	observe(kindIntent, int(IntentStateExecuted))

	// The explicit takeover. GrantControl is the only way authority moves, and
	// it is a transition the previous holder's queued input does not survive.
	next, err := grant(m, person())
	if err != nil {
		return err
	}
	observe(kindPrincipal, int(next.Holder.Kind))

	for _, id := range []IntentID{queuedA, queuedB} {
		if got := m.IntentState(id); got != IntentStateCancelled {
			return failed("takeover/cancels-admitted-input",
				"an intent the agent had already admitted is %s after the handover, want cancelled", intentStateName(got))
		}
	}
	observe(kindIntent, int(IntentStateCancelled))

	// The other half, and the reason the cancellation is not a purge: what
	// already reached the PTY cannot be recalled, so no revocation may report an
	// executed intent as cancelled.
	if got := m.IntentState(now); got != IntentStateExecuted {
		return failed("takeover/executed-survives-the-handover",
			"the handover reported an executed intent as %s; bytes on a PTY cannot be recalled", intentStateName(got))
	}
	if id, state, err := m.Execute(); !errors.Is(err, ErrNothingAdmitted) {
		return failed("takeover/queue-is-empty",
			"Execute after the handover returned id=%d state=%s err=%v, want %v",
			id, intentStateName(state), err, ErrNothingAdmitted)
	}
	if len(m.executed) != 1 {
		return failed("takeover/nothing-cancelled-reached-the-pty",
			"%d intents reached the PTY, want only the one executed before the handover", len(m.executed))
	}
	return nil
}

// ---------------------------------------------------------------------------
// 5. ruleExecutedIsIrreversible, and why it has no schedule of its own.
//
// The rule guards an intent that has EXECUTED from being re-reported as
// cancelled by cancelSuperseded. Through the model's own transitions that guard
// is unreachable and its removal is inert: Execute takes an intent off the
// admitted order, and cancelSuperseded only ever walks what is still on it, so
// no admitted intent is ever also executed. Driving it would mean injecting an
// id back onto that order after its bytes had left — a state no implementation
// satisfying Runtime can be in — and a schedule that needed it would be
// evidence about the model's internals rather than about the contract, and
// could not run against the real runtime at all.
//
// So the rule is exercised where it is observable: section 4 asserts the
// CONSEQUENCE — an executed intent survives the handover and is never reported
// as cancelled, which is what a revocation acting on a stale queue would break.
// The inertness of the guard itself is reported to the coordinator, because the
// fix, if the guard is to be tested, is a model transition that the Runtime
// interface also supports — not a state a schedule can inject.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 6. The OBSERVER goes away with input admitted. Paired with
// ruleObserverLossIsNotControlLoss.
// ---------------------------------------------------------------------------

func scheduleDisconnectAfterAdmission(m *model) error {
	ctrl, err := grant(m, person())
	if err != nil {
		return err
	}
	observe(kindPrincipal, int(ctrl.Holder.Kind))

	id, err := admitKey(m, ctrl, []byte("h"))
	if err != nil {
		return err
	}
	observe(kindIntent, int(IntentStateAdmitted))

	// A watcher coming and going is not a transition of the terminal: the
	// person already committed to this keystroke, and throwing it away would
	// discard a decision nobody withdrew.
	m.observerLost()
	if got := m.IntentState(id); got != IntentStateAdmitted {
		return failed("observer-loss/keeps-admitted-input",
			"an observer disconnect left an admitted intent %s; losing a watcher is not losing control", intentStateName(got))
	}
	if _, state, execErr := m.Execute(); execErr != nil || state != IntentStateExecuted {
		return failed("observer-loss/input-still-executes",
			"executing an intent admitted before the observer left: state=%s err=%v", intentStateName(state), execErr)
	}
	observe(kindIntent, int(IntentStateExecuted))

	// The contrasting case, the same shape and the opposite answer: losing
	// CONTROL does cancel admitted input, because the authority it was admitted
	// under is gone.
	later, err := admitKey(m, ctrl, []byte("j"))
	if err != nil {
		return err
	}
	if _, err := m.RevokeControl(); err != nil {
		return failed("observer-loss/revocation-refused", "revoking control: %v", err)
	}
	if got := m.IntentState(later); got != IntentStateCancelled {
		return failed("observer-loss/control-loss-does-cancel",
			"an admitted intent is %s after control was revoked, want cancelled", intentStateName(got))
	}
	observe(kindIntent, int(IntentStateCancelled))
	observe(kindPrincipal, int(m.Control().Holder.Kind)) // none: the epoch names a revocation
	return nil
}

// ---------------------------------------------------------------------------
// 7. Geometry committed while output is flowing, and a resize refused on one
// side. Paired with ruleAtomicGeometry.
// ---------------------------------------------------------------------------

func scheduleResizeDuringOutput(m *model) error {
	if err := m.Ingest([]byte("$ make test\r\n")); err != nil {
		return failed("geometry/output-flowing", "ingesting output before the first commit: %v", err)
	}
	first, err := m.CommitGeometry(Geometry{Cols: 100, Rows: 30})
	if err != nil {
		return failed("geometry/first-commit", "committing 100x30: %v", err)
	}
	observe(kindAvailability, int(m.Availability()))

	if ingestErr := m.Ingest([]byte("ok\r\n")); ingestErr != nil {
		return failed("geometry/output-between-commits", "ingesting output after the first commit: %v", ingestErr)
	}
	if got := m.Geometry(); got != first {
		return failed("geometry/ingest-moves-nothing",
			"output left the commit in force at %+v, want the commit the session is running at, %+v", got, first)
	}

	second, err := m.CommitGeometry(Geometry{Cols: 120, Rows: 40})
	if err != nil {
		return failed("geometry/second-commit", "committing 120x40: %v", err)
	}
	if second.Revision <= first.Revision {
		return failed("geometry/revision-rises",
			"the second commit is at revision %d, which is not after the first, %d", second.Revision, first.Revision)
	}
	if got := m.Geometry(); got != second {
		return failed("geometry/one-commit-in-force",
			"the commit in force is %+v, want the one just made, %+v", got, second)
	}
	if err := m.Ingest([]byte("done\r\n")); err != nil {
		return failed("geometry/output-after-the-second-commit", "ingesting output after the second commit: %v", err)
	}
	if got := m.Geometry(); got != second {
		return failed("geometry/still-exactly-one-commit", "the commit in force is %+v, want %+v", got, second)
	}

	// The failure half. A resize that fails on either side commits on NEITHER,
	// and the session goes on describing what it is still running at rather than
	// what was asked for — a size one side took and the other did not is a
	// screen and a PTY disagreeing about every cell after this column.
	wanted := Geometry{Cols: 90, Rows: 20}
	m.emulatorRefusesResize = true
	if _, err := m.CommitGeometry(wanted); err == nil {
		return failed("geometry/emulator-refusal-is-an-error",
			"the emulator refused the size and CommitGeometry reported success")
	}
	if got := m.Geometry(); got != second {
		return failed("geometry/emulator-refusal-commits-neither",
			"the commit in force is %+v after the emulator refused, want the previous commit %+v", got, second)
	}
	m.emulatorRefusesResize = false

	m.ptyRefusesResize = true
	if _, err := m.CommitGeometry(wanted); err == nil {
		return failed("geometry/pty-refusal-is-an-error", "the PTY refused the size and CommitGeometry reported success")
	}
	if got := m.Geometry(); got != second {
		return failed("geometry/pty-refusal-commits-neither",
			"the commit in force is %+v after the PTY refused, want the previous commit %+v", got, second)
	}
	m.ptyRefusesResize = false
	return nil
}

// ---------------------------------------------------------------------------
// 8. An observer resynchronising: one consistent read, then the changes after
// it. Paired with ruleSnapshotIsPassive.
// ---------------------------------------------------------------------------

func scheduleObserverResync(m *model) error {
	ctrl, err := grant(m, person())
	if err != nil {
		return err
	}
	if _, err := admitKey(m, ctrl, []byte("t")); err != nil {
		return err
	}
	if err := m.Ingest([]byte("$ git status\r\n")); err != nil {
		return failed("resync/ingest", "ingesting output before the snapshot: %v", err)
	}

	before := fingerprintOf(m)
	snap := m.Snapshot()

	// A read changes nothing, and the first thing it must not move is the clock
	// every other fact is relative to: two observers resynchronising otherwise
	// produce revisions neither of them can reason about.
	if m.Revision() != before.Revision {
		return failed("snapshot/read-must-not-move-the-clock",
			"taking a snapshot moved Revision from %d to %d", before.Revision, m.Revision())
	}
	if snap.Revision != m.Revision() {
		return failed("snapshot/fields-share-one-revision",
			"the snapshot names revision %d while the runtime is at %d", snap.Revision, m.Revision())
	}
	// Every field of it belongs to that one revision, which is what makes "the
	// cards through R and the live frame at R, then the changes after R"
	// expressible at all.
	if snap.At != m.Incarnation() || snap.Availability != m.Availability() || snap.Control != m.Control() ||
		snap.Geometry != m.Geometry() || snap.Rendezvous != m.Rendezvous().State ||
		snap.Completeness != m.Completeness() || !bytes.Equal(snap.Screen, m.screen) {
		return failed("snapshot/every-field-at-that-revision",
			"the snapshot's fields do not all describe revision %d", snap.Revision)
	}
	if d := before.diff(fingerprintOf(m)); d != "" {
		return failed("snapshot/read-changes-nothing", "a read changed the runtime (%s)", d)
	}
	observe(kindCompleteness, int(m.Completeness()))
	observe(kindAvailability, int(m.Availability()))
	observe(kindIntent, int(IntentStateAdmitted))

	// And then the changes after it.
	if err := m.Ingest([]byte("nothing to commit\r\n")); err != nil {
		return failed("resync/ingest-after-the-snapshot", "ingesting output after the snapshot: %v", err)
	}
	next := m.Snapshot()
	if next.Revision <= snap.Revision {
		return failed("snapshot/changes-compose-after-it",
			"the second snapshot is at revision %d, not after the first, %d", next.Revision, snap.Revision)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 9. Input is intent, not bytes: the same key is a different byte sequence
// depending on the mode the PROGRAM set. Paired with ruleEncodeAgainstModes.
// ---------------------------------------------------------------------------

func scheduleKeyEncodedAgainstModes(m *model) error {
	// The program turns application cursor keys on.
	if err := m.Ingest([]byte("\x1b[?1h")); err != nil {
		return failed("key/ingest-set-mode", "ingesting the DECCKM set: %v", err)
	}
	if !m.applicationCursorKeys {
		return failed("key/program-set-the-mode",
			"the program set application cursor keys and the runtime did not record it")
	}
	ctrl, err := grant(m, person())
	if err != nil {
		return err
	}
	up, err := admitKey(m, ctrl, []byte("Up"))
	if err != nil {
		return err
	}
	if id, state, execErr := m.Execute(); execErr != nil || id != up || state != IntentStateExecuted {
		return failed("key/execute", "executing a key intent: id=%d state=%s err=%v", id, intentStateName(state), execErr)
	}
	if got := lastExecuted(m); !bytes.Equal(got, []byte("\x1bOA")) {
		return failed("key/encoded-against-the-program-mode",
			"with application cursor keys set the key reached the PTY as %q, want %q — the client sent %q, and passing its own encoding through is the defect", got, "\x1bOA", "Up")
	}

	// The program turns them off again. The SAME intent now means a different
	// byte sequence, which a client could not have known when it sent it: a
	// frame of cells conveys nothing about DECCKM (ADR-0066, AD-1 as amended).
	if ingestErr := m.Ingest([]byte("\x1b[?1l")); ingestErr != nil {
		return failed("key/ingest-clear-mode", "ingesting the DECCKM clear: %v", ingestErr)
	}
	if m.applicationCursorKeys {
		return failed("key/program-cleared-the-mode",
			"the program cleared application cursor keys and the runtime did not record it")
	}
	up, err = admitKey(m, ctrl, []byte("Up"))
	if err != nil {
		return err
	}
	if id, state, err := m.Execute(); err != nil || id != up || state != IntentStateExecuted {
		return failed("key/execute-again", "executing the second key intent: id=%d state=%s err=%v", id, intentStateName(state), err)
	}
	if got := lastExecuted(m); !bytes.Equal(got, []byte("\x1b[A")) {
		return failed("key/encoded-against-the-program-mode",
			"with application cursor keys cleared the key reached the PTY as %q, want %q", got, "\x1b[A")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 10. The bounded wait elapsing with one half missing.
//
// No rule guards this one, so it has no removal variant: the transition IS the
// call, which is why ExpireRendezvous is a call rather than a timer.
// ---------------------------------------------------------------------------

func scheduleRendezvousExpiresUnjoined(m *model) error {
	if err := m.SightFence(nonceOf(0x77), []byte("$ \x1b]133;D;0\x07")); err != nil {
		return failed("rendezvous/expiry-sighting", "sighting the fence: %v", err)
	}

	// The authenticated half never arrives, and the bounded wait elapses.
	if err := m.ExpireRendezvous(); err != nil {
		return failed("rendezvous/expiry-is-an-outcome", "expiring a half-joined rendezvous: %v", err)
	}
	if got := m.Rendezvous().State; got != RendezvousExpired {
		return failed("rendezvous/expiry-is-named",
			"the rendezvous is %s after the bounded wait elapsed, want expired", rendezvousName(got))
	}
	observe(kindRendezvous, int(RendezvousExpired))
	if got := m.Rendezvous().PinnedSource; got != nil {
		return failed("rendezvous/expiry-releases-the-pin",
			"the expired rendezvous still pins %q; the pin lives only while the rendezvous is pending", got)
	}

	// An interval with no authenticated boundary may still be worth keeping; it
	// may not be described as the command's complete output.
	if got := m.Completeness(); got != CompletenessNoFence {
		return failed("rendezvous/expiry-forces-no-fence",
			"completeness is %s after the rendezvous expired, want no-fence", completenessName(got))
	}
	observe(kindCompleteness, int(CompletenessNoFence))
	return nil
}

// ---------------------------------------------------------------------------
// 11. The screen moves between admission and execution. Paired with
// ruleRevalidateAtExecution.
// ---------------------------------------------------------------------------

func schedulePreconditionStaleAtExecution(m *model) error {
	if err := m.Ingest([]byte("Overwrite the file? [y/N] ")); err != nil {
		return failed("precondition/read-the-screen", "ingesting the prompt: %v", err)
	}
	ctrl, err := grant(m, person())
	if err != nil {
		return err
	}

	// The caller read the screen and asks to write into it, so it carries the
	// evidence it acted on.
	id, err := m.Admit(Intent{
		At:      m.Incarnation(),
		Under:   ctrl.Epoch,
		By:      ctrl.Holder,
		Kind:    IntentKindText,
		Payload: []byte("y"),
		Precondition: &Precondition{
			ScreenRevision: m.Revision(),
			Digest:         sha256.Sum256(m.screen),
		},
	})
	if err != nil {
		return failed("precondition/admit", "admitting an intent carrying a precondition: %v", err)
	}
	observe(kindIntent, int(IntentStateAdmitted))

	// Between admission and execution the screen moves — another line arrives,
	// or the dialog redraws. ADR-0064's rule is that the identification
	// authorising a write is the one taken immediately before it, never the one
	// the caller saw.
	if ingestErr := m.Ingest([]byte("\r\nwait, checking upstream\r\n")); ingestErr != nil {
		return failed("precondition/the-screen-moves", "ingesting the change: %v", ingestErr)
	}

	before := fingerprintOf(m)
	gotID, state, err := m.Execute()
	if !errors.Is(err, ErrPreconditionStale) {
		return failed("precondition/revalidated-at-execution",
			"executing against the screen the caller read returned %v, want %v", err, ErrPreconditionStale)
	}
	if gotID != id || state != IntentStateRefused {
		return failed("precondition/refused-not-executed",
			"the refused intent is id=%d state=%s, want id=%d refused", gotID, intentStateName(state), id)
	}
	observe(kindIntent, int(IntentStateRefused))

	// Execute is the ONE event that consumes the intent it refuses — leaving it
	// admitted would execute it later against the very evidence that made it
	// stale. What must not move is the PTY and the runtime's own clock: nothing
	// was written, and no revision was minted for a write that did not happen.
	if len(m.executed) != len(before.Executed) {
		return failed("precondition/nothing-reached-the-pty",
			"%d intents had reached the PTY before the refusal and %d after", len(before.Executed), len(m.executed))
	}
	if m.Revision() != before.Revision {
		return failed("precondition/the-clock-does-not-move", "a refused write moved Revision from %d to %d", before.Revision, m.Revision())
	}
	if m.Control() != before.Control || m.Geometry() != before.Geometry || m.Availability() != before.Availability ||
		m.Completeness() != before.Completeness || !bytes.Equal(m.screen, before.Screen) {
		return failed("precondition/only-the-intent-moved",
			"a refused write moved something other than the intent it refused")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 12. The runtime fails. Paired with ruleFailRevokes.
// ---------------------------------------------------------------------------

func scheduleRuntimeFailure(m *model) error {
	ctrl, err := grant(m, agent())
	if err != nil {
		return err
	}
	first, err := admitKey(m, ctrl, []byte("l"))
	if err != nil {
		return err
	}
	second, err := admitKey(m, ctrl, []byte("s"))
	if err != nil {
		return err
	}
	observe(kindIntent, int(IntentStateAdmitted))

	if err := m.Fail("the PTY is gone"); err != nil {
		return failed("failure/is-reported", "failing an available runtime: %v", err)
	}
	if got := m.Availability(); got != AvailabilityUnavailable {
		return failed("failure/becomes-unavailable",
			"availability is %s after the runtime failed, want unavailable", availabilityName(got))
	}
	observe(kindAvailability, int(AvailabilityUnavailable))

	// Writes are revoked: the authority that admitted them is over, and an
	// intent that will never run must not be reported as if it might.
	for _, id := range []IntentID{first, second} {
		if got := m.IntentState(id); got != IntentStateCancelled {
			return failed("failure/cancels-admitted-input",
				"an admitted intent is %s after the runtime failed, want cancelled", intentStateName(got))
		}
	}
	observe(kindIntent, int(IntentStateCancelled))
	if got := m.Control(); got.Holder.Kind != PrincipalNone {
		return failed("failure/revokes-control", "control is still held by %+v after the runtime failed", got.Holder)
	}
	observe(kindPrincipal, int(PrincipalNone))

	// Every later call is refused rather than silently doing nothing, and the
	// incarnation is over: no later evidence about it is accepted.
	if _, err := m.Admit(Intent{At: m.Incarnation(), Under: 1, By: person(), Kind: IntentKindKey, Payload: []byte("l")}); !errors.Is(err, ErrUnavailable) {
		return failed("failure/admit-refused", "Admit on a failed runtime returned %v, want %v", err, ErrUnavailable)
	}
	if _, _, err := m.Execute(); !errors.Is(err, ErrUnavailable) {
		return failed("failure/execute-refused", "Execute on a failed runtime returned %v, want %v", err, ErrUnavailable)
	}
	if _, err := m.CommitGeometry(Geometry{Cols: 80, Rows: 24}); !errors.Is(err, ErrUnavailable) {
		return failed("failure/commit-geometry-refused", "CommitGeometry on a failed runtime returned %v, want %v", err, ErrUnavailable)
	}
	if err := m.Ingest([]byte("x")); !errors.Is(err, ErrUnavailable) {
		return failed("failure/ingest-refused", "Ingest on a failed runtime returned %v, want %v", err, ErrUnavailable)
	}
	if _, err := m.GrantControl(person()); !errors.Is(err, ErrUnavailable) {
		return failed("failure/grant-control-refused", "GrantControl on a failed runtime returned %v, want %v", err, ErrUnavailable)
	}

	// Transparent recovery is deliberately NOT promised here: the session is
	// unavailable and writes are revoked until a new runtime starts a new
	// incarnation, rather than a surviving process being adopted under an
	// invented terminal state (nocx-ygxjv.3).
	return nil
}

// ---------------------------------------------------------------------------
// The pairs. Every schedule above runs once with every rule on, where it must
// pass, and once with exactly one rule removed, where a NAMED assertion must
// fail. The second run is the acceptance criterion: a rule that cannot be
// removed to make a named assertion fail is not being tested by the schedule
// paired with it.
// ---------------------------------------------------------------------------

// assertionFailed is the second half of every pair: the schedule must have
// failed through a named assertion, and it must be the one the rule's removal
// is supposed to break.
func assertionFailed(t *testing.T, err error, want string) {
	t.Helper()
	var ae *assertionError
	if !errors.As(err, &ae) {
		t.Fatalf("the schedule failed with %v, which is not a named assertion", err)
	}
	if ae.Assertion != want {
		t.Fatalf("expected assertion %q to fail, got %q: %v", want, ae.Assertion, err)
	}
}

func TestSchedule_FenceAuthenticatedFirst(t *testing.T) {
	if err := scheduleFenceAuthenticatedFirst(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_FenceAuthenticatedFirst_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleFenceAuthenticatedFirst(newModel(without(ruleSightingAuthorisesNothing)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleSightingAuthorisesNothing])
	}
	assertionFailed(t, err, "fence/sighting-authorises-nothing")
}

func TestSchedule_FenceSightedFirst(t *testing.T) {
	if err := scheduleFenceSightedFirst(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_FenceSightedFirst_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleFenceSightedFirst(newModel(without(ruleSightingAuthorisesNothing)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleSightingAuthorisesNothing])
	}
	assertionFailed(t, err, "fence/sighting-authorises-nothing")
}

func TestSchedule_FenceSightedFirstSurvivesScreenTrim(t *testing.T) {
	if err := scheduleFenceSightedFirstSurvivesScreenTrim(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_FenceSightedFirstSurvivesScreenTrim_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleFenceSightedFirstSurvivesScreenTrim(newModel(without(rulePinSource)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[rulePinSource])
	}
	assertionFailed(t, err, "fence/sighted-first-pins-source")
}

func TestSchedule_TakeoverWithInputQueued(t *testing.T) {
	if err := scheduleTakeoverWithInputQueued(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_TakeoverWithInputQueued_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleTakeoverWithInputQueued(newModel(without(ruleCancelOnRevoke)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleCancelOnRevoke])
	}
	assertionFailed(t, err, "takeover/cancels-admitted-input")
}

func TestSchedule_DisconnectAfterAdmission(t *testing.T) {
	if err := scheduleDisconnectAfterAdmission(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_DisconnectAfterAdmission_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleDisconnectAfterAdmission(newModel(without(ruleObserverLossIsNotControlLoss)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleObserverLossIsNotControlLoss])
	}
	assertionFailed(t, err, "observer-loss/keeps-admitted-input")
}

func TestSchedule_ResizeDuringOutput(t *testing.T) {
	if err := scheduleResizeDuringOutput(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_ResizeDuringOutput_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleResizeDuringOutput(newModel(without(ruleAtomicGeometry)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleAtomicGeometry])
	}
	assertionFailed(t, err, "geometry/emulator-refusal-commits-neither")
}

func TestSchedule_ObserverResync(t *testing.T) {
	if err := scheduleObserverResync(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_ObserverResync_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleObserverResync(newModel(without(ruleSnapshotIsPassive)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleSnapshotIsPassive])
	}
	assertionFailed(t, err, "snapshot/read-must-not-move-the-clock")
}

func TestKeyIsEncodedAgainstTheModeTheProgramSet(t *testing.T) {
	if err := scheduleKeyEncodedAgainstModes(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestKeyIsEncodedAgainstTheModeTheProgramSet_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleKeyEncodedAgainstModes(newModel(without(ruleEncodeAgainstModes)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleEncodeAgainstModes])
	}
	assertionFailed(t, err, "key/encoded-against-the-program-mode")
}

func TestSchedule_RendezvousExpiresUnjoined(t *testing.T) {
	if err := scheduleRendezvousExpiresUnjoined(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_PreconditionStaleAtExecution(t *testing.T) {
	if err := schedulePreconditionStaleAtExecution(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_PreconditionStaleAtExecution_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := schedulePreconditionStaleAtExecution(newModel(without(ruleRevalidateAtExecution)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleRevalidateAtExecution])
	}
	assertionFailed(t, err, "precondition/revalidated-at-execution")
}

// ---------------------------------------------------------------------------
// The runtime's failure, which is a transition like any other.
// ---------------------------------------------------------------------------

func TestRuntimeFailureRevokesAndDoesNotAdopt(t *testing.T) {
	if err := scheduleRuntimeFailure(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the failure schedule must hold: %v", err)
	}
}

func TestRuntimeFailureRevokesAndDoesNotAdopt_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleRuntimeFailure(newModel(without(ruleFailRevokes)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleFailRevokes])
	}
	assertionFailed(t, err, "failure/cancels-admitted-input")
}

// ---------------------------------------------------------------------------
// The errors the vocabulary declares, each produced and each shown to change
// nothing. An event the runtime refuses is a transition that does not happen,
// and that is exactly why it can be asserted: if a refusal mutated state on its
// way out, nothing a caller read could be reasoned about.
//
// One error is deliberately NOT here. ErrPreconditionStale is produced by
// Execute, which must consume the intent it refuses — leaving it admitted would
// execute it later against the same stale evidence — so its schedule asserts
// the narrower property instead: nothing reached the PTY, and the clock, the
// screen, control, geometry, availability and completeness did not move.
// ---------------------------------------------------------------------------

type invalidEvent struct {
	name string
	// drive brings the runtime to the state the event is invalid in, performs
	// the event, and asserts both the refusal and that nothing changed.
	drive func(m *model) error
}

func invalidEvents() []invalidEvent {
	return []invalidEvent{
		{
			name: "Admit carrying evidence about another incarnation",
			drive: func(m *model) error {
				ctrl, err := grant(m, person())
				if err != nil {
					return err
				}
				before := fingerprintOf(m)
				stale := Incarnation{Session: m.Incarnation().Session, Generation: m.Incarnation().Generation + 1}
				_, err = m.Admit(Intent{At: stale, Under: ctrl.Epoch, By: ctrl.Holder, Kind: IntentKindKey, Payload: []byte("l")})
				if !errors.Is(err, ErrStaleIncarnation) {
					return failed("invalid/stale-incarnation-refused",
						"Admit naming incarnation %+v returned %v, want %v", stale, err, ErrStaleIncarnation)
				}
				return mustBeUnchanged(m, before, "invalid/stale-incarnation-changes-nothing")
			},
		},
		{
			name: "Admit carrying a superseded control epoch",
			drive: func(m *model) error {
				// The agent holds control, then the person takes over: the epoch
				// the agent's client computed is now superseded, and an intent
				// still carrying it is refused rather than applied late.
				superseded, err := grant(m, agent())
				if err != nil {
					return err
				}
				if _, grantErr := grant(m, person()); grantErr != nil {
					return grantErr
				}
				before := fingerprintOf(m)
				_, err = m.Admit(Intent{At: m.Incarnation(), Under: superseded.Epoch, By: agent(), Kind: IntentKindKey, Payload: []byte("l")})
				if !errors.Is(err, ErrStaleControlEpoch) {
					return failed("invalid/stale-control-epoch-refused",
						"Admit under epoch %d, where %d is current, returned %v, want %v",
						superseded.Epoch, m.Control().Epoch, err, ErrStaleControlEpoch)
				}
				return mustBeUnchanged(m, before, "invalid/stale-control-epoch-changes-nothing")
			},
		},
		{
			name: "Admit with nobody holding control",
			drive: func(m *model) error {
				before := fingerprintOf(m)
				_, err := m.Admit(Intent{At: m.Incarnation(), Under: 1, By: person(), Kind: IntentKindKey, Payload: []byte("l")})
				if !errors.Is(err, ErrNoController) {
					return failed("invalid/no-controller-refused",
						"Admit with no holder returned %v, want %v", err, ErrNoController)
				}
				return mustBeUnchanged(m, before, "invalid/no-controller-changes-nothing")
			},
		},
		{
			name: "Execute while completeness is unknown",
			drive: func(m *model) error {
				ctrl, err := grant(m, person())
				if err != nil {
					return err
				}
				if _, admitErr := admitKey(m, ctrl, []byte("l")); admitErr != nil {
					return admitErr
				}
				// While the runtime cannot say whether it holds the whole stream
				// it executes nothing, and this is the state a real runtime
				// STARTS in: establishNothing is the producer the model would
				// have had (the real one is nocx-ygxjv.2's).
				m.establishNothing()
				observe(kindCompleteness, int(CompletenessUnknown))
				before := fingerprintOf(m)
				id, state, err := m.Execute()
				if !errors.Is(err, ErrCompletenessUnknown) {
					return failed("invalid/unknown-completeness-refuses-the-write",
						"Execute returned id=%d state=%s err=%v, want %v", id, intentStateName(state), err, ErrCompletenessUnknown)
				}
				return mustBeUnchanged(m, before, "invalid/unknown-completeness-changes-nothing")
			},
		},
		{
			name: "ReportGeometry with a size no terminal can run at",
			drive: func(m *model) error {
				before := fingerprintOf(m)
				if err := m.ReportGeometry(Geometry{}); !errors.Is(err, ErrGeometryInvalid) {
					return failed("invalid/reported-geometry-refused",
						"ReportGeometry of a zero size returned %v, want %v", err, ErrGeometryInvalid)
				}
				return mustBeUnchanged(m, before, "invalid/reported-geometry-changes-nothing")
			},
		},
		{
			name: "CommitGeometry with a size no terminal can run at",
			drive: func(m *model) error {
				before := fingerprintOf(m)
				if _, err := m.CommitGeometry(Geometry{}); !errors.Is(err, ErrGeometryInvalid) {
					return failed("invalid/committed-geometry-refused",
						"CommitGeometry of a zero size returned %v, want %v", err, ErrGeometryInvalid)
				}
				return mustBeUnchanged(m, before, "invalid/committed-geometry-changes-nothing")
			},
		},
		{
			name: "ExpireRendezvous with nothing in flight",
			drive: func(m *model) error {
				before := fingerprintOf(m)
				if err := m.ExpireRendezvous(); !errors.Is(err, ErrNoRendezvous) {
					return failed("invalid/expiry-without-a-rendezvous-refused",
						"ExpireRendezvous with an idle rendezvous returned %v, want %v", err, ErrNoRendezvous)
				}
				return mustBeUnchanged(m, before, "invalid/expiry-without-a-rendezvous-changes-nothing")
			},
		},
	}
}

func TestInvalidEventsChangeNothing(t *testing.T) {
	for _, ev := range invalidEvents() {
		t.Run(ev.name, func(t *testing.T) {
			if err := ev.drive(newModel(allRules())); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}
}

// The write gate is the one rule with no schedule: it is a refusal, and the
// table above drives it. Removing the rule must let the write through — if it
// did not, the refusal would be coming from somewhere else and the rule would
// be decoration.
func TestUnknownCompletenessRefusalFailsWhenItsRuleIsRemoved(t *testing.T) {
	m := newModel(without(ruleUnknownCompletenessRefusesWrites))
	ctrl, err := grant(m, person())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admitKey(m, ctrl, []byte("l")); err != nil {
		t.Fatal(err)
	}
	m.establishNothing()
	if _, state, err := m.Execute(); err != nil || state != IntentStateExecuted {
		t.Fatalf("with rule %q removed the write must go through: state=%s err=%v",
			ruleNames[ruleUnknownCompletenessRefusesWrites], intentStateName(state), err)
	}
}

// ---------------------------------------------------------------------------
// The completeness transition the schedules above do not exercise: output lost
// before the emulator saw it is a DIFFERENT honest answer from an expired
// fence, and an emulator fed only the surviving suffix is not authoritative.
// ---------------------------------------------------------------------------

func driveReportHole(m *model) error {
	before := fingerprintOf(m)
	if err := m.ReportHole(0); err != nil {
		return failed("hole/zero-is-not-a-hole", "ReportHole(0) returned %v", err)
	}
	if got := m.Completeness(); got != CompletenessComplete {
		return failed("hole/zero-is-not-a-hole",
			"a hole of zero bytes left completeness %s, want the complete it was", completenessName(got))
	}
	if d := before.diff(fingerprintOf(m)); d != "" {
		return failed("hole/zero-changes-nothing", "reporting a hole of zero bytes changed the runtime (%s)", d)
	}

	if err := m.ReportHole(3); err != nil {
		return failed("hole/is-reported", "ReportHole(3) returned %v", err)
	}
	if got := m.Completeness(); got != CompletenessLostIngest {
		return failed("hole/is-named-lost-ingest",
			"completeness is %s after a hole was reported, want lost-ingest", completenessName(got))
	}
	observe(kindCompleteness, int(CompletenessLostIngest))
	return nil
}

func TestReportHoleNamesTheLoss(t *testing.T) {
	if err := driveReportHole(newModel(allRules())); err != nil {
		t.Fatalf("reporting a hole: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Every state, reached or named.
//
// A vocabulary member with no producer is either a gap somebody must fill or a
// state nobody can be in; leaving that ambiguous is what this test refuses.
// Each is either observed by a schedule in this file or named here with the
// reason it is not — and the naming is checked in both directions, because a
// state called unreachable after a schedule reached it is as wrong as the
// reverse.
//
// The constants are iota-sequential, so the walk also checks it has no gaps:
// a constant added to contract.go in the middle of one of these enumerations
// fails here rather than being silently absent from the walk.
// ---------------------------------------------------------------------------

type stateWalk struct {
	name  string
	kind  string
	value int
	// unreachable is the reason, in one line, that nothing in this file reaches
	// the member. Empty means a schedule must reach it.
	unreachable string
}

func intentWalk(s IntentState, unreachable string) stateWalk {
	return stateWalk{name: "intent state " + intentStateName(s), kind: kindIntent, value: int(s), unreachable: unreachable}
}

func rendezvousWalk(s RendezvousState, unreachable string) stateWalk {
	return stateWalk{name: "rendezvous " + rendezvousName(s), kind: kindRendezvous, value: int(s), unreachable: unreachable}
}

func completenessWalk(c Completeness, unreachable string) stateWalk {
	return stateWalk{name: "completeness " + completenessName(c), kind: kindCompleteness, value: int(c), unreachable: unreachable}
}

func availabilityWalk(a Availability, unreachable string) stateWalk {
	return stateWalk{name: "availability " + availabilityName(a), kind: kindAvailability, value: int(a), unreachable: unreachable}
}

func principalWalk(k PrincipalKind, unreachable string) stateWalk {
	return stateWalk{name: "principal " + principalName(k), kind: kindPrincipal, value: int(k), unreachable: unreachable}
}

func TestEveryStateIsReachableOrNamedUnreachable(t *testing.T) {
	// The schedules are the evidence, so this test drives them itself rather
	// than trusting whatever ran before it.
	observedStates = map[string]bool{}

	for _, s := range []struct {
		name  string
		sched func(*model) error
	}{
		{"FenceAuthenticatedFirst", scheduleFenceAuthenticatedFirst},
		{"FenceSightedFirst", scheduleFenceSightedFirst},
		{"FenceSightedFirstSurvivesScreenTrim", scheduleFenceSightedFirstSurvivesScreenTrim},
		{"TakeoverWithInputQueued", scheduleTakeoverWithInputQueued},
		{"DisconnectAfterAdmission", scheduleDisconnectAfterAdmission},
		{"ResizeDuringOutput", scheduleResizeDuringOutput},
		{"ObserverResync", scheduleObserverResync},
		{"KeyEncodedAgainstModes", scheduleKeyEncodedAgainstModes},
		{"RendezvousExpiresUnjoined", scheduleRendezvousExpiresUnjoined},
		{"PreconditionStaleAtExecution", schedulePreconditionStaleAtExecution},
		{"RuntimeFailure", scheduleRuntimeFailure},
	} {
		if err := s.sched(newModel(allRules())); err != nil {
			t.Fatalf("schedule %s must pass with every rule on, or the states it reaches are not evidence: %v", s.name, err)
		}
	}
	for _, ev := range invalidEvents() {
		if err := ev.drive(newModel(allRules())); err != nil {
			t.Fatalf("invalid event %q must be refused without changing anything, or the states it passes through are not evidence: %v", ev.name, err)
		}
	}
	if err := driveReportHole(newModel(allRules())); err != nil {
		t.Fatalf("reporting a hole, or the states it passes through are not evidence: %v", err)
	}

	walk := []stateWalk{
		intentWalk(IntentStateNone, "the zero value: IntentState reports it for an id nothing was admitted under, so it names the ABSENCE of an intent rather than a state an intent occupies"),
		intentWalk(IntentStateAdmitted, ""),
		intentWalk(IntentStateExecuted, ""),
		intentWalk(IntentStateCancelled, ""),
		intentWalk(IntentStateRefused, ""),
		intentWalk(IntentStateFailed, "no producer yet: Execute either writes (executed) or refuses before writing (refused, cancelled), and a real runtime reaches this only when proc.Write itself fails part-way — nocx-ygxjv.2"),

		rendezvousWalk(RendezvousIdle, ""),
		rendezvousWalk(RendezvousAwaitingSighting, ""),
		rendezvousWalk(RendezvousAwaitingAuthenticated, ""),
		rendezvousWalk(RendezvousComplete, ""),
		rendezvousWalk(RendezvousExpired, ""),

		completenessWalk(CompletenessUnknown, ""),
		completenessWalk(CompletenessComplete, ""),
		completenessWalk(CompletenessLostIngest, ""),
		completenessWalk(CompletenessNoFence, ""),
		completenessWalk(CompletenessEvicted, "deliberately keeps less than the whole, which is retention's decision and not the runtime's — it arrives with the content store's retention policy, owned by nocx-2v80t.2"),

		availabilityWalk(AvailabilityUnknown, "the zero value: the state of a runtime that has established nothing, which no transition in this vocabulary passes through — the model is constructed available, and a constructor that established nothing is the producer, in nocx-ygxjv.2"),
		availabilityWalk(AvailabilityAvailable, ""),
		availabilityWalk(AvailabilityUnavailable, ""),

		principalWalk(PrincipalNone, ""),
		principalWalk(PrincipalPerson, ""),
		principalWalk(PrincipalAgent, ""),
	}

	next := map[string]int{}
	var unreachable []string
	for _, w := range walk {
		if w.value != next[w.kind] {
			t.Errorf("%s: the walk expects %s value %d here, which means a constant was added to contract.go and not to this walk", w.name, w.kind, next[w.kind])
		}
		next[w.kind] = w.value + 1

		switch {
		case w.unreachable == "":
			if !wasObserved(w.kind, w.value) {
				t.Errorf("%s is neither reached by a schedule nor named unreachable with a reason", w.name)
			}
		default:
			if wasObserved(w.kind, w.value) {
				t.Errorf("%s is named unreachable but a schedule reached it, so the reason is wrong", w.name)
			}
			unreachable = append(unreachable, w.name+": "+w.unreachable)
		}
	}
	t.Logf("deliberately unreachable: %s", strings.Join(unreachable, " | "))
}
