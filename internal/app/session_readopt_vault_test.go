package app

// The pre-listen reconciliation pass must not wait on a person it has not
// let connect, and a session that needed one must still be taken back once a
// client IS there to answer (nocx-xn63t.6.10, round 2).
//
// ROUND 1 of this bead made a re-adoption's credential fetch SUSPEND on a
// sealed vault instead of refusing at once (a since-reverted
// WSServer.PrimePresence), which fixed the observed CI failure but
// introduced a worse one nobody had measured: reconcileSessions runs every
// pending session's readoptPass.Readopt SEQUENTIALLY, each bounded by
// readoptAttemptTimeout — and Transport.Start, the only thing that lets a
// client attach, does not run until reconcileSessions returns (App.Start,
// nocx-73aln). A suspend inside that pass cannot be answered by anything: no
// client can attach until the pass gives up, so every password-authenticated
// session pending across a restart would cost its own attempt bound before
// Start ever finishes — for the "eight restored ssh panes" case
// internal/vault/presence.go's own comment names, minutes before the app is
// reachable at all.
//
// TestReconcileSessions_PreListenPassRefusesPromptlyEvenWithNPendingSessions
// is the measurement the coordinator asked for: it proves the pass returns
// quickly with N sealed-vault sessions pending and no client, and its sibling
// TestReconcileSessions_IfPresenceIsPrimedTooEarlyEachSessionCostsItsBound
// pins the SHAPE of the mistake round 1 made, so nothing reintroduces it by
// priming presence ahead of this pass again.
//
// TestReconcileSessions_ASessionLeftPendingIsTakenBackOnceAttachedAndUnsealed
// is the other half: the session the pre-listen pass could not judge is not
// abandoned. App.Start now runs reconcileSessions a second time, once
// Transport.Start has returned — by which point a client CAN attach and
// unseal — and that second pass is what this test drives directly, proving
// the retry (not a suspend inside the first pass) is what takes the session
// back.

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	vaultfile "github.com/shady2k/nocx/internal/vault/file"
)

// refusingUnlockRequester is the production answer for zero connected
// clients (transport.WSServer.RequestUnlock, unlock_requester.go's
// broadcastAsk: "if len(conns) == 0 { return noClientErr }"), reproduced
// here without a real WebSocket because what is under test is
// Vault.EnsureUnsealed's own decision of whether to call this at all, not
// the socket underneath it.
type refusingUnlockRequester struct{}

func (refusingUnlockRequester) RequestUnlock(context.Context, string) error {
	return vault.ErrNoUnlockClient
}

// sealedVaultForReadopt builds a real, sealed, file-backed vault — the same
// provider a build with no OS keystore uses (internal/app/app.go's file.New)
// — with an UnlockRequester wired the way App.New wires the transport, so
// EnsureUnsealed takes the SAME branch production does rather than the
// early "nobody is watching" return a nil requester would give it.
func sealedVaultForReadopt(t *testing.T) *vault.Vault {
	t.Helper()
	store := storage.NewDocumentStore(t.TempDir())
	fileProv := vaultfile.New(store, "vault-file.json")
	reg, err := vault.NewRegistry(fileProv)
	if err != nil {
		t.Fatalf("vault.NewRegistry: %v", err)
	}
	v, err := vault.New(store, reg, discardLogger())
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)
	if _, err := v.Setup(context.Background(), vault.SetupRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("vault.Setup: %v", err)
	}
	v.SetUnlockRequester(refusingUnlockRequester{})
	v.Seal()
	if v.State() != vault.StateSealed {
		t.Fatalf("vault state = %v, want sealed", v.State())
	}
	return v
}

// freshlySetUpVaultForReadopt is sealedVaultForReadopt's other half: a real
// vault that is set up and, like a vault the instant after Setup runs,
// UNSEALED — for the one caller that needs to do real vault-backed work
// (opening a session, in this file) before sealing it itself to model the
// restart under test.
func freshlySetUpVaultForReadopt(t *testing.T) *vault.Vault {
	t.Helper()
	store := storage.NewDocumentStore(t.TempDir())
	fileProv := vaultfile.New(store, "vault-file.json")
	reg, err := vault.NewRegistry(fileProv)
	if err != nil {
		t.Fatalf("vault.NewRegistry: %v", err)
	}
	v, err := vault.New(store, reg, discardLogger())
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)
	if _, err := v.Setup(context.Background(), vault.SetupRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("vault.Setup: %v", err)
	}
	v.SetUnlockRequester(refusingUnlockRequester{})
	if v.State() != vault.StateUnsealed {
		t.Fatalf("vault state = %v, want unsealed straight after Setup", v.State())
	}
	return v
}

