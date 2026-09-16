//go:build nocx_local_ssh

package app

// The live-sshd fixture's bundle carrier, in a build WITH the helper's ssh
// service — this machine's helper, for real.
//
// `live_sshd_carrier_test.go` (the untagged half) says why the two exist and
// what each is for. This one makes every live-sshd journey — a real OpenSSH, a
// real bash, a real lifecycle domain — publish through the production route:
// the real ssh service on a real socketpair, the real coordinator client, the
// real carrier, and the fixture's own sshd as the far side. So
// `TestLiveSshd_BashReachesAcceptedDomain` reaches its accepted domain with the
// bundle written by this machine's helper into the session's `$HOME`, which is
// the epic's acceptance criterion (nocx-50w7p.15) rather than a rehearsal of
// it.

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

// withClientKeyFile makes the fixture dial with `IdentityFile`-shaped options
// instead of an inline signer: the private key is written to a file the session
// may read, and every connection the journey makes names that PATH.
//
// It is defined in THIS file because its only callers are here — an option
// nothing calls in the other build is a lint failure, and hanging it where it is
// used is honest where a fabricated caller is not. What it selects is a real
// profile difference rather than a test convenience: the two shapes differ in
// exactly the place this epic cares about, because a helper is handed a
// REFERENCE and reads nothing itself. A path is something the coordinator can
// resolve and sign for; a signer value in memory is not, there being nothing to
// name.
func withClientKeyFile() liveSshdOption {
	return func(c *liveSshdConfig) { c.clientKeyFile = true }
}

// liveFixtureKeyRef names the fixture's client key on this wire. It is opaque
// to the helper, which echoes it back when it needs a signature.
const liveFixtureKeyRef = "sec:live-sshd:fixture-key"

// liveBundleCarrier is the publisher a live-sshd journey wires: this machine's
// helper, dialing the fixture sshd.
func liveBundleCarrier(t *testing.T, fx *liveSshd) ssh.RemoteInstaller {
	t.Helper()
	inner, key := startLiveHelperStand(t, fx)
	return &helperLiveCarrier{fx: fx, inner: inner, key: key}
}

// helperLiveCarrier is the production carrier plus the fixture's own identity.
//
// The identity is APPENDED here and not put on the pane's connect options, and
// that is deliberate: a helper needs a credential REFERENCE and a resolver
// (it never holds the key), while this fixture's pane dials with an inline
// signer — and `ssh.ResolveTarget` refuses an inline method by name
// (ErrNoHelperIdentity), because a helper has no file and no agent. So the
// fixture supplies the binding for the same key, and the pane's own dial is
// untouched by it.
type helperLiveCarrier struct {
	fx    *liveSshd
	inner *remoteInstallerAdapter
	key   liveFixtureKeyResolver
}

func (c *helperLiveCarrier) EnsureInstalledRemote(ctx context.Context, host string, opts ...ssh.ConnectOption) error {
	identity := append([]ssh.ConnectOption{}, opts...)
	identity = append(identity,
		ssh.WithCredentials(c.key, liveFixtureKeyRef),
		ssh.WithKeySecretID(liveFixtureKeyRef),
		// The credential is authorized for the destination it was minted for;
		// the fixture's sshd IS that destination.
		ssh.WithAuthorizedEndpoint(c.fx.addr),
	)
	return c.inner.EnsureInstalledRemote(ctx, host, identity...)
}

func (c *helperLiveCarrier) UninstallRemote(context.Context, *gossh.Client) ([]string, []string, error) {
	return nil, nil, nil
}

// liveFixtureKeyResolver answers the fixture's own private key, PEM-encoded,
// for the one reference this stand asks about.
//
// It is what the reverse `sign` op asks for: the helper must DECLARE which key
// it offers and then ask for one signature per challenge, so the private half
// never leaves this process.
type liveFixtureKeyResolver struct{ pem []byte }

func (r liveFixtureKeyResolver) Resolve(_ context.Context, _ credential.SecretID, _ credential.Stance) (credential.Secret, error) {
	return credential.NewSecret(string(r.pem)), nil
}

// startLiveHelperStand assembles this machine's helper in-process, pointed at
// the fixture: the same stand the bundle acceptance builds, with the fixture's
// key as the credential the helper must present.
func startLiveHelperStand(t *testing.T, fx *liveSshd) (*remoteInstallerAdapter, liveFixtureKeyResolver) {
	t.Helper()
	logger := discardLogger(t)
	sshLog := log.NewSlogAdapter(logger)

	block, err := gossh.MarshalPrivateKey(fx.clientRaw, "")
	if err != nil {
		t.Fatalf("marshal the fixture key: %v", err)
	}
	key := liveFixtureKeyResolver{pem: pem.EncodeToMemory(block)}

	// The coordinator's client: it RESOLVES and it answers host-key questions.
	// The fixture's host key is recorded, so the helper's verdict is
	// `trusted` and no accept sheet is raised.
	rc, err := ssh.NewReal(sshLog, ssh.WithKnownHostsFile(fx.knownHostsPath(t)))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

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
		Reverse:     helperReverseHandlers(rc, key, &helperPrompt{log: logger}, nil, logger),
		Log:         logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	opener := &localHelperOpener{log: logger}
	opener.installed = helperlocal.Installed{
		Binary:     filepath.Join(t.TempDir(), "no-binary-is-started-here"),
		Generation: filesFixtureGenera,
	}
	opener.dir = t.TempDir()
	opener.client = peer

	over := &sshOverHelper{local: opener, resolve: rc, log: logger}
	probes := &helperProbes{local: opener, resolve: rc}
	impl := shellintegration.New(log.NewSlogAdapter(logger))
	return &remoteInstallerAdapter{
		inner: impl,
		bundle: &helperBundlePublisher{
			probes: probes, channels: over, publish: impl, log: logger,
		},
	}, key
}

