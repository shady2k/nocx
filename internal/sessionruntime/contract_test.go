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
// # What is the contract here, and what is the MODEL's own falsifiability
//
// Every schedule takes a Runtime and nothing else, and every named assertion is
// about the CONTRACT: it holds for any implementation, and a real runtime
// (nocx-ygxjv.2) is judged by the same sentences. What a schedule needs to see
// beyond the transitions it drives, it reads at the boundary the runtime was
// constructed over — the terminal it writes to, the emulator it resizes, the
// consumers it emits to — or from the records the interface answers with
// (Snapshot, Intents, ReportedGeometry, IngestState). Nothing here reads an
// implementation's private state, which is what makes the promise in doc.go
// true rather than aspirational.
//
// The RULES are the model's, and so is the PAIRING: removing one rule is how
// the model is shown to be falsifiable — a shortcut it could have taken — and a
// real runtime has no rules to switch off. So "with every rule on the schedule
// must pass" and "with exactly this rule removed THIS assertion must fail" are
// statements about the model in model_test.go; the assertion named in each one
// is a statement about the contract. A schedule paired with a rule is evidence
// that the rule has teeth, not that the contract has only one implementation.
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
func grant(m Runtime, p Principal) (Control, error) {
	c, err := m.GrantControl(p)
	if err != nil {
		return c, failed("setup/grant-control", "granting control to %+v: %v", p, err)
	}
	return c, nil
}

