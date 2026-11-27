package assistant

// Files on a pane (stage 10, ADR-anntnxdj §5). Which machine a call reaches is
// the BINDING's fact, not the pane's kind: RunContext.Files is the route the
// transport mints for the run's session, and the file tools hold nothing else
// (agenttools.narrow.go). Two things are asserted here, and both are the
// assistant's own half of the stage:
//
//   - a run WITH a binding reads, edits and creates through it, so the route is
//     wired to the tools rather than merely present;
//   - a binding that cannot reach its host answers with the connection's own
//     helper reason (mode-raw, helper-lost, …), as OUR refusal in the call's
//     slot rather than as a failure that ends the turn — and the tools that do
//     not need that host keep working in the same run.
//
// That an SSH pane's binding reaches THAT server, and never this machine's
// disk, is asserted where the binding is minted and used
// (internal/transport's acceptance test) and end to end in
// e2e/assistant-ssh-files.spec.ts. Asserting it here would prove only that the
// tools use whatever source a test handed them.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
)

// localRunContext is the run context of a caller whose run carries a binding to
// this machine's files — what the file tools' capability tests below and
// elsewhere assume.
func localRunContext(t *testing.T) agenttools.RunContext {
	t.Helper()
	return agenttools.RunContext{Files: newLocalFileSource(t)}
}