// vaultGatedLaneProvider stands in for the whole path a real re-adoption's
// LaneConn takes through this machine's own ssh auth ladder to the
// credential a helper must present: sshOverHelper.LaneConn resolves the
// target, opens a lane, and the far side's reverse ssh.secret ask reaches
// credential.Resolver's Operation stance, which reaches
// Vault.EnsureUnsealed (internal/app/helper_sshchan.go, helper_reverse.go,
// internal/credential/resolver.go). What is under test is EnsureUnsealed's
// own timing decision, not the four layers above it, so this seam collapses
// them to the one call that matters and delegates everything else — the
// connection material a re-adopted session needs — to the SAME
// fakeLaneProvider every other test in this file already drives once the
// vault has answered.
type vaultGatedLaneProvider struct {
	v      *vault.Vault
	reason string
	inner  laneProvider
}

func (p *vaultGatedLaneProvider) LaneConn(
	ctx context.Context, host string, machine proto.Machine, generation proto.GenerationID,
	opts ...ssh.ConnectOption,
) (helperclient.HelperConn, error) {
	if err := p.v.EnsureUnsealed(ctx, p.reason); err != nil {
		return nil, err
	}
	return p.inner.LaneConn(ctx, host, machine, generation, opts...)
}

// passwordPendingSessions builds n distinct PendingSession rows for one
// remote/helper connection — matching how nocx's own carried-over binding
// looks for a saved password host (session_readopt.go's field list: every
// one of SessionID, Generation, Host, ProfileID, HelperCommand and PaneID
// must be non-empty or Readopt refuses the binding before it ever reaches a
// carrier). n distinct ids on ONE host, because the measurement below is
// about the SEQUENTIAL cost the reconcile loop pays per pending row, not
// about n distinct hosts.
func passwordPendingSessions(n int) []content.PendingSession {
	out := make([]content.PendingSession, 0, n)
	for i := range n {
		out = append(out, content.PendingSession{
			SessionID:     "sess-" + string(rune('a'+i)),
			PaneID:        "pane-" + string(rune('a'+i)),
			Host:          "host.example",
			Account:       "u",
			ProfileID:     "profile-1",
			Generation:    syntheticArtifactHash,
			HelperCommand: "/fake/nocx-helper",
		})
	}
	return out
}

// The measurement the coordinator asked for: does reconcileSessions scale
// with N when every pending session's own credential fetch meets a sealed
// vault and nobody to ask?
//
// It must not: at this point in App.Start, Transport.Start has not run, so
// no client has attached and none can — clientsKnown is still at its zero
// value (nothing has called Vault.ClientsAttached, ever) and EnsureUnsealed
// answers from the "vault nobody reports presence to" branch
// (internal/vault/unlock.go's own name for it), which is the SAME branch a
// package that builds a vault of its own is always in — one raise,
// ErrNoUnlockClient, no suspension. Three pending sessions must therefore
// cost about what one does, not three times as much.
func TestReconcileSessions_PreListenPassRefusesPromptlyEvenWithNPendingSessions(t *testing.T) {
	const n = 3
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	coord := newCoordinator(t, provider)

	v := sealedVaultForReadopt(t)
	coord.reg.lanes = &vaultGatedLaneProvider{v: v, reason: "the ssh session needs its key", inner: provider}

	pending := passwordPendingSessions(n)
	routes := &stubRoutes{host: "host.example", cfg: helperConnection("u")}
	pass := readoptFixture(t, coord, routes, &stubAdopter{})
	// A bound short enough that the test is fast, long enough that a
	// suspend and an instant refusal are unmistakably different durations.
	pass.timeout = 300 * time.Millisecond

	rec := &recordingReconciler{pending: pending}
	start := time.Now()
	reconcileSessions(context.Background(), rec, coord.reg.inventories(), pass, time.Hour, quietLogger())
	elapsed := time.Since(start)

	if len(rec.applied) != n {
		t.Fatalf("judgements = %d, want %d (one per pending session)", len(rec.applied), n)
	}
	for _, j := range rec.applied {
		if j.Verdict != content.VerdictUnknown {
			t.Fatalf("session %s verdict = %q, want unknown — nobody could be asked yet", j.SessionID, j.Verdict)
		}
	}
	// Half of ONE session's own bound: an instant refusal costs
	// microseconds, so this is generous headroom for a loaded machine while
	// still catching the defect, which costs at minimum the full bound —
	// and n of them, sequentially, at least n times that.
	if budget := pass.timeout / 2; elapsed > budget {
		t.Fatalf("reconcileSessions took %s for %d pending sessions (budget %s) — "+
			"it is waiting on a client that cannot attach until it returns", elapsed, n, budget)
	}
}