func admitKey(m Runtime, ctrl Control, payload []byte) (IntentID, error) {
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
// can see the bytes the runtime decided on. It is read from the terminal the
// runtime was CONSTRUCTED over and not from the runtime: a runtime that kept
// its own log of what it sent would be spending memory on an observation the
// injected terminal already holds, and the interesting assertion — "the same
// intent is different bytes depending on the mode the PROGRAM set" — is
// checkable exactly where the bytes land.
func lastExecuted(m Runtime) ([]byte, error) {
	terminal, err := terminalOf(m)
	if err != nil {
		return nil, err
	}
	written := terminal.Written()
	if len(written) == 0 {
		return nil, nil
	}
	return written[len(written)-1], nil
}

// showsText reports whether the screen the runtime serves carries this text.
//
// It is the ONE way a schedule in this file reads a screen, because a screen
// has two shapes and a schedule is judged by both. The model's is a window on
// the TEXT it ingested; a real one is the GRID the program drew — the rows it
// filled, joined by line breaks, each row's trailing blank cells dropped. What
// the two shapes agree on is what a person sees, so that is what a schedule may
// assert. "The screen ENDS with these bytes" is a third claim: true of the
// model, false of a grid for a reason that has nothing to do with the runtime
// (a row is a rectangle of cells and has no newline at its end), so a schedule
// asserting it measures the model's representation instead of the runtime's
// behaviour.
//
// It is not the weaker claim, and a caller owes it one thing: text nothing else
// in that schedule's stream contains. Showing it then can only mean the ingest
// under test put it on the screen — the drawer of a grid and the buffer of a
// model both hold what they were told to draw.
func showsText(m Runtime, text string) bool {
	return bytes.Contains(m.Snapshot().Screen, []byte(text))
}

// whatTheRuntimeDidNotHandOnIsAccountedFor is the half of both
// hostile-sequence schedules that is a statement about the runtime's OWN
// account rather than about where a pending sequence lives. It is one function
// because it is one property, owed by every runtime whichever side owns the
// parser: bytes the runtime did not hand on are HELD, within the bound the
// vocabulary states, or DROPPED with the loss counted and reported. The one
// thing a runtime may not do is neither — hold a sequence it could not have
// kept whole while counting nothing, which is a screen that looks whole served
// from bytes that were discarded (design §6.7), and it is the defect these
// schedules were written against.
//
// fed is how many bytes the caller fed as one unterminated sequence, and it owes
// more than MaxPendingSequence of them. That premise is what makes the first
// assertion say a drop rather than a sequence the runtime simply kept: with more
// fed than the bound, a runtime holding some of it and counting nothing did not
// keep the rest, and the contract's own meaning of [IngestState.Pending] — the
// trailing open sequence THIS runtime is keeping, everything before it drawn or
// completed — leaves it nowhere else for the rest to be but discarded. The
// premise is asserted rather than trusted, because a schedule that shortened its
// input would turn this into a claim about nothing.
func whatTheRuntimeDidNotHandOnIsAccountedFor(m Runtime, fed int, assertion string) error {
	if fed <= MaxPendingSequence {
		return failed("setup/the-sequence-is-longer-than-the-bound",
			"the schedule fed %d bytes of an open sequence, which is not more than MaxPendingSequence (%d): a runtime may hold that much and have dropped nothing, so there would be nothing to account for",
			fed, MaxPendingSequence)
	}

	held := len(m.IngestState().Pending)
	lost := m.IngestState().Lost

	if held > 0 && lost == 0 {
		return failed(assertion,
			"the runtime holds %d of the %d bytes of an open sequence and counts none of them discarded: what it did not hand on is neither held whole nor reported",
			held, fed)
	}
	if lost > 0 && m.Completeness() != CompletenessLostIngest {
		return failed(assertion,
			"the runtime discarded %d bytes of the stream and reports completeness %s, want lost-ingest: an attaching client must be told, not handed a screen that looks whole (design §6.7)",
			lost, completenessName(m.Completeness()))
	}
	return nil
}

// terminalOf and emulatorOf are the two instruments a schedule reads the
// boundary through: the record of what reached the program and the refusal that
// makes one side of a commit fail. Both return the CONTRACT's instrument types
// and not a fixture's — a real runtime is judged by the same schedules by being
// CONSTRUCTED over instruments of its own (each implements one of these), which
// is why the requirement is a published interface and not this package's test
// struct. A runtime whose terminal is neither is a harness that cannot be
// judged, and it says so here, once, rather than surfacing as "the key reached
// the PTY as \"\"".
func terminalOf(m Runtime) (TerminalInstrument, error) {
	t, ok := m.Terminal().(TerminalInstrument)
	if !ok {
		return nil, failed("harness/the-terminal-is-an-instrument",
			"the runtime's terminal is a %T, which does not implement TerminalInstrument (contract.go): these schedules judge a runtime constructed over an instrument, and a shipped runtime implements Terminal alone", m.Terminal())
	}
	return t, nil
}

func emulatorOf(m Runtime) (EmulatorInstrument, error) {
	e, ok := m.Emulator().(EmulatorInstrument)
	if !ok {
		return nil, failed("harness/the-emulator-is-an-instrument",
			"the runtime's emulator is a %T, which does not implement EmulatorInstrument (contract.go): these schedules judge a runtime constructed over an instrument", m.Emulator())
	}
	return e, nil
}

// newestEffect and holdsKind read what a consumer is HOLDING, which is the only
// way a duplicate policy and a delivery can be told apart: two deliveries of one
// EffectID are one effect, and two effects of one kind are two.
func newestEffect(c Consumer) (Effect, bool) {
	held := c.Effects()
	if len(held) == 0 {
		return Effect{}, false
	}
	return held[len(held)-1], true
}

func holdsKind(c Consumer, k EffectKind) bool {
	for _, e := range c.Effects() {
		if e.Kind == k {
			return true
		}
	}
	return false
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

func deliveryClassName(c DeliveryClass) string {
	switch c {
	case DeliveryUnclassified:
		return "unclassified"
	case DeliveryLossless:
		return "lossless"
	case DeliveryCoalescable:
		return "coalescable"
	case DeliveryAtMostOnce:
		return "at-most-once"
	default:
		return fmt.Sprintf("DeliveryClass(%d)", int(c))
	}
}

func effectKindName(k EffectKind) string {
	switch k {
	case EffectNone:
		return "no effect"
	case EffectBell:
		return "a bell"
	case EffectNotification:
		return "a notification request"
	case EffectClipboard:
		return "a clipboard write"
	case EffectTitle:
		return "a title"
	case EffectCwdReport:
		return "a cwd report"
	default:
		return fmt.Sprintf("EffectKind(%d)", int(k))
	}
}

// ---------------------------------------------------------------------------
// Reachability bookkeeping.
//
// The schedules below record every enumerated state they pass through, so the
// walk at the end of this file can say of each constant either that a schedule
// reaches it or, in words, why nothing does. It is package state because a
// schedule's signature is a runtime and an error and nothing else; the walk
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
	kindDelivery     = "delivery-class"
	kindEffect       = "effect-kind"
)

func observe(kind string, value int) { observedStates[fmt.Sprintf("%s/%d", kind, value)] = true }

func wasObserved(kind string, value int) bool {
	return observedStates[fmt.Sprintf("%s/%d", kind, value)]
}

// ---------------------------------------------------------------------------
// "An invalid event changes nothing" — the property that makes invalid events
// testable at all, and the reason a runtime's refusals can be asserted rather
// than hoped for. fingerprint is everything a caller can observe of a RUNTIME:
// a comparison is then a statement about the runtime rather than about the one
// field somebody remembered to check.
//
// It is built from the interface and nothing else, which is what makes it a
// statement about any implementation — the records Intents, ReportedGeometry
// and IngestState answer with, the delivery side each consumer reports, and the
// bytes the injected terminal was handed. Two things a model happens to hold
// are deliberately NOT here, because the vocabulary does not promise them to a
// caller: the id the next Admit will mint (nothing says which id it is, and a
// refusal that burned one is not a fact any caller can read) and the identity
// the next effect will carry (an effect is observable as the thing delivered,
// and an identity minted and never delivered is not).
// ---------------------------------------------------------------------------

type fingerprint struct {
	Snapshot

	Intents   []IntentRecord
	Executed  [][]byte
	Reported  Geometry
	Ingest    IngestState
	Consumers []consumerFingerprint
}

// consumerFingerprint is everything observable about one consumer's queue.
type consumerFingerprint struct {
	Session     SessionID
	Pending     int
	Held        int
	Coalesced   uint64
	EffectsLost uint64
	Stale       bool
	Effects     int
}

// fingerprintOf reads a runtime. It takes a snapshot, so it is passive only
// while ruleSnapshotIsPassive is on — and that rule is the MODEL's, so every
// use in this file is on a model built by allRules(), where it is.
func fingerprintOf(m Runtime) fingerprint {
	f := fingerprint{
		Snapshot: m.Snapshot(),
		Intents:  m.Intents(),
		Reported: m.ReportedGeometry(),
		Ingest:   m.IngestState(),
	}
	// The bytes that reached the program are the instrument's record — the same
	// place the schedules read them from — and a runtime built over a terminal
	// that is not an instrument simply contributes none of them here. The
	// schedules that ASSERT on those bytes go through terminalOf, which names
	// that as a harness failure rather than reporting it as a finding.
	if terminal, ok := m.Terminal().(TerminalInstrument); ok {
		f.Executed = terminal.Written()
	}
	for _, c := range m.Consumers().Attached() {
		f.Consumers = append(f.Consumers, consumerFingerprint{
			Session:     m.Incarnation().Session,
			Pending:     c.Pending(),
			Held:        c.HeldBytes(),
			Coalesced:   c.Coalesced(),
			EffectsLost: c.EffectsLost(),
			Stale:       c.Stale(),
			Effects:     len(c.Effects()),
		})
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
	add("intents", !slices.Equal(f.Intents, g.Intents))
	add("executed bytes", !reflect.DeepEqual(f.Executed, g.Executed))
	add("pending sequence", !bytes.Equal(f.Ingest.Pending, g.Ingest.Pending))
	add("ingest work", f.Ingest.Work != g.Ingest.Work)
	add("ingest loss", f.Ingest.Lost != g.Ingest.Lost)
	add("consumers", !reflect.DeepEqual(f.Consumers, g.Consumers))
	if len(changed) == 0 {
		return ""
	}
	return "changed fields: " + strings.Join(changed, ", ")
}

func mustBeUnchanged(m Runtime, before fingerprint, assertion string) error {
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

func scheduleFenceAuthenticatedFirst(m Runtime) error {
	observe(kindRendezvous, int(m.Rendezvous().State)) // idle: nothing is in flight yet

	inc := m.Incarnation()
	nonce := nonceOf(0x11)
	source := []byte("$ \x1b]133;D;0\x07")

	m.AuthenticatedEvents().Completed(inc, nonce, 0)
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

func scheduleFenceSightedFirst(m Runtime) error {
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
	m.AuthenticatedEvents().Completed(m.Incarnation(), nonceOf(0x55), 0)
	if got := m.Rendezvous().State; got != RendezvousAwaitingAuthenticated {
		return failed("fence/parked-sighting-closes-nothing",
			"a completion with a foreign nonce closed the parked rendezvous: %s", rendezvousName(got))
	}

	m.AuthenticatedEvents().Completed(m.Incarnation(), nonce, 0)
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

func scheduleFenceSightedFirstSurvivesScreenTrim(m Runtime) error {
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
	if bytes.Contains(m.Snapshot().Screen, source) {
		return failed("fence/trim-must-actually-happen",
			"the screen still holds the sighted content, so this schedule would prove nothing")
	}
	if got := m.Rendezvous().PinnedSource; !bytes.Equal(got, source) {
		return failed("fence/pin-outlives-the-trim",
			"the pin is %q after the screen trimmed past it, want %q", got, source)
	}

	m.AuthenticatedEvents().Completed(m.Incarnation(), nonce, 0)
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

func scheduleTakeoverWithInputQueued(m Runtime) error {
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
	terminal, terr := terminalOf(m)
	if terr != nil {
		return terr
	}
	if len(terminal.Written()) != 1 {
		return failed("takeover/nothing-cancelled-reached-the-pty",
			"%d intents reached the PTY, want only the one executed before the handover", len(terminal.Written()))
	}
	return nil
}

// ---------------------------------------------------------------------------
// 5. ruleExecutedIsIrreversible shares section 4's schedule, and why that is
// enough (nocx-ygxjv.5).
//
// The rule guards an intent that has EXECUTED from being re-reported as
// cancelled when its epoch is superseded. It was briefly INERT, and the reason
// is worth keeping because it is the trap: cancelSuperseded walked the pending
// QUEUE, an executed intent is not on it, so removing the guard changed nothing
// and the rule could not be falsified.
//
// The fix was not to inject a state — that would have been evidence about model
// internals rather than about the contract, and could not judge a real runtime.
// It was to make the model take the shape a real implementation must: the
// RECORDS outlive the queue, because IntentState(id) has to answer long after
// an intent left it, so a revocation is written against the records. That is
// the ordinary shape and therefore the ordinary mistake, and with it the guard
// is live — removing the rule cancels an executed intent and section 4's
// takeover/executed-survives-the-handover fires.
//
// So the rule is paired like the other ten; it simply shares a schedule with
// ruleCancelOnRevoke rather than having one of its own, because one arrival
// order exercises both halves of what a handover owes.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 6. The OBSERVER goes away with input admitted. Paired with
// ruleObserverLossIsNotControlLoss.
// ---------------------------------------------------------------------------

func scheduleDisconnectAfterAdmission(m Runtime) error {
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
	m.Consumers().Lost()
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

func scheduleResizeDuringOutput(m Runtime) error {
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

	// The failure half, and the half that has to be stated in terms a real
	// terminal can HONOUR. A resize is not atomic: the terminal is resized and
	// the program receives SIGWINCH, and a signal already delivered cannot be
	// recalled by anything the runtime does next. So the rule is about the
	// COMMIT rather than about the two calls — it opens on both or it does not
	// open at all, and the commit in force keeps standing — and the other half
	// of it is that no side is left at a size nobody committed, the side that
	// took the refused size being put back to the commit in force. A screen and
	// a PTY disagreeing about every cell after this column is what that half
	// exists against.
	wanted := Geometry{Cols: 90, Rows: 20}
	terminal, ptyErr := terminalOf(m)
	if ptyErr != nil {
		return ptyErr
	}
	screen, emuErr := emulatorOf(m)
	if emuErr != nil {
		return emuErr
	}

	screen.RefuseResize()
	if _, err := m.CommitGeometry(wanted); err == nil {
		return failed("geometry/emulator-refusal-is-an-error",
			"the emulator refused the size and CommitGeometry reported success")
	}
	if got := m.Geometry(); got != second {
		return failed("geometry/emulator-refusal-commits-neither",
			"the commit in force is %+v after the emulator refused, want the previous commit %+v", got, second)
	}
	if got, running := terminal.Size(), screen.Size(); got != second.Geometry || running != second.Geometry {
		return failed("geometry/emulator-refusal-leaves-no-side-at-an-uncommitted-size",
			"after the emulator refused %+v the terminal is at %+v and the emulator at %+v, want both at the commit in force %+v: the terminal had already taken the refused size, and a size nobody committed is not one either side may keep", wanted, got, running, second.Geometry)
	}
	screen.AcceptResize()

	terminal.RefuseResize()
	if _, err := m.CommitGeometry(wanted); err == nil {
		return failed("geometry/pty-refusal-is-an-error", "the PTY refused the size and CommitGeometry reported success")
	}
	if got := m.Geometry(); got != second {
		return failed("geometry/pty-refusal-commits-neither",
			"the commit in force is %+v after the PTY refused, want the previous commit %+v", got, second)
	}
	if got, running := terminal.Size(), screen.Size(); got != second.Geometry || running != second.Geometry {
		return failed("geometry/pty-refusal-leaves-no-side-at-an-uncommitted-size",
			"after the terminal refused %+v it is at %+v and the emulator at %+v, want both at the commit in force %+v", wanted, got, running, second.Geometry)
	}
	terminal.AcceptResize()
	return nil
}

// ---------------------------------------------------------------------------
// 8. An observer resynchronising: one consistent read, then the changes after
// it. Paired with ruleSnapshotIsPassive.
// ---------------------------------------------------------------------------

func scheduleObserverResync(m Runtime) error {
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
		snap.Completeness != m.Completeness() || !bytes.Equal(snap.Screen, m.Snapshot().Screen) {
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

func scheduleKeyEncodedAgainstModes(m Runtime) error {
	// The program turns application cursor keys on, and the mode is observable
	// through what it DOES and not as a flag: nothing in this contract reports
	// which modes are set, and a schedule that read one would be reading an
	// implementation's private state rather than the runtime's behaviour. What
	// the mode means here is the bytes the same intent becomes.
	if err := m.Ingest([]byte("\x1b[?1h")); err != nil {
		return failed("key/ingest-set-mode", "ingesting the DECCKM set: %v", err)
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
	got, err := lastExecuted(m)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, []byte("\x1bOA")) {
		return failed("key/encoded-against-the-program-mode",
			"with application cursor keys set the key reached the PTY as %q, want %q — the client sent %q, and passing its own encoding through is the defect", got, "\x1bOA", "Up")
	}

	// The program turns them off again, and the SAME intent now means a
	// different byte sequence, which a client could not have known when it sent
	// it: a frame of cells conveys nothing about DECCKM (ADR-0066, AD-1 as
	// amended).
	if ingestErr := m.Ingest([]byte("\x1b[?1l")); ingestErr != nil {
		return failed("key/ingest-clear-mode", "ingesting the DECCKM clear: %v", ingestErr)
	}
	up, err = admitKey(m, ctrl, []byte("Up"))
	if err != nil {
		return err
	}
	if id, state, execErr := m.Execute(); execErr != nil || id != up || state != IntentStateExecuted {
		return failed("key/execute-again", "executing the second key intent: id=%d state=%s err=%v", id, intentStateName(state), execErr)
	}
	got, err = lastExecuted(m)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, []byte("\x1b[A")) {
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

func scheduleRendezvousExpiresUnjoined(m Runtime) error {
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

func schedulePreconditionStaleAtExecution(m Runtime) error {
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
			Digest:         sha256.Sum256(m.Snapshot().Screen),
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
	terminal, terr := terminalOf(m)
	if terr != nil {
		return terr
	}
	if len(terminal.Written()) != len(before.Executed) {
		return failed("precondition/nothing-reached-the-pty",
			"%d intents had reached the PTY before the refusal and %d after", len(before.Executed), len(terminal.Written()))
	}
	if m.Revision() != before.Revision {
		return failed("precondition/the-clock-does-not-move", "a refused write moved Revision from %d to %d", before.Revision, m.Revision())
	}
	if m.Control() != before.Control || m.Geometry() != before.Geometry || m.Availability() != before.Availability ||
		m.Completeness() != before.Completeness || !bytes.Equal(m.Snapshot().Screen, before.Screen) {
		return failed("precondition/only-the-intent-moved",
			"a refused write moved something other than the intent it refused")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 12. The runtime fails. Paired with ruleFailRevokes.
// ---------------------------------------------------------------------------

func scheduleRuntimeFailure(m Runtime) error {
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
// 13. The consumer that never reads (bead nocx-ygxjv.4).
//
// A subscriber that stops reading is the ordinary case, not the hostile one: a
// laptop suspends, a tab is backgrounded, a client is mid-handshake. Three
// things must hold while it is wedged, and each is one rule below.
//
// Paired with ruleConsumerQueueIsBounded, ruleLosslessIngestSurvivesASlowConsumer
// and ruleConsumerLossIsReported — in that order, because each rule's removal
// must fail this schedule at ITS assertion rather than at an earlier one.
// ---------------------------------------------------------------------------

func scheduleConsumerThatNeverReads(m Runtime) error {
	wedged := m.Consumers().Attach()
	const ingests = 4 * MaxPendingFrames

	// Far more output than its queue can hold, and nothing here reads.
	for range ingests {
		if err := m.Ingest([]byte("a line of output arrived\r\n")); err != nil {
			return failed("delivery/ingest", "ingesting output for a consumer that never reads: %v", err)
		}
	}
	observe(kindDelivery, int(DeliveryCoalescable))
	observe(kindDelivery, int(DeliveryLossless))

	// The bound the vocabulary states, and the memory it implies: the payloads
	// held for this consumer, and the bytes those payloads are.
	if got := wedged.Pending(); got > MaxPendingFrames {
		return failed("delivery/the-queue-is-at-its-bound",
			"the runtime holds %d payloads for a consumer read none of the %d it was sent, want at most MaxPendingFrames (%d)",
			got, ingests, MaxPendingFrames)
	}
	if got := wedged.HeldBytes(); got > MaxPendingFrames*modelScreenBytes {
		return failed("delivery/the-queue-is-at-its-bound",
			"the runtime holds %d bytes for a consumer that never reads, want at most MaxPendingFrames*modelScreenBytes (%d)",
			got, MaxPendingFrames*modelScreenBytes)
	}

	// The LOSSLESS class lost nothing. The flood was coalesced FOR THE
	// CONSUMER; the runtime's own ingest saw every byte of it, and the output
	// arriving after the flood still reaches the emulator. A slow consumer is
	// never a reason to discard what the program said.
	if m.IngestState().Lost != 0 {
		return failed("delivery/a-wedged-consumer-costs-no-ingest",
			"%d bytes of output were discarded while a consumer was wedged, want none: a consumer's queue is coalescable, the stream is not", m.IngestState().Lost)
	}
	// The text is named once and read once, so that "the screen shows it" can
	// only mean this ingest drew it: nothing else in this schedule's stream
	// contains it, and [showsText] is the one reading of a screen both shapes
	// of screen can honour.
	const lastLine = "the last line"
	if err := m.Ingest([]byte(lastLine + "\r\n")); err != nil {
		return failed("delivery/ingest-after-the-flood", "ingesting output after the flood: %v", err)
	}
	if !showsText(m, lastLine) {
		return failed("delivery/a-wedged-consumer-costs-no-ingest",
			"the output ingested after the flood did not reach the screen: it is %q, which does not show %q", m.Snapshot().Screen, lastLine)
	}

	// And what the consumer lost is REPORTED to it. A client handed a stale
	// screen and told nothing paints it as current, which is the whole reason
	// the coalescable class is allowed to lose anything at all.
	if wedged.Coalesced() == 0 || !wedged.Stale() {
		return failed("delivery/what-the-consumer-lost-is-reported",
			"the consumer was sent %d payloads it never read, %d of them are counted as dropped and its staleness reads %v, want a count above zero and a consumer that knows what it holds",
			ingests, wedged.Coalesced(), wedged.Stale())
	}

	// A program can also emit EFFECTS at a rate no client keeps up with — a
	// bell in a loop — and an at-most-once payload is not a free one: the queue
	// holding it is bounded like any other. The class permits ZERO deliveries;
	// it never permits an unreported one.
	for range 4 * MaxPendingFrames {
		if err := m.Ingest([]byte("\x07")); err != nil {
			return failed("delivery/ingest-a-bell", "ingesting a bell for a wedged consumer: %v", err)
		}
	}
	if got := wedged.Pending(); got > MaxPendingFrames {
		return failed("delivery/the-queue-is-at-its-bound",
			"the runtime holds %d payloads after a flood of effects, want at most MaxPendingFrames (%d)", got, MaxPendingFrames)
	}
	if wedged.EffectsLost() == 0 {
		return failed("delivery/what-the-consumer-lost-is-reported",
			"the runtime shed effects for a consumer that never reads and counted %d of them", wedged.EffectsLost())
	}
	return nil
}

// ---------------------------------------------------------------------------
// 14. One session may not spend another session's allowance.
//
// AD-10's per-session fairness, over frames. The two models share ONE budget on
// purpose: whether the allowance is per session is otherwise a sentence in a
// comment rather than a difference a schedule can see. Paired with
// rulePerSessionAllowance.
// ---------------------------------------------------------------------------

// The fairness schedule takes its TWO runtimes, one session each, and it is the
// harness that builds them over a single budget — the arrangement the schedule
// is about, and construction rather than judgement. Whether the allowance is
// per session is otherwise a sentence in a comment; it is a difference only
// when two runtimes share one account, and a schedule may count on how it was
// wired without wiring it.
func scheduleOneSessionCannotSpendAnothersAllowance(busy, other Runtime) error {
	// The first session runs ahead of its consumer, which reads nothing.
	busyConsumer := busy.Consumers().Attach()
	for range 4 * MaxPendingFrames {
		if err := busy.Ingest([]byte("the first session is busy\r\n")); err != nil {
			return failed("setup/busy-session-ingest", "ingesting output on the busy session: %v", err)
		}
	}
	if busyConsumer.Pending() != MaxPendingFrames || busyConsumer.Coalesced() == 0 || !busyConsumer.Stale() {
		return failed("setup/the-first-session-is-wedged",
			"the busy session holds %d payloads, dropped %d and reads stale=%v; this schedule is about the OTHER session, so its setup failing is not its finding",
			busyConsumer.Pending(), busyConsumer.Coalesced(), busyConsumer.Stale())
	}

	// The second session is a different terminal with a consumer of its own,
	// reading nothing either — it is simply not the one that ran ahead.
	otherConsumer := other.Consumers().Attach()
	for range MaxPendingFrames {
		if err := other.Ingest([]byte("the other session is idle\r\n")); err != nil {
			return failed("setup/other-session-ingest", "ingesting output on the other session: %v", err)
		}
	}
	if otherConsumer.Coalesced() != 0 || otherConsumer.EffectsLost() != 0 || otherConsumer.Stale() {
		return failed("delivery/one-session-cannot-spend-anothers-allowance",
			"the other session lost %d coalescable and %d at-most-once payloads (stale=%v) because the busy session spent the allowance, want none: the allowance is per session",
			otherConsumer.Coalesced(), otherConsumer.EffectsLost(), otherConsumer.Stale())
	}
	if got := otherConsumer.Pending(); got != MaxPendingFrames {
		return failed("delivery/one-session-cannot-spend-anothers-allowance",
			"the other session holds %d payloads, want all %d of its own", got, MaxPendingFrames)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 15. At-most-once, with a duplicate policy (design §6.2).
//
// Two ways one effect is delivered twice, and the class refuses both: the
// producer offering it again, and a FULL FRAME — a snapshot, a resend, a
// re-attachment — carrying it. Paired with ruleDuplicateEffectIsSuppressed and
// ruleResendCarriesNoEffects.
// ---------------------------------------------------------------------------

func scheduleEffectDeliveryPolicy(m Runtime) error {
	c := m.Consumers().Attach()

	// The program writes to the clipboard, through OSC 52.
	if err := m.Ingest([]byte("\x1b]52;c;aGVsbG8=\x07")); err != nil {
		return failed("effect/ingest", "ingesting a clipboard write: %v", err)
	}
	if got := len(c.Effects()); got != 1 {
		return failed("effect/the-stream-produced-one-effect",
			"the consumer holds %d at-most-once payloads after one OSC 52, want one", got)
	}
	observe(kindEffect, int(EffectClipboard))
	observe(kindDelivery, int(DeliveryAtMostOnce))
	clipboard, ok := newestEffect(c)
	if !ok {
		return failed("effect/the-effect-carries-its-identity",
			"the consumer holds no effect after an OSC 52 the runtime accepted")
	}
	if clipboard.Kind != EffectClipboard || clipboard.ID == 0 {
		return failed("effect/the-effect-carries-its-identity",
			"the effect the stream produced is %+v, want a clipboard write with an identity the runtime minted", clipboard)
	}

	// The same effect offered a second time — a carrier re-delivering a chunk,
	// a hub fanning one program's display out to two consumers that merged — is
	// the SAME clipboard write, and must not be applied twice.
	if err := m.Consumers().Offer(clipboard); err != nil {
		return failed("effect/second-delivery",
			"delivering an effect the consumer already holds: %v", err)
	}
	if got := len(c.Effects()); got != 1 {
		return failed("effect/a-duplicate-is-not-delivered-twice",
			"the consumer holds %d clipboard writes after the same effect was delivered twice, want one: the identity is what makes them one effect", got)
	}

	// And a full frame is a resend of STATE: cells, never an effect. This is
	// the half ADR-0066 states in terms — a full frame must never repeat a
	// clipboard write or a notification.
	before := c.Pending()
	if err := m.Consumers().Resend(); err != nil {
		return failed("effect/resend", "resending the state: %v", err)
	}
	if got := len(c.Effects()); got != 1 {
		return failed("effect/a-resend-carries-no-effect",
			"the consumer holds %d at-most-once payloads after a resend of state, want the one it already had: a resend of state must not resend an effect", got)
	}
	if got := c.Pending() - before; got != 1 {
		return failed("effect/a-resend-is-one-frame",
			"the resend handed the consumer %d payloads, want the one frame it is", got)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 16. Every effect kind the vocabulary declares is produced by the stream and
// delivered once.
//
// OSC 9 and OSC 777 are two spellings of ONE kind, so this chunk produces six
// effects of five kinds — and nothing downstream may depend on which spelling a
// program chose.
// ---------------------------------------------------------------------------

func driveEffectKinds(m Runtime) error {
	c := m.Consumers().Attach()
	stream := []byte("\x07\x1b]9;build finished\x07\x1b]777;notify;nocx;done\x07" +
		"\x1b]52;c;aGVsbG8=\x07\x1b]0;a title\x07\x1b]7;file://host/tmp\x07")
	if err := m.Ingest(stream); err != nil {
		return failed("effect/kind-stream", "ingesting one chunk carrying every effect kind: %v", err)
	}
	if got := len(c.Effects()); got != 6 {
		return failed("effect/every-kind-is-delivered",
			"the consumer holds %d at-most-once payloads after a chunk carrying every kind, want 6 (six effects, five kinds)", got)
	}
	for _, k := range []EffectKind{EffectBell, EffectNotification, EffectClipboard, EffectTitle, EffectCwdReport} {
		observe(kindEffect, int(k))
		if !holdsKind(c, k) {
			return failed("effect/every-kind-is-delivered",
				"the consumer was handed no %s from a chunk that carried one", effectKindName(k))
		}
	}

	// And the sequences that carry NO effect carry none: a ConEmu progress hint
	// is the same OSC number as a notification with a payload the renderer's
	// parser returns null for, and turning it into a message would be a
	// delivery the program never asked for.
	before := len(c.Effects())
	if err := m.Ingest([]byte("\x1b]9;4;1;50\x07\x1b]133;D;0\x07")); err != nil {
		return failed("effect/non-effect-stream", "ingesting a progress hint and a fence: %v", err)
	}
	if got := len(c.Effects()); got != before {
		return failed("effect/a-sequence-with-no-effect-delivers-none",
			"a progress hint and a fence produced %d at-most-once payloads, want none: an OSC arriving is not an effect arriving", got-before)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 17. Hostile captures (bead nocx-ygxjv.4, criterion 3).
//
// A remote program can print anything, and ADR-0065 measured what one small
// sequence costs an emulator that does not clamp it: `CSI 1000000000 b` is
// ~180 s and ~10⁹ allocations on x/vt — a projection from completed smaller
// cases, which that record says out loud — while libghostty-vt clamps REP at
// 65535.
//
// WHICH of those bounds applies is the EMULATOR's, and the split is the point.
// With the parser inside the library (ADR-0065, contract.go's Emulator) an
// unterminated sequence is the LIBRARY's to hold, and nothing in this
// vocabulary asks a runtime to hold one. So each schedule states what a runtime
// owes on its OWN account — a number rather than an adjective, and a claim any
// runtime can honour whichever side owns the parser:
//
//   - one ingest call carries at most MaxIngestBytes, and a larger one is
//     refused rather than truncated;
//   - the bytes of an open sequence the runtime keeps ITSELF are bounded by
//     MaxPendingSequence;
//   - whatever it did not hand on is ACCOUNTED for — held within that bound, or
//     dropped with the loss counted and completeness reporting lost-ingest;
//   - the work it spends is linear in the bytes it examined and never in a
//     count inside them.
//
// Whether the LIBRARY bounds what IT holds is the library's own bound, and it is
// measured where the emulator is chosen rather than asserted here: the port
// carries no reading of it (contract.go's Emulator is the geometry half) and a
// schedule handed a Runtime cannot reach one. The measurement at the pin this
// repository links — an unterminated OSC on both of its paths, and a DCS — is
// TestTheLibraryBoundsWhatItHoldsForAnUnterminatedSequence in
// internal/emulator/ghostty, and it is what makes "the emulator's bound is the
// emulator's" a checked sentence rather than the half of one this section used
// to state.
//
// Paired with ruleIngestIsBounded.
// ---------------------------------------------------------------------------

func scheduleHostileRepeatCount(m Runtime) error {
	// A complete, sixteen-byte sequence asking for a billion repetitions.
	capture := []byte("\x1b[1000000000b")
	before := m.IngestState().Work
	if err := m.Ingest(capture); err != nil {
		return failed("hostile/repeat-is-accepted",
			"ingesting a %d-byte sequence inside the ingest bound: %v", len(capture), err)
	}
	if spent := m.IngestState().Work - before; spent > MaxIngestBytes {
		return failed("hostile/repeat-costs-the-bytes-it-carries",
			"the runtime spent %d work units on a %d-byte repeat sequence, want no more than MaxIngestBytes (%d): the count inside it is the emulator's work, and clamping it is nocx-ygxjv.2's",
			spent, len(capture), MaxIngestBytes)
	}
	if got := len(m.IngestState().Pending); got != 0 {
		return failed("hostile/repeat-leaves-nothing-open",
			"the runtime is holding %d bytes after a sequence that terminates: %q", got, m.IngestState().Pending)
	}
	if got := m.Completeness(); got != CompletenessComplete {
		return failed("hostile/repeat-loses-nothing",
			"completeness is %s after a sequence the runtime accepted whole, want complete", completenessName(got))
	}
	return nil
}

func scheduleHostileUnterminatedOSC(m Runtime) error {
	// An OSC that opens and never terminates. A program can do this by accident
	// — a title with a stray byte — or deliberately, as a denial of service
	// aimed at the terminal's memory. What the runtime owes for it is bounded
	// and its own: the part it keeps ITSELF is within MaxPendingSequence, the
	// work it spends is linear in the bytes it examined, and whatever it did
	// not hand on is accounted for. Which runtime ends up holding the sequence
	// is decided by who owns the parser (ADR-0065) — this one holds none of it
	// and hands the whole sequence to the emulator, the model holds the trailing
	// open sequence and trims it — and the assertion is the same for both
	// because it is about the account rather than about the architecture. That
	// the library's own holding is bounded is measured where the emulator is
	// chosen (ghostty's TestTheLibraryBoundsWhatItHoldsForAnUnterminatedSequence).
	body := bytes.Repeat([]byte("A"), 4*MaxPendingSequence)
	capture := append([]byte("\x1b]0;"), body...)
	before := m.IngestState().Work
	if err := m.Ingest(capture); err != nil {
		return failed("hostile/unterminated-osc-is-accepted",
			"ingesting %d bytes of an unterminated OSC, inside the ingest bound: %v", len(capture), err)
	}
	if got := len(m.IngestState().Pending); got > MaxPendingSequence {
		return failed("hostile/unterminated-osc-is-bounded",
			"the runtime holds %d bytes of a sequence that never terminates, want at most MaxPendingSequence (%d)", got, MaxPendingSequence)
	}
	if spent := m.IngestState().Work - before; spent > MaxIngestBytes {
		return failed("hostile/unterminated-osc-costs-the-bytes-it-carries",
			"the runtime spent %d work units on %d bytes of output, want no more than MaxIngestBytes (%d)", spent, len(capture), MaxIngestBytes)
	}
	if err := whatTheRuntimeDidNotHandOnIsAccountedFor(m, len(capture), "hostile/the-dropped-sequence-is-reported"); err != nil {
		return err
	}
	observe(kindCompleteness, int(CompletenessLostIngest))
	return nil
}

func scheduleHostileOversizedDCS(m Runtime) error {
	// A DCS larger than any bound the runtime states, in ONE call. It is
	// refused, and a refusal changes nothing — not the clock, not the sequence
	// held, not the work charged.
	oversized := append([]byte("\x1bP"), bytes.Repeat([]byte("D"), MaxIngestBytes)...)
	before := fingerprintOf(m)
	if err := m.Ingest(oversized); !errors.Is(err, ErrIngestTooLarge) {
		return failed("hostile/an-oversized-ingest-is-refused",
			"Ingest of %d bytes returned %v, want %v", len(oversized), err, ErrIngestTooLarge)
	}
	if d := before.diff(fingerprintOf(m)); d != "" {
		return failed("hostile/an-oversized-ingest-changes-nothing",
			"a refused ingest changed the runtime (%s); an event the vocabulary refuses must change no state", d)
	}

	// Delivered in calls the runtime does take, the same sequence is still
	// bounded and still accounted for, by the same property the OSC schedule
	// asserts and for the same reason: the bound below is on what the runtime
	// keeps ITSELF, and the accounting is what makes "the excess was dropped"
	// true rather than assumed. Which side holds the DCS bytes while it is open
	// is decided by who owns the parser (ADR-0065), and the library's own
	// holding is measured where the emulator is chosen.
	fed := 0
	for range 4 {
		chunk := append([]byte("\x1bP"), bytes.Repeat([]byte("D"), MaxPendingSequence)...)
		if err := m.Ingest(chunk); err != nil {
			return failed("hostile/chunked-dcs-is-accepted", "ingesting one DCS chunk: %v", err)
		}
		fed += len(chunk)
	}
	if got := len(m.IngestState().Pending); got > MaxPendingSequence {
		return failed("hostile/oversized-dcs-is-bounded",
			"the runtime holds %d bytes of an unterminated DCS, want at most MaxPendingSequence (%d)", got, MaxPendingSequence)
	}
	return whatTheRuntimeDidNotHandOnIsAccountedFor(m, fed, "hostile/oversized-dcs-is-bounded")
}

// ---------------------------------------------------------------------------
// The pairs. Every schedule above runs once with every rule on, where it must
// pass, and once with exactly one rule removed, where a NAMED assertion must
// fail. The second run is the acceptance criterion: a rule that cannot be
// removed to make a named assertion fail is not being tested by the schedule
// paired with it.
//
// Read the two halves differently, because they are evidence about two
// different things. "With every rule on the schedule must pass" is evidence
// about the CONTRACT, and so is the assertion the second half NAMES: the
// schedule is written against the interface, so the sentence it fails with is
// one a real runtime is judged by too. The REMOVAL is evidence about the MODEL
// and nothing else — which rule makes which schedule fail is a fact about
// model_test.go's rule set, and a runtime with no rules to remove cannot be
// asked a question of that shape. Pairing the assertion with a rule is how the
// model is kept falsifiable; it is not how the contract is kept true.
// ---------------------------------------------------------------------------

// The two session ids the fairness schedule is run over, named here because
// BUILDING the arrangement is the harness's business and not the schedule's.
const (
	busySessionID  SessionID = "the-busy-session"
	otherSessionID SessionID = "the-other-session"
)

// sessionsOverOneBudget builds the fairness schedule's two runtimes — two
// sessions, one delivery allowance between them — which is a piece of
// CONSTRUCTION: the schedule judges what the arrangement does, and a schedule
// that wired it would be asserting against its own setup. Whether the allowance
// is per session is unobservable unless these two share one account.
func sessionsOverOneBudget(rules ruleSet) (Runtime, Runtime) {
	budget := newDeliveryBudget()
	return newSessionModel(rules, budget, busySessionID), newSessionModel(rules, budget, otherSessionID)
}

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

// The second half of the same schedule. A handover owes two things that pull in
// opposite directions — cancel what has not run, and never disown what has —
// and a model that walks its records rather than its queue can get the first
// right while breaking the second. See section 5.
func TestSchedule_TakeoverWithInputQueued_FailsWhenExecutedIsReversible(t *testing.T) {
	err := scheduleTakeoverWithInputQueued(newModel(without(ruleExecutedIsIrreversible)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleExecutedIsIrreversible])
	}
	assertionFailed(t, err, "takeover/executed-survives-the-handover")
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

// --- the delivery rules (bead nocx-ygxjv.4) --------------------------------

func TestSchedule_ConsumerThatNeverReads(t *testing.T) {
	if err := scheduleConsumerThatNeverReads(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_ConsumerThatNeverReads_FailsWhenItsQueueRuleIsRemoved(t *testing.T) {
	err := scheduleConsumerThatNeverReads(newModel(without(ruleConsumerQueueIsBounded)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleConsumerQueueIsBounded])
	}
	assertionFailed(t, err, "delivery/the-queue-is-at-its-bound")
}

func TestSchedule_ConsumerThatNeverReads_FailsWhenItsReportingRuleIsRemoved(t *testing.T) {
	err := scheduleConsumerThatNeverReads(newModel(without(ruleConsumerLossIsReported)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleConsumerLossIsReported])
	}
	assertionFailed(t, err, "delivery/what-the-consumer-lost-is-reported")
}

func TestSchedule_ConsumerThatNeverReads_FailsWhenItsLosslessRuleIsRemoved(t *testing.T) {
	err := scheduleConsumerThatNeverReads(newModel(without(ruleLosslessIngestSurvivesASlowConsumer)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleLosslessIngestSurvivesASlowConsumer])
	}
	assertionFailed(t, err, "delivery/a-wedged-consumer-costs-no-ingest")
}

func TestSchedule_OneSessionCannotSpendAnothersAllowance(t *testing.T) {
	busy, other := sessionsOverOneBudget(allRules())
	if err := scheduleOneSessionCannotSpendAnothersAllowance(busy, other); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_OneSessionCannotSpendAnothersAllowance_FailsWhenItsRuleIsRemoved(t *testing.T) {
	// The paired run differs in the RULES alone, over the same arrangement: two
	// sessions, one shared budget.
	busy, other := sessionsOverOneBudget(without(rulePerSessionAllowance))
	err := scheduleOneSessionCannotSpendAnothersAllowance(busy, other)
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[rulePerSessionAllowance])
	}
	assertionFailed(t, err, "delivery/one-session-cannot-spend-anothers-allowance")
}

func TestSchedule_EffectDeliveryPolicy(t *testing.T) {
	if err := scheduleEffectDeliveryPolicy(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_EffectDeliveryPolicy_FailsWhenItsDuplicateRuleIsRemoved(t *testing.T) {
	err := scheduleEffectDeliveryPolicy(newModel(without(ruleDuplicateEffectIsSuppressed)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleDuplicateEffectIsSuppressed])
	}
	assertionFailed(t, err, "effect/a-duplicate-is-not-delivered-twice")
}

func TestSchedule_EffectDeliveryPolicy_FailsWhenItsResendRuleIsRemoved(t *testing.T) {
	err := scheduleEffectDeliveryPolicy(newModel(without(ruleResendCarriesNoEffects)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleResendCarriesNoEffects])
	}
	assertionFailed(t, err, "effect/a-resend-carries-no-effect")
}

func TestSchedule_HostileRepeatCount(t *testing.T) {
	if err := scheduleHostileRepeatCount(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_HostileRepeatCount_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleHostileRepeatCount(newModel(without(ruleIngestIsBounded)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleIngestIsBounded])
	}
	assertionFailed(t, err, "hostile/repeat-costs-the-bytes-it-carries")
}

func TestSchedule_HostileUnterminatedOSC(t *testing.T) {
	if err := scheduleHostileUnterminatedOSC(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_HostileUnterminatedOSC_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleHostileUnterminatedOSC(newModel(without(ruleIngestIsBounded)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleIngestIsBounded])
	}
	assertionFailed(t, err, "hostile/unterminated-osc-is-bounded")
}

func TestSchedule_HostileOversizedDCS(t *testing.T) {
	if err := scheduleHostileOversizedDCS(newModel(allRules())); err != nil {
		t.Fatalf("with every rule on the schedule must pass: %v", err)
	}
}

func TestSchedule_HostileOversizedDCS_FailsWhenItsRuleIsRemoved(t *testing.T) {
	err := scheduleHostileOversizedDCS(newModel(without(ruleIngestIsBounded)))
	if err == nil {
		t.Fatalf("removing rule %q must make this schedule fail; it did not", ruleNames[ruleIngestIsBounded])
	}
	assertionFailed(t, err, "hostile/an-oversized-ingest-is-refused")
}

// Every payload belongs to exactly one class, and WHICH class an effect kind
// belongs to is decided in one place, exhaustively. A kind added to contract.go
// without being named there falls to DeliveryUnclassified, is refused by the
// delivery path, and fails here — which is what makes the sentence in
// contract.go a check rather than a claim.
func TestEveryEffectKindBelongsToTheAtMostOnceClass(t *testing.T) {
	for k := EffectBell; k <= EffectCwdReport; k++ {
		if got := classOfEffect(k); got != DeliveryAtMostOnce {
			t.Errorf("%s classifies as %s, want at-most-once: every effect kind the vocabulary declares changes no cell and must not be applied twice",
				effectKindName(k), deliveryClassName(got))
		}
	}
	if got := classOfEffect(EffectNone); got != DeliveryUnclassified {
		t.Errorf("the zero effect kind classifies as %s, want unclassified: it names no effect, so it is not a payload any class covers",
			deliveryClassName(got))
	}
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
	// build is the runtime the event is invalid IN, when the ordinary one —
	// every rule on, completeness established — is not the state it needs. The
	// only event that needs another is the write gate, which refuses while
	// completeness is unknown and that is a state a runtime BEGINS in: it is
	// built rather than switched on through the contract, because "establish
	// nothing" is a producer a real runtime has and a schedule is not one.
	// A nil builder means newModel.
	build func(ruleSet) Runtime
	// drive brings the runtime to the state the event is invalid in, performs
	// the event, and asserts both the refusal and that nothing changed.
	drive func(m Runtime) error
}

// runtime applies the builder, or the ordinary construction when there is none.
func (ev invalidEvent) runtime(rules ruleSet) Runtime {
	if ev.build != nil {
		return ev.build(rules)
	}
	return newModel(rules)
}

func invalidEvents() []invalidEvent {
	return []invalidEvent{
		{
			name: "Admit carrying evidence about another incarnation",
			drive: func(m Runtime) error {
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
			drive: func(m Runtime) error {
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
			drive: func(m Runtime) error {
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
			// The runtime BEGINS here, which is why this is a builder and not a
			// step in the drive: while it cannot say whether it holds the whole
			// stream it executes nothing, and establishing anything is a
			// producer a real runtime has (nocx-ygxjv.2) rather than something
			// a schedule reaches for through the contract.
			build: func(r ruleSet) Runtime { return newUnestablishedModel(r) },
			drive: func(m Runtime) error {
				ctrl, err := grant(m, person())
				if err != nil {
					return err
				}
				if _, admitErr := admitKey(m, ctrl, []byte("l")); admitErr != nil {
					return admitErr
				}
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
			drive: func(m Runtime) error {
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
			drive: func(m Runtime) error {
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
			drive: func(m Runtime) error {
				before := fingerprintOf(m)
				if err := m.ExpireRendezvous(); !errors.Is(err, ErrNoRendezvous) {
					return failed("invalid/expiry-without-a-rendezvous-refused",
						"ExpireRendezvous with an idle rendezvous returned %v, want %v", err, ErrNoRendezvous)
				}
				return mustBeUnchanged(m, before, "invalid/expiry-without-a-rendezvous-changes-nothing")
			},
		},
		{
			name: "Delivering a payload whose class nobody decided",
			drive: func(m Runtime) error {
				// The zero value of DeliveryClass is not a class: a payload
				// carrying it is refused rather than delivered as though the
				// zero value were a policy. This is the direction that makes
				// "every payload belongs to exactly one class" a promise.
				c := m.Consumers().Attach()
				before := fingerprintOf(m)
				err := m.Consumers().Offer(Effect{ID: 1, At: m.Incarnation(), Kind: EffectNone})
				if !errors.Is(err, ErrUnclassifiedDelivery) {
					return failed("invalid/unclassified-delivery-refused",
						"delivering an effect of kind %s returned %v, want %v", effectKindName(EffectNone), err, ErrUnclassifiedDelivery)
				}
				if err := mustBeUnchanged(m, before, "invalid/unclassified-delivery-changes-nothing"); err != nil {
					return err
				}
				if got := len(c.Effects()); got != 0 {
					return failed("invalid/unclassified-delivery-changes-nothing",
						"a refused payload left the consumer holding %d of them", got)
				}
				return nil
			},
		},
	}
}

func TestInvalidEventsChangeNothing(t *testing.T) {
	for _, ev := range invalidEvents() {
		t.Run(ev.name, func(t *testing.T) {
			if err := ev.drive(ev.runtime(allRules())); err != nil {
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
	m := newUnestablishedModel(without(ruleUnknownCompletenessRefusesWrites))
	ctrl, err := grant(m, person())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admitKey(m, ctrl, []byte("l")); err != nil {
		t.Fatal(err)
	}
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

func driveReportHole(m Runtime) error {
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

func deliveryWalk(c DeliveryClass, unreachable string) stateWalk {
	return stateWalk{name: "delivery class " + deliveryClassName(c), kind: kindDelivery, value: int(c), unreachable: unreachable}
}

func effectWalk(k EffectKind, unreachable string) stateWalk {
	return stateWalk{name: "effect " + effectKindName(k), kind: kindEffect, value: int(k), unreachable: unreachable}
}

func TestEveryStateIsReachableOrNamedUnreachable(t *testing.T) {
	// The schedules are the evidence, so this test drives them itself rather
	// than trusting whatever ran before it.
	observedStates = map[string]bool{}

	for _, s := range []struct {
		name  string
		sched func() error
	}{
		{"FenceAuthenticatedFirst", func() error { return scheduleFenceAuthenticatedFirst(newModel(allRules())) }},
		{"FenceSightedFirst", func() error { return scheduleFenceSightedFirst(newModel(allRules())) }},
		{"FenceSightedFirstSurvivesScreenTrim", func() error { return scheduleFenceSightedFirstSurvivesScreenTrim(newModel(allRules())) }},
		{"TakeoverWithInputQueued", func() error { return scheduleTakeoverWithInputQueued(newModel(allRules())) }},
		{"DisconnectAfterAdmission", func() error { return scheduleDisconnectAfterAdmission(newModel(allRules())) }},
		{"ResizeDuringOutput", func() error { return scheduleResizeDuringOutput(newModel(allRules())) }},
		{"ObserverResync", func() error { return scheduleObserverResync(newModel(allRules())) }},
		{"KeyEncodedAgainstModes", func() error { return scheduleKeyEncodedAgainstModes(newModel(allRules())) }},
		{"RendezvousExpiresUnjoined", func() error { return scheduleRendezvousExpiresUnjoined(newModel(allRules())) }},
		{"PreconditionStaleAtExecution", func() error { return schedulePreconditionStaleAtExecution(newModel(allRules())) }},
		{"RuntimeFailure", func() error { return scheduleRuntimeFailure(newModel(allRules())) }},

		// The delivery schedules (bead nocx-ygxjv.4). The fairness one is run
		// over the two runtimes sessionsOverOneBudget wires, which is why this
		// list takes closures: the arrangement is the harness's.
		{"ConsumerThatNeverReads", func() error { return scheduleConsumerThatNeverReads(newModel(allRules())) }},
		{"OneSessionCannotSpendAnothersAllowance", func() error {
			busy, other := sessionsOverOneBudget(allRules())
			return scheduleOneSessionCannotSpendAnothersAllowance(busy, other)
		}},
		{"EffectDeliveryPolicy", func() error { return scheduleEffectDeliveryPolicy(newModel(allRules())) }},
		{"HostileRepeatCount", func() error { return scheduleHostileRepeatCount(newModel(allRules())) }},
		{"HostileUnterminatedOSC", func() error { return scheduleHostileUnterminatedOSC(newModel(allRules())) }},
		{"HostileOversizedDCS", func() error { return scheduleHostileOversizedDCS(newModel(allRules())) }},
	} {
		if err := s.sched(); err != nil {
			t.Fatalf("schedule %s must pass with every rule on, or the states it reaches are not evidence: %v", s.name, err)
		}
	}
	for _, ev := range invalidEvents() {
		if err := ev.drive(ev.runtime(allRules())); err != nil {
			t.Fatalf("invalid event %q must be refused without changing anything, or the states it passes through are not evidence: %v", ev.name, err)
		}
	}
	if err := driveReportHole(newModel(allRules())); err != nil {
		t.Fatalf("reporting a hole, or the states it passes through are not evidence: %v", err)
	}
	if err := driveEffectKinds(newModel(allRules())); err != nil {
		t.Fatalf("delivering every effect kind, or the states it passes through are not evidence: %v", err)
	}

	walk := []stateWalk{
		intentWalk(IntentStateNone, "the zero value: IntentState reports it for an id nothing was admitted under, so it names the ABSENCE of an intent rather than a state an intent occupies"),
		intentWalk(IntentStateAdmitted, ""),
		intentWalk(IntentStateExecuted, ""),
		intentWalk(IntentStateCancelled, ""),
		intentWalk(IntentStateRefused, ""),
		intentWalk(IntentStateFailed, "the producer exists and no schedule injects one: Execute reports it when the TERMINAL refuses the write (a real one does when proc.Write fails part-way — nocx-ygxjv.2), and the terminal every schedule here is constructed over takes everything it is handed, because a schedule's subject is the runtime's decisions and not a tty's"),

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

		deliveryWalk(DeliveryUnclassified, "the zero value, and NOT a class: a payload nobody classified is REFUSED rather than delivered as though the zero value were a policy, and the refusal is asserted in the invalid-event table — which is why no schedule observes it as a delivery"),
		deliveryWalk(DeliveryLossless, ""),
		deliveryWalk(DeliveryCoalescable, ""),
		deliveryWalk(DeliveryAtMostOnce, ""),

		effectWalk(EffectNone, "the zero value: an OSC number that carries no effect — a fence, a progress hint — yields NO effect rather than one of this kind, so nothing produces it"),
		effectWalk(EffectBell, ""),
		effectWalk(EffectNotification, ""),
		effectWalk(EffectClipboard, ""),
		effectWalk(EffectTitle, ""),
		effectWalk(EffectCwdReport, ""),
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
