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
	"fmt"
	"net"
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
	logger := discardLogger()
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
		Reverse:     helperReverseHandlers(rc, key, logger),
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

// TestLiveSshd_AnInlineKeyConnectionStillOpensAndNamesItsPublishRefusal is the
// END-TO-END half of the direct-host consequence (nocx-50w7p.15), on a real
// sshd and through the product's own `RealClient.Connect`.
//
// The fixture's connect options carry an INLINE signer — which is what a
// direct-host open (`spec.Host`, an alias through ~/.ssh/config) carries when
// its profile holds no credential binding — and the publish is handed those
// same options. A helper cannot be handed an inline key (no file, no agent,
// no secret at rest), so `ssh.ResolveTarget` refuses it by name, and the three
// things a person can observe are asserted here:
//
//  1. the session still opens and runs commands — the publish is fail-open
//     (ADR-0004), which is why this is a degrade and not a lost terminal;
//  2. the refusal is BEFORE any dial: the server sees the pane and this
//     harness's lifecycle transport and NOT the helper, where a helper dial
//     would read one login more (the tagged one-slot test measures that 3);
//  3. the far side reports a terminal outcome on the session integration
//     axis, so the renderer has something to show, and the product log names
//     the sentinel rather than only "the publish failed".
//
// `liveBundleCarrier` deliberately appends this fixture's identity so the
// ROUTING journeys can publish at all; this test is the one that does not,
// which is what makes it the direct-host case rather than a rehearsal of it.
func TestLiveSshd_AnInlineKeyConnectionStillOpensAndNamesItsPublishRefusal(t *testing.T) {
	logs, logger := captureProductLogs(t)
	fx := startLiveSshd(t, true)
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

	// 1. A working shell, which is the absolute part of ADR-0004.
	waittest.WaitForTimeout(t, "a usable prompt with the publish refused", 30*time.Second, func() bool {
		kernel.mu.Lock()
		domain := kernel.domain
		kernel.mu.Unlock()
		if domain != "" {
			if d, ok := kernel.Domain(domain); ok && d.State == lifecycle.DomainEstablished {
				t.Errorf("the domain established although nothing could be published")
			}
		}
		if _, err := ch.Write([]byte("printf 'KEYFILE%s\\n' _OK\n")); err != nil {
			return false
		}
		return strings.Contains(out.String(), "KEYFILE_OK")
	})

	// 2. No helper dial: the pane's login and this harness's lifecycle
	// transport lease are the whole count.
	if n := fx.authCount(); n != 2 {
		t.Errorf("the server accepted %d authentications, want 2 — the pane and this harness's lifecycle transport. A third would be a helper that dialed for a credential it must not be handed", n)
	}

	// 3. The refusal is visible where a person can see it: the far side's own
	//    verdict for a publish that did not happen, on the session integration
	//    axis the renderer reads, and the sentinel's sentence in the log.
	if reason := ch.ShellIntegrationReason(); reason != ssh.ReasonPublishUnavailable {
		t.Errorf("the session integration reason is %q, want %q — a degrade nobody can see is the log-only degrade AGENTS.md forbids", reason, ssh.ReasonPublishUnavailable)
	}
	if got := logs.String(); !strings.Contains(got, ssh.ErrNoHelperIdentity.Error()) {
		t.Errorf("the product log does not name the refusal (%q); it must say which credential a helper cannot be handed rather than only that the publish failed", ssh.ErrNoHelperIdentity.Error())
	}
	t.Logf("MEASURED an inline-key connection: prompt ok, %d authentication(s), reason %q, refusal named in the log",
		fx.authCount(), ch.ShellIntegrationReason())

	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}
