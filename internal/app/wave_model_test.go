package app

// THE EPIC'S OWN CRITERION: the coordinator's calls come from a MODEL
// (nocx-dkawo.14).
//
// Every wave call was already asserted where it lives, and the sequence they
// drive was asserted on real sessions in real panes — by a test making the
// calls. That is what AGENTS.md rule 1 is about: a test written by reading
// the implementation cannot report a missing feature, it can only confirm
// that what was written does what it was written to do. Nothing had ever let
// an agent CHOOSE to spawn, wait and close.
//
// So this runs the real assistant engine — the shipped loop, the shipped tool
// registry, the shipped executors — against a provider that proposes tool
// calls, over the real wave record, opening real sessions in real panes.
//
// WHAT IT DOES NOT COVER, said here so nobody reads it as covering more. The
// provider is scripted rather than a model, so it shows that a model's calls
// move the record and not that any model would make them; that half is
// persuasion (§5) and has no test anywhere. And the launcher is stood in for:
// a real worker enrols over the authenticated channel from the shell bundle,
// which internal/shellintegration proves on a real pty in bash and zsh, and
// here a goroutine supplies the enrolment the launcher would have sent.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/wave"
)

// scriptedTurn is one thing the provider proposes.
type scriptedTurn struct {
	tool string
	args string
	// answer ends the run instead of proposing a tool.
	answer string
}

// scriptedProvider walks a script, one step per completion request, and keeps
// what it was handed back. Reading the results is the point: a provider that
// only proposed would prove the calls were made and nothing about what they
// answered.
type scriptedProvider struct {
	mu      sync.Mutex
	script  []scriptedTurn
	at      int
	results []string
	// then is consulted once the script runs out. It is handed the last
	// tool result so a step can ADDRESS what it was told about — which is
	// the whole point of an id in an answer, and the only way a scripted
	// provider can close workers whose ids are minted at runtime.
	then func(lastResult string) (scriptedTurn, bool)
}

func (p *scriptedProvider) serve(w http.ResponseWriter, r *http.Request) {
	body := readAll(r)
	p.mu.Lock()
	if strings.Contains(body, `"role":"tool"`) {
		p.results = append(p.results, body)
	}
	var turn scriptedTurn
	switch {
	case p.at < len(p.script):
		turn = p.script[p.at]
		p.at++
	case p.then != nil:
		last := ""
		if len(p.results) > 0 {
			last = p.results[len(p.results)-1]
		}
		if next, more := p.then(last); more {
			turn = next
		} else {
			turn = scriptedTurn{answer: "done"}
		}
	default:
		turn = scriptedTurn{answer: "done"}
	}
	p.mu.Unlock()

	if turn.answer != "" {
		streamModelAnswer(w, turn.answer)
		return
	}
	streamModelToolCall(w, turn.tool, turn.args)
}

// toolResults decodes every tool result the provider was handed, in order.
//
// It decodes rather than substring-matches the raw body: the results arrive
// inside a JSON string, so a search for a field name would be searching for
// its escaped form and would pass or fail for reasons that have nothing to do
// with the answer.
func (p *scriptedProvider) toolResults(t *testing.T) []string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.results) == 0 {
		return nil
	}
	// The last request carries the whole conversation, so one decode has
	// every result in it.
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(p.results[len(p.results)-1]), &body); err != nil {
		t.Fatalf("decode the provider's last request: %v", err)
	}
	var out []string
	for _, m := range body.Messages {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func readAll(r *http.Request) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// streamModelToolCall writes the streamed shape an openai-compatible provider
// uses to propose one tool call.
func streamModelToolCall(w http.ResponseWriter, name, args string) {
	frame := map[string]any{
		"id": "chatcmpl-wave", "object": "chat.completion.chunk", "model": "probe-model",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
				"index": 0, "id": "call-1", "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			}}},
			"finish_reason": "tool_calls",
		}},
	}
	streamFrames(w, frame)
}

func streamModelAnswer(w http.ResponseWriter, text string) {
	frame := map[string]any{
		"id": "chatcmpl-wave", "object": "chat.completion.chunk", "model": "probe-model",
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	}
	streamFrames(w, frame)
}

