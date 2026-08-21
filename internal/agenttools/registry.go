package agenttools

// The tool registry: the single place a tool comes into existence (design
// §5). One row per tool — its effect class, the resource kinds it touches,
// where it executes, and its params schema under contracts/tools/. Four
// consumers read a row: BeforeAgent (what to declare to the model under a
// grant), the policy (what to evaluate), the middleware (which capability to
// construct — nocx-lndv), and the schema the model is shown and its arguments
// validated against.
//
// This slice is declaration-only: nothing executes, the constructor for the
// narrowed capability (Narrow) is deliberately absent until nocx-lndv owns
// the capability types, and the engine that consumes the set is
// internal/assistant (the same commit that makes this package reachable from
// main). The three tests of design §5 are what keep the table honest, not the
// table itself.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/shady2k/nocx/internal/content"
)

type Declaration struct {
	// Name is the tool name the model calls, e.g. "files.read".
	Name string
	// Effect is the tool's class on the ADR-0020 lattice (the ledger's
	// vocabulary — content.Effect).
	Effect content.Effect
	// Resources are the resource kinds the tool touches, from the ledger's
	// closed set. A tool declaring none is offered whenever its effect is
	// permitted — the filter below has nothing to exclude it with.
	Resources []content.ResourceKind
	// ResourceArg names the argument that identifies the resource the call
	// touches — the parameter the policy's scope check reads ("is this call
	// inside the grant"). Empty when the tool names no resource in its
	// parameters: its scope is the grant's own scope for the kinds it
	// declares (git.status's repository is the grant's path scope itself).
	ResourceArg string
	// Executes says where the tool's work happens: InGo or InRenderer.
	Executes Executes
	// Params is the tool's params schema path, relative to the ROOT of the
	// fs.FS supplied to Assemble (the composition root passes the directory
	// that contains the schemas — contracts/tools — not the repo root).
	Params string
	// Narrow constructs the narrowed capability a tool executes through —
	// the only path by which a tool gains authority (ADR-0028 decision 4:
	// the dispatcher narrows, it does not check). Given the run's grant it
	// returns a capability scoped to exactly what the grant permits; the
	// tool holds nothing else, so it cannot exceed the grant because it
	// never has more. Nil until the tool's execution is wired (git.status
	// in this slice): the middleware refuses to run such a tool rather than
	// executing it without a capability.
	Narrow Narrow
}

// Capability is the narrowed authority one tool executes through. Its
// concrete type is per-tool (files.read's is *filesystem.ScopedReader); the
// registry never interprets it — the execution layer that consumes it does,
// by the same tool name the middleware looked the declaration up with, so a
// capability and its executor stay paired by construction.
type Capability any

// Narrow is a capability constructor: grant → capability. It is the
// declaration's own builder, so the middleware needs no per-tool switch to
// know how to narrow a tool — the row carries it.
type Narrow func(grant content.Grant) (Capability, error)

// Tool is one assembled declaration: the row plus its params schema, loaded
// and validated at assembly. ParamsSchema is the exact JSON the model is
// shown; nothing here validates arguments against it — that is the
// middleware's step (design §6.2).
type Tool struct {
	Declaration
	ParamsSchema json.RawMessage
}

// Registry is the assembled set of tools. It is immutable once assembled;
// the set for a grant is a projection, never a mutation.
type Registry struct {
	tools []Tool
}

// declarations is the table — the only place a tool comes into existence.
// Four rows, three execution states (design §4.1–§4.2): files.read executes
// in Go (its Narrow is wired and its executor lives in internal/assistant),
// readScreen and run execute in the renderer (InRenderer tools — the
// executor asks the renderer through the transport's broker; readScreen
// reads a session's screen, run submits a command through the same submit
// path a person uses), and git.status is declared-but-not-executable
// (Narrow nil — the middleware refuses to run it honestly). The exclusion
// tests need at least two permitted/refused rows so a grant can be shown to
// admit and to refuse.
var declarations = []Declaration{
	{
		Name:        "files.read",
		Effect:      content.EffectObserve,
		Resources:   []content.ResourceKind{content.ResourcePath},
		ResourceArg: "path",
		Executes:    InGo,
		Params:      "files.read.schema.json",
		Narrow:      narrowFilesRead,
	},
	{
		Name:        "readScreen",
		Effect:      content.EffectObserve,
		Resources:   []content.ResourceKind{content.ResourceSession},
		ResourceArg: "sessionId",
		Executes:    InRenderer,
		Params:      "readScreen.schema.json",
		Narrow:      narrowReadScreen,
	},
	{
		Name:        "run",
		Effect:      content.EffectMutateDestructive,
		Resources:   []content.ResourceKind{content.ResourceSession},
		ResourceArg: "sessionId",
		Executes:    InRenderer,
		Params:      "run.schema.json",
		Narrow:      narrowRun,
	},
	{
		Name:      "git.status",
		Effect:    content.EffectObserve,
		Resources: []content.ResourceKind{content.ResourcePath},
		Executes:  InGo,
		Params:    "git.status.schema.json",
	},
}

