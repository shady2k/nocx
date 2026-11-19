package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
)

type modeLeaseService struct{ fingerprint string }

func (*modeLeaseService) Name() string  { return proto.ServiceSSH }
func (*modeLeaseService) Ops() []string { return []string{proto.OpLease, proto.OpUnlease} }
func (*modeLeaseService) ParamsSchema(op string) *host.Schema {
	if op == proto.OpLease {
		return host.SchemaFor(proto.LeaseParams{})
	}
	return host.SchemaFor(proto.UnleaseParams{})
}

func (s *modeLeaseService) Call(_ context.Context, op string, _ json.RawMessage) (any, error) {
	if op == proto.OpLease {
		return proto.LeaseResult{Lease: proto.LeaseID{1}, HostKeyFingerprint: s.fingerprint}, nil
	}
	return proto.UnleaseResult{}, nil
}

func TestHostModeUsesAuthenticatedLeaseBeforePublishing(t *testing.T) {
	for _, verified := range []string{"SHA256:raw", "SHA256:script", ""} {
		t.Run(verified, func(t *testing.T) {
			stand := startPaneStand(t)
			// Replace the scripted endpoint with one answering authenticated lease identity.
			spawner := &scriptedSpawner{}
			dir, err := os.MkdirTemp("", "sec-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner, &modeLeaseService{fingerprint: verified})
			stand.opener.dir = daemon.dir
			stand.opener.modes = newTestHostModes(t)
			mustRecordHostMode(t, stand.opener.modes, "SHA256:script", hostmode.Script)
			mustRecordHostMode(t, stand.opener.modes, "SHA256:raw", hostmode.Raw)
			installer := &paneInstaller{daemon: daemon}
			initialMode := "script"
			if verified == "SHA256:script" {
				initialMode = "raw"
			}
			cfg := stand.sshConfig(initialMode, installer)
			opened, err := stand.openSSHOnce(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := opened.Session.Close(); closeErr != nil {
					t.Errorf("close session: %v", closeErr)
				}
			}()
			hosts, _ := installer.calls()
			wantMode := proto.SSHMode(hostmode.Raw)
			wantPublishes := 0
			if verified == "SHA256:script" {
				wantMode = proto.SSHMode(hostmode.Script)
				wantPublishes = 1
			}
			if wantPublishes != 0 && installer.pins[0] != verified {
				t.Errorf("publish pin = %q, want %q", installer.pins[0], verified)
			}
			if len(hosts) != wantPublishes {
				t.Errorf("published %v for verified key %q, want %d calls", hosts, verified, wantPublishes)
			}
			requests := spawner.sshSpawns()
			if len(requests) != 1 {
				t.Fatalf("spawns = %d", len(requests))
			}
			if got := requests[0].HostKeyFingerprint; got != verified {
				t.Errorf("spawn key = %q, want authenticated key %q", got, verified)
			}
			if requests[0].Mode != wantMode {
				t.Errorf("spawn mode = %q, want %q", requests[0].Mode, wantMode)
			}
			if verified == "" {
				if opened.IntegrationStatus != "conventional" || string(opened.IntegrationReason) != "host-key-unconfirmed" {
					t.Errorf("missing key must explain fallback: status=%q reason=%q", opened.IntegrationStatus, opened.IntegrationReason)
				}
			} else if opened.IntegrationReason != "" {
				t.Errorf("authenticated mode must not report a hold failure: %q", opened.IntegrationReason)
			}
		})
	}
}

func TestHostModeProbeFailureWithoutKeyDoesNotChooseKnownHostsEntry(t *testing.T) {
	r := newTestRegistry(t, &fakeLaneProvider{probeFail: errors.New("probe unavailable")}, nil)
	mustRecordHostMode(t, r.modes, "SHA256:script", hostmode.Script)
	mustRecordHostMode(t, r.modes, "SHA256:raw", hostmode.Raw)
	cfg := remoteConfigFor()
	_, hold, selected, err := r.openHoldingLease(context.Background(), cfg, "ambiguous")
	hold.release()
	if err != nil || selected {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	if hostmode.Mode(cfg.Remote.IntegrationMode).DeliversScripts() {
		t.Fatalf("unverified destination selected script delivery: %q", cfg.Remote.IntegrationMode)
	}
}

func TestHostModeFailedLeaseDeliversNothing(t *testing.T) {
	stand := startPaneStand(t) // This endpoint has no SSH lease service.
	stand.opener.modes = newTestHostModes(t)
	mustRecordHostMode(t, stand.opener.modes, "SHA256:script", hostmode.Script)
	installer := &paneInstaller{daemon: stand.daemon}
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", installer))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := opened.Session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()
	if hosts, _ := installer.calls(); len(hosts) != 0 {
		t.Fatalf("failed lease published for %v", hosts)
	}
	if got := stand.daemon.spawner.sshSpawns()[0].Mode; got != proto.SSHMode(hostmode.Raw) {
		t.Fatalf("spawn mode %q, want raw", got)
	}
	if opened.IntegrationStatus != "conventional" || string(opened.IntegrationReason) != "host-key-unconfirmed" {
		t.Fatalf("failed lease must explain the safe fallback: status=%q reason=%q", opened.IntegrationStatus, opened.IntegrationReason)
	}
	if opened.IntegrationShell == "" {
		t.Fatal("degraded integration must name its shell so transport registers the reason")
	}
}

type pinRefusingSpawner struct{ *scriptedSpawner }

func (s pinRefusingSpawner) SpawnSSH(ctx context.Context, req helpersession.SSHSpawnRequest) (helpersession.Process, error) {
	if req.HostKeyFingerprint != "" {
		s.scriptedSpawner.mu.Lock()
		s.scriptedSpawner.ssh = append(s.scriptedSpawner.ssh, req)
		s.scriptedSpawner.mu.Unlock()
		return nil, &proto.Refusal{Code: proto.ErrCodeHostKeyPinMismatch, Message: "replacement key"}
	}
	return s.scriptedSpawner.SpawnSSH(ctx, req)
}

func TestHostKeyMismatchDuringSpawnRetriesOnlyRaw(t *testing.T) {
	stand := startPaneStand(t)
	spawner := pinRefusingSpawner{&scriptedSpawner{}}
	dir, err := os.MkdirTemp("", "sec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner.scriptedSpawner, &modeLeaseService{fingerprint: "SHA256:script"})
	stand.opener.dir = daemon.dir
	stand.opener.modes = newTestHostModes(t)
	mustRecordHostMode(t, stand.opener.modes, "SHA256:script", hostmode.Script)
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", &paneInstaller{daemon: daemon}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := opened.Session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()
	requests := spawner.sshSpawns()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].HostKeyFingerprint != "SHA256:script" || requests[0].Mode != proto.SSHMode(hostmode.Script) {
		t.Errorf("initial request: %+v", requests[0])
	}
	if requests[1].HostKeyFingerprint != "" || requests[1].Mode != proto.SSHMode(hostmode.Raw) || requests[1].Lifecycle != nil {
		t.Errorf("unsafe retry: %+v", requests[1])
	}
	if opened.IntegrationStatus != "conventional" || opened.IntegrationReason != ssh.ReasonHostKeyUnconfirmed {
		t.Errorf("invisible fallback: %q %q", opened.IntegrationStatus, opened.IntegrationReason)
	}
}

func TestReopenedHostRecordUsesAuthenticatedKeyNotPriorMode(t *testing.T) {
	stand := startPaneStand(t)
	doc := storage.NewDocumentStore(t.TempDir())
	logger := log.NewSlogAdapter(discardLogger(t))
	written := hostmode.NewStore(logger, doc, "host-modes.json")
	mustRecordHostMode(t, written, "SHA256:prior", hostmode.Script)
	mustRecordHostMode(t, written, "SHA256:current", hostmode.Raw)
	stand.opener.modes = hostmode.NewStore(logger, doc, "host-modes.json")
	spawner := &scriptedSpawner{}
	dir, err := os.MkdirTemp("", "sec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner, &modeLeaseService{fingerprint: "SHA256:current"})
	stand.opener.dir = daemon.dir
	installer := &paneInstaller{daemon: daemon}
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", installer))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := opened.Session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()
	if hosts, _ := installer.calls(); len(hosts) != 0 {
		t.Fatalf("published using prior host record: %v", hosts)
	}
	if req := spawner.sshSpawns()[0]; req.Mode != proto.SSHMode(hostmode.Raw) || req.HostKeyFingerprint != "SHA256:current" {
		t.Fatalf("reopened using prior record: %+v", req)
	}
}

func TestHostKeyMismatchDuringPublishOpensRawPane(t *testing.T) {
	cause := refusalWith(proto.ErrCodeHostKeyPinMismatch, nil)
	stand := startPaneStand(t)
	spawner := &scriptedSpawner{}
	dir, err := os.MkdirTemp("", "sec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner, &modeLeaseService{fingerprint: "SHA256:script"})
	stand.opener.dir = daemon.dir
	stand.opener.modes = newTestHostModes(t)
	mustRecordHostMode(t, stand.opener.modes, "SHA256:script", hostmode.Script)
	installer := &paneInstaller{daemon: daemon, err: cause}
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", installer))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := opened.Session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()
	request := spawner.sshSpawns()[0]
	if request.Mode != proto.SSHMode(hostmode.Raw) || request.HostKeyFingerprint != "" || request.Lifecycle != nil {
		t.Errorf("unsafe fallback: %+v", request)
	}
	if opened.IntegrationStatus != "conventional" || opened.IntegrationReason != ssh.ReasonHostKeyUnconfirmed {
		t.Errorf("invisible fallback: %q %q", opened.IntegrationStatus, opened.IntegrationReason)
	}
}
