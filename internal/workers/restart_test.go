package workers

// The restart record (ADR-0079, nocx-xn63t.5.1): the durable tuple that lets a
// worker's pane be found again after the backend that spawned it is gone, and
// the classification a startup restore makes of it.
//
// The record's whole reason to exist is the sentence in ADR-0079's context: an
// agent's conversation store lives on disk and survives a reboot, so a resume
// is a LAUNCH against a recorded identity rather than a reattach to something
// still running. So the tests here are about what a restart can reconstruct,
// and about what it must refuse to pretend.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

// A whole document store a test owns — the reopen in these tests is a second
// store over the same directory, which is what a backend restart against the
// same app directory is at this layer.
func restartDocs(t *testing.T) storage.DocumentStore {
	t.Helper()
	return storage.NewDocumentStore(t.TempDir())
}

// A recorded worker, in the shape the spawn side writes: a pane, a worktree,
// and a conversation it continues by id.
func recordedWorker(dir string) RestartRecord {
	return RestartRecord{
		Participant:        ParticipantID("p-restart"),
		Group:              testGroup,
		CoordinatorSession: coordSession,
		PaneID:             "0192f0c0-0000-7000-8000-00000000pane",
		TabID:              "0192f0c0-0000-7000-8000-00000000tab",
		Agent:              "claude",
		Command:            "claude",
		Environment:        "env-local",
		Cwd:                dir,
		Worktree:           Worktree{Path: dir, Branch: "feat/one", Base: "4f2a1c9b"},
		Resume:             ResumeIdentity{Mode: ResumeByID, ID: "conv-9f3a"},
		RecordedAt:         time.Unix(1_700_000_000, 0).UTC(),
	}
}

// Acceptance 1: close the backend and reopen it against the same app
// directory, and the whole tuple resolves — pane, tab, agent, launch
// directory, worktree and resume identity. One assertion over the complete
// value, because a record that kept three of the five facts would answer this
// test with the three that happen to match.
func TestTheRestartRecordSurvivesAReopenAndResolvesTheWholeTuple(t *testing.T) {
	ctx := context.Background()
	docs := restartDocs(t)
	dir := t.TempDir()
	want := recordedWorker(dir)

	before := NewFileRestartStore(docs, "worker-restarts.json")
	if err := before.Record(ctx, want); err != nil {
		t.Fatalf("record: %v", err)
	}

	// The reopen: a second store over the same document, which is what a
	// restarted backend builds. Nothing is carried in memory between them.
	after := NewFileRestartStore(docs, "worker-restarts.json")
	got, err := after.Records(ctx)
	if err != nil {
		t.Fatalf("records after reopen: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records after reopen = %d, want exactly the one written", len(got))
	}
	if got[0] != want {
		t.Fatalf("record after reopen =\n%+v\nwant\n%+v", got[0], want)
	}
}

// The tuple is only worth anything if it comes back as a LAUNCH: acceptance 2's
// second half, that a valid persisted record normally reconstructs the
// expected launch request.
func TestAValidRecordReconstructsTheLaunchRequest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	want := recordedWorker(dir)

	got := Restore(ctx, []RestartRecord{want}, DiskProbe{})
	if len(got) != 1 {
		t.Fatalf("restorations = %d, want one", len(got))
	}
	x := got[0]
	if !x.Restorable() {
		t.Fatalf("a complete record in a live checkout was refused: %+v", x.Failure)
	}
	if x.Request == nil {
		t.Fatalf("a restorable record carried no launch request")
	}
	wantRequest := want.Request()
	if *x.Request != wantRequest {
		t.Fatalf("launch request =\n%+v\nwant\n%+v", *x.Request, wantRequest)
	}
	// Every field of the tuple is IN the request, which is the point of the
	// record: pane to reopen, checkout to reopen it in, identity to continue.
	if x.Request.PaneID != want.PaneID || x.Request.Worktree.Path != want.Worktree.Path ||
		x.Request.Resume != want.Resume || x.Request.Agent != want.Agent {
		t.Fatalf("the reconstructed launch lost part of the tuple: %+v", x.Request)
	}
}

