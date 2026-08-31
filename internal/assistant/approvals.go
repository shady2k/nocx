package assistant

// The approval record (design §7.2, bead nocx-z9hj4): a human's yes binds to
// ONE exact tool proposal — run, attempt, tool name, call id and a hash of
// the canonical arguments — so approving one thing never authorises a changed
// thing. The approved call runs as its own SUBSEQUENT attempt of the
// proposal's own entry (ADR-0020 decision 4: a retry after approval is an
// execution of the same intent, never a new intent), and the store is what
// carries the yes across the checkpoint, because the checkpoint itself is
// process-lifetime state (ADR-0028 decision: checkpoints are not records).
// Process-lifetime like the checkpoint: approval does not survive a restart,
// which is already what the recovery rule says.
//
// The store also carries the egress gate's retained result (design §7.1 —
// "send it as it is"): the withheld bytes of the finding that suspended the
// run, keyed by the same proposal. A resume that re-ran the tool would repeat
// the effect, so the gate retains the exact result the person was shown and
// the approved resume sends THAT, never a newly produced one.

import (
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/content"
)

// FileVersionBindingState says whether a proposal names files whose versions
// must be captured. The zero value is deliberately unknown and fails closed:
// no-file approvals must say NotApplicable, while file-bearing approvals must
// say Captured and carry at least one version.
type FileVersionBindingState string

const (
	FileVersionBindingUnknown       FileVersionBindingState = ""
	FileVersionBindingNotApplicable FileVersionBindingState = "not-applicable"
	FileVersionBindingRequired      FileVersionBindingState = "required"
	FileVersionBindingCaptured      FileVersionBindingState = "captured"
)

// Approval is one human decision about one exact proposal.
type Approval struct {
	RunID   string
	Attempt int
	Tool    string
	CallID  string
	ArgHash string
	Effect  content.Effect
	// EntryID is the ledger row that recorded the proposal — the entry the
	// approved call runs as a SUBSEQUENT attempt of. It is a carrier, NOT
	// part of the binding: the key stays the five binding fields, so a
	// changed argument hashes differently and never resumes under the old
	// approval (nocx-5dldy).
	EntryID string
	// Invocation is the canonical command parse used to create an
	// invocation rule for a standing answer. It is a carrier, not part of
	// the exact-proposal key.
	Invocation content.Invocation
	// CommandInvocation preserves command-vs-non-command provenance even when
	// a malformed command has no parsed invocation to carry.
	CommandInvocation bool
	// FileVersionState distinguishes an explicit no-file proposal from a
	// file-bearing proposal whose identities were never captured.
	FileVersionState FileVersionBindingState
	// FileVersions are the exact path versions the proposal is allowed to
	// read or execute. They are carriers, not part of the proposal key.
	FileVersions []FileVersion
}

type approvalKey struct {
	runID   string
	attempt int
	tool    string
	callID  string
	argHash string
}

type approvalEntry struct {
	entryID           string
	effect            content.Effect
	invocation        content.Invocation
	commandInvocation bool
	fileVersionState  FileVersionBindingState
	fileVersions      []FileVersion
}

// DeclineKind is what a person's no means, recorded with the declined
// proposal so the resumed attempt's middleware can say it in the right
// words (nocx-uvac6.1): the refusal is the call's result, and whether it is
// standing — and how far — changes the sentence the model reads.
type DeclineKind string

const (
	// DeclineCallOnce: the no covers this call only; nothing is standing.
	DeclineCallOnce DeclineKind = "once"
	// DeclineCallSession: the no, and a standing refusal of this kind of
	// call for the rest of this session.
	DeclineCallSession DeclineKind = "session"
	// DeclineCallAlways: the no, and a standing refusal of this kind of
	// call from now on.
	DeclineCallAlways DeclineKind = "always"
)

type declinedEntry struct {
	runID             string
	kind              DeclineKind
	effect            content.Effect
	invocation        content.Invocation
	commandInvocation bool
	fileVersionState  FileVersionBindingState
	fileVersions      []FileVersion
}

// retainedValue is the withheld result of an egress finding (design §7.1):
// the exact bytes — or the exact error string — the person was shown, never a
// re-run's freshly produced ones.
type retainedValue struct {
	out      string
	wasError bool
}

