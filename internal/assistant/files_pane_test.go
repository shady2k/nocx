package assistant

// A file tool on a pane that is not this machine is REFUSED, and this
// machine's disk is not touched (ADR-anntnxdj §5, decision 3): until stage 8
// the file tools are built on local.New(), so `files.*` refuse on a non-local
// pane, say that the remote file tools are coming, and stay declared — the
// tool list is the cache prefix and it does not move when they arrive.
//
// The refusal is a REFUSAL AND NOT A FAULT: the call's own slot carries our
// sentence, the run continues, and the person is never handed a failure about
// a constructor. That is why the answer comes from a chain point — the same
// shape every other refusal has — while the capability constructor refuses
// too, so no chain can reach this machine's disk through a forgotten link.

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
)

// localPaneEnvironment is where a pane on THIS machine is, as the transport
// derives it (environmentForSession): the kind and the id that rule makes.
func localPaneEnvironment() content.Environment {
	return content.Environment{ID: content.EnvironmentIDFor(content.EnvLocal, ""), Kind: content.EnvLocal}
}

// remotePaneEnvironment is the same derivation for a pane on another machine.
func remotePaneEnvironment() content.Environment {
	const endpoint = "deploy@build-01:22"
	host := endpoint
	return content.Environment{
		ID:       content.EnvironmentIDFor(content.EnvSSH, endpoint),
		Kind:     content.EnvSSH,
		Endpoint: &host,
	}
}

// localRunContext is the run context of a caller whose pane is this machine —
// what the file tools' capability tests below and elsewhere assume.
func localRunContext() agenttools.RunContext {
	return agenttools.RunContext{Environment: localPaneEnvironment()}
}

// kernelOn builds the effect kernel over the real registry and dispatcher for
// a run whose pane is `pane`, with a ledger and the announcement seam the
// tests watch.
func kernelOn(t *testing.T, grant content.Grant, pane content.Environment, ledger AttemptLedger, watch *watchedCalls) *effectKernel {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, ledger, nil, &fakeKnownMaterial{}, "run-1", "session-1", 1, "turn-entry", nil, Attachments{}, func(call ToolCall) error {
		watch.seen = append(watch.seen, call)
		return nil
	}, toolSeams{paneEnvironment: pane})
	if err != nil {
		t.Fatalf("newEffectKernel: %v", err)
	}
	return k
}

// TestFileToolsRefuseAPaneThatIsNotThisMachine is the criterion: on an SSH
// pane each of the three refuses, and nothing on this machine is read or
// changed.
func TestFileToolsRefuseAPaneThatIsNotThisMachine(t *testing.T) {
	dir := t.TempDir()
	readPath := filepath.Join(dir, "notes.txt")
	const body = "this machine's copy, which the remote pane must never see\n"
	writeFile(t, readPath, body)
	editPath := filepath.Join(dir, "edit-me.txt")
	writeFile(t, editPath, "before\n")
	created := filepath.Join(dir, "created.txt")

	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	ledger := &fakeLedger{}
	watch := &watchedCalls{}
	k := kernelOn(t, grant, remotePaneEnvironment(), ledger, watch)

	calls := []struct{ method, params string }{
		{"files.read", fmt.Sprintf(`{"path":%q}`, readPath)},
		{"files.edit", fmt.Sprintf(`{"path":%q,"revision":"r1","patch":"PUT 1.=1:\n+after"}`, editPath)},
		{"files.create", fmt.Sprintf(`{"path":%q,"content":"created"}`, created)},
	}
	for _, call := range calls {
		out, err := k.Invoke(context.Background(), call.method, "call-1", call.params)
		if err != nil {
			t.Fatalf("%s on an SSH pane: err = %v, want a refusal the model reads as an answer", call.method, err)
		}
		if !strings.Contains(out, "REFUSED") || !strings.Contains(out, call.method) {
			t.Fatalf("%s on an SSH pane: result = %q, want the refusal naming the tool", call.method, out)
		}
		if !strings.Contains(out, "remote file tools are coming") {
			t.Fatalf("%s on an SSH pane: result = %q, want it to say the remote file tools are coming", call.method, out)
		}
		if strings.Contains(out, strings.TrimSpace(body)) {
			t.Fatalf("%s on an SSH pane: the answer carries this machine's file contents, so the file was read", call.method)
		}
	}
	// The executor never ran: nothing announced a call, and nothing opened an
	// attempt in the ledger.
	if len(watch.seen) != 0 {
		t.Fatalf("announced tool calls = %v, want none — the tools never ran", watch.seen)
	}
	if got := ledger.calls(); len(got) != 0 {
		t.Fatalf("ledger writes = %v, want none — no attempt was opened", got)
	}
	// And the disk is as it was.
	if got := readFileString(t, readPath); got != body {
		t.Fatalf("%s = %q, want it unchanged", readPath, got)
	}
	if got := readFileString(t, editPath); got != "before\n" {
		t.Fatalf("%s = %q, want it unchanged", editPath, got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("%s exists after a refused files.create (err = %v)", created, err)
	}
}

// And the paired half: on a pane that IS this machine the three work exactly
// as they did before, so "refuses a remote pane" cannot be satisfied by a
// capability that refuses everywhere.
func TestFileToolsWorkOnAPaneThatIsThisMachine(t *testing.T) {
	dir := t.TempDir()
	readPath := filepath.Join(dir, "notes.txt")
	writeFile(t, readPath, "hello\n")
	created := filepath.Join(dir, "created.txt")

	grant := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
	watch := &watchedCalls{}
	k := kernelOn(t, grant, localPaneEnvironment(), &fakeLedger{}, watch)

	out, err := k.Invoke(context.Background(), "files.read", "call-1", fmt.Sprintf(`{"path":%q}`, readPath))
	if err != nil {
		t.Fatalf("files.read on a local pane: %v", err)
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
	if _, invokeErr := k.Invoke(context.Background(), "files.edit", "call-2", string(editParams)); invokeErr != nil {
		t.Fatalf("files.edit on a local pane: %v", invokeErr)
	}
	if got := readFileString(t, readPath); got != "hello there\n" {
		t.Fatalf("%s = %q, want the patch applied", readPath, got)
	}

	createParams, err := json.Marshal(map[string]string{"path": created, "content": "made\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := k.Invoke(context.Background(), "files.create", "call-3", string(createParams)); createErr != nil {
		t.Fatalf("files.create on a local pane: %v", createErr)
	}
	if got := readFileString(t, created); got != "made\n" {
		t.Fatalf("%s = %q, want the file created", created, got)
	}
	if len(watch.seen) != 3 {
		t.Fatalf("announced tool calls = %v, want the three calls that ran", watch.seen)
	}
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
