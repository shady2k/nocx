package workers

// The restart record: the minimal durable link from a worker pane to the
// identity a relaunch needs, so that a conversation survives the machine
// nocx runs on (ADR-0079).
//
// # WHAT THIS IS NOT
//
// It is not a second layout store and not a durable participant record. The
// layout chain (internal/content/layout.go) owns pane, tab and workspace
// identity and keeps it; the live store (store.go's MemoryStore) keeps what is
// true right now. This is four facts the chain does not hold — which agent a
// pane ran, where it was launched, and the resume identity its conversation is
// continued under — and it is read at exactly one moment: the startup restore.
// ADR-0079 supersedes one half of the 2026-08-15 spec's D5 ("at stage 1
// workers die with the backend") and defers everything else D5 defers, so
// nothing here survives a process: the PTY, the helper session and the live
// coordinator state all still die with the machine.
//
// # WHY A CONVERSATION OUTLIVES THE PROCESS THAT HELD IT
//
// The agent's own conversation store lives on disk and survives a reboot;
// nocx's does not. So a resume is a LAUNCH — `claude --resume <id>`, `codex
// resume <id>` — against an identity this record holds, not a reattach to
// something that was still running. That is why the record carries the resume
// identity at all, and why a record whose checkout or resume identity is gone
// is a FAILURE TO RESTORE rather than an empty shell: the conversation either
// comes back or the pane says why it did not (nocx-xn63t.5.2).
//
// # INTERRUPTED, NEVER LIVE
//
// Every record read back describes a process that is gone, so every
// restoration reports StateInterrupted. Nothing here may report a live
// participant: the record's liveness is bound to the backend instance that
// minted it (Liveness.BackendInstance, AD-7), and that instance did not
// survive the restart. A relaunch writes a NEW liveness through the ordinary
// MarkLive path, which is the only way anything becomes live again.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

// ResumeMode is HOW a worker's conversation is continued, and it is the launch
// record's question rather than this package's (nocx-dz9vj, nocx-2txuc): the
// record stores what it was told and never decides a flag itself, because a
// mode derived here would be a second answer to a question the agent's own
// record owns.
type ResumeMode string

const (
	// ResumeNone means the agent does not resume at all, and a restart opens
	// the pane in its checkout with a fresh conversation. It is a real
	// recorded state and not the absence of one: an agent that cannot resume
	// is a fact worth persisting, because it is what lets the restore say "this
	// one starts over" rather than "this one cannot be restored".
	ResumeNone ResumeMode = "none"
	// ResumeByID resumes by an explicit conversation id — `claude --resume
	// <id>`, `codex resume <id>` — which is what a shared checkout needs,
	// because a most-recent-session flag there would lasso another task's
	// conversation (nocx-2txuc).
	ResumeByID ResumeMode = "by-id"
	// ResumeByCwd resumes the agent's own most-recent session for the launch
	// directory, which is correct in a worktree because each tree has its own
	// directory (nocx-2txuc).
	ResumeByCwd ResumeMode = "by-cwd"
)

// ResumeIdentity is the identity one agent's conversation is continued under.
//
// An id with no mode is not a resumable conversation, and neither is a mode
// with no id: the first is a fact about the past and the second is a mode that
// cannot name anything. Complete is what the restore asks, so the rule lives
// once beside the values rather than at each reader.
type ResumeIdentity struct {
	// Mode is how the agent resumes.
	Mode ResumeMode
	// ID is the conversation id ResumeByID continues. Empty for every other
	// mode, and ignored there.
	ID string
}

// Complete reports whether this identity can name a conversation to continue.
// An unknown mode is incomplete rather than assumed: a record written by a
// newer build is not this build's to interpret.
func (r ResumeIdentity) Complete() bool {
	switch r.Mode {
	case ResumeByID:
		return r.ID != ""
	case ResumeByCwd:
		return true
	case ResumeNone:
		// Deliberately false, and this is the load-bearing part of the whole
		// method: an agent that does not resume has no conversation to
		// continue, so a restart cannot RESUME this pane. It may still open it
		// in its checkout, which is what RestoreNone answers — but calling that
		// a resume would be the claim this record refuses to make.
		return false
	default:
		return false
	}
}

