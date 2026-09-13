package app

// THE BEARER A LAUNCH CARRIES IS THE BEARER THE INTERVAL ADMITS WITH (nocx-50w7p.16).
//
// This is the join the whole carrier exists for, and it is a join rather than a
// field because of an ORDER: the far shell is staged with a bearer before the
// pane has a session id — the helper mints that and reports it in the spawn's
// result — while the interval that gives the bearer its meaning is created later
// still, when the pane's agent enrols and a person answers. So the opener mints
// with the launch, remembers it against the session the helper reported, and the
// interval BINDS it. A bearer minted at approval would be one the staged
// configuration could never have learned, and the far agent would be refused for
// presenting a value that was correct when it was written.

import (
	"strings"
	"testing"
)

// TestALaunchsBearerSurvivesEveryApprovalOfItsSession — the book PEEKS rather
// than takes, and re-approval is why: a session can hold two intervals with
// identical approval (withdrawn and enrolled again) and both belong to the same
// launch. The far shell staged one value and cannot learn a second, so an
// interval that rotated the bearer would refuse the pane's own agent for
// presenting the value that was correct when it was written.
func TestALaunchsBearerSurvivesEveryApprovalOfItsSession(t *testing.T) {
	book := &spawnTokens{}
	book.record(farPaneP, "aa")

	if token, ok := book.SpawnToken(farPaneP); !ok || token != "aa" {
		t.Fatalf("the book answered %q, %v, want the bearer the launch was given", token, ok)
	}
	if token, ok := book.SpawnToken(farPaneP); !ok || token != "aa" {
		t.Fatalf("a second interval was answered %q, %v, want the SAME launch's bearer", token, ok)
	}
	if token, ok := book.SpawnToken("11111111111111111111111111111111"); ok {
		t.Fatalf("the book produced %q for a session nobody launched", token)
	}

	// WHAT RETIRES IT IS THE SESSION ENDING, which is the one event that makes
	// the far pane and its staged configuration gone together.
	book.Forget(farPaneP)
	if token, ok := book.SpawnToken(farPaneP); ok {
		t.Fatalf("the book still holds %q after the session ended", token)
	}
}

// TestAReApprovalKeepsTheLaunchesBearer — the same property over a real
// endpoint: approve, withdraw, approve again, and the interval still admits with
// the value the launch staged.
func TestAReApprovalKeepsTheLaunchesBearer(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	launched := strings.Repeat("cd", 32)
	book := &spawnTokens{}
	book.record(farPaneP, launched)
	stand.approval.SetSpawnTokens(book)

	stand.enrol(t, farPaneP, "claude")
	if got := stand.paneToken(t, string(farPaneP)); got != launched {
		t.Fatalf("the first interval holds %q, want the launch's bearer", got)
	}

	// WITHDRAWN, as a revocation is: the interval ends, the launch does not.
	stand.approval.Forget(farPaneP)
	stand.enrol(t, farPaneP, "claude")

	if got := stand.paneToken(t, string(farPaneP)); got != launched {
		t.Fatalf("after re-approval the interval holds %q, want the SAME launch's bearer", got)
	}
	if env := stand.callOverWithToken(t, string(farPaneP), launched); env.Error != nil {
		t.Fatalf("the pane's staged bearer was refused after re-approval: %+v", env.Error)
	}
}

// TestTheIntervalAdmitsWithTheBearerItsLaunchCarried — the whole join, over a
// real endpoint: a switchboard that took the launch's bearer admits a connection
// presenting it, and the interval holds that value rather than one it minted.
func TestTheIntervalAdmitsWithTheBearerItsLaunchCarried(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))

	// THE LAUNCH: a bearer minted into the spawn's params, bound to the session
	// the helper reported — before anyone had answered anything about this pane.
	launched := strings.Repeat("cd", 32)
	book := &spawnTokens{}
	book.record(farPaneP, launched)
	stand.approval.SetSpawnTokens(book)

	stand.enrol(t, farPaneP, "claude")

	if got := stand.paneToken(t, string(farPaneP)); got != launched {
		t.Fatalf("the interval holds %q, want the bearer the launch carried", got)
	}
	// AND THAT IS WHAT ADMITS: the value the far shell staged, presented by the
	// pane's own bridge, is the value the endpoint accepts.
	if env := stand.callOverWithToken(t, string(farPaneP), launched); env.Error != nil {
		t.Fatalf("the launch's own bearer was refused: %+v", env.Error)
	}
}

// TestASessionEndingDropsWhatItsLaunchLeft — the cleanup through the REAL
// wiring, not through the book's own method: the approval service is what sees a
// session end, and a pane nobody enrolled would otherwise keep its entry for the
// life of the coordinator.
func TestASessionEndingDropsWhatItsLaunchLeft(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	launched := strings.Repeat("ef", 32)
	book := &spawnTokens{}
	book.record(farPaneP, launched)
	stand.approval.SetSpawnTokens(book)
	stand.enrol(t, farPaneP, "claude")

	stand.approval.SessionEnded(string(farPaneP))

	if token, ok := book.SpawnToken(farPaneP); ok {
		t.Fatalf("the session's end left %q behind", token)
	}
}
