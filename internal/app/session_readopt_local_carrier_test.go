package app

// nocx-73aln: a REMOTE session's re-adoption dials THIS MACHINE'S OWN local
// helper for its lane's carrier (nocx-50w7p.10 — the coordinator itself opens
// no ssh exec lane any more), and that carrier answers nothing until
// installLocalHelper has put this machine's generation on disk. Reconciliation
// used to run at New, before Start ever gets there, so every remote
// re-adoption attempted at startup met a carrier that could only refuse —
// silently, as `unknown`, which is why a fresh coordinator could list a
// still-running helper session as gone from `sessions.live` with nothing in
// the log louder than a warn (measured with removeSession logging, b95c5c34:
// the helper closed zero sessions and the fresh coordinator's own inventory
// never gained the one under test).
//
// These tests exercise the REAL collaborators app.go wires for that lane —
// helperRegistry.lanes is the real *sshOverHelper, bound to the real
// *localHelperOpener — rather than the fakeLaneProvider the rest of this
// package's readopt tests use, because the fake is exactly what hid the
// defect: it never depended on this machine's own install at all.
//
// A carried-over LOCAL session was never affected — routeDir's own doc says
// why (its ask dials the generation the BINDING names, on a probe connection
// that may not start one) — which is also why this went unnoticed until a
// REMOTE one crossed a restart.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// remoteReadoptFixture is one carried-over remote binding, and the fakes a
// test needs to reconcile it without a real far host: a route that resolves
// the profile id to the binding's own host and account, and nothing else.
// Fingerprint is deliberately empty — this is about the LANE, not about
// consent, and an empty fingerprint is what skips readoptPass.Readopt's
// consent re-ask entirely.
func remoteReadoptFixture() (content.PendingSession, *stubRoutes) {
	binding := content.PendingSession{
		SessionID: "1cb68f92668993083b0f5ce0775442b6",
		Host:      "203.0.113.10", Account: "u",
		Generation:    "1d05bbcf9f88279db7f978b9a19445e5bf0f7b35f4c1615371a024708ff2fb44",
		PaneID:        "pane-1",
		ProfileID:     "profile-1",
		HelperCommand: "/opt/nocx/helper/nocx-helper",
	}
	return binding, &stubRoutes{host: binding.Host, cfg: helperConnection(binding.Account)}
}

// TestRemoteReadoptNeedsTheLocalHelperInstalledFirst pins the MECHANISM: the
// real lane provider app.go wires (sshOverHelper over localHelperOpener)
// refuses a remote re-adoption's connect attempt, by name, until this
// machine's own generation is on disk — and stops refusing THAT once it is,
// whatever else the (unreachable, TEST-NET) far host then fails on.
func TestRemoteReadoptNeedsTheLocalHelperInstalledFirst(t *testing.T) {
	storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: []byte("#!/bin/sh\nexit 0\n")}

	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	defer a.Shutdown(ctx)

	// New() alone must not have installed anything — the fix keeps New a
	// wiring method, and installing here is exactly the "brain method"
	// nocx-ie23r.5 already refused for this same opener.
	if _, _, connectErr := a.localHelper.connect(ctx); connectErr == nil {
		t.Fatal("New() alone left this machine's local helper installed; " +
			"the seam this test is about no longer exists")
	} else if !strings.Contains(connectErr.Error(), "not installed") {
		t.Fatalf("connect before Start failed with %q, want a NOT-INSTALLED refusal", connectErr)
	}

	binding, routes := remoteReadoptFixture()
	adopter := &stubAdopter{}
	pass := &readoptPass{registry: a.helperRegistry, routes: routes, adopter: adopter, local: a.localHelper}

	// ── RED: before Start, the only carrier a remote re-adoption has is
	// refused before it ever reaches the network, and the failure is
	// unmistakably about installation rather than about the far host.
	before := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(ctx, before, a.helperRegistry.inventories(), pass, time.Hour, a.slogger)
	if len(before.applied) != 1 {
		t.Fatalf("judgements before Start = %+v, want exactly one", before.applied)
	}
	got := before.applied[0]
	if got.Verdict != content.VerdictUnknown {
		t.Fatalf("verdict before Start = %q, want unknown — nobody could be asked yet", got.Verdict)
	}
	if !strings.Contains(got.Detail, "not installed") {
		t.Fatalf("detail = %q, want it to name the local helper as not installed — "+
			"otherwise this is not the ordering bug nocx-73aln measured", got.Detail)
	}
	if len(adopter.adoptedIDs()) != 0 {
		t.Fatal("a session was adopted although this machine's own carrier was never ready")
	}

	// Start(): installLocalHelper puts this machine's generation on disk.
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	// ── GREEN: the SAME binding, reconciled again with the SAME real lane
	// provider, no longer trips over its own carrier.
	after := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(ctx, after, a.helperRegistry.inventories(), pass, time.Hour, a.slogger)
	if len(after.applied) != 1 {
		t.Fatalf("judgements after Start = %+v, want exactly one", after.applied)
	}
	if strings.Contains(after.applied[0].Detail, "not installed") {
		t.Fatalf("still refused as uninstalled after Start: %+v", after.applied[0])
	}
}

// TestStartReconcilesARemoteSessionOnlyAfterInstallingTheLocalHelper is the
// composition-level proof, and the one that actually pins app.go's call site:
// it substitutes the two inputs New() now captures for Start to use — the
// store's own SessionReconciler and the profile resolver — with doubles, so
// Start's OWN internal reconcileSessions call is what this test observes,
// with no encrypted content.db to seed for a pending row.
//
// Before nocx-73aln's fix this call lived in New, before these two fields
// existed to substitute into at all — so the failure mode this test would
// have shown then is not "wrong verdict" but "nothing in Start ever consulted
// the doubles", which is exactly what the first assertion below checks for.
func TestStartReconcilesARemoteSessionOnlyAfterInstallingTheLocalHelper(t *testing.T) {
	storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: []byte("#!/bin/sh\nexit 0\n")}

	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	defer a.Shutdown(ctx)

	binding, routes := remoteReadoptFixture()
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	// Splicing in doubles for the two fields New() populated is what lets
	// this test watch Start's own call without a real, encrypted
	// content.db carrying the pending row: production wires the store's
	// real reconciler and profile resolver here, and neither is what this
	// test is about.
	a.sessionReconciler = rec
	a.sessionRoutes = routes

	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one — Start must reconcile the "+
			"binding it was given, which it can only do by calling reconcileSessions itself", rec.applied)
	}
	if got := rec.applied[0]; strings.Contains(got.Detail, "not installed") {
		t.Fatalf("Start reconciled %+v before installing this machine's own local helper — "+
			"reconcileSessions ran ahead of installLocalHelper again", got)
	}
}
