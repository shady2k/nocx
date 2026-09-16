package app

// THE BEARER A PANE'S LAUNCH CARRIES IS THE BEARER ITS ADMISSION ACCEPTS
// (nocx-50w7p.16, AC1) — proved through the route the pane actually takes,
// rather than through the fixtures that stand beside it.
//
// Every half of this join has a test of its own already: the book peeks instead
// of consuming (worker_spawn_token_test.go), the endpoint admits a pane that
// presents the interval's bearer (worker_far_pane_test.go), and the bearer
// survives the helper's own wire (internal/helper/proto/tool_token_test.go).
// What none of them does is take the value OUT of a launch that really happened.
// The book tests seed the book by hand, so a coordinator that minted one
// bearer, sent a second and bound a third would pass every one of them — and
// that is the defect the whole carrier exists to make impossible.
//
// So this file opens a pane through the shipped opener, against a scripted
// daemon that answers `spawn-ssh` (the same route
// panescreen_ssh_owner_test.go opens one down), reads the bearer off the
// request THAT daemon decoded, and asks the real endpoint — real approval
// service, real authorizer, real socket — to admit a connection presenting it.
//
// WHAT IS SCRIPTED: the daemon's ssh half, because the real one dials somebody
// else's host and nothing here may. Everything between the two ends is the
// shipped path — destination resolution, the helper client, the host protocol,
// the session service's decode, and the launch the bearer rides in.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

// sshTokenPaneConfig is the destination these tests open a pane for: a named
// credential bound to the endpoint its profile names, built exactly as
// panescreen_ssh_owner_test.go builds one. Nothing here dials — the endpoint's
// scripted ssh half answers `spawn-ssh` without one — so the password is never
// presented anywhere.
func sshTokenPaneConfig() session.Config {
	return session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			Secrets:            rememberedPassword{value: openPasswordFixturePassword},
			SecretID:           "sec:launch-token:1",
			AuthorizedEndpoint: "host.example:22",
		},
	}
}

// sshTokenRoute is the shipped opener over a scripted daemon, plus the bearer
// book this opener records launches in — the two things every assertion below
// reads.
func sshTokenRoute(t *testing.T) (*localHelperOpener, *spawnTokens, *fakeLocalEndpoint) {
	t.Helper()
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	// The endpoint this build's generation is named for, so the local route
	// reaches THIS daemon rather than starting another — and its ssh half is
	// the scripted one that keeps what a spawn asked for.
	ep := startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	logger := log.NewSlogAdapter(discardLogger(t))
	lg := discardLogger(t)
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	rc, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(home+"/known_hosts"))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	book := &spawnTokens{}
	opener := &localHelperOpener{
		log:      lg,
		registry: reg,
		dir:      endpoint.Dir(home),
		installed: helperlocal.Installed{
			Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
		},
		sshTargets:  rc,
		spawnTokens: book,
	}
	return opener, book, ep
}

// sshTokenPaneOpened is what the two tests below share: one pane opened through
// the shipped route, and the bearer its launch carried as the DAEMON decoded it
// off the helper's wire.
//
// The bearer is read from the request and not from the coordinator's own
// params, which is the difference between "what we meant to send" and "what the
// launch carried": the second is the fact the far shell is staged with.
func sshTokenPaneOpened(t *testing.T, opener *localHelperOpener, ep *fakeLocalEndpoint) session.ID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, selected, err := opener.OpenHosted(ctx, sshTokenPaneConfig(), "")
	if err != nil || !selected {
		t.Fatalf("opening an ssh pane through this machine's helper: selected=%v err=%v", selected, err)
	}
	sid := opened.Session.ID()
	if _, encErr := panebind.Encode(string(sid)); encErr != nil {
		// A pane the far agent could not name is a pane whose bearer could
		// never be presented: the record that carries the name is validated on
		// both ends, and this is the end that mints the name.
		t.Fatalf("the daemon reported session %q, which no pane record can carry: %v", sid, encErr)
	}

	asks := ep.spawner.sshSpawns()
	if len(asks) != 1 {
		t.Fatalf("the daemon was asked to open %d ssh session(s), want exactly 1", len(asks))
	}
	launched := asks[0].AgentToolToken
	if _, encErr := panebind.EncodeToken(launched); encErr != nil {
		t.Fatalf("the launch carried %q as the pane's bearer, which the endpoint's own reader would refuse: %v",
			launched, encErr)
	}
	return sid
}

// launchedBearer is the value sshTokenPaneOpened validated, read back for the
// assertions below.
func launchedBearer(t *testing.T, ep *fakeLocalEndpoint) string {
	t.Helper()
	asks := ep.spawner.sshSpawns()
	if len(asks) != 1 {
		t.Fatalf("the daemon was asked to open %d ssh session(s), want exactly 1", len(asks))
	}
	return asks[0].AgentToolToken
}

