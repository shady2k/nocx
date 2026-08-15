package lifecycle

import (
	"errors"
	"testing"
	"time"
)

func TestPendingDomainLaneStartsNative(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	if err := k.BindTransport("T", p); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RequestDomain("L", nil, "T"); err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}

	st := mustState(t, k, "L")
	if st.Lifecycle != LifecycleNative {
		t.Fatalf("pending domain lane lifecycle = %v, want Native", st.Lifecycle)
	}
}

// --- sequence and replay (decision 7) ---------------------------------------

func TestSequenceDuplicateAndDecreasingRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	// seq 2 accepted, then the same seq 2 replayed, then a decreasing 1.
	mustIngest(t, k, "T", env("L", h, 2, startEvt(nil, "ls")))
	att, _ := k.OpenAttempt(h.Domain)
	if _, err := k.Ingest("T", env("L", h, 2, startEvt(nil, "ls"))); !errors.Is(err, ErrSequenceReplay) {
		t.Fatalf("duplicate seq must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 1, promptReadyEvt())); !errors.Is(err, ErrSequenceReplay) {
		t.Fatalf("decreasing seq must be rejected, got %v", err)
	}
	// The attempt is still open: the rejected frames mutated nothing.
	if _, ok := k.Attempt(att.ID); !ok {
		t.Fatal("attempt must survive rejected frames")
	}
}

func TestSequenceStateMutatesOnlyAfterAuthentication(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	// A wrong-capability frame with a high sequence is rejected and must
	// not advance the counter: the same sequence is then accepted with the
	// right capability.
	wrong := h.Capability
	wrong[0] ^= 0xFF
	if _, err := k.Ingest("T", envRaw("L", h.Domain, h.Epoch, wrong, 999, startEvt(nil, "evil"))); !errors.Is(err, ErrBadCapability) {
		t.Fatalf("wrong capability must be rejected, got %v", err)
	}
	mustIngest(t, k, "T", env("L", h, 2, startEvt(nil, "ok")))
	att, ok := k.OpenAttempt(h.Domain)
	if !ok || att.Command != "ok" {
		t.Fatalf("counter must not have advanced past the rejected frame, got %+v", att)
	}
}

func TestReconnectNeverResetsCounterWithinEpoch(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	mustIngest(t, k, "T", env("L", h, 2, startEvt(nil, "a")))
	att, _ := k.OpenAttempt(h.Domain)

	// The shell's connection drops and reconnects: a hello with the same
	// epoch and capability is a reconnect, answered with accept, and the
	// counter continues from 3 — a replayed 2 is still rejected.
	p.reset()
	mustIngest(t, k, "T", env("L", h, 3, helloEvt("bash")))
	mustAccept(t, p)
	if _, err := k.Ingest("T", env("L", h, 2, promptReadyEvt())); !errors.Is(err, ErrSequenceReplay) {
		t.Fatalf("reconnect must not reset the counter, got %v", err)
	}
	mustIngest(t, k, "T", env("L", h, 4, completeEvt(att.ID, 0, fence(0xAA))))
	if got, _ := k.Attempt(att.ID); got.State != AttemptCompleted {
		t.Fatalf("attempt must complete after reconnect, got %v", got.State)
	}
}

// --- loss and abandonment (decisions 8, 5) -----------------------------------

func TestAttemptUnknownOnTransportLossNeverSuccessful(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "long job", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "long job")))

	if err := k.TransportLost("T"); err != nil {
		t.Fatal(err)
	}
	got, ok := k.Attempt(att.ID)
	if !ok || got.State != AttemptUnknown {
		t.Fatalf("open attempt must become unknown on loss, got %+v", got)
	}
	if got.ExitCode != nil {
		t.Fatalf("loss must never assign an exit code, got %d", *got.ExitCode)
	}
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleLost {
		t.Fatalf("lane must be Lost, got %v", st.Lifecycle)
	}
}

func TestDomainClosedUnknownsOpenAttempts(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "sudo ls", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "sudo ls")))
	mustIngest(t, k, "T", env("L", h, 3, closeEvt()))
	if got, _ := k.Attempt(att.ID); got.State != AttemptUnknown {
		t.Fatalf("closing a domain must unknown its open attempt, got %v", got.State)
	}
}

func TestAbandonAttempt(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "thing", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "thing")))
	if err := k.AbandonAttempt(att.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.Attempt(att.ID); got.State != AttemptUnknown || got.ExitCode != nil {
		t.Fatalf("abandoned attempt must be unknown with no exit code, got %+v", got)
	}
	// A completion for an abandoned attempt is rejected.
	if _, err := k.Ingest("T", env("L", h, 3, completeEvt(att.ID, 0, fence(0xBB)))); !errors.Is(err, ErrAttemptNotOpen) {
		t.Fatalf("completion of an abandoned attempt must be rejected, got %v", err)
	}
}

// --- desynchronization and the snapshot (decision 7) -------------------------

func TestGapDesynchronizesAndOnlySnapshotRestores(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "make", "/work", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "make")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 512, 3)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	// The kernel demanded a snapshot.
	kinds := p.kinds()
	if len(kinds) != 1 || kinds[0] != KindRefreshRequest {
		t.Fatalf("desync must emit exactly one refresh_request, got %v", kinds)
	}
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	st := mustState(t, k, "L")
	if st.Lifecycle != LifecycleDesynchronized || st.Domain != h.Domain {
		t.Fatalf("lane must be Desynchronized, got %+v", st)
	}

	// Ordinary lifecycle events are quarantined: rejected, nothing mutated.
	if _, err := k.Ingest("T", env("L", h, 3, promptReadyEvt())); !errors.Is(err, ErrDomainDesynchronized) {
		t.Fatalf("events while desynced must be quarantined, got %v", err)
	}
	// A snapshot answering the wrong request is rejected.
	if _, err := k.Ingest("T", env("L", h, 4, snapshotEvt("req-other", ShellRunning, &att.ID, nil, 5))); !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("snapshot answering another request must be rejected, got %v", err)
	}
	// The real answer restores authority and keeps the open attempt running.
	mustIngest(t, k, "T", env("L", h, 5, snapshotEvt(rid, ShellRunning, &att.ID, nil, 6)))
	st = mustState(t, k, "L")
	assertState(t, st, LifecycleRunning, h.Domain, att.ID, []DomainID{h.Domain})
	if got, _ := k.Attempt(att.ID); got.State != AttemptOpen {
		t.Fatalf("snapshot-named active attempt must stay open, got %v", got.State)
	}
	mustIngest(t, k, "T", env("L", h, 6, completeEvt(att.ID, 1, fence(0xCC))))
	if got, _ := k.Attempt(att.ID); got.State != AttemptCompleted || *got.ExitCode != 1 {
		t.Fatalf("attempt must complete after resync, got %+v", got)
	}
}

