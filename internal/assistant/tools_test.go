package assistant

// The engine's tool-declaration tests: what the model is actually offered on
// the wire, driven through the real eino agent and recorded by the fake
// OpenAI server (the same seam the probe tests use). These are the
// acceptance tests of nocx-pgtrh criteria 3, 4 and 5 — the set built for the
// model, asserted on the request the engine really sends.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
)

// realToolsFS reaches the repo's contracts/tools directory from the package
// dir, exactly as the transport tests reach contracts/.
const realToolsFS = "../../contracts/tools"

func testAskParams(baseURL string) AskParams {
	return AskParams{
		Key:      credential.NewSecret("sk-test-123"),
		BaseURL:  baseURL,
		Model:    "probe-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}
}

func askWithGrant(t *testing.T, grant *content.Grant) *fakeOpenAIServer {
	t.Helper()
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	p := testAskParams(srv.URL)
	p.Grant = grant
	p.KnownMaterial = &fakeKnownMaterial{}
	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	if err := cl.Ask(context.Background(), p, func(string) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	return f
}

// requestTools extracts the tools array the request actually carried.
func requestTools(t *testing.T, body string) []map[string]any {
	t.Helper()
	var req struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("request body %q: %v", body, err)
	}
	return req.Tools
}

func toolNames(t *testing.T, tools []map[string]any) []string {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		fn, ok := tl["function"].(map[string]any)
		if !ok {
			t.Fatalf("tool entry has no function object: %v", tl)
		}
		name, _ := fn["name"].(string)
		names = append(names, name)
	}
	return names
}

// TestAsk_DeclaresExactlyThePermittedTools is acceptance criterion 3: given
// a grant, the set the engine hands the model contains exactly the permitted
// tools, and a forbidden tool is absent rather than present-and-filtered —
// asserted on what the engine really built (the request off the wire).
func TestAsk_DeclaresExactlyThePermittedTools(t *testing.T) {
	observePath := &content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	f := askWithGrant(t, observePath)
	got := toolNames(t, requestTools(t, f.body()))
	want := []string{"files.read", "git.status"}
	if len(got) != len(want) {
		t.Fatalf("declared tools = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("declared tools = %v, want exactly %v", got, want)
		}
	}

	// A grant whose effect is not permitted declares nothing — the observe
	// tools are absent, never declared-and-filtered.
	f = askWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectMutateReversible},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	})
	if got := requestTools(t, f.body()); len(got) != 0 {
		t.Fatalf("mutate grant declared tools %v, want none", toolNames(t, got))
	}

	// A grant whose resource kinds are not covered declares nothing: the
	// declared tools touch paths and sessions, never credentials.
	f = askWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceCredential, ID: "cred-1"}},
	})
	if got := requestTools(t, f.body()); len(got) != 0 {
		t.Fatalf("credential grant declared tools %v, want none", toolNames(t, got))
	}

	// A session grant declares exactly the session tool — readScreen is the
	// first tool whose resource kind is a session, so a session scope is
	// now meaningful (the positive end of the same rule).
	f = askWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceSession, ID: "lane-1"}},
	})
	got = toolNames(t, requestTools(t, f.body()))
	if len(got) != 1 || got[0] != "readScreen" {
		t.Fatalf("session grant declared tools %v, want exactly [readScreen]", got)
	}
}

// TestAsk_NoGrantTakesTheNoToolsPath is acceptance criterion 5: an ask with
// no grant at all — every caller today — declares no tools, and the request
// carries no tools array, exactly as the explain-mode runs have always been.
func TestAsk_NoGrantTakesTheNoToolsPath(t *testing.T) {
	f, srv := newFakeOpenAI(nil)
	defer srv.Close()

	cl, clErr := newClient(nil, os.DirFS(realToolsFS))
	if clErr != nil {
		t.Fatalf("newClient: %v", clErr)
	}
	p := testAskParams(srv.URL) // Grant is nil
	if err := cl.Ask(context.Background(), p, func(string) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	body := f.body()
	if strings.Contains(body, `"tools"`) {
		t.Fatalf("no-grant ask declared tools: %s", body)
	}
}

// TestAsk_PermittedToolCarriesItsSchema is acceptance criterion 4, the
// positive end of the interval: a permitted tool is present in the set AND
// carries the schema the model needs to call it — the actual contracts/tools
// file, byte for byte. A test that only proved exclusion would pass over a
// set that is always empty, which is exactly the state the code is in today.
func TestAsk_PermittedToolCarriesItsSchema(t *testing.T) {
	f := askWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	})
	tools := requestTools(t, f.body())
	if len(tools) != 2 {
		t.Fatalf("declared %d tools, want 2", len(tools))
	}
	found := map[string]json.RawMessage{}
	for _, tl := range tools {
		fn, ok := tl["function"].(map[string]any)
		if !ok {
			t.Fatalf("tool entry has no function object: %v", tl)
		}
		name, _ := fn["name"].(string)
		params, err := json.Marshal(fn["parameters"])
		if err != nil {
			t.Fatalf("%s: marshal parameters: %v", name, err)
		}
		found[name] = params
	}
	readSchema, ok := found["files.read"]
	if !ok {
		t.Fatalf("files.read not declared; declared = %v", toolNames(t, tools))
	}
	// The schema the model was shown is the contract file itself: it names
	// path as required and closes the object — the model cannot invent an
	// argument the validator will refuse.
	var params struct {
		AdditionalProperties json.RawMessage `json:"additionalProperties"`
		Required             []string        `json:"required"`
	}
	if err := json.Unmarshal(readSchema, &params); err != nil {
		t.Fatalf("files.read parameters %s: %v", readSchema, err)
	}
	if string(params.AdditionalProperties) != "false" {
		t.Errorf("files.read parameters lack additionalProperties: false: %s", readSchema)
	}
	if len(params.Required) != 1 || params.Required[0] != "path" {
		t.Errorf("files.read required = %v, want [path]", params.Required)
	}
}
