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

func TestASpawnMintedBearerIsTakenOnceAndOnlyOnce(t *testing.T) {
	book := &spawnTokens{}
	book.record("0f9b4d7159d38afee9648a843654516f", "aa")

	token, ok := book.SpawnToken("0f9b4d7159d38afee9648a843654516f")
	if !ok || token != "aa" {
		t.Fatalf("the book answered %q, %v, want the bearer the launch was given", token, ok)
	}
	// CONSUMED ON THE READ: from here the interval holds it, and a second reader
	// would be a second owner of one bearer.
	if token, ok := book.SpawnToken("0f9b4d7159d38afee9648a843654516f"); ok {
		t.Fatalf("the book handed %q to a second reader", token)
	}
	// And it invents nothing: a session nothing launched has no bearer here.
	if token, ok := book.SpawnToken("11111111111111111111111111111111"); ok {
		t.Fatalf("the book produced %q for a session nobody launched", token)
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
	// The book is spent, and the interval is what holds the bearer now.
	if _, ok := book.SpawnToken(farPaneP); ok {
		t.Fatal("the launch's bearer stayed in the book after the interval took it")
	}
}