func TestSnapshotReconcilesOpenAttemptAsUnknown(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "lost in gap", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "lost in gap")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	// The shell is at a prompt with no active attempt and no completion for
	// ours: the open attempt must become unknown, never successful.
	last := &CompletedRef{AttemptID: "att-other", ExitCode: intPtr(0)}
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4)))
	if got, _ := k.Attempt(att.ID); got.State != AttemptUnknown || got.ExitCode != nil {
		t.Fatalf("unrecoverable attempt must be unknown with no exit code, got %+v", got)
	}
	st := mustState(t, k, "L")
	assertState(t, st, LifecyclePromptReady, h.Domain, "", []DomainID{h.Domain})
}

func TestSnapshotCreatesShellOriginatedActiveAttempt(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	// The gap swallowed the Start; the snapshot names the running attempt.
	sid := AttemptID("att-shell-1")
	mustIngest(t, k, "T", env("L", h, 2, snapshotEvt(rid, ShellRunning, &sid, nil, 3)))
	st := mustState(t, k, "L")
	assertState(t, st, LifecycleRunning, h.Domain, sid, []DomainID{h.Domain})
	if got, ok := k.Attempt(sid); !ok || got.Origin != OriginShell || got.State != AttemptOpen {
		t.Fatalf("snapshot-declared attempt must exist shell-originated and open, got %+v", got)
	}
}

func TestSnapshotContradictionsRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "x", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "x")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	// Active and last-completed naming the same attempt: contradiction.
	if _, err := k.Ingest("T", env("L", h, 3, snapshotEvt(rid, ShellRunning, &att.ID, &CompletedRef{AttemptID: att.ID, ExitCode: intPtr(0)}, 4))); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("contradictory snapshot must be rejected, got %v", err)
	}
	// A snapshot with a next sequence that does not advance is rejected.
	if _, err := k.Ingest("T", env("L", h, 4, snapshotEvt(rid, ShellRunning, &att.ID, nil, 3))); !errors.Is(err, ErrSnapshotSequence) {
		t.Fatalf("non-advancing snapshot must be rejected, got %v", err)
	}
	// A snapshot answering a different request id is rejected.
	if _, err := k.Ingest("T", env("L", h, 5, snapshotEvt("req-none", ShellRunning, &att.ID, nil, 6))); !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("snapshot answering another request must be rejected, got %v", err)
	}
	// The domain is still desynchronized after all the rejections.
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleDesynchronized {
		t.Fatalf("rejections must not restore authority, got %v", st.Lifecycle)
	}
}

func TestSnapshotValidationPrecedesMutation(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	// A foreign-domain attempt on a second lane.
	hO := establish(t, k, "T", p, "L2", nil)
	foreign, err := k.SubmitAttempt(hO.Domain, "other", "/", "other")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, k, "T", env("L2", hO, 2, startEvt(&foreign.ID, "other")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID

	// The snapshot names an unknown active attempt (which the apply phase
	// would create) and a last-completed attempt from a foreign domain:
	// the whole envelope must be rejected before anything mutates.
	ghost := AttemptID("att-ghost")
	last := &CompletedRef{AttemptID: foreign.ID, ExitCode: intPtr(0)}
	if _, err := k.Ingest("T", env("L", h, 3, snapshotEvt(rid, ShellRunning, &ghost, last, 4))); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("foreign last-completed must be rejected, got %v", err)
	}
	if _, exists := k.Attempt(ghost); exists {
		t.Fatal("rejected snapshot must not have created the unknown active attempt")
	}
	if got, _ := k.Attempt(foreign.ID); got.State != AttemptOpen {
		t.Fatalf("rejected snapshot must not have completed the foreign attempt, got %v", got.State)
	}
	// The domain stays desynchronized: the rejection restored nothing.
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleDesynchronized {
		t.Fatalf("rejection must not restore authority, got %v", st.Lifecycle)
	}
}

// The payoff (brief criterion 2): a completion lost inside the gap is
// recovered by the snapshot when the shell names the attempt it just
// completed — the shell's own id, resolved through the alias recorded at
// attach. The exit status the user sees is the real one.
func TestSnapshotRecoversLostCompletionViaShellAlias(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "make", "/work", "local")
	shellID := AttemptID("s-7-0")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&shellID, "make"))) // attach + alias

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	// The complete (exit 2) was swallowed by the gap; the shell reports the
	// attempt it just finished under its own id with the real status.
	last := &CompletedRef{AttemptID: shellID, ExitCode: intPtr(2)}
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4)))
	got, ok := k.Attempt(att.ID)
	if !ok || got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatalf("lost completion must reconcile to its real status via the shell alias, got %+v", got)
	}
	if st := mustState(t, k, "L"); st.Lifecycle != LifecyclePromptReady {
		t.Fatalf("lane must return to a ready prompt, got %v", st.Lifecycle)
	}
}

// The safety direction of the same rule: the shell reports a completion for
// an id that does not resolve to the domain's open attempt (a later command
// completed in the gap, or the complete was for something the kernel never
// saw). The open attempt reconciles to unknown and no exit code is invented.
func TestSnapshotUnknownShellIDNeverInventsSuccess(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "make", "/work", "local")
	shellID := AttemptID("s-7-0")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&shellID, "make")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	// A second command's id: it never attached, so the snapshot cannot
	// connect it to the open attempt.
	last := &CompletedRef{AttemptID: "s-8-0", ExitCode: intPtr(0)}
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4)))
	if got, _ := k.Attempt(att.ID); got.State != AttemptUnknown || got.ExitCode != nil {
		t.Fatalf("unconnected completion must reconcile unknown with no code, got %+v", got)
	}
}

// active_attempt resolves through the alias too: the shell reports the
// running attempt under its own id, and it stays open under the app id —
// the identity the lane and the published facts expose (constraint b).
func TestSnapshotActiveAttemptResolvesViaShellAlias(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "make", "/work", "local")
	shellID := AttemptID("s-7-0")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&shellID, "make")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellRunning, &shellID, nil, 4)))
	st := mustState(t, k, "L")
	assertState(t, st, LifecycleRunning, h.Domain, att.ID, []DomainID{h.Domain})
	if got, _ := k.Attempt(att.ID); got.State != AttemptOpen {
		t.Fatalf("snapshot-named active attempt must stay open under the app id, got %+v", got)
	}
}

// Constraint (a), second direction: a snapshot can name an alias the kernel
// already learned but never create one. An unknown active_attempt is created
// shell-originated with that id — the existing rule — and carries no alias,
// and an app attempt the snapshot could not connect to still reconciles
// unknown rather than attaching by position.
func TestSnapshotNeverCreatesAnAlias(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	// An app attempt whose start was lost entirely: no alias was recorded.
	att, _ := k.SubmitAttempt(h.Domain, "x", "/", "local")

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	ghost := AttemptID("s-9-0")
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellRunning, &ghost, nil, 4)))
	if got, _ := k.Attempt(ghost); got.State != AttemptOpen || got.shellID != "" {
		t.Fatalf("snapshot-created attempt must carry no alias, got %+v", got)
	}
	if got, _ := k.Attempt(att.ID); got.State != AttemptUnknown {
		t.Fatalf("app attempt with no alias must reconcile unknown, got %v", got.State)
	}
}