func TestASpawnedPanesLaunchBearerIsTheOneItsAdmissionAccepts(t *testing.T) {
	opener, book, ep := sshTokenRoute(t)
	sid := sshTokenPaneOpened(t, opener, ep)
	launched := launchedBearer(t, ep)

	// THE BINDING THE LAUNCH MADE: the opener knows which session the bearer
	// was for only once the daemon reports one, and this is that entry.
	if got, ok := book.SpawnToken(sid); !ok || got != launched {
		t.Fatalf("the opener bound %q to pane %q, want the bearer the launch carried (%q)", got, sid, launched)
	}

	// ONE STAND PER OBSERVABLE. A session serves one caller at a time and a
	// connection's slot outlives the client's close by a moment, so the admitted
	// call below and the refused one get a stand each: on one stand the second
	// dial can be refused for the SLOT, and the sentence asserted there would be
	// the wrong one.
	admitted := farStandOverLaunch(t, book, sid)
	if got := admitted.paneToken(t, string(sid)); got != launched {
		t.Fatalf("the pane's interval admits with %q, want the bearer its launch carried (%q)", got, launched)
	}

	// ADMITTED, presenting the value that travelled: the far pane's own agent.
	env := admitted.callOnce(t, string(sid), launched)
	if env.Error != nil {
		t.Fatalf("the pane's own agent was refused with the bearer its launch carried: %+v", env.Error)
	}
	if got := admitted.dispatch.last().RunContext.Session; got != string(sid) {
		t.Fatalf("the call ran under session %q, want the pane the record named (%q)", got, sid)
	}

	// AND REFUSED BY NAME for any other value — the same pane, the same live
	// interval, the same launch: only the bearer differs.
	refused := farStandOverLaunch(t, book, sid)
	env = refused.callOnce(t, string(sid), strings.Repeat("ab", 32))
	if env.Error == nil {
		t.Fatal("a connection presenting a value that is not the pane's bearer was admitted")
	}
	if !strings.Contains(env.Error.Data.Reason, "did not present the bearer nocx holds for the pane") {
		t.Fatalf("the wrong bearer was refused for %q, want the bearer's own sentence", env.Error.Data.Reason)
	}
	if got := refused.dispatch.count(); got != 0 {
		t.Fatalf("the dispatcher ran %d call(s) for a connection presenting the wrong bearer", got)
	}
}

// farStandOverLaunch is a coordinator-side chain for the pane a launch produced,
// holding the bearer book that launch recorded in: the shipped approval service,
// authorizer and tool endpoint, with the pane's interval bound to whatever the
// launch carried.
func farStandOverLaunch(t *testing.T, book *spawnTokens, sid session.ID) *farStand {
	t.Helper()
	stand := newFarStand(t, remoteSession(sid, "host.example", "dev", "SHA256:key-a"))
	stand.approval.SetSpawnTokens(book)
	stand.enrol(t, sid, "claude")
	return stand
}

// TestASpawnThatFailsLeavesNoBearerBehind — the other half of the same property,
// and the one that makes the binding above mean something: the opener records a
// pane's bearer only for a pane it actually got back.
//
// The failure is the daemon's own refusal, which is the shape a person meets
// when a far host will not give nocx a shell — so this is not a test of "nothing
// was minted". It asserts first that the launch DID carry a bearer (otherwise
// the emptiness below would be vacuous, and would pass for a coordinator that
// minted nothing at all), and then that a spawn that failed left nothing behind
// for a session that never existed.
func TestASpawnThatFailsLeavesNoBearerBehind(t *testing.T) {
	opener, book, ep := sshTokenRoute(t)
	ep.spawner.refuseSSH(errors.New("the far host closed the channel before a shell was opened"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, selected, err := opener.OpenHosted(ctx, sshTokenPaneConfig(), "")
	if err == nil {
		t.Fatalf("a refused ssh spawn was reported as a success (session %q)", opened.Session.ID())
	}
	if !selected {
		t.Fatal("the daemon's refusal was not reported as this opener's, so nothing here was tested")
	}

	asks := ep.spawner.sshSpawns()
	if len(asks) != 1 {
		t.Fatalf("the daemon was asked to open %d ssh session(s), want exactly 1", len(asks))
	}
	if asks[0].AgentToolToken == "" {
		t.Fatal("the failed launch carried no bearer, so there was nothing for this test to find left behind")
	}

	book.mu.Lock()
	held := len(book.byID)
	book.mu.Unlock()
	if held != 0 {
		t.Fatalf("a spawn that failed left %d bearer(s) behind for a pane that does not exist", held)
	}
}
