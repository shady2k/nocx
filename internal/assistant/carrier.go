package assistant

// THE CARRIER SEAM (nocx-d6gn4.8): what the model is offered, and what
// happens when it reaches for it.
//
// The epic's question is whether multi-step work is better composed by the
// model naming one effect at a time or by the model writing one thing that
// composes them. A question like that is only answerable with CONTROLLED
// COHORTS: declare both methods to the model at once and what gets measured
// is tool-description salience, ordering and model bias — the metric the bead
// names and rejects. So a person chooses the method, and every run records
// which one it used.
//
// AD-8 IS WHY THIS IS AN INTERFACE AND NOT A SWITCH STATEMENT IN Ask. The
// variance between the three is exactly two facts — the tool set the model
// sees, and what one invocation does — and both belong to the carrier. A
// fourth carrier is a fourth implementation of this interface and nothing
// else; if adding one means editing Ask, the seam is in the wrong place.
//
// WHAT IS NOT VARIANT, and must never become variant: the effect kernel.
// Validation, policy, the attempt record, the narrowed capability, execution
// and egress screening happen once, in one owner, for all three. A carrier
// that could reach an effect without the kernel would be a way round the gate
// rather than another way to compose work.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/shady2k/nocx/internal/agenttools"
)

// CarrierKind names one method. The values are the settings option ids
// (settings.AssistantCarrier) — one name for one thing, so a run's recorded
// carrier and the control that chose it cannot drift apart.
type CarrierKind string

const (
	// CarrierCalls is the shipped method and the authority floor: the model
	// names one effect, the host runs it, the result returns to the model's
	// context, and the model names the next.
	CarrierCalls CarrierKind = "calls"
	// CarrierProgram is the program carrier (starlarkcarrier.go).
	CarrierProgram CarrierKind = "program"
	// CarrierGraph is the plan carrier (graphcarrier.go).
	CarrierGraph CarrierKind = "graph"
)

// carrier is the seam the switch selects between.
type carrier interface {
	// Declare is the tool set this run's model is offered, projected from the
	// tools the grant permits.
	Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error)
	// Invoke runs one thing the model reached for and returns what the model
	// is to be told.
	Invoke(ctx context.Context, name, callID, rawArgs string) (modelResult, error)
	// FrameForModel marks a result as untrusted data on its way back to the
	// model.
	FrameForModel(tool, result string) string
	// UnknownTool is what the model is told when it reached for a name this
	// carrier never offered it.
	//
	// IT IS ON THE CARRIER because the answer differs per carrier and is
	// useless if it does not: under the declared-call carrier the model
	// invented a tool, and under a composing carrier it most likely reached
	// for one of the functions the envelope's description names — which is
	// not a mistake about what exists, but about WHERE to write the call.
	// Saying "no such tool" to that would be true and would not help.
	UnknownTool(name, rawArgs string) (string, error)
}

// newCarrier builds the one this run uses. An unknown kind is an ERROR and
// never a quiet fall back to the shipped carrier: a run recorded as one
// method and executed as another makes every measurement the experiment takes
// unattributable, which is worse than a refused question.
func newCarrier(kind CarrierKind, k *effectKernel, runs *parkedRuns, runID string) (carrier, error) {
	switch kind {
	case "", CarrierCalls:
		return &callsCarrier{effectKernel: k}, nil
	case CarrierProgram:
		return &programCarrier{kernel: k, runs: runs, runID: runID}, nil
	case CarrierGraph:
		return &planCarrier{kernel: k, runs: runs, runID: runID}, nil
	default:
		return nil, fmt.Errorf("ask: no such carrier %q; the methods are %q, %q and %q",
			string(kind), CarrierCalls, CarrierProgram, CarrierGraph)
	}
}

// ── The declared-call carrier ──────────────────────────────────────────

// callsCarrier is the kernel offered directly: every declared tool is a tool
// the model may name, and one invocation is one effect.
type callsCarrier struct{ *effectKernel }

func (c *callsCarrier) Invoke(ctx context.Context, name, callID, rawArgs string) (modelResult, error) {
	return c.effectKernel.invokeClassified(ctx, name, callID, rawArgs)
}

func (c *callsCarrier) Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error) {
	return declaredTools(permitted)
}

