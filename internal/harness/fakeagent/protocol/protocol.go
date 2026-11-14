// Package protocol is the JSON-lines protocol between the fake agent and the
// fake connector. It is test support: a real connector reads its own agent's
// documented interface, and this one exists so the conformance suite can make
// an agent stream, call a tool, hang, fail and exit on cue.
package protocol

// Script is what the fake agent does in one turn. It arrives in the
// FAKEAGENT_SCRIPT environment variable as JSON.
type Script struct {
	Steps []Step `json:"steps"`
	// PIDFile, when set, receives the fake agent's own process id.
	PIDFile string `json:"pidFile,omitempty"`
	// ChildPIDFile, when set, receives the pid of a child a StepChild left
	// behind.
	ChildPIDFile string `json:"childPidFile,omitempty"`
	// ResultFile, when set, receives the text one StepTool's call returned.
	ResultFile string `json:"resultFile,omitempty"`
	// IgnoreStdinEOF makes the agent deaf to its stdin closing. It is how a
	// check proves Cancel stops the group rather than merely closing the
	// polite pipe: this agent leaves only on a signal.
	IgnoreStdinEOF bool `json:"ignoreStdinEOF,omitempty"`
}

// Step is one instruction to the fake agent.
type Step struct {
	Kind StepKind `json:"kind"`
	// Text is the delta, for StepAnswer and StepReasoning.
	Text string `json:"text,omitempty"`
	// Name and Arguments describe the tool call, for StepTool.
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	// Model and Usage are what StepEnd states.
	Model string `json:"model,omitempty"`
	Usage *Usage `json:"usage,omitempty"`
	// Reason is what StepFail states.
	Reason string `json:"reason,omitempty"`
	// Code is what StepExit exits with.
	Code int `json:"code,omitempty"`
}

// StepKind is the closed set of instructions.
type StepKind string

const (
	// StepAnswer streams one answer delta.
	StepAnswer StepKind = "answer"
	// StepReasoning streams one reasoning delta.
	StepReasoning StepKind = "reasoning"
	// StepTool calls one tool through the turn's bridge, then writes the
	// result to ResultFile.
	StepTool StepKind = "tool"
	// StepEnd emits the end event.
	StepEnd StepKind = "end"
	// StepFail emits a failure event.
	StepFail StepKind = "fail"
	// StepHang blocks until the process is stopped.
	StepHang StepKind = "hang"
	// StepExit exits with Code, without an end event unless one came first.
	StepExit StepKind = "exit"
	// StepChild starts a long-lived child in the same process group and
	// carries on, so the turn can leave a process behind.
	StepChild StepKind = "child"
)

// Line is one event the fake agent writes to stdout.
type Line struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	Usage  *Usage `json:"usage,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Usage is the fake agent's stated usage.
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
}

// Prompt is the one line a connector writes to the fake agent's stdin. It is
// the turn's input; the script stays in the environment because it is the
// fixture, not the conversation.
type Prompt struct {
	ConversationID string `json:"conversationId,omitempty"`
	Text           string `json:"text,omitempty"`
	Model          string `json:"model,omitempty"`
	SystemPrompt   string `json:"systemPrompt,omitempty"`
}
