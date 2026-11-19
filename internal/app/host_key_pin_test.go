package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

type pinWireService struct {
	leases  []proto.LeaseParams
	opens   []proto.OpenChannelParams
	replace bool
	homes   []proto.HomeParams
	lanes   []proto.LaneParams
}

func (*pinWireService) Refusal(err error) (string, json.RawMessage) {
	if refusal, ok := err.(*proto.Refusal); ok {
		return refusal.Code, refusal.Details
	}
	return "", nil
}
func (*pinWireService) Name() string { return proto.ServiceSSH }
func (*pinWireService) Ops() []string {
	return []string{proto.OpLease, proto.OpUnlease, proto.OpOpen, proto.OpHome, proto.OpLane}
}

func (*pinWireService) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpLease:
		return host.SchemaFor(proto.LeaseParams{})
	case proto.OpOpen:
		return host.SchemaFor(proto.OpenChannelParams{})
	case proto.OpHome:
		return host.SchemaFor(proto.HomeParams{})
	case proto.OpLane:
		return host.SchemaFor(proto.LaneParams{})
	default:
		return host.SchemaFor(proto.UnleaseParams{})
	}
}

func (s *pinWireService) Call(_ context.Context, op string, raw json.RawMessage) (any, error) {
	switch op {
	case proto.OpLease:
		var p proto.LeaseParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		s.leases = append(s.leases, p)
		if s.replace && len(s.leases) > 1 {
			return nil, &proto.Refusal{Code: proto.ErrCodeHostKeyPinMismatch, Message: "connection replaced"}
		}
		return proto.LeaseResult{Lease: proto.LeaseID{byte(len(s.leases))}, HostKeyFingerprint: "SHA256:script"}, nil
	case proto.OpLane:
		var p proto.LaneParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		s.lanes = append(s.lanes, p)
		return nil, &proto.Refusal{Code: proto.ErrCodeHostKeyPinMismatch, Message: "connection replaced"}
	case proto.OpHome:
		var p proto.HomeParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		s.homes = append(s.homes, p)
		return proto.HomeResult{Home: "/home/dev"}, nil
	case proto.OpOpen:
		var p proto.OpenChannelParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		s.opens = append(s.opens, p)
		return nil, &proto.Refusal{Code: proto.ErrCodeHostKeyPinMismatch, Message: "connection replaced"}
	default:
		return proto.UnleaseResult{}, nil
	}
}

func pinWireStand(t *testing.T, service *pinWireService) (*paneStand, *sshOverHelper) {
	t.Helper()
	stand := startPaneStand(t)
	dir, err := os.MkdirTemp("", "sec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	spawner := &scriptedSpawner{}
	daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner, service)
	stand.opener.dir = daemon.dir
	stand.daemon = daemon
	return stand, &sshOverHelper{local: stand.opener, resolve: stand.opener.sshTargets.(*ssh.RealClient)}
}

func TestSFTPChannelCarriesApprovedHostKeyPin(t *testing.T) {
	service := &pinWireService{}
	stand, channels := pinWireStand(t, service)
	cfg := stand.sshConfig("script", nil)
	opts := append(session.SSHOptionsFromConfig(cfg.Remote), ssh.WithHostKeyPin("SHA256:script"))
	_, err := channels.openChannel(context.Background(), proto.ChannelSFTP, cfg.Host, opts)
	if err == nil {
		t.Fatal("replacement key was accepted")
	}
	if len(service.opens) != 1 || service.opens[0].HostKeyFingerprint != "SHA256:script" {
		t.Fatalf("open pin lost: %+v", service.opens)
	}
}

func TestBrokenConnectionBetweenHoldAndPublishWritesNothing(t *testing.T) {
	service := &pinWireService{replace: true}
	stand, channels := pinWireStand(t, service)
	stand.opener.modes = newTestHostModes(t)
	mustRecordHostMode(t, stand.opener.modes, "SHA256:script", hostmode.Script)
	publisher := &helperBundlePublisher{probes: &helperProbes{local: stand.opener, resolve: stand.opener.sshTargets}, channels: channels}
	installer := &remoteInstallerAdapter{bundle: publisher}
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", installer))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Session.Close()
	if len(service.leases) != 2 || service.leases[1].HostKeyFingerprint != "SHA256:script" {
		t.Fatalf("publish lease pin lost: %+v", service.leases)
	}
	if len(service.opens) != 0 {
		t.Fatal("SFTP opened after host identity changed")
	}
	if opened.IntegrationReason != ssh.ReasonHostKeyUnconfirmed {
		t.Fatalf("invisible degradation: %q", opened.IntegrationReason)
	}
}