// A record read back after a restart describes a process that is gone. Nothing
// may report it live, whatever the record says about resume: liveness is bound
// to the backend instance that minted it (AD-7) and that instance is gone.
func TestARecordReadBackIsInterruptedAndNeverLive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	for _, rec := range []RestartRecord{
		recordedWorker(dir),
		func() RestartRecord {
			r := recordedWorker(dir)
			r.Resume = ResumeIdentity{Mode: ResumeNone}
			return r
		}(),
	} {
		x := Restore(ctx, []RestartRecord{rec}, DiskProbe{})[0]
		if x.State != StateInterrupted {
			t.Fatalf("state = %q, want %q: a dead process is not a live participant", x.State, StateInterrupted)
		}
		if x.State == StateLive {
			t.Fatalf("a persisted record was reported live")
		}
	}
}

// Acceptance 2's first half: a record whose checkout is gone, and one whose
// resume identity names no conversation, are explicit restore failures. Not
// empty launches, and never "live" — a pane that cannot resume says so.
func TestARecordThatCannotBeRestoredIsAnExplicitFailure(t *testing.T) {
	ctx := context.Background()
	live := t.TempDir()

	goneCheckout := recordedWorker(filepath.Join(t.TempDir(), "removed"))
	goneCheckout.Cwd = "/nowhere/at/all"
	goneCheckout.Worktree = Worktree{Path: "/nowhere/at/all", Branch: "feat/one", Base: "4f2a1c9b"}

	noResume := recordedWorker(live)
	noResume.Resume = ResumeIdentity{}

	unknownAgent := recordedWorker(live)
	unknownAgent.Agent = ""

	noProbe := Restore(ctx, []RestartRecord{recordedWorker(live)}, nil)[0]

	got := Restore(ctx, []RestartRecord{goneCheckout, noResume, unknownAgent, recordedWorker(live)}, DiskProbe{})
	if len(got) != 4 {
		t.Fatalf("restorations = %d, want one per record", len(got))
	}

	for _, want := range []struct {
		reason RestoreReason
		detail string
	}{
		{RestoreCheckoutUnavailable, "/nowhere/at/all"},
		{RestoreResumeUnavailable, ""},
		{RestoreResumeUnavailable, "no agent"},
	} {
		x := got[0]
		got = got[1:]
		if x.Restorable() {
			t.Fatalf("a record that cannot be restored produced a launch: %+v", x.Request)
		}
		if x.Request != nil {
			t.Fatalf("a failed restore carried a launch request beside its failure")
		}
		if x.Failure.Reason != want.reason {
			t.Fatalf("failure reason = %q, want %q (%s)", x.Failure.Reason, want.reason, x.Failure.Detail)
		}
		if want.detail != "" && !strings.Contains(x.Failure.Detail, want.detail) {
			t.Fatalf("failure detail = %q, want it to name %q", x.Failure.Detail, want.detail)
		}
	}

	last := got[0]
	if !last.Restorable() {
		t.Fatalf("the one good record was refused too: %+v", last.Failure)
	}
	// A restore asked with no probe says so rather than claiming the pane is
	// fine — the difference between "cannot be restored" and "nocx did not
	// look" is the one a person reading the pane needs.
	if noProbe.Failure == nil || noProbe.Failure.Reason != RestoreProbeMissing {
		t.Fatalf("restore with no probe = %+v, want a probe-missing failure", noProbe.Failure)
	}
	if noProbe.Restorable() {
		t.Fatalf("a restore that could not look reported the record restorable")
	}
}

