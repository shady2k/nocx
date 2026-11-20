//go:build nocx_local_ssh

package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// The old daemon is synthetic so its session survives coordinator replacement.
// After re-adoption, only lane admission changes: it runs through the local
// helper's real SSH service, against a trusted key at the very same address.
type readoptPinLaneProvider struct {
	*fakeLaneProvider
	mu     sync.Mutex
	bridge *sshOverHelper
}

func (p *readoptPinLaneProvider) LaneConn(ctx context.Context, host string, machine proto.Machine, generation proto.GenerationID, opts ...ssh.ConnectOption) (helperclient.HelperConn, error) {
	p.mu.Lock()
	bridge := p.bridge
	p.mu.Unlock()
	if bridge != nil {
		return bridge.LaneConn(ctx, host, machine, generation, opts...)
	}
	return p.fakeLaneProvider.LaneConn(ctx, host, machine, generation, opts...)
}

func TestReadoptedGitRefusesAnotherTrustedHostKeyAfterClientLoss(t *testing.T) {
	for _, staged := range []bool{false, true} {
		name := "direct"
		if staged {
			name = "staged"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			srv := startPasswordSSHServer(t)
			stand := startProbeStack(t, srv)
			trustFixtureHostKey(t, stand.khPath, srv)
			cfg, destination := probeCfg(srv)
			opts := session.SSHOptionsFromConfig(cfg)
			probes := &helperProbes{local: stand.helper.local, resolve: stand.client}
			holder, err := probes.acquire(ctx, destination, opts...)
			if err != nil {
				t.Fatalf("warm the helper's SSH connection pool: %v", err)
			}
			defer func() {
				if closeErr := holder.Close(); closeErr != nil {
					t.Errorf("close original SSH holder: %v", closeErr)
				}
			}()
			if got := srv.awaitConns(1); got != 1 {
				t.Fatalf("warm pool authenticated %d connections, want 1", got)
			}

			svc := sharedHelperService(t)
			fake := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
			first := newCoordinator(t, fake)
			opened, selected, err := first.reg.OpenHosted(ctx, session.Config{
				Kind: session.KindRemote, Host: destination, Cwd: t.TempDir(),
				PaneID: "pin-pane", ProfileID: "pin-profile", Remote: cfg,
			}, "")
			if err != nil || !selected {
				t.Fatalf("open original helper session: selected %v, error %v", selected, err)
			}
			if opened.StartLifecycle != nil {
				opened.StartLifecycle()
			}
			binding := content.PendingSession{
				SessionID: string(opened.Session.ID()), Host: opened.Host,
				Account: opened.Account, Generation: opened.Generation,
				PaneID: "pin-pane", ProfileID: "pin-profile",
				HelperCommand: opened.HelperCommand, Fingerprint: opened.Fingerprint,
			}
			if binding.Fingerprint == "" || binding.Fingerprint == pwServerFingerprint(srv) {
				t.Fatal("fixture needs a durable session key distinct from the trusted SSH server key")
			}
			first.quit()

			second := newCoordinator(t, fake)
			provider := &readoptPinLaneProvider{fakeLaneProvider: fake}
			second.reg.lanes = provider
			adopter := &stubAdopter{}
			pass := readoptFixture(t, second, &stubRoutes{host: destination, cfg: cfg}, adopter)
			if staged {
				if stageErr := pass.stageReadopt(ctx, binding); stageErr != nil {
					t.Fatalf("stage readopt: %v", stageErr)
				}
			}
			if _, readoptErr := pass.Readopt(ctx, binding); readoptErr != nil {
				t.Fatalf("readopt: %v", readoptErr)
			}
			sess, err := second.sess.Get(session.ID(binding.SessionID))
			if err != nil {
				t.Fatalf("readopt did not register original session: %v (adopter: %v)", err, adopter.failure())
			}
			h := second.reg.hosts[sess.ID()]
			if h == nil {
				t.Fatal("readopt did not register its helper in git's registry")
			}
			h.mu.Lock()
			client := h.client
			h.mu.Unlock()
			if client == nil {
				t.Fatal("successful readopt has no helper client to lose")
			}
			h.invalidate(client)
			if closeErr := client.Close(); closeErr != nil {
				t.Fatalf("lose readopt client: %v", closeErr)
			}
			provider.mu.Lock()
			provider.bridge = stand.helper
			provider.mu.Unlock()

			_, err = h.inventoryClient(ctx)
			var refusal *helperclient.RefusalError
			if !errors.As(err, &refusal) || refusal.Code != proto.ErrCodeHostKeyPinMismatch {
				t.Fatalf("carrier after readopt client loss = %v, want host-key-pin-mismatch", err)
			}
			if second.reg.hosts[sess.ID()] != h {
				t.Fatal("git replaced the readopt registry entry")
			}
			if commands := srv.execCommands(); len(commands) != 0 {
				t.Fatalf("refused git bridge executed commands: %v", commands)
			}
			valid, err := probes.acquire(ctx, destination, append(opts, ssh.WithHostKeyPin(pwServerFingerprint(srv)))...)
			if err != nil {
				t.Fatalf("matching holder cannot acquire original pooled connection: %v", err)
			}
			if err := valid.Close(); err != nil {
				t.Fatal(err)
			}
			if got := srv.connCount(); got != 1 {
				t.Fatalf("refusal redialed or evicted the pooled connection: %d connections, want 1", got)
			}
		})
	}
}
