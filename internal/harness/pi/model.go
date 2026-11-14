package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/harness"
)

// modelSelection validates the selection in the process that will answer.
// Pi's CLI model resolver can fuzzy-match; RPC set_model is exact.
type modelSelection struct {
	selected  string
	responses chan string
	decode    harness.Decoder
}

func newModelSelection(selected string, decode harness.Decoder) *modelSelection {
	return &modelSelection{selected: selected, responses: make(chan string, 2), decode: decode}
}

func (s *modelSelection) Decode(line []byte) ([]harness.Event, error) {
	var response struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Model *struct {
				Provider string `json:"provider"`
				ID       string `json:"id"`
			} `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, err
	}
	if response.Type != "response" || (response.Command != "set_model" && response.Command != "get_state") {
		return s.decode(line)
	}
	if !response.Success {
		return s.failure(response.Error)
	}
	if response.Command == "get_state" {
		m := response.Data.Model
		if m == nil || m.Provider+"/"+m.ID != s.selected {
			return s.failure("the answering process selected a different model")
		}
	}
	s.responses <- response.Command
	return nil, nil
}

func (s *modelSelection) failure(reason string) ([]harness.Event, error) {
	// Route the failure through the same terminal owner as all other events.
	raw, _ := json.Marshal(map[string]any{"type": "response", "command": "set_model", "success": false, "error": fmt.Sprintf("model %q is not in pi's catalogue or could not be selected exactly: %s", s.selected, reason)})
	return s.decode(raw)
}

func (s *modelSelection) prompt(t *turn, text string) {
	provider, model, ok := strings.Cut(s.selected, "/")
	if !ok {
		provider = ""
		model = s.selected
	}
	for _, command := range []map[string]string{
		{"type": "set_model", "provider": provider, "modelId": model},
		{"type": "get_state"},
	} {
		if err := t.writeActiveCommand(command); err != nil {
			if !errors.Is(err, harness.ErrCancelled) {
				_ = t.Process.Cancel(context.Background())
			}
			return
		}
		select {
		case <-s.responses:
		case <-t.done:
			return
		}
	}
	t.startPrompt(text)
}
