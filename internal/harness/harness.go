// Package harness is the one contract between nocx-server and an agent it runs.
//
// ADR-anntnxdj decision 1: every agent is an implementation of this contract,
// and the core knows nothing else. A connector supplies what its agent's own
// interface requires; this package supplies what every connector needs — the
// process and its pipes, the turn's lifetime, and the event vocabulary the
// product reads. Nothing outside a connector names an agent.
//
// One agent process per turn. The turn is the unit of the process, its MCP
// bridge, its bearer and its lifetime, so a turn ending closes all of it. A
// turn's success is never read from one event: Wait is the authority, and a
// process that exits non-zero or ends without its end event is a failure.
package harness

import "context"

// Harness runs one agent. It is the whole of what the core and the renderer
// see; a connector implements it for one agent family.
type Harness interface {
	// Readiness reports whether this agent can run a turn now, and what to
	// tell a person when it cannot.
	Readiness(ctx context.Context) Readiness
	// Run starts one turn. The returned Turn owns the agent process; it is
	// the caller's to cancel and to wait for.
	Run(ctx context.Context, spec TurnSpec) (Turn, error)
}

// Turn is one agent process and its ordered event stream.
type Turn interface {
	// Events is the turn's stream. It is ordered, closes exactly once, and
	// carries one terminal event (an end or a failure) before it closes. The
	// channel must be drained: the caller that stops reading stops the turn's
	// progress through it.
	Events() <-chan Event
	// Cancel stops the turn and everything it started. It is safe to call
	// after the turn ended.
	Cancel(ctx context.Context) error
	// Wait returns only once the process and anything it started are gone.
	// Its result is the turn's truth: a non-zero exit or a stream that ended
	// without an end event is an error, whatever the last event said.
	Wait(ctx context.Context) (Outcome, error)
}

// Steerer is an optional capability: an agent that accepts a person's message
// while a turn is running. Asked of a connector, never inferred from its name.
type Steerer interface {
	Steer(ctx context.Context, text string) error
}

// Cataloguer is an optional capability: the models an agent can run, for the
// role picker. A connector without it runs on the agent's own default.
type Cataloguer interface {
	Models(ctx context.Context) ([]Model, error)
}

// EventKind names one kind of event in a turn's ordered stream. The set is
// closed: a new kind is a contract change, because the block draws each one
// differently.
type EventKind string

const (
	// EventAnswerDelta is a chunk of the answer — the model's own prose, and
	// the only thing that is the answer.
	EventAnswerDelta EventKind = "answer_delta"
	// EventReasoningDelta is a chunk of the model's thinking. Its own kind
	// because an answer that concatenates thinking with prose is not the
	// answer.
	EventReasoningDelta EventKind = "reasoning_delta"
	// EventToolStarted is a tool call the agent is about to make.
	EventToolStarted EventKind = "tool_started"
	// EventToolFinished is that call's completion. The result itself belongs
	// to the tool endpoint's record, not to this stream.
	EventToolFinished EventKind = "tool_finished"
	// EventEnd is the agent's own end of a successful turn, with the model
	// that answered and its usage when the agent states one.
	EventEnd EventKind = "end"
	// EventFailure is a turn that failed, with the reason a person can read.
	EventFailure EventKind = "failure"
)

// Event is one event of a turn's ordered stream.
type Event struct {
	Kind EventKind
	// Text is the delta, for EventAnswerDelta and EventReasoningDelta.
	Text string
	// Tool is the call, for EventToolStarted and EventToolFinished.
	Tool *ToolCall
	// End is the end facts, for EventEnd.
	End *End
	// Failure is the reason, for EventFailure.
	Failure *Failure
}

// ToolCall identifies one tool call by the name the agent used and the id the
// model gave it. The id is what lets a started call and its finish be one
// thing on screen.
type ToolCall struct {
	Name string
	ID   string
}

// End is what the agent states when a turn succeeds.
type End struct {
	// Model is the model that actually answered. It is what the block names,
	// and what a conversation pins.
	Model string
	// Usage is what the agent reported, or nil when it reported none. nil is
	// not zero: an agent that says nothing about usage shows nothing rather
	// than a made-up zero.
	Usage *Usage
}

// Usage is what an agent states about one turn's model calls.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Failure is a turn that did not succeed, as the agent stated it or as the
// shared base concluded from the process.
type Failure struct {
	Reason string
}

// ReadinessState is the closed set a person's Settings sees for one agent.
type ReadinessState string

const (
	// NotInstalled means the agent program was not found.
	NotInstalled ReadinessState = "not_installed"
	// NotConfigured means the agent is installed but cannot run a turn yet —
	// for pi, no provider is configured.
	NotConfigured ReadinessState = "not_configured"
	// Ready means a turn can run now.
	Ready ReadinessState = "ready"
)

// Readiness is whether an agent can run a turn, and the sentence a person
// needs to fix it when it cannot.
type Readiness struct {
	State   ReadinessState
	Message string
}

// Model is one model an agent can run, as its catalogue names it.
type Model struct {
	ID   string
	Name string
}

// Outcome is what Wait reports for a successful turn.
type Outcome struct {
	Model string
	Usage *Usage
}

// TurnSpec is one turn's input. Everything that belongs to one turn lives
// here; nocx's system prompt does not, because it is the same bytes on every
// turn and the cache prefix depends on that.
type TurnSpec struct {
	// ConversationID names the conversation whose agent session this turn
	// continues.
	ConversationID string
	// Text is the person's message.
	Text string
	// Model is the chosen model, or empty for the agent's own default.
	Model string
	// SystemPrompt is nocx's system prompt, with the person's own
	// instructions already in it. A connector appends it to the agent's own
	// prompt rather than replacing it.
	SystemPrompt string
	// Dir is the working directory the agent runs in.
	Dir string
	// Tools is how this turn's tools reach the agent.
	Tools ToolBridge
}

// ToolBridge is the turn's one way to nocx's tools: the argv the agent's MCP
// server entry launches, and the variables added to THAT child's environment
// only. The bearer lives in Env; the agent itself must never see it.
type ToolBridge struct {
	Command []string
	Env     []string
}