// TestLiveSshd_OneSessionPerConnectionStillIntegratesOverTheHelper pins the
// CONSEQUENCE this bead bought, on a real sshd that allows ONE session channel
// per connection.
//
// The publish used to run its home probe and its sftp channel on the PANE's
// connection, so under that bound it was the publish the far side refused and
// the session could never integrate — the state
// TestEpicE2E_MaxSessions1LeavesAWorkingUnintegratedPrompt's saved-connection
// case was written to pin. The publish is this machine's helper's now, on a
// connection of its own, so the pane's one slot belongs to the user's shell and
// nocx's own work cannot spend it: the domain reaches Established, and the
// server sees three logins — the pane, this harness's lifecycle transport
// lease, and the helper's own dial.
//
// This is the tagged half of that pair on purpose. What the far side answers
// there depends on which transport the build wires, so the common observables
// live in the epic test and the route's own verdict is asserted HERE, where the
// route is this machine's helper and nothing is declared at runtime.
func TestLiveSshd_OneSessionPerConnectionStillIntegratesOverTheHelper(t *testing.T) {
	fx := startLiveSshd(t, true, withSshdConfig("MaxSessions 1"))
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, liveBundleCarrier(t, fx))
	t.Cleanup(func() { _ = ch.Close() })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("session terminal:\n%s", out.String())
		}
	})

	waittest.WaitForTimeoutDetail(t, "the domain under one session per connection", 30*time.Second,
		func() string { return fmt.Sprintf("terminal:\n%s", out.String()) },
		func() bool {
			kernel.mu.Lock()
			domain := kernel.domain
			kernel.mu.Unlock()
			if domain == "" {
				return false
			}
			d, ok := kernel.Domain(domain)
			return ok && d.State == lifecycle.DomainEstablished
		})

	// The publish ran on the helper's connection and nothing of nocx's went
	// looking for a slot on the pane's: the server authenticated three times,
	// once per connection and never once per channel.
	if n := fx.authCount(); n != 3 {
		t.Errorf("the server accepted %d authentications, want 3 — the pane's session, this harness's lifecycle transport, and this machine's helper dialing for the publish", n)
	}
	t.Logf("MEASURED a saved connection under MaxSessions 1 over the helper: domain established, %d authentication(s)", fx.authCount())
}

