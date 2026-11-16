package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/waittest"
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

// A message the agent has taken but not yet used when the turn is CANCELLED is
// handed back to the caller, which asks it as the conversation's next question
// — the session that held it ends with the cancel (ADR-4khgzeyp decision 3).
// The message is in the actor's queue — sent before pi has even come up — so
// the agent has certainly not consumed it.
func TestRealPiCancelWhileASteerIsQueuedHandsItBack(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	c, fake := configuredPi(t, fakemodel.Response{Text: []string{"first answer"}})
	defer func() { _ = c.Close(context.Background()) }()
	begun := make(chan struct{})
	queued := make(chan struct{})
	var once, queuedOnce sync.Once
	// The same conversation store, with an observation of the actor's own
	// transitions: "begin-turn" is the window in which the agent is coming up
	// and a person's message is queued for it.
	c = NewWithBase(c.dataDir, c.env, harness.Base{Observe: func(transition harness.Transition) {
		switch transition.Kind {
		case "begin-turn":
			once.Do(func() { close(begun) })
		case "steer-queued":
			queuedOnce.Do(func() { close(queued) })
		}
	}})
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	spec := harness.TurnSpec{ConversationID: "queued-steer", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work: look at the logs"}
	turn, err := c.Run(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-begun:
	case <-ctx.Done():
		t.Fatal("the turn never began")
	}
	steerer, ok := turn.(harness.Steerer)
	if !ok {
		t.Fatal("pi's turn has no Steer")
	}
	steered := make(chan error, 1)
	go func() { steered <- steerer.Steer(ctx, "ask this next instead") }()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("the message was never queued for the turn")
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case err := <-steered:
		if !errors.Is(err, harness.ErrSteerNotTaken) {
			t.Fatalf("a message the cancelled turn never took = %v, want ErrSteerNotTaken", err)
		}
	case <-ctx.Done():
		t.Fatal("the queued message was never answered")
	}

	// The caller asks it as the next question, and the conversation answers.
	const message = "ask this next instead"
	spec.Text = "pane=local cwd=/work: " + message
	next := piTurn(t, c, spec)
	events := piCollect(t, next)
	if _, err := next.Wait(ctx); err != nil {
		t.Fatalf("the next turn: %v events=%+v", err, events)
	}
	requests := fake.Requests()
	if len(requests) == 0 {
		t.Fatal("the question after the cancel never reached the model")
	}
	// The cancelled turn was stopped before its model call in most runs, so what
	// matters is not how many calls there were but that the LAST one is the
	// question the caller asked with the message.
	last := requests[len(requests)-1]
	if !strings.Contains(string(last.Body), message) {
		t.Fatalf("the last model call does not carry the message the turn handed back:\n%s", last.Body)
	}
}

// Normally the message is consumed inside the turn: no extra turn is asked, and
// the text never becomes a question of its own.
func TestRealPiSteerIsConsumedInsideTheTurn(t *testing.T) {
	gate := make(chan struct{})
	c, fake := configuredPi(t,
		fakemodel.Response{
			Deltas:    []fakemodel.Delta{{Text: "reading the logs "}, {Text: "first", Block: gate}},
			ToolCalls: []fakemodel.ToolCall{{ID: "probe-call", Name: "mcp__nocx__steer_probe", Arguments: `{"note":"probe"}`}},
		},
		fakemodel.Response{Text: []string{"the answer after the message"}},
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
	spec := harness.TurnSpec{ConversationID: "consumed-steer", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work: look at the logs"}
	turn := piTurn(t, c, spec)
	events := make(chan harness.Event, 256)
	go func() {
		defer close(events)
		for ev := range turn.Events() {
			events <- ev
		}
	}()
	for streamed := false; !streamed; {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the turn ended before the first answer arrived")
			}
			streamed = ev.Kind == harness.EventAnswerDelta && ev.Text == "reading the logs "
		case <-ctx.Done():
			t.Fatal("the first model call never streamed")
		}
	}
	steerer, ok := turn.(harness.Steerer)
	if !ok {
		t.Fatal("pi's turn has no Steer")
	}
	if err := steerer.Steer(ctx, "also check the second log"); err != nil {
		t.Fatalf("steer while pi answers: %v", err)
	}
	close(gate)
	for range events {
	}
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Consumed inside the turn: the agent asked the model ONCE MORE with the
	// message, and the caller was never handed it back to ask again.
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want exactly the two of one turn", len(requests))
	}
	if !strings.Contains(string(requests[1].Body), "also check the second log") {
		t.Fatal("the message was not carried into the turn")
	}
}

