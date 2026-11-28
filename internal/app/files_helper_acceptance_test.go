//go:build nocx_local_ssh

package app

// The app-level SSH fixture runs the local helper's real protocol, SSH
// service, and session service over a socket pair. The fixture server can
// serve SFTP, and the app answers the helper's host-key and credential
// requests through the same reverse handlers used in production.

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/helper/sshconn"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

const (
	filesFixtureSecret = "sec:helper-files:1"
	filesFixtureUser   = "dev"
	filesFixtureGenera = "0123456789abcdef"
)

// filesStand assembles the local helper, the coordinator, and an SSH fixture.
type filesStand struct {
	opts []ssh.ConnectOption
	// khPath is the coordinator's known_hosts. It is exposed because a ROUTE
	// has more than one host in it: a test that adds a bastion has to record
	// that bastion's key too, and the coordinator's own verdict is what decides.
	khPath string
	// opener and routes are the two halves of this machine's helper exactly as
	// the composition root builds them. They are exposed because a test of the
	// open path uses hostedOpeners for the selection probe and pane spawn,
	// with install and exec leases available through the same routes.
	opener *localHelperOpener
	routes installLeaseRoutes
	// prompts is the reverse side's prompt seam: the value a test sets an
	// asker on so a person's answer reaches the helper's questions. Nil-asker
	// is the stand's default, which refuses every ask with no_auth_channel —
	// the honest state for tests that raise no password.
	prompts *helperPrompt
	// registry is the session registry an opened pane is ADOPTED into. It is
	// what makes a hosted open an open rather than a spawn: the helper mints
	// the id, and this process takes the session under it.
	registry *session.Reg
	// modes is the HOST record the open path reads instead of the connection's
	// field (ADR-727g63tr decision 1). Every fixture this stand serves answers
	// a plain shell request and refuses a launch exec, which is the raw
	// method, so each one is recorded raw here; a test that opens under
	// another method overwrites its host's record before the open.
	modes    *hostmode.Store
	hostKeys interface {
		KnownHostsFingerprintsFor(context.Context, string, *ssh.ConnectConfig) ([]string, error)
	}
}

// askRecorder is the reverse side's credential resolver: it answers from a
// fixed value, or refuses with a sentinel. It records every reference asked
// for, which is what makes the sealed case's evidence specific.
type askRecorder struct {
	value  string
	refuse error

	mu   sync.Mutex
	refs []string
}

func (a *askRecorder) Resolve(_ context.Context, id credential.SecretID, _ credential.Stance) (credential.Secret, error) {
	a.mu.Lock()
	a.refs = append(a.refs, string(id))
	refuse := a.refuse
	a.mu.Unlock()
	if refuse != nil {
		return credential.Secret{}, refuse
	}
	return credential.NewSecret(a.value), nil
}

func (a *askRecorder) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.refs...)
}

// startFilesStand assembles the stack against an SSH fixture.
//
// extra names FURTHER destinations this stand dials — a second host the open
// seam asks for, a bastion a route goes through. They are fixtures like srv and
// not options of it, and the reason they are parameters rather than something a
// test arranges afterwards is the known_hosts file: the coordinator's verdict is
// taken from ONE file, built here, and a host whose key is not in it is a host
// the helper must ask its coordinator about rather than one it can trust.
func startFilesStand(t *testing.T, srv *pwSSHServer, secrets credential.Resolver, extra ...*pwSSHServer) *filesStand {
	t.Helper()
	return startFilesStandWith(t, srv, secrets, nil, extra...)
}