// ApprovalStore keeps the pending requests (what the human is being asked),
// the approvals (what the human said yes to), the declined proposals (what
// the human said no to) and the retained egress results (what was withheld
// pending the decision). All keyed by the exact proposal.
type ApprovalStore struct {
	mu       sync.Mutex
	approved map[approvalKey]approvalEntry
	declined map[approvalKey]declinedEntry
	pending  map[approvalKey]approvalEntry
	retained map[approvalKey]retainedValue
}

// NewApprovalStore builds the process-lifetime approval store. The transport
// owns one per server and passes it on every Ask, so the run that escalated
// and the run that resumes consult the SAME decisions; the store is what
// carries a yes or a no across the suspension.
func NewApprovalStore() *ApprovalStore {
	return &ApprovalStore{
		approved: make(map[approvalKey]approvalEntry),
		declined: make(map[approvalKey]declinedEntry),
		pending:  make(map[approvalKey]approvalEntry),
		retained: make(map[approvalKey]retainedValue),
	}
}

func (s *ApprovalStore) Request(ap Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[keyOf(ap)] = approvalEntry{
		entryID: ap.EntryID, effect: ap.Effect, invocation: cloneInvocation(ap.Invocation),
		commandInvocation: ap.CommandInvocation || (ap.Invocation.Parsed && ap.Invocation.Commands != nil),
		fileVersionState:  ap.FileVersionState,
		fileVersions:      cloneFileVersions(ap.FileVersions),
	}
}

// Approve records a yes to this exact proposal: the pending ask is answered
// and the proposal moves to approved, so the resume's re-run of the pipeline
// skips the ask. It returns false when the proposal was NOT pending — never
// asked, or already answered — and records nothing: a yes to a question
// nobody was asked is not a decision, and a stale or unknown approval id
// must not resume anything (acceptance criterion 7). The caller (the
// transport's agent.approve) checks IsPending first and treats a false
// return as the honest "unknown approval" refusal.
func (s *ApprovalStore) Approve(ap Approval) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.pending[keyOf(ap)]
	if !ok {
		return false
	}
	delete(s.pending, keyOf(ap))
	// The wire's approve carries only the five binding fields; the entry
	// the proposal was recorded under, its effect and its canonical
	// invocation are the pending record's own.
	if ap.EntryID == "" {
		ap.EntryID = cur.entryID
	}
	if ap.Effect == "" {
		ap.Effect = cur.effect
	}
	if len(ap.Invocation.Commands) == 0 {
		ap.Invocation = cur.invocation
	}
	s.approved[keyOf(ap)] = approvalEntry{
		entryID: ap.EntryID, effect: ap.Effect, invocation: cloneInvocation(ap.Invocation),
		commandInvocation: cur.commandInvocation,
		fileVersionState:  cur.fileVersionState,
		fileVersions:      cloneFileVersions(cur.fileVersions),
	}
	return true
}

// IsApproved reports whether this exact proposal was approved.
func (s *ApprovalStore) IsApproved(ap Approval) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.approved[keyOf(ap)]
	return ok
}

// ApprovedFileVersions returns a copy of every path identity carried by the
// approved proposal. The copy keeps callers from mutating the store's binding.
func (s *ApprovalStore) ApprovedFileVersions(ap Approval) ([]FileVersion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.approved[keyOf(ap)]
	if !ok {
		return nil, false
	}
	return cloneFileVersions(entry.fileVersions), true
}

// VerifyApprovedFileVersions checks all approved paths immediately before
// dispatch. The first failure names the file whose approval is no longer
// valid; no caller should execute the tool after a non-nil error.
func (s *ApprovalStore) VerifyApprovedFileVersions(ap Approval) error {
	s.mu.Lock()
	entry, ok := s.approved[keyOf(ap)]
	state := entry.fileVersionState
	versions := cloneFileVersions(entry.fileVersions)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("approval: proposal is not approved")
	}
	switch state {
	case FileVersionBindingNotApplicable:
		return nil
	case FileVersionBindingCaptured:
		if len(versions) == 0 {
			return fmt.Errorf("approval: file version binding is marked captured but contains no versions")
		}
	default:
		return fmt.Errorf("approval: file version binding is missing")
	}
	for _, version := range versions {
		if err := VerifyFileVersion(version); err != nil {
			return err
		}
	}
	return nil
}

