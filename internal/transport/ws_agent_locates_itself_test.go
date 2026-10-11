package transport

// The epic's own sentence, watched end to end (nocx-z4hgm, closing
// nocx-avogl): a person asks the assistant to look at THEIR session; it
// calls session.read for the session the backend granted to the run.
//
// The model receives no session identity in its tool arguments. The backend
// injects the grant's sole session scope into the resolver, and this test
// asserts that the backend capture source reads the pane the person asked about.
// A second session is open the whole time, so "the right session" is only a
// claim if there was another one to get wrong.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/storage"
)

// sessionIDInSystemPrompt reads only the model-facing system message. The
// run's pane identity stays backend-owned and must not be exposed there; the
// backend resolves an omitted sessionId from the run's narrowed capability.
var sessionIDShape = regexp.MustCompile(`[0-9a-f]{32}`)

// The two panes the person has open. The question is asked in the second.
const (
	locatePaneOther = "0198f2b0-0000-7000-8000-0000000000c8"
	locatePaneThis  = "0198f2b0-0000-7000-8000-0000000000c9"
)

func sessionIDInSystemPrompt(body string) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		if id := sessionIDShape.FindString(m.Content); id != "" {
			return id
		}
	}
	return ""
}

func operatorSessionScopedPolicyStore(t *testing.T) *assistant.GlobalPolicyStore {
	t.Helper()
	matrix := autonomousMatrixForTests()
	matrix.Observe.Scopes = []content.GrantScope{{
		Kind: content.ResourceSession,
		ID:   "operator-authored-session",
	}}
	store := assistant.NewGlobalPolicyStore(storage.NewDocumentStore(t.TempDir()), "agent-policy.json")
	if err := store.SetPolicy(matrix); err != nil {
		t.Fatalf("seed operator-scoped policy: %v", err)
	}
	return store
}

// selfLocatingProvider is the fake model. The test never tells it which
// session it is in; it finds out the way the real one does, and it answers
// with what the tool result actually carried — so the answer is evidence
// about the product rather than about the fake.
type selfLocatingProvider struct {
	mu       sync.Mutex
	marker   string // the line only the renderer's frame can supply
	requests int
	learned  string   // the id it read out of the system prompt
	bodies   []string // every request body, for the failure message
}

func (p *selfLocatingProvider) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)

	p.mu.Lock()
	p.requests++
	n := p.requests
	p.bodies = append(p.bodies, body)
	if n == 1 {
		p.learned = sessionIDInSystemPrompt(body)
	}
	marker := p.marker
	p.mu.Unlock()

	if n == 1 {
		streamToolCallChunk(w, "session.read", `{}`)
		return
	}
	if strings.Contains(body, marker) {
		streamAnswerChunk(w, "the screen says "+marker+" — the key was refused")
		return
	}
	// The tool result did not carry the screen: say so rather than
	// inventing an answer, so a broken capture reads as a broken capture.
	streamAnswerChunk(w, "I could not see the screen")
}

func (p *selfLocatingProvider) state() (int, string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests, p.learned, append([]string(nil), p.bodies...)
}

// awaitReadScreenRequest waits for the broker's request the way the
// renderer does, and reports what happened INSTEAD when it never comes: a
// run that terminalized first is the refusal path (a call the grant does
// not cover is refused before the broker is ever asked), and its sentence
// is the only useful thing to print.

