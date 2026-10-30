package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestValidateToolCallIDPreservesUnicodeIdentityBounds(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{"ordinary", "call-1", true},
		{"surrounding spaces are identity bytes", " call-1 ", true},
		{"unicode boundary", strings.Repeat("界", 128), true},
		{"missing", "", false},
		{"whitespace", " \t\u2003\n", false},
		{"ascii over bound", strings.Repeat("x", 129), false},
		{"unicode over bound", strings.Repeat("界", 129), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateToolCallID(tc.id); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestAskInvalidToolCallIdentityRefusesWholeBatch(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"missing", ""},
		{"whitespace", "\u2003\t"},
		{"ascii over bound", strings.Repeat("x", 129)},
		{"unicode over bound", strings.Repeat("界", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant, dir := testDirGrant(t, autonomousMatrix())
			path := filepath.Join(dir, "a.txt")
			writeFile(t, path, "observable file bytes")
			args := fmt.Sprintf(`{"path":%q}`, path)
			f, srv := newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) {
				calls := []map[string]any{
					{"index": 0, "id": "valid-first", "type": "function", "function": map[string]any{"name": "files.read", "arguments": args}},
					{"index": 1, "type": "function", "function": map[string]any{"name": "files.read", "arguments": args}},
				}
				if tc.id != "" {
					calls[1]["id"] = tc.id
				}
				chunk, err := json.Marshal(map[string]any{
					"id": "completion-1", "object": "chat.completion.chunk", "model": "probe-model",
					"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": calls}, "finish_reason": "tool_calls"}},
				})
				if err != nil {
					panic(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			})
			t.Cleanup(srv.Close)
			cl, _, err := newTestClientAndRegistry(t, nil, content.Floor{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ledger := &fakeLedger{}
			announced := 0
			err = cl.Ask(t.Context(), askParams(srv.URL, &grant, ledger, NewApprovalStore()), func(event AskEvent) error {
				if event.Kind == AskToolCall {
					announced++
				}
				return nil
			})
			if !errors.Is(err, errInvalidToolCallID) {
				t.Fatalf("invalid original provider identity was not rejected: %v", err)
			}
			if announced != 0 || ledger.started() != 0 || f.requests.Load() != 1 {
				t.Fatalf("invalid sibling permitted earlier work: announced=%d attempts=%d requests=%d", announced, ledger.started(), f.requests.Load())
			}
		})
	}
}
