//go:build linux || darwin

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// This is a real native process/restart scenario, not a mocked acknowledgement:
// the production opener, coordinator, encrypted store and WS handlers all run.
func TestSandboxNativeReplacementKeepsPidAcrossRestartAndRemoveIsOneAttempt(t *testing.T) {
	if err := sandbox.NativeAvailable(); err != nil {
		t.Skipf("native backend unsupported: %v", err)
	}
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper")) })
	work, ro, outside := filepath.Join(home, "project"), filepath.Join(home, "readonly"), filepath.Join(home, "outside")
	for _, dir := range []string{work, ro, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "outside-file"), []byte("native fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	probePath := filepath.Join(work, "observe-policy.sh")
	probe := fmt.Sprintf("#!/bin/sh\nresult=%s\n: >\"$result\"\nif cat %s >/dev/null 2>&1; then printf 'outside-readable\\n' >>\"$result\"; else printf 'outside-denied\\n' >>\"$result\"; fi\nif (printf fixture >%s) 2>/dev/null; then printf 'ro-writable\\n' >>\"$result\"; else printf 'ro-locked\\n' >>\"$result\"; fi\nif [ \"$HOME\" = %s ]; then printf 'host-home\\n' >>\"$result\"; else printf 'private-home\\n' >>\"$result\"; fi\nprintf 'complete\\n' >>\"$result\"\n", quote(filepath.Join(work, "observed-policy")), quote(filepath.Join(outside, "outside-file")), quote(filepath.Join(ro, "write-attempt")), quote(home))
	if err := os.WriteFile(probePath, []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const workspaceID = "019cf90f-0000-7000-8000-000000000001"
	const tabID = "019cf90f-0000-7000-8000-000000000002"
	const paneID = "019cf90f-0000-7000-8000-000000000003"
	first := bootLocalAppOn(t, src)
	conn := dialRenderer(t, first)
	if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		t.Fatal(err)
	}
	created := callAppWS(t, conn, "workspaces.create", map[string]any{
		"id": workspaceID, "name": "Native replacement", "colour": nil, "position": 0,
		"firstTab":  map[string]any{"id": tabID, "name": nil, "colour": nil, "position": 0, "pinned": false, "layout": "row"},
		"firstPane": map[string]any{"id": paneID, "cwd": work, "kind": "local", "endpoint": nil, "sizeShare": 1},
	}, 19)
	if created.Error != nil {
		t.Fatalf("workspaces.create: %+v", created.Error)
	}
	ordinary, err := first.Transport.OpenSession(ctx, transport.OpenSpec{PaneID: paneID, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	profileResponse := callAppWS(t, conn, "sandbox.profile.update", map[string]any{"expectedRevision": 0, "enabled": true, "roots": map[string]any{"readOnlyDirs": []string{ro}, "readWriteDirs": []string{}}}, 20)
	if profileResponse.Error != nil {
		t.Fatalf("enable profile: %+v", profileResponse.Error)
	}
	identity := ordinary.Session.Identity()
	previewResponse := callAppWS(t, conn, "sandbox.preview", map[string]any{"paneId": paneID, "source": map[string]any{"sessionId": string(ordinary.Session.ID()), "instanceId": string(identity.InstanceID), "sessionEpoch": identity.Epoch}, "expectedHeadId": "", "mode": "enforce", "delta": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}}}, 21)
	if previewResponse.Error != nil {
		t.Fatalf("preview: %+v", previewResponse.Error)
	}
	var preview transport.SandboxPreviewResult
	if err = json.Unmarshal(previewResponse.Result, &preview); err != nil {
		t.Fatal(err)
	}
	replaceResponse := callAppWS(t, conn, "sandbox.replace", map[string]any{"operationId": preview.OperationID, "confirmationId": preview.ConfirmationID}, 22)
	if replaceResponse.Error != nil {
		t.Fatalf("replace: %+v", replaceResponse.Error)
	}
	var replaced struct {
		State string `json:"state"`
		Open  *struct {
			SessionID string `json:"sessionId"`
		} `json:"open"`
	}
	if err = json.Unmarshal(replaceResponse.Result, &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.State != "active" || replaced.Open == nil || replaced.Open.SessionID == string(ordinary.Session.ID()) {
		t.Fatalf("replacement did not select a native candidate: %+v", replaced)
	}
	sid := session.ID(replaced.Open.SessionID)
	native, err := first.Session.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	defer logSandboxNativeScreenOnFailure(t, first, sid)
	pid, known := first.Session.OwnedProcessPID(sid)
	if !known || pid <= 0 {
		t.Fatal("native process PID was not recorded")
	}
	if _, err = ordinary.Session.Write([]byte("printf must-not-run\\n")); !errors.Is(err, session.ErrInputRefused) {
		t.Fatalf("retired source accepted new input: %v", err)
	}
	if _, err = native.Write([]byte("/bin/sh " + quote(probePath) + "\n")); err != nil {
		t.Fatal(err)
	}
	observed := waitSandboxNativeResult(t, filepath.Join(work, "observed-policy"))
	if observed != "outside-denied\nro-locked\nprivate-home\ncomplete\n" {
		t.Fatalf("native filesystem/home policy: %q", observed)
	}
	grant, err := first.launches.GetLaunch(ctx, preview.OperationID)
	if err != nil || grant.GrantID == nil || grant.Helper == nil {
		t.Fatalf("persisted native grant: %+v %v", grant, err)
	}
	_ = conn.Close()
	first.Shutdown(context.Background())

	second := bootLocalAppOn(t, src)
	restored, err := second.Session.Get(sid)
	if err != nil {
		t.Fatalf("protected native session not recovered: %v", err)
	}
	restoredPID, known := second.Session.OwnedProcessPID(restored.ID())
	if !known || restoredPID != pid {
		t.Fatalf("restart replaced native process: old=%d restored=%d", pid, restoredPID)
	}
	restoredGrant, err := second.launches.Head(ctx, paneID)
	if err != nil || restoredGrant.ID != grant.ID || restoredGrant.GrantID == nil || *restoredGrant.GrantID != *grant.GrantID || restoredGrant.PolicyDigest != grant.PolicyDigest {
		t.Fatalf("restart changed immutable authority: %+v %v", restoredGrant, err)
	}
	conn2 := dialRenderer(t, second)
	if err = conn2.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		t.Fatal(err)
	}
	replayResponse := callAppWS(t, conn2, "sandbox.operation.get", map[string]any{"operationId": preview.OperationID}, 23)
	if replayResponse.Error != nil {
		t.Fatalf("lost-response recovery: %+v", replayResponse.Error)
	}
	var replay struct {
		Open *struct {
			SessionID string `json:"sessionId"`
		} `json:"open"`
	}
	if err = json.Unmarshal(replayResponse.Result, &replay); err != nil || replay.Open == nil || replay.Open.SessionID != string(sid) {
		t.Fatalf("operation recovery lost same process: %+v %v", replay, err)
	}
	status, err := second.sandboxCoordinator.Status(ctx, transport.SandboxStatusRequest{PaneID: paneID})
	if err != nil || status.Source == nil {
		t.Fatalf("restored source claim: %+v %v", status, err)
	}
	removePreviewResponse := callAppWS(t, conn2, "sandbox.preview", map[string]any{"paneId": paneID, "source": status.Source, "expectedHeadId": grant.ID, "mode": "off", "delta": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}}}, 24)
	if removePreviewResponse.Error != nil {
		t.Fatalf("remove preview: %+v", removePreviewResponse.Error)
	}
	var removePreview transport.SandboxPreviewResult
	if err = json.Unmarshal(removePreviewResponse.Result, &removePreview); err != nil {
		t.Fatal(err)
	}
	removeParams := map[string]any{"operationId": removePreview.OperationID, "confirmationId": removePreview.ConfirmationID}
	removeResponse := callAppWS(t, conn2, "sandbox.replace", removeParams, 25)
	if removeResponse.Error != nil {
		t.Fatalf("remove: %+v", removeResponse.Error)
	}
	var removed struct {
		Open *struct {
			SessionID string `json:"sessionId"`
		} `json:"open"`
	}
	if err = json.Unmarshal(removeResponse.Result, &removed); err != nil || removed.Open == nil {
		t.Fatalf("remove did not publish: %+v %v", removed, err)
	}
	offID := session.ID(removed.Open.SessionID)
	offPID, known := second.Session.OwnedProcessPID(offID)
	if !known || offPID <= 0 {
		t.Fatal("unrestricted replacement PID was not recorded")
	}
	repeatedRemoval := callAppWS(t, conn2, "sandbox.replace", removeParams, 27)
	if repeatedRemoval.Error != nil {
		t.Fatalf("consumed removal replay: %+v", repeatedRemoval.Error)
	}
	removed.Open = nil
	if err = json.Unmarshal(repeatedRemoval.Result, &removed); err != nil || removed.Open == nil || removed.Open.SessionID != string(offID) {
		t.Fatalf("repeated removal selected a different process: %+v %v", removed, err)
	}
	_ = conn2.Close()
	second.Shutdown(ctx)
	second = bootLocalAppOn(t, src)
	off, err := second.Session.Get(offID)
	if err != nil {
		t.Fatalf("live Off replacement was not restored: %v", err)
	}
	restoredOffPID, known := second.Session.OwnedProcessPID(offID)
	offBinding, bindingKnown := second.Session.LaunchBinding(offID)
	if !known || restoredOffPID != offPID || !bindingKnown || offBinding.Mode != "off" || offBinding.LaunchID != removePreview.OperationID || offBinding.GrantID != 0 {
		t.Fatalf("Off restart changed process or authority: PID=%d binding=%+v", restoredOffPID, offBinding)
	}
	conn2 = dialRenderer(t, second)
	if err = conn2.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(work, "observed-policy")); err != nil {
		t.Fatal(err)
	}
	if _, err = off.Write([]byte("/bin/sh " + quote(probePath) + "\n")); err != nil {
		t.Fatal(err)
	}
	if observed = waitSandboxNativeResult(t, filepath.Join(work, "observed-policy")); observed != "outside-readable\nro-writable\nhost-home\ncomplete\n" {
		t.Fatalf("confirmed removal did not restore ordinary scope: %q", observed)
	}
	if err = second.Session.EndSession(offID); err != nil {
		t.Fatal(err)
	}
	closedReplay := callAppWS(t, conn2, "sandbox.operation.get", map[string]any{"operationId": removePreview.OperationID}, 26)
	if closedReplay.Error != nil {
		t.Fatalf("closed removal lookup: %+v", closedReplay.Error)
	}
	removed.Open = nil
	if err = json.Unmarshal(closedReplay.Result, &removed); err != nil || removed.Open != nil {
		t.Fatalf("closed removal was republished after restart: %+v %v", removed, err)
	}
	inventory, err := second.sandboxHelper.SandboxInventory(ctx, restoredGrant.TargetGeneration)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range inventory {
		if entry.HostSessionID.Session != string(offID) && entry.Exit == nil {
			t.Fatalf("replay spawned another live process: %+v", entry)
		}
	}
	if _, err = second.Session.Get(offID); err == nil {
		t.Fatal("closed removal was republished on replay")
	}
	t.Log("NATIVE_REPLACEMENT_RESTART_REMOVE_PROOF_COMPLETE")
}

func waitSandboxNativeResult(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		body, err := os.ReadFile(path) // #nosec G304 — this scenario's own temporary result file.
		if err == nil && strings.HasSuffix(string(body), "complete\n") {
			return string(body)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("native program did not complete; observed %q", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func logSandboxNativeScreenOnFailure(t *testing.T, a *App, sid session.ID) {
	t.Helper()
	if !t.Failed() {
		return
	}
	if err := a.paneViews.Enrol(string(sid)); err != nil {
		t.Logf("native fixture screen enrolment: %v", err)
		return
	}
	frame, err := a.paneViews.Frame(string(sid))
	if err != nil {
		t.Logf("native fixture screen read: %v", err)
		return
	}
	for row := range frame.Rows {
		if line := strings.TrimSpace(frame.Text(row)); line != "" {
			t.Logf("native fixture screen row %d: %s", row+1, line)
		}
	}
}
