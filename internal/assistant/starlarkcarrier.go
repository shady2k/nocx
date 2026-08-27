package assistant

// The PROGRAM carrier (nocx-d6gn4.6): the model writes one bounded program,
// and the interpreter — not the model — carries a result from one effect into
// the arguments of the next.
//
// WHAT IT BUYS, stated as the epic states it: a question whose answer needs
// two DEPENDENT effects is answered in one model turn instead of three, and
// the intermediate result never enters the model's context. Under the
// declared-call carrier the model must see what step one returned in order to
// write step two; here it is a local variable.
//
// WHY STARLARK. Not the syntax — the bounds. No `while`, recursion off by
// default, iteration over finite collections only, a step budget, no I/O
// except what the host hands in, and values frozen after a module runs. Those
// are the properties a host needs when the program's author is untrusted.
//
// WHY NO REPLAY. The bead proposed Temporal-style replay: on resume, re-run
// the program from the top and let each intrinsic return its journaled value.
// That machinery exists to reconstruct a continuation after the process that
// held it is gone, and durable suspension is explicitly out of scope for this
// epic — suspension is process-lifetime only. Within one process a parked
// goroutine holds the continuation exactly, so the interpreter runs in its own
// goroutine and an intrinsic that needs a person's answer BLOCKS. That deletes
// the whole trap list replay brings with it: code before an effect running
// repeatedly, arguments having to be stable across runs, journal-prefix
// equivalence, duplicated logging. If durability is ever wanted, it comes back
// — and it comes back with a requirement attached, not as a default.
//
// THE KERNEL DECIDES, ALWAYS. Every intrinsic here does exactly one thing:
// shape the call and hand it to the effect kernel, which validates, decides,
// records the attempt, narrows the capability, executes and screens the
// result. This file defines no schema, no effect class, no authorization and
// no ledger behaviour — AD-8, and nocx-d6gn4.3's whole point.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// invoker is the kernel as this carrier needs it: propose one effect, get
// back what the caller must hand to whoever proposed it.
type invoker interface {
	Invoke(ctx context.Context, name, callID, rawArgs string) (string, error)
	// Declares answers whether a tool exists, for a carrier that validates
	// before it runs.
	Declares(tool string) bool
}

// starlarkInvocation is what the carrier remembers about one effect it
// asked for — the record the tests read, and the beginning of what a run
// report will read.
type starlarkInvocation struct {
	tool    string
	callID  string
	rawArgs string
}

// Suspension is one question the program stopped on. The interpreter's
// goroutine is parked inside the intrinsic that raised it and holds every
// local the program had; Resume lets it make the same call again, which is
// what an approval is for — the kernel re-decides with the person's answer in
// the store, and the run continues rather than restarts.
//
// Resume is safe to call once. A second call is a programming error in the
// host, not a state the program can reach, so it is not defended against here.
type Suspension struct {
	Request *ApprovalRequest
	resume  chan struct{}
}

// Resume releases the parked program.
func (s *Suspension) Resume() { close(s.resume) }

type starlarkCarrier struct {
	// kernel is REBOUND on every resume, and that is load-bearing
	// (nocx-d6gn4.8.1). A parked program holds the kernel of the ask that
	// parked it, and that kernel's ask-scoped seams — the sink that
	// announces a call, and the context its durable writes go through —
	// die when that ask returns. A program resumed eight seconds later
	// announced its effect into a dead stream and got back a bare
	// context.Canceled, which the transport read as a lost connection.
	//
	// Only the program's LOCALS have to survive an approval; the kernel
	// does not. The drive that resumes it hands in its own
	// (parkedrun.go), so the effect is announced into the stream a person
	// is actually watching and recorded as an attempt of the ask that is
	// running.
	kernelMu sync.RWMutex
	kernel   invoker
	// tools is the run's vocabulary: the tools the grant permits, and nothing
	// else. It is passed in rather than read off the kernel because the
	// allowlist a program sees and the tool set the model was shown must be
	// ONE projection of the grant — two derivations of "which tools" is the
	// shape AGENTS.md spends a section on.
	tools []string

	// suspensions carries a question out to the host. UNBUFFERED on purpose:
	// the send completes only when somebody is listening, so a program cannot
	// park on a question nobody will ever see.
	suspensions chan *Suspension

	mu    sync.Mutex
	calls []starlarkInvocation
	// said is everything the program told the person, in the order it said
	// it. BOTH print and answer append here, and that is a decision measured
	// rather than assumed — see Print's comment.
	said    []string
	nextSeq int
}

func newStarlarkCarrier(kernel invoker, tools []string) *starlarkCarrier {
	return &starlarkCarrier{kernel: kernel, tools: tools, suspensions: make(chan *Suspension)}
}