func (c *callsCarrier) UnknownTool(name, _ string) (string, error) {
	return fmt.Sprintf("There is no such tool %q. The tools you may call are the ones you were given and no others: %s.",
		name, strings.Join(permittedNames(c.effectKernel), ", ")), nil
}

// ── The program carrier ────────────────────────────────────────────────

// programToolName is the one tool a program-carrier run declares. It is NOT a
// row in the agenttools registry, and that is deliberate: a registry row is an
// EFFECT — it has a class, resources and a policy row — and this is an
// envelope that produces effects rather than being one. Putting it in the
// table would give the policy something to decide about that has nothing to
// decide.
const programToolName = "run_program"

type programCarrier struct {
	kernel *effectKernel
	runs   *parkedRuns
	runID  string
}

func (c *programCarrier) Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error) {
	return []tool.BaseTool{&declaredCarrierTool{info: &schema.ToolInfo{
		Name:        programToolName,
		Desc:        programDescription(permitted),
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(stringParamSchema("source", "The program to run.")),
	}}}, nil
}

// FrameForModel: ALWAYS, and unconditionally. The registry frames observe
// tools and leaves the rest alone, because there a result's provenance is the
// tool's own row. Here it is not: everything a program hands back is derived
// from tool output, from a file, from a screen, or from text the model itself
// wrote, and the envelope has no row to ask. So the whole of it is marked
// untrusted — which is also what keeps a program from being the way round the
// marker that a declared call cannot be.
func (c *programCarrier) FrameForModel(_, result string) string {
	return agenttools.FrameUntrusted(result)
}

// UnknownTool: almost always the model calling one of the program's own
// functions directly, which is what a live model did the first time it was
// offered this carrier. So it is answered as the misplacement it is, with the
// call it just tried rewritten into the shape that would have worked.
func (c *programCarrier) UnknownTool(name, rawArgs string) (string, error) {
	return fmt.Sprintf(
		"%q is not a tool — it is a function you may call INSIDE a program. "+
			"Do not call it directly. Call %s with a program that calls it, like this:\n\n"+
			"%s(source = ...) where the program is:\n"+
			"  result = %s(...)   # the arguments you just tried: %s\n"+
			"  print(result[\"text\"])\n\n"+
			"The only tool this run has is %s.",
		name, programToolName, programToolName, name, rawArgs, programToolName), nil
}

func (c *programCarrier) Invoke(ctx context.Context, name, _, rawArgs string) (modelResult, error) {
	if name != programToolName {
		return modelResult{}, fmt.Errorf("this run composes work with a program: call %s, not %q", programToolName, name)
	}
	source, err := stringArg(rawArgs, "source")
	if err != nil {
		return repairableModel("", err)
	}
	// THE PROGRAM IS RECORDED BEFORE IT RUNS, and the run's outcome after
	// it. Under the declared-call carrier every effect announces itself with
	// its arguments and the log has the shape of the work; under this one the
	// whole of what the model decided is one string nobody could see, and
	// three failures in a row were diagnosed by guessing because of it.
	c.kernel.note("agent program: running", "run", c.runID, "source", source)
	out, err := c.runs.drive(ctx, c.runID, c.granted(), func() (<-chan *Suspension, func(context.Context) (string, error), func(invoker)) {
		sc := newStarlarkCarrier(c.granted(), c.permittedNames())
		return sc.Suspensions(), func(runCtx context.Context) (string, error) {
			return sc.Run(runCtx, source)
		}, sc.setKernel
	})
	if err != nil {
		c.kernel.warn("agent program: stopped", "run", c.runID, "error", err)
	}
	return repairableModel(out, err)
}

// ── The plan carrier ───────────────────────────────────────────────────

const planToolName = "run_plan"

type planCarrier struct {
	kernel *effectKernel
	runs   *parkedRuns
	runID  string
}

func (c *planCarrier) Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error) {
	return []tool.BaseTool{&declaredCarrierTool{info: &schema.ToolInfo{
		Name:        planToolName,
		Desc:        planDescription(permitted),
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(stringParamSchema("plan", "The plan to run, as a JSON object.")),
	}}}, nil
}

// FrameForModel: always, for the reason programCarrier's says.
func (c *planCarrier) FrameForModel(_, result string) string {
	return agenttools.FrameUntrusted(result)
}

