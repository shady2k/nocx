package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
)

// The invocation envelope's derivation edge (nocx-d6gn4.9).
//
// The edge answers the question nocx-d6gn4.10 is gated on: how DEEP are the
// dependent chains our real tasks need. It may never be read off adjacency —
// ADR-0019 §2 is explicit that ingest order is commit order and not causality,
// and two calls in a row are routinely independent.
//
// Under THIS carrier the model authors the arguments, so the exact fact lives
// only inside the model. Asking for it would mean a derivedFrom field in every
// tool's schema, which is rejected on validity rather than cost: the question
// would alter the very behaviour the experiment exists to measure. What is
// left is host-observable evidence — which spans of the canonical arguments
// appear verbatim in an earlier result of the same run — recorded together
// with the method that produced it, so a reader knows what it is holding.

// derivationOf reads the derivation block back off a recorded attempt payload.
func derivationOf(t *testing.T, payload string) (method string, edges []string) {
	t.Helper()
	var body struct {
		DerivedFrom *struct {
			Method string   `json:"method"`
			Edges  []string `json:"edges"`
		} `json:"derivedFrom"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("attempt payload: %v", err)
	}
	if body.DerivedFrom == nil {
		return "", nil
	}
	return body.DerivedFrom.Method, body.DerivedFrom.Edges
}

// TestAsk_ADependentCallRecordsTheInvocationItsArgumentsCameFrom is the
// positive half: the model lists a session's items, reads the block id out of
// that result, and reads it. The second call's record names the first.
func TestAsk_ADependentCallRecordsTheInvocationItsArgumentsCameFrom(t *testing.T) {
	src := &fakeBlocks{
		items: SessionItems{Items: []SessionItem{{
			ID: "blk-df", Command: "df -h", State: "exited", Lines: 3,
		}}},
		item: SessionItemRead{
			Command: "df -h", State: "exited", Total: 3, Start: 0, End: 3,
			Text: "one\ntwo\nthree",
		},
	}
	var turn int
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) {
		turn++
		switch turn {
		case 1:
			streamToolCalls(w, toolCallSpec{name: "session.list", args: `{"sessionId":"session-a"}`, id: "call_list"})
		case 2:
			// blk-df is a value the model can only have got from the list.
			streamToolCalls(w, toolCallSpec{
				name: "session.read",
				args: `{"sessionId":"session-a","id":"blk-df","start":0,"count":3}`,
				id:   "call_read",
			})
		default:
			streamAnswer(w, "done")
		}
	})
	defer srv.Close()

	ledger := &fakeLedger{}
	p := askParams(srv.URL, ptrGrant(sessionGrant("session-a", autonomousMatrix())), ledger, nil)
	p.Requester = &blocksOnlyRequester{blocks: src}
	p.Messages = []Message{{Role: "user", Content: "what did df say?"}}

	cl, err := newClient(nil, toolsDirFS(t))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	read, ok := submissionFor(ledger, "session.read")
	if !ok {
		t.Fatalf("no session.read attempt recorded; intents = %v", intentsOf(ledger))
	}
	method, edges := derivationOf(t, read)
	if len(edges) != 1 {
		t.Fatalf("session.read derivation edges = %v, want exactly the session.list invocation", edges)
	}
	if edges[0] != "entry-session.list" {
		t.Errorf("edge = %q, want %q", edges[0], "entry-session.list")
	}
	if method == "" {
		t.Errorf("derivation records no method; a reader cannot tell evidence from certainty")
	}
}

// TestAsk_TwoAdjacentIndependentCallsRecordNoDerivation is the null half, and
// it is the half that makes the positive one mean anything: adjacency is not
// causality, so a second call whose arguments owe nothing to the first result
// records NO edge. Without this a derivation field that simply named the
// previous call would pass the test above.
func TestAsk_TwoAdjacentIndependentCallsRecordNoDerivation(t *testing.T) {
	src := &fakeBlocks{
		items: SessionItems{Items: []SessionItem{{
			ID: "blk-df", Command: "df -h", State: "exited", Lines: 3,
		}}},
		item: SessionItemRead{
			Command: "df -h", State: "exited", Total: 3, Start: 0, End: 3,
			Text: "alpha\nbeta\ngamma",
		},
	}
	var turn int
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) {
		turn++
		switch turn {
		case 1:
			// Reads a block the model was told about, not one it discovered.
			streamToolCalls(w, toolCallSpec{
				name: "session.read",
				args: `{"sessionId":"session-a","id":"blk-df","start":0,"count":3}`,
				id:   "call_read",
			})
		case 2:
			// Nothing here came out of "alpha beta gamma".
			streamToolCalls(w, toolCallSpec{name: "session.list", args: `{"sessionId":"session-a"}`, id: "call_list"})
		default:
			streamAnswer(w, "done")
		}
	})
	defer srv.Close()

	ledger := &fakeLedger{}
	p := askParams(srv.URL, ptrGrant(sessionGrant("session-a", autonomousMatrix())), ledger, nil)
	p.Requester = &blocksOnlyRequester{blocks: src}
	p.Messages = []Message{{Role: "user", Content: "what is going on here?"}}

	cl, err := newClient(nil, toolsDirFS(t))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	list, ok := submissionFor(ledger, "session.list")
	if !ok {
		t.Fatalf("no session.list attempt recorded; intents = %v", intentsOf(ledger))
	}
	if _, edges := derivationOf(t, list); len(edges) != 0 {
		t.Fatalf("independent session.list recorded derivation %v; adjacency is not causality", edges)
	}
}

// submissionFor returns the payload of the LAST attempt recorded for a tool.
func submissionFor(l *fakeLedger, intent string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.submissions) - 1; i >= 0; i-- {
		if l.submissions[i].intent == intent {
			return l.submissions[i].payload, true
		}
	}
	return "", false
}

func intentsOf(l *fakeLedger) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, 0, len(l.submissions))
	for _, s := range l.submissions {
		names = append(names, s.intent)
	}
	return fmt.Sprintf("%v", names)
}

// TestAsk_TheEnvelopeNamesTheToolVersionTheCallWasMadeAgainst: the record says
// WHICH descriptor the model was working from. Two cohorts measured across a
// description change are not one measurement, and without this the break is
// invisible in the data.
func TestAsk_TheEnvelopeNamesTheToolVersionTheCallWasMadeAgainst(t *testing.T) {
	src := &fakeBlocks{items: SessionItems{Items: []SessionItem{{
		ID: "blk-df", Command: "df -h", State: "exited", Lines: 3,
	}}}}
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "session.list", args: `{"sessionId":"session-a"}`, id: "call_list",
	}))
	defer srv.Close()

	ledger := &fakeLedger{}
	p := askParams(srv.URL, ptrGrant(sessionGrant("session-a", autonomousMatrix())), ledger, nil)
	p.Requester = &blocksOnlyRequester{blocks: src}
	p.Messages = []Message{{Role: "user", Content: "what ran here?"}}

	cl, err := newClient(nil, toolsDirFS(t))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if err = cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	payload, ok := submissionFor(ledger, "session.list")
	if !ok {
		t.Fatalf("no session.list attempt recorded; intents = %v", intentsOf(ledger))
	}
	var body struct {
		Descriptor string `json:"descriptor"`
	}
	if err = json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("attempt payload: %v", err)
	}

	registry, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := registry.Lookup("session.list")
	if !ok {
		t.Fatal("session.list is not in the registry")
	}
	if body.Descriptor != tool.DescriptorDigest() {
		t.Fatalf("recorded descriptor = %q, want the registry's %q", body.Descriptor, tool.DescriptorDigest())
	}
}

// TestAsk_TheDerivationRecordsWhatItComparedAgainst: "no edge" is two different
// facts — nothing to compare against, or several candidates and none matched —
// and a reader that cannot separate them will read a run with one call as a run
// with no dependencies. So the candidate set is recorded beside the edges.
func TestAsk_TheDerivationRecordsWhatItComparedAgainst(t *testing.T) {
	src := &fakeBlocks{
		items: SessionItems{Items: []SessionItem{{
			ID: "blk-df", Command: "df -h", State: "exited", Lines: 3,
		}}},
		item: SessionItemRead{
			Command: "df -h", State: "exited", Total: 3, Start: 0, End: 3,
			Text: "alpha\nbeta\ngamma",
		},
	}
	var turn int
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) {
		turn++
		switch turn {
		case 1:
			streamToolCalls(w, toolCallSpec{
				name: "session.read",
				args: `{"sessionId":"session-a","id":"blk-df","start":0,"count":3}`,
				id:   "call_read",
			})
		case 2:
			streamToolCalls(w, toolCallSpec{name: "session.list", args: `{"sessionId":"session-a"}`, id: "call_list"})
		default:
			streamAnswer(w, "done")
		}
	})
	defer srv.Close()

	ledger := &fakeLedger{}
	p := askParams(srv.URL, ptrGrant(sessionGrant("session-a", autonomousMatrix())), ledger, nil)
	p.Requester = &blocksOnlyRequester{blocks: src}
	p.Messages = []Message{{Role: "user", Content: "what is going on?"}}

	cl, err := newClient(nil, toolsDirFS(t))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// The FIRST call had nothing to compare against.
	first, ok := submissionFor(ledger, "session.read")
	if !ok {
		t.Fatalf("no session.read attempt; intents = %v", intentsOf(ledger))
	}
	if got := candidatesOf(t, first); len(got) != 0 {
		t.Errorf("first call compared against %v; there was nothing before it", got)
	}

	// The SECOND had one candidate and matched none of it.
	second, ok := submissionFor(ledger, "session.list")
	if !ok {
		t.Fatalf("no session.list attempt; intents = %v", intentsOf(ledger))
	}
	cands := candidatesOf(t, second)
	if len(cands) != 1 || cands[0] != "entry-session.read" {
		t.Fatalf("second call candidates = %v, want exactly [entry-session.read]", cands)
	}
	if _, edges := derivationOf(t, second); len(edges) != 0 {
		t.Fatalf("edges = %v, want none: a candidate considered is not a candidate matched", edges)
	}
}

// candidatesOf reads the prior invocations a derivation was checked against.
func candidatesOf(t *testing.T, payload string) []string {
	t.Helper()
	var body struct {
		DerivedFrom *struct {
			Candidates []string `json:"candidates"`
		} `json:"derivedFrom"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("attempt payload: %v", err)
	}
	if body.DerivedFrom == nil {
		return nil
	}
	return body.DerivedFrom.Candidates
}