// setKernel rebinds the carrier to the kernel of the drive that is running
// now. Called by the host before a parked effect is released, never by the
// program.
func (c *starlarkCarrier) setKernel(k invoker) {
	c.kernelMu.Lock()
	defer c.kernelMu.Unlock()
	c.kernel = k
}

// kernelNow is the kernel as of THIS moment — read at each call rather than
// captured, so an effect that parks across an approval makes its second
// attempt through the drive that resumed it.
func (c *starlarkCarrier) kernelNow() invoker {
	c.kernelMu.RLock()
	defer c.kernelMu.RUnlock()
	return c.kernel
}

// logf records one effect's outcome through the kernel's own logger when the
// kernel has one. The carrier holds no logger of its own: there is one owner
// of "how this process logs", and a second would be a second configuration to
// keep in step.
func (c *starlarkCarrier) logf(msg, tool, callID, rawArgs string, err error) {
	k, ok := c.kernelNow().(grantedInvoker)
	if !ok {
		return
	}
	k.warn(msg, "tool", tool, "call", callID, "args", rawArgs, "error", err)
}

// Suspensions is where the host learns that the program stopped on a question.
// One value per question, in the order the program asked; the host answers
// through the approval store and calls Resume.
func (c *starlarkCarrier) Suspensions() <-chan *Suspension {
	return c.suspensions
}

