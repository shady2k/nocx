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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// kernelFor builds the effect kernel for one test grant + the real registry,
// with no framework object anywhere in the call.
func kernelFor(t *testing.T, grant content.Grant, ledger AttemptLedger) *effectKernel {
	t.Helper()
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, ledger, nil, &fakeKnownMaterial{}, "run-1", "", 1, "turn-entry", nil, nil, nil)
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
