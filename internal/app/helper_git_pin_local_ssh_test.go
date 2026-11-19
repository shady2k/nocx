//go:build nocx_local_ssh

package app

import (
	"context"
	"errors"
	"testing"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// Only deployment is synthetic: bridge lanes use the local helper's real SSH
// service and connection pool, including admission against the observed key.
type pinnedGitLaneProvider struct {
	*fakeLaneProvider
	bridge *sshOverHelper
}

func (p pinnedGitLaneProvider) LaneConn(ctx context.Context, host string, machine proto.Machine, generation proto.GenerationID, opts ...ssh.ConnectOption) (helperclient.HelperConn, error) {
	return p.bridge.LaneConn(ctx, host, machine, generation, opts...)
}

type pinnedGitSession struct {
	*fakeRemoteSession
	opts []ssh.ConnectOption
}

func (s pinnedGitSession) SSHOptions() []ssh.ConnectOption { return s.opts }

func TestHostKeyPinHelperGitBridgeRefusesAnotherPooledHostKey(t *testing.T) {
	srv := startPasswordSSHServer(t)
	stand := startProbeStack(t, srv)
	trustFixtureHostKey(t, stand.khPath, srv)
	cfg, destination := probeCfg(srv)
	opts := session.SSHOptionsFromConfig(cfg)
	probes := &helperProbes{local: stand.helper.local, resolve: stand.client}
	ctx := context.Background()
	holder, err := probes.acquire(ctx, destination, opts...)
	if err != nil {
		t.Fatalf("warm the helper's connection pool: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if got := srv.awaitConns(1); got != 1 {
		t.Fatalf("warm pool authenticated %d connections, want 1", got)
	}

	const sessionPin = "SHA256:previous-session-host"
	sess := pinnedGitSession{
		fakeRemoteSession: &fakeRemoteSession{id: "pinned-git", host: destination, fingerprint: sessionPin},
		opts:              opts,
	}
	provider := pinnedGitLaneProvider{fakeLaneProvider: &fakeLaneProvider{}, bridge: stand.helper}
	store, installs := testConsentStoresFor(t, sessionPin)
	selectGit, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, discardLogger(t))
	endStandOnCleanup(t, nil, reg)
	selection := selectGit(sess)
	if selection.Factory == nil {
		t.Fatalf("git selection refused before opening its bridge: %+v", selection.Refusal)
	}
	repo, outcome, err := selection.Factory.Open(ctx, fixtureRepo(t))
	if repo != nil {
		_ = repo.Close()
		t.Fatal("git opened a repository on a host with another key")
	}
	var refusal *helperclient.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != proto.ErrCodeHostKeyPinMismatch {
		t.Fatalf("git bridge refusal = %v (outcome %+v), want host-key-pin-mismatch", err, outcome)
	}
	if commands := srv.execCommands(); len(commands) != 0 {
		t.Fatalf("refused git bridge executed commands: %v", commands)
	}

	// Refusing this bridge must leave the original holder's pooled connection
	// usable, without authenticating a replacement transport.
	valid, err := probes.acquire(ctx, destination, append(opts, ssh.WithHostKeyPin(pwServerFingerprint(srv)))...)
	if err != nil {
		t.Fatalf("original host key no longer acquires its connection: %v", err)
	}
	if err := valid.Close(); err != nil {
		t.Fatalf("close matching acquisition: %v", err)
	}
	if got := srv.connCount(); got != 1 {
		t.Fatalf("git bridge refusal or matching acquisition redialed: %d connections, want 1", got)
	}
}
