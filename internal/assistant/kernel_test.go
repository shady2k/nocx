package assistant

// The carrier seam (nocx-d6gn4.12). Two tests, and between them they say the
// whole thing: the effect kernel can be reached by a caller that is not
// eino, and the kernel's own source knows nothing about eino.
//
// WHY THIS IS A DEFECT TODAY AND NOT PREPARATION FOR AN EXPERIMENT. AD-8's
// corollary is that variation is expressed by the interface, and that a new
// implementation must be addable "without editing a switch and without
// copying lines". The permit/ask/refuse decision, the attempt interval, the
// narrowed capability and the batch latch were reachable ONLY through
// adk.ChatModelAgentMiddleware.WrapInvokableToolCall, so any second way of
// proposing an effect had to import the framework or copy the kernel. Both
// are the violation; the second is the one that ends with two owners of what
// an effect IS.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
)

// kernelFor builds the effect kernel for one test grant + the real registry,
// with no framework object anywhere in the call.
func kernelFor(t *testing.T, grant content.Grant, ledger AttemptLedger) *effectKernel {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, ledger, nil, &fakeKnownMaterial{}, "run-1", "", 1, "turn-entry", nil, Attachments{}, nil, nil)
	if err != nil {
		t.Fatalf("newEffectKernel: %v", err)
	}
	return k
}

// A caller that is not the framework proposes an effect and gets the same
// pipeline: validation, policy, the attempt written before the call, the
// narrowed capability, the execution, the outcome recorded.
func TestEffectKernel_IsReachableWithoutTheFrameworkSeam(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	writeFile(t, filepath.Join(dir, "a.txt"), "in scope")
	led := &fakeLedger{}
	k := kernelFor(t, grant, led)

	out, err := k.Invoke(context.Background(), "files.read", "call-1",
		`{"path":"`+filepath.Join(dir, "a.txt")+`"}`)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "in scope") {
		t.Fatalf("result = %q, want the file's contents", out)
	}
	// The attempt interval is the kernel's, not the adapter's: it exists
	// because the kernel wrote it, with no framework in the call at all.
	if got := led.recordedCauses(); len(got) != 1 || got[0].turn != "turn-entry" {
		t.Fatalf("causes = %+v, want the one action entry joined to turn-entry", got)
	}
}

// And a refusal reaches that caller as a RESULT, not as an error — the
// promise the system prompt makes ("a refusal is an answer") belongs to the
// kernel, so every carrier keeps it without copying a line.
func TestEffectKernel_RefusalIsAnAnswerForAnyCaller(t *testing.T) {
	grant, _ := testDirGrant(t, autonomousMatrix())
	k := kernelFor(t, grant, &fakeLedger{})

	out, err := k.Invoke(context.Background(), "files.read", "call-1", `{"path":"/etc/passwd"}`)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(out, "files.read") {
		t.Fatalf("refusal = %q, want the tool named in the refusal the model reads", out)
	}
}

// The mechanical half, and the one that survives: NO file that declares a
// method on the kernel may import the framework. Stated over the receiver
// rather than over one file name, because the guard has to hold however the
// package is laid out later — a helper moved back into an eino-importing
// file is the same violation as writing adk into the kernel itself.
func TestEffectKernel_NoFileDeclaringItImportsTheFramework(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if !declaresKernelMethod(file) {
				continue
			}
			for _, imp := range file.Imports {
				if strings.Contains(imp.Path.Value, "cloudwego/eino") {
					t.Fatalf("%s declares a kernel method and imports %s — the kernel may not know the framework (AD-8)", name, imp.Path.Value)
				}
			}
		}
	}
}

// declaresKernelMethod reports whether the file declares any method whose
// receiver is *effectKernel.
func declaresKernelMethod(file *ast.File) bool {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); ok && id.Name == "effectKernel" {
			return true
		}
	}
	return false
}

