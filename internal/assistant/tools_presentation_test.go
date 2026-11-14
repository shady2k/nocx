package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func TestAsk_LazyPresentationProjectsEssentialAndSearch(t *testing.T) {
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	p := testAskParams(srv.URL)
	p.Grant = &content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceSession, ID: "lane-1"}},
	}
	p.KnownMaterial = &fakeKnownMaterial{}
	p.Presentation = &agenttools.PresentationConfig{
		Lazy:             true,
		Essential:        []string{"session.list"},
		SchemaTokenLimit: 1,
	}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := toolNames(t, requestTools(t, f.body()))
	if !reflect.DeepEqual(got, []string{"session.list", "tools.search"}) {
		t.Fatalf("lazy declared tools = %v, want essential plus search", got)
	}
}

func TestAsk_LazyPresentationFallsBackBelowSchemaThreshold(t *testing.T) {
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	p := testAskParams(srv.URL)
	p.Grant = &content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceSession, ID: "lane-1"}},
	}
	p.KnownMaterial = &fakeKnownMaterial{}
	p.Presentation = &agenttools.PresentationConfig{Lazy: true, SchemaTokenLimit: 100000}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := toolNames(t, requestTools(t, f.body()))
	if !reflect.DeepEqual(got, []string{"session.list", "session.read", "session.run", "session.wait", "workers.holdings", "workers.say"}) {
		t.Fatalf("threshold fallback declared tools = %v, want all eligible tools", got)
	}
}

func TestMiddleware_PresentationRemovesLoadedToolAfterGrantShrinks(t *testing.T) {
	mutating := content.Grant{
		Effects: []content.Effect{content.EffectMutateReversible},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	current := mutating
	mw := middlewareFor(t, mutating, &fakeLedger{}, nil)
	mw.presentation = agenttools.PresentationConfig{
		Lazy:             true,
		Essential:        []string{"files.read"},
		SchemaTokenLimit: 1,
	}
	mw.presentationState = newPresentationState([]string{"files.edit"})
	mw.grantProvider = func() content.Grant { return current }
	mw.searchSchema, _ = os.ReadFile("../../contracts/tools/search.schema.json")

	_, state, err := mw.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{}, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}
	if got := infoNames(state.ToolInfos); !reflect.DeepEqual(got, []string{"files.edit", "tools.search"}) {
		t.Fatalf("first projection = %v, want loaded mutation plus search", got)
	}

	current = content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	_, state, err = mw.BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("shrunk projection: %v", err)
	}
	if got := infoNames(state.ToolInfos); !reflect.DeepEqual(got, []string{"files.read", "tools.search"}) {
		t.Fatalf("shrunk projection = %v, want loaded mutation removed", got)
	}
}