// RestartRecord is the durable tuple for one worker pane: which pane it was,
// which agent ran there, where it was launched, and the identity its
// conversation continues under. Everything else about the participant is the
// live record's, and the pane's own identity is the layout chain's.
//
// It is written at MarkLive — the moment the record accepts the checkout's
// continued existence, which is the first instant all of these facts exist —
// and forgotten when the participant is closed or its registration is
// compensated.
type RestartRecord struct {
	// Participant is the worker's own id, which is what a coordinator is told
	// back and what a restore addresses the record by.
	Participant ParticipantID
	// Group and CoordinatorSession name the worker this participant was in.
	Group              ID
	CoordinatorSession string
	// PaneID and TabID are the durable layout identities. The record does not
	// resolve them and never derives one: they are what the chain minted, and
	// a restore reads the pane row rather than a copy of it.
	PaneID string
	TabID  string
	// Agent is the agent this pane was enrolled as — the name the enrolment
	// act carried, which is nocx's only evidence of what is running there
	// (ADR-0024 decision 2). Empty when the record was written before the
	// enrolment landed, and an empty agent is a restore failure rather than a
	// guess at which binary to launch.
	Agent string
	// Command and Environment are the launch as it was asked for, carried
	// rather than resolved: what makes an agent is the caller's business
	// (SpawnRequest.Command's own doc), and the launch record (nocx-dz9vj)
	// is what turns them into an argv.
	Command     string
	Environment string
	// Cwd is the directory the pane was launched in, which is the checkout's
	// path when the spawn made one and the coordinator's own directory when it
	// did not. It is recorded because the pane row's cwd is where a restore
	// REOPENS the pane and says nothing about what ran there.
	Cwd string
	// Worktree is the checkout the spawn made, as MarkLive accepted it, or the
	// zero value for every spawn that shared its coordinator's checkout.
	Worktree Worktree
	// Resume is the identity this pane's conversation continues under. Written
	// by whoever launched the agent and read by whoever restores it; nothing
	// in between invents a value for it.
	Resume ResumeIdentity
	// RecordedAt is when the record was written.
	RecordedAt time.Time
}

// RestoreRequest is what a persisted record reconstructs: everything this
// record holds about a launch, in one value.
//
// It is a type of its own rather than a SpawnRequest because the launch
// record (nocx-dz9vj) does not exist yet, so there is no argv to build and no
// resume arguments to carry: a SpawnRequest whose Worktree was the ASK and no
// way to express "resume this conversation" would advertise a launch nobody can
// perform. nocx-xn63t.5.2 turns this into the real request.
type RestoreRequest struct {
	Participant        ParticipantID
	Group              ID
	CoordinatorSession string
	PaneID             string
	TabID              string
	Agent              string
	Command            string
	Environment        string
	Cwd                string
	Worktree           Worktree
	Resume             ResumeIdentity
}

// Request is the launch this record reconstructs.
func (r RestartRecord) Request() RestoreRequest {
	return RestoreRequest{
		Participant:        r.Participant,
		Group:              r.Group,
		CoordinatorSession: r.CoordinatorSession,
		PaneID:             r.PaneID,
		TabID:              r.TabID,
		Agent:              r.Agent,
		Command:            r.Command,
		Environment:        r.Environment,
		Cwd:                r.Cwd,
		Worktree:           r.Worktree,
		Resume:             r.Resume,
	}
}

// RestoreReason is the closed vocabulary of why a persisted record cannot be
// restored. It is names and not sentences because a caller renders it — in a
// tab, in a log line, or in a test's failure — and one reason has one place
// deciding what it means.
type RestoreReason string