// ── one evaluator, one typed cause (nocx-t6h2u) ──
//
// The kernel used to compute its own containment answer beside
// content.EffectPolicy's, and the two disagreed: the command path asked, the
// declared path refused with RefusedOutOfScope, and EffectRow's doc comment
// stated refusal for both. The kernel now asks the same evaluator, and
// RefusedOutOfScope is the sentence for ONE cause — the immutable fence.

// observeGrantFencedTo mints a grant whose observe row permits within
// selector, inside the run fence. Both are path scopes, so the fence is the
// immutable half and the selector is the half a person can widen.
func observeGrantFencedTo(t *testing.T, fence, selector string) content.Grant {
	t.Helper()
	policy := autonomousMatrix()
	policy.Observe.Scopes = []content.GrantScope{{Kind: content.ResourcePath, ID: selector}}
	return policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: fence}})
}

func TestDeclaredResourceOutsideARowScopeIsAQuestionAndOutsideTheFenceIsARefusal(t *testing.T) {
	root := t.TempDir()
	selector := filepath.Join(root, "src")
	kernel := &effectKernel{grant: observeGrantFencedTo(t, root, selector)}
	tool := agenttools.Tool{
		Declaration: agenttools.Declaration{Effect: []content.Effect{content.EffectObserve}},
		Effect:      content.EffectObserve,
	}
	parsed := content.Invocation{Parsed: true}

	// Inside the selector: nothing fell outside, so the row's own permit stands.
	inside := []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: filepath.Join(selector, "a.txt")}}
	if outcome, reason, _, _ := kernel.decideInvocationWithReason(tool, inside, true, parsed); outcome != policyPermit || reason != "" {
		t.Errorf("a declared resource inside the selector gave outcome=%v reason=%q, want permit", outcome, reason)
	}

	// Inside the fence, outside the operator's selector: a question. The
	// person can widen the selector, so refusing would withhold the only
	// answer that helps.
	editable := []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: filepath.Join(root, "lib", "b.txt")}}
	if outcome, reason, _, _ := kernel.decideInvocationWithReason(tool, editable, true, parsed); outcome != policyAsk || reason != "" {
		t.Errorf("a declared resource outside the editable row scope gave outcome=%v reason=%q, want ask with no refusal reason",
			outcome, reason)
	}

	// Outside the fence: a refusal, and RefusedOutOfScope is its sentence.
	// No answer a person could give makes this call executable.
	fenced := []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: "/etc/hosts"}}
	if outcome, reason, _, _ := kernel.decideInvocationWithReason(tool, fenced, true, parsed); outcome != policyRefuse || reason != RefusedOutOfScope {
		t.Errorf("a declared resource outside the run fence gave outcome=%v reason=%q, want refuse/%s",
			outcome, reason, RefusedOutOfScope)
	}
}

// TestTheKernelAndThePolicyReturnOneVerdictForOneResource is the acceptance
// criterion that closes the disagreement: for one policy and one resource,
// the command path and the declared path answer the SAME Verdict — decision,
// cause and resource — not merely the same decision.
func TestTheKernelAndThePolicyReturnOneVerdictForOneResource(t *testing.T) {
	root := t.TempDir()
	selector := filepath.Join(root, "src")
	grant := observeGrantFencedTo(t, root, selector)
	fence := grant.Policy.RunFence()

	for _, path := range []string{
		filepath.Join(selector, "a.txt"),    // inside both
		filepath.Join(root, "lib", "b.txt"), // inside the fence, outside the selector
		"/etc/hosts",                        // outside both
	} {
		t.Run(path, func(t *testing.T) {
			command := grant.Policy.EvaluateInvocation(
				content.EffectObserve,
				content.Invocation{
					Commands: [][]string{{"cat", path}},
					Parsed:   true,
					Resources: content.ResourceReport{
						Resources: []content.Resource{{Path: path, Verb: content.ResourceRead}},
					},
				},
				fence,
			)
			declared := grant.Policy.EvaluateResources(
				content.EffectObserve,
				grant.Policy.DecisionFor(content.EffectObserve),
				[]content.GrantScope{{Kind: content.ResourcePath, ID: path}},
				fence,
			)
			if command != declared {
				t.Fatalf("one resource, two verdicts: `cat %s` gave %+v and the declared path gave %+v", path, command, declared)
			}
		})
	}
}