// startFilesStandWith is the same stand with the coordinator's observation
// callback, for a test that drives the pane's connection state through the
// panedriver: the events a real coordinator receives are the ones a
// pane.connection is rendered from, and a test that presses a pane's Reconnect
// has to have them.
func startFilesStandWith(t *testing.T, srv *pwSSHServer, secrets credential.Resolver, onEvent func(string, proto.ConnectionStateEvent), extra ...*pwSSHServer) *filesStand {
	t.Helper()
	logger := discardLogger(t)
	// The ssh client takes the harness's own logging seam (internal/log), the
	// same adapter app.go builds beside it.
	sshLog := log.NewSlogAdapter(logger)

	// The coordinator's client: it RESOLVES and it answers host-key questions.
	// The local helper owns the actual dial.
	_, khPath := homeForKnownHosts(t)
	writeKnownHostsFor(t, khPath, append([]*pwSSHServer{srv}, extra...)...)
	rc, err := ssh.NewReal(sshLog)
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// The helper: the real daemon-side client dials, the real ssh service is
	// registered on a real host protocol engine, and the carrier between the
	// two sides is a socketpair. This is sshsvc's own stand, assembled here.
	//
	// A pane uses the real session service registered beside the SSH service.
	helperEnd, coordEnd := net.Pipe()
	sshdialClient, err := sshdial.New(logger)
	if err != nil {
		t.Fatalf("sshdial.New: %v", err)
	}
	svc := sshsvc.New(sshconn.New(sshdialClient, log.NewSlogAdapter(logger)), logger)
	engine := host.New(helperEnd, helperEnd, filesFixtureGenera, "instance-1", logger)
	engine.Register(svc)
	sessions := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(filesFixtureGenera),
		SSHSpawner: helpersession.NewSSHSpawner(svc, logger),
		Log:        logger,
	})
	// The daemon ends with the test, so the sessions it holds do too
	// (nocx-1ebrs.66.1.29).
	t.Cleanup(sessions.Close)
	engine.Register(sessions)
	// The session service is BOUND to its connection, as cmd/nocx-helper binds
	// it: an attach names the subscriber as the connection it arrived on, and
	// the service refuses one from a connection it does not serve.
	bindSessions := sessions.Bind(engine)
	t.Cleanup(bindSessions)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = engine.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})

	prompts := &helperPrompt{log: logger}
	peer, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:              helperclient.NewSocketConn(coordEnd),
		ExpectHash:        filesFixtureGenera,
		SentinelTTL:       5 * time.Second,
		OnConnectionEvent: onEvent,
		Reverse:           helperReverseHandlers(rc, secrets, prompts, nil, logger),
		Log:               logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	// The opener in its "one connection, every pane" state: the connection is
	// already this machine's daemon's, which is the branch localHelperOpener
	// takes for every consumer after the first. Nothing here installs or
	// starts anything, because the daemon is in this process for the length of
	// this test.
	//
	// The resolver is wired because a PANE open resolves its destination here
	// before the helper is handed an address (the same split app.go makes at
	// setSSHTargets).
	opener := &localHelperOpener{log: logger}
	opener.installed = helperInstalledStub()
	opener.dir = t.TempDir()
	opener.client = peer
	opener.setSSHTargets(rc)
	registry := session.New(log.NewSlogAdapter(logger), &openPasswordPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(logger))})
	opener.registry = registry

	over := &sshOverHelper{local: opener, resolve: rc, log: logger}
	routes := installLeaseRoutes{viaLocal: over, probes: &helperProbes{local: opener, resolve: rc}}

	modes := newTestHostModes(t)
	for _, fixture := range append([]*pwSSHServer{srv}, extra...) {
		mustRecordHostMode(t, modes, gossh.FingerprintSHA256(fixture.hostSigner.PublicKey()), hostmode.Raw)
	}

	return &filesStand{
		hostKeys: rc,
		khPath:   khPath,
		opener:   opener,
		routes:   routes,
		prompts:  prompts,
		registry: registry,
		modes:    modes,
		opts: []ssh.ConnectOption{
			ssh.WithUser(filesFixtureUser),
			ssh.WithAuthMode("password"),
			ssh.WithCredentials(secrets, filesFixtureSecret),
			// The credential may only be spent on the endpoint its profile
			// identifies; the resolver stamps this, so the test does too.
			ssh.WithAuthorizedEndpoint(srv.addr),
			ssh.WithConnectionName("Files acceptance"),
		},
	}
}

// recordFixtureMode answers the method this fixture's HOST keeps, through the
// same store and the same write the connect-time ask performs — the record the
// open path reads (ADR-727g63tr decision 1), rather than the connection's
// stored field.
func recordFixtureMode(t *testing.T, stand *filesStand, srv *pwSSHServer, mode hostmode.Mode) {
	t.Helper()
	fingerprint := gossh.FingerprintSHA256(srv.hostSigner.PublicKey())
	if mode == hostmode.Auto {
		// Auto is the ABSENCE of an answer (ADR-0033), and this is the write
		// the editor's auto performs.
		if err := stand.modes.PersistMany([]string{fingerprint}, hostmode.Auto); err != nil {
			t.Fatalf("forget the method for %s: %v", fingerprint, err)
		}
		return
	}
	mustRecordHostMode(t, stand.modes, fingerprint, mode)
}