func streamFrames(w http.ResponseWriter, frames ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, f := range frames {
		b, _ := json.Marshal(f)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// launcherStandIn supplies the enrolment a real launcher would send, for
// every worker session that appears while the model is working.
//
// It stands in for exactly one thing and says so: the enrolment act itself is
// internal/shellintegration's, proved there on a real pty in both shells. A
// spawn blocks until an enrolment arrives, so without this the model's first
// tool call would sit out its whole deadline.
func launcherStandIn(t *testing.T, w *wakeStand, stop <-chan struct{}) *sync.WaitGroup {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		seen := map[session.ID]bool{w.coordinator: true}
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			for _, s := range w.reg.List() {
				if seen[s.ID()] {
					continue
				}
				seen[s.ID()] = true
				lane := lifecycle.LaneID("lane-" + string(s.ID()))
				w.lanes.register(lane, string(s.ID()))
				w.enrol.enrolled(s.ID(), string(lane))
			}
		}
	}()
	return &wg
}

// A MODEL SPAWNS THREE WORKERS, WAITS ON THE WAVE, AND CLOSES THEM.
//
// Nothing in this test calls the registrar. Every participant that exists
// exists because the engine executed a tool call the provider proposed, and
// every one that ends ends the same way.
func TestAModelSpawnsThreeWorkersWaitsOnTheWaveAndClosesThem(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)

	provider := &scriptedProvider{script: []scriptedTurn{
		{tool: "wave.spawn", args: `{"command":"claude","task":"read AGENTS.md"}`},
		{tool: "wave.spawn", args: `{"command":"claude","task":"read the architecture"}`},
		{tool: "wave.spawn", args: `{"command":"claude","task":"read the vision"}`},
		// One wait, on the wave and not on a worker.
		{tool: "wave.wait", args: `{"seconds":20}`},
		{tool: "wave.holdings", args: `{}`},
	}}
	// Then it closes what it was told is still running, BY THE IDS IT WAS
	// GIVEN. A script with the ids written into it would be addressing
	// workers nobody told it about, which is not what an id in an answer is
	// for.
	var toClose []string
	provider.then = func(last string) (scriptedTurn, bool) {
		// Read the ids ONCE, from the holdings answer. After the first
		// close the newest tool result is that close's own, which names no
		// participants — a step that re-read it every time would close one
		// worker and call it done.
		if toClose == nil {
			toClose = liveWorkerIDs(last)
		}
		if len(toClose) == 0 {
			return scriptedTurn{}, false
		}
		id := toClose[0]
		toClose = toClose[1:]
		return scriptedTurn{tool: "wave.close", args: `{"worker":"` + id + `"}`}, true
	}
	srv := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer srv.Close()

	stop := make(chan struct{})
	wg := launcherStandIn(t, w, stop)
	defer func() { close(stop); wg.Wait() }()

	client, _, err := assistant.NewClientAndRegistry(log.NewSlogAdapter(nil), nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant client: %v", err)
	}
	env := content.EnvironmentIDFor(content.EnvLocal, "")
	grant := autonomousWaveGrant(string(w.coordinator), env)

	// A worker settles while the model is waiting. It is not the test making
	// a call the model should have made: the coordinator does not end its
	// own workers here, the WORK does, which is the case the wait exists for.
	go func() {
		waittest.WaitFor(t, "the first worker to be live", func() bool {
			held, herr := w.db.Waves().HeldBy(ctx, string(w.coordinator))
			return herr == nil && len(held) >= 1 && held[0].State == wave.StateLive
		})
		held, _ := w.db.Waves().HeldBy(ctx, string(w.coordinator))
		_ = w.reg.Close(session.ID(held[0].Liveness.SessionID))
	}()

	askErr := client.Ask(ctx, assistant.AskParams{
		Key:           credential.NewSecret("sk-test"),
		BaseURL:       srv.URL,
		Model:         "probe-model",
		SessionID:     string(w.coordinator),
		Messages:      []assistant.Message{{Role: "user", Content: "start three workers and wait"}},
		Grant:         &grant,
		AttemptLedger: w.db.Ledger(),
		// The egress comparison the engine refuses to run without: a run
		// that may execute tools must screen its results against known
		// vault material. This stand has no vault, so nothing is known —
		// which is a real answer and not a bypass.
		KnownMaterial:   noKnownMaterial{},
		Waves:           w.record,
		WaveEnvironment: env,
	}, func(assistant.AskEvent) error { return nil })
	if askErr != nil {
		t.Fatalf("Ask: %v", askErr)
	}

	// THREE WORKERS EXIST, and the test never registered one.
	held, err := w.db.Waves().HeldBy(ctx, string(w.coordinator))
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 3 {
		t.Fatalf("the model's calls produced %d workers, want 3", len(held))
	}
	tasks := map[string]bool{}
	for _, p := range held {
		tasks[p.Task] = true
	}
	for _, want := range []string{"read AGENTS.md", "read the architecture", "read the vision"} {
		if !tasks[want] {
			t.Fatalf("no worker carries the task %q; got %v", want, tasks)
		}
	}

	// WHAT THE MODEL WAS TOLD, off the real results rather than off a struct
	// this test built. Five calls, five results.
	results := provider.toolResults(t)
	// Five scripted calls, then one close for each worker that was still
	// running when the model looked.
	if len(results) != 7 {
		t.Fatalf("the model was handed %d tool results, want 7 (three spawns, a wait, holdings, two closes)", len(results))
	}
	// Each spawn told it a worker id it can address later, and told it the
	// worker is LIVE — which is the enrolment having arrived, never that the
	// call returned.
	for i, r := range results[:3] {
		if !strings.Contains(r, `"state":"live"`) {
			t.Fatalf("spawn %d did not tell the model the worker is live: %s", i+1, r)
		}
	}
	// The wait answered with what the session holds, and it names the worker
	// that settled while the model was waiting.
	waitAnswer := results[3]
	if !strings.Contains(waitAnswer, "participants") {
		t.Fatalf("the wait answered without participants: %s", waitAnswer)
	}
	if !strings.Contains(waitAnswer, `"state":"abandoned"`) &&
		!strings.Contains(waitAnswer, `"state":"completed"`) {
		t.Fatalf("the wait returned before anything settled: %s", waitAnswer)
	}
	if !strings.Contains(waitAnswer, `"state":"live"`) {
		t.Fatalf("the wait reported nothing still running, so it did not return on the FIRST: %s", waitAnswer)
	}
	// And holdings answered afterwards with the same three.
	if strings.Count(results[4], `"task"`) != 3 {
		t.Fatalf("holdings did not report all three workers: %s", results[4])
	}

	// EVERY WORKER IS ENDED, and the two the model closed ended because it
	// closed them: it read their ids out of the answer it was given and
	// addressed them. The record reaches a terminal state through the exit
	// path, so nothing here wrote one.
	for _, r := range results[5:] {
		if !strings.Contains(r, `"ended":true`) {
			t.Fatalf("a close did not report the worker ended: %s", r)
		}
	}
	for _, p := range held {
		waittest.WaitFor(t, "every worker to reach a terminal state", func() bool {
			stored, serr := w.db.Waves().Participant(ctx, p.ID)
			return serr == nil && stored.State.Terminal()
		})
	}
	if len(w.reg.List()) != 1 {
		t.Fatalf("%d sessions are still open; only the coordinator's should be", len(w.reg.List()))
	}
}