// TestAsk_TwoRunCallsRecordTheChainBetweenThem reproduces the run the owner
// drove on 2026-08-26: a find that prints a path, then a command that IS that
// path. `run` is the tool the whole experiment turns on and no end-to-end test
// had ever exercised the derivation through it — the earlier ones all used
// session.list and session.read, which is how two defects in a row reached a
// screenshot instead of a test.
func TestAsk_TwoRunCallsRecordTheChainBetweenThem(t *testing.T) {
	runner := &recordingRunner{
		body: runResolvedBody("entry-find", new(0), "success", 1, 0, 1, "122919 ./log.txt"),
	}
	var turn int
	_, srv := newFakeOpenAI(func(w http.ResponseWriter, _ *http.Request) {
		turn++
		switch turn {
		case 1:
			streamToolCalls(w, toolCallSpec{
				name: "run",
				args: `{"sessionId":"session-a","command":"find . -maxdepth 1 -type f -printf '%s %p\n' | sort -rn | head -1"}`,
				id:   "call_find",
			})
		case 2:
			// The path came out of the first command's OUTPUT.
			streamToolCalls(w, toolCallSpec{
				name: "run",
				args: `{"sessionId":"session-a","command":"head -n 10 ./log.txt"}`,
				id:   "call_head",
			})
		default:
			streamAnswer(w, "done")
		}
	})
	defer srv.Close()

	ledger := &fakeLedger{}
	p := askParams(srv.URL, ptrGrant(sessionGrant("session-a", autonomousMatrix())), ledger, nil)
	p.Requester = runner
	p.Messages = []Message{{Role: "user", Content: "what is the biggest text file, and its first lines?"}}

	cl, err := newClient(nil, toolsDirFS(t))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	var calls []ToolCall
	if err = cl.Ask(context.Background(), p, func(ev AskEvent) error {
		if ev.Kind == AskToolCall && ev.Call != nil {
			calls = append(calls, *ev.Call)
		}
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("announced %d calls, want 2", len(calls))
	}
	if len(calls[0].DerivedFrom) != 0 {
		t.Errorf("the FIRST run announced %v; nothing preceded it", calls[0].DerivedFrom)
	}
	if len(calls[1].DerivedFrom) != 1 || calls[1].DerivedFrom[0] != "call_find" {
		t.Fatalf("the second run announced %v, want [call_find]: ./log.txt came out of the first command's output", calls[1].DerivedFrom)
	}
}