// WHAT pi DID NOT DELIVER, exactly.
//
// ADR-4khgzeyp decision 3: a message pi had QUEUED for steering when the turn
// was cancelled is not lost — it becomes the next question. pi's own
// clear_queue answers with the steering texts it removed, so that answer is the
// authority; nothing has to be inferred from timing.
func TestRealPiCancelDropsTheQueuedSteerAndHandsBackExactlyIt(t *testing.T) {
	gate := make(chan struct{})
	c, fake := configuredPi(t,
		fakemodel.Response{Deltas: []fakemodel.Delta{{Text: "reading the logs "}, {Text: "first", Block: gate}}},
		fakemodel.Response{Text: []string{"answered the message"}},
	)
	defer func() { _ = c.Close(context.Background()) }()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	spec := harness.TurnSpec{ConversationID: "dropped-steer", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work: look at the logs"}
	turn := piTurn(t, c, spec)
	events := make(chan harness.Event, 256)
	go func() {
		defer close(events)
		for ev := range turn.Events() {
			events <- ev
		}
	}()
	for streamed := false; !streamed; {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the turn ended before the first answer arrived")
			}
			streamed = ev.Kind == harness.EventAnswerDelta && ev.Text == "reading the logs "
		case <-ctx.Done():
			t.Fatal("the first model call never streamed")
		}
	}
	const message = "ask this next instead"
	steerer, ok := turn.(harness.Steerer)
	if !ok {
		t.Fatal("pi's turn has no Steer")
	}
	// pi takes it ("queued") and holds it for its next model call — which the
	// cancel below prevents.
	if err := steerer.Steer(ctx, message); err != nil {
		t.Fatalf("steer while pi answers: %v", err)
	}
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}

	handback, ok := turn.(harness.SteerHandback)
	if !ok {
		t.Fatal("a cancelled turn cannot hand back what the agent dropped")
	}
	dropped := handback.DroppedSteers()
	if len(dropped) != 1 || dropped[0] != message {
		t.Fatalf("dropped = %v, want exactly the message pi never delivered", dropped)
	}

	// The caller asks it as the conversation's next question, and it is
	// answered — the conversation continues after the cancelled session.
	spec.Text = "pane=local cwd=/work: " + message
	next := piTurn(t, c, spec)
	if _, err := next.Wait(ctx); err != nil {
		t.Fatalf("the next turn: %v", err)
	}
	requests := fake.Requests()
	if len(requests) < 2 || !strings.Contains(string(requests[len(requests)-1].Body), message) {
		t.Fatalf("the question after the cancel does not carry the message: %d request(s)", len(requests))
	}
}

