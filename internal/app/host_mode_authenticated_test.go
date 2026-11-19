package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostmode"
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
			defer opened.Session.Close()
			hosts, _ := installer.calls()
			wantMode := proto.SSHMode(hostmode.Raw)
			wantPublishes := 0
			if verified == "SHA256:script" {
				wantMode = proto.SSHMode(hostmode.Script)
				wantPublishes = 1
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
	defer opened.Session.Close()
	if hosts, _ := installer.calls(); len(hosts) != 0 {
		t.Fatalf("failed lease published for %v", hosts)
	}
	if got := stand.daemon.spawner.sshSpawns()[0].Mode; got != proto.SSHMode(hostmode.Raw) {
		t.Fatalf("spawn mode %q, want raw", got)
	}
}
