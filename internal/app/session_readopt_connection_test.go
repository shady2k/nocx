package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/waittest"
)

// The helper's connection op owns the identity. A replacing coordinator must
// ask it again, not recover an id from a host, fingerprint or session id.
type readoptConnectionService struct {
	driver *panedriver.Driver
	fail   bool
}

func (*readoptConnectionService) Name() string { return proto.ServiceSSH }
func (*readoptConnectionService) Ops() []string {
	return []string{proto.OpConnection, proto.OpConnections}
}

func (*readoptConnectionService) ParamsSchema(op string) *helperhost.Schema {
	if op == proto.OpConnection {
		return helperhost.SchemaFor(proto.ConnectionParams{})
	}
	return helperhost.SchemaFor(proto.ConnectionsParams{})
}

func (s *readoptConnectionService) Call(_ context.Context, op string, raw json.RawMessage) (any, error) {
	summary := farHelperSummary("surviving-connection", 7, 3, proto.ConnectionConnected, 1, "host.example", "u", "SHA256:test-host")
	if op == proto.OpConnections {
		return proto.ConnectionsResult{Connections: []proto.ConnectionSummary{summary}}, nil
	}
	if s.fail {
		return nil, errors.New("connection binding refused")
	}
	var p proto.ConnectionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.PaneID != "pane-restored" {
		return nil, errors.New("wrong restored pane")
	}
	// Real sshsvc republishes the supervisor's current observation on binding.
	s.driver.ApplyFrom("instance-1", summary.Connection)
	return proto.ConnectionResult{ConnectionID: summary.Connection.ConnectionID}, nil
}

