package farhelper

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
)

// Rows are intentional behavior assertions, not a generated Cartesian product.
// The sentinels force a new enum member to add an independently reviewed row.
func TestEveryPhaseEventTransition(t *testing.T) {
	rows := []struct {
		kind eventKind
		want [4]phase
	}{
		{eventObserve, [4]phase{phaseDeciding, phaseDeciding, phaseAbsent, phaseBroken}},
		{eventInputs, [4]phase{phaseAbsent, phaseAbsent, phaseAbsent, phaseAbsent}},
		{eventFreeze, [4]phase{phaseDeciding, phaseDeciding, phaseAbsent, phaseBroken}},
		{eventThaw, [4]phase{phaseDeciding, phaseServing, phaseAbsent, phaseBroken}},
		{eventRefresh, [4]phase{phaseDeciding, phaseServing, phaseAbsent, phaseBroken}},
		{eventLost, [4]phase{phaseDeciding, phaseBroken, phaseAbsent, phaseBroken}},
		{eventResult, [4]phase{phaseDeciding, phaseServing, phaseAbsent, phaseBroken}},
		{eventTick, [4]phase{phaseDeciding, phaseServing, phaseAbsent, phaseBroken}},
		{eventClose, [4]phase{phaseDeciding, phaseDeciding, phaseAbsent, phaseBroken}},
	}
	if int(phaseCount) != 4 || len(rows) != int(eventCount) {
		t.Fatal("new phase/event needs an explicit expectation")
	}
	seen := map[eventKind]bool{}
	for _, row := range rows {
		if seen[row.kind] {
			t.Fatalf("duplicate event %d", row.kind)
		}
		seen[row.kind] = true
		for ph := phase(0); ph < phaseCount; ph++ {
			peer := &client.Client{}
			s := decisionState{phase: ph, conn: Connection{Generation: 2, State: hostconn.Connected}, haveConn: true, mode: hostmode.Helper, consent: consent.Granted, consentKnown: true, installed: &Installed{}, client: peer, rev: 3, workID: 4, working: true}
			if ph != phaseServing {
				s.client = nil
			}
			ev := event{kind: row.kind, now: time.Second, peer: peer, inputs: inputs{mode: hostmode.Raw}, conn: Connection{Generation: 2, State: hostconn.Stalled}, result: stepResult{kind: stepProbe, generation: 2, revision: 3, id: 4}}
			next, fx := transition(s, ev)
			if next.phase != row.want[ph] {
				t.Errorf("phase=%d event=%d: phase=%d want=%d", ph, row.kind, next.phase, row.want[ph])
			}
			if row.kind == eventFreeze && (!next.frozen || !fx.cancel) {
				t.Errorf("freeze did not fence work in phase %d", ph)
			}
			if row.kind == eventClose && (!next.closed || !fx.cancel) {
				t.Errorf("close did not end phase %d", ph)
			}
			if row.kind == eventInputs && (next.reason != ReasonModeRaw || next.rev != 4) {
				t.Errorf("raw not applied in phase %d: %+v", ph, next)
			}
		}
	}
}

func TestDecisionFieldsHaveOnlyOneWriter(t *testing.T) {
	guarded := map[string]bool{"phase": true, "reason": true, "action": true, "rev": true, "attempt": true, "readFailures": true, "nextAttemptAt": true, "installed": true, "platform": true, "frozen": true}
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fs := token.NewFileSet()
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		node, err := parser.ParseFile(fs, file.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "transition" {
				continue
			}
			check := func(expr ast.Expr) {
				ast.Inspect(expr, func(n ast.Node) bool {
					if field, ok := n.(*ast.SelectorExpr); ok && guarded[field.Sel.Name] {
						t.Errorf("%s: guarded write outside transition", fs.Position(field.Pos()))
					}
					return true
				})
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						check(lhs)
					}
				case *ast.IncDecStmt:
					check(x.X)
				case *ast.UnaryExpr:
					// A pointer alias would hide the eventual *alias write.
					// Refuse taking a guarded field's address outside reducer.
					if x.Op == token.AND {
						check(x.X)
					}
				case *ast.CompositeLit:
					// A struct literal can bypass selector-based checks, including a local
					// state alias. Every guarded named field is checked regardless of type.
					for _, elt := range x.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if id, ok := kv.Key.(*ast.Ident); ok && guarded[id.Name] {
								t.Errorf("%s: guarded composite write outside transition", fs.Position(id.Pos()))
							}
						}
					}
				}
				return true
			})
		}
	}
}