// Constraint (c): aliases are per-domain. Two domains can mint the same
// shell id (independent shells); each domain's snapshot resolves only its
// own alias, and never touches the other domain's attempt.
func TestSnapshotAliasResolutionIsPerDomain(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "LA", nil)
	hB := establish(t, k, "T", p, "LB", nil)
	shared := AttemptID("s-1-0")
	attA, _ := k.SubmitAttempt(hA.Domain, "a", "/", "local")
	mustIngest(t, k, "T", env("LA", hA, 2, startEvt(&shared, "a")))
	attB, _ := k.SubmitAttempt(hB.Domain, "b", "/", "local")
	mustIngest(t, k, "T", env("LB", hB, 2, startEvt(&shared, "b")))

	p.reset()
	outs, err := k.NotifyGap("T", hA.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	last := &CompletedRef{AttemptID: shared, ExitCode: intPtr(3)}
	mustIngest(t, k, "T", env("LA", hA, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4)))
	if got, _ := k.Attempt(attA.ID); got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("domain A's snapshot must resolve its own alias, got %+v", got)
	}
	if got, _ := k.Attempt(attB.ID); got.State != AttemptOpen {
		t.Fatalf("domain B's attempt must be untouched by A's snapshot, got %v", got.State)
	}
}

// Constraint (e), stated as an interval with both ends named: from the
// moment the alias is recorded — the authenticated start's acceptance — until
// the domain ends, the shell id resolves to the app attempt; after the domain
// ends the resolution gate is the domain-state check itself, so no snapshot
// can reach the alias at all.
func TestShellAliasLivesUntilTheDomainEnds(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "make", "/", "local")
	shellID := AttemptID("s-4-0")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&shellID, "make"))) // interval opens
	if got, _ := k.Attempt(att.ID); got.shellID != shellID {
		t.Fatalf("alias must be recorded at the authenticated start, got %q", got.shellID)
	}

	// Still within the domain's life: the alias resolves through a desync.
	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	last := &CompletedRef{AttemptID: shellID, ExitCode: intPtr(1)}
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4)))
	if got, _ := k.Attempt(att.ID); got.State != AttemptCompleted || got.shellID != shellID {
		t.Fatalf("alias must resolve and persist on the record, got %+v", got)
	}

	// The domain ends: its alias dies with it — no snapshot can reach it.
	mustIngest(t, k, "T", env("L", h, 4, closeEvt()))
	if _, err := k.Ingest("T", env("L", h, 5, snapshotEvt(rid, ShellAtPrompt, nil, last, 6))); !errors.Is(err, ErrSnapshotUnexpected) {
		t.Fatalf("snapshot for a closed domain must be rejected, got %v", err)
	}
}

// Brief defect 1, second face: a snapshot from domain B naming B's OWN id
// resolves to B's attempt while an identically-numbered attempt (same
// per-shell counter, different domain) exists under domain A — and does not
// produce ErrSnapshotConflict. With the mint carrying the domain the ids
// differ by construction; before that fix both domains minted s-$$-0 and the
// exact-match arm of resolveAttempt resolved cross-domain into a rejection.
func TestSnapshotResolvesOwnDomainIDWithSameCounterElsewhere(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "LA", nil)
	hB := establish(t, k, "T", p, "LB", nil)
	// Both shells at counter 0: distinct ids, identical numbers.
	idA := AttemptID("s-" + string(hA.Domain) + "-0")
	idB := AttemptID("s-" + string(hB.Domain) + "-0")
	mustIngest(t, k, "T", env("LA", hA, 2, startEvt(&idA, "a")))
	mustIngest(t, k, "T", env("LA", hA, 3, completeEvt(idA, 0, fence(0x01))))
	mustIngest(t, k, "T", env("LA", hA, 4, promptReadyEvt()))
	mustIngest(t, k, "T", env("LB", hB, 2, startEvt(&idB, "b")))

	p.reset()
	outs, err := k.NotifyGap("T", hB.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	last := &CompletedRef{AttemptID: idB, ExitCode: intPtr(2)}
	mustIngest(t, k, "T", env("LB", hB, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4)))
	if got, _ := k.Attempt(idB); got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatalf("domain B's snapshot must resolve B's own attempt, got %+v", got)
	}
	if got, _ := k.Attempt(idA); got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("domain A's identically-numbered attempt must be untouched, got %+v", got)
	}
}

// The cross-domain check keeps its value once ids are unique: a shell that
// GENUINELY names another domain's attempt id (misconfiguration, or a
// hostile shell) still resolves cross-domain and is rejected as a
// contradiction — it is not silently treated as an unknown id.
func TestSnapshotCrossDomainIDStillConflict(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "LA", nil)
	hB := establish(t, k, "T", p, "LB", nil)
	idA := AttemptID("s-" + string(hA.Domain) + "-0")
	mustIngest(t, k, "T", env("LA", hA, 2, startEvt(&idA, "a")))
	mustIngest(t, k, "T", env("LA", hA, 3, completeEvt(idA, 0, fence(0x01))))
	mustIngest(t, k, "T", env("LA", hA, 4, promptReadyEvt()))

	p.reset()
	outs, err := k.NotifyGap("T", hB.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	// B's shell names A's attempt id: a contradiction, not an unknown id.
	last := &CompletedRef{AttemptID: idA, ExitCode: intPtr(9)}
	if _, err := k.Ingest("T", env("LB", hB, 3, snapshotEvt(rid, ShellAtPrompt, nil, last, 4))); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("snapshot naming another domain's attempt id must conflict, got %v", err)
	}
	if got, _ := k.Attempt(idA); got.ExitCode != nil && *got.ExitCode == 9 {
		t.Fatalf("domain A's attempt must be untouched by B's rejected snapshot, got %+v", got)
	}
}