// TestLiveSshd_AnInlineKeyFileConnectionPublishesAndReachesItsDomain is the
// END-TO-END half of the inline-key success (nocx-50w7p.11 with nocx-50w7p.15),
// on a real sshd and through the product's own `RealClient.Connect`.
//
// The fixture dials with `IdentityFile`-shaped options — the shape a direct-host
// open carries when its profile names a key rather than binding one in the
// store — and the publish is handed those same options. This test used to
// assert the OPPOSITE: a helper could not be handed an inline key (no file, no
// agent, no secret at rest), so `ssh.ResolveTarget` refused the connection by
// name and the far side was left with no generation. Now the coordinator reads
// the file and signs with it, and what is asserted here is the consequence a
// person sees:
//
//  1. the publish HAPPENS — the bundle lands under the session's `$HOME`, and
//     the far side reaches its accepted domain because it found the generation
//     it was handed, which is this epic's acceptance criterion;
//  2. the helper dialed for it: the server accepted three authentications — the
//     pane, this harness's lifecycle transport, and the helper — where the old
//     shape measured two and named a refusal;
//  3. the session integration axis reports no refusal, and a shell runs
//     commands, so the absolute part of ADR-0004 is intact either way.
//
// `liveBundleCarrier` deliberately APPENDS a stored identity for the routing
// journeys; this test passes the production carrier the fixture's own options,
// which is what makes it the inline-key case rather than a rehearsal of one.
func TestLiveSshd_AnInlineKeyFileConnectionPublishesAndReachesItsDomain(t *testing.T) {
	logs, logger := captureProductLogs(t)
	fx := startLiveSshd(t, true, withClientKeyFile())
	fx.logger = logger

	carrier, _ := startLiveHelperStand(t, fx)

	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, carrier)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("session terminal:\n%s", out.String())
			t.Logf("product log:\n%s", logs.String())
		}
	})

	// 1. The accepted domain: the far side loaded the generation the publish
	//    put under its $HOME. This is the observable the epic's acceptance is
	//    written on, and it is the one the old refusal could never reach.
	waittest.WaitForTimeoutDetail(t, "the accepted domain for an inline-key connection", 30*time.Second,
		func() string { return fmt.Sprintf("terminal:\n%s", out.String()) },
		func() bool {
			kernel.mu.Lock()
			domain := kernel.domain
			kernel.mu.Unlock()
			if domain == "" {
				return false
			}
			d, ok := kernel.Domain(domain)
			return ok && d.State == lifecycle.DomainEstablished
		})

	// The bundle is where the far side looks for it. The domain above is the
	// far side's own verdict; this is the file, so a domain that established
	// over a generation written somewhere else could not pass both.
	if _, err := os.Stat(filepath.Join(fx.home, ".nocx", "manifest.json")); err != nil {
		t.Errorf("the bundle is not under the session $HOME (%s): %v", fx.home, err)
	}

	// 2. The helper dialed, and the credential it presented authenticated:
	//    three logins — the pane's session, this harness's lifecycle transport
	//    lease, and the helper's own dial for the publish. The old shape
	//    measured two, because the refusal came before any dial.
	if n := fx.authCount(); n != 3 {
		t.Errorf("the server accepted %d authentications, want 3 — the pane, this harness's lifecycle transport, and this machine's helper dialing for an inline-key publish", n)
	}

	// 3. Nothing on the integration axis reports a missing publish, and the
	//    shell is usable: the publish succeeds, and the terminal is not what
	//    paid for it.
	if reason := ch.ShellIntegrationReason(); reason == ssh.ReasonPublishUnavailable {
		t.Errorf("the session integration reason is %q, but the publish succeeded: a refusal reported over a live generation is as wrong as the reverse", reason)
	}
	if _, err := ch.Write([]byte("printf 'KEYFILE%s\n' _OK\n")); err != nil {
		t.Fatalf("write to the remote shell: %v", err)
	}
	waittest.WaitForTimeout(t, "a usable prompt on an inline-key connection", 30*time.Second, func() bool {
		return strings.Contains(out.String(), "KEYFILE_OK")
	})
	t.Logf("MEASURED an inline-key connection: prompt ok, %d authentication(s), reason %q, bundle under %s",
		fx.authCount(), ch.ShellIntegrationReason(), fx.home)

	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_ALockedKeyFileRefusesThePublishBeforeDialing is the paired
// refusal, on the same real sshd and through the same product carrier: the
// connection names a key FILE this process cannot open (an encrypted key with
// nowhere to read its passphrase), and the publish is refused as
// `ssh.ErrEncryptedKey` BEFORE any dial.
//
// Both halves matter. The TYPE is what keeps a locked key from reaching a
// person as "the server refused your credential" — the key is fine, it needs a
// passphrase, and the host is not the thing to go and look at. The COUNT is
// what keeps the refusal honest: a helper that dialed first and read afterwards
// would have offered a connection to a host for a credential nobody could
// present.
func TestLiveSshd_ALockedKeyFileRefusesThePublishBeforeDialing(t *testing.T) {
	logs, logger := captureProductLogs(t)
	fx := startLiveSshd(t, true)
	fx.logger = logger

	carrier, _ := startLiveHelperStand(t, fx)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("product log:\n%s", logs.String())
		}
	})

	// The fixture's own key, encrypted: the same key the journeys above
	// authenticate with, in the one state this process cannot open.
	block, err := gossh.MarshalPrivateKeyWithPassphrase(fx.clientRaw, "", []byte("a passphrase nobody has"))
	if err != nil {
		t.Fatalf("marshal the locked key: %v", err)
	}
	locked := filepath.Join(t.TempDir(), "id_ed25519")
	if writeErr := os.WriteFile(locked, pem.EncodeToMemory(block), 0o600); writeErr != nil {
		t.Fatalf("write the locked key: %v", writeErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = carrier.EnsureInstalledRemote(ctx, fx.addr,
		ssh.WithUser(fx.user),
		ssh.WithKeyFile(locked),
		ssh.WithAuthorizedEndpoint(fx.addr),
	)

	var lockedErr *ssh.ErrEncryptedKey
	if !errors.As(err, &lockedErr) {
		t.Fatalf("the publish for a locked key file is %v (%T), want *ssh.ErrEncryptedKey", err, err)
	}
	if n := fx.authCount(); n != 0 {
		t.Errorf("the server accepted %d authentication(s), want 0 — this refusal is reached before any dial, and a helper that dialed first would be offering a host a credential nobody can present", n)
	}
	if _, statErr := os.Stat(filepath.Join(fx.home, ".nocx", "manifest.json")); !os.IsNotExist(statErr) {
		t.Errorf("a bundle appeared under the session home although the credential could not be read: stat err = %v", statErr)
	}
	t.Logf("MEASURED the named refusal for a locked key file: %v, %d authentication(s)", err, fx.authCount())
}