func infoNames(infos []*schema.ToolInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

func TestPresentationSearchReturnsOnlyEligibleHiddenTools(t *testing.T) {
	mw := middlewareFor(t, content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceSession, ID: "lane-1"}},
	}, &fakeLedger{}, nil)
	mw.presentation = agenttools.PresentationConfig{Lazy: true, SchemaTokenLimit: 1}
	mw.searchSchema, _ = os.ReadFile("../../contracts/tools/search.schema.json")

	out, err := mw.searchTools(context.Background(), `{"query":"list"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("search result: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != "session.list" {
		t.Fatalf("search result = %+v, want only eligible session.list", result.Tools)
	}
}

func TestAsk_HiddenEligibleToolUsesFullKernelPipeline(t *testing.T) {
	grant, dir := testDirGrant(t, autonomousMatrix())
	path := filepath.Join(dir, "hidden.txt")
	if err := os.WriteFile(path, []byte("hidden tool works"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	args := `{"path":` + quoted(path) + `}`
	f, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "files.read", args: args, id: "hidden-call"}))
	defer srv.Close()

	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	p := testAskParams(srv.URL)
	p.Grant = &grant
	p.AttemptLedger = &fakeLedger{}
	p.KnownMaterial = &fakeKnownMaterial{}
	p.Presentation = &agenttools.PresentationConfig{
		Lazy:             true,
		Essential:        []string{},
		SchemaTokenLimit: 1,
	}
	var calls []string
	if err := cl.Ask(context.Background(), p, func(event AskEvent) error {
		if event.Kind == AskToolCall && event.Call != nil {
			calls = append(calls, event.Call.Tool)
		}
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"files.read"}) {
		t.Fatalf("tool calls = %v, want hidden files.read through the kernel; request=%s", calls, f.body())
	}
	if !strings.Contains(f.body(), "hidden tool works") {
		t.Fatalf("final model request lacks the completed files.read result: %s", f.body())
	}
}

func TestAsk_LazySearchIsVisibleWithoutObserveGrant(t *testing.T) {
	policy := allRows(content.DecisionRefuse)
	policy.MutateReversible.Decision = content.DecisionPermit
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}})
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	p := testAskParams(srv.URL)
	p.Grant = &grant
	p.KnownMaterial = &fakeKnownMaterial{}
	p.Presentation = &agenttools.PresentationConfig{
		Lazy:             true,
		Essential:        []string{},
		SchemaTokenLimit: 1,
	}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got := toolNames(t, requestTools(t, f.body())); !reflect.DeepEqual(got, []string{toolsSearchName}) {
		t.Fatalf("mutation-only lazy tools = %v, want discoverable search infrastructure", got)
	}
}

func TestAsk_ConcurrentLazySearchUsesPrivateLoadedState(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"tool_call_id"`) {
			streamOK(w)
			return
		}
		streamToolCalls(w, toolCallSpec{name: toolsSearchName, args: `{"query":"list"}`})
	}
	_, srv := newFakeOpenAI(handler)
	defer srv.Close()

	cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	grant := allRows(content.DecisionPermit).AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "lane-1"}})
	p := testAskParams(srv.URL)
	p.Grant = &grant
	p.KnownMaterial = &fakeKnownMaterial{}
	p.Presentation = &agenttools.PresentationConfig{
		Lazy:             true,
		Essential:        []string{},
		SchemaTokenLimit: 1,
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- cl.Ask(context.Background(), p, func(AskEvent) error { return nil })
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Ask: %v", err)
		}
	}
}

func TestAsk_ApprovalWideningKeepsInitialToolDeclarations(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "nil presentation"
		if explicit {
			name = "explicit presentation"
		}
		t.Run(name, func(t *testing.T) {
			policy := allRows(content.DecisionRefuse)
			policy.Observe.Decision = content.DecisionAsk
			grant, dir := testDirGrant(t, policy)
			path := filepath.Join(dir, "read.txt")
			writeFile(t, path, "approved read")
			widened := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: dir}})
			var mu sync.Mutex
			var requests []string
			_, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
					return
				}
				mu.Lock()
				requests = append(requests, string(body))
				n := len(requests)
				mu.Unlock()
				if n == 1 {
					streamToolCalls(w, toolCallSpec{name: "files.read", args: `{"path":` + quoted(path) + `}`, id: "read-call"})
					return
				}
				streamOK(w)
			})
			defer srv.Close()
			cl, err := newClientWithTestToolsFS(nil, os.DirFS(realToolsFS), nil, content.Floor{})
			if err != nil {
				t.Fatal(err)
			}
			approvals := NewApprovalStore()
			q := &recordingQuestioner{store: approvals, answer: Approved{}}
			p := askParams(srv.URL, &grant, &fakeLedger{}, approvals)
			p.Questioner = q
			mints := 0
			p.FreshGrant = func() *content.Grant { mints++; return &widened }
			if explicit {
				p.Presentation = &agenttools.PresentationConfig{}
			} else {
				p.Presentation = nil
			}
			if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if len(q.asked) != 1 || mints != 1 {
				t.Fatalf("approval questions=%d refreshes=%d, want one each", len(q.asked), mints)
			}
			mu.Lock()
			bodies := append([]string(nil), requests...)
			mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("model requests=%d, want two", len(bodies))
			}
			before := toolNames(t, requestTools(t, bodies[0]))
			after := toolNames(t, requestTools(t, bodies[1]))
			if !reflect.DeepEqual(before, []string{"files.read"}) {
				t.Fatalf("initial tools=%v, want files.read only", before)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("approval widened declarations from %v to %v", before, after)
			}
			if !strings.Contains(bodies[1], "approved read") {
				t.Fatal("approved read did not reach next model request")
			}
		})
	}
}
