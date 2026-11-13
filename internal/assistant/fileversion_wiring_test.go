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
	proposal, _, err := approveWiringProposal(t, mw, approvals, "files.read", args)
	if err != nil {
		t.Fatalf("the approved read: %v", err)
	}

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
	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.read", args, func() {
		writeFile(t, path, "after")
	})
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
	_, out, err := approveWiringProposal(t, mw, approvals, "files.read", args, func() {
		replacement := filepath.Join(dir, "replacement.txt")
		writeFile(t, replacement, "after")
		if renameErr := os.Rename(replacement, path); renameErr != nil {
			t.Fatalf("replace file: %v", renameErr)
		}
	})
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

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.create", args)
	if err != nil {
		t.Fatalf("approved create returned error: %v", err)
	}
	versions, ok := approvals.ApprovedFileVersions(proposal)
	if !ok {
		t.Fatal("approved create proposal is missing")
	}
	if len(versions) != 0 {
		t.Fatalf("create proposal versions = %+v, want no versions for a missing path", versions)
	}
	if !strings.Contains(out, `"status":"created"`) {
		t.Fatalf("create result = %q, want created status", out)
	}
}

// approveWiringProposal puts one call to a person who says yes, optionally
// doing something while the question is open (between), and returns the
// proposal that was approved together with the call's result.
func approveWiringProposal(t *testing.T, mw *policyMiddleware, approvals *ApprovalStore, tool, args string, between ...func()) (Approval, string, error) {
	t.Helper()
	q := &recordingQuestioner{store: approvals, answer: Approved{}}
	if len(between) > 0 {
		q.before = between[0]
	}
	mw.kernel.questioner = q
	out, err := wrappedEndpoint(mw, tool, "call_1", args)
	if len(q.asked) != 1 {
		t.Fatalf("%s ran instead of requesting approval", tool)
	}
	return Approval{
		RunID: "run-1", Attempt: 1, Tool: tool, CallID: "call_1",
		ArgHash: canonicalArgHash(args),
	}, out, err
}