// connCount is how many connections authenticated, and subsystemsSeen is the
// subsystem names asked for in order.
func (s *pwSSHServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

func (s *pwSSHServer) subsystemsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.subsystems...)
}

// helperInstalledStub is the installed-generation fact localHelperOpener
// requires before it will answer with the connection it already holds. Nothing
// installs or starts anything here: the daemon is in this process for the
// length of the test, and the connection is already on the opener.
func helperInstalledStub() helperlocal.Installed {
	return helperlocal.Installed{
		Binary:     "/this/test/has/no/binary",
		Generation: proto.GenerationID(filesFixtureGenera),
	}
}

// A host in raw mode may offer SFTP, but opening Files uses its far-helper axis.
func TestFilesOnRawSSHHostRefusesWithoutOpeningSFTP(t *testing.T) {
	root := t.TempDir()
	srv := startPasswordSFTPSSHServer(t, root)
	secrets := &askRecorder{value: openPasswordFixturePassword}
	stand := startFilesStand(t, srv, secrets)
	driver := panedriver.New("files-raw", nil)
	stand.opener.paneConns = driver
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	sess := openPanes(t, ctx, openSeamOver(t, stand), paneConfig(srv, secrets, "pane-raw"), "claim-files-raw")
	connectionID, bound := driver.SessionConnectionID(sess.PaneID(), string(sess.ID()))
	if !bound || srv.connCount() != 1 {
		t.Fatalf("open SSH pane: connection = %q (bound %t), authenticated connections = %d", connectionID, bound, srv.connCount())
	}

	connections := newFarHelperConnections()
	helpers := farhelper.New(farhelper.Config{Connections: connections, Modes: stand.modes})
	t.Cleanup(helpers.Close)
	connections.Observe(farhelper.Connection{
		ID: connectionID, State: hostconn.Connected, Generation: 1,
		HostKeyFingerprint: gossh.FingerprintSHA256(srv.hostSigner.PublicKey()),
	})
	waittest.WaitFor(t, "raw host's unavailable helper axis", func() bool {
		axis, ok := helpers.Axis(connectionID)
		return ok && axis.State == farhelper.StateUnavailable && axis.Reason == farhelper.ReasonModeRaw
	})

	bindings := filesystem.New()
	factory := filesystemProviderFactory(helpers, driver.SessionConnectionID)
	op := capability.NewFilesystemOpenOperation(control.Unrestricted(), stand.registry, capability.ProviderFactory(factory), bindings)
	err := op.Open(ctx, func(ctx context.Context, svc capability.FilesystemOpenService) error {
		bid, _, openErr := svc.OpenBinding(ctx, sess, root)
		if openErr != nil {
			t.Fatalf("OpenBinding: %v", openErr)
		}
		defer func() { _ = bindings.Close(bid) }()
		handle, release, acquireErr := svc.Acquire(bid, localFilesOwner{owner: sess.ID()})
		if acquireErr != nil {
			t.Fatalf("acquire Files binding: %v", acquireErr)
		}
		defer release()
		want := filesystem.HelperAxis{State: "unavailable", Reason: "mode-raw", Action: "host-settings"}
		if axis, known := handle.HelperAxis(); !known || axis != want {
			t.Errorf("binding helper axis = %+v (known %t), want %+v", axis, known, want)
		}
		_, rootErr := handle.Root(ctx)
		return rootErr
	})
	var unavailable *filesystem.ErrUnavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != "mode-raw" || unavailable.Action != "host-settings" {
		t.Errorf("opening Files on a raw host: %v, want ErrUnavailable(mode-raw, host-settings)", err)
	}
	if seen := srv.subsystemsSeen(); slices.Contains(seen, "sftp") {
		t.Errorf("opening Files requested an SFTP subsystem: %v", seen)
	}
}