// TestAPolicyAskDoesNotBecomeAFilesystemRead is the other half of §5.3, and
// the reason the row scope may ask at all: asking is not a widening. The
// policy layer and the capability layer are two layers, and moving the row's
// answer from refuse to ask moves nothing at the capability.
//
// content.GrantScope.Contains is a policy-time predicate over spelled ids and
// does no provider canonicalization (resource_scope.go, its doc at :64-72),
// so it cannot see a symlink escape. internal/filesystem/scoped.go can, and
// it is what actually authorizes the read — it refuses on canonical identity
// whether the matrix permitted, asked or refused.
func TestAPolicyAskDoesNotBecomeAFilesystemRead(t *testing.T) {
	root := t.TempDir()
	selector := filepath.Join(root, "src")
	outside := filepath.Join(root, "lib")
	for _, dir := range []string{selector, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	target := filepath.Join(outside, "b.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
	// A symlink INSIDE the row scope that resolves OUTSIDE it. Its spelled
	// path is inside the scope, so the policy predicate cannot tell it from
	// any other file in there; only the canonical identity can.
	escape := filepath.Join(selector, "escape.txt")
	if err := os.Symlink(target, escape); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	grant := observeGrantFencedTo(t, root, selector)
	kernel := &effectKernel{grant: grant}
	decl := filesReadTool(t)
	decl.Effect = content.EffectObserve

	// The policy ASKS for the path outside the selector: it is inside the
	// run fence, so the selector is the only thing excluding it and a person
	// could widen it.
	refs := []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: target}}
	outcome, reason, _, _ := kernel.decideInvocationWithReason(decl, refs, true, content.Invocation{Parsed: true})
	if outcome != policyAsk || reason != "" {
		t.Fatalf("policy on %q gave outcome=%v reason=%q, want ask", target, outcome, reason)
	}

	// And the capability minted from that very grant still refuses it.
	capability, err := decl.Narrow(grant, refs, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("narrow files.read: %v", err)
	}
	reader, ok := capability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("files.read narrowed to %T, not *filesystem.ScopedReader", capability)
	}
	if _, readErr := reader.Read(context.Background(), target, 1<<20); !errors.Is(readErr, filesystem.ErrOutOfScope) {
		t.Fatalf("the asked-about path read back with error %v, want ErrOutOfScope — a policy ask is not a widening", readErr)
	}

	// The symlink escape: spelled inside the scope, canonically outside it.
	// The policy predicate lets it through, and the capability does not.
	escapeRefs := []agenttools.ResourceRef{{Kind: content.ResourcePath, ID: escape}}
	if outcome, _, _, _ := kernel.decideInvocationWithReason(decl, escapeRefs, true, content.Invocation{Parsed: true}); outcome != policyPermit {
		t.Fatalf("policy on the symlink %q gave outcome=%v; the lexical predicate cannot see the escape and must permit it here", escape, outcome)
	}
	escapeCapability, err := decl.Narrow(grant, escapeRefs, agenttools.RunContext{})
	if err != nil {
		t.Fatalf("narrow files.read for the symlink: %v", err)
	}
	escapeReader, ok := escapeCapability.(*filesystem.ScopedReader)
	if !ok {
		t.Fatalf("files.read narrowed to %T, not *filesystem.ScopedReader", escapeCapability)
	}
	if _, readErr := escapeReader.Read(context.Background(), escape, 1<<20); !errors.Is(readErr, filesystem.ErrOutOfScope) {
		t.Fatalf("the symlink escape read back with error %v, want ErrOutOfScope — the capability is the enforcement", readErr)
	}
}
