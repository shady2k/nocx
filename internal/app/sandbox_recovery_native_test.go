//go:build linux || darwin

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// This drives the durable preparing boundary directly, then replaces the App
// before its normal commit/publication phase can run. The candidate itself is
// opened by the production local helper adapter and is a real native process.
func TestSandboxNativePreparingRecoveryRollsBackUnpublishedCandidate(t *testing.T) {
	testSandboxNativeCrashRecovery(t, content.LaunchPreparing)
}

func TestSandboxNativeCommittedRecoveryPublishesTheSameCandidate(t *testing.T) {
	testSandboxNativeCrashRecovery(t, content.LaunchActive)
}

func TestSandboxNativeExitedWhileCoordinatorAbsentRestoresEnded(t *testing.T) {
	testSandboxNativeCrashRecovery(t, content.LaunchEnded)
}

func testSandboxNativeCrashRecovery(t *testing.T, targetState content.LaunchState) {
	if err := sandbox.NativeAvailable(); err != nil {
		t.Skipf("native backend unsupported: %v", err)
	}
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper")) })
	work := filepath.Join(home, "project")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const workspaceID = "019cf90f-0000-7000-8000-000000000011"
	const tabID = "019cf90f-0000-7000-8000-000000000012"
	const paneID = "019cf90f-0000-7000-8000-000000000013"
	first := bootLocalAppOn(t, src)
	conn := dialRenderer(t, first)
	if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		t.Fatal(err)
	}
	created := callAppWS(t, conn, "workspaces.create", map[string]any{
		"id": workspaceID, "name": "Native recovery", "colour": nil, "position": 0,
		"firstTab":  map[string]any{"id": tabID, "name": nil, "colour": nil, "position": 0, "pinned": false, "layout": "row"},
		"firstPane": map[string]any{"id": paneID, "cwd": work, "kind": "local", "endpoint": nil, "sizeShare": 1},
	}, 31)
	if created.Error != nil {
		t.Fatalf("workspaces.create: %+v", created.Error)
	}
	ordinary, err := first.Transport.OpenSession(ctx, transport.OpenSpec{PaneID: paneID, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	enabled := callAppWS(t, conn, "sandbox.profile.update", map[string]any{"expectedRevision": 0, "enabled": true, "roots": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}}}, 32)
	if enabled.Error != nil {
		t.Fatalf("enable profile: %+v", enabled.Error)
	}
	profile, err := first.sandboxCoordinator.Profile(ctx, transport.SandboxProfileRequest{WorkspaceID: workspaceID})
	if err != nil || profile.Workspace == nil {
		t.Fatalf("sandbox profile: %+v %v", profile, err)
	}
	source, err := first.launches.SourceBinding(ctx, paneID, string(ordinary.Session.ID()))
	if err != nil || source.Host != "" || source.Account != "" || source.Generation == "" || source.SessionID != string(ordinary.Session.ID()) {
		t.Fatalf("ordinary source binding: %+v %v", source, err)
	}
	sourcePID, sourcePIDKnown := first.Session.OwnedProcessPID(ordinary.Session.ID())
	if !sourcePIDKnown || sourcePID <= 0 {
		t.Fatal("ordinary source has no owned PID")
	}
	cfg := session.Config{Kind: session.KindLocal, Cwd: work, PaneID: paneID, Enhanced: true, LaunchBinding: session.LaunchBinding{Mode: string(content.LaunchEnforce)}}
	size := ordinary.Session.EffectiveSize()
	cfg.Cols, cfg.Rows, cfg.XPixel, cfg.YPixel = size.Cols, size.Rows, size.XPixel, size.YPixel
	cfg.Parent, _ = ordinary.Session.Parent()

	stage := func(owner *App, operationID string, openCandidate bool) (*transport.SandboxCandidate, content.Launch) {
		t.Helper()
		params := proto.SandboxPrepareParams{
			OperationID: operationID, LaunchID: operationID, Mode: proto.SandboxEnforce, Workspace: proto.WorkspaceID(workspaceID), Cwd: work,
			Enforce: &proto.SandboxEnforceIntent{StandardRevision: profile.Standard.Revision, WorkspaceRevision: profile.Workspace.Revision, Profile: profile.Effective, ProfileProvenance: sandbox.StandardRoot, Delta: sandbox.ProfileRoots{}},
		}
		prepared, prepareErr := owner.sandboxHelper.SandboxPrepare(ctx, params)
		if prepareErr != nil {
			t.Fatalf("helper prepare %s: %v", operationID, prepareErr)
		}
		if prepared.Preparation.Policy == nil || prepared.Preparation.Digest == "" {
			t.Fatalf("helper did not mint immutable enforce grant: %+v", prepared.Preparation)
		}
		launch, prepareErr := owner.launches.Prepare(ctx, content.LaunchPrepare{ID: operationID, PaneID: paneID, WorkspaceID: workspaceID, Source: source, TargetGeneration: prepared.Generation, ExpectedHeadID: "", StandardRevision: profile.Standard.Revision, WorkspaceRevision: profile.Workspace.Revision, Mode: content.LaunchEnforce, Policy: prepared.Preparation.Policy, PolicyDigest: prepared.Preparation.Digest, PolicyVersion: prepared.Preparation.Policy.Version})
		if prepareErr != nil {
			t.Fatalf("persist launch preparation %s: %v", operationID, prepareErr)
		}
		if launch.State != content.LaunchPreparing || launch.GrantID == nil || launch.Helper != nil {
			t.Fatalf("unexpected preparing launch: %+v", launch)
		}
		if !openCandidate {
			return nil, launch
		}
		cfg.LaunchBinding = session.LaunchBinding{Mode: string(launch.Mode), LaunchID: launch.ID, Digest: launch.PolicyDigest, Version: launch.PolicyVersion, GrantID: *launch.GrantID}
		paramsForLaunch := proto.SandboxLaunchParams{
			Ticket: prepared.Preparation.Ticket, OperationID: operationID, LaunchID: operationID, Mode: proto.SandboxEnforce,
			Grant: &proto.SandboxGrantBinding{ID: *launch.GrantID, Digest: launch.PolicyDigest, Version: launch.PolicyVersion},
			Shape: proto.SandboxLaunchShape{Cols: cfg.Cols, Rows: cfg.Rows, XPixel: cfg.XPixel, YPixel: cfg.YPixel},
		}
		candidate, openErr := owner.sandboxHelper.SandboxOpenCandidate(ctx, cfg, prepared, paramsForLaunch)
		if openErr != nil {
			t.Fatalf("open real candidate %s: %v", operationID, openErr)
		}
		return &candidate, launch
	}

	candidate, launch := stage(first, "019cf90f000070008000000000000021", true)
	pid, ok := 0, candidate.Entry.Launch != nil
	if ok {
		pid = candidate.Entry.Launch.Pid
	}
	if !ok || pid <= 0 || candidate.Entry.Exit != nil || candidate.Identity.Generation != launch.TargetGeneration || candidate.Identity.Host != "" || candidate.Identity.Account != "" {
		t.Fatalf("candidate is not a live local native process: %+v", candidate)
	}
	binding, err := first.sandboxHelper.SandboxBinding(ctx, candidate.Identity)
	if err != nil || binding.LaunchID != launch.ID || binding.OperationID != launch.ID || binding.Grant == nil || binding.Grant.ID != *launch.GrantID {
		t.Fatalf("candidate immutable helper binding: %+v %v", binding, err)
	}
	if _, err = first.Session.Get(session.ID(candidate.Identity.SessionID)); err == nil {
		t.Fatal("uncommitted candidate was published in the local registry")
	}
	if head, headErr := first.launches.Head(ctx, paneID); !errors.Is(headErr, content.ErrNotFound) {
		t.Fatalf("preparing operation changed public head: %+v %v", head, headErr)
	}
	inventory, err := first.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
	if err != nil || !nativeInventoryHasLive(t, inventory, candidate.Identity.SessionID) {
		t.Fatalf("candidate absent from native helper inventory: %v %+v", err, inventory)
	}
	if targetState != content.LaunchPreparing {
		launch, err = first.launches.Commit(ctx, content.LaunchCommit{
			LaunchID: launch.ID, ExpectedSource: source, SourceCwd: work, Candidate: candidate.Identity,
			Binding: content.Session{ID: candidate.Identity.SessionID, WorkspaceID: workspaceID, PaneID: paneID, Generation: candidate.Identity.Generation, LifecycleApplied: new(uint64)},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Replace the coordinator after atomic selection, before publication
		// or retirement. The selected native process remains helper-owned.
		first.Shutdown(ctx)
		if targetState == content.LaunchEnded {
			// End only this fixture's exact owned shell after the coordinator
			// has left. Its helper must retain authoritative exit inventory.
			if err = syscall.Kill(pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				entries, inventoryErr := first.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
				exited := false
				for _, entry := range entries {
					if entry.HostSessionID.Session == candidate.Identity.SessionID && entry.Exit != nil {
						exited = true
					}
				}
				if inventoryErr == nil && exited {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("native exit was not retained while coordinator absent: %v %+v", inventoryErr, entries)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		second := bootLocalAppOn(t, src)
		if targetState == content.LaunchEnded {
			status, statusErr := second.sandboxCoordinator.Status(ctx, transport.SandboxStatusRequest{PaneID: paneID})
			if statusErr != nil || status.Head == nil || status.Head.LaunchID != launch.ID || status.Head.State != content.LaunchEnded || status.Head.Enforcement != "ended" || status.Source != nil {
				binding, bindingErr := second.sandboxHelper.SandboxBinding(ctx, candidate.Identity)
				t.Fatalf("cold exited launch is not definitively ended: status=%+v head=%+v error=%v binding=%+v bindingError=%v", status, status.Head, statusErr, binding, bindingErr)
			}
			head, headErr := second.launches.Head(ctx, paneID)
			if headErr != nil || head.ID != launch.ID || head.State != content.LaunchEnded || head.Helper == nil || *head.Helper != candidate.Identity || head.GrantID == nil || *head.GrantID != *launch.GrantID {
				t.Fatalf("cold exit changed selected launch identity or grant: %+v %v", head, headErr)
			}
			entries, inventoryErr := second.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
			if inventoryErr != nil || nativeInventoryHasLive(t, entries, candidate.Identity.SessionID) {
				t.Fatalf("cold exit was silently relaunched: %+v %v", entries, inventoryErr)
			}
			t.Log("NATIVE_OFFLINE_EXIT_RESTORES_ENDED_WITHOUT_SPAWN_PROOF_COMPLETE")
			return
		}
		recovered, lookupErr := second.Session.Get(session.ID(candidate.Identity.SessionID))
		if lookupErr != nil {
			t.Fatalf("committed candidate was not readopted: %v", lookupErr)
		}
		recoveredPID, pidKnown := second.Session.OwnedProcessPID(recovered.ID())
		grant, grantKnown := second.Session.LaunchBinding(recovered.ID())
		if !pidKnown || recoveredPID != pid || !grantKnown || grant.LaunchID != launch.ID || grant.GrantID != *launch.GrantID || grant.Digest != launch.PolicyDigest || grant.Version != launch.PolicyVersion {
			t.Fatalf("recovery changed committed process or grant: pid=%d binding=%+v", recoveredPID, grant)
		}
		head, headErr := second.launches.Head(ctx, paneID)
		if headErr != nil || head.ID != launch.ID || head.State != content.LaunchActive {
			t.Fatalf("committed head changed across publication recovery: %+v %v", head, headErr)
		}
		retirement, retireErr := second.launches.Retirement(ctx, source)
		if retireErr != nil || retirement.ClosePending || retirement.OperationID != launch.ID {
			t.Fatalf("source retirement was not completed: %+v %v", retirement, retireErr)
		}
		if _, lookupErr = second.Session.Get(ordinary.Session.ID()); lookupErr == nil {
			t.Fatal("recovery readopted the retired ordinary source")
		}
		entries, inventoryErr := second.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
		if inventoryErr != nil || nativeInventoryHasLive(t, entries, source.SessionID) || !nativeInventoryHasLive(t, entries, candidate.Identity.SessionID) {
			t.Fatalf("publication recovery native inventory: %+v %v", entries, inventoryErr)
		}
		screenProof := filepath.Join(work, "recovered-screen.sh")
		if err = os.WriteFile(screenProof, []byte("#!/bin/sh\nprintf '\\033[2J\\033[3;5HSANDBOX_COMMITTED_RECOVERY_SCREEN_READY\\n'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = second.paneViews.Enrol(string(recovered.ID())); err != nil {
			t.Fatalf("recovered native screen unavailable: %v", err)
		}
		if _, err = recovered.Write([]byte("/bin/sh " + screenProof + "\n")); err != nil {
			t.Fatalf("recovered native process refused input: %v", err)
		}
		waitForMarker(t, second, recovered.ID(), "SANDBOX_COMMITTED_RECOVERY_SCREEN_READY")
		recoveredConn := dialRenderer(t, second)
		defer func() { _ = recoveredConn.Close() }()
		if err = recoveredConn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for rpcID := 40; rpcID < 42; rpcID++ {
			response := callAppWS(t, recoveredConn, "sandbox.operation.get", map[string]any{"operationId": launch.ID}, rpcID)
			if response.Error != nil {
				t.Fatalf("recovered operation RPC: %+v", response.Error)
			}
			var result struct {
				Open *struct {
					SessionID string `json:"sessionId"`
				} `json:"open"`
			}
			if err = json.Unmarshal(response.Result, &result); err != nil || result.Open == nil || result.Open.SessionID != candidate.Identity.SessionID {
				t.Fatalf("recovered operation did not bind the selected native process: %+v %v", result, err)
			}
		}
		t.Log("NATIVE_COMMITTED_PUBLICATION_RECOVERY_PROOF_COMPLETE")
		return
	}
	// Hide only this fixture's endpoint namespace. The daemon and candidate
	// remain alive; an unreachable exact generation is not proof of absence.
	socketPath, err := endpoint.Path(endpoint.Dir(home), proto.GenerationID(launch.TargetGeneration))
	if err != nil {
		t.Fatal(err)
	}
	hiddenSocket := socketPath + ".hidden"
	if err = os.Rename(socketPath, hiddenSocket); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if restoreErr := os.Rename(hiddenSocket, socketPath); restoreErr != nil {
				t.Fatal(restoreErr)
			}
		}()
		if err = first.sandboxCoordinator.Recover(ctx); err == nil {
			t.Fatal("unreachable exact helper generation was treated as cleanup proof")
		}
		pending, lookupErr := first.launches.GetLaunch(ctx, launch.ID)
		if lookupErr != nil || pending.State != content.LaunchPreparing {
			t.Fatalf("unknown cleanup erased its durable claim: %+v %v", pending, lookupErr)
		}
		for _, mode := range []content.LaunchMode{content.LaunchEnforce, content.LaunchOff} {
			_, previewErr := first.sandboxCoordinator.Preview(ctx, transport.SandboxPreviewRequest{
				PaneID: paneID, Source: &transport.SandboxSource{
					SessionID:    string(ordinary.Session.ID()),
					InstanceID:   string(ordinary.Session.Identity().InstanceID),
					SessionEpoch: ordinary.Session.Identity().Epoch,
				}, Mode: mode,
			})
			if previewErr == nil {
				t.Fatalf("%s bypassed unresolved native candidate cleanup", mode)
			}
		}
		peer, _, connectErr := first.localHelper.connect(ctx)
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		live, listErr := peer.Sessions(ctx)
		if listErr != nil || !nativeInventoryHasLive(t, live, candidate.Identity.SessionID) {
			t.Fatalf("unknown cleanup did not preserve actual process: %+v %v", live, listErr)
		}
	}()

	_ = conn.Close()
	first.Shutdown(context.Background())

	second := bootLocalAppOn(t, src)
	failed, err := second.launches.GetLaunch(ctx, launch.ID)
	if err != nil || failed.State != content.LaunchFailed || failed.Helper == nil || *failed.Helper != candidate.Identity {
		t.Fatalf("recovered candidate launch: %+v %v", failed, err)
	}
	retirement, err := second.launches.Retirement(ctx, candidate.Identity)
	if err != nil || retirement.Cause != content.RetirementFailedCandidate || retirement.OperationID != launch.ID || retirement.ClosePending {
		t.Fatalf("candidate retirement was not durably completed: %+v %v", retirement, err)
	}
	if _, err = second.launches.Head(ctx, paneID); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("recovery published a failed candidate head: %v", err)
	}
	if _, err = second.Session.Get(session.ID(candidate.Identity.SessionID)); err == nil {
		t.Fatal("recovery generically adopted the failed candidate")
	}
	inventory, err = second.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
	if err != nil || nativeInventoryHasLive(t, inventory, candidate.Identity.SessionID) {
		t.Fatalf("candidate process survived rollback: %v %+v", err, inventory)
	}
	if _, err = second.launches.SourceBinding(ctx, paneID, candidate.Identity.SessionID); err == nil {
		t.Fatal("failed candidate acquired a public pane binding")
	}
	ordinaryAgain, err := second.Session.Get(ordinary.Session.ID())
	if err != nil {
		t.Fatalf("ordinary source was not readopted after recovery: %v", err)
	}
	recoveredSourcePID, recoveredSourceKnown := second.Session.OwnedProcessPID(ordinaryAgain.ID())
	if !recoveredSourceKnown || recoveredSourcePID != sourcePID || ordinaryAgain.ID() != ordinary.Session.ID() {
		t.Fatalf("ordinary source process changed across recovery: old=%d new=%d", sourcePID, recoveredSourcePID)
	}
	if _, err = ordinaryAgain.Write([]byte("printf source-still-live\\n")); err != nil {
		t.Fatalf("ordinary source lost continuity: %v", err)
	}
	if err = second.sandboxCoordinator.Recover(ctx); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
	inventory, err = second.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
	if err != nil || nativeInventoryHasLive(t, inventory, candidate.Identity.SessionID) {
		t.Fatalf("repeat recovery respawned candidate: %v %+v", err, inventory)
	}
	// After rollback resolves the pane's first preparing claim, stage an unused
	// preparation on that same pane and replace the coordinator again.
	unused, unusedLaunch := stage(second, "019cf90f000070008000000000000022", false)
	if unused != nil || unusedLaunch.State != content.LaunchPreparing {
		t.Fatalf("unused operation was not left preparing: %+v", unusedLaunch)
	}
	second.Shutdown(context.Background())
	third := bootLocalAppOn(t, src)
	unusedFailed, err := third.launches.GetLaunch(ctx, unusedLaunch.ID)
	if err != nil || unusedFailed.State != content.LaunchFailed || unusedFailed.Helper != nil {
		t.Fatalf("unused preparation did not fail without identity: %+v %v", unusedFailed, err)
	}
	inventory, err = third.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range inventory {
		if entry.Exit == nil && entry.HostSessionID.Session != source.SessionID {
			t.Fatalf("unused recovery spawned a live process: %+v", entry)
		}
	}
	t.Logf("NATIVE_PREPARING_RECOVERY_ROLLBACK_PROOF_COMPLETE pid=%d", pid)
}

func nativeInventoryHasLive(t *testing.T, inventory []helperclient.SessionEntry, sessionID string) bool {
	t.Helper()
	for _, entry := range inventory {
		if entry.HostSessionID.Session == sessionID {
			return entry.Exit == nil
		}
	}
	return false
}
