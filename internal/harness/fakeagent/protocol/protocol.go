// Package protocol is the JSON-lines protocol between the fake agent and the
// fake connector. It is test support: a real connector reads its own agent's
// documented interface, and this one exists so the conformance suite can make
// an agent stream, call a tool, hang, fail and exit on cue.
package protocol

import "github.com/shady2k/nocx/internal/harness"

// Script is what the fake agent does on each prompt. It arrives in the
// FAKEAGENT_SCRIPT environment variable as JSON.
type Script struct {
	// ProbeEnvironment adds agent environment facts to calls to the conformance probe tool.
	ProbeEnvironment bool `json:"probeEnvironment,omitempty"`
	// IgnoreAbort makes the actor exercise its shutdown bound.
	IgnoreAbort      bool     `json:"ignoreAbort,omitempty"`
	Turns            [][]Step `json:"turns,omitempty"`
	DefaultModelFile string   `json:"defaultModelFile,omitempty"`
	PromptFile       string   `json:"promptFile,omitempty"`
	EnvironmentFile  string   `json:"environmentFile,omitempty"`
	CleanupGate      string   `json:"cleanupGate,omitempty"`
	HangFile         string   `json:"hangFile,omitempty"`
	Gate             string   `json:"gate,omitempty"`
	Steps            []Step   `json:"steps"`
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
	// Decision answers each quiet-running command renewal in StepWaitRun.
	Decision string `json:"decision,omitempty"`
	// Text is the delta, for StepAnswer and StepReasoning.
	Text string `json:"text,omitempty"`
	// Name and Arguments describe the tool call, for StepTool.
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	// FailOnError ends the turn with the actual MCP tool error text.
	FailOnError bool `json:"failOnError,omitempty"`
	// ArgumentsFromResult copies JSON fields from the preceding tool-output.
	ArgumentsFromResult map[string]string `json:"argumentsFromResult,omitempty"`
	// ArgumentOffsets adds a numeric offset after resolving a result argument.
	ArgumentOffsets map[string]float64 `json:"argumentOffsets,omitempty"`
	// ArgumentsFromPrompt copies facts the agent learned from its prompt.
	// The supported fact is attachedItemID (the first attached terminal item).
	ArgumentsFromPrompt map[string]string `json:"argumentsFromPrompt,omitempty"`
	// ResultFile records this step's result separately from the script's last result.
	ResultFile string `json:"resultFile,omitempty"`
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
	// StepBridge starts an idle bridge and holds its stdin open.
	StepBridge StepKind = "bridge"
	// StepTool calls one tool through the turn's bridge, then writes the
	// result to ResultFile.
	StepTool StepKind = "tool"
	// StepWaitRun answers session.wait while the previous tool result says STILL RUNNING.
	StepWaitRun StepKind = "wait_run"
	// StepEnd emits the end event.
	StepEnd StepKind = "end"
	// StepFail emits a failure event.
	StepFail StepKind = "fail"
	// StepHang blocks until the process is stopped.
	StepHang StepKind = "hang"
	// StepGate waits for the suite to release a FIFO after observing a delta.
	StepGate StepKind = "gate"
	// StepExit exits with Code, without an end event unless one came first.
	StepExit StepKind = "exit"
	// StepChild starts a long-lived child in the same process group and
	// carries on, so the turn can leave a process behind.
	StepChild StepKind = "child"
)

// Line is one event the fake agent writes to stdout.
type Line struct {
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Model   string `json:"model,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// Usage is the fake agent's stated usage.
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
}

// Prompt is the input carried by a prompt command. It is
// the turn's input and bridge configuration; only the non-secret fixture
// script arrives through the environment.
type Prompt struct {
	Tools          harness.ToolBridge `json:"tools"`
	Dir            string             `json:"dir,omitempty"`
	ConversationID string             `json:"conversationId,omitempty"`
	Text           string             `json:"text,omitempty"`
	Model          string             `json:"model,omitempty"`
	SystemPrompt   string             `json:"systemPrompt,omitempty"`
}

// Session identifies the process incarnation acknowledged during setup.
type Session struct {
	ConversationID string `json:"conversationId"`
	Generation     uint64 `json:"generation"`
}

// Command carries a session command and the actor's correlation id.
type Command struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Prompt  *Prompt  `json:"prompt,omitempty"`
	Session *Session `json:"session,omitempty"`
}