// Constraint (c), parent-restored face: a stale child alias must not resolve
// after the parent is restored. The child attached an app attempt under the
// alias s-<child>-0 and closed; the parent resumes and its snapshot names
// that alias. Resolution is scoped to the parent's domain, so the child's
// record stays exactly as the close left it — unknown, no invented status.
func TestStaleChildAliasNeverResolvesAfterParentRestored(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "L", nil)
	mustIngest(t, k, "T", env("L", hA, 2, suspendEvt()))
	hB := establish(t, k, "T", p, "L", &hA.Domain)
	attB, _ := k.SubmitAttempt(hB.Domain, "child make", "/", "local")
	childAlias := AttemptID("s-" + string(hB.Domain) + "-0")
	mustIngest(t, k, "T", env("L", hB, 2, startEvt(&childAlias, "child make")))
	if got, _ := k.Attempt(attB.ID); got.shellID != childAlias {
		t.Fatalf("child alias must be recorded at attach, got %q", got.shellID)
	}
	// The child shell exits: its domain closes and the open attempt becomes
	// unknown — the state the parent must never be able to resurrect.
	mustIngest(t, k, "T", env("L", hB, 3, closeEvt()))
	if got, _ := k.Attempt(attB.ID); got.State != AttemptUnknown {
		t.Fatalf("child's open attempt must reconcile unknown on close, got %v", got.State)
	}
	mustIngest(t, k, "T", env("L", hA, 3, activateEvt()))

	p.reset()
	outs, err := k.NotifyGap("T", hA.Domain, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	last := &CompletedRef{AttemptID: childAlias, ExitCode: intPtr(5)}
	mustIngest(t, k, "T", env("L", hA, 4, snapshotEvt(rid, ShellAtPrompt, nil, last, 5)))
	if got, _ := k.Attempt(attB.ID); got.State != AttemptUnknown || got.ExitCode != nil {
		t.Fatalf("the stale child alias must never resolve in the parent: child attempt got %+v", got)
	}
	if st := mustState(t, k, "L"); st.Lifecycle != LifecyclePromptReady {
		t.Fatalf("parent's snapshot must restore the lane to a ready prompt, got %v", st.Lifecycle)
	}
}

func TestDesyncBudgetExhaustionRevokesDomain(t *testing.T) {
	// Episode budget of one: the second gap revokes the domain outright.
	k, _, _ := newTestKernel(Options{Budgets: Budgets{MaxDesyncEpisodes: 1}})
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "x", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "x")))

	p.reset()
	outs, err := k.NotifyGap("T", h.Domain, 16, 1)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	rid := p.envelopes()[0].Event.RefreshRequest.RequestID
	mustIngest(t, k, "T", env("L", h, 3, snapshotEvt(rid, ShellRunning, &att.ID, nil, 4)))
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleRunning {
		t.Fatalf("first episode must be recoverable, got %v", st.Lifecycle)
	}
	// The second episode exceeds the budget of one: the gap itself revokes
	// (it is accepted, then the domain is gone), and later gaps are rejected.
	if _, err := k.NotifyGap("T", h.Domain, 16, 1); err != nil {
		t.Fatalf("the revoking gap must not error, got %v", err)
	}
	if _, err := k.NotifyGap("T", h.Domain, 16, 1); !errors.Is(err, ErrDomainNotLive) {
		t.Fatalf("gap on a revoked domain must be rejected, got %v", err)
	}
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleNative || len(st.Stack) != 0 {
		t.Fatalf("revoked domain must leave a native lane, got %+v", st)
	}
	if got, _ := k.Attempt(att.ID); got.State != AttemptUnknown {
		t.Fatalf("revocation must unknown the open attempt, got %v", got.State)
	}
}

