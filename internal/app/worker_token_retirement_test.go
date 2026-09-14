package app

// WHAT RETIRES A PANE'S BEARER, AND WHAT A RETIRED BEARER MEETS
// (nocx-50w7p.16, AC2).
//
// A bearer is minted with a launch and bound when the pane's agent is approved,
// and the reason it is BOUND rather than minted per interval is that the far
// shell staged one value and can never learn a second. Its retirement is the
// other half of that fact: the interval it was bound to has to be the interval
// that ends, and both ways an answer stops holding — the session finishing, and
// a person revoking the approval — have to refuse the bearer, by name.
//
// THE REFUSAL IS ASSERTED BY ITS SENTENCE, not merely as "an error". Two
// different faults reach a far agent through this arm and they send a reader to
// different places: a pane nocx does not hold, and a pane whose admission is
// over. A test that accepted any error would pass with the wrong one, which is
// the shape this epic's refusal work exists to remove.
//
// ONE STAND PER OBSERVABLE, and this file learned it the hard way. A session
// serves ONE caller at a time and a connection's slot is released when its serve
// loop ends — a moment after the client sees its close — so a SECOND connection
// to the same session, in the same stand, can be refused for the slot rather
// than for the fact the test is about. Measured: with the paired-success call
// and the connection under test on one stand, a full-package run failed with
// "another call from this session is still running" where the test expected the
// bearer's sentence. Every observable below therefore gets its own stand, and
// the stands are the same fixture — the same pane, the same launch's bearer,
// the same approval service — so nothing is being compared across two different
// worlds.
//
// WHY THE TWO MUTATIONS DIFFER, measured rather than assumed. A revocation
// removes the answer from the durable store AND ends the interval, and the
// admission path re-reads that store on every decision — so ending the interval
// is NOT what refuses a revoked bearer; the store already does. Measured: with
// the interval-ending loop removed from ForgetAgentAccess, the refusal half of
// this file still passes, and the connection half is what fails. What only the
// ended interval produces is the CLOSE of a connection that was admitted before
// the revocation (ADR-0058 admits once per connection), and that is the
// assertion that carries the mutation for revocation. A session ending revokes
// nothing: the answer is still in the store and still Granted, so nothing but
// the interval's end refuses that bearer, and there the mutation lands on the
// refusal itself.

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
)

// The machine this file's pane is on: the four facts sessionDomain derives from
// a session's own route, spelled once.
const (
	farPaneHost    = "build.example.com"
	farPaneAccount = "deploy"
	farPaneHostKey = "SHA256:key-a"
)

// farPaneSession is the pane both retirements act on.
func farPaneSession() session.Session {
	return remoteSession(farPaneP, farPaneHost, farPaneAccount, farPaneHostKey)
}

// farPaneDomainOf asks THE BACKEND'S OWN derivation which machine this pane's
// answer was keyed to, rather than restating it: the revocation below addresses
// a row the backend itself listed, so a test that spelled the domain out again
// could revoke the wrong one and still look right.
func farPaneDomainOf(t *testing.T, stand *farStand, sid session.ID) transport.MachineFacts {
	t.Helper()
	domain, err := sessionDomain(stand.sessions, sid)
	if err != nil {
		t.Fatalf("deriving the pane's machine the way the backend does: %v", err)
	}
	return machineFacts(domain)
}

// launchBearerBoundPaneFacts is what every stand in this file starts from: one
// far pane, one live interval, and the launch's bearer bound to it — with
// NOTHING admitted yet, so the first connection a test opens is the one it means
// to be about.
type boundPaneFacts struct {
	stand    *farStand
	sid      session.ID
	launched string
	agent    string
}

func launchBearerBoundPane(t *testing.T) boundPaneFacts {
	t.Helper()
	stand := newFarStand(t, farPaneSession())
	agent := fakeAgent(t, "far-pane-agent")
	launched := strings.Repeat("cd", 32)
	book := &spawnTokens{}
	book.record(farPaneP, launched)
	stand.approval.SetSpawnTokens(book)
	stand.enrol(t, farPaneP, agent)
	return boundPaneFacts{stand: stand, sid: farPaneP, launched: launched, agent: agent}
}

// admits is the PAIRED SUCCESS: the pane's own bearer, presented on this stand's
// one and only connection, is admitted and the call runs under the pane.
func (p boundPaneFacts) admits(t *testing.T) {
	t.Helper()
	env := p.stand.callOnce(t, string(p.sid), p.launched)
	if env.Error != nil {
		t.Fatalf("before anything retired it, the launch's bearer was refused: %+v", env.Error)
	}
	if got := p.stand.dispatch.last().RunContext.Session; got != string(p.sid) {
		t.Fatalf("the call ran under session %q, want the pane the record named (%q)", got, p.sid)
	}
}

// TestASessionEndRetiresThePanesBearerByName — the pane's own session finishing.
//
// The registry still holds the session and nocx is still watching the pane,
// which is the point: what ended is the admission interval, so the refusal has
// to name that and not the pane's identity.
func TestASessionEndRetiresThePanesBearerByName(t *testing.T) {
	launchBearerBoundPane(t).admits(t)

	// A stand of its own for the retirement, so the refusal below is the one
	// this test is about rather than the caller slot.
	retired := launchBearerBoundPane(t)
	retired.stand.approval.SessionEnded(string(retired.sid))

	env := retired.stand.callOnce(t, string(retired.sid), retired.launched)
	if env.Error == nil {
		t.Fatal("the bearer of a session that has ended was admitted")
	}
	if !strings.Contains(env.Error.Data.Reason, "holds no live admission interval") {
		t.Fatalf("the retired bearer was refused for %q, want the interval's own sentence", env.Error.Data.Reason)
	}
	if got := retired.stand.dispatch.count(); got != 0 {
		t.Fatalf("the dispatcher ran %d call(s) for a session that had ended", got)
	}
}