// UnknownTool: as the program carrier's, one shape further out — a step of a
// plan rather than a line of a program.
func (c *planCarrier) UnknownTool(name, rawArgs string) (string, error) {
	return fmt.Sprintf(
		"%q is not a tool — it is an effect you may name in a PLAN STEP. "+
			"Do not call it directly. Call %s with a plan that uses it, like this:\n\n"+
			`  {"steps":[{"id":"one","effect":%q,"args":{...}},{"answer":"one.text"}]}`+
			"\n\nThe arguments you just tried were: %s. The only tool this run has is %s.",
		name, planToolName, name, rawArgs, planToolName), nil
}

func (c *planCarrier) Invoke(ctx context.Context, name, _, rawArgs string) (modelResult, error) {
	if name != planToolName {
		return modelResult{}, fmt.Errorf("this run composes work with a plan: call %s, not %q", planToolName, name)
	}
	source, err := stringArg(rawArgs, "plan")
	if err != nil {
		return repairableModel("", err)
	}
	// Compiled BEFORE anything is driven, because a plan that cannot finish
	// must be refused whole rather than half-run — and the refusal is a
	// result the model can read and repair, not a failed run.
	gc, err := newGraphCarrier(c.granted(), source)
	if err != nil {
		c.kernel.warn("agent plan: refused whole", "run", c.runID, "plan", source, "error", err)
		return repairableModel("", err)
	}
	c.kernel.note("agent plan: running", "run", c.runID, "plan", source)
	out, err := c.runs.drive(ctx, c.runID, c.granted(), func() (<-chan *Suspension, func(context.Context) (string, error), func(invoker)) {
		return gc.Suspensions(), gc.Run, gc.setKernel
	})
	if err != nil {
		c.kernel.warn("agent plan: stopped", "run", c.runID, "error", err)
	}
	return repairableModel(out, err)
}

// repairable is what a composing carrier returns when the thing the model
// wrote did not work.
//
// IT IS A RESULT, NOT AN ERROR, and that is the whole point. Under the
// declared-call carrier a call the kernel refused comes back to the model as a
// RESULT — a refusal is an answer — and the model reads it and tries something
// else. A program that does not compile is the same event one level up: the
// model wrote something wrong and what it needs is the diagnostic. Returning
// it as an error made a mistyped bracket TERMINAL, which no declared call has
// ever been: the framework wrapped it as a NodeRunError, nothing could name
// the cause, and the person read "the model failed to answer" over a missing
// parenthesis. Observed 2026-08-27 on a live model, first question asked.
//
// TWO THINGS STAY ERRORS, and they are exactly the ones the model cannot act
// on: a SUSPENSION, because the host reads the person's question off it and a
// swallowed one would strand the run; and a CANCELLED CONTEXT, because the run
// is over and there is nobody left to repair anything.
func repairable(out string, err error) (string, error) {
	if err == nil {
		return out, nil
	}
	var ask *ApprovalRequestedError
	var egress *EgressRequestedError
	if errors.As(err, &ask) || errors.As(err, &egress) {
		return "", err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	// AND A WITHHELD RESULT, which is the one that taught this list its third
	// member. Egress screening failing is not a mistake in what the model
	// wrote: the gate could not see the result, so nothing left, and it will
	// fail identically on every retry until a PERSON acts — the vault is
	// sealed, or screening is misconfigured. Handed back as a repairable
	// result it made a live model retry the same call three times against a
	// sealed vault. As an error it terminalizes the run with the sentence the
	// person already has for it ("the result could not be screened, so it was
	// withheld. Check the vault…"), which is addressed to whoever can fix it.
	var withheld *EgressScreeningError
	if errors.As(err, &withheld) {
		return "", err
	}
	return "That did not work:\n" + err.Error() + "\n\nFix it and call this tool again.", nil
}

func repairableModel(out string, err error) (modelResult, error) {
	out, err = repairable(out, err)
	if err != nil {
		return modelResult{}, err
	}
	return modelResult{text: out, kind: modelToolOutput}, nil
}

// ── Shared between the two composing carriers ──────────────────────────

// granted is the kernel narrowed to "may this run reach that tool" rather
// than "does that tool exist". A carrier that validates a whole program or
// plan before running it must refuse a name outside the grant THERE — the
// strongest refusal is the one never proposed, and a plan previewed to a
// person must not show a step that was never going to be allowed.
func (c *programCarrier) granted() invoker { return granted(c.kernel) }
func (c *planCarrier) granted() invoker    { return granted(c.kernel) }

func (c *programCarrier) permittedNames() []string { return permittedNames(c.kernel) }

func granted(k *effectKernel) invoker {
	return grantedInvoker{effectKernel: k, permitted: permittedSet(k)}
}

type grantedInvoker struct {
	*effectKernel
	permitted map[string]bool
}

func (g grantedInvoker) Declares(tool string) bool { return g.permitted[tool] }

func permittedSet(k *effectKernel) map[string]bool {
	out := map[string]bool{}
	for _, name := range permittedNames(k) {
		out[name] = true
	}
	return out
}

func permittedNames(k *effectKernel) []string {
	var out []string
	for _, t := range k.registry.ForGrant(k.grant) {
		out = append(out, t.Name)
	}
	return out
}

// declaredCarrierTool is the envelope the model is offered. Its InvokableRun
// is never reached: the middleware wraps every invokable tool call and hands
// it to the carrier, exactly as it does for a declared effect. It says so
// rather than returning something plausible, because a carrier envelope that
// silently returned "" would look like a program that answered nothing.
type declaredCarrierTool struct {
	info *schema.ToolInfo
}

func (d *declaredCarrierTool) Info(context.Context) (*schema.ToolInfo, error) {
	return d.info, nil
}

func (d *declaredCarrierTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "", fmt.Errorf("tool %q reached the framework without passing the carrier", d.info.Name)
}

// stringParamSchema is the one-string-parameter schema both envelopes take.
// Inline rather than a file in contracts/: contracts/tools holds the params
// of REGISTRY tools — the shapes the kernel validates a model's arguments
// against — and these two are not registry tools (see programToolName).
func stringParamSchema(name, desc string) *jsonschema.Schema {
	props := jsonschema.NewProperties()
	props.Set(name, &jsonschema.Schema{Type: "string", Description: desc})
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             []string{name},
		AdditionalProperties: jsonschema.FalseSchema,
	}
}

