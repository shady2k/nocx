package transport

// The epic's own sentence, watched end to end (nocx-z4hgm, closing
// nocx-avogl): a person asks the assistant to look at THEIR session; it
// calls readScreen with the right session id and answers.
//
// What makes this test different from the readScreen wiring test beside it
// (ws_readscreen_test.go) is where the model learns the id. There, the test
// hands the fake provider the session id in Go — `fake.session = sid` — so
// the call would name the right session even if the backend told the model
// nothing at all. Here the provider is told NOTHING by the test: it reads
// the id out of the system message it was sent, the way the real model has
// to, and the assertion is on the id that then travelled back through the
// broker. Stop pinning the id into the prompt (nocx-avogl.1) and there is
// nothing for the provider to find, so the call names a session the run's
// grant does not cover, the scope check refuses it before the broker is
// asked, and this test goes red — on the id, not on the prompt's wording.
//
// A second session is open the whole time. "The right session id" is only a
// claim if there was another one to get wrong.

import (
	"context"
	"encoding/json"
	"fmt"
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
)

// sessionIDInSystemPrompt is the model's own path to where it is: the
// system message of the request it was just sent, scanned for a session id.
//
// It matches the SHAPE of a session id (session.NewID: 32 hex characters)
// rather than the prompt's prose, so rewording "Session id:" does not
// break the test while dropping the id does — which is the direction the
// bead asks for. It reads only the system message: the id must arrive as a
// standing fact about the pane, not because the person happened to type it
// into their question.
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
	learned := p.learned
	marker := p.marker
	p.mu.Unlock()

	if n == 1 {
		streamToolCallChunk(w, "session.read", fmt.Sprintf(`{"sessionId":%q}`, learned))
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
func awaitReadScreenRequest(t *testing.T, h *askHarness, prov *selfLocatingProvider, d time.Duration) (requestID, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			reqs, learned, bodies := prov.state()
			t.Fatalf("no agent.readScreenRequest within %s; provider requests=%d, id it read from the prompt=%q, bodies=%v",
				d, reqs, learned, bodies)
		}
		_ = h.conn.SetReadDeadline(time.Now().Add(remaining))
		_, msg, err := h.conn.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for agent.readScreenRequest: %v", err)
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
		case "agent.readScreenRequest":
			var req struct {
				RequestID string `json:"requestId"`
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(n.Params, &req); err != nil {
				t.Fatalf("readScreenRequest unmarshal: %v\nraw: %s", err, n.Params)
			}
			return req.RequestID, req.SessionID
		case "agent.runState":
			var st struct {
				State string `json:"state"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(n.Params, &st)
			_, learned, _ := prov.state()
			t.Fatalf("the run reached %q (%s) before the screen was ever asked for; "+
				"the model called readScreen with %q — the id it found in the system prompt",
				st.State, st.Error, learned)
		}
	}
}

// TestAssistant_LooksAtThePaneTheQuestionWasAskedIn is nocx-avogl's success
// criterion, end to end against the real backend: the real engine with the
// embedded schemas, the real socket, the real ledger, a real grant. The
// renderer half is this test.
func TestAssistant_LooksAtThePaneTheQuestionWasAskedIn(t *testing.T) {
	const marker = "(publickey)."

	prov := &selfLocatingProvider{marker: marker}
	srv := httptest.NewServer(http.HandlerFunc(prov.serve))
	defer srv.Close()

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarnessWithOpts(t, client, WithAgentPolicy(autonomousPolicyStore(t)))
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

	// THE ASSERTION: the id that travelled. The model was told nothing by
	// this test; whatever reached the broker came out of the system prompt
	// and through the run's scope check.
	requestID, gotSession := awaitReadScreenRequest(t, h, prov, 15*time.Second)
	if gotSession != sid {
		which := "an id belonging to neither open pane"
		if gotSession == otherSession {
			which = "the OTHER pane's session"
		}
		t.Fatalf("readScreen was aimed at %q (%s); the question was asked in %q", gotSession, which, sid)
	}
	if requestID == "" {
		t.Fatal("readScreenRequest carries no requestId")
	}

	// The renderer answers with the screen the person is looking at.
	reply := jsonrpcCall(t, h.conn, "agent.readScreenResolved",
		readScreenFrameWire(t, requestID, "Permission denied", marker))
	var rerr struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(reply, &rerr); err != nil {
		t.Fatalf("resolution response unmarshal: %v", err)
	}
	if rerr.Error != nil {
		t.Fatalf("readScreenResolved refused: %+v", rerr.Error)
	}

	// And the answer arrives, naming what only the frame could have told
	// it, and the run completes: a refusal or a failure is not this test
	// passing.
	answer, state, stateErr := collectAnswer(t, h, res.RunID, 20*time.Second)
	if state != "completed" {
		reqs, learned, bodies := prov.state()
		t.Fatalf("run state = %q (%s); answer so far %q; provider requests=%d learned=%q bodies=%v",
			state, stateErr, answer, reqs, learned, bodies)
	}
	if !strings.Contains(answer, marker) {
		reqs, learned, bodies := prov.state()
		t.Fatalf("answer = %q, want it to name %q — the line only the frame carried; provider requests=%d learned=%q bodies=%v",
			answer, marker, reqs, learned, bodies)
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
