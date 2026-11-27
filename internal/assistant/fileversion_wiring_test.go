package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
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
	if err := VerifyFileVersion(t.Context(), newLocalFileSource(t), versions[0]); err != nil {
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
	if err := approvals.VerifyApprovedFileVersions(t.Context(), newLocalFileSource(t), proposal); err == nil {
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

// An approval whose evidence could not be captured authorises nothing: the
// call is refused before a capability exists, and the person's yes covers no
// bytes at all.
func TestMiddlewareApprovalFailsClosedWhenEvidenceCannotBeCaptured(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "unreadable.txt")
	writeFile(t, path, "before")

	approvals := NewApprovalStore()
	ledger := &fakeLedger{}
	mw := middlewareForSeams(t, grant, ledger, approvals, nil, &fakeKnownMaterial{}, "", localPaneEnvironment(),
		failingFileSource{err: fmt.Errorf("binding refused for %s", path)})
	args := fmt.Sprintf(`{"path":%q}`, path)

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.read", args)
	if err != nil {
		t.Fatalf("evidence capture failure returned an error: %v", err)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Fatalf("refusal = %q, want a refusal — an approval with no captured evidence authorises nothing", out)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d executions, want only the proposal attempt — the call must not run", got)
	}
	if versions, ok := approvals.ApprovedFileVersions(proposal); !ok || len(versions) != 0 {
		t.Fatalf("approved versions = %v, %v; a failed capture binds nothing", versions, ok)
	}
}

// The evidence an approval binds on a pane that is NOT this machine comes from
// that pane's binding and from nowhere else. The path does not exist on this
// machine at all, so a captured version is itself the proof: an evidence read
// that stat'ed or read the local path could only have failed.
func TestMiddlewareApprovalOnANonLocalPaneReadsOnlyThePanesBinding(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "only-on-the-host.conf")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fixture precondition: %s must not exist on this machine (err = %v)", path, err)
	}
	remoteVersion := filesystem.Version{
		Size:    44,
		MtimeNs: 1_700_000_000_000_000_000,
		SHA256:  strings.Repeat("ab", 32),
	}
	source, provider := newForeignFileSource(t, remoteVersion)

	approvals := NewApprovalStore()
	mw := middlewareForSeams(t, grant, &fakeLedger{}, approvals, nil, &fakeKnownMaterial{}, "", remotePaneEnvironment(), source)
	args := fmt.Sprintf(`{"path":%q}`, path)

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.read", args)
	if err != nil {
		t.Fatalf("the approved read: %v", err)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Fatalf("result = %q, want the remote-pane refusal from the stage's stub", out)
	}
	versions, ok := approvals.ApprovedFileVersions(proposal)
	if !ok || len(versions) != 1 {
		t.Fatalf("approved file versions = %v, %v; want the one path", versions, ok)
	}
	if versions[0].Version != remoteVersion {
		t.Fatalf("captured version = %+v, want the pane's %+v", versions[0].Version, remoteVersion)
	}
	if got := provider.state(); got.reads != 1 {
		t.Fatalf("pane reads during the question = %d, want exactly 1", got.reads)
	}
	// The same binding answers the re-read after the answer, and the re-read
	// still sees the version the person was shown.
	if err := approvals.VerifyApprovedFileVersions(t.Context(), source, proposal); err != nil {
		t.Fatalf("verification through the pane's binding: %v", err)
	}
	if got := provider.state(); got.reads != 2 {
		t.Fatalf("pane reads after verification = %d, want 2", got.reads)
	}
}

// approveWiringProposal puts one call to a person who says yes, optionally
// doing something while the question is open (between), and returns the
// proposal that was approved together with the call's result.
func approveWiringProposal(t *testing.T, mw *kernelFixture, approvals *ApprovalStore, tool, args string, between ...func()) (Approval, string, error) {
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