// TestARevokedAnswersBearerIsRefusedByName — the Settings surface's revocation,
// from the far agent's side: the bearer it holds is refused, and refused for the
// reason that says the pane's admission is over rather than for one that sends
// the person hunting an orchestration fault.
func TestARevokedAnswersBearerIsRefusedByName(t *testing.T) {
	launchBearerBoundPane(t).admits(t)

	revoked := launchBearerBoundPane(t)
	revokeFarPaneAnswer(t, revoked)

	env := revoked.stand.callOnce(t, string(revoked.sid), revoked.launched)
	if env.Error == nil {
		t.Fatal("the bearer of a revoked answer was admitted")
	}
	if !strings.Contains(env.Error.Data.Reason, "holds no live admission interval") {
		t.Fatalf("the revoked bearer was refused for %q, want the interval's own sentence", env.Error.Data.Reason)
	}
}

// TestARevocationClosesTheConnectionItAdmitted is the half a refusal cannot
// express, and the one the mutation for this case lands on.
//
// A tool connection is admitted ONCE (ADR-0058) and nothing re-reads the store
// per call, so the approval a revoked connection carries is the approval it was
// let in under. Without the interval's end, the refusal below would still hold
// — the store no longer answers Granted and the admission path re-reads it —
// and the connection would go on serving a grant nobody holds.
func TestARevocationClosesTheConnectionItAdmitted(t *testing.T) {
	pane := launchBearerBoundPane(t)

	// THE CONNECTION UNDER TEST IS ALSO THE PAIRED SUCCESS: it is admitted and
	// proved admitted by being served, which is what makes "it was closed" a
	// statement about a live connection rather than one that never got in.
	conn := pane.stand.dialWithToken(t, string(pane.sid), pane.launched)
	if env := pane.stand.answerFrom(t, conn); env.Error != nil {
		t.Fatalf("the pane's own bearer was refused before the revocation: %+v", env.Error)
	}

	revokeFarPaneAnswer(t, pane)

	expectClosed(t, conn, "a revoked far pane's admitted connection")
}

// revokeFarPaneAnswer performs the revocation a person performs in Settings,
// addressed at the answer this pane actually holds: the same executable its
// enrolment resolved, and the machine the backend derives from the pane's own
// route.
func revokeFarPaneAnswer(t *testing.T, pane boundPaneFacts) {
	t.Helper()
	executable, err := agentapproval.IdentityForExecutable(pane.agent)
	if err != nil {
		t.Fatalf("identify the revoked agent: %v", err)
	}
	forgotten, revokeErr := pane.stand.approval.ForgetAgentAccess(
		executable.Path, executable.SHA256, workerTestWorkspace, farPaneDomainOf(t, pane.stand, pane.sid))
	if revokeErr != nil {
		t.Fatalf("revoking the far pane's answer: %v", revokeErr)
	}
	if !forgotten {
		t.Fatal("the revocation forgot nothing, so this pane never held the answer it revoked")
	}
}

// TestAFarPaneWhoseIntervalHoldsNoBearerRefusesEverybody — the mint's own
// failure, at the seam the wire cannot reach.
//
// mintToolToken answers a failed 32-byte read with the EMPTY string, and its
// comment says what that means: a bearer no connection can present. The
// comparison alone does not honour that, because ConstantTimeCompare answers 1
// for two empty slices — so an interval whose mint failed and a connection that
// presented nothing would have AGREED, and admitted a pane on no bearer at all.
//
// The wire is not where this can be reached (the endpoint reads the bearer as a
// fixed-length line and refuses a connection that stops short of one, which
// TestAFarConnectionIsRefusedWithoutThePanesCurrentBearer's "nothing presented"
// case pins), so the proof belongs at the seam: an approval whose live interval
// holds no bearer.
func TestAFarPaneWhoseIntervalHoldsNoBearerRefusesEverybody(t *testing.T) {
	stand := newFarStand(t, farPaneSession())

	bearerless, err := newToolAuthorizer(
		nil, stand.sessions, stand.watched, emptyWorkerRecord(), workerTestWorkspace, bearerlessApproval{})
	if err != nil {
		t.Fatalf("newToolAuthorizer: %v", err)
	}
	for _, presented := range []string{"", strings.Repeat("ab", 32)} {
		_, _, _, err := bearerless.admittedPeer(toolendpoint.Peer{Pane: string(farPaneP), Token: presented})
		if !errors.Is(err, toolendpoint.ErrBearerRefused) {
			t.Fatalf("a pane whose interval holds no bearer admitted a connection presenting %q: err = %v",
				presented, err)
		}
	}
}

// bearerlessApproval is an approval seam with ONE live interval that holds no
// bearer: the state a failed mint leaves behind, which nothing in production can
// produce on demand (rand.Read failing is not a fixture).
type bearerlessApproval struct{}

func (bearerlessApproval) Interval(session.ID, string) (toolendpoint.AdmissionEpoch, bool) {
	return 7, true
}

func (bearerlessApproval) IntervalToken(session.ID, string) (toolendpoint.AdmissionEpoch, string, bool) {
	return 7, "", true
}
