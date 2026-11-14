// Package fakeconnector is the connector the conformance suite proves the
// shared base with. It speaks only the fake agent's JSON-lines protocol; every
// process and lifetime fact it relies on comes from harness.Base.
package fakeconnectortest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

// Config describes one fake agent.
type Config struct {
	// AgentPath is the fake agent program.
	AgentPath string
	// Script is what that program must do in every turn.
	Script protocol.Script
	// Base is the shared process ownership. Zero uses its defaults.
	Base harness.Base
}

// Connector implements harness.Harness on the fake agent.
type Connector struct {
	cfg     Config
	manager *harness.SessionManager
}

// New builds the fake connector.
func New(cfg Config) *Connector {
	c := &Connector{cfg: cfg}
	c.manager = harness.NewSessionManager(cfg.Base, c)
	return c
}

// Readiness reports the fake agent as installed when its program exists.
func (c *Connector) Readiness(ctx context.Context) harness.Readiness {
	if _, err := os.Stat(c.cfg.AgentPath); err != nil {
		return harness.Readiness{
			State:   harness.NotInstalled,
			Message: "the fake agent is not built",
		}
	}
	return harness.Readiness{State: harness.Ready, Message: "the fake agent is ready"}
}

// Facts states the fixture's actual limits: it forwards conversation IDs but
// does not retain conversations between processes.
func (c *Connector) Facts() harness.Facts {
	return harness.Facts{ToolBoundary: harness.NocxToolsOnly, ReportsAnsweringModel: true}
}

// Start describes the process without creating or owning it.
func (c *Connector) Start(ctx context.Context, spec harness.TurnSpec) (harness.Launch, error) {
	script, err := json.Marshal(c.cfg.Script)
	if err != nil {
		return harness.Launch{}, err
	}
	return harness.Launch{Argv: []string{c.cfg.AgentPath}, Env: []string{"FAKEAGENT_SCRIPT=" + string(script)}, Dir: spec.Dir}, nil
}

// NewCodec allocates wire state for one process incarnation.
func (c *Connector) NewCodec(info harness.SessionInfo) harness.Codec {
	return &codec{info: info}
}

type codec struct {
	info harness.SessionInfo
}

func (c *codec) Setup() []harness.Step {
	session := protocol.Session{ConversationID: c.info.ConversationID, Generation: c.info.Generation}
	return []harness.Step{{Command: harness.Command{Name: "initialize", Payload: session}, Check: func(payload any) error {
		value, ok := payload.(map[string]any)
		if !ok || value["conversationId"] != session.ConversationID || value["generation"] != float64(session.Generation) {
			return fmt.Errorf("fakeconnector: invalid initialize response: %v", payload)
		}
		return nil
	}}}
}

func (c *codec) BeginTurn(spec harness.TurnSpec) []harness.Step {
	return []harness.Step{
		{Command: harness.Command{Name: "get_state"}, Barrier: true},
		{Command: harness.Command{Name: "prompt", Payload: protocol.Prompt{ConversationID: spec.ConversationID, Text: spec.Text, Model: spec.Model, SystemPrompt: spec.SystemPrompt, Tools: spec.Tools}}, Prompt: true},
	}
}

func (c *codec) CancelSequence() []harness.Command { return []harness.Command{{Name: "abort"}} }

func (c *codec) Encode(id string, command harness.Command) ([]byte, error) {
	wire := protocol.Command{ID: id, Name: command.Name}
	switch command.Name {
	case "initialize":
		session, ok := command.Payload.(protocol.Session)
		if !ok {
			return nil, fmt.Errorf("fakeconnector: invalid initialize payload")
		}
		wire.Session = &session
	case "prompt":
		prompt, ok := command.Payload.(protocol.Prompt)
		if !ok {
			return nil, fmt.Errorf("fakeconnector: invalid prompt payload")
		}
		wire.Prompt = &prompt
	}
	raw, err := json.Marshal(wire)
	return append(raw, '\n'), err
}

func (c *codec) Decode(line []byte) (harness.Decoded, error) {
	var wire protocol.Line
	if err := json.Unmarshal(line, &wire); err != nil {
		return harness.Decoded{}, err
	}
	if wire.Kind == "response" {
		return harness.Decoded{Response: &harness.Response{ID: wire.ID, Error: wire.Reason, Payload: wire.Payload}}, nil
	}
	events, err := decode(line)
	return harness.Decoded{Events: events}, err
}

func (c *Connector) Run(ctx context.Context, spec harness.TurnSpec) (harness.Turn, error) {
	return c.manager.Run(ctx, spec)
}
func (c *Connector) Close(ctx context.Context) error { return c.manager.Close(ctx) }
func (c *Connector) CloseConversation(ctx context.Context, id string) error {
	return c.manager.CloseConversation(ctx, id)
}

// decode reads the fake agent's one event per line.
func decode(line []byte) ([]harness.Event, error) {
	var wire protocol.Line
	if err := json.Unmarshal(line, &wire); err != nil {
		return nil, err
	}
	switch wire.Kind {
	case "answer":
		return []harness.Event{{Kind: harness.EventAnswerDelta, Text: wire.Text}}, nil
	case "reasoning":
		return []harness.Event{{Kind: harness.EventReasoningDelta, Text: wire.Text}}, nil
	case "tool_started":
		return []harness.Event{{Kind: harness.EventToolStarted, Tool: &harness.ToolCall{ID: wire.ID, Name: wire.Name}}}, nil
	case "tool_finished":
		return []harness.Event{{Kind: harness.EventToolFinished, Tool: &harness.ToolCall{ID: wire.ID, Name: wire.Name}}}, nil
	case "end":
		return []harness.Event{{Kind: harness.EventEnd, End: &harness.End{Model: wire.Model, Usage: usage(wire.Usage)}}}, nil
	case "failure":
		return []harness.Event{{Kind: harness.EventFailure, Failure: &harness.Failure{Reason: wire.Reason}}}, nil
	default:
		return nil, fmt.Errorf("unknown event %q", wire.Kind)
	}
}

func usage(u *protocol.Usage) *harness.Usage {
	if u == nil {
		return nil
	}
	return &harness.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

var _ harness.Harness = (*Connector)(nil)
