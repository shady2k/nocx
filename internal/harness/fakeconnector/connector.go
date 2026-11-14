// Package fakeconnector is the connector the conformance suite proves the
// shared base with. It speaks only the fake agent's JSON-lines protocol; every
// process and lifetime fact it relies on comes from harness.Base.
package fakeconnector

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
	cfg Config
}

// New builds the fake connector.
func New(cfg Config) *Connector { return &Connector{cfg: cfg} }

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

// Run starts one scripted turn.
func (c *Connector) Run(ctx context.Context, spec harness.TurnSpec) (harness.Turn, error) {
	script, err := json.Marshal(c.cfg.Script)
	if err != nil {
		return nil, fmt.Errorf("fakeconnector: script: %w", err)
	}
	tools, err := json.Marshal(spec.Tools)
	if err != nil {
		return nil, fmt.Errorf("fakeconnector: tool bridge: %w", err)
	}
	env := []string{
		"FAKEAGENT_SCRIPT=" + string(script),
		"FAKEAGENT_TOOLS=" + string(tools),
	}
	process, err := c.cfg.Base.Start(ctx, harness.Launch{
		Argv:   []string{c.cfg.AgentPath},
		Env:    env,
		Dir:    spec.Dir,
		Decode: decode,
	})
	if err != nil {
		return nil, err
	}
	// The turn's input goes over the agent's own stdin, the way a real
	// connector speaks its agent's protocol.
	if err := json.NewEncoder(process.Stdin()).Encode(protocol.Prompt{
		ConversationID: spec.ConversationID,
		Text:           spec.Text,
		Model:          spec.Model,
		SystemPrompt:   spec.SystemPrompt,
	}); err != nil {
		_ = process.Cancel(ctx)
		_, _ = process.Wait(ctx)
		return nil, fmt.Errorf("fakeconnector: the prompt: %w", err)
	}
	return process, nil
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