const (
	// RestoreCheckoutUnavailable is a record whose launch directory or
	// checkout is no longer on disk. The pane can still be reopened, but not
	// where its conversation was, and reopening it elsewhere would put a
	// worker in a checkout nobody chose.
	RestoreCheckoutUnavailable RestoreReason = "checkout-unavailable"
	// RestoreResumeUnavailable is a record whose agent is unknown or whose
	// resume identity names no conversation, so there is nothing to continue.
	RestoreResumeUnavailable RestoreReason = "resume-unavailable"
	// RestoreProbeMissing is a restore asked with no probe, which is nocx
	// admitting it cannot answer the question rather than guessing yes. It is
	// a distinct reason because a person reading the pane needs to know the
	// difference between "this cannot be restored" and "nocx did not look".
	RestoreProbeMissing RestoreReason = "probe-missing"
)

// RestoreFailure is why one record does not become a launch, in nocx's own
// words and beside the reason.
type RestoreFailure struct {
	Reason RestoreReason
	Detail string
}

// Restoration is what a startup restore says about one persisted record:
// either the launch it reconstructs, or the failure that replaces it. Never
// both, and never neither.
type Restoration struct {
	// Record is the record as it was persisted.
	Record RestartRecord
	// State is always StateInterrupted. A record read back after a restart
	// describes a process that is gone, and reporting it live would be a
	// claim about an incarnation this backend did not mint (Liveness's own
	// doc, AD-7).
	State State
	// Request is the launch this record reconstructs, or nil when Failure is
	// set.
	Request *RestoreRequest
	// Failure is why this record does not become a launch, or nil when
	// Request is set.
	Failure *RestoreFailure
}

// Restorable reports whether this record became a launch request.
func (x Restoration) Restorable() bool { return x.Failure == nil }

// RestartProbe answers the two questions a record cannot answer about itself:
// whether the place it was launched in still exists, and whether the identity
// it names is one nocx can act on.
//
// It is an interface because neither question belongs to this package. Whether
// a checkout is still on disk is the git seam's and the filesystem's answer;
// whether a conversation can still be resumed is the agent's own store's, and
// nocx does not own that (nocx-dz9vj). DiskProbe is what nocx can answer
// today, and it says so rather than reaching further.
type RestartProbe interface {
	// Checkout answers whether this launch directory is still usable. Both
	// facts are carried because a record may name either: the worktree's path
	// when the spawn made one, the cwd otherwise.
	Checkout(ctx context.Context, worktree Worktree, cwd string) error
	// Resume answers whether this agent and resume identity name a
	// conversation that can be continued.
	Resume(ctx context.Context, agent string, resume ResumeIdentity) error
}

// DiskProbe is the probe nocx ships: the checkout question is answered against
// the filesystem, and the resume question against the record's own
// completeness.
//
// IT IS NOT THE AGENT'S ANSWER, and the doc above says why. It can say a
// conversation is restorable when the agent's store will refuse it, which is
// why a relaunch that fails reports its own failure (nocx-xn63t.5.2) rather
// than treating this as permission. What it cannot do is claim more than it
// knows: an unknown agent and an incomplete resume identity are both
// refusals, so the honest answer and the pessimistic one coincide.
type DiskProbe struct{}

// Checkout answers whether the recorded launch directory is still a directory
// on this machine.
func (DiskProbe) Checkout(_ context.Context, worktree Worktree, cwd string) error {
	path := worktree.Path
	if path == "" {
		path = cwd
	}
	if path == "" {
		return errors.New("the record names no launch directory")
	}
	info, err := statDir(path)
	if err != nil {
		return fmt.Errorf("the launch directory %q is not there to reopen in: %w", path, err)
	}
	if !info {
		return fmt.Errorf("%q is not a directory any more", path)
	}
	return nil
}

