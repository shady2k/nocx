package app

// Machine consent, and only machine consent, decides whether a live helper
// shell is taken back after a coordinator replacement (ALX-205 B1; ADR-727g63tr
// decision 1's "a shell already open keeps the integration it opened with
// until it is reopened").
//
// Reading the host's saved integration method here was the defect these tests
// pin: a host whose method had been changed to raw or script between two runs
// lost the re-adoption of a shell that was still running, although the machine
// had granted the helper and nothing had withdrawn that grant. The saved method
// governs NEW opens and the helper panels; the consent record governs opening a
// helper channel to a machine (ADR-0034, ADR-0068, ADR-0105).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
)

// secondRunWithHostMode is the replacement coordinator every test below reads
// through, with one saved method recorded for the binding's machine. The method
// is deliberately the variable under test: whichever value it holds, the
// machine's own answer must decide a re-adoption.
func secondRunWithHostMode(t *testing.T, provider *fakeLaneProvider, binding content.PendingSession, mode hostmode.Mode) *coordinator {
	t.Helper()
	if binding.Fingerprint == "" {
		t.Fatal("the fixture's binding recorded no fingerprint; nothing below would be a question about a machine")
	}
	second := newCoordinator(t, provider)
	second.reg.modes = hostmode.NewStore(
		log.NewSlogAdapter(discardLogger(t)),
		storage.NewDocumentStore(t.TempDir()),
		"host-modes.json",
	)
	mustRecordHostMode(t, second.reg.modes, binding.Fingerprint, mode)
	return second
}

// T-B1a: the machine granted the helper; the host's saved method says Raw.
// A shell that is already running is taken back, because Raw governs the next
// open and the panels, not the permission a live helper channel rides on.
func TestTB1aAGrantedMachineIsReadoptedThoughTheHostsSavedMethodIsRaw(t *testing.T) {
	svc := sharedHelperService(t)
	provider := &fakeLaneProvider{owner: t.Name(), peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	second := secondRunWithHostMode(t, provider, binding, hostmode.Raw)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger(t))

	if len(rec.applied) != 1 || rec.applied[0].Verdict != content.VerdictLive {
		t.Fatalf("verdict = %+v, want live: the machine granted the helper, and the saved Raw method governs new opens, not a shell that is already running", rec.applied)
	}
	if ids := adopter.adoptedIDs(); len(ids) != 1 || string(ids[0]) != binding.SessionID {
		t.Fatalf("adopted = %v, want [%s] (%v)", ids, binding.SessionID, adopter.failure())
	}
}

// T-B1b: the same rule through the staging seam the vault-readiness poll uses.
// Script is an answer about new opens, not a withdrawal of the machine's grant.
func TestTB1bStagingAReadoptionIgnoresTheHostsSavedScriptMethod(t *testing.T) {
	svc := sharedHelperService(t)
	provider := &fakeLaneProvider{owner: t.Name(), peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	second := secondRunWithHostMode(t, provider, binding, hostmode.Script)
	pass := readoptFixture(t, second, routesFor(binding), &stubAdopter{})
	if err := pass.stageReadopt(context.Background(), binding); err != nil {
		t.Fatalf("staging the re-adoption failed: %v — a Script method does not withdraw the machine's grant, and the helper channel is what that grant permits", err)
	}
	h := second.reg.takeReadoptHelper(session.ID(binding.SessionID))
	if h == nil {
		t.Fatal("stageReadopt reported success but staged no helper: the re-adoption has nothing to attach with")
	}
	h.mu.Lock()
	closeStagedReadoptHelper(h)
	h.mu.Unlock()
}

// T-B1c: a denial still stops the helper channel, and the refusal must name the
// machine's answer rather than the host's saved method — the method is not what
// refused.
func TestTB1cADeniedMachineStopsReadoptionWhateverTheHostsMethodSays(t *testing.T) {
	svc := sharedHelperService(t)
	provider := &fakeLaneProvider{owner: t.Name(), peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	second := secondRunWithHostMode(t, provider, binding, hostmode.Script)
	second.consent = seedAnsweredDocument(t, t.TempDir(), binding.Fingerprint, consent.Denied)
	second.reg.consent = second.consent
	lanesBefore := provider.laneCount()

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger(t))

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict != content.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown: a denied machine must stop the channel, and a refusal is never a verdict of absence", got.Verdict)
	}
	if !strings.Contains(got.Detail, "this machine no longer consents to the nocx helper") {
		t.Fatalf("detail = %q, want the machine's denial named — the saved Script method is not what refused", got.Detail)
	}
	if strings.Contains(got.Detail, "integration method") {
		t.Fatalf("detail = %q, names the host's saved method; a re-adoption asks only the machine's consent", got.Detail)
	}
	if provider.laneCount() != lanesBefore {
		t.Fatalf("%d lanes opened to a denied machine, want none", provider.laneCount()-lanesBefore)
	}
	if ids := adopter.adoptedIDs(); len(ids) != 0 {
		t.Fatalf("adopted = %v, want none", ids)
	}
}

// T-B1d: a machine nobody has answered is still not a grant, and the sentence
// must send the person to the ask ("reconnect … to be asked") rather than blame
// a Raw method for a decision the machine never made.
func TestTB1dAnUnansweredMachineIsNotReadoptedThoughTheHostsSavedMethodIsRaw(t *testing.T) {
	svc := sharedHelperService(t)
	provider := &fakeLaneProvider{owner: t.Name(), peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()

	second := secondRunWithHostMode(t, provider, binding, hostmode.Raw)
	unanswered := consent.NewStore(
		log.NewSlogAdapter(discardLogger(t)),
		storage.NewDocumentStore(t.TempDir()),
		"consent.json",
	)
	second.consent = unanswered
	second.reg.consent = unanswered

	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	adopter := &stubAdopter{}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), adopter), time.Hour, quietLogger(t))

	if len(rec.applied) != 1 {
		t.Fatalf("judgements = %+v, want exactly one", rec.applied)
	}
	got := rec.applied[0]
	if got.Verdict != content.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown: nobody has answered for this machine", got.Verdict)
	}
	if !strings.Contains(got.Detail, "nocx has not been told whether it may use its helper on") ||
		!strings.Contains(got.Detail, "reconnect that connection to be asked") {
		t.Fatalf("detail = %q, want the unanswered-machine sentence that names the ask", got.Detail)
	}
	if strings.Contains(got.Detail, "integration method") {
		t.Fatalf("detail = %q, names the host's saved method; a re-adoption asks only the machine's consent", got.Detail)
	}
}
