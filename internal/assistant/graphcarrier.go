package assistant

// The GRAPH carrier (nocx-d6gn4.11): the model emits a plan — effect sites and
// the pure expressions that feed them — and the host walks it.
//
// WHY A THIRD CARRIER AT ALL. It exists to be compared against the program
// carrier on one axis: WHAT AN APPROVAL CAN HONESTLY SHOW. A program is
// Turing-complete, so a prompt can state the current resolved effect and
// whatever bounds are readable off the source, and nothing more. A validated
// plan can be shown whole before anything runs — every effect site, its tool,
// which earlier steps it depends on — because those are structural facts, not
// consequences of running it.
//
// It pays for that with everything its node kinds cannot express. Today those
// kinds are `effect` and `answer`; `let`, `if` and a bounded `foreach` are
// named in the bead and are not here yet. THAT IS THE POINT OF THE
// COMPARISON, not a gap to be quietly filled: the moment this carrier grows
// enough kinds to express what a program expresses, it IS a language, and the
// argument for it has to be made again from there.
//
// THE DEPENDENCY EDGE IS EXACT HERE, and that asymmetry has to be carried into
// any comparison. This carrier reads dependencies off the expression: `index`
// appearing in step two's argument IS the edge. Under the declared-call
// carrier the same fact can only ever be evidence — a value that appears in an
// earlier result — because the model, not the host, wrote the argument.
// Scoring both by the sharper method would hand this carrier a win it did not
// earn.
//
// CEL FOR EXPRESSIONS, AND ONLY EXPRESSIONS. It is non-Turing-complete by
// construction: no loops, no recursion, no I/O, bounded evaluation. What it
// cannot do is orchestrate, which is exactly why orchestration is the plan's
// job and not the expression language's.
//
// THE KERNEL DECIDES, ALWAYS — same as every other carrier. This file defines
// no schema, no effect class, no authorization and no ledger behaviour.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
)