// stringArg reads one required string member out of the model's arguments.
func stringArg(rawArgs, name string) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &obj); err != nil {
		return "", fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	v, ok := obj[name].(string)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s is required and must be a string", name)
	}
	return v, nil
}

// ── the signature a composing carrier shows for one tool ───────────────────
//
// WHAT THIS FIXES, measured 2026-08-27 (nocx-d6gn4.8.1). A composing carrier
// shows the model a list of functions and, until this, told it NOTHING about
// either end of them: the arguments were printed as "(...)" — three literal
// dots — and the result was described as "the tool's result as a dict". Under
// a declared call neither gap exists: the model is handed the params schema
// and reads the result as text. Under a program it must spell the arguments
// and index the dict, and it had nothing to spell them from.
//
// A live run cost three turns to that. `session_list()` with no arguments,
// refused by the params schema it was never shown; then the key from the
// worked example, `result["text"]`, which session.list does not have; then it
// gave up and answered with the whole dict. It never reached the second tool
// the question needed.
//
// Both halves are rendered from the SAME schemas the kernel enforces — the
// params the model's arguments are validated against, and the result the
// executor is held to (kernel.checkResult). A description built any other way
// would be prose that drifts, which is the defect this replaces, not a fix
// for it.

// signature renders one tool as a program's function: its named arguments,
// which are required, and the keys of what it hands back.
func signature(t agenttools.Tool) string {
	var b strings.Builder
	b.WriteString(intrinsicName(t.Name) + "(" + strings.Join(argumentNames(t.ParamsSchema), ", ") + ")")
	b.WriteString(" — " + t.Description + "\n")
	if fields := resultFields(t.ResultSchema); len(fields) > 0 {
		b.WriteString("      returns a dict with: " + strings.Join(fields, ", ") + "\n")
	}
	return b.String()
}

// argumentNames lists a tool's parameters, required ones first and optional
// ones marked, straight off the schema the kernel validates against.
func argumentNames(params json.RawMessage) []string {
	var doc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(params, &doc); err != nil {
		return nil
	}
	required := map[string]bool{}
	out := make([]string, 0, len(doc.Properties))
	for _, name := range doc.Required {
		required[name] = true
		if _, ok := doc.Properties[name]; ok {
			out = append(out, name)
		}
	}
	optional := make([]string, 0, len(doc.Properties))
	for name := range doc.Properties {
		if !required[name] {
			optional = append(optional, name+" (optional)")
		}
	}
	sort.Strings(optional)
	return append(out, optional...)
}