// Decline records a NO to this exact proposal (nocx-uvac6.1): the pending
// ask is answered and the proposal moves to declined, so the resumed
// attempt's re-run of the pipeline returns the refusal as the call's result
// instead of re-asking. It returns false when the proposal was NOT pending
// — never asked, or already answered — and records nothing: a no to a
// question nobody was asked is not a decision.
//
// kind is what the no means beyond this call — the standing half of the
// answer, which the middleware's refusal text carries ("in this session",
// "from now on", or nothing for a one-off no). The standing part is written
// by transport as an invocation rule for command tools or an effect row for
// non-command tools. The store carries both the effect and canonical
// invocation from the pending record so the resumed pipeline applies the
// same decision without reparsing.
func (s *ApprovalStore) Decline(ap Approval, kind DeclineKind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.pending[keyOf(ap)]
	if !ok {
		return false
	}
	delete(s.pending, keyOf(ap))
	s.declined[keyOf(ap)] = declinedEntry{
		runID: ap.RunID, kind: kind, effect: cur.effect,
		invocation:        cloneInvocation(cur.invocation),
		commandInvocation: cur.commandInvocation,
		fileVersionState:  cur.fileVersionState,
		fileVersions:      cloneFileVersions(cur.fileVersions),
	}
	return true
}

// DeclinedKind returns what the person's no to this exact proposal meant.
// ok is false when the proposal was never declined — the ordinary case for
// every call that was not asked about.
func (s *ApprovalStore) DeclinedKind(ap Approval) (DeclineKind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.declined[keyOf(ap)]
	return e.kind, ok
}

// DowngradeDeclined lowers a declined proposal's standing half to a one-off
// no when the standing policy write did not stick. The person still refused
// this call, but the model must not be told the refusal is permanent when
// later policy decisions will not enforce it.
func (s *ApprovalStore) DowngradeDeclined(ap Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := keyOf(ap)
	if e, ok := s.declined[k]; ok {
		e.kind = DeclineCallOnce
		s.declined[k] = e
	}
}

// DeclinedEffect reports a standing no covering a non-command effect class
// within one run. Command tools use DeclinedInvocation instead, because an
// effect-wide refusal would cover unrelated commands.
func (s *ApprovalStore) DeclinedEffect(runID string, effect content.Effect) (DeclineKind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.declined {
		if e.runID == runID && e.effect == effect && e.kind != DeclineCallOnce {
			return e.kind, true
		}
	}
	return "", false
}

// IsPending reports whether the human is CURRENTLY being asked about this
// exact proposal — the source of truth a stale or unknown approval id is
// answered against (acceptance criterion 7): an id that is not pending was
// never asked, or was already answered, and must not resume anything.
func (s *ApprovalStore) IsPending(ap Approval) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[keyOf(ap)]
	return ok
}

// EntryIDFor returns the ledger entry that recorded the proposal — the entry
// the approved call runs as a subsequent attempt of. ok is false when the
// proposal is neither pending nor approved, or was recorded without an entry
// (a nil-ledger run: no durable thread exists).
func (s *ApprovalStore) EntryIDFor(ap Approval) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.approved[keyOf(ap)]; ok && e.entryID != "" {
		return e.entryID, true
	}
	if e, ok := s.pending[keyOf(ap)]; ok && e.entryID != "" {
		return e.entryID, true
	}
	return "", false
}

// NoteEffect records the effect class the gate decided this proposal under —
// the matrix row a "this session" or "always" answer is about
// (nocx-ki305). It is recorded separately from Request because the two facts
// have two writers: the middleware calls Request at escalation, carrying the
// binding and the ledger entry, and the effect only reaches the transport,
// which builds the question the person is shown. Noting it therefore never
// touches the entry id the middleware's record carries — overwriting that is
// what would cost the approved call its ledger thread.
//
// A proposal that is not pending is not invented, and an empty effect notes
// nothing: an answer whose row is unknown must fall through to asking rather
// than write a row nobody named.
func (s *ApprovalStore) NoteEffect(ap Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ap.Effect == "" {
		return
	}
	k := keyOf(ap)
	e, ok := s.pending[k]
	if !ok {
		return
	}
	e.effect = ap.Effect
	s.pending[k] = e
}

