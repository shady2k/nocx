package assistant

// The mutating half of nocx-4yjwk.3's claim (nocx-4yjwk.5). `files.read` was
// brought under the kernel's fault-or-answer seam one commit ago; `files.edit`
// and `files.create` were not, and they are the reason the inconsistency
// existed at all: both swallowed EVERY editor error into a tool result shaped
// {"status":"refused","reason":err.Error()}, so the capability's own sentence —
//
//	filesystem: path outside the grant's scope: /home/someone/private/notes.md
//
// — reached the model verbatim, carrying an absolute path outside the grant and
// the shape of the fence with it. refusalResult's contract says the refusal text
// is OURS, written per reason, never the framework's stringification, and that
// it says nothing the policy keeps from the person.
//
// These tests assert the interval with BOTH ends: from the moment the capability
// refuses (nothing was written, and nothing about the target leaves) until the
// model holds our sentence and the person holds a call announced and an attempt
// closed as a failure. A refusal the model is told about and the person is not
// is the soft degrade AGENTS.md forbids, in the other direction.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/hashline"
)

// mutationFence mints the state every test here is about: a run whose write
// row is fenced to one directory, holding a symlink whose canonical identity
// is outside it. The lexical predicate the policy evaluates sees a path INSIDE
// the fence — so the policy permits, the call runs, and only the narrowed
// capability, which compares provider-canonical identities, can refuse.
//
// It returns the fence, the symlink a model would name, and the body beyond it
// that no call may change.
func mutationFence(t *testing.T) (grant content.Grant, fence, link, beyond, target string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temp root: %v", err)
	}
	fence = filepath.Join(root, "fence")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{fence, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	beyond = "the bytes past the fence\n"
	target = filepath.Join(outside, "fenced.txt")
	writeFile(t, target, beyond)
	link = filepath.Join(fence, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
	grant = autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: fence}})

	// The premise, asserted rather than assumed: the lexical predicate does
	// NOT refuse this resource. If it ever did, the gate would refuse before
	// the capability was reached and these tests would be about a different
	// check entirely — one that never had the leak.
	v := grant.Policy.EvaluateResources(content.EffectMutateReversible, content.DecisionPermit,
		[]content.GrantScope{{Kind: content.ResourcePath, ID: link}}, grant.Policy.RunFence())
	if v.Decision == content.DecisionRefuse {
		t.Fatalf("the lexical predicate already refuses %s (%+v) — the capability is no longer the check under test", link, v)
	}
	return grant, fence, link, beyond, target
}

// watchedCalls is the person's half: the seam a visible tool call leaves
// through (nocx-shxv0). A refusal that never reaches it is one the person
// cannot see happened.
type watchedCalls struct {
	seen []ToolCall
}

// middlewareWatchingCalls is middlewareFor with the onCall seam wired, which
// the shared helper passes nil for. It is here rather than beside that helper
// because only these tests assert on it.
func middlewareWatchingCalls(t *testing.T, grant content.Grant, ledger AttemptLedger, approvals *ApprovalStore, watch *watchedCalls) *policyMiddleware {
	t.Helper()
	reg := realRegistry(t)
	mw, err := newPolicyMiddleware(nil, grant, reg, ledger, approvals, &fakeKnownMaterial{}, "run-1", "", 1, "", nil, Attachments{}, nil,
		func(c ToolCall) error {
			watch.seen = append(watch.seen, c)
			return nil
		})
	if err != nil {
		t.Fatalf("newPolicyMiddleware: %v", err)
	}
	return mw
}

// assertOurRefusal is criteria 1–3 in one place, applied to the string the
// MODEL actually receives: our sentence is present, and every word the
// capability would have leaked is absent — the provider's prefix, its message,
// the absolute paths, the fence itself, and the effect the policy row names.
func assertOurRefusal(t *testing.T, tool, out, fence, link, target, beyond string) {
	t.Helper()
	if want := refusalResult(tool, RefusedOutOfScope, ""); out != want {
		t.Fatalf("%s answered\n  %q\nwant our out-of-scope refusal\n  %q", tool, out, want)
	}
	for _, leak := range []string{
		"filesystem:",            // the capability's own prefix
		"path outside the grant", // its sentence
		link, target, fence,      // absolute paths, and the fence's shape
		string(content.EffectMutateReversible), // the effect lattice
		beyond,                                 // the bytes the fence protects
	} {
		if leak == "" {
			continue
		}
		if strings.Contains(out, leak) {
			t.Fatalf("the %s refusal carries %q — the model gets our sentence and nothing the policy keeps from the person: %q", tool, leak, out)
		}
	}
}

