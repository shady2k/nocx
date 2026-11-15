package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

// The steer is pi's own: one command carrying the person's message, answered
// with "queued".
func TestCodecSteerIsPiSteerWithTheMessage(t *testing.T) {
	connector := newTestConnector(t.TempDir(), nil)
	launch, err := connector.Start(context.Background(), harness.TurnSpec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Cleanup() })
	codec := connector.NewCodec(harness.SessionInfo{}, launch)
	command, ok := codec.SteerCommand("check the second log too")
	if !ok {
		t.Fatal("pi has a steer and the codec says it has not")
	}
	wire := codecWire(t, codec, "actor-steer-3", command)
	if wire["message"] != "check the second log too" {
		t.Fatalf("steer wire = %+v", wire)
	}
}

// A person's message typed while pi answers reaches pi before its NEXT model
// call: the first call is held mid-stream, the message is accepted while it is
// open, and the second call's input carries it.
func TestRealPiSteerReachesTheNextModelCall(t *testing.T) {
	gate := make(chan struct{})
	c, fake := configuredPi(t,
		fakemodel.Response{
			Deltas:    []fakemodel.Delta{{Text: "reading the logs "}, {Text: "first", Block: gate}},
			ToolCalls: []fakemodel.ToolCall{{ID: "probe-call", Name: "mcp__nocx__steer_probe", Arguments: `{"note":"probe"}`}},
		},
		fakemodel.Response{Text: []string{"the answer after the steer"}},
	)
	defer func() { _ = c.Close(context.Background()) }()
	source := `export default function(pi) {
 pi.registerTool({name:'mcp__nocx__steer_probe', label:'Steer probe', description:'Fixed steer probe',
 parameters:{type:'object',properties:{note:{type:'string'}},required:['note']},
 async execute() { return {content:[{type:'text',text:'probe result'}],isError:false}; }});
}`
	extensions := filepath.Join(piAgentDir(c.env), "extensions")
	if err := os.MkdirAll(extensions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensions, "steer.ts"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	spec := harness.TurnSpec{ConversationID: "steer", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work: look at the logs"}
	turn := piTurn(t, c, spec)
	events := make(chan harness.Event, 256)
	go func() {
		defer close(events)
		for ev := range turn.Events() {
			events <- ev
		}
	}()
	// The first model call is open and has said something: this is the moment
	// a person types.
	var received []harness.Event
	for streamed := false; !streamed; {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("the turn ended before the first answer arrived: %+v", received)
			}
			received = append(received, ev)
			streamed = ev.Kind == harness.EventAnswerDelta && ev.Text == "reading the logs "
		case <-ctx.Done():
			t.Fatal("the first model call never streamed")
		}
	}
	steerer, ok := turn.(harness.Steerer)
	if !ok {
		t.Fatal("pi's turn has no Steer")
	}
	const message = "also check the second log"
	if err := steerer.Steer(ctx, message); err != nil {
		t.Fatalf("steer while pi answers: %v", err)
	}
	close(gate)
	for ev := range events {
		received = append(received, ev)
	}
	outcome, err := turn.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v events=%+v", err, received)
	}
	if outcome.Model != "nocx-test/test-model" {
		t.Fatalf("outcome = %+v", outcome)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want the first and the one after the steer", len(requests))
	}
	if strings.Contains(string(requests[0].Body), message) {
		t.Fatal("the message was in the first model call, which ran before the person typed")
	}
	if !strings.Contains(string(requests[1].Body), message) {
		t.Fatalf("the next model call does not carry the person's message:\n%s", requests[1].Body)
	}
	// It carries it as the person wrote it: one user message at the end of the
	// next call's history, after the tool result the call waited on.
	var last struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(requests[1].Messages[len(requests[1].Messages)-1], &last); err != nil {
		t.Fatal(err)
	}
	if last.Role != "user" || !strings.Contains(string(last.Content), message) {
		t.Fatalf("last message of the next model call = %s (%s)", last.Content, last.Role)
	}
}