// EffectFor returns the effect class the proposal was decided under. ok is
// false when the proposal is neither pending, approved, nor declined, or was
// recorded without one — and a false there is the fail-toward-asking end:
// the answer applies to this call and nothing is written to any policy.
func (s *ApprovalStore) EffectFor(ap Approval) (content.Effect, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := keyOf(ap)
	if e, ok := s.pending[k]; ok && e.effect != "" {
		return e.effect, true
	}
	if e, ok := s.approved[k]; ok && e.effect != "" {
		return e.effect, true
	}
	if e, ok := s.declined[k]; ok && e.effect != "" {
		return e.effect, true
	}
	return "", false
}

// InvocationFor returns the canonical invocation recorded with a pending,
// approved or declined proposal, together with whether the proposal was for a
// command tool. The second result is false for malformed command parses and
// non-command tools; the third preserves that distinction.
func (s *ApprovalStore) InvocationFor(ap Approval) (content.Invocation, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := keyOf(ap)
	if e, ok := s.pending[k]; ok {
		return cloneInvocation(e.invocation), e.invocation.Parsed, e.commandInvocation
	}
	if e, ok := s.approved[k]; ok {
		return cloneInvocation(e.invocation), e.invocation.Parsed, e.commandInvocation
	}
	if e, ok := s.declined[k]; ok {
		return cloneInvocation(e.invocation), e.invocation.Parsed, e.commandInvocation
	}
	return content.Invocation{}, false, false
}

func cloneInvocation(inv content.Invocation) content.Invocation {
	out := content.Invocation{
		Parsed:       inv.Parsed,
		Disqualified: inv.Disqualified,
		Resources: content.ResourceReport{
			Resources:  append([]content.Resource(nil), inv.Resources.Resources...),
			Unresolved: append([]content.UnresolvedResource(nil), inv.Resources.Unresolved...),
		},
	}
	if inv.Commands == nil {
		return out
	}
	out.Commands = make([][]string, len(inv.Commands))
	for i, command := range inv.Commands {
		out.Commands[i] = append([]string(nil), command...)
	}
	return out
}

// DeclinedInvocation reports a standing refusal matching the canonical
// invocation. Effect-wide standing refusals are intentionally not supported:
// unrelated commands must remain available.
func (s *ApprovalStore) DeclinedInvocation(runID string, inv content.Invocation) (DeclineKind, bool) {
	if !inv.Parsed {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.declined {
		if e.runID != runID || e.kind == DeclineCallOnce || !sameInvocation(e.invocation, inv) {
			continue
		}
		return e.kind, true
	}
	return "", false
}

func sameInvocation(a, b content.Invocation) bool {
	if a.Parsed != b.Parsed || a.Disqualified != b.Disqualified || len(a.Commands) != len(b.Commands) {
		return false
	}
	for i := range a.Commands {
		if len(a.Commands[i]) != len(b.Commands[i]) {
			return false
		}
		for j := range a.Commands[i] {
			if a.Commands[i][j] != b.Commands[i][j] {
				return false
			}
		}
	}
	return true
}

// Retain holds the withheld result of an egress finding (design §7.1) so the
// approved resume can send the EXACT bytes the person was shown — a resume
// that re-ran the tool would repeat the effect and could produce a different
// result than the one approved. The result is bounded by the ingest bound
// (maxToolResultBytes); it is process-lifetime, like every other piece of the
// approval machinery, and dies with the restart.
func (s *ApprovalStore) Retain(ap Approval, out string, wasError bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retained[keyOf(ap)] = retainedValue{out: out, wasError: wasError}
}

// RetainedResult returns the withheld result of an egress finding for the
// exact proposal, when one is retained. The approved resume returns it
// instead of re-running the tool.
func (s *ApprovalStore) RetainedResult(ap Approval) (string, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.retained[keyOf(ap)]
	return v.out, v.wasError, ok
}

// ClearRetained drops the retained result: the approved resume has sent it,
// or the run has terminalized — the bytes are no longer needed.
func (s *ApprovalStore) ClearRetained(ap Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.retained, keyOf(ap))
}

// keyOf is the binding and ONLY the binding: EntryID and Effect are carriers
// the record holds, never part of what a yes matches.
func keyOf(ap Approval) approvalKey {
	return approvalKey{
		runID:   ap.RunID,
		attempt: ap.Attempt,
		tool:    ap.Tool,
		callID:  ap.CallID,
		argHash: ap.ArgHash,
	}
}
