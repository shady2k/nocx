package pi

import (
	"encoding/json"

	"github.com/shady2k/nocx/internal/harness"
)

type message struct {
	Role         string `json:"role"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
	Content      []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
	} `json:"usage"`
}

func newDecoder() func([]byte) ([]harness.Event, error) {
	var last message
	var usage *harness.Usage
	return func(line []byte) ([]harness.Event, error) {
		var record struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Data    struct {
				Disposition string `json:"disposition"`
			} `json:"data"`
			Aborted    bool            `json:"aborted"`
			Success    *bool           `json:"success"`
			Error      string          `json:"error"`
			Message    json.RawMessage `json:"message"`
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Update     struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, err
		}
		failure := func(reason string) ([]harness.Event, error) {
			return []harness.Event{{Kind: harness.EventFailure, Failure: &harness.Failure{Reason: reason}}}, nil
		}
		switch record.Type {
		case "response":
			if record.Command == "prompt" && record.Success != nil && *record.Success && record.Data.Disposition == "handled" {
				return failure("pi extension handled the prompt without an answer")
			}
			if record.Success != nil && !*record.Success {
				return failure(record.Error)
			}
		case "extension_error":
			return failure(record.Error)
		case "message_update":
			switch record.Update.Type {
			case "text_delta":
				return []harness.Event{{Kind: harness.EventAnswerDelta, Text: record.Update.Delta}}, nil
			case "thinking_delta":
				return []harness.Event{{Kind: harness.EventReasoningDelta, Text: record.Update.Delta}}, nil
			}
		case "tool_execution_start", "tool_execution_end":
			kind := harness.EventToolStarted
			if record.Type == "tool_execution_end" {
				kind = harness.EventToolFinished
			}
			return []harness.Event{{Kind: kind, Tool: &harness.ToolCall{Name: record.ToolName, ID: record.ToolCallID}}}, nil
		case "message_end":
			var role struct {
				Role string `json:"role"`
			}
			if err := json.Unmarshal(record.Message, &role); err != nil {
				return nil, err
			}
			if role.Role == "assistant" {
				if err := json.Unmarshal(record.Message, &last); err != nil {
					return nil, err
				}
				if u := last.Usage; u != nil {
					if usage == nil {
						usage = &harness.Usage{}
					}
					usage.InputTokens += u.Input
					usage.OutputTokens += u.Output
					usage.CacheReadTokens += u.CacheRead
					usage.CacheWriteTokens += u.CacheWrite
				}
			}
		case "agent_settled":
			if record.Aborted {
				return failure("pi aborted the turn")
			}
			if last.ErrorMessage != "" {
				return failure(last.ErrorMessage)
			}
			if last.StopReason != "stop" && last.StopReason != "length" {
				return failure("pi ended without a completed answer")
			}
			answer := false
			for _, block := range last.Content {
				if block.Type == "text" && block.Text != "" {
					answer = true
				}
			}
			if !answer {
				return failure("pi ended without an answer")
			}
			model := last.Model
			if last.Provider != "" {
				model = last.Provider + "/" + model
			}
			return []harness.Event{{Kind: harness.EventEnd, End: &harness.End{Model: model, Usage: statedUsage(usage)}}}, nil
		}
		return nil, nil
	}
}

// statedUsage drops a usage pi filled with zeros. pi 1.5.2 reports
// {input:0, output:0, cacheRead:0, cacheWrite:0} when its provider sent no
// usage at all, and an answer that ended normally cannot have taken zero
// input and produced zero output, so all zeros means "not stated" and the
// block shows nothing rather than three zeros.
func statedUsage(u *harness.Usage) *harness.Usage {
	if u == nil || (u.InputTokens == 0 && u.OutputTokens == 0) {
		return nil
	}
	return u
}