// Run executes one program and returns what it answered.
//
// It runs on the caller's goroutine, and the caller is expected to be one the
// host started for it (parkedrun.go): an effect that needs a person's answer
// BLOCKS inside this call, so a host that ran a program inline would block
// itself. A program that never calls answer() returns the empty string rather
// than an error: saying nothing is a poor answer, not a broken program, and
// the difference matters to whoever reads the turn.
func (c *starlarkCarrier) Run(ctx context.Context, source string) (string, error) {
	thread := &starlark.Thread{Name: "program"}
	thread.SetMaxExecutionSteps(maxProgramSteps)
	// print() IS IN THE LANGUAGE and there is no taking it out, so the only
	// question is where its output goes. Left alone it goes to the library's
	// default writer, which is nowhere anybody can read.
	//
	// PRINT REACHES THE PERSON (nocx-d6gn4.8.1), and this reverses the
	// decision b0fc089a made from one observation.
	//
	// That decision routed print to the MODEL as the program's working notes,
	// on the reasoning that a live model had written print where it meant
	// answer. The prohibition was then stated twice — in the tool description
	// and in the run's failure sentence — and measured across 40 programs
	// from two models it did not hold: 40% called print, 42% called answer,
	// and 20% called neither, so a fifth of all programs told the person
	// nothing at all. Reading the print calls settles what they were for —
	// `print("Exit Code:", code)` and `print("Text:", text)` in answer to a
	// question about a command's output are addressed to the person, not to
	// the model that wrote them.
	//
	// So the affordance is given rather than the habit forbidden: print is
	// what every model already means by "show this to the reader", and it now
	// is that. A program that prints has answered.
	thread.Print = func(_ *starlark.Thread, msg string) {
		c.say(msg)
	}
	// The context travels on the thread rather than in a closure so that
	// every intrinsic sees the same cancellation, including ones added later.
	thread.SetLocal(ctxLocal, ctx)

	if _, err := starlark.ExecFileOptions(programOptions, thread, "program.star", source, c.predeclared()); err != nil {
		var evalErr *starlark.EvalError
		if ok := asEvalError(err, &evalErr); ok {
			// The backtrace is the useful half for a reader: it names the
			// line of the program that failed, which is the only thing
			// anybody can act on when the author is a model.
			//
			// The CAUSE has to survive with it, and the first version of this
			// dropped it — the backtrace went in as a string, so the error
			// chain ended here and every caller upstream saw an opaque
			// sentence. That is how a sealed vault reached a live model as
			// the words "context canceled" and had it retrying a thing no
			// retry could fix. %w on the cause, %s for the backtrace: one
			// error carrying both.
			if cause := errors.Unwrap(evalErr); cause != nil {
				return "", fmt.Errorf("program failed: %s: %w", evalErr.Backtrace(), cause)
			}
			return "", fmt.Errorf("program failed: %s", evalErr.Backtrace())
		}
		return "", fmt.Errorf("program failed: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.said, "\n"), nil
}

// say records one thing the program told the person. The order is the
// program's own, so a program that prints a heading and then answers reads
// the way it was written.
func (c *starlarkCarrier) say(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.said = append(c.said, text)
}

// ctxLocal is the thread-local key the request context travels under.
const ctxLocal = "nocx.ctx"

// programOptions is the dialect a model may write, stated explicitly rather
// than taken from the package's legacy globals — which is what the deprecated
// ExecFile reads, and those globals are process-wide, so any other user of
// the library could widen this program's dialect from another package.
//
// The two that stay off are off for one reason: a program must TERMINATE.
// `while` has no bound a reader can see, and recursion has none either; `for`
// over a finite collection does. The step budget below bounds what is left.
//
// GLOBAL REASSIGNMENT IS ON, and it was off until it was measured
// (nocx-d6gn4.8.1). Starlark forbids it by default because a module's globals
// freeze when it is loaded, so a reassignment would break the determinism
// another module's `load` depends on. A program here is loaded by nobody: it
// is one script, run once, and there is no `load` in its vocabulary at all.
// So the rule protected a property this dialect does not have — and it cost
// the thing the carrier exists to save. One live run, one question, ten
// programs in a row, every one of them dying on it:
//
//	program.star:10:5: cannot reassign global lines declared at program.star:6:1
//	program.star:11:5: cannot reassign global start_index declared at line 9
//	program.star:15:5: cannot reassign global line declared at program.star:10:5
//
// A model writes a top-level loop and then reuses the name, which is what
// ordinary Python looks like. Ten model turns spent on a restriction that
// bought nothing is the same lesson print taught: do not fight the habit
// where the habit costs nothing.
var programOptions = &syntax.FileOptions{
	Set:               false,
	While:             false,
	TopLevelControl:   true, // if/for at the top level: a program is a script, not a module of functions
	GlobalReassign:    true,
	LoadBindsGlobally: false,
	Recursion:         false,
}

// maxProgramSteps bounds a program that terminates but takes far too long
// getting there — the budget the language's own structure cannot give,
// because a finite collection can still be large and nested loops multiply.
//
// The number is a first one and is deliberately generous: a program that
// selects an item, formats a sentence and asks for three effects is orders of
// magnitude below it, so hitting this is evidence of a program doing
// something nobody intended rather than of a budget set too tight.
const maxProgramSteps = 10_000_000

// predeclared is the program's whole vocabulary. Anything not named here does
// not exist for the program — the allowlist IS the capability surface, and it
// is built from the same declarations the wire tools are projected from.
func (c *starlarkCarrier) predeclared() starlark.StringDict {
	d := starlark.StringDict{
		// `answer` is the name the description does NOT use, kept bound
		// because models type it anyway — 17 of 40 measured programs did.
		// It is print under another name, not a second way to say things
		// (carrier.go's vocabulary line says why only one is advertised).
		"answer": starlark.NewBuiltin("answer", c.recordAnswer),
	}
	for _, tool := range c.tools {
		name := intrinsicName(tool)
		d[name] = starlark.NewBuiltin(name, c.effect(tool))
	}
	return d
}

// effect returns the intrinsic for one declared tool. It looks like an
// ordinary function that returns a value; what it does is hand a canonical
// invocation to the kernel.
func (c *starlarkCarrier) effect(tool string) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			// Keyword arguments only, and this is not a style rule: the tool's
			// parameters are a JSON object with named members, and a
			// positional call would have to invent an order for them that the
			// schema does not have.
			return nil, fmt.Errorf("%s: pass arguments by name", tool)
		}
		obj := map[string]any{}
		for _, kv := range kwargs {
			name, ok := starlark.AsString(kv[0])
			if !ok {
				return nil, fmt.Errorf("%s: argument name is not a string", tool)
			}
			v, err := fromStarlark(kv[1])
			if err != nil {
				return nil, fmt.Errorf("%s: argument %q: %w", tool, name, err)
			}
			obj[name] = v
		}
		raw, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("%s: arguments: %w", tool, err)
		}

		ctx, _ := thread.Local(ctxLocal).(context.Context)
		if ctx == nil {
			ctx = context.Background()
		}

		c.mu.Lock()
		c.nextSeq++
		callID := fmt.Sprintf("prog-%d", c.nextSeq)
		c.calls = append(c.calls, starlarkInvocation{tool: tool, callID: callID, rawArgs: string(raw)})
		c.mu.Unlock()

		out, err := invokeParking(ctx, c.kernelNow, c.suspensions, tool, callID, string(raw))
		if err != nil {
			// EVERY effect a program asks for is recorded with its outcome,
			// which the declared-call carrier gets for free (each call is
			// announced) and this one had not. A program that stops on the
			// third of five effects is otherwise one opaque sentence.
			c.logf("agent program effect failed", tool, callID, string(raw), err)
			return nil, err
		}
		return toStarlark(out), nil
	}
}

