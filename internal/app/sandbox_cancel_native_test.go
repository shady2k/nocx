//go:build linux || darwin

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

func TestSandboxNativeCancelDuringReplacementResumesSource(t *testing.T) {
	testSandboxNativeInterruptedReplacement(t, false)
}

func TestSandboxNativeShutdownDuringReplacementDrainsCandidate(t *testing.T) {
	testSandboxNativeInterruptedReplacement(t, true)
}

func testSandboxNativeInterruptedReplacement(t *testing.T, shutdown bool) {
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
	a := bootLocalAppOn(t, src)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn := dialRenderer(t, a)
	defer func() { _ = conn.Close() }()
	cancelConn := dialRenderer(t, a)
	defer func() { _ = cancelConn.Close() }()
	deadline := time.Now().Add(2 * time.Minute)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := cancelConn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	const workspaceID = "019cf90f-0000-7000-8000-000000000101"
	const tabID = "019cf90f-0000-7000-8000-000000000102"
	const paneID = "019cf90f-0000-7000-8000-000000000103"
	created := callAppWS(t, conn, "workspaces.create", map[string]any{
		"id": workspaceID, "name": "Cancel replacement", "colour": nil, "position": 0,
		"firstTab":  map[string]any{"id": tabID, "name": nil, "colour": nil, "position": 0, "pinned": false, "layout": "row"},
		"firstPane": map[string]any{"id": paneID, "cwd": work, "kind": "local", "endpoint": nil, "sizeShare": 1},
	}, 101)
	if created.Error != nil {
		t.Fatalf("workspaces.create: %+v", created.Error)
	}
	ordinary, err := a.Transport.OpenSession(ctx, transport.OpenSpec{PaneID: paneID, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	profile := callAppWS(t, conn, "sandbox.profile.update", map[string]any{"expectedRevision": 0, "enabled": true, "roots": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}}}, 102)
	if profile.Error != nil {
		t.Fatalf("enable profile: %+v", profile.Error)
	}
	identity := ordinary.Session.Identity()
	previewResponse := callAppWS(t, conn, "sandbox.preview", map[string]any{
		"paneId":         paneID,
		"source":         map[string]any{"sessionId": string(ordinary.Session.ID()), "instanceId": string(identity.InstanceID), "sessionEpoch": identity.Epoch},
		"expectedHeadId": "", "mode": "enforce", "delta": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}},
	}, 103)
	if previewResponse.Error != nil {
		t.Fatalf("preview: %+v", previewResponse.Error)
	}
	var preview transport.SandboxPreviewResult
	if err = json.Unmarshal(previewResponse.Result, &preview); err != nil {
		t.Fatal(err)
	}
	ref := session.Ref{ID: ordinary.Session.ID(), Identity: identity}
	admitted := make(chan struct{})
	releaseInput := make(chan struct{})
	inputDone := make(chan error, 1)
	go func() {
		inputDone <- a.Session.WithInput(ctx, ref, func() error {
			close(admitted)
			select {
			case <-releaseInput:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-admitted:
	case <-ctx.Done():
		t.Fatal("source input was not admitted")
	}
	type replaceRPCResult struct {
		response *wsRPCResult
		err      error
	}
	replaceDone := make(chan replaceRPCResult, 1)
	go func() {
		response, callErr := callAppWSNoFatal(conn, "sandbox.replace", map[string]any{"operationId": preview.OperationID, "confirmationId": preview.ConfirmationID}, 104)
		replaceDone <- replaceRPCResult{response: response, err: callErr}
	}()
	launch := waitNativeLaunch(t, ctx, a, preview.OperationID, content.LaunchPreparing)
	if launch.TargetGeneration == "" {
		t.Fatalf("replacement has no target helper generation: %+v", launch)
	}
	var candidateSessionID session.ID
	var candidatePID int
	waitUntilNative(t, ctx, "private native candidate present in helper inventory", func() bool {
		entries, inventoryErr := a.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
		if inventoryErr != nil {
			return false
		}
		for _, entry := range entries {
			if entry.HostSessionID.Generation == launch.TargetGeneration && entry.HostSessionID.Session != launch.Source.SessionID && entry.Launch != nil && entry.Launch.Pid > 0 && entry.Exit == nil {
				candidateSessionID = session.ID(entry.HostSessionID.Session)
				candidatePID = entry.Launch.Pid
				return true
			}
		}
		return false
	})
	waitUntilNative(t, ctx, "source fence waiting for admitted input to drain", func() bool {
		return !a.Session.InputAllowed(ordinary.Session.ID())
	})
	if shutdown {
		a.Shutdown(ctx)
		close(releaseInput)
		select {
		case <-inputDone:
		case <-ctx.Done():
			t.Fatal("held source input did not finish after shutdown")
		}
		_ = waitNativeLaunch(t, ctx, a, preview.OperationID, content.LaunchFailed)
		entries, inventoryErr := a.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
		if inventoryErr != nil {
			t.Fatal(inventoryErr)
		}
		sourceAlive := false
		for _, entry := range entries {
			if entry.Launch != nil && entry.Launch.Pid == candidatePID && entry.Exit == nil {
				t.Fatal("shutdown returned with an unpublished native candidate alive")
			}
			if entry.HostSessionID.Session == launch.Source.SessionID && entry.Exit == nil {
				sourceAlive = true
			}
		}
		if !sourceAlive {
			t.Fatal("shutdown ended the unchanged source process")
		}
		if _, headErr := a.launches.Head(ctx, paneID); !errors.Is(headErr, content.ErrNotFound) {
			t.Fatalf("shutdown selected an uncommitted candidate: %v", headErr)
		}
		t.Log("NATIVE_SHUTDOWN_DRAIN_ROLLBACK_PROOF_COMPLETE")
		return
	}
	cancelResponse := callAppWS(t, cancelConn, "sandbox.cancel", map[string]any{"operationId": preview.OperationID, "confirmationId": preview.ConfirmationID}, 105)
	if cancelResponse.Error != nil {
		t.Fatalf("sandbox.cancel during replacement: %+v", cancelResponse.Error)
	}
	close(releaseInput)
	select {
	case inputErr := <-inputDone:
		if inputErr != nil {
			t.Fatalf("held source input: %v", inputErr)
		}
	case <-ctx.Done():
		t.Fatal("held source input did not drain")
	}
	select {
	case replaceResult := <-replaceDone:
		if replaceResult.err != nil {
			t.Fatalf("sandbox.replace RPC: %v", replaceResult.err)
		}
		if replaceResult.response.Error == nil {
			t.Fatalf("cancelled replace unexpectedly succeeded: %s", replaceResult.response.Result)
		}
	case <-ctx.Done():
		t.Fatal("replacement RPC did not return after cancellation")
	}
	waitUntilNative(t, ctx, "durable failed state and resolved candidate retirement", func() bool {
		failed, getErr := a.launches.GetLaunch(ctx, preview.OperationID)
		if getErr != nil || failed.State != content.LaunchFailed || failed.Helper == nil || failed.Helper.SessionID != string(candidateSessionID) || candidatePID <= 0 {
			return false
		}
		entries, inventoryErr := a.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
		if inventoryErr != nil {
			return false
		}
		for _, entry := range entries {
			if entry.HostSessionID.Generation == launch.TargetGeneration && entry.HostSessionID.Session == string(candidateSessionID) && entry.Launch != nil && entry.Launch.Pid == candidatePID && entry.Exit == nil {
				return false
			}
		}
		return true
	})
	if _, err = a.Session.Get(candidateSessionID); err == nil {
		t.Fatal("cancelled candidate was published as an ordinary session")
	}
	if _, err = a.launches.Head(ctx, paneID); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("cancelled replacement committed a head: %v", err)
	}
	if !a.Session.InputAllowed(ordinary.Session.ID()) {
		t.Fatal("cancelled replacement left the source fenced")
	}
	retirement, err := a.launches.Retirement(ctx, content.HelperIdentity{
		Generation: launch.TargetGeneration, SessionID: string(candidateSessionID),
	})
	if err != nil || retirement.ClosePending || retirement.Cause != content.RetirementFailedCandidate {
		t.Fatalf("cancelled candidate retirement was not completed: %+v %v", retirement, err)
	}
	sourceProof := filepath.Join(work, "source-after-cancel.sh")
	if err = os.WriteFile(sourceProof, []byte("#!/bin/sh\nprintf '\\033[2J\\033[3;5HSANDBOX_CANCEL_SOURCE_STILL_USABLE\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = a.paneViews.Enrol(string(ordinary.Session.ID())); err != nil {
		t.Fatal(err)
	}
	if _, err = ordinary.Session.Write([]byte("/bin/sh " + sourceProof + "\n")); err != nil {
		t.Fatalf("ordinary source write after cancellation: %v", err)
	}
	waitForMarker(t, a, ordinary.Session.ID(), "SANDBOX_CANCEL_SOURCE_STILL_USABLE")

	unusedResponse := callAppWS(t, conn, "sandbox.preview", map[string]any{
		"paneId": paneID, "source": map[string]any{"sessionId": string(ordinary.Session.ID()), "instanceId": string(identity.InstanceID), "sessionEpoch": identity.Epoch},
		"expectedHeadId": "", "mode": "enforce", "delta": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}},
	}, 106)
	if unusedResponse.Error != nil {
		t.Fatalf("unused preview: %+v", unusedResponse.Error)
	}
	var unused transport.SandboxPreviewResult
	if err = json.Unmarshal(unusedResponse.Result, &unused); err != nil {
		t.Fatal(err)
	}
	livePIDs := func() map[string]int {
		entries, inventoryErr := a.sandboxHelper.SandboxInventory(ctx, launch.TargetGeneration)
		if inventoryErr != nil {
			t.Fatalf("helper inventory: %v", inventoryErr)
		}
		pids := make(map[string]int)
		for _, entry := range entries {
			if entry.Launch != nil && entry.Exit == nil {
				pids[entry.HostSessionID.Session] = entry.Launch.Pid
			}
		}
		return pids
	}
	beforeCancel := livePIDs()
	unusedParams := map[string]any{"operationId": unused.OperationID, "confirmationId": unused.ConfirmationID}
	unusedCancel := callAppWS(t, cancelConn, "sandbox.cancel", unusedParams, 107)
	if unusedCancel.Error != nil {
		t.Fatalf("cancel unused preview: %+v", unusedCancel.Error)
	}
	refused := callAppWS(t, conn, "sandbox.replace", unusedParams, 108)
	if refused.Error == nil {
		t.Fatalf("replace after cancelling unused preview unexpectedly succeeded: %s", refused.Result)
	}
	afterCancel := livePIDs()
	if len(afterCancel) != len(beforeCancel) {
		t.Fatalf("cancelled unused preview changed live native inventory: before=%v after=%v", beforeCancel, afterCancel)
	}
	for sessionID, pid := range beforeCancel {
		if afterCancel[sessionID] != pid {
			t.Fatalf("cancelled unused preview changed helper process %s: before PID %d after PID %d", sessionID, pid, afterCancel[sessionID])
		}
	}
}

func waitNativeLaunch(t *testing.T, ctx context.Context, a *App, id string, state content.LaunchState) content.Launch {
	t.Helper()
	var got content.Launch
	waitUntilNative(t, ctx, "durable sandbox launch state", func() bool {
		var err error
		got, err = a.launches.GetLaunch(ctx, id)
		return err == nil && got.State == state
	})
	return got
}

func waitUntilNative(t *testing.T, ctx context.Context, what string, observed func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if observed() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", what, ctx.Err())
		case <-ticker.C:
		}
	}
}

func callAppWSNoFatal(conn *websocket.Conn, method string, params map[string]any, id int) (*wsRPCResult, error) {
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	if err = conn.WriteMessage(websocket.TextMessage, request); err != nil {
		return nil, err
	}
	for {
		messageType, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return nil, readErr
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var response wsRPCResult
		if err = json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		if response.ID == id {
			return &response, nil
		}
	}
}