// resultFields lists the keys a tool's result carries, the required ones
// first, so a program indexes what is there rather than what it guessed.
// Types are deliberately left out: a JSON number reaches a program as a
// float whatever the schema calls it (starlarkcarrier.go), and a description
// that promised "integer" would be its own small lie.
func resultFields(result json.RawMessage) []string {
	if len(result) == 0 {
		return nil
	}
	var doc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		return nil
	}
	required := map[string]bool{}
	out := make([]string, 0, len(doc.Properties))
	for _, name := range doc.Required {
		required[name] = true
		if _, ok := doc.Properties[name]; ok {
			out = append(out, name)
		}
	}
	optional := make([]string, 0, len(doc.Properties))
	for name := range doc.Properties {
		if !required[name] {
			optional = append(optional, name+" (may be absent)")
		}
	}
	sort.Strings(optional)
	return append(out, optional...)
}

// bareField drops the "(may be absent)" a rendered field carries.
func bareField(field string) string {
	if i := strings.Index(field, " ("); i > 0 {
		return field[:i]
	}
	return field
}

// exampleTool picks the tool the worked example is written from: one that
// actually HAS the field the example indexes. The example used to be built
// from whichever tool came first and always index ["text"] — and when that
// tool was session.list, which returns items and no text, the example taught
// a key that does not exist and a live model copied it.
func exampleTool(permitted []agenttools.Tool) (agenttools.Tool, string, bool) {
	for _, t := range permitted {
		for _, field := range resultFields(t.ResultSchema) {
			// Matched on the bare name: resultFields decorates an optional
			// one, and `text` is optional for files.read (a binary file has
			// none) while still being the field an example should show.
			if bareField(field) == "text" {
				return t, "text", true
			}
		}
	}
	for _, t := range permitted {
		if fields := resultFields(t.ResultSchema); len(fields) > 0 {
			return t, bareField(fields[0]), true
		}
	}
	return agenttools.Tool{}, "", false
}

