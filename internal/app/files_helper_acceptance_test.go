//go:build nocx_local_ssh

package app

// FILES GOES THROUGH THIS MACHINE'S HELPER, proved at the app level
// (nocx-50w7p.12).
//
// The subject is the PRODUCT's own Files provider — the value
// filesystemProviderFactory returns for a remote session — not a lease, and not
// a stream: everything between the factory and a real SFTP server is the
// shipped code path. What is scripted, deliberately and only, is the two things
// that are not this package's:
//
//   - the SSH SERVER (somebody else's machine: pwSSHServer, extended with an
//     sftp subsystem over a real directory for this test);
//   - the COORDINATOR's answers to the helper's questions (helperReverseHandlers
//     over a real ssh.RealClient for host keys and a stub credential resolver
//     for material — the same registry the composition root builds at
//     app.go's localOpener.setReverseHandlers).
//
// Between them sits the REAL helper: the real host protocol over a socketpair,
// the real ssh service registered on it, the real daemon-side ssh client that
// dials, and the real coordinator client with a real reverse registry. That is
// the stand internal/helper/sshsvc's own suite builds, assembled here because
// this is the half only internal/app can assert: that the factory a session
// gets takes the helper's channel at all.
//
// # Why the helper is assembled in-process
//
// A daemon process would add installation, generation selection and a Unix
// endpoint to a test whose claim is about the ROUTE. The route is the
// composition root's: sshOverHelper over a connection to this machine's helper,
// wired into installLeaseRoutes, consumed by filesystemProviderFactory. The
// carrier between the two sides is the one thing that changes, and sshsvc's
// suite already proves the protocol over a socketpair is the protocol.
//
// # What this test does NOT prove, and why
//
// "One authenticated connection serves a pane and its Files panel" needs the
// PANE on the helper too (nocx-50w7p.5, which moves the route); today the pane
// is still the coordinator's own dial, so the fixture's authentication count
// here is the Files lease's alone and is asserted as exactly one for THAT
// reason: the lease must not buy a second login, and it must ride the helper's
// connection rather than reach for one of its own.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/filesystem"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

const (
	filesFixtureSecret = "sec:helper-files:1"
	filesFixtureUser   = "dev"
	filesFixtureGenera = "0123456789abcdef"
)