func TestDesyncScanBudgetExhaustionRevokesDomain(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	outs, err := k.NotifyGap("T", h.Domain, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	// 200 garbage frames exceeds the 128-frame budget.
	outs, err = k.NotifyGap("T", h.Domain, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleNative {
		t.Fatalf("scan-budget exhaustion must revoke, got %v", st.Lifecycle)
	}
	if d, _ := k.Domain(h.Domain); d.State != DomainClosed {
		t.Fatalf("domain must be closed, got %v", d.State)
	}
}

func TestDesyncDurationBudgetExhaustionRevokesDomain(t *testing.T) {
	k, clock, _ := newTestKernel(Options{Budgets: Budgets{ScanDuration: 5 * time.Second}})
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	outs, err := k.NotifyGap("T", h.Domain, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	clock.advance(6 * time.Second)
	outs, err = k.NotifyGap("T", h.Domain, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustDeliver(t, k, outs)
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleNative {
		t.Fatalf("duration-budget exhaustion must revoke, got %v", st.Lifecycle)
	}
}

// --- handshake (decision 3) ---------------------------------------------------

func TestNothingBeforeAccept(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h, err := k.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	// Lifecycle events for a Pending domain are rejected before any accept.
	if _, err := k.Ingest("T", env("L", h, 1, startEvt(nil, "ls"))); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("start before accept must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 1, promptReadyEvt())); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("prompt_ready before accept must be rejected, got %v", err)
	}
	if len(p.envelopes()) != 0 {
		t.Fatalf("nothing may be sent before accept, got %v", p.kinds())
	}
}

func TestHandshakeRateLimit(t *testing.T) {
	k, clock, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	// Eight failed handshakes fill the budget…
	for i := 0; i < 8; i++ {
		wrong := h.Capability
		wrong[0] ^= byte(i + 1)
		if _, err := k.Ingest("T", envRaw("L", h.Domain, h.Epoch, wrong, 100, helloEvt("bash"))); !errors.Is(err, ErrBadCapability) {
			t.Fatalf("failed handshake %d must be rejected, got %v", i, err)
		}
	}
	// …and new establishment on the lane is refused.
	if _, err := k.RequestDomain("L", nil, "T"); !errors.Is(err, ErrHandshakeRateLimited) {
		t.Fatalf("establishment must be rate limited, got %v", err)
	}
	// The window drains; the limiter clears (the lane is still busy with its
	// established domain, so ErrLaneBusy is the expected non-rate-limit
	// outcome — it proves the limiter no longer refuses).
	clock.advance(31 * time.Second)
	if _, err := k.RequestDomain("L", nil, "T"); errors.Is(err, ErrHandshakeRateLimited) {
		t.Fatalf("establishment must recover after the window")
	}
}

// --- attempts (decision 5) ----------------------------------------------------

func TestStartAttachRules(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	// A start naming the attempt in the shell's own namespace over a pending
	// app attempt attaches and records the shell id as a per-attempt alias:
	// the shell never learns the app-minted id (protocol §8 — no outbound
	// envelope carries one), so its own id is the only name it can later
	// report in a snapshot. The app id stays authoritative (constraint b):
	// the attempt keeps its id and app-owned text, and no attempt appears
	// under the shell id.
	att, _ := k.SubmitAttempt(h.Domain, "app cmd", "/", "local")
	shellID := AttemptID("s-1-0")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&shellID, "evil")))
	if got, _ := k.Attempt(att.ID); !got.Started || got.Command != "app cmd" || got.shellID != shellID {
		t.Fatalf("shell-named start must attach with the alias recorded, got %+v", got)
	}
	if _, exists := k.Attempt(shellID); exists {
		t.Fatal("the shell id must never become an attempt's identity")
	}

	// A second start while the attempt runs is still a violation — the
	// attach window admits exactly one start, because Started flips and the
	// next start hits ErrAttemptOpen (constraint d) — with an explicit id
	// and without.
	if _, err := k.Ingest("T", env("L", h, 3, startEvt(&att.ID, "again"))); !errors.Is(err, ErrAttemptOpen) {
		t.Fatalf("start while running must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 4, startEvt(nil, "again"))); !errors.Is(err, ErrAttemptOpen) {
		t.Fatalf("anonymous start while running must be rejected, got %v", err)
	}

	// The anonymous start still attaches to the next app attempt.
	mustIngest(t, k, "T", env("L", h, 5, completeEvt(att.ID, 0, fence(0x01))))
	mustIngest(t, k, "T", env("L", h, 6, promptReadyEvt()))
	att2, _ := k.SubmitAttempt(h.Domain, "app cmd 2", "/", "local")
	mustIngest(t, k, "T", env("L", h, 7, startEvt(nil, "evil2")))
	if got, _ := k.Attempt(att2.ID); !got.Started || got.Command != "app cmd 2" {
		t.Fatalf("anonymous start must attach to the app attempt, got %+v", got)
	}
}

// The regression that matters (brief defect 1): two domains in one lane —
// a docker-exec/ssh child over its parent — each with a shell-originated
// start whose per-shell counter is at the same value. The minted id carries
// the domain (s-<dom>-<n>), so equal counters are distinct ids and BOTH
// starts succeed. Before that fix the mint was s-$$-<n>: PID spaces are not
// shared across domains, two shells routinely share a low PID, and the
// kernel's global k.attempts lookup rejected the second domain's first
// command with ErrAttemptIDExists.
func TestShellOriginatedStartsSameCounterAcrossDomains(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "L", nil)
	// The parent shell's first command: shell-originated, counter 0.
	idA := AttemptID("s-" + string(hA.Domain) + "-0")
	mustIngest(t, k, "T", env("L", hA, 2, startEvt(&idA, "parent-cmd")))
	if got, ok := k.Attempt(idA); !ok || !got.Started || got.Origin != OriginShell {
		t.Fatalf("parent's shell-originated attempt must exist under its own id, got %+v ok=%v", got, ok)
	}
	mustIngest(t, k, "T", env("L", hA, 3, completeEvt(idA, 0, fence(0x01))))
	mustIngest(t, k, "T", env("L", hA, 4, promptReadyEvt()))

	// The nested shell (sudo/su/ssh/docker) suspends the parent and gets its
	// own authenticated domain in the same lane.
	mustIngest(t, k, "T", env("L", hA, 5, suspendEvt()))
	hB := establish(t, k, "T", p, "L", &hA.Domain)
	// The child shell is also at counter 0 — under the pre-fix mint its id
	idB := AttemptID("s-" + string(hB.Domain) + "-0")
	mustIngest(t, k, "T", env("L", hB, 2, startEvt(&idB, "child-cmd")))
	if got, ok := k.Attempt(idB); !ok || !got.Started || got.Origin != OriginShell {
		t.Fatalf("child's shell-originated attempt must exist under its own id, got %+v ok=%v", got, ok)
	}
	if got, _ := k.Attempt(idA); got.State != AttemptCompleted {
		t.Fatalf("parent's attempt must be untouched by the child's start, got %v", got.State)
	}
}

func TestStartRequiresPromptReady(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "x", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "x")))
	mustIngest(t, k, "T", env("L", h, 3, completeEvt(att.ID, 0, fence(0x01))))
	// The lane is Running with a closed attempt, awaiting prompt_ready: a
	// fresh start here would open a second attempt — rejected.
	if _, err := k.Ingest("T", env("L", h, 4, startEvt(nil, "too early"))); !errors.Is(err, ErrNotPromptReady) {
		t.Fatalf("start before prompt_ready must be rejected, got %v", err)
	}
}

func TestPromptReadyOverOpenAttemptRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "x", "/", "local")
	mustIngest(t, k, "T", env("L", h, 2, startEvt(&att.ID, "x")))
	if _, err := k.Ingest("T", env("L", h, 3, promptReadyEvt())); !errors.Is(err, ErrPromptOverAttempt) {
		t.Fatalf("prompt_ready over an open attempt must be rejected, got %v", err)
	}
}

func TestCompleteValidation(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(h.Domain, "x", "/", "local")

	// Complete before start: rejected.
	if _, err := k.Ingest("T", env("L", h, 2, completeEvt(att.ID, 0, fence(0x02)))); !errors.Is(err, ErrAttemptNotStarted) {
		t.Fatalf("completion of an unstarted attempt must be rejected, got %v", err)
	}
	mustIngest(t, k, "T", env("L", h, 3, startEvt(&att.ID, "x")))
	// Missing fence: rejected.
	if _, err := k.Ingest("T", env("L", h, 4, completeEvtNoFence(att.ID))); !errors.Is(err, ErrFenceMissing) {
		t.Fatalf("fence-less completion must be rejected, got %v", err)
	}
	mustIngest(t, k, "T", env("L", h, 5, completeEvt(att.ID, 7, fence(0x03))))
	// Exit status is set exactly once.
	if _, err := k.Ingest("T", env("L", h, 6, completeEvt(att.ID, 0, fence(0x04)))); !errors.Is(err, ErrAttemptNotOpen) {
		t.Fatalf("second completion must be rejected, got %v", err)
	}
	if got, _ := k.Attempt(att.ID); got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("first status must persist, got %+v", got)
	}
}

func TestCompleteCannotCrossDomains(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "L", nil)
	att, _ := k.SubmitAttempt(hA.Domain, "sudo", "/", "local")
	mustIngest(t, k, "T", env("L", hA, 2, startEvt(&att.ID, "sudo")))
	mustIngest(t, k, "T", env("L", hA, 3, suspendEvt()))
	hB := establish(t, k, "T", p, "L", &hA.Domain)
	// A completion for A's attempt arriving with B's domain on the envelope:
	// wrong domain. With A's own domain: A is inactive. Both rejected.
	if _, err := k.Ingest("T", env("L", hB, 2, completeEvt(att.ID, 0, fence(0x05)))); !errors.Is(err, ErrAttemptDomainMismatch) {
		t.Fatalf("cross-domain completion must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", env("L", hA, 4, completeEvt(att.ID, 0, fence(0x06)))); !errors.Is(err, ErrDomainInactive) {
		t.Fatalf("completion for an inactive domain must be rejected, got %v", err)
	}
}

// --- domain stack (decisions 2, 6) ---------------------------------------------

