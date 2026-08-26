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
	"fmt"
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
	Invoke(ctx context.Context, name, callID, rawArgs string) (string, error)
	// FrameForModel marks a result as untrusted data on its way back to the
	// model.
	FrameForModel(tool, result string) string
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

func (c *callsCarrier) Declare(permitted []agenttools.Tool) ([]tool.BaseTool, error) {
	return declaredTools(permitted)
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

func (c *programCarrier) FrameForModel(_, result string) string { return result }

func (c *programCarrier) Invoke(ctx context.Context, name, _, rawArgs string) (string, error) {
	if name != programToolName {
		return "", fmt.Errorf("this run composes work with a program: call %s, not %q", programToolName, name)
	}
	source, err := stringArg(rawArgs, "source")
	if err != nil {
		return "", err
	}
	return c.runs.drive(ctx, c.runID, func() (<-chan *Suspension, func(context.Context) (string, error)) {
		sc := newStarlarkCarrier(c.granted(), c.permittedNames())
		return sc.Suspensions(), func(runCtx context.Context) (string, error) {
			return sc.Run(runCtx, source)
		}
	})
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

func (c *planCarrier) FrameForModel(_, result string) string { return result }

func (c *planCarrier) Invoke(ctx context.Context, name, _, rawArgs string) (string, error) {
	if name != planToolName {
		return "", fmt.Errorf("this run composes work with a plan: call %s, not %q", planToolName, name)
	}
	source, err := stringArg(rawArgs, "plan")
	if err != nil {
		return "", err
	}
	// Compiled BEFORE anything is driven, because a plan that cannot finish
	// must be refused whole rather than half-run — and the refusal is a
	// result the model can read and repair, not a failed run.
	gc, err := newGraphCarrier(c.granted(), source)
	if err != nil {
		return "", err
	}
	return c.runs.drive(ctx, c.runID, func() (<-chan *Suspension, func(context.Context) (string, error)) {
		return gc.Suspensions(), gc.Run
	})
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

// programDescription is what the model is told it may write. The vocabulary
// is derived from the grant's tools rather than written out, so a tool the
// grant does not permit is never named to the model and a tool it does permit
// cannot be forgotten here.
func programDescription(permitted []agenttools.Tool) string {
	var b strings.Builder
	b.WriteString("Run one small program that does the whole job in a single step. " +
		"Write it in Starlark (Python-like). The program's own variables carry each " +
		"result into the next call, so you never have to see an intermediate value to " +
		"use it. There are no loops without a bound, no imports, no file or network " +
		"access, and nothing exists except the functions below.\n\n" +
		"Call answer(text) with what the person should be told; call it more than once " +
		"to say several things in order.\n\nAvailable functions:\n")
	for _, t := range permitted {
		fmt.Fprintf(&b, "  %s(...) — %s\n", intrinsicName(t.Name), t.Description)
	}
	b.WriteString("\nEvery function takes its arguments by name and returns the tool's " +
		"result as a dict. Permissions are unchanged: a call the person has to approve " +
		"still stops and asks.")
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
		"other name.\n\nAvailable effects:\n")
	for _, t := range permitted {
		fmt.Fprintf(&b, "  %s — %s\n", t.Name, t.Description)
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