// assertThePersonSaw is criterion 5: the refusal is not invisible in the
// product. The call was announced before it ran, and the attempt it opened is
// CLOSED with a failure — an interval with both ends, not a call that quietly
// reports nothing.
func assertThePersonSaw(t *testing.T, tool string, watch *watchedCalls, ledger *fakeLedger) {
	t.Helper()
	if len(watch.seen) != 1 || watch.seen[0].Tool != tool {
		t.Fatalf("the person was shown %+v, want exactly the one %s call that met the fence", watch.seen, tool)
	}
	if got := ledger.started(); got != 1 {
		t.Fatalf("the ledger opened %d attempts (%v), want the one refused call", got, ledger.calls())
	}
	if got := strings.Count(strings.Join(ledger.calls(), " "), "finish:"); got != 1 {
		t.Fatalf("the ledger closed %d attempts (%v), want the attempt closed — an interval ends with a reason", got, ledger.calls())
	}
	if got := ledger.recordedCaptures(); len(got) != 0 {
		t.Fatalf("the refused call recorded %d result bodies (%+v), want none — nothing was written", len(got), got)
	}
}

// TEST — files.edit: the fence's refusal is OUR sentence, and the bytes beyond
// it are untouched.
func TestFilesEdit_AFenceRefusalAnswersInOurWordsNotTheFilesystems(t *testing.T) {
	grant, fence, link, beyond, target := mutationFence(t)
	snapshot, err := hashline.Read(target, testResultMaxBytes())
	if err != nil {
		t.Fatalf("hashline.Read(target): %v", err)
	}
	args := string(mustJSON(t, map[string]string{
		"path": link, "revision": snapshot.Revision, "patch": "PUT 1.=1:\n+changed",
	}))

	ledger := &fakeLedger{}
	watch := &watchedCalls{}
	mw := middlewareWatchingCalls(t, grant, ledger, NewApprovalStore(), watch)

	out, err := mw.kernel.Invoke(context.Background(), "files.edit", "call_1", args)
	if err != nil {
		var failed *ToolFailedError
		if errors.As(err, &failed) {
			t.Fatalf("the fenced edit ended the run with %v — a capability refusal is an answer, not a fault", err)
		}
		t.Fatalf("the fenced edit returned %v, want the out-of-scope refusal as a result", err)
	}
	assertOurRefusal(t, "files.edit", out, fence, link, target, beyond)
	assertThePersonSaw(t, "files.edit", watch, ledger)

	// The refusal is not permission: the fence held, both ends.
	// #nosec G304 -- target is created under t.TempDir.
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != beyond {
		t.Fatalf("the file beyond the fence = %q, want unchanged %q — the refusal was cosmetic", got, beyond)
	}
}

// TEST — files.create: same claim, on the path where the target does not exist
// yet and the PARENT is what the capability canonicalizes.
func TestFilesCreate_AFenceRefusalAnswersInOurWordsNotTheFilesystems(t *testing.T) {
	grant, fence, _, _, _ := mutationFence(t)
	root := filepath.Dir(fence)
	outside := filepath.Join(root, "outside")
	linkDir := filepath.Join(fence, "elsewhere")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatalf("symlink %s -> %s: %v", linkDir, outside, err)
	}
	newPath := filepath.Join(linkDir, "planted.txt")
	args := string(mustJSON(t, map[string]string{"path": newPath, "content": "planted\n"}))

	ledger := &fakeLedger{}
	watch := &watchedCalls{}
	mw := middlewareWatchingCalls(t, grant, ledger, NewApprovalStore(), watch)

	out, err := mw.kernel.Invoke(context.Background(), "files.create", "call_1", args)
	if err != nil {
		var failed *ToolFailedError
		if errors.As(err, &failed) {
			t.Fatalf("the fenced create ended the run with %v — a capability refusal is an answer, not a fault", err)
		}
		t.Fatalf("the fenced create returned %v, want the out-of-scope refusal as a result", err)
	}
	assertOurRefusal(t, "files.create", out, fence, newPath, linkDir, "")
	assertThePersonSaw(t, "files.create", watch, ledger)

	if _, statErr := os.Stat(filepath.Join(outside, "planted.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("stat beyond the fence = %v, want nonexistent — the refusal did not stop the write", statErr)
	}
}