// An agent that does not resume is a RECORDED state, not an absent one — and
// it is still not a resume. The distinction is what lets the restore say "this
// one starts over" instead of "this one cannot be restored", and the second
// half is why Complete answers false for it.
func TestAnAgentThatDoesNotResumeIsRecordedAsSuchAndIsNotAResume(t *testing.T) {
	if (ResumeIdentity{Mode: ResumeNone}).Complete() {
		t.Fatalf("ResumeNone reported itself a resumable conversation")
	}
	if !(ResumeIdentity{Mode: ResumeByCwd}).Complete() {
		t.Fatalf("a by-cwd resume in a worktree reported itself incomplete")
	}
	if (ResumeIdentity{Mode: ResumeByID}).Complete() {
		t.Fatalf("a by-id resume with no id reported itself complete")
	}
	if (ResumeIdentity{Mode: "by-something-new"}).Complete() {
		t.Fatalf("an unknown resume mode was interpreted rather than refused")
	}

	ctx := context.Background()
	dir := t.TempDir()
	rec := recordedWorker(dir)
	rec.Resume = ResumeIdentity{Mode: ResumeNone}
	got := Restore(ctx, []RestartRecord{rec}, DiskProbe{})[0]
	if got.Failure == nil || got.Failure.Reason != RestoreResumeUnavailable {
		t.Fatalf("a non-resuming agent = %+v, want a resume-unavailable failure", got.Failure)
	}
	if !strings.Contains(got.Failure.Detail, string(ResumeNone)) {
		t.Fatalf("failure detail = %q, want it to name the recorded mode", got.Failure.Detail)
	}
}

