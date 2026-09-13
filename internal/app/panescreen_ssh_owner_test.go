package app

// AN SSH PANE THIS MACHINE'S HELPER OPENS MUST HAVE A READABLE SCREEN
// (nocx-50w7p.5).
//
// paneScreen.owner picks a route by asking what KIND a session is: KindLocal is
// this machine's daemon's, everything else is a helper-hosted remote session if
// the registry holds a host for it. That reading was sound while the two
// coincided — a pane was local, or its channel came from a helper on the far
// host, and never both.
//
// 08e90002 is the case where they diverge. An ssh pane is opened by THIS
// machine's daemon, so its DESTINATION is remote (KindRemote is right, and the
// registry must report it) while its CARRIER is the daemon here. The remote
// half of owner's question then has nothing to answer with — the far-helper
// registry knows only panes IT opened — so the pane falls to errNoPaneRuntime
// and its screen can never be read. The backend is meant to answer every
// session; this one it cannot answer at all.
//
// ONE OPENER OPENS AND IS ASKED. The pane is opened through the same
// localHelperOpener that owner consults, because ownership is that opener's
// fact: a stand-in that opened the pane plus a different, hollow opener asked
// about it would agree on nothing, and the test would assert a state no code
// can reach.
//
// The destination carries a named credential because resolution must produce a
// target at all — WireIdentity sets Identity.Credential only when the resolved
// endpoint has one (ssh_helpertarget.go:604-609), and the daemon refuses an
// ssh spawn whose destination names none. Nothing here dials: the endpoint's
// scripted spawner answers `spawn-ssh` without one, so the password is never
// presented anywhere.

import (
	"context"
	"errors"
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
)

func TestAnSSHPaneOpenedByThisMachinesHelperAnswersItsScreen(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	src := fakeArtifacts{payload: syntheticPayload}
	gen := src.hash()
	// The endpoint this build's generation is named for, so the local route
	// reaches THIS daemon rather than starting another. Its spawner answers
	// `spawn-ssh` too, which is what lets an ssh pane be held here at all.
	_ = startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	logger := log.NewSlogAdapter(discardLogger())
	lg := discardLogger()
	reg := session.New(logger, &reachPTYFactory{stub: pty.NewStub(logger)})
	rc, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(home+"/known_hosts"))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	opener := &localHelperOpener{
		log:      lg,
		registry: reg,
		dir:      endpoint.Dir(home),
		installed: helperlocal.Installed{
			Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
		},
		sshTargets: rc,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, selected, err := opener.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			// A NAMED credential, which is what puts a reference on the wire:
			// the helper holds no secret, so the destination must say which
			// one to ask this coordinator for.
			Secrets:  rememberedPassword{value: openPasswordFixturePassword},
			SecretID: "sec:screen-proof:1",
			// A linked credential may only be spent on the endpoint its
			// profile names, and the coordinator is the party that checks it —
			// so the target has to be the one this credential is bound to.
			AuthorizedEndpoint: "host.example:22",
		},
	}, "")
	if err != nil || !selected {
		t.Fatalf("opening an ssh pane through this machine's helper: selected=%v err=%v", selected, err)
	}
	sid := opened.Session.ID()
	if got := opened.Session.Kind(); got != session.KindRemote {
		t.Fatalf("the pane's kind is %v, want KindRemote — its DESTINATION is remote, which is why the "+
			"owner must not read the kind as the carrier", got)
	}

	// THE OWNER, built as the composition root builds it: the registry, this
	// machine's route, and the far-helper registry.
	ps := newPaneScreen(lg, reg, opener, &helperRegistry{})

	// Available is the enrolment probe — the question a pane asks before a
	// watch exists over it — and it routes through owner exactly as Screen
	// does.
	err = ps.Available(string(sid))
	if errors.Is(err, errNoPaneRuntime) {
		t.Fatalf("reading this pane's screen answered %q, but this machine's helper IS holding its "+
			"terminal: the owner chose the route by the session's KIND, which says where the "+
			"DESTINATION is, rather than by the helper that opened it", err)
	}
	if err != nil {
		t.Fatalf("the pane's screen was refused for another reason: %v", err)
	}
	t.Logf("MEASURED the screen read for an ssh pane carried by this machine's helper succeeded")
}