func TestChildCannotEstablishOverActiveParent(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "L", nil)
	// The child's hello arrives while the parent is still active: rejected.
	hB, err := k.RequestDomain("L", &hA.Domain, "T")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Ingest("T", env("L", hB, 1, helloEvt("bash"))); !errors.Is(err, ErrParentActive) {
		t.Fatalf("child over an active parent must be rejected, got %v", err)
	}
	if _, ok := k.Domain(hB.Domain); !ok {
		t.Fatal("the pending domain must still exist (rejected, not destroyed)")
	}
	// After the parent suspends, the same hello establishes the child.
	mustIngest(t, k, "T", env("L", hA, 2, suspendEvt()))
	mustIngest(t, k, "T", env("L", hB, 1, helloEvt("bash")))
	if st := mustState(t, k, "L"); st.Lifecycle != LifecyclePromptReady || st.Domain != hB.Domain {
		t.Fatalf("child must be active after parent suspends, got %+v", st)
	}
}

func TestSecondRootRejectedWhileLaneLive(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	establish(t, k, "T", p, "L", nil)
	if _, err := k.RequestDomain("L", nil, "T"); !errors.Is(err, ErrLaneBusy) {
		t.Fatalf("a second top-level domain on a live lane must be rejected, got %v", err)
	}
}

func TestActivateAndCloseOrdering(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "L", nil)
	// Activating an established (active) domain is not a thing.
	if _, err := k.Ingest("T", env("L", hA, 2, activateEvt())); !errors.Is(err, ErrNotSuspended) {
		t.Fatalf("activating an established domain must be rejected, got %v", err)
	}
	mustIngest(t, k, "T", env("L", hA, 3, suspendEvt()))
	hB := establish(t, k, "T", p, "L", &hA.Domain)

	// The parent cannot be activated while the child is on top.
	if _, err := k.Ingest("T", env("L", hA, 4, activateEvt())); !errors.Is(err, ErrDomainNotTop) {
		t.Fatalf("activation under a live child must be rejected, got %v", err)
	}
	// The child closes; closing it again is rejected.
	mustIngest(t, k, "T", env("L", hB, 2, closeEvt()))
	if _, err := k.Ingest("T", env("L", hB, 3, closeEvt())); !errors.Is(err, ErrDomainNotLive) {
		t.Fatalf("closing a closed domain must be rejected, got %v", err)
	}
}

func TestSuspendedDomainEventsRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	hA := establish(t, k, "T", p, "L", nil)
	mustIngest(t, k, "T", env("L", hA, 2, suspendEvt()))
	for _, evt := range []Event{
		startEvt(nil, "x"),
		promptReadyEvt(),
		suspendEvt(),
	} {
		if _, err := k.Ingest("T", env("L", hA, 3, evt)); !errors.Is(err, ErrDomainInactive) {
			t.Fatalf("suspended-domain event %s must be rejected, got %v", evt.Kind, err)
		}
	}
	if _, err := k.SubmitAttempt(hA.Domain, "x", "/", "local"); !errors.Is(err, ErrDomainInactive) {
		t.Fatalf("submit into a suspended domain must be rejected, got %v", err)
	}
}

// --- transports (decision 8) --------------------------------------------------

func TestTransportLossCascadesToDescendants(t *testing.T) {
	k, _, _ := newTestKernel()
	p1, p2 := &fakePort{}, &fakePort{}
	_ = k.BindTransport("T1", p1)
	_ = k.BindTransport("T2", p2)
	// Local shell on T1; its ssh child on T2 (real nested topology).
	hA := establish(t, k, "T1", p1, "L", nil)
	mustIngest(t, k, "T1", env("L", hA, 2, suspendEvt()))
	hB := establish(t, k, "T2", p2, "L", &hA.Domain)

	// Losing the parent's transport takes the child down with it, even
	// though the child's own transport is untouched.
	if err := k.TransportLost("T1"); err != nil {
		t.Fatal(err)
	}
	if dA, _ := k.Domain(hA.Domain); dA.State != DomainLost {
		t.Fatalf("parent must be lost, got %v", dA.State)
	}
	if dB, _ := k.Domain(hB.Domain); dB.State != DomainLost {
		t.Fatalf("child must be lost with its parent chain, got %v", dB.State)
	}
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleLost {
		t.Fatalf("lane must be lost, got %v", st.Lifecycle)
	}
}

// TestLossThenNewEstablishmentGetsFreshEpoch is the acceptance-literal form
// of protocol §12: after a transport loss, a new session on a new transport
// gets a FRESH epoch — strictly greater, never resumed, never reused — and
// the dead domain's epoch and capability authenticate nothing on the new
// domain. The assertion is on the epoch value, not merely on the existence
// of a session.
func TestLossThenNewEstablishmentGetsFreshEpoch(t *testing.T) {
	k, _, _ := newTestKernel()
	p1 := &fakePort{}
	_ = k.BindTransport("T1", p1)
	h1 := establish(t, k, "T1", p1, "L", nil)

	if err := k.TransportLost("T1"); err != nil {
		t.Fatal(err)
	}
	if st := mustState(t, k, "L"); st.Lifecycle != LifecycleLost {
		t.Fatalf("lane must be lost, got %v", st.Lifecycle)
	}

	p2 := &fakePort{}
	_ = k.BindTransport("T2", p2)
	h2, err := k.RequestDomain("L", nil, "T2")
	if err != nil {
		t.Fatal(err)
	}
	if h2.Epoch <= h1.Epoch {
		t.Fatalf("new establishment must mint a strictly fresh epoch: %d <= %d", h2.Epoch, h1.Epoch)
	}
	if h2.Domain == h1.Domain {
		t.Fatalf("new establishment must mint a new domain id, got %s", h2.Domain)
	}
	// The dead domain's authenticators are dead: the old epoch on the new
	// domain, and the old capability on the new domain, are both rejected
	// before any state is consulted.
	if _, err := k.Ingest("T2", envRaw("L", h2.Domain, h1.Epoch, h2.Capability, 1, helloEvt("bash"))); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale epoch must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T2", envRaw("L", h2.Domain, h2.Epoch, h1.Capability, 1, helloEvt("bash"))); !errors.Is(err, ErrBadCapability) {
		t.Fatalf("dead domain's capability must be rejected, got %v", err)
	}
	// The fresh domain still establishes normally.
	mustIngest(t, k, "T2", env("L", h2, 1, helloEvt("bash")))
	mustAccept(t, p2)
}

// --- addressing and envelope validation ---------------------------------------

func TestEnvelopeAddressingRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	badVersion := env("L", h, 2, startEvt(nil, "x"))
	badVersion.Version = 99
	badLane := envRaw("L2", h.Domain, h.Epoch, h.Capability, 2, startEvt(nil, "x"))
	badDomain := envRaw("L", "dom-nope", h.Epoch, h.Capability, 2, startEvt(nil, "x"))
	unknownTransport := env("L", h, 2, startEvt(nil, "x"))

	if _, err := k.Ingest("T2", unknownTransport); !errors.Is(err, ErrUnknownTransport) {
		t.Fatalf("unknown transport must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", badDomain); !errors.Is(err, ErrUnknownDomain) {
		t.Fatalf("unknown domain must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", badVersion); !errors.Is(err, ErrBadVersion) {
		t.Fatalf("bad version must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", badLane); !errors.Is(err, ErrWrongLane) {
		t.Fatalf("wrong lane must be rejected, got %v", err)
	}
}

func TestKernelOriginatedKindsRejectedInbound(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	for _, evt := range []Event{
		{Kind: KindAccept, Accept: &Accept{}},
		{Kind: KindRefreshRequest, RefreshRequest: &RefreshRequest{RequestID: "r"}},
		{Kind: KindDomainEstablished, DomainEstablished: &DomainEstablishedEvent{}},
	} {
		if _, err := k.Ingest("T", env("L", h, 2, evt)); !errors.Is(err, ErrIllegalEvent) {
			t.Fatalf("kernel-originated kind %s must be rejected inbound, got %v", evt.Kind, err)
		}
	}
}

func TestRegistrySupportsSeveralDomainsOnOneTransport(t *testing.T) {
	r := NewDomainRegistry()
	d1 := &Domain{ID: "d1", Transport: "T", State: DomainPending}
	d2 := &Domain{ID: "d2", Transport: "T", State: DomainPending}
	d3 := &Domain{ID: "d3", Transport: "T2", State: DomainPending}
	r.Register(d1)
	r.Register(d2)
	r.Register(d3)
	if got := r.DomainsOnTransport("T"); len(got) != 2 {
		t.Fatalf("want 2 domains on T, got %d", len(got))
	}
	if got := r.DomainsOnTransport("T2"); len(got) != 1 {
		t.Fatalf("want 1 domain on T2, got %d", len(got))
	}
	if d, ok := r.Lookup("d2"); !ok || d.ID != "d2" {
		t.Fatalf("lookup failed: %v %v", d, ok)
	}
}

func TestStateUnknownLane(t *testing.T) {
	k, _, _ := newTestKernel()
	if _, err := k.State("nowhere"); !errors.Is(err, ErrUnknownLane) {
		t.Fatalf("unknown lane must error, got %v", err)
	}
}

func TestOversizeCommandRejected(t *testing.T) {
	k, _, _ := newTestKernel(Options{Budgets: Budgets{MaxCommandBytes: 16}})
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	big := make([]byte, 64)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := k.SubmitAttempt(h.Domain, string(big), "/", "local"); !errors.Is(err, ErrOversizeCommand) {
		t.Fatalf("oversize submit must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 2, startEvt(nil, string(big)))); !errors.Is(err, ErrOversizeCommand) {
		t.Fatalf("oversize start must be rejected, got %v", err)
	}
}

func intPtr(i int) *int { return &i }

// A shell that attached to an app-submitted attempt never learns the app-minted
// id, so it completes without naming one and the kernel resolves the domain's
// single open attempt. A required id here made completion unreachable from the
// shell on the primary path (found by the shell adapter, nocx-u7uh.3).
func TestCompleteWithoutAttemptIDResolvesByContext(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	att, err := k.SubmitAttempt(h.Domain, "echo hi", "/home/dev", "local")
	if err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 2, startEvt(nil, "echo hi"))); err != nil {
		t.Fatalf("start: %v", err)
	}
	ev := Event{Kind: KindComplete, Complete: &Complete{ExitCode: intPtr(0), Fence: fence(0x31)}}
	if _, err := k.Ingest("T", env("L", h, 3, ev)); err != nil {
		t.Fatalf("unnamed completion must resolve the open attempt: %v", err)
	}
	got, ok := k.Attempt(att.ID)
	if !ok || got.State != AttemptCompleted || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("attempt not completed by context: %+v", got)
	}
}

// A named id that is not the domain's open attempt is still refused, so making
// the field optional did not loosen the cross-attempt rule.
func TestCompleteWithForeignAttemptIDRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	if _, err := k.SubmitAttempt(h.Domain, "echo hi", "/home/dev", "local"); err != nil {
		t.Fatalf("SubmitAttempt: %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 2, startEvt(nil, "echo hi"))); err != nil {
		t.Fatalf("start: %v", err)
	}
	bogus := AttemptID("does-not-exist")
	ev := Event{Kind: KindComplete, Complete: &Complete{AttemptID: &bogus, ExitCode: intPtr(0), Fence: fence(0x32)}}
	if _, err := k.Ingest("T", env("L", h, 3, ev)); !errors.Is(err, ErrAttemptNotOpen) {
		t.Fatalf("foreign attempt id must be rejected, got %v", err)
	}
}

// --- restoration acknowledgement (decision 8) ------------------------------

// TestRecoverLaneLostToNativeOnly is the kernel half of decision 8's
// composite ACK: the ack moves a Lost lane to Native and nothing else. The
// domain stays permanently Lost — any future integration is a fresh epoch —
// and a lane with a live domain is refused (the ack can never revoke
// authority). Idempotent: an already-Native lane is a no-op success.
func TestRecoverLaneLostToNativeOnly(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)

	// Not lost: refused.
	if err := k.RecoverLane("L"); !errors.Is(err, ErrNotLost) {
		t.Fatalf("recover over a live lane = %v, want ErrNotLost", err)
	}
	// Unknown lane: refused.
	if err := k.RecoverLane("nope"); !errors.Is(err, ErrUnknownLane) {
		t.Fatalf("recover over an unknown lane = %v, want ErrUnknownLane", err)
	}

	if err := k.TransportLost("T"); err != nil {
		t.Fatal(err)
	}
	// The ack lands: Lost → Native.
	if err := k.RecoverLane("L"); err != nil {
		t.Fatalf("RecoverLane: %v", err)
	}
	st := mustState(t, k, "L")
	if st.Lifecycle != LifecycleNative {
		t.Fatalf("lane after recover = %v, want Native", st.Lifecycle)
	}
	// The domain stays permanently lost.
	if d, _ := k.Domain(h.Domain); d.State != DomainLost {
		t.Fatalf("domain after recover = %v, want permanently DomainLost", d.State)
	}
	// Idempotent: a duplicate ack is a no-op success.
	if err := k.RecoverLane("L"); err != nil {
		t.Fatalf("duplicate recover = %v, want idempotent no-op", err)
	}
}