// TEST — criterion 6: only the CAPABILITY refusal moves. An ordinary editor
// failure inside the fence — a stale revision, which hashline raises after the
// capability has already allowed the path — still answers as the tool result it
// always did, with the editor's own reason in it.
func TestFilesEdit_AnOrdinaryEditorFailureStillAnswersAsAToolResult(t *testing.T) {
	grant, fence, _, _, _ := mutationFence(t)
	inside := filepath.Join(fence, "note.txt")
	writeFile(t, inside, "before\n")
	args := string(mustJSON(t, map[string]string{
		"path": inside, "revision": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"patch": "PUT 1.=1:\n+after",
	}))

	ledger := &fakeLedger{}
	watch := &watchedCalls{}
	mw := middlewareWatchingCalls(t, grant, ledger, NewApprovalStore(), watch)

	out, err := mw.kernel.Invoke(context.Background(), "files.edit", "call_1", args)
	if err != nil {
		t.Fatalf("a stale revision returned %v, want the editor's refusal as this call's result", err)
	}
	if out == refusalResult("files.edit", RefusedOutOfScope, "") {
		t.Fatalf("a stale revision answered the out-of-scope refusal — the capability's carve-out swallowed an ordinary failure: %q", out)
	}
	var result struct {
		Path   string `json:"path"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if decodeErr := json.Unmarshal([]byte(out), &result); decodeErr != nil {
		t.Fatalf("a stale revision no longer answers with the tool's own result shape (%q): %v", out, decodeErr)
	}
	if result.Status != "refused" || result.Path != inside || result.Reason == "" {
		t.Fatalf("stale-revision result = %+v, want the unchanged refused shape with the editor's reason", result)
	}
	// #nosec G304 -- inside is created under t.TempDir.
	got, readErr := os.ReadFile(inside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "before\n" {
		t.Fatalf("file = %q, want unchanged — a stale revision must not apply", got)
	}
}

// TEST — the executor seam itself: the capability's refusal LEAVES the tool as
// an error the one predicate recognises, rather than being answered inside the
// tool. This is criterion 4 stated where it can be checked directly — a second
// call site of capabilityRefusal is allowed, a second predicate is not.
func TestExecuteFilesMutations_HandTheCapabilityRefusalToTheKernelSeam(t *testing.T) {
	grant, fence, link, _, _ := mutationFence(t)
	reg := realRegistry(t)
	root := filepath.Dir(fence)
	linkDir := filepath.Join(fence, "elsewhere")
	if err := os.Symlink(filepath.Join(root, "outside"), linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, tc := range []struct {
		tool string
		path string
		args map[string]string
	}{
		{"files.edit", link, map[string]string{"path": link, "revision": "sha256:x", "patch": "PUT 1.=1:\n+changed"}},
		{"files.create", filepath.Join(linkDir, "planted.txt"), map[string]string{"path": filepath.Join(linkDir, "planted.txt"), "content": "planted\n"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			decl, ok := reg.Lookup(tc.tool)
			if !ok {
				t.Fatalf("%s not registered", tc.tool)
			}
			refs, err := decl.ResolveResources(map[string]any{"path": tc.path}, agenttools.RunContext{})
			if err != nil {
				t.Fatalf("ResolveResources: %v", err)
			}
			capability, err := decl.Narrow(grant, refs, agenttools.RunContext{})
			if err != nil {
				t.Fatalf("Narrow: %v", err)
			}
			out, execErr := executors[tc.tool](toolTestContext(), capability, mustJSON(t, tc.args), toolSeams{})
			if execErr == nil {
				t.Fatalf("%s answered its own scope refusal with %q — the fault-or-answer decision belongs to the kernel seam, not the tool", tc.tool, out)
			}
			if !capabilityRefusal(execErr) {
				t.Fatalf("%s returned %v, which the kernel's one predicate does not recognise as a capability refusal", tc.tool, execErr)
			}
			if out != "" {
				t.Fatalf("%s returned both a result %q and an error — the refused call has no result", tc.tool, out)
			}
		})
	}
}

// realRegistry assembles the shipped declarations — the same registry the
// pipeline builds — so the executor test runs the real narrowing.
func realRegistry(t *testing.T) agenttools.Registry {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return reg
}