// Assemble loads every declaration's params schema from fsys and builds the
// set. A tool whose schema is absent or unreadable does not assemble into the
// set — the strongest refusal is the one never proposed — and is named in the
// returned error; the set that DID assemble is returned alongside so the
// caller can see exactly what the model would be offered. A missing root (the
// schemas are not shipped in this build) assembles an empty set with no
// error; a missing file inside a present root is a broken declaration and is
// loud.
func Assemble(fsys fs.FS) (Registry, error) {
	return assemble(fsys, declarations)
}

func assemble(fsys fs.FS, decls []Declaration) (Registry, error) {
	// A root that is not there is "the schemas are not shipped in this build"
	// (the shipped app carries no contracts/ tree): assemble an empty set
	// quietly, because today's every-grant-is-empty world offers nothing either
	// way and a startup failure for an unshipped artifact would break the app.
	// A root that IS there but missing a schema file is a broken declaration
	// and is loud below.
	if _, err := fs.Stat(fsys, "."); err != nil {
		return Registry{}, nil
	}
	var problems []string
	tools := make([]Tool, 0, len(decls))
	for _, d := range decls {
		if msg := validateDeclaration(d); msg != "" {
			problems = append(problems, fmt.Sprintf("%s: %s", d.Name, msg))
			continue
		}
		raw, err := fs.ReadFile(fsys, d.Params)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: params schema %q: %v", d.Name, d.Params, err))
			continue
		}
		tools = append(tools, Tool{Declaration: d, ParamsSchema: raw})
	}
	return Registry{tools: tools}, joinProblems(problems)
}

// validateDeclaration checks a row's classification: every enum value must be
// a known member, and the row must carry everything a consumer needs. The
// typed fields make an unclassified tool not compile; this is the value-level
// tripwire for a member that exists but nobody has handled.
func validateDeclaration(d Declaration) string {
	var bad []string
	if strings.TrimSpace(d.Name) == "" {
		bad = append(bad, "missing name")
	}
	if !supportedEffect(d.Effect) {
		bad = append(bad, fmt.Sprintf("unsupported effect %q", d.Effect))
	}
	for _, k := range d.Resources {
		if !supportedResourceKind(k) {
			bad = append(bad, fmt.Sprintf("unsupported resource kind %q", k))
		}
	}
	if !supportedExecutes(d.Executes) {
		bad = append(bad, fmt.Sprintf("unsupported execution site %q", d.Executes))
	}
	if strings.TrimSpace(d.Params) == "" {
		bad = append(bad, "missing params schema path")
	}
	return strings.Join(bad, "; ")
}

func joinProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.New("tools not assembled: " + strings.Join(problems, "; "))
}

// ForGrant returns exactly the tools the grant permits: every declared tool
// whose effect the grant allows AND whose resource kinds the grant covers.
// Nothing is returned "for later filtering" — a tool the grant forbids is
// absent from the set, because the strongest refusal is the one never
// proposed. The result is in table order. The grant is the ledger's type
// (content.Grant): one grant model, owned by the ledger that records it.
func (r Registry) ForGrant(g content.Grant) []Tool {
	effectPermitted := make(map[content.Effect]bool, len(g.Effects))
	for _, e := range g.Effects {
		effectPermitted[e] = true
	}
	kindCovered := make(map[content.ResourceKind]bool, len(g.Scopes))
	for _, s := range g.Scopes {
		kindCovered[s.Kind] = true
	}

	var out []Tool
	for _, t := range r.tools {
		if !effectPermitted[t.Effect] {
			continue
		}
		covered := true
		for _, k := range t.Resources {
			if !kindCovered[k] {
				covered = false
				break
			}
		}
		if covered {
			out = append(out, t)
		}
	}
	return out
}

// Lookup returns the tool with the given name — the middleware's declaration
// lookup (design §6.1): a name absent from the registry is malformed model
// output, not a refusal; there is nothing to call.
func (r Registry) Lookup(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// All returns the assembled set, in table order — what the middleware
// compiles validators for.
func (r Registry) All() []Tool {
	return r.tools
}

// LiveEffects is the set of effect classes at least one DECLARED tool
// carries, deduplicated, in the lattice's canonical order. Today: observe
// and mutate-destructive — the other five rows of the policy matrix have no
// tool behind them at all.
//
// The settings surface needs this and cannot derive it: five controls that
// govern nothing must not look like the two that do, and only the
// declaration table knows which is which. It goes on the wire (policy.get's
// "live") for exactly that reason.
//
// It reads the TABLE, not an assembled Registry, and the difference is
// deliberate. Assembly can drop a row whose params schema did not reach the
// binary — a build defect the composition root already fails loudly on
// (assistant.newClient refuses an empty set) — and "this row governs
// nothing" is a fact about what has been declared, not about which files
// shipped. A package-level answer is also the only one the transport can ask
// for without a registry of its own beside the assistant's, which would be a
// second composition root for one table.
func LiveEffects() []content.Effect {
	return liveEffects(declarations)
}

func liveEffects(decls []Declaration) []content.Effect {
	carried := make(map[content.Effect]bool, len(decls))
	for _, d := range decls {
		carried[d.Effect] = true
	}
	// Iterating the lattice rather than the table gives the canonical order
	// and the deduplication in one pass, and makes an effect no member of
	// allEffects covers unrepresentable here.
	out := make([]content.Effect, 0, len(allEffects))
	for _, e := range allEffects {
		if carried[e] {
			out = append(out, e)
		}
	}
	return out
}