// TestRecoveryNonceMintedPerDomain proves the pre-provisioned one-shot fence
// (decision 8): each domain mints a distinct recovery nonce alongside its
// capability, the lane mirrors it and exposes it after loss (the read model
// the publisher attaches to the lost fact), and a fresh establishment mints
// a fresh nonce — a late ack from an old episode can never match.
func TestRecoveryNonceMintedPerDomain(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h1 := establish(t, k, "T", p, "L", nil)
	if h1.Recovery == (FenceNonce{}) {
		t.Fatal("every domain must mint a one-shot recovery fence")
	}
	if FenceNonce(h1.Capability) == h1.Recovery {
		t.Fatal("the recovery fence must be distinct from the capability")
	}

	if err := k.TransportLost("T"); err != nil {
		t.Fatal(err)
	}
	st := mustState(t, k, "L")
	if st.RecoveryNonce != h1.Recovery {
		t.Fatal("the lost lane must still expose its domain's recovery nonce")
	}

	// A fresh establishment on a fresh transport mints a fresh nonce.
	p2 := &fakePort{}
	_ = k.BindTransport("T2", p2)
	h2, err := k.RequestDomain("L", nil, "T2")
	if err != nil {
		t.Fatal(err)
	}
	if h2.Recovery == h1.Recovery {
		t.Fatal("a fresh domain must mint a fresh recovery fence — never reused")
	}
	if st2 := mustState(t, k, "L"); st2.RecoveryNonce != h2.Recovery {
		t.Fatal("the lane must mirror the new domain's nonce")
	}
}

// --- decision 9: the accept must be delivered before the domain is live ------

// TestEstablishmentUndeliveredAcceptNotLive: a hello is accepted and the
// accept minted, but the accept is NOT delivered — the domain is not live
// (decision 9: live means past ACCEPT). Lifecycle events are rejected while
// the accept is undelivered; delivering it is the closing event that makes
// them legal.
func TestEstablishmentUndeliveredAcceptNotLive(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h, err := k.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	outs, err := k.Ingest("T", env("L", h, 1, helloEvt("bash")))
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if len(outs) != 1 || outs[0].Envelope.Event.Kind != KindAccept {
		t.Fatalf("hello must produce exactly one accept outbound, got %v", outboundKinds(outs))
	}
	// The shell has not received the accept: the domain must not be live.
	if _, err := k.Ingest("T", env("L", h, 2, startEvt(nil, "ls"))); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("start before accept delivery must be rejected as not past accept, got %v", err)
	}
	if _, err := k.Ingest("T", env("L", h, 2, promptReadyEvt())); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("prompt_ready before accept delivery must be rejected, got %v", err)
	}
	// Delivering the accept is the closing event: the domain becomes live.
	if err := k.Deliver(outs[0]); err != nil {
		t.Fatalf("Deliver(accept): %v", err)
	}
	mustIngest(t, k, "T", env("L", h, 2, startEvt(nil, "ls")))
	att, ok := k.OpenAttempt(h.Domain)
	if !ok || att.Command != "ls" {
		t.Fatalf("start must attach after the accept is delivered, got %+v", att)
	}
}

// TestEstablishmentTimeoutRevokesUndeliveredAccept: an accept that never
// reaches the shell (no renderer acknowledgement) is rolled back by
// EstablishmentTimeout — the domain is revoked, the lane falls to Native,
// and the safe state is what the caller publishes. The accept itself is
// never delivered afterwards: a late Deliver is refused.
func TestEstablishmentTimeoutRevokesUndeliveredAccept(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h, err := k.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	outs, err := k.Ingest("T", env("L", h, 1, helloEvt("bash")))
	if err != nil {
		t.Fatal(err)
	}
	// The ack never arrives; the establishment bound expires.
	if err := k.EstablishmentTimeout(h.Domain); err != nil {
		t.Fatalf("EstablishmentTimeout: %v", err)
	}
	st := mustState(t, k, "L")
	if st.Lifecycle != LifecycleNative || len(st.Stack) != 0 {
		t.Fatalf("timeout must revoke to a native lane, got %+v", st)
	}
	if d, _ := k.Domain(h.Domain); d.State != DomainClosed {
		t.Fatalf("timed-out domain must be Closed, got %v", d.State)
	}
	// A late delivery of the minted accept is refused: the shell must never
	// receive an accept for a dead domain (it would suppress against it).
	if err := k.Deliver(outs[0]); !errors.Is(err, ErrDomainNotLive) {
		t.Fatalf("late accept delivery must be refused, got %v", err)
	}
	if got := p.kinds(); len(got) != 0 {
		t.Fatalf("no envelope may reach the port after rollback, got %v", got)
	}
}

// TestEstablishmentTimeoutNoOpAfterDelivery: the acknowledgement raced the
// timeout — the accept WAS delivered, so the domain is live and the timeout
// must not revoke it.
func TestEstablishmentTimeoutNoOpAfterDelivery(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	if err := k.EstablishmentTimeout(h.Domain); err != nil {
		t.Fatalf("EstablishmentTimeout after delivery: %v", err)
	}
	st := mustState(t, k, "L")
	if st.Lifecycle != LifecyclePromptReady || st.Domain != h.Domain {
		t.Fatalf("delivered domain must stay live, got %+v", st)
	}
	// Events still accepted.
	mustIngest(t, k, "T", env("L", h, 2, promptReadyEvt()))
}

// TestEstablishmentTimeoutNoOpOnPendingDomain: a domain that never helloed is
// not touched by EstablishmentTimeout — the transport's own hello bound
// (TransportLost) owns that case.
func TestEstablishmentTimeoutNoOpOnPendingDomain(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h, err := k.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	if err := k.EstablishmentTimeout(h.Domain); err != nil {
		t.Fatalf("EstablishmentTimeout on a pending domain: %v", err)
	}
	if d, _ := k.Domain(h.Domain); d.State != DomainPending {
		t.Fatalf("pending domain must be untouched, got %v", d.State)
	}
}

// TestReconnectAcceptDoesNotReRequireDelivery: a reconnect hello within the
// epoch produces a fresh accept but does NOT take the domain back to
// accept-pending — the domain was already live, and the fresh accept is the
// publisher's gating concern (it may reuse or re-gate it), not a revocation
// of liveness. Events stay legal while the reconnect accept is undelivered.
func TestReconnectAcceptDoesNotReRequireDelivery(t *testing.T) {
	k, _, _ := newTestKernel()
	p := &fakePort{}
	_ = k.BindTransport("T", p)
	h := establish(t, k, "T", p, "L", nil)
	p.reset()
	outs, err := k.Ingest("T", env("L", h, 2, helloEvt("bash")))
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Envelope.Event.Kind != KindAccept {
		t.Fatalf("reconnect hello must produce an accept, got %v", outboundKinds(outs))
	}
	// The reconnect accept is NOT delivered, yet the domain stays live.
	mustIngest(t, k, "T", env("L", h, 3, startEvt(nil, "still live")))
	att, ok := k.OpenAttempt(h.Domain)
	if !ok || att.Command != "still live" {
		t.Fatalf("events must stay legal across an undelivered reconnect accept, got %+v", att)
	}
}

func outboundKinds(outs []Outbound) []EventKind {
	kinds := make([]EventKind, 0, len(outs))
	for _, o := range outs {
		kinds = append(kinds, o.Envelope.Event.Kind)
	}
	return kinds
}