// filesStand is one assembled stack: the fixture server, the helper peer, the
// coordinator's resolver, and the Files factory the composition root builds.
type filesStand struct {
	srv     *pwSSHServer
	root    string
	factory func(session.Session, string) (filesystem.Provider, error)
	opts    []ssh.ConnectOption
	// khPath is the coordinator's known_hosts. It is exposed because a ROUTE
	// has more than one host in it: a test that adds a bastion has to record
	// that bastion's key too, and the coordinator's own verdict is what decides.
	khPath string
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

// startFilesStand assembles the stack against a fixture serving root over sftp.
func startFilesStand(t *testing.T, srv *pwSSHServer, secrets credential.Resolver) *filesStand {
	t.Helper()
	logger := discardLogger()
	// The ssh client takes the harness's own logging seam (internal/log), the
	// same adapter app.go builds beside it.
	sshLog := log.NewSlogAdapter(logger)

	// The coordinator's client: it RESOLVES and it answers host-key questions.
	// It never dials for the Files lease, and the fixture's authentication
	// count is what says so.
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHostsFor(t, khPath, srv)
	rc, err := ssh.NewReal(sshLog, ssh.WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// The helper: the real daemon-side client dials, the real ssh service is
	// registered on a real host protocol engine, and the carrier between the
	// two sides is a socketpair. This is sshsvc's own stand, assembled here.
	helperEnd, coordEnd := net.Pipe()
	sshdialClient, err := sshdial.New(logger)
	if err != nil {
		t.Fatalf("sshdial.New: %v", err)
	}
	svc := sshsvc.New(sshdialClient, logger)
	engine := host.New(helperEnd, helperEnd, filesFixtureGenera, "instance-1", logger)
	engine.Register(svc)
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

	peer, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(coordEnd),
		ExpectHash:  filesFixtureGenera,
		SentinelTTL: 5 * time.Second,
		Reverse:     helperReverseHandlers(rc, secrets, &helperPrompt{log: logger}, logger),
		Log:         logger,
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
	opener := &localHelperOpener{log: logger}
	opener.installed = helperInstalledStub()
	opener.dir = t.TempDir()
	opener.client = peer

	over := &sshOverHelper{local: opener, resolve: rc, log: logger}
	// Both halves of the dispatch, as the composition root builds it: this
	// test's factory asks for the FILE lease (viaLocal), and the probe half is
	// filled in so the value under test has the shape production has rather
	// than a nil leg that would panic the day something else asks.
	routes := installLeaseRoutes{viaLocal: over, probes: &helperProbes{local: opener, resolve: rc}}

	return &filesStand{
		srv:     srv,
		root:    srv.rootDir,
		khPath:  khPath,
		factory: filesystemProviderFactory(routes),
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

// provider asks the PRODUCT's factory for the provider a remote session gets:
// the same call the transport makes with the session and the root the caller
// verified. The root is the fixture's served directory — the provider refuses a
// relative path and hands absolute ones to the server, so "where this endpoint
// starts" has to be named, exactly as a real session's OSC 7 cwd does.
func (s *filesStand) provider(t *testing.T) (filesystem.Provider, error) {
	t.Helper()
	return s.factory(factorySession{kind: session.KindRemote, host: s.srv.addr, opts: s.opts}, s.root)
}

// path is one seeded entry's absolute remote path.
func (s *filesStand) path(name string) string { return filepath.Join(s.root, name) }

// ── the happy path ────────────────────────────────────────────────────────

// TestFilesOnAnSSHHostRideThisMachinesHelper is the acceptance: a directory is
// listed and a file is read through the provider a remote session is handed,
// the SFTP subsystem that served them was opened by THIS MACHINE'S HELPER, and
// the fixture authenticated exactly once — the lease does not reach for a
// connection of its own.
func TestFilesOnAnSSHHostRideThisMachinesHelper(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from the far side"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	srv := startPasswordSFTPSSHServer(t, root)
	secrets := &askRecorder{value: openPasswordFixturePassword}
	stand := startFilesStand(t, srv, secrets)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := stand.provider(t)
	if err != nil {
		t.Fatalf("the Files provider for an ssh session: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	listing, err := p.List(ctx, stand.root, filesystem.Page{Limit: 50})
	if err != nil {
		t.Fatalf("List through the helper's channel: %v", err)
	}
	names := make(map[string]bool, len(listing.Entries))
	for _, e := range listing.Entries {
		names[e.Name] = true
	}
	for _, want := range []string{"hello.txt", "sub"} {
		if !names[want] {
			t.Errorf("the listing through the helper's channel is missing %q (got %v)", want, names)
		}
	}

	content, err := p.Read(ctx, stand.path("hello.txt"), 0)
	if err != nil {
		t.Fatalf("Read through the helper's channel: %v", err)
	}
	if content.Text != "hello from the far side" {
		t.Errorf("the file read through the helper's channel is %q, want %q", content.Text, "hello from the far side")
	}

	// The helper asked the coordinator for the material it had to present.
	// Without this the test could pass against a lease that never consulted the
	// reverse registry at all.
	if refs := secrets.asked(); len(refs) == 0 || refs[0] != filesFixtureSecret {
		t.Errorf("the credential references the helper asked for are %v, want %q first — the password crosses only when the helper presents it",
			refs, filesFixtureSecret)
	}

	// The server's own view: one subsystem, opened once, over one connection.
	if seen := srv.subsystemsSeen(); len(seen) != 1 || seen[0] != "sftp" {
		t.Errorf("the fixture was asked for subsystems %v, want exactly [sftp]", seen)
	}
	if got := srv.connCount(); got != 1 {
		t.Errorf("the fixture authenticated %d connection(s), want exactly 1 — the Files lease rides this machine's helper's connection and must not raise one of its own", got)
	}
	t.Logf("MEASURED %d authentication(s), subsystems %v, %d credential ask(s)", srv.connCount(), srv.subsystemsSeen(), len(secrets.asked()))
}

// ── the failure paths, each paired with the success above ─────────────────

// TestFilesNamesARefusedSFTPSubsystem: a host that runs SSH and no sftp-server
// answers the subsystem request false, and the refusal reaches the caller as
// the typed error the file panel switches on — not as a generic transport
// failure, and not as an empty listing.
func TestFilesNamesARefusedSFTPSubsystem(t *testing.T) {
	root := t.TempDir()
	srv := startPasswordSFTPSSHServer(t, root)
	srv.refuseSubsystem = true
	stand := startFilesStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	if _, err := stand.provider(t); !errors.Is(err, ssh.ErrFSSubsystemRefused) {
		t.Fatalf("the Files provider for a host with no sftp-server is %v, want a refusal wrapping ErrFSSubsystemRefused", err)
	}
	// Nothing was served: the fixture saw the request and refused it.
	if got := srv.subsystemsSeen(); len(got) != 0 {
		t.Errorf("subsystems recorded = %v, want none — the request was refused, not served", got)
	}
}

// TestFilesLosesTheChannelMidReadAndSaysSo: the transport dies between two
// reads, and the second read REPORTS the loss rather than answering from a
// stale handle or hanging. That distinction is the whole reason the lease
// carries Done/LostErr and the typed ladder.
func TestFilesLosesTheChannelMidRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("first read"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	srv := startPasswordSFTPSSHServer(t, root)
	stand := startFilesStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := stand.provider(t)
	if err != nil {
		t.Fatalf("the Files provider for an ssh session: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// The success half, so the failure below is a CHANGE and not a provider
	// that never worked.
	first, err := p.Read(ctx, stand.path("hello.txt"), 0)
	if err != nil {
		t.Fatalf("the first read: %v", err)
	}
	if first.Text != "first read" {
		t.Fatalf("the first read is %q, want %q", first.Text, "first read")
	}

	srv.killConns()

	_, err = p.Read(ctx, stand.path("hello.txt"), 0)
	if err == nil {
		t.Fatal("a read after the channel died SUCCEEDED — a stale handle answered where the transport was gone")
	}
	if !errors.Is(err, ssh.ErrFSLost) {
		t.Fatalf("the read after the loss is %v, want a failure wrapping ErrFSLost — the caller has to be able to tell a dead channel from a missing file", err)
	}
	t.Logf("MEASURED the named loss: %v", err)
}

// TestFilesNamesASealedVaultOnTheHelpersQuestion: the material the helper must
// present lives in a sealed vault, so the COORDINATOR's own reverse answer is
// the vault's refusal — and it reaches the caller by name, rather than being
// flattened into "the channel could not be opened".
func TestFilesNamesASealedVaultOnTheHelpersQuestion(t *testing.T) {
	root := t.TempDir()
	srv := startPasswordSFTPSSHServer(t, root)
	sealed := &askRecorder{refuse: vault.ErrVaultSealed}
	stand := startFilesStand(t, srv, sealed)

	_, err := stand.provider(t)
	if err == nil {
		t.Fatal("the Files provider was built for a host whose credential is in a sealed vault")
	}
	if !strings.Contains(err.Error(), vault.ErrVaultSealed.Error()) {
		t.Fatalf("the refusal is %q, want the vault's own sentence (%q) in it — a sealed vault the person cannot see is a refusal they cannot act on",
			err.Error(), vault.ErrVaultSealed.Error())
	}
	// The evidence is specific: the refusal came from the coordinator's
	// answer to the helper's question, not from a dial that never happened.
	if refs := sealed.asked(); len(refs) == 0 {
		t.Fatalf("the assistant was never asked for material, so %v did not come from the vault", err)
	}
	if got := srv.connCount(); got != 0 {
		t.Errorf("the fixture authenticated %d connection(s), want 0 — the dial must not have got as far as a login", got)
	}
	t.Logf("MEASURED the sealed refusal: %v", err)
}

// ── the stand's own pieces ────────────────────────────────────────────────

// killConns closes every established server-side connection, which is the
// fixture's way of losing the transport under a lease that is mid-conversation.
func (s *pwSSHServer) killConns() {
	s.liveMu.Lock()
	conns := make([]*gossh.ServerConn, 0, len(s.live))
	for c := range s.live {
		conns = append(conns, c)
	}
	s.liveMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
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
