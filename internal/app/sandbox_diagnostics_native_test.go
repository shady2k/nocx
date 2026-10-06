//go:build linux || darwin

package app

import (
	"context"
	"encoding/json"
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

func TestSandboxNativeDiagnosticPromotionRequiresNewLaunch(t *testing.T) {
	if err := sandbox.NativeAvailable(); err != nil {
		t.Skipf("native backend unsupported: %v", err)
	}
	artifacts := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, artifacts.hash()), "nocx-helper")) })
	work, outside := filepath.Join(home, "project"), filepath.Join(home, "outside")
	for _, directory := range []string{work, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var canonicalErr error
	work, canonicalErr = filepath.EvalSymlinks(work)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	outside, canonicalErr = filepath.EvalSymlinks(outside)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	observedPath := filepath.Join(outside, "observed-file")
	if err := os.WriteFile(observedPath, []byte("native diagnostic fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	const workspaceID = "019cf910-0000-7000-8000-000000000001"
	const tabID = "019cf910-0000-7000-8000-000000000002"
	const paneID = "019cf910-0000-7000-8000-000000000003"
	app := bootLocalAppOn(t, artifacts)
	defer app.Shutdown(context.Background())
	conn := dialRenderer(t, app)
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	requestID := 300
	call := func(method string, params map[string]any, target any) {
		t.Helper()
		requestID++
		response := callAppWS(t, conn, method, params, requestID)
		if response.Error != nil {
			t.Fatalf("%s refused: %+v", method, response.Error)
		}
		if target != nil {
			if err := json.Unmarshal(response.Result, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	call("workspaces.create", map[string]any{
		"id": workspaceID, "name": "Diagnostic promotion", "colour": nil, "position": 0,
		"firstTab":  map[string]any{"id": tabID, "name": nil, "colour": nil, "position": 0, "pinned": false, "layout": "row"},
		"firstPane": map[string]any{"id": paneID, "cwd": work, "kind": "local", "endpoint": nil, "sizeShare": 1},
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ordinary, err := app.Transport.OpenSession(ctx, transport.OpenSpec{PaneID: paneID, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	call("sandbox.profile.update", map[string]any{"expectedRevision": 0, "enabled": true, "roots": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}}}, nil)
	launch := func(source session.Session, head string) (transport.SandboxPreviewResult, session.Session) {
		t.Helper()
		identity := source.Identity()
		var preview transport.SandboxPreviewResult
		call("sandbox.preview", map[string]any{
			"paneId": paneID, "source": map[string]any{"sessionId": string(source.ID()), "instanceId": string(identity.InstanceID), "sessionEpoch": identity.Epoch},
			"expectedHeadId": head, "mode": "enforce", "delta": map[string]any{"readOnlyDirs": []string{}, "readWriteDirs": []string{}},
		}, &preview)
		var operation struct {
			State string `json:"state"`
			Open  *struct {
				SessionID string `json:"sessionId"`
			} `json:"open"`
		}
		call("sandbox.replace", map[string]any{"operationId": preview.OperationID, "confirmationId": preview.ConfirmationID}, &operation)
		if operation.State != "active" || operation.Open == nil {
			t.Fatalf("native launch not published: %+v", operation)
		}
		native, lookupErr := app.Session.Get(session.ID(operation.Open.SessionID))
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		return preview, native
	}
	preview, native := launch(ordinary.Session, "")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	probe := func(process session.Session, name, want string) {
		t.Helper()
		result := filepath.Join(work, name)
		command := fmt.Sprintf("if cat %s >/dev/null 2>&1; then printf 'allowed\\ncomplete\\n' >%s; else printf 'denied\\ncomplete\\n' >%s; fi\n", quote(observedPath), quote(result), quote(result))
		if _, writeErr := process.Write([]byte(command)); writeErr != nil {
			t.Fatal(writeErr)
		}
		if got := waitSandboxNativeResult(t, result); got != want+"\ncomplete\n" {
			t.Fatalf("%s: %q, want %s", name, got, want)
		}
	}
	probe(native, "before-promotion", "denied")
	var listed transport.SandboxAccessListResult
	var observed sandbox.DiagnosticRecord
	deadline := time.Now().Add(15 * time.Second)
	for {
		call("sandbox.access.list", map[string]any{"paneId": paneID, "launchId": preview.OperationID, "cursor": 0, "limit": 200}, &listed)
		for _, record := range listed.Inbox.Records {
			if record.PathKnown && record.Path == observedPath && record.Access == sandbox.DiagnosticRead {
				observed = record
				break
			}
		}
		if observed.ID != "" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("denied native access was not observed: observer=%s dropped=%d records=%+v", listed.Inbox.Observer, listed.Inbox.Dropped, listed.Inbox.Records)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if observed.Proposal == nil || observed.Proposal.Directory != outside || observed.Proposal.MissingTarget {
		t.Fatalf("diagnostic did not propose the exact existing parent: %+v", observed)
	}
	before, err := app.launches.Head(ctx, paneID)
	if err != nil {
		t.Fatal(err)
	}
	resolveParams := map[string]any{
		"paneId": paneID, "launchId": preview.OperationID, "eventId": observed.ID, "eventRevision": observed.Revision,
		"decision": "allow-ro", "expectedStandardRevision": listed.StandardRevision, "expectedWorkspaceRevision": listed.WorkspaceRevision,
	}
	var resolved transport.SandboxAccessResolveResult
	call("sandbox.access.resolve", resolveParams, &resolved)
	if resolved.Record.State != sandbox.DiagnosticFuturePolicy || resolved.Profile.Workspace == nil || resolved.Profile.Workspace.WorkspaceID != workspaceID || resolved.Profile.Workspace.Revision != listed.WorkspaceRevision+1 || resolved.Profile.Standard.Revision != listed.StandardRevision {
		t.Fatalf("promotion did not commit only the bound named future profile: %+v", resolved)
	}
	var duplicate transport.SandboxAccessResolveResult
	call("sandbox.access.resolve", resolveParams, &duplicate)
	if duplicate.Record.FutureRevision != resolved.Record.FutureRevision || duplicate.Profile.Workspace == nil || duplicate.Profile.Workspace.Revision != resolved.Profile.Workspace.Revision {
		t.Fatalf("duplicate confirmation wrote a second profile revision: %+v", duplicate)
	}
	after, err := app.launches.Head(ctx, paneID)
	if err != nil || after.ID != before.ID || after.GrantID == nil || before.GrantID == nil || *after.GrantID != *before.GrantID || after.PolicyDigest != before.PolicyDigest {
		t.Fatalf("future promotion changed the current immutable grant: before=%+v after=%+v err=%v", before, after, err)
	}
	probe(native, "after-promotion-same-process", "denied")
	_, next := launch(native, before.ID)
	probe(next, "after-explicit-relaunch", "allowed")
	requestID++
	stale := callAppWS(t, conn, "sandbox.access.resolve", resolveParams, requestID)
	if stale.Error == nil {
		t.Fatal("retired launch diagnostic was accepted against the replacement")
	}
	t.Log("NATIVE_DIAGNOSTIC_FUTURE_POLICY_RELAUNCH_PROOF_COMPLETE")
}