// liveWorkerIDs pulls the ids of the still-running workers out of a holdings
// answer, the way a model reading that answer would.
func liveWorkerIDs(requestBody string) []string {
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(requestBody), &body); err != nil {
		return nil
	}
	last := ""
	for _, m := range body.Messages {
		if m.Role == "tool" {
			last = m.Content
		}
	}
	open := last[strings.Index(last, "{"):]
	var answer struct {
		Participants []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"participants"`
	}
	// The content wraps the JSON in a labelled block, so it is trimmed to
	// the object rather than decoded whole.
	if end := strings.LastIndex(open, "}"); end >= 0 {
		open = open[:end+1]
	}
	if err := json.Unmarshal([]byte(open), &answer); err != nil {
		return nil
	}
	var out []string
	for _, p := range answer.Participants {
		if p.State == string(wave.StateLive) {
			out = append(out, p.ID)
		}
	}
	return out
}

// noKnownMaterial is the egress comparison for a stand with no vault: it
// knows no secrets, so it matches none. It is not a bypass — the screen still
// runs on every result and still finds nothing, which is what a machine with
// an empty vault genuinely answers.
type noKnownMaterial struct{}

func (noKnownMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, nil
}

// autonomousWaveGrant mints the authority a coordinator run holds: every
// effect permitted, fenced to its own session and to the local environment.
// It is minted through EffectPolicy.AsGrant rather than written as a literal,
// so the deadline is stamped the way production stamps it.
func autonomousWaveGrant(sessionID, env string) content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	policy := content.EffectPolicy{
		Observe: permit, MutateReversible: permit, MutateDestructive: permit,
		PrivilegeChange: permit, Disclose: permit, CrossBoundary: permit, Delegate: permit,
	}
	return policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: sessionID},
		{Kind: content.ResourceEnvironment, ID: env},
	})
}