func TestHelperInstallMismatchOpensRawWithoutReusingHostMode(t *testing.T) {
	stand := startPaneStand(t)
	provider := &fakeLaneProvider{peer: realHelperPeer(t), installFail: refusalWith(proto.ErrCodeHostKeyPinMismatch, nil)}
	remote := newTestRegistry(t, provider, seedGrantedDocument(t, t.TempDir(), "SHA256:fake"))
	mustRecordHostMode(t, remote.modes, "SHA256:fake", hostmode.Helper)
	stand.opener.modes = remote.modes
	installer := &paneInstaller{daemon: stand.daemon}
	cfg := stand.sshConfig("helper", installer)
	opener := &hostedOpeners{local: stand.opener, remote: remote}
	opened, selected, err := opener.OpenHosted(context.Background(), cfg, "install-replacement")
	if err != nil || !selected {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	defer opened.Session.Close()
	if hosts, _ := installer.calls(); len(hosts) != 0 {
		t.Fatalf("published after refused install: %v", hosts)
	}
	if req := stand.daemon.spawner.sshSpawns()[0]; req.Mode != proto.SSHMode(hostmode.Raw) || req.HostKeyFingerprint != "" || req.Lifecycle != nil {
		t.Fatalf("unsafe fallback: %+v", req)
	}
	if opened.IntegrationReason != ssh.ReasonHostKeyUnconfirmed || opened.IntegrationStatus != "conventional" {
		t.Fatalf("invisible fallback: %q %q", opened.IntegrationStatus, opened.IntegrationReason)
	}
	if provider.install != nil || provider.laneCount() != 0 {
		t.Fatal("replacement host received helper")
	}
}

func TestOneOpenPinsHomeAndSFTPBeforeReplacementFallback(t *testing.T) {
	service := &pinWireService{}
	stand, channels := pinWireStand(t, service)
	stand.opener.modes = newTestHostModes(t)
	mustRecordHostMode(t, stand.opener.modes, "SHA256:script", hostmode.Script)
	installer := &remoteInstallerAdapter{bundle: &helperBundlePublisher{probes: &helperProbes{local: stand.opener, resolve: stand.opener.sshTargets}, channels: channels}}
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", installer))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Session.Close()
	if len(service.leases) != 2 || service.leases[0].HostKeyFingerprint != "" || service.leases[1].HostKeyFingerprint != "SHA256:script" {
		t.Fatalf("lease pins: %+v", service.leases)
	}
	if len(service.homes) != 1 || service.homes[0].Lease != (proto.LeaseID{2}) {
		t.Fatalf("home lease: %+v", service.homes)
	}
	if len(service.opens) != 1 || service.opens[0].HostKeyFingerprint != "SHA256:script" {
		t.Fatalf("SFTP pin: %+v", service.opens)
	}
	if req := stand.daemon.spawner.sshSpawns()[0]; req.Mode != proto.SSHMode(hostmode.Raw) || req.HostKeyFingerprint != "" || req.Lifecycle != nil {
		t.Fatalf("replacement fallback: %+v", req)
	}
	if opened.IntegrationReason != ssh.ReasonHostKeyUnconfirmed {
		t.Fatalf("invisible fallback: %q", opened.IntegrationReason)
	}
}

func TestHelperLaneCarriesApprovedHostKeyPin(t *testing.T) {
	service := &pinWireService{}
	stand, channels := pinWireStand(t, service)
	opts := append(session.SSHOptionsFromConfig(stand.sshConfig("helper", nil).Remote), ssh.WithHostKeyPin("SHA256:script"))
	_, err := channels.LaneConn(context.Background(), "host.example", proto.Machine{}, proto.GenerationID("test"), opts...)
	if !hostKeyPinMismatch(err) {
		t.Fatalf("lane refusal: %v", err)
	}
	if len(service.lanes) != 1 || service.lanes[0].HostKeyFingerprint != "SHA256:script" {
		t.Fatalf("lane pin lost: %+v", service.lanes)
	}
}
