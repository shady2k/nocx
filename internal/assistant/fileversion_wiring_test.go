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
	mw := middlewareForSeams(t, grant, ledger, approvals, nil, &fakeKnownMaterial{}, "",
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

// An approval whose evidence could not be captured refuses with the REASON, not
// with the fact that nothing was recorded: a file over the read ceiling is a
// fact the model can act on ("read a window of it instead"), while "file
// version binding is missing" names nothing and invites the same call again.
func TestMiddlewareApprovalNamesWhyTheEvidenceCouldNotBeCaptured(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "too-big.log")
	big := make([]byte, filesystem.MaxReadBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	approvals := NewApprovalStore()
	ledger := &fakeLedger{}
	mw := middlewareForSeams(t, grant, ledger, approvals, nil, &fakeKnownMaterial{}, "", newLocalFileSource(t))
	args := fmt.Sprintf(`{"path":%q}`, path)

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.read", args)
	if err != nil {
		t.Fatalf("the refusal returned an error: %v — a refused call is an answer", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, path) {
		t.Fatalf("refusal = %q, want nocx's refusal naming %s", out, path)
	}
	if strings.Contains(out, "binding is missing") {
		t.Fatalf("refusal = %q, want the cause the capture failed with rather than the bare missing binding", out)
	}
	for _, want := range []string{"exceeds", "operation limit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("refusal = %q, want it to name the read ceiling (%q)", out, want)
		}
	}
	if versions, ok := approvals.ApprovedFileVersions(proposal); !ok || len(versions) != 0 {
		t.Fatalf("approved versions = %v, %v; a failed capture binds nothing", versions, ok)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("ledger opened %d executions, want only the proposal attempt", got)
	}
}

// The cause a refusal carries is told in the path the CALL named, and never in
// the identity the pane resolved.
//
// A symlink inside the approved area pointing at a file over the read ceiling
// OUTSIDE it is the case that makes the difference: the provider's own sentence
// names the canonical target and the size it observed, and both would reach the
// model as the detail of `REFUSED` — a path it was never given, and how large a
// file it is not allowed to read. The lexical name is the one the person
// answered about, so it is the only one a refusal may repeat.
func TestMiddlewareApprovalDoesNotDiscloseAPathOutsideTheApprovedArea(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside-the-area.bin")
	big := make([]byte, filesystem.MaxReadBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(outsideFile, big, 0o600); err != nil {
		t.Fatalf("write the file outside the approved area: %v", err)
	}
	link := filepath.Join(dir, "inside-link.conf")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatalf("link the approved area at the file outside it: %v", err)
	}

	approvals := NewApprovalStore()
	mw := middlewareForSeams(t, grant, &fakeLedger{}, approvals, nil, &fakeKnownMaterial{}, "", newLocalFileSource(t))
	args := fmt.Sprintf(`{"path":%q}`, link)

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.read", args)
	if err != nil {
		t.Fatalf("the refusal returned an error: %v — a refused call is an answer", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, link) {
		t.Fatalf("refusal = %q, want a refusal naming the path the call carried (%s)", out, link)
	}
	if !strings.Contains(out, "exceeds") {
		t.Fatalf("refusal = %q, want it to say the file is over the read ceiling", out)
	}
	for _, leaked := range []string{filepath.Base(outsideFile), fmt.Sprint(len(big))} {
		if strings.Contains(out, leaked) {
			t.Fatalf("refusal = %q, want nothing about %q, which is outside the approved area", out, leaked)
		}
	}
	if versions, ok := approvals.ApprovedFileVersions(proposal); !ok || len(versions) != 0 {
		t.Fatalf("approved versions = %v, %v; a failed capture binds nothing", versions, ok)
	}
}

// files.create resolves the name it will occupy the way the editor does — the
// parent's canonical identity plus the base name — so an absolute but
// un-normalized path is not refused for a spelling the write itself resolves.
// The path's own canonicalization would answer ErrInvalidPath ("path is not
// clean"), which is not ErrNotFound, and the approval would have failed closed
// on a call that can succeed.
func TestFilesCreateApprovalResolvesTheNameAsTheEditorDoes(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := dir + "/sub/../created.txt"
	if cleaned := filepath.Join(dir, "created.txt"); cleaned == path {
		t.Fatalf("fixture precondition: %q must not be the clean spelling", path)
	}

	approvals := NewApprovalStore()
	mw := middlewareForSeams(t, grant, &fakeLedger{}, approvals, nil, &fakeKnownMaterial{}, "", newLocalFileSource(t))
	args := fmt.Sprintf(`{"path":%q,"content":"created"}`, path)

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.create", args)
	if err != nil {
		t.Fatalf("approved create returned error: %v", err)
	}
	if !strings.Contains(out, `"status":"created"`) {
		t.Fatalf("create result = %q, want created", out)
	}
	if versions, ok := approvals.ApprovedFileVersions(proposal); !ok || len(versions) != 0 {
		t.Fatalf("approved versions = %v, %v; a name that does not exist yet binds no version", versions, ok)
	}
	if got := readFileString(t, filepath.Join(dir, "created.txt")); got != "created" {
		t.Fatalf("created file = %q, want the bytes the call carried", got)
	}
}

// The evidence an approval binds on a pane that is NOT this machine comes from
// that pane's binding and from nowhere else.
//
// The fixture deliberately puts a REAL file with the SAME path and DIFFERENT
// bytes on this machine, because the defect this stage fixed was exactly the
// case a missing local file cannot catch: the evidence used to be an os.Stat
// and an os.ReadFile of the named path, and on an SSH pane that described this
// machine's same-named file. An implementation that read the local file when
// one exists — and fell back to the binding otherwise — passes every assertion
// that only asks for "the pane was read".
func TestMiddlewareApprovalOnANonLocalPaneReadsOnlyThePanesBinding(t *testing.T) {
	grant, dir := testDirGrant(t, askEveryTimeMatrix())
	path := filepath.Join(dir, "only-on-the-host.conf")
	const localCopy = "this machine's copy, which the question must not describe\n"
	writeFile(t, path, localCopy)
	before := localFileState(t, path)
	remoteVersion := filesystem.Version{
		Size:    44,
		MtimeNs: 1_700_000_000_000_000_000,
		SHA256:  strings.Repeat("ab", 32),
	}
	source, provider := newForeignFileSource(t, remoteVersion)

	approvals := NewApprovalStore()
	mw := middlewareForSeams(t, grant, &fakeLedger{}, approvals, nil, &fakeKnownMaterial{}, "", source)
	args := fmt.Sprintf(`{"path":%q}`, path)

	proposal, out, err := approveWiringProposal(t, mw, approvals, "files.read", args)
	if err != nil {
		t.Fatalf("the approved read: %v", err)
	}
	versions, ok := approvals.ApprovedFileVersions(proposal)
	if !ok || len(versions) != 1 {
		t.Fatalf("approved file versions = %v, %v; want the one path", versions, ok)
	}
	if versions[0].Version != remoteVersion {
		t.Fatalf("captured version = %+v, want the pane's %+v", versions[0].Version, remoteVersion)
	}
	// Three reads, all of them the pane's: the capture before the question,
	// the re-read after the answer, and the tool's own read of the file. The
	// local copy is never read, which the unchanged bytes and mtime below say.
	if got := provider.state(); got.reads != 3 {
		t.Fatalf("pane reads for the whole call = %d, want capture, re-read and the tool's read", got.reads)
	}
	// The call then RUNS — the pane is reached through the helper, so there is
	// nothing left to refuse — and what it answers is the pane's bytes.
	if !strings.Contains(out, "remote bytes") || strings.Contains(out, "this machine's copy") {
		t.Fatalf("result = %q, want the pane's bytes and never this machine's", out)
	}
	// The same binding answers a later re-read, and it still sees the version
	// the person was shown.
	if err := approvals.VerifyApprovedFileVersions(t.Context(), source, proposal); err != nil {
		t.Fatalf("verification through the pane's binding: %v", err)
	}
	if got := provider.state(); got.reads != 4 {
		t.Fatalf("pane reads after a further verification = %d, want one more", got.reads)
	}
	// And the local same-named file was not touched: same bytes, same mtime.
	if got := localFileState(t, path); got != before {
		t.Fatalf("local %s = %+v, want it byte for byte as it was %+v", path, got, before)
	}
	if got := readFileString(t, path); got != localCopy {
		t.Fatalf("local %s = %q, want this machine's copy untouched", path, got)
	}
}

// fileState is what "this file was not touched" means: the bytes' length,
// modification time and content hash.
type fileState struct {
	size    int64
	mtimeNs int64
	body    string
}

func localFileState(t *testing.T, path string) fileState {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fileState{size: info.Size(), mtimeNs: info.ModTime().UnixNano(), body: readFileString(t, path)}
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
