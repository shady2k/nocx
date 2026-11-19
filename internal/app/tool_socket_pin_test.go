package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

type pinToolSocketService struct {
	pinWireService
	sockets []proto.ToolSocketParams
}

func (*pinToolSocketService) Ops() []string { return []string{proto.OpToolSocket} }
func (*pinToolSocketService) ParamsSchema(string) *host.Schema {
	return host.SchemaFor(proto.ToolSocketParams{})
}

func (s *pinToolSocketService) Call(_ context.Context, _ string, raw json.RawMessage) (any, error) {
	var p proto.ToolSocketParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.sockets = append(s.sockets, p)
	return nil, &proto.Refusal{Code: proto.ErrCodeHostKeyPinMismatch, Message: "connection replaced"}
}

func TestToolSocketCarriesApprovedHostKeyPin(t *testing.T) {
	service := &pinToolSocketService{}
	stand := startPaneStand(t)
	spawner := &scriptedSpawner{}
	dir, err := os.MkdirTemp("", "pin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			t.Errorf("remove endpoint: %v", removeErr)
		}
	})
	daemon := startLocalEndpointWith(t, dir, stand.daemon.generation, spawner, spawner, service)
	stand.opener.dir = daemon.dir
	resolver, ok := stand.opener.sshTargets.(*ssh.RealClient)
	if !ok {
		t.Fatal("fixture has no SSH target resolver")
	}
	channels := &sshOverHelper{local: stand.opener, resolve: resolver}
	opts := append(session.SSHOptionsFromConfig(stand.sshConfig("helper", nil).Remote), ssh.WithHostKeyPin("SHA256:script"))
	_, err = channels.OpenFarPaneToolSocket(context.Background(), "host.example", opts, farPaneToolRoutes{Path: "/tmp/tool.sock", Target: "/tmp/local.sock"}, "pane")
	if !hostKeyPinMismatch(err) {
		t.Fatalf("socket refusal: %v", err)
	}
	if len(service.sockets) != 1 || service.sockets[0].HostKeyFingerprint != "SHA256:script" {
		t.Fatalf("tool socket pin lost: %+v", service.sockets)
	}
}