// programDescription is what the model is told it may write. The vocabulary
// is derived from the grant's tools rather than written out, so a tool the
// grant does not permit is never named to the model and a tool it does permit
// cannot be forgotten here.
func programDescription(permitted []agenttools.Tool) string {
	var b strings.Builder
	// "DO THE WHOLE JOB IN ONE PROGRAM" was the first wording, and it cost a
	// live model three minutes and thirty-five thousand characters of
	// reasoning without ever emitting a call: told it had one shot, it
	// planned instead of acting, rewriting the same program in its head. The
	// premise was false as well as expensive — this tool may be called as
	// many times as it likes, each call its own turn — so saying so is both
	// more accurate and the thing that unblocks it. What the carrier is FOR
	// still gets said, one sentence later: a chain of dependent steps belongs
	// in ONE program, because that is the saving. The two are not in tension;
	// the first wording just left out the half that gives permission to try.
	b.WriteString("You act by writing a program and passing it as `source`. " +
		"This is the only tool you have; the names below are functions inside the program, " +
		"NOT tools — calling one of them as a tool does nothing.\n\n" +
		"YOU MAY CALL THIS AS MANY TIMES AS YOU LIKE. Do not plan the perfect program: " +
		"write a short one, look at what comes back, and write the next. What belongs in " +
		"ONE program is a chain where a later step needs an earlier step's RESULT — that " +
		"chain costs you one turn here instead of one turn per step.\n\n" +
		"The language is Starlark, which reads like Python. A value one call returns is a " +
		"variable the next call can use, so you never have to see an intermediate result to " +
		"act on it. No imports, no file or network access, no unbounded loops, and nothing " +
		"exists except the functions below and `print`.\n\n" +
		"WHAT IS NOT IN IT, and every line of this list is something a live model " +
		"reached for and lost a turn to:\n" +
		"  - No import, and no standard library. There is no os, no subprocess, no re.\n" +
		"  - No try/except. A call that fails ends the program and its error comes " +
		"back to you; you do not guard against it.\n" +
		"  - No f-strings. \"{x} here\" is that text literally. Use + , or " +
		"\"{} here\".format(x), or just pass the pieces to print as separate arguments.\n" +
		"  - No `else` on a `for` loop, no `while`, and no recursion.\n" +
		"What IS there: def, if/elif/else, for over a list or dict, list and dict " +
		"comprehensions, break and continue, and the ordinary string, list and dict " +
		"methods — .strip() .split() .startswith() .lower() .format() .get() .append().\n\n")

	b.WriteString("Functions — each one's ARGUMENTS and the keys of what it RETURNS:\n")
	for _, t := range permitted {
		b.WriteString("  " + signature(t))
	}
	// ONE NAME FOR SAYING SOMETHING, and it is the one every model already
	// types. `answer` is still bound and still works — 42% of the programs
	// measured called it, and failing them on an unknown name would spend a
	// repair turn to enforce our preference — but it is not advertised: a
	// second documented name for one behaviour is a second thing to keep in
	// step, and the model needs no choice to make here.
	b.WriteString("  print(...) — EVERYTHING YOU SEND TO print, THE PERSON SEES. It takes any " +
		"number of values, of any type. Print as often as you like; the person reads what " +
		"you printed, in the order you printed it, and nothing else reaches them.\n\n")

	// A WORKED EXAMPLE, and it is not decoration. Told only the rules, a live
	// model called one of the functions above as a tool — which is what the
	// rest of its prompt had trained it to do. An example is the shortest
	// unambiguous statement of "this is program text, and here is its shape".
	// It is built from a real permitted tool rather than written out, so it
	// cannot name something this run does not have.
	if t, field, ok := exampleTool(permitted); ok {
		args := make([]string, 0, 2)
		for _, name := range argumentNames(t.ParamsSchema) {
			if strings.HasSuffix(name, "(optional)") {
				continue
			}
			args = append(args, name+` = ...`)
		}
		fmt.Fprintf(&b, "Example of the shape (not of what to do):\n"+
			"  result = %s(%s)\n"+
			"  print(result[%q])\n\n",
			intrinsicName(t.Name), strings.Join(args, ", "), field)
	}

	b.WriteString("Every function takes its arguments BY NAME — the names listed above, and no " +
		"others — and returns a dict with exactly the keys listed above and no others. " +
		"If a program fails you get the error and can send another one, so prefer " +
		"trying to deliberating. Permissions are unchanged: a call the person has to approve " +
		"still stops and asks, and the program continues from there once they answer.")
	return b.String()
}

// planDescription is the same for a plan.
func planDescription(permitted []agenttools.Tool) string {
	var b strings.Builder
	b.WriteString("Lay out the whole job as a plan and it will be run for you. " +
		`The plan is a JSON object {"steps":[...]}. Each step is either ` +
		`{"id":"name","effect":"tool.name","args":{"param":"<expression>"}} or ` +
		`{"answer":"<expression>"} — exactly one answer step, last. ` +
		"An expression is CEL: string literals are quoted, and a step's result is " +
		"read by naming its id, so \"one.text.trim()\" is the trimmed text the step " +
		"called `one` returned. A later step may name any earlier step's id and no " +
		"other name.\n\nEffects — each one's ARGUMENTS and the fields of its result, " +
		"which is what a later step reads through the step's id:\n")
	// The same two facts the program carrier shows, and for the same reason:
	// a step naming `one.text` on an effect that has no text is the identical
	// blind index, and here it fails the whole plan at validation.
	for _, t := range permitted {
		b.WriteString("  " + t.Name + "(" + strings.Join(argumentNames(t.ParamsSchema), ", ") + ") — " + t.Description + "\n")
		if fields := resultFields(t.ResultSchema); len(fields) > 0 {
			b.WriteString("      its result has: " + strings.Join(fields, ", ") + "\n")
		}
	}
	b.WriteString("\nThe whole plan is checked before anything runs, so a plan that " +
		"cannot finish is returned to you unrun. Permissions are unchanged: a step the " +
		"person has to approve still stops and asks.")
	return b.String()
}

// intrinsicName is the tool's name as a program spells it. A dot is not an
// identifier character in any of the dialects, so it becomes an underscore —
// and this function is the ONE place that mapping exists, so the name the
// model is shown and the name the interpreter binds cannot disagree.
func intrinsicName(tool string) string {
	return strings.ReplaceAll(tool, ".", "_")
}