// The mistake round 1 made, pinned so nothing reintroduces it: if ANYTHING
// tells the vault presence is being tracked before this pass runs — round
// 1's WSServer.PrimePresence did it by calling the same ClientsAttached(0)
// this test calls directly — EnsureUnsealed stops refusing and starts
// suspending (internal/vault/presence.go's awaitClient, tracked branch), and
// since nothing can attach before the pass returns, every session pays its
// own bound in full. This is why PrimePresence was removed rather than kept:
// it did not fix anything at the point it ran, it only made the same failure
// slower.
func TestReconcileSessions_IfPresenceIsPrimedTooEarlyEachSessionCostsItsBound(t *testing.T) {
	const n = 3
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}
	coord := newCoordinator(t, provider)

	v := sealedVaultForReadopt(t)
	v.ClientsAttached(0) // the exact call PrimePresence made, this early
	coord.reg.lanes = &vaultGatedLaneProvider{v: v, reason: "the ssh session needs its key", inner: provider}

	pending := passwordPendingSessions(n)
	routes := &stubRoutes{host: "host.example", cfg: helperConnection("u")}
	pass := readoptFixture(t, coord, routes, &stubAdopter{})
	pass.timeout = 80 * time.Millisecond

	rec := &recordingReconciler{pending: pending}
	start := time.Now()
	reconcileSessions(context.Background(), rec, coord.reg.inventories(), pass, time.Hour, quietLogger())
	elapsed := time.Since(start)

	// (n-1)*bound rather than n*bound: the vault coalesces one prompt per
	// pending state (internal/unlock.go's own "one raise, one answer"), and
	// a session whose own EnsureUnsealed call lands in the narrow window
	// between the previous one's abandon() and its cleanup joins that
	// ALREADY-ending prompt instead of raising a fresh one — costing it
	// microseconds rather than the full bound. That is real vault behaviour
	// under this test's own back-to-back calls, not the defect under test,
	// so the threshold allows for exactly one session finishing that way
	// and still requires every OTHER one to have paid its bound in full.
	want := time.Duration(n-1) * pass.timeout
	if elapsed < want-pass.timeout/2 {
		t.Fatalf("elapsed = %s, want at least close to (n-1)*bound (%s) — "+
			"priming presence before the pass no longer stalls it the way this test documents", elapsed, want)
	}
}

// The other half: a session the pre-listen pass could not judge is not
// abandoned. This drives the SAME retry App.Start now runs once
// Transport.Start has returned (a second reconcileSessions call, not a
// suspend inside the first) directly, because building a real Transport and
// a real client attach for this one fact would test the WebSocket rather
// than the retry.
func TestReconcileSessions_ASessionLeftPendingIsTakenBackOnceAttachedAndUnsealed(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	// A REAL session, opened while the vault is unsealed (a person is
	// present, exactly as they were when they first connected this saved
	// password host) — the shell this whole test is about taking back.
	v := freshlySetUpVaultForReadopt(t)
	first := newCoordinator(t, provider)
	first.reg.lanes = &vaultGatedLaneProvider{v: v, reason: "the ssh session needs its key", inner: provider}
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	// The restart: a fresh coordinator over the SAME far helper (sharedHelperPeer
	// keeps one session service for every lane, matching what the bridge
	// actually does), and the vault reseals — a fresh process, same as a
	// real restart's file-backed vault answers "sealed" until the passphrase
	// is entered again.
	v.Seal()
	if v.State() != vault.StateSealed {
		t.Fatalf("vault state = %v, want sealed after the simulated restart", v.State())
	}
	second := newCoordinator(t, provider)
	second.reg.lanes = &vaultGatedLaneProvider{v: v, reason: "the ssh session needs its key", inner: provider}
	pass := readoptFixture(t, second, routesFor(binding), &stubAdopter{})
	pass.timeout = 300 * time.Millisecond
	pending := []content.PendingSession{binding}

	// PASS ONE: the pre-listen pass. No client, sealed vault — refused
	// promptly, exactly like the test above, and left pending rather than
	// deleted (VerdictUnknown never sweeps the row).
	firstPass := &recordingReconciler{pending: pending}
	reconcileSessions(context.Background(), firstPass, second.reg.inventories(), pass, time.Hour, quietLogger())
	if len(firstPass.applied) != 1 || firstPass.applied[0].Verdict != content.VerdictUnknown {
		t.Fatalf("first pass = %+v, want exactly one unknown verdict", firstPass.applied)
	}

	// A client has now attached (Transport.Start's own trailing
	// notePresence, and then the real connection — neither reproduced here,
	// since what matters is only their effect: presence is known) and
	// unsealed the vault (the person's one gesture).
	v.ClientsAttached(1)
	if err := v.Unseal(context.Background(), vault.UnsealRequest{Passphrase: "hunter2"}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// PASS TWO: App.Start's retry, run exactly as Start runs it — the same
	// reconciler is asked again (session_reconcile.go's own contract:
	// idempotent, and a session that stayed pending is repeated), and this
	// time EnsureUnsealed's first branch answers before ever reaching a
	// requester: the vault is simply unsealed now.
	secondPass := &recordingReconciler{pending: pending}
	reconcileSessions(context.Background(), secondPass, second.reg.inventories(), pass, time.Hour, quietLogger())

	if len(secondPass.applied) != 1 || secondPass.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("retry pass = %+v, want exactly one live verdict", secondPass.applied)
	}
	sid := session.ID(binding.SessionID)
	if _, err := second.sess.Get(sid); err != nil {
		t.Fatalf("the retried session is not in the registry sessions.live reads: %v", err)
	}
}