func TestReadoptRestoresThePaneConnectionForEveryCarrier(t *testing.T) {
	for _, route := range []string{"remote", "staged-remote", "local-daemon"} {
		for _, fail := range []bool{false, true} {
			name := route
			if fail {
				name += "-binding-fails"
			}
			t.Run(name, func(t *testing.T) {
				home := storagetest.IsolateWithHome(t)
				ctx := t.Context()
				spawner := &lifecycleSpawner{}
				svc := helperWithIntegratedShells(t, spawner)
				provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
				first := newIntegratedCoordinator(t, provider)
				binding := openHostedFixture(t, first.coordinator, "pane-restored")
				shell, _ := spawner.theShell(t)
				shell.drain()
				shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
				waittest.WaitFor(t, "initial shell integration", func() bool { return len(first.emitter.any()) > 0 })
				first.quit()
				second := newIntegratedCoordinator(t, provider)
				adopter := &stubAdopter{}
				pass := readoptFixture(t, second.coordinator, routesFor(binding), adopter)
				driver := panedriver.New("replacement", farHelperTestLogger(t))
				snapshots := &helperAxisSnapshots{}
				driver.SetPublisher(snapshots.add)
				connectionSvc := &readoptConnectionService{driver: driver, fail: fail}
				connectionClient := dialInProcessHelper(t, discardLogger(t), connectionSvc)
				source := newFarHelperConnections()
				seams := &gitFarSeams{t: t, peer: helperPeerWithoutSession(t)}
				far := farhelper.New(farhelper.Config{
					Connections: source, Modes: &reviewModeStore{mode: hostmode.Helper}, Consents: &reviewConsentStore{},
					Probe: seams, Install: seams, Dial: seams, Log: farHelperTestLogger(t),
				})
				defer far.Close()
				driver.SetHelperSource(paneHelperAxis{reg: far})
				opener := &localHelperOpener{
					log: discardLogger(t), registry: second.sess, dir: endpoint.Dir(home),
					installed: helperlocal.Installed{Binary: "/nonexistent/nocx-helper", Generation: "generation-under-test"},
					client:    connectionClient, sshTargets: &recordingResolver{}, paneConns: driver, farConnections: source,
				}
				defer opener.close()
				pass.local = opener
				if route == "local-daemon" {
					// This daemon carries a remote destination; the saved route names it,
					// while the attachment still rides this machine's endpoint.
					src := fakeArtifacts{payload: syntheticPayload}
					ep := startFakeLocalEndpoint(t, endpoint.Dir(home), src.hash())
					seed, err := helperlocal.Open(ctx, helperlocal.Config{Dir: ep.dir, Generation: proto.GenerationID(src.hash()), Log: discardLogger(t)})
					if err != nil {
						t.Fatal(err)
					}
					entry, err := seed.Spawn(ctx, proto.SpawnParams{Cols: 80, Rows: 24})
					if err != nil {
						t.Fatal(err)
					}
					if err := seed.Close(); err != nil {
						t.Fatal(err)
					}
					binding.SessionID = entry.HostSessionID.Session
					binding.Generation = src.hash()
					binding.HelperCommand = ""
				}
				if route == "staged-remote" {
					if err := pass.stageReadopt(ctx, binding); err != nil {
						t.Fatal(err)
					}
				}
				inv, err := pass.Readopt(ctx, binding)
				if err != nil || inv == nil {
					t.Fatalf("readopt: inventory=%v err=%v", inv, err)
				}
				sess, err := second.sess.Get(session.ID(binding.SessionID))
				if err != nil {
					t.Fatalf("binding must not prevent shell adoption: %v", err)
				}
				selectGit := helperGitFactory(far, func() *panedriver.Driver { return driver })
				if fail {
					if id, ok := driver.ConnectionID(binding.PaneID); ok {
						t.Fatalf("failed binding adopted %q", id)
					}
					selected := selectGit(sess)
					if selected.Factory != nil || selected.Refusal == nil || !strings.Contains(selected.Refusal.Message, "connection") {
						t.Fatalf("Git after failed binding = %+v", selected)
					}
				} else {
					id, ok := driver.ConnectionID(binding.PaneID)
					if !ok || id != "surviving-connection" {
						t.Fatalf("restored pane connection = %q, known=%v", id, ok)
					}
					waittest.WaitFor(t, "restored pane to publish current connection and serving helper", func() bool {
						got := snapshots.last()
						return got.SessionID == binding.SessionID && got.Transport.State == "connected" && got.Helper.State == panedriver.HelperServing
					})
					got := snapshots.last()
					if got.InstanceID != "replacement" {
						t.Fatalf("instance = %q, want the replacement coordinator", got.InstanceID)
					}
					selected := selectGit(sess)
					if selected.Factory == nil {
						t.Fatalf("Git selection = %+v", selected)
					}
					repo, outcome, err := selected.Factory.Open(ctx, fixtureRepo(t))
					if err != nil || outcome.State != git.OpenOK {
						t.Fatalf("Git open = %+v, %v", outcome, err)
					}
					if _, statusErr := repo.Status(ctx); statusErr != nil {
						t.Fatal(statusErr)
					}
					if closeErr := repo.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
					repo, outcome, err = selected.Factory.Open(ctx, t.TempDir())
					if repo != nil || err != nil || outcome.State != git.OpenNotARepository {
						t.Fatalf("not-repo = %+v, %v", outcome, err)
					}
				}
				if route != "local-daemon" {
					// Git's not-repository answer (or a connection-bind refusal)
					// must not close the separately adopted lifecycle carrier.
					attempt := lifecycle.AttemptID("after-git")
					shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &attempt, Command: "echo after-git"}})
					code := 0
					shell.send(t, lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{3}}})
					waittest.WaitFor(t, "command after Git refusal to produce a completed block", func() bool { return len(second.emitter.completed()) > 0 })
					completed := second.emitter.completed()
					last := completed[len(completed)-1]
					if last.Attempt.Command != "echo after-git" || last.Attempt.ExitCode == nil || *last.Attempt.ExitCode != 0 {
						t.Fatalf("block after Git refusal = %+v", last.Attempt)
					}
				}
				if _, err := sess.Write([]byte("still usable\n")); err != nil {
					t.Fatalf("Git/binding refusal damaged shell input: %v", err)
				}
				select {
				case <-sess.Done():
					t.Fatal("Git/binding refusal ended adopted shell")
				default:
				}
			})
		}
	}
}

var _ helperhost.Service = (*readoptConnectionService)(nil)
