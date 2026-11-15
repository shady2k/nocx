package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

// The tool's refusal is a result, not a change to the declared catalogue.
// Grant enforcement itself is covered through the real endpoint in transport.
func TestRealPiOrderedInputAfterToolCycle(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "refused"
		if cancelled {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			defer close(gate)
			cycle := fakemodel.Response{Text: []string{"refusal explained"}}
			if cancelled {
				cycle = fakemodel.Response{Deltas: []fakemodel.Delta{{Text: "cancelled partial answer"}, {Text: "never delivered", Block: gate}}}
			}
			c, fake := configuredPi(t,
				fakemodel.Response{Text: []string{"completed first answer"}},
				fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "refused-call", Name: "mcp__nocx__prefix_probe", Arguments: `{"note":"request"}`}}},
				cycle,
				fakemodel.Response{Text: []string{"continued answer"}},
			)
			defer func() { _ = c.Close(context.Background()) }()
			observations := t.TempDir()
			installPiObserver(t, c, observations)
			source := `export default function(pi) {
 pi.registerTool({name:'mcp__nocx__prefix_probe', label:'Prefix probe', description:'Fixed prefix probe',
 parameters:{type:'object',properties:{note:{type:'string'}},required:['note']},
 async execute() { return {content:[{type:'text',text:'refused by policy'}],isError:true}; }});
}`
			if err := os.WriteFile(filepath.Join(piAgentDir(c.env), "extensions", "prefix-cycle.ts"), []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			spec := harness.TurnSpec{ConversationID: "ordered-cycle", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work/alpha host=local: first question"}
			piSuccess(t, c, spec)
			pid := piPID(t, observations)
			spec.Dir = t.TempDir()
			spec.Text = "pane=remote cwd=/srv/beta host=ssh.example: use the probe"
			turn := piTurn(t, c, spec)
			if cancelled {
				ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				// Cancel only after a completed tool cycle and an observed partial answer.
				for {
					select {
					case ev, ok := <-turn.Events():
						if !ok {
							t.Fatal("turn ended before partial answer")
						}
						if ev.Kind == harness.EventAnswerDelta && ev.Text == "cancelled partial answer" {
							goto cancelTurn
						}
					case <-ctx.Done():
						t.Fatal("partial answer not delivered")
					}
				}
			cancelTurn:
				if err := turn.Cancel(ctx); err != nil {
					t.Fatal(err)
				}
				piCollect(t, turn)
				if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
					t.Fatalf("cancel: %v", err)
				}
			} else {
				piCollect(t, turn)
				if _, err := turn.Wait(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			before := fake.Requests()
			if len(before) != 3 {
				t.Fatalf("tool cycle requests: %d", len(before))
			}
			// Assert whole ordered messages, including the refused call's ID, arguments
			// and result. Substrings would not detect a reordered or omitted result.
			var toolResult struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			}
			if err := json.Unmarshal(before[2].Messages[len(before[2].Messages)-1], &toolResult); err != nil {
				t.Fatal(err)
			}
			if toolResult.Role != "tool" || toolResult.Content != "refused by policy" || toolResult.ToolCallID != "refused-call" {
				t.Fatalf("refusal missing: %+v", toolResult)
			}
			spec.Dir = t.TempDir()
			spec.Text = "pane=third cwd=/opt/gamma host=other.example: continue"
			piSuccess(t, c, spec)
			after := fake.Requests()
			if len(after) != 4 {
				t.Fatalf("requests: %d", len(after))
			}
			if len(after[0].Tools) != 1 {
				t.Fatalf("nonempty fixed catalogue required: %s", after[0].Body)
			}
			for i := 1; i < len(after); i++ {
				if !bytes.Equal(after[0].Messages[0], after[i].Messages[0]) || !reflect.DeepEqual(after[0].Tools, after[i].Tools) {
					t.Fatalf("standing system/tool bytes changed at request %d", i)
				}
				if !piInputPrefixPreserved(after[i-1], after[i]) {
					t.Fatalf("ordered model input changed at request %d:\nbefore=%s\nafter=%s", i, after[i-1].Body, after[i].Body)
				}
			}
			if cancelled {
				if piPID(t, observations) == pid {
					t.Fatal("cancelled session was reused")
				}
				// ADR-anntnxdj decision 8 permits losing only the cancelled partial
				// answer. Every message sent before it, including the completed tool
				// call and result, must survive, followed by exactly the new question.
				expected := append(append([]json.RawMessage(nil), before[2].Messages...), json.RawMessage(`{"role":"user","content":[{"type":"text","text":"`+spec.Text+`"}]}`))
				if !equalPiMessages(expected, after[3].Messages) {
					t.Fatalf("cancel changed more than the partial answer:\nwant=%s\ngot=%s", expected, after[3].Messages)
				}
			} else {
				if piPID(t, observations) != pid {
					t.Fatal("normal refusal restarted the session")
				}
				expected := append(append([]json.RawMessage(nil), before[2].Messages...),
					json.RawMessage(`{"role":"assistant","content":"refusal explained"}`),
					json.RawMessage(`{"role":"user","content":[{"type":"text","text":"`+spec.Text+`"}]}`))
				if !equalPiMessages(expected, after[3].Messages) {
					t.Fatalf("completed refusal answer changed or lost:\nwant=%s\ngot=%s", expected, after[3].Messages)
				}
			}
		})
	}
}

func equalPiMessages(a, b []json.RawMessage) bool {
	decode := func(messages []json.RawMessage) []any {
		result := make([]any, len(messages))
		for i, message := range messages {
			if json.Unmarshal(message, &result[i]) != nil {
				return nil
			}
		}
		return result
	}
	return reflect.DeepEqual(decode(a), decode(b))
}