// unsealsThenBlocksRequester models exactly what this bead measured against
// the real container: a caller unseals the vault OUTSIDE the unlock-prompt
// handshake — e2e/ssh-helper-happy-path.spec.ts's unsealIfSealed calls
// vault.unseal directly and never vault.unlockResolved — while an
// EnsureUnsealed call is already suspended waiting for THIS requester to
// answer. Unseal itself resolves no pending ask (internal/vault/vault.go's
// Unseal touches rootKey and wakeAutoSeal, nothing in unlock.go), so the
// requester goes on blocking until its caller's own context ends — the
// measured "context deadline exceeded" 15s after the log's own "vault
// unsealed" line, from App.retryVaultSealedSessions's single-attempt
// predecessor.
type unsealsThenBlocksRequester struct {
	v *vault.Vault
}

func (r unsealsThenBlocksRequester) RequestUnlock(ctx context.Context, reason string) error {
	_ = r.v.Unseal(context.Background(), vault.UnsealRequest{Passphrase: "hunter2"})
	<-ctx.Done()
	return ctx.Err()
}

// The poll App.retryVaultSealedSessions runs (short, repeated attempts
// rather than one long suspend) is what catches the exact race the test
// above cannot happen in this shape: an attempt suspended on an ask nobody
// will ever resolve, while the vault gets unsealed some OTHER way in the
// meantime. This drives that shape directly — the first attempt is lost
// exactly as measured, and the second, a FRESH EnsureUnsealed call, finds
// the vault already unsealed (State is checked before any requester is
// touched) and needs nobody's answer at all.
func TestReconcileSessions_APollOfShortAttemptsCatchesARawUnsealASuspendedOneMissed(t *testing.T) {
	svc := sharedHelperService()
	provider := &fakeLaneProvider{peer: sharedHelperPeer(svc)}

	v := freshlySetUpVaultForReadopt(t)
	first := newCoordinator(t, provider)
	first.reg.lanes = &vaultGatedLaneProvider{v: v, reason: "the ssh session needs its key", inner: provider}
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	v.Seal()
	v.SetUnlockRequester(unsealsThenBlocksRequester{v: v})
	v.ClientsAttached(1) // a client IS attached, matching Transport.Start having already returned

	second := newCoordinator(t, provider)
	second.reg.lanes = &vaultGatedLaneProvider{v: v, reason: "the ssh session needs its key", inner: provider}
	pass := readoptFixture(t, second, routesFor(binding), &stubAdopter{})
	pass.timeout = 100 * time.Millisecond // vaultSealedRetryAttempt's own shape: short, so this is a poll
	pending := []content.PendingSession{binding}

	const maxAttempts = 5 // comfortably more than the two this needs
	var last content.SessionVerdict
	for i := range maxAttempts {
		rec := &recordingReconciler{pending: pending}
		reconcileSessions(context.Background(), rec, second.reg.inventories(), pass, time.Hour, quietLogger())
		if len(rec.applied) != 1 {
			t.Fatalf("attempt %d: judgements = %+v, want exactly one", i, rec.applied)
		}
		last = rec.applied[0].Verdict
		if last == content.VerdictLive {
			break
		}
	}
	if last != content.VerdictLive {
		t.Fatalf("verdict after %d short attempts = %q, want live — the poll never caught the raw unseal", maxAttempts, last)
	}
}