// Resume answers whether this agent and identity name a conversation. It
// refuses an agent nocx never saw and an identity that names nothing, and it
// otherwise accepts: whether the agent's own store still holds the
// conversation is a question for the relaunch.
func (DiskProbe) Resume(_ context.Context, agent string, resume ResumeIdentity) error {
	if agent == "" {
		return errors.New("the record names no agent, so there is nothing to launch")
	}
	if !resume.Complete() {
		return fmt.Errorf("the record's resume identity (%q) names no conversation", resume.Mode)
	}
	return nil
}

// Restore is the one read of the restart record: every persisted record,
// classified. A nil probe is a refusal for every record rather than a
// permissive default — a restore that could not look must not claim the pane
// is fine.
func Restore(ctx context.Context, records []RestartRecord, probe RestartProbe) []Restoration {
	out := make([]Restoration, 0, len(records))
	for _, rec := range records {
		out = append(out, restoreOne(ctx, rec, probe))
	}
	return out
}

// restoreOne is the classification for one record, and the order of its
// questions is the order of what a person needs to hear: where it was, and
// then what it was.
func restoreOne(ctx context.Context, rec RestartRecord, probe RestartProbe) Restoration {
	// The state is settled before anything can fail, because it is the answer
	// in every case: whatever became of this record, the process it described
	// is interrupted.
	x := Restoration{Record: rec, State: StateInterrupted}
	if probe == nil {
		x.Failure = &RestoreFailure{
			Reason: RestoreProbeMissing,
			Detail: "this backend cannot tell whether the record can be restored",
		}
		return x
	}
	if err := probe.Checkout(ctx, rec.Worktree, rec.Cwd); err != nil {
		x.Failure = &RestoreFailure{Reason: RestoreCheckoutUnavailable, Detail: fmt.Sprint(err)}
		return x
	}
	if err := probe.Resume(ctx, rec.Agent, rec.Resume); err != nil {
		x.Failure = &RestoreFailure{Reason: RestoreResumeUnavailable, Detail: fmt.Sprint(err)}
		return x
	}
	request := rec.Request()
	x.Request = &request
	return x
}

// RestartIdentity is what the spawn side knows and the record cannot derive:
// which pane and tab the participant was minted in, which agent it enrolled
// as, and what it was launched with.
//
// It is the optional half of Spawned for the same reason WorktreeSource is: a
// launcher that made no pane and enrolled nothing says nothing, which is the
// zero RestartIdentity rather than an invented one.
type RestartIdentity struct {
	PaneID      string
	TabID       string
	Agent       string
	Command     string
	Environment string
	Cwd         string
	Resume      ResumeIdentity
}

// RestartIdentified is the optional interface a Spawned implements when it can
// say what a restart would need to resume this participant.
type RestartIdentified interface {
	RestartIdentity() RestartIdentity
}

// RestartRecorder is where the durable restart record is written and dropped.
// It is the Registrar's seam rather than a field on the Store, because the live
// store's whole subject is what is true right now and this record is not that
// (ADR-0079).
//
// Forget is not the undo of Record by symmetry only: a closed or compensated
// participant's record must go, or a later restart would relaunch a worker
// that ended on purpose. Forgetting a record that is not there is not an
// error, because the desired end state is its absence.
type RestartRecorder interface {
	Record(ctx context.Context, rec RestartRecord) error
	Forget(ctx context.Context, p ParticipantID) error
}

// RestartStore is the persistence seam for the restart record, read once at
// the startup restore and written at every MarkLive.
type RestartStore interface {
	RestartRecorder
	// Records is every persisted record, in a stable order (by participant
	// id) so two restores of one file agree.
	Records(ctx context.Context) ([]RestartRecord, error)
}

// restartDocumentVersion is this document's own schema version. Modules own
// their version numbers (ADR-0011 §6); there is no app-wide one.
const restartDocumentVersion = 1

// restartDocument is the on-disk shape. It carries no completeness claim and
// no derived field, because everything here is a fact somebody wrote and
// nothing is computed from the set.
type restartDocument struct {
	Version int             `json:"version"`
	Records []RestartRecord `json:"records"`
}

