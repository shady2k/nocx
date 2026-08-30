package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMiddlewareApprovalCapturesEveryPathVersion(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "before")

	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, &fakeLedger{}, approvals)
	args := fmt.Sprintf(`{"path":%q}`, path)
	proposal := approveWiringProposal(t, mw, approvals, "files.read", args)

	versions, ok := approvals.ApprovedFileVersions(proposal)
	if !ok {
		t.Fatal("approved proposal has no stored file versions")
	}
	if len(versions) != 1 || versions[0].Path != path {
		t.Fatalf("approved file versions = %+v, want exactly %s", versions, path)
	}
	if err := VerifyFileVersion(versions[0]); err != nil {
		t.Fatalf("stored file version is not current: %v", err)
	}
}

func TestMiddlewareApprovalRefusesChangedFileBeforeExecution(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "changed.txt")
	writeFile(t, path, "before")

	approvals := NewApprovalStore()
	ledger := &fakeLedger{}
	mw := middlewareFor(t, grant, ledger, approvals)
	args := fmt.Sprintf(`{"path":%q}`, path)
	proposal := approveWiringProposal(t, mw, approvals, "files.read", args)
	writeFile(t, path, "after")

	out, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("changed-file refusal returned error: %v", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, path) {
		t.Fatalf("refusal = %q, want refusal naming %s", out, path)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d executions, want only the proposal attempt", got)
	}
	if err := approvals.VerifyApprovedFileVersions(proposal); err == nil {
		t.Fatal("changed approved file still verifies")
	}
}

func TestMiddlewareApprovalRefusesRenamedFileBeforeExecution(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "renamed.txt")
	writeFile(t, path, "before")

	approvals := NewApprovalStore()
	ledger := &fakeLedger{}
	mw := middlewareFor(t, grant, ledger, approvals)
	args := fmt.Sprintf(`{"path":%q}`, path)
	approveWiringProposal(t, mw, approvals, "files.read", args)

	replacement := filepath.Join(dir, "replacement.txt")
	writeFile(t, replacement, "after")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("replace file: %v", err)
	}

	out, err := wrappedEndpoint(mw, "files.read", "call_1", args)
	if err != nil {
		t.Fatalf("renamed-file refusal returned error: %v", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, path) {
		t.Fatalf("refusal = %q, want refusal naming %s", out, path)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d executions, want only the proposal attempt", got)
	}
}

func TestFilesCreateApprovalAllowsMissingPath(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "new.txt")
	args := fmt.Sprintf(`{"path":%q,"content":"created"}`, path)
	approvals := NewApprovalStore()
	mw := middlewareFor(t, grant, &fakeLedger{}, approvals)

	proposal := approveWiringProposal(t, mw, approvals, "files.create", args)
	versions, ok := approvals.ApprovedFileVersions(proposal)
	if !ok {
		t.Fatal("approved create proposal is missing")
	}
	if len(versions) != 0 {
		t.Fatalf("create proposal versions = %+v, want no versions for a missing path", versions)
	}
	out, err := wrappedEndpoint(mw, "files.create", "call_1", args)
	if err != nil {
		t.Fatalf("approved create returned error: %v", err)
	}
	if !strings.Contains(out, `"status":"created"`) {
		t.Fatalf("create result = %q, want created status", out)
	}
	// #nosec G304 -- the path is this test's own temporary file.
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "created" {
		t.Fatalf("created file = %q, read error = %v", data, readErr)
	}
}

func approveWiringProposal(t *testing.T, mw *policyMiddleware, approvals *ApprovalStore, tool, args string) Approval {
	t.Helper()
	_, err := wrappedEndpoint(mw, tool, "call_1", args)
	if err == nil {
		t.Fatalf("%s ran instead of requesting approval", tool)
	}
	proposal := Approval{
		RunID: "run-1", Attempt: 1, Tool: tool, CallID: "call_1",
		ArgHash: canonicalArgHash(args),
	}
	if !approvals.Approve(proposal) {
		t.Fatalf("approval proposal was not pending: %v", err)
	}
	return proposal
}
