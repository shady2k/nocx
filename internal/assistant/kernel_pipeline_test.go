package assistant

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTheKernelDispatchesThroughThePipeline is this stage's structural
// assertion (nocx-a62cl.2.3): the kernel's one dispatch site calls the
// authority pipeline instead of dispatch.go's ad-hoc entry. The stage is
// preparatory and nothing a person sees changes, so what it can assert is the
// call the source makes; the behaviour that must not change is asserted by
// every existing test in this package, including the run lease's outcome
// under an already-cancelled context, which is why the kernel's chain keeps
// its own cancellation semantics.
func TestTheKernelDispatchesThroughThePipeline(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "kernel.go", nil, 0)
	if err != nil {
		t.Fatalf("parse kernel.go: %v", err)
	}
	var oldEntry, pipelineCall bool
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "dispatch" {
			if receiver, ok := selector.X.(*ast.SelectorExpr); ok && receiver.Sel.Name == "dispatcher" {
				oldEntry = true
			}
		}
		if selector.Sel.Name == "runOrdered" {
			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "pipeline" {
				pipelineCall = true
			}
		}
		return true
	})
	if oldEntry {
		t.Fatal("kernel.go still calls dispatchOperation.dispatch with transform, gate and pre-execute arguments")
	}
	if !pipelineCall {
		t.Fatal("kernel.go does not run its calls through the pipeline")
	}
}