// toolsOnWithFiles binds the harness's tool entry for one run with the Files
// source supplied, so a test can hand it a spy and assert what it reached.
func toolsOnWithFiles(t *testing.T, grant content.Grant, files filesystem.HandleSource, ledger AttemptLedger, watch *watchedCalls) *RunTools {
	t.Helper()
	owner, _, err := NewClientAndRegistry(nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindRunTools(context.Background(), owner, AskParams{Grant: &grant, FreshGrant: func() *content.Grant { return &grant }, Files: files, AttemptLedger: ledger, KnownMaterial: &fakeKnownMaterial{}, RunID: "run-1", SessionID: "session-1", Attempt: 1, TurnEntryID: "turn-entry"}, func(call ToolCall) error { watch.seen = append(watch.seen, call); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func invokePaneTool(bound *RunTools, method, callID, args string) (string, error) {
	invocation, err := bound.Admit(context.Background())
	if err != nil {
		return "", err
	}
	invocation.Context = context.Background()
	invocation.Method, invocation.RawParams = method, []byte(args)
	return bound.Dispatch(invocation)
}

// TestFileToolsWorkWithAPaneBinding is the paired half of the no-binding
// refusal (agenttools.narrow_pane_test.go): with the run's binding present the
// three tools do their work through it, so "no capability without a binding"
// cannot be satisfied by a capability that does nothing.
func TestFileToolsWorkWithAPaneBinding(t *testing.T) {
	dir := t.TempDir()
	readPath := filepath.Join(dir, "notes.txt")
	writeFile(t, readPath, "hello\n")
	created := filepath.Join(dir, "created.txt")

	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	watch := &watchedCalls{}
	files := &spyFileSource{inner: newLocalFileSource(t)}
	k := toolsOnWithFiles(t, grant, files, &fakeLedger{}, watch)

	out, err := invokePaneTool(k, "files.read", "call-1", fmt.Sprintf(`{"path":%q}`, readPath))
	if err != nil {
		t.Fatalf("files.read: %v", err)
	}
	var readResult struct {
		Revision string `json:"revision"`
	}
	if decodeErr := json.Unmarshal([]byte(out), &readResult); decodeErr != nil {
		t.Fatalf("files.read result does not parse: %v", decodeErr)
	}
	if readResult.Revision == "" {
		t.Fatalf("files.read result = %q, want a revision", out)
	}

	editParams, err := json.Marshal(map[string]string{
		"path": readPath, "revision": readResult.Revision, "patch": "PUT 1.=1:\n+hello there",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, invokeErr := invokePaneTool(k, "files.edit", "call-2", string(editParams)); invokeErr != nil {
		t.Fatalf("files.edit: %v", invokeErr)
	}
	if got := readFileString(t, readPath); got != "hello there\n" {
		t.Fatalf("%s = %q, want the patch applied", readPath, got)
	}

	createParams, err := json.Marshal(map[string]string{"path": created, "content": "made\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := invokePaneTool(k, "files.create", "call-3", string(createParams)); createErr != nil {
		t.Fatalf("files.create: %v", createErr)
	}
	if got := readFileString(t, created); got != "made\n" {
		t.Fatalf("%s = %q, want the file created", created, got)
	}
	if len(watch.seen) != 3 {
		t.Fatalf("announced tool calls = %v, want the three calls that ran", watch.seen)
	}
	// One binding per call: the route is taken for each operation and closed
	// again, so a call cannot inherit the previous one's handle.
	if got := files.acquisitions(); got < 3 {
		t.Fatalf("file binding acquisitions = %d, want at least one per call", got)
	}
}

// TestFilesOnAPaneWhoseHelperIsUnavailableRefuseWithItsReason is the stage's
// third criterion at the seam a model reads: a binding whose host cannot be
// reached answers `files.*` with the connection's published reason, as our
// sentence in the refused call's slot — and the run goes on, so `session.read`
// in the same run is unaffected.
func TestFilesOnAPaneWhoseHelperIsUnavailableRefuseWithItsReason(t *testing.T) {
	grant := autonomousMatrix().AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-a"},
		{Kind: content.ResourcePath, ID: "/srv/proj"},
	})
	ledger := &fakeLedger{}
	mw := middlewareForSeams(t, grant, ledger, nil, &recordingRequester{body: liveFrameBody("load 1.00", "idle")},
		&fakeKnownMaterial{}, "", unavailableFileSource{err: &filesystem.ErrUnavailable{
			Reason: "mode-raw", Action: "host-settings",
		}})

	out, err := wrappedEndpoint(mw, "files.read", "call-1", `{"path":"/srv/proj/deploy.conf"}`)
	if err != nil {
		t.Fatalf("files.read on an unreachable host returned an error: %v — a refusal is an answer, not a fault", err)
	}
	if !strings.HasPrefix(out, "REFUSED") || !strings.Contains(out, "files.read") {
		t.Fatalf("refusal = %q, want nocx's own refusal naming the tool", out)
	}
	// The reason and the action are the connection's own, the same tokens the
	// Files panel names for that host: a model told only "unavailable" would
	// propose the same call again.
	for _, want := range []string{"mode-raw", "host-settings", "helper"} {
		if !strings.Contains(out, want) {
			t.Fatalf("refusal = %q, want it to name %q", out, want)
		}
	}
	// The attempt was opened and closed: the refusal is recorded, not lost.
	if ledger.started() != 1 {
		t.Fatalf("ledger opened %d executions, want the refused call's own attempt", ledger.started())
	}
	finished := 0
	for _, call := range ledger.calls() {
		if strings.HasPrefix(call, "finish:") {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("ledger finished %d executions, want the refused attempt closed", finished)
	}

	// And the tools that do not need the host still work: the refusal is about
	// this route, never about the run.
	read, readErr := wrappedEndpoint(mw, "session.read", "call-2", `{}`)
	if readErr != nil {
		t.Fatalf("session.read in the same run: %v", readErr)
	}
	if !strings.Contains(read, "load 1.00") {
		t.Fatalf("session.read = %q, want the pane's frame", read)
	}
}

// unavailableFileSource is a run whose binding cannot reach its host at all —
// the state helperfs and the provider factory both answer with.
type unavailableFileSource struct{ err error }

func (s unavailableFileSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return nil, nil, s.err
}

// readFileString reads a file this test created under t.TempDir.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	// #nosec G304 -- the path is built under t.TempDir by the test.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