// planStep is one line of a plan: an effect site, or the answer.
type planStep struct {
	ID     string            `json:"id,omitempty"`
	Effect string            `json:"effect,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
	Answer string            `json:"answer,omitempty"`
}

type plan struct {
	Steps []planStep `json:"steps"`
}

// PlannedEffect is one effect site as a person can be shown it BEFORE the plan
// runs: which tool, under which step name, and which earlier steps its
// arguments read. Deliberately not the resolved arguments — those do not exist
// yet, and a preview that invented them would be a promise the run may not
// keep.
type PlannedEffect struct {
	ID        string
	Tool      string
	Args      map[string]string
	DependsOn []string
}

// compiledStep is a step with its expressions compiled and its dependencies
// resolved — the form that exists only after the whole plan validated.
type compiledStep struct {
	step      planStep
	args      map[string]cel.Program
	answer    cel.Program
	dependsOn []string
}

type graphCarrier struct {
	kernel invoker
	steps  []compiledStep

	// suspensions carries a question out to the host, exactly as the program
	// carrier's does. ONE mechanism for stopping, shared, because two ways to
	// suspend would be two things to keep in step (AD-8).
	suspensions chan *Suspension

	mu      sync.Mutex
	calls   []starlarkInvocation
	nextSeq int
}

// newGraphCarrier parses, validates and compiles a plan. IT RUNS NOTHING. A
// plan that cannot finish is refused whole, here, before any effect has
// happened — half a plan executed is worse than none, because the effects that
// already happened cannot be taken back and a person who approved a whole plan
// approved one that could finish.
func newGraphCarrier(kernel invoker, source string) (*graphCarrier, error) {
	var p plan
	if err := json.Unmarshal([]byte(source), &p); err != nil {
		return nil, fmt.Errorf("plan is not readable: %w", err)
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("plan has no steps")
	}

	c := &graphCarrier{kernel: kernel, suspensions: make(chan *Suspension)}
	// known grows as the walk goes forward, which is what makes the plan
	// ACYCLIC BY CONSTRUCTION rather than by a cycle check: an expression can
	// only name a step already declared above it, so there is no edge that
	// can point backwards to close a loop.
	known := map[string]bool{}
	seenAnswer := false

	for i, s := range p.Steps {
		switch {
		case s.Answer != "":
			if seenAnswer {
				return nil, fmt.Errorf("step %d: the plan answers twice", i+1)
			}
			seenAnswer = true
			prog, deps, err := compileExpr(s.Answer, known)
			if err != nil {
				return nil, fmt.Errorf("step %d: answer: %w", i+1, err)
			}
			c.steps = append(c.steps, compiledStep{step: s, answer: prog, dependsOn: deps})
		case s.Effect != "":
			if s.ID == "" {
				return nil, fmt.Errorf("step %d: an effect step needs an id, so later steps can name its result", i+1)
			}
			if known[s.ID] {
				return nil, fmt.Errorf("step %d: id %q is already used", i+1, s.ID)
			}
			if !kernel.Declares(s.Effect) {
				// Refused here rather than at the call, and the difference is
				// the whole point of a preview: a plan naming a tool that does
				// not exist can never finish, and a person must not be shown
				// it as though it could.
				return nil, fmt.Errorf("step %d: no such tool %q", i+1, s.Effect)
			}
			compiled := compiledStep{step: s, args: map[string]cel.Program{}}
			names := map[string]bool{}
			for arg, expr := range s.Args {
				prog, deps, err := compileExpr(expr, known)
				if err != nil {
					return nil, fmt.Errorf("step %d: argument %q: %w", i+1, arg, err)
				}
				compiled.args[arg] = prog
				for _, d := range deps {
					names[d] = true
				}
			}
			for d := range names {
				compiled.dependsOn = append(compiled.dependsOn, d)
			}
			sort.Strings(compiled.dependsOn)
			c.steps = append(c.steps, compiled)
			known[s.ID] = true
		default:
			return nil, fmt.Errorf("step %d: neither an effect nor an answer", i+1)
		}
	}
	if !seenAnswer {
		return nil, fmt.Errorf("plan never answers")
	}
	return c, nil
}

// compileExpr compiles one CEL expression against the steps declared so far
// and returns the step names it reads. A name that is not a declared step is
// an error HERE — the alternative is a plan that looks runnable and fails
// halfway, which is the state this carrier exists to make impossible.
func compileExpr(expr string, known map[string]bool) (cel.Program, []string, error) {
	// The string extension, and it is a deliberate widening rather than a
	// convenience: without trim/split/replace a plan cannot turn one tool's
	// output into another tool's argument, which is the only thing this
	// carrier exists to do. It stays non-Turing-complete — the extension adds
	// functions over values, not control flow — and its cost is bounded by
	// CEL's own evaluation limits.
	opts := []cel.EnvOption{ext.Strings()}
	for name := range known {
		opts = append(opts, cel.Variable(name, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("expression environment: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, nil, issues.Err()
	}
	prog, err := env.Program(ast)
	if err != nil {
		return nil, nil, fmt.Errorf("expression: %w", err)
	}
	var deps []string
	for name := range known {
		// Word-boundary-free on purpose for now: a step id is an identifier
		// and the expression is CEL source, so a substring hit is possible in
		// a string literal. It over-reports a dependency, never under-reports
		// one, and over-reporting shows a person a wider plan than the truth
		// rather than a narrower one.
		if strings.Contains(expr, name) {
			deps = append(deps, name)
		}
	}
	sort.Strings(deps)
	return prog, deps, nil
}

// Preview is the whole plan as a person can be shown it before it runs.
func (c *graphCarrier) Preview() []PlannedEffect {
	var out []PlannedEffect
	for _, s := range c.steps {
		if s.step.Effect == "" {
			continue
		}
		out = append(out, PlannedEffect{
			ID:        s.step.ID,
			Tool:      s.step.Effect,
			Args:      s.step.Args,
			DependsOn: s.dependsOn,
		})
	}
	return out
}

// Suspensions is where the host learns the plan stopped on a question.
func (c *graphCarrier) Suspensions() <-chan *Suspension { return c.suspensions }

// invocations returns the effects this carrier asked the kernel for, in order.
func (c *graphCarrier) invocations() []starlarkInvocation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]starlarkInvocation(nil), c.calls...)
}

// Run walks the plan and returns what it answered.
func (c *graphCarrier) Run(ctx context.Context) (string, error) {
	results := map[string]any{}
	for _, s := range c.steps {
		if s.step.Effect == "" {
			val, _, err := s.answer.Eval(results)
			if err != nil {
				return "", fmt.Errorf("answer: %w", err)
			}
			return valueString(val), nil
		}
		args := map[string]any{}
		for name, prog := range s.args {
			val, _, err := prog.Eval(results)
			if err != nil {
				return "", fmt.Errorf("step %q: argument %q: %w", s.step.ID, name, err)
			}
			args[name] = val.Value()
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("step %q: arguments: %w", s.step.ID, err)
		}

		c.mu.Lock()
		c.nextSeq++
		callID := fmt.Sprintf("plan-%d", c.nextSeq)
		c.calls = append(c.calls, starlarkInvocation{tool: s.step.Effect, callID: callID, rawArgs: string(raw)})
		c.mu.Unlock()

		out, err := invokeParking(ctx, c.kernel, c.suspensions, s.step.Effect, callID, string(raw))
		if err != nil {
			return "", err
		}
		results[s.step.ID] = decodeResult(out)
	}
	return "", fmt.Errorf("plan never answers")
}

// decodeResult turns a tool's result into something an expression can read —
// the same decision the program carrier makes, for the same reason: an
// expression doing string surgery on a serialisation would be written against
// the wire format rather than against the tool.
func decodeResult(out string) any {
	var decoded any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		return out
	}
	return decoded
}

// valueString renders what the plan answered. A string answers as itself;
// anything else answers as its CEL rendering, which is what a plan that
// answers with a structure asked for.
func valueString(val ref.Val) string {
	if s, ok := val.(types.String); ok {
		return string(s)
	}
	return fmt.Sprintf("%v", val.Value())
}