// invokeParking is the kernel call plus the one thing a carrier that owns its
// own control flow does that the declared-call carrier does not: when the
// kernel says a person must answer first, it PARKS rather than unwinding.
//
// SHARED BY EVERY SUCH CARRIER, deliberately. Suspension is not a property of
// Starlark or of a plan walker; it is what any carrier does with the kernel's
// "ask first". Two copies would be two things to keep in step, which AD-8
// spends a paragraph on.
//
// The retry after Resume is the same call — same id, same arguments — because
// that is what the approval is bound to: a changed argument hashes differently
// and deliberately does not resume under the old answer. One retry, never a
// loop: if the kernel asks again about a proposal that has just been answered,
// something is wrong that asking a person twice will not fix, and a carrier
// that kept retrying would be the ask-forever loop the resume path exists to
// end.
func invokeParking(ctx context.Context, kernelNow func() invoker, suspensions chan *Suspension, tool, callID, rawArgs string) (string, error) {
	out, err := kernelNow().Invoke(ctx, tool, callID, rawArgs)
	var ask *ApprovalRequestedError
	if !errors.As(err, &ask) {
		return out, err
	}

	s := &Suspension{Request: ask.Request, resume: make(chan struct{})}
	// context.Cause, never ctx.Err: the host cancels a parked program with a
	// NAMED cause (parkedrun.go), and flattening that to context.Canceled is
	// what let a program this process killed on purpose report itself as a
	// lost connection. The causes wrap context.Canceled, so every existing
	// check still holds.
	select {
	case suspensions <- s:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
	select {
	case <-s.resume:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
	// THE CURRENT kernel, deliberately re-read: the one this function was
	// entered with belongs to an ask that has since returned.
	return kernelNow().Invoke(ctx, tool, callID, rawArgs)
}

// recordAnswer is what the program says to the person. Called more than once,
// the answers accumulate in order — a program that writes a sentence, does
// something, and writes another sentence is exactly the shape the turn draws.
func (c *starlarkCarrier) recordAnswer(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("answer: takes no keyword arguments")
	}
	var parts []string
	for _, a := range args {
		if s, ok := starlark.AsString(a); ok {
			parts = append(parts, s)
			continue
		}
		parts = append(parts, a.String())
	}
	c.say(strings.Join(parts, " "))
	return starlark.None, nil
}

// toStarlark turns a tool's result into something a program can navigate.
//
// A tool returns a JSON object with named members (files.read's text, total
// and window; session.list's items), and a program that had to do string
// surgery on that would be a program written against the SERIALISATION rather
// than against the tool. So JSON is decoded and handed over as a dict; a
// result that is not JSON is a string, which is the honest answer for a tool
// that returns prose.
//
// The DEPTH here is deliberately dumb — no schema, no typed handle, no
// identity. Typed handles are nocx-d6gn4.2 and they are a different question
// with a different owner; anticipating them here would be this carrier
// growing a second answer to it.
func toStarlark(out string) starlark.Value {
	var decoded any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		return starlark.String(out)
	}
	v, err := valueOf(decoded)
	if err != nil {
		return starlark.String(out)
	}
	return v
}

// valueOf converts one decoded JSON value into a Starlark value. A number is
// a float even when it is whole, because that is what encoding/json produced
// and inventing an integer here would make the type depend on the value.
func valueOf(v any) (starlark.Value, error) {
	switch t := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(t), nil
	case float64:
		return starlark.Float(t), nil
	case string:
		return starlark.String(t), nil
	case []any:
		elems := make([]starlark.Value, 0, len(t))
		for _, e := range t {
			ev, err := valueOf(e)
			if err != nil {
				return nil, err
			}
			elems = append(elems, ev)
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		d := starlark.NewDict(len(t))
		for key, e := range t {
			ev, err := valueOf(e)
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(key), ev); err != nil {
				return nil, err
			}
		}
		return d, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", v)
	}
}

// fromStarlark converts one argument value into what json.Marshal will write
// as the tool's parameter. Only the types a tool schema can carry: a value
// this cannot convert is refused HERE, with the argument named, rather than
// reaching the kernel as something its validator has to reject in the
// model's words.
func fromStarlark(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.String:
		return string(t), nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.Int:
		i, ok := t.Int64()
		if !ok {
			return nil, fmt.Errorf("integer out of range")
		}
		return i, nil
	case starlark.Float:
		return float64(t), nil
	case starlark.NoneType:
		return nil, nil
	case *starlark.List:
		out := make([]any, 0, t.Len())
		for i := 0; i < t.Len(); i++ {
			e, err := fromStarlark(t.Index(i))
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, nil
	case *starlark.Dict:
		out := map[string]any{}
		for _, item := range t.Items() {
			key, ok := starlark.AsString(item[0])
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
			e, err := fromStarlark(item[1])
			if err != nil {
				return nil, err
			}
			out[key] = e
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value %s", v.Type())
	}
}

// asEvalError is errors.As for starlark's own error, kept as a named helper
// so the import of errors does not read as though the kernel's error taxonomy
// were involved here.
func asEvalError(err error, target **starlark.EvalError) bool {
	e, ok := err.(*starlark.EvalError)
	if ok {
		*target = e
	}
	return ok
}
