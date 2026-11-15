package toolendpoint

// What an agent's MCP session is TOLD when it initializes (nocx-xn63t.6.45).
//
// # The defect this answers
//
// A coordinator's Claude session initialized nocx's stdio MCP server and got
// back protocolVersion, capabilities and serverInfo — and nothing to say who
// was talking to it. Asked to spawn a worker, the model reached for the global
// herdr skill it already had and started a pane outside nocx, while the tools
// it had been granted sat unused one namespace away. The grant was never the
// problem; the sentence that points at it was missing.
//
// # Why the ENDPOINT says this, and not the MCP adapter
//
// internal/mcpstdio owns protocol translation and transport lifecycle, and it
// is held to that by its own boundary test: the domain's vocabulary ("worker",
// the workers.* names) may not appear in its source at all, so that the adapter
// stays a translator rather than becoming a second place that knows what a
// worker is. The words belong where the GRANT is, and that is here — the same
// rule contracts/toolendpoint.md states for tools.catalogue: "No caller may
// derive its own eligibility." A caller that had to work out its own role from
// its tools would be deriving it.
//
// # Why two texts, and how the role is decided
//
// The role is read off the admitted grant rather than guessed or passed in: a
// coordinator's grant permits Delegate — handing work to another agent is what
// the effect names, and spawning is that act — and a worker's grant permits
// Observe alone (internal/app/worker_auth.go's participantGrant says so in one
// place). So the question "may this caller start a worker" is answerable from
// authority the endpoint already holds, and it is answerable BEFORE the caller
// has listed a single tool.
//
// A CALLER THAT IS NEITHER — a grant that may delegate nothing and reports
// nothing — is told the neutral sentence. It is not an error and not silence:
// an MCP client that receives no instructions at all is the state this finding
// was filed against.
//
// # What the text owes the reader
//
// Three things, and each is a defect when missing:
//
//   - WHERE IT IS. That the session runs inside nocx, because the model's other
//     habits (a global skill, a terminal multiplexer the shell provides) are
//     exactly what it reaches for otherwise.
//   - WHICH CALLS DO THE WORK, named as the wire names them. A tool name in
//     this text that the registry does not declare, or that this caller's grant
//     does not offer, sends an agent to a call that does not exist and the
//     failure is silent — it simply never spawns. instructions_test.go looks
//     every name up in the assembled registry and in the caller's own
//     catalogue, with a negative half proving the scan bites.
//   - HOW TO REACH A DEFERRED TOOL. Some clients (Claude Code) do not put MCP
//     tools in the model's list at all; they are loaded by a search first, and
//     a model told to use a tool it cannot see reports that it has none. The
//     selector is spelled in the client's own syntax and its server name is
//     pinned to the key the staged MCP config writes.

import (
	"encoding/json"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// instructionsResult is the `tools.instructions` result. One field, and it is
// the MCP protocol's own word for what mcpstdio puts into initialize —
// translating a domain answer into a client's vocabulary is the adapter's job
// and not this document's (contracts/toolendpoint.md makes the same point about
// description/inputSchema/outputSchema).
type instructionsResult struct {
	Instructions string `json:"instructions"`
}

// instructionsFor is the sentence a caller is told, chosen by the authority it
// was admitted with.
func instructionsFor(grant content.Grant) string {
	switch {
	case grantPermits(grant, content.EffectDelegate):
		return coordinatorInstructions
	case grantPermits(grant, content.EffectObserve):
		return workerInstructions
	default:
		return plainInstructions
	}
}

// buildInstructions answers `tools.instructions`.
//
// Params are ignored on purpose, exactly as tools.catalogue ignores them: the
// grant is the admitted invocation's, never an argument a caller can name.
// buildDispatcherInstructions lets the assistant bind fixed initialization
// text, while pane callers retain the authority-derived role instructions.
func buildDispatcherInstructions(dispatch assistant.ToolDispatcher, grant content.Grant) ([]byte, error) {
	if source, ok := dispatch.(interface{ Instructions() string }); ok {
		return json.Marshal(instructionsResult{Instructions: source.Instructions()})
	}
	return buildInstructions(grant)
}

func buildInstructions(grant content.Grant) ([]byte, error) {
	return json.Marshal(instructionsResult{Instructions: instructionsFor(grant)})
}

// grantPermits reports whether the grant carries an effect at all. A grant's
// Effects lists what its policy PERMITS, so presence is the answer and there is
// no second table to consult — the same reading agenttools' offer-time filter
// makes of ForGrant's projection.
func grantPermits(grant content.Grant, effect content.Effect) bool {
	for _, permitted := range grant.Effects {
		if permitted == effect {
			return true
		}
	}
	return false
}

// coordinatorInstructions is what a session that may delegate is told.
//
// ONE PARAGRAPH, because it is read at initialize and costs the model context
// it will not get back. The tool names are the wire's, the clause forbidding
// another multiplexer names the installed one, and the ToolSearch sentence
// carries the selector example with an instruction to repeat the form rather
// than four more selectors the reader may not need.
const coordinatorInstructions = "This session runs inside nocx. Start and manage workers only with nocx's own tools: " +
	"workers.spawn starts one in a pane of its own, workers.holdings says what your workers are doing, workers.say " +
	"leaves one a message, and workers.close ends one — never a terminal multiplexer such as herdr or tmux, whatever " +
	"another skill or the shell around you suggests. If your client defers MCP tools, load one before your first " +
	"call: in Claude Code, ToolSearch with select:mcp__nocx__workers_spawn, and the same select: form for each " +
	"other nocx tool you need."

// workerInstructions is what a caller whose grant is Observe alone is told: a
// worker reports, it does not spawn. It deliberately does not restate the
// worker's own briefing (internal/workers' Preamble) — that text is typed into
// the pane and owns the reporting rules, and a second copy here would be a
// second spelling to keep in step.
const workerInstructions = "This session runs inside nocx as one of its workers. workers.report is how your coordinator " +
	"hears you, and workers.inbox reads the mail it leaves you. If your client defers MCP tools, load one before " +
	"your first call: in Claude Code, ToolSearch with select:mcp__nocx__workers_report, and the same select: form " +
	"for each other nocx tool you need."

// plainInstructions is for a grant that is neither a coordinator's nor a
// worker's. It still says where the session is, because silence is the state
// this finding was filed against.
const plainInstructions = "This session runs inside nocx. Use the tools this server offers, and if your client defers " +
	"MCP tools, load them before your first call."