func TestAssistant_LooksAtPaneDespiteOperatorSessionScope(t *testing.T) {
	const marker = "(publickey)."

	prov := &selfLocatingProvider{marker: marker}
	srv := httptest.NewServer(http.HandlerFunc(prov.serve))
	defer srv.Close()

	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	source := &recordingScreenCapture{body: screenCaptureBody(t, 0, "Permission denied", marker)}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(operatorSessionScopedPolicyStore(t)), WithScreenCaptureSource(source))
	h.createEndpointAt(srv.URL)

	// Two real panes on the layout chain, each with a live session as its
	// pipe. The person is in the second one; the first is the session the
	// run must NOT reach for.
	seedPaneChain(t, h.db, locatePaneOther, locatePaneThis)
	otherSession, openErr := openSessionInPane(t, h.conn, locatePaneOther, 10)
	if openErr != nil {
		t.Fatalf("open in the other pane: %+v", openErr)
	}
	sid, openErr := openSessionInPane(t, h.conn, locatePaneThis, 11)
	if openErr != nil {
		t.Fatalf("open in this pane: %+v", openErr)
	}
	if sid == "" || sid == otherSession {
		t.Fatalf("the two opens returned %q and %q, want two distinct sessions", otherSession, sid)
	}

	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "0198f2b0-0000-7000-8000-00000000ab01",
		"sessionId": sid,
		"question":  "look at my terminal — why did that fail?",
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("ask: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("ask state = %q, want prepared", res.State)
	}

	// The model names no session id. The backend resolves the run's pane
	// from its narrowed capability, not from the neighboring pane or the
	// operator-authored policy scope.
	answer, state, stateErr := collectAnswer(t, h, res.RunID, 20*time.Second)
	if state != "completed" {
		reqs, learned, bodies := prov.state()
		t.Fatalf("run state = %q (%s); answer %q; provider requests=%d learned=%q bodies=%v", state, stateErr, answer, reqs, learned, bodies)
	}
	if !strings.Contains(answer, marker) {
		reqs, learned, bodies := prov.state()
		t.Fatalf("answer = %q, want it to name %q — the backend screen capture carried it; provider requests=%d learned=%q bodies=%v", answer, marker, reqs, learned, bodies)
	}
	_, learned, _ := prov.state()
	if learned != "" {
		t.Fatalf("system prompt exposed session id %q; pane identity must stay backend-owned", learned)
	}
	calls := source.snapshotCalls()
	if len(calls) != 1 || calls[0].sessionID != sid {
		which := "an id belonging to neither open pane"
		if len(calls) != 0 && calls[0].sessionID == otherSession {
			which = "the OTHER pane's session"
		}
		t.Fatalf("backend screen capture calls = %+v (%s); the question was asked in %q", calls, which, sid)
	}

	// The answer reaches the block: the turn's prose, sealed, carrying the
	// same sentence the person watched stream. Its home is a `text` child
	// rather than a body on the turn since ADR-0040, and the claim being made
	// is unchanged — what streamed is what was kept.
	led := h.db.Ledger()
	ctx := context.Background()
	ans, entryErr := led.Entry(ctx, res.EntryID)
	if entryErr != nil || ans == nil {
		t.Fatalf("answer entry: %v (err %v)", ans, entryErr)
	}
	if len(ans.Executions) != 1 || len(ans.Executions[0].Artifacts) != 0 {
		t.Fatalf("answer entry executions/artifacts = %d/%d, want 1/0 — the answer is its prose children",
			len(ans.Executions), len(ans.Executions[0].Artifacts))
	}
	if body := proseBodyOf(t, led, res.EntryID); !strings.Contains(body, marker) {
		t.Fatalf("the stored answer = %q, want the answer the person watched stream", body)
	}
	assertProseSealed(t, led, res.EntryID)
	if ans.Phase != content.PhaseClosed || ans.Status != content.EntrySuccess {
		t.Errorf("answer entry phase/status = %q/%q, want closed/success", ans.Phase, ans.Status)
	}
}

// collectAnswer reads the run's own deltas until it terminalizes, and hands
// back the whole answer with the terminal state. It never waits on a
// duration for the answer to be "done": the terminal agent.runState is the
// observable end of the stream.
func collectAnswer(t *testing.T, h *askHarness, runID int64, d time.Duration) (answer, state, stateErr string) {
	t.Helper()
	var b strings.Builder
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("run %d never terminalized; answer so far %q", runID, b.String())
		}
		_ = h.conn.SetReadDeadline(time.Now().Add(remaining))
		_, msg, err := h.conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading the answer stream: %v", err)
		}
		var n struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal(msg, &n); err != nil || n.ID != nil {
			continue
		}
		switch n.Method {
		case "agent.runDelta":
			var delta struct {
				RunID int64  `json:"runId"`
				Text  string `json:"text"`
			}
			if err := json.Unmarshal(n.Params, &delta); err != nil {
				t.Fatalf("runDelta unmarshal: %v\nraw: %s", err, n.Params)
			}
			if delta.RunID != runID {
				t.Fatalf("runDelta runId = %d, want %d", delta.RunID, runID)
			}
			b.WriteString(delta.Text)
		case "agent.runState":
			var st struct {
				RunID int64  `json:"runId"`
				State string `json:"state"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(n.Params, &st); err != nil {
				t.Fatalf("runState unmarshal: %v\nraw: %s", err, n.Params)
			}
			if st.RunID != runID {
				continue
			}
			return b.String(), st.State, st.Error
		}
	}
}