// FileRestartStore keeps the restart record in one atomic JSON document under
// the profile directory, readable and repairable by hand — the same shape
// agentapproval's store and the settings documents use, and the reason a person
// whose worker did not come back can be told what the file says.
type FileRestartStore struct {
	doc  storage.DocumentStore
	name string

	mu      sync.Mutex
	loaded  bool
	records map[ParticipantID]RestartRecord
}

// NewFileRestartStore roots a store at a document the caller owns — in the
// product, the profile's config directory.
func NewFileRestartStore(doc storage.DocumentStore, name string) *FileRestartStore {
	return &FileRestartStore{doc: doc, name: name, records: map[ParticipantID]RestartRecord{}}
}

var _ RestartStore = (*FileRestartStore)(nil)

// Record writes one participant's record, replacing whatever was there for
// that participant: a second MarkLive of the same id is the same record, and
// leaving the first would be two rows for one pane.
//
// The in-memory copy changes only after the document write succeeds, so a
// failed write never authorizes a restore this process cannot honour.
func (s *FileRestartStore) Record(ctx context.Context, rec RestartRecord) error {
	if rec.Participant == "" {
		return errors.New("worker: a restart record needs a participant id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	previous, existed := s.records[rec.Participant]
	s.records[rec.Participant] = rec
	if err := s.writeLocked(); err != nil {
		if existed {
			s.records[rec.Participant] = previous
		} else {
			delete(s.records, rec.Participant)
		}
		return err
	}
	return nil
}

// Forget drops one participant's record. A participant the document does not
// carry is already in the desired end state.
func (s *FileRestartStore) Forget(ctx context.Context, p ParticipantID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	if _, held := s.records[p]; !held {
		return nil
	}
	previous := s.records[p]
	delete(s.records, p)
	if err := s.writeLocked(); err != nil {
		s.records[p] = previous
		return err
	}
	return nil
}

// Records is every persisted record, in participant order.
func (s *FileRestartStore) Records(ctx context.Context) ([]RestartRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	out := make([]RestartRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Participant < out[j].Participant })
	return out, nil
}

// loadLocked reads the document once per process. A missing, unreadable,
// malformed or version-unknown document is an empty store: there is nothing
// older to be compatible with, and a record set nocx cannot read is a set it
// must not relaunch from.
func (s *FileRestartStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	var raw restartDocument
	found, err := s.doc.Read(s.name, &raw)
	if err != nil {
		return fmt.Errorf("worker: read the restart record document: %w", err)
	}
	if found {
		if raw.Version != restartDocumentVersion {
			return fmt.Errorf(
				"worker: the restart record document is version %d and this build reads version %d",
				raw.Version, restartDocumentVersion)
		}
		for _, rec := range raw.Records {
			if rec.Participant == "" {
				continue
			}
			s.records[rec.Participant] = rec
		}
	}
	s.loaded = true
	return nil
}

// writeLocked writes the whole set in one atomic document write, under the
// store's own mutex: a record set is read-modify-write, so two concurrent
// spawns would otherwise lose one's row.
func (s *FileRestartStore) writeLocked() error {
	doc := restartDocument{Version: restartDocumentVersion, Records: make([]RestartRecord, 0, len(s.records))}
	for _, rec := range s.records {
		doc.Records = append(doc.Records, rec)
	}
	sort.Slice(doc.Records, func(i, j int) bool {
		return doc.Records[i].Participant < doc.Records[j].Participant
	})
	if err := s.doc.Write(s.name, doc); err != nil {
		return fmt.Errorf("worker: write the restart record document: %w", err)
	}
	return nil
}

// statDir answers whether path names a directory on this machine, which is the
// question the checkout probe asks and the only filesystem fact this package
// reads.
func statDir(path string) (bool, error) {
	info, err := os.Stat(path) //nolint:gosec // a path this package's own record persisted
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}