func TestStaleResultAndDisconnectedDialNeverMoveSchedule(t *testing.T) {
	s := decisionState{phase: phaseDeciding, conn: Connection{Generation: 2, State: hostconn.Stalled}, rev: 4, workID: 8, working: false, everServed: true}
	next, fx := transition(s, event{kind: eventResult, result: stepResult{kind: stepDial, generation: 2, revision: 4, id: 7, err: ErrExecForbidden}, now: time.Hour})
	if next.phase != phaseDeciding || next.attempt != 0 || next.nextAttemptAt != 0 || fx.start {
		t.Fatalf("late disconnected dial altered schedule: %+v", next)
	}
}

func TestDisconnectedCurrentDialDoesNotEarnBackoff(t *testing.T) {
	s := decisionState{phase: phaseDeciding, conn: Connection{Generation: 2, State: hostconn.Stalled}, rev: 4, workID: 8, working: true, everServed: true}
	next, _ := transition(s, event{kind: eventResult, result: stepResult{kind: stepDial, generation: 2, revision: 4, id: 8, err: ErrExecForbidden}, now: time.Hour})
	if next.phase != phaseDeciding || next.attempt != 0 || next.nextAttemptAt != 0 || next.working {
		t.Fatalf("current disconnected dial altered schedule: %+v", next)
	}
}

func TestReadFailureBudgetSurvivesTransportPause(t *testing.T) {
	s := decisionState{phase: phaseDeciding, conn: Connection{Generation: 2, State: hostconn.Connected}, haveConn: true, rev: 4, workID: 8, working: true, readFailures: 2, attempt: 2}
	s, _ = transition(s, event{kind: eventObserve, conn: Connection{Generation: 2, State: hostconn.Stalled}})
	s, _ = transition(s, event{kind: eventObserve, conn: Connection{Generation: 2, State: hostconn.Connected}})
	s, _ = transition(s, event{kind: eventTick, now: time.Hour})
	next, _ := transition(s, event{kind: eventResult, result: stepResult{kind: stepProbe, generation: 2, revision: 4, id: s.workID, err: context.DeadlineExceeded}, now: time.Hour})
	if next.phase != phaseAbsent || next.reason != ReasonInstallFailed || next.readFailures != 3 {
		t.Fatalf("pause renewed read budget: %+v", next)
	}
}

func TestTransitionHasNoExternalCalls(t *testing.T) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "entry.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"append": true, "redialDelay": true, "inputVerdict": true, "outcomeAbsent": true, "retire": true, "interrupt": true, "absent": true}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "transition" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && allowed[id.Name] {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "errors" && sel.Sel.Name == "Is" {
					return true
				}
			}
			t.Errorf("%s: external call inside pure transition", fs.Position(call.Pos()))
			return true
		})
	}
}

func TestFingerprintRevisionClearsOldHostOutcomeAndBuild(t *testing.T) {
	s := decisionState{phase: phaseAbsent, reason: ReasonInstallFailed, action: ActionNone, haveConn: true, conn: Connection{Generation: 2, Seq: 8, HostKeyFingerprint: "old", State: hostconn.Connected}, rev: 4, mode: hostmode.Helper, consent: consent.Granted, consentKnown: true, installed: &Installed{}, everServed: true}
	c := s.conn
	c.HostKeyFingerprint = "new"
	next, _ := transition(s, event{kind: eventObserve, conn: c})
	next, _ = transition(next, event{kind: eventInputs, inputs: inputs{mode: hostmode.Helper, consent: consent.Granted, known: true}})
	if next.rev != 5 || next.phase != phaseDeciding || next.installed != nil || next.everServed {
		t.Fatalf("old host outcome/build survived new fingerprint: %+v", next)
	}
}
