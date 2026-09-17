package shellintegration

// Why a pane's agent gets no nocx tools, in a closed set.
//
// The owner's decision of 2026-09-14 draws the line: agent orchestration works
// on a machine where a nocx helper is INSTALLED, because the bridge the agent
// runs is that helper (`nocx-helper mcp`), the integration bundle carries no
// executable, and nothing else may put one there. So a pane whose shell runs on
// a host with no helper — an ssh pane this machine's helper carries, and any
// nested `ssh` — has no tool surface at all, and the honest thing to do is to
// SAY SO rather than leave a shell reporting a path it was never given.
//
// # Why a CODE crosses and the sentence does not
//
// The launch carries a code, and the SHELL renders the sentence a person reads
// (nocx.bash's own stage reason). Two owners of one sentence would be two
// answers to "why has my agent no tools", and the shell is the party that
// reports: it knows whether it is holding a path, whether the launch named one,
// and whether it failed for a reason of its own (an unwritable launch
// directory) rather than nocx's. What the launch adds is the one fact the shell
// cannot see — WHY nocx named nothing — and a code is the narrowest way to say
// it.
//
// The set is closed on purpose: a free-form string here would be a sentence
// written in Go and printed by bash, and the two would drift.

// AgentToolsAbsentEnvVar is the variable a launch names when this pane's agent
// has no tool surface. Like the other agent variables it is a NAME, not a
// secret: it is exported into the far shell's environment and read by the
// stage.
const AgentToolsAbsentEnvVar = "NOCX_AGENT_TOOLS_ABSENT"

// AgentToolsAbsent is one reason a pane's agent gets no nocx tools.
type AgentToolsAbsent string

// AgentToolsNoHelperOnHost is the only reason this product has today: the host
// the pane's shell runs on has no nocx helper installed, so there is no bridge
// for the agent to run and no socket for it to dial. It is what an ssh pane
// THIS machine's helper carries is — the far host's own helper declined or is
// not installed — and what a nested `ssh` is by construction (nocx neither
// installs on nor dials the host the user's own ssh reaches).
const AgentToolsNoHelperOnHost AgentToolsAbsent = "no-helper-on-host"

// Known reports whether a code is one this build's shells understand. It exists
// for the wire boundary: a helper generation is not the party that decides what
// a launch means, and a code from a newer coordinator must be refused rather
// than rendered as an empty sentence.
func (a AgentToolsAbsent) Known() bool { return a == AgentToolsNoHelperOnHost }

// String is the wire spelling, and it is the code itself: the value crosses as
// the code and is never translated here.
func (a AgentToolsAbsent) String() string { return string(a) }