// The record's interval: it exists from MarkLive — the first moment the pane,
// the checkout and the agent all exist — until the participant is closed or its
// registration is compensated. A record outliving either would relaunch a
// worker that ended on purpose.
func TestTheRecordIsWrittenAtMarkLiveAndDroppedWhenTheParticipantEnds(t *testing.T) {
	ctx := context.Background()
	docs := restartDocs(t)
	store := NewFileRestartStore(docs, "worker-restarts.json")

	h := newHarnessBound(t, 2, WithRestartRecords(store))
	h.spawn.wt = Worktree{Path: "/data/worktrees/repo-a1b2c3d4/feat-one", Branch: "feat/one", Base: "4f2a1c9b"}
	h.spawn.ident = RestartIdentity{
		PaneID:      "pane-1",
		TabID:       "tab-1",
		Agent:       "claude",
		Command:     "claude",
		Environment: "env-local",
		Cwd:         "/data/worktrees/repo-a1b2c3d4/feat-one",
		Resume:      ResumeIdentity{Mode: ResumeByID, ID: "conv-1"},
	}

	// Before the fork there is nothing to record, and the record for a
	// participant that never went live would outlive its own compensation.
	atFork, err := store.Records(ctx)
	if err != nil {
		t.Fatalf("records before: %v", err)
	}
	if len(atFork) != 0 {
		t.Fatalf("records before any spawn = %+v, want none", atFork)
	}

	reg, err := h.reg.Register(ctx, RegisterRequest{
		Group:              testGroup,
		CoordinatorSession: coordSession,
		Role:               RoleWorker,
		Task:               "read AGENTS.md and report",
		Command:            "claude",
		Environment:        "env-local",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	p := reg.Participant
	got, err := store.Records(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records after the spawn = %+v, want the one that went live", got)
	}
	rec := got[0]
	if rec.Participant != p.ID || rec.PaneID != "pane-1" || rec.TabID != "tab-1" ||
		rec.Agent != "claude" || rec.Worktree != h.spawn.wt || rec.Resume != h.spawn.ident.Resume ||
		rec.Group != testGroup || rec.CoordinatorSession != coordSession ||
		rec.Command != "claude" || rec.Environment != "env-local" {
		t.Fatalf("the written record lost the tuple: %+v", rec)
	}

	// A close ends the worker, so its record goes with it.
	h.reg.closer = closedOnce{}
	if _, closeErr := h.reg.Close(ctx, coordSession, p.ID); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	got, err = store.Records(ctx)
	if err != nil {
		t.Fatalf("records after the close: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("records after a close = %+v, want none: a closed worker must not come back", got)
	}
}

// The other end of the interval. A registration that failed after the checkout
// was accepted leaves nothing behind, and a record that survived it would
// relaunch a worker this backend compensated.
func TestACompensatedRegistrationLeavesNoRecord(t *testing.T) {
	ctx := context.Background()
	store := NewFileRestartStore(restartDocs(t), "worker-restarts.json")

	h := newHarnessBound(t, 2, WithRestartRecords(store))
	// The failure lands after MarkLive and after the record was written, which
	// is the only window in which forgetting it is the compensation's job.
	h.spawn.ident = RestartIdentity{PaneID: "pane-1", Agent: "claude", Resume: ResumeIdentity{Mode: ResumeByID, ID: "conv-1"}}
	h.sup.failOn = 1

	if _, err := h.register(ctx); err == nil {
		t.Fatalf("a registration whose supervision attach failed reported success")
	}
	got, err := store.Records(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("records after a compensated registration = %+v, want none", got)
	}
}

// A store whose document cannot be read is a store that holds nothing, and
// that refusal is reported rather than swallowed: a restore from an unreadable
// record set would relaunch from whatever the in-memory copy happened to hold.
func TestAnUnreadableDocumentRefusesRatherThanRestoringEmpty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	docs := storage.NewDocumentStore(dir)
	if err := docs.Write("worker-restarts.json", map[string]any{"version": 99}); err != nil {
		t.Fatalf("seed the document: %v", err)
	}
	store := NewFileRestartStore(docs, "worker-restarts.json")
	if _, err := store.Records(ctx); err == nil {
		t.Fatalf("a document of an unknown version read as empty")
	}

	// And a malformed one, which is what a half-written file looks like to a
	// person editing it by hand.
	bad := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write the malformed document: %v", err)
	}
	if _, err := NewFileRestartStore(storage.NewDocumentStore(dir), "malformed.json").Records(ctx); err == nil {
		t.Fatalf("a malformed document read as empty")
	}
}

// Two records, two documents, one set: the store is read-modify-write under its
// own lock, and a second write must not lose the first participant's row.
func TestARecordIsReplacedForOneParticipantAndNeverDuplicated(t *testing.T) {
	ctx := context.Background()
	store := NewFileRestartStore(restartDocs(t), "worker-restarts.json")
	dir := t.TempDir()

	first := recordedWorker(dir)
	if err := store.Record(ctx, first); err != nil {
		t.Fatalf("record: %v", err)
	}
	second := recordedWorker(dir)
	second.Participant = ParticipantID("p-restart-2")
	if err := store.Record(ctx, second); err != nil {
		t.Fatalf("record second: %v", err)
	}

	updated := first
	updated.Resume = ResumeIdentity{Mode: ResumeByID, ID: "conv-later"}
	if err := store.Record(ctx, updated); err != nil {
		t.Fatalf("record update: %v", err)
	}

	got, err := store.Records(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("records = %+v, want two participants and no duplicate", got)
	}
	byID := map[ParticipantID]RestartRecord{}
	for _, rec := range got {
		byID[rec.Participant] = rec
	}
	if byID[first.Participant].Resume.ID != "conv-later" {
		t.Fatalf("the update did not replace the record: %+v", byID[first.Participant])
	}
	if byID[second.Participant].Resume.ID != second.Resume.ID {
		t.Fatalf("the second participant's record was disturbed: %+v", byID[second.Participant])
	}
	// Stable order, so two restores of one file agree.
	if got[0].Participant > got[1].Participant {
		t.Fatalf("records came back unordered: %+v", got)
	}
}

// closedOnce is a Closer that ends nothing and answers an empty result: this
// test is about what the record does, not about the close's own work.
type closedOnce struct{}

func (closedOnce) Close(context.Context, Participant) (CloseResult, error) {
	return CloseResult{}, nil
}