// The other end of the same rule: a message pi DID deliver is inside the turn
// and is not asked again when the turn is then cancelled (ADR-4khgzeyp decision
// 3 protects what was never used, not what was).
func TestRealPiADeliveredSteerIsNotHandedBackByACancel(t *testing.T) {
	gate := make(chan struct{})
	// The second model call — the one that carries the message — is held on a
	// gate of its own, so the cancel lands while that call is open.
	afterGate := make(chan struct{})
	defer close(afterGate)
	c, fake := configuredPi(t,
		fakemodel.Response{
			Deltas: []fakemodel.Delta{{Text: "reading the logs "}, {Text: "first", Block: gate}},
		},
		fakemodel.Response{Deltas: []fakemodel.Delta{{Text: "the answer after the message"}, {Text: "…", Block: afterGate}}},
	)
	defer func() { _ = c.Close(context.Background()) }()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	spec := harness.TurnSpec{ConversationID: "delivered-steer", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work: look at the logs"}
	turn := piTurn(t, c, spec)
	events := make(chan harness.Event, 256)
	go func() {
		defer close(events)
		for ev := range turn.Events() {
			events <- ev
		}
	}()
	for streamed := false; !streamed; {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the turn ended before the first answer arrived")
			}
			streamed = ev.Kind == harness.EventAnswerDelta && ev.Text == "reading the logs "
		case <-ctx.Done():
			t.Fatal("the first model call never streamed")
		}
	}
	const message = "this one is delivered"
	steerer, ok := turn.(harness.Steerer)
	if !ok {
		t.Fatal("pi's turn has no Steer")
	}
	if err := steerer.Steer(ctx, message); err != nil {
		t.Fatalf("steer while pi answers: %v", err)
	}
	close(gate)
	// The SECOND model call is pi delivering the message: its body carries it.
	waittest.WaitFor(t, "pi to deliver the message into the next model call", func() bool {
		requests := fake.Requests()
		return len(requests) >= 2 && strings.Contains(string(requests[len(requests)-1].Body), message)
	})
	if err := turn.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if _, err := turn.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	handback, ok := turn.(harness.SteerHandback)
	if !ok {
		t.Fatal("a cancelled turn cannot hand back what the agent dropped")
	}
	if dropped := handback.DroppedSteers(); len(dropped) != 0 {
		t.Fatalf("dropped = %v, want nothing: pi had already delivered the message", dropped)
	}
}

// A turn that ENDS ON ITS OWN with a message pi delivered hands nothing back,
// and the message does not leak into the conversation's next question: pi used
// it inside the turn, which is exactly what a steered message is for
// (nocx-a62cl.6.3).
func TestRealPiATurnEndingOnItsOwnLeavesADeliveredSteerUsed(t *testing.T) {
	gate := make(chan struct{})
	c, fake := configuredPi(t,
		fakemodel.Response{Deltas: []fakemodel.Delta{{Text: "reading the logs "}, {Text: "first", Block: gate}}},
		fakemodel.Response{Text: []string{"the answer after the message"}},
		fakemodel.Response{Text: []string{"the second answer"}},
	)
	defer func() { _ = c.Close(context.Background()) }()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	spec := harness.TurnSpec{ConversationID: "natural-end", Dir: t.TempDir(), SystemPrompt: "Fixed nocx instructions", Text: "pane=local cwd=/work: look at the logs"}
	turn := piTurn(t, c, spec)
	events := make(chan harness.Event, 256)
	go func() {
		defer close(events)
		for ev := range turn.Events() {
			events <- ev
		}
	}()
	for streamed := false; !streamed; {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the turn ended before the first answer arrived")
			}
			streamed = ev.Kind == harness.EventAnswerDelta && ev.Text == "reading the logs "
		case <-ctx.Done():
			t.Fatal("the first model call never streamed")
		}
	}
	const message = "this one is delivered"
	steerer, ok := turn.(harness.Steerer)
	if !ok {
		t.Fatal("pi's turn has no Steer")
	}
	if err := steerer.Steer(ctx, message); err != nil {
		t.Fatalf("steer while pi answers: %v", err)
	}
	close(gate)
	// Let the turn END on its own: no cancel, no second question.
	for range events {
	}
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatalf("the turn: %v", err)
	}
	handback, ok := turn.(harness.SteerHandback)
	if !ok {
		t.Fatal("a turn cannot report what it handed back")
	}
	if dropped := handback.DroppedSteers(); len(dropped) != 0 {
		t.Fatalf("dropped = %v, want nothing: pi had already delivered the message", dropped)
	}

	// NOTHING LEAKS INTO THE NEXT TURN: the message is history there, once.
	spec.Text = "pane=local cwd=/work: and the second answer?"
	piSuccess(t, c, spec)
	requests := fake.Requests()
	if len(requests) != 3 {
		t.Fatalf("model calls = %d, want two for the steered turn and one for the next question", len(requests))
	}
	if count := strings.Count(string(requests[2].Body), message); count != 1 {
		t.Fatalf("the next question carries the message %d times, want once as history", count)
	}
}
