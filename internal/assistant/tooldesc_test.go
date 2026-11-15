package assistant

// The harness catalogue preserves the canonical descriptions and parameter schemas.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// catalogueWithGrant reads the declarations offered by the harness tool entry.
func catalogueWithGrant(t *testing.T, grant *content.Grant) []agenttools.Tool {
	t.Helper()
	cl, _, err := NewClientAndRegistry(nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindRunTools(context.Background(), cl, AskParams{Grant: grant, KnownMaterial: &fakeKnownMaterial{}, FreshGrant: func() *content.Grant { return grant }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return bound.Catalogue(*grant)
}

func catalogueDescriptions(tools []agenttools.Tool) map[string]string {
	out := map[string]string{}
	for _, tool := range tools {
		out[tool.Name] = tool.Description
	}
	return out
}

// TestToolDescription_IsTheDeclarationsSentence: the description the model
// reads is the declaration's own sentence, byte for byte. One vocabulary,
// owned by the table where every other fact about a tool already lives.
func TestToolDescription_IsTheDeclarationsSentence(t *testing.T) {
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	want := map[string]string{}
	for _, tl := range reg.All() {
		if tl.Narrow == nil {
			continue
		}
		want[tl.Name] = tl.Description
	}

	catalogue := catalogueWithGrant(t, &content.Grant{
		// A grant that reaches EVERYTHING, which is what makes the count
		// below an assertion about descriptions rather than about
		// eligibility. delegate and the environment scope are here for
		// workers.spawn; without them the grant would silently stop covering
		// one tool and the test would be measuring the wrong set.
		Effects: []content.Effect{
			content.EffectObserve, content.EffectMutateReversible,
			content.EffectMutateDestructive, content.EffectCrossBoundary,
			content.EffectDelegate,
		},
		Scopes: []content.GrantScope{
			{Kind: content.ResourcePath, ID: "/workspace"},
			{Kind: content.ResourceSession, ID: "lane-1"},
			{Kind: content.ResourceContent, ID: "content"},
			{Kind: content.ResourceDestination, ID: "*"},
			{Kind: content.ResourceEnvironment, ID: content.EnvironmentIDFor(content.EnvLocal, "")},
			// The participant's kind (A11). No ordinary run's fence carries
			// one — which is exactly what keeps workers.inbox off a
			// coordinator's offer — so this grant names it deliberately, to
			// keep "every declared tool has a sentence" a claim about ALL of
			// them rather than about the coordinator's set.
			{Kind: content.ResourceWorkspace, ID: "workspace/workspace:default"},
		},
	})
	got := catalogueDescriptions(catalogue)
	if len(got) != len(want) {
		t.Fatalf("declared %d tools, want all %d described", len(got), len(want))
	}
	for name, desc := range got {
		if want[name] == "" {
			t.Fatalf("tool %q is declared but the registry has no sentence for it", name)
		}
		if desc != want[name] {
			t.Errorf("tool %q description = %q, want the declaration's sentence %q", name, desc, want[name])
		}
	}
}

// TestToolDescription_CarriesNoAuthorityVocabulary is the negative end: the
// effect class, and where the tool executes, are facts the policy and the
// middleware read. The model never sees them — it was the whole of the old
// rendering.
func TestToolDescription_CarriesNoAuthorityVocabulary(t *testing.T) {
	catalogue := catalogueWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive},
		Scopes: []content.GrantScope{
			{Kind: content.ResourcePath, ID: "/workspace"},
			{Kind: content.ResourceSession, ID: "lane-1"},
		},
	})
	descriptions := catalogueDescriptions(catalogue)
	if len(descriptions) == 0 {
		t.Fatal("no tools declared; the assertion would pass over an empty set")
	}
	for name, desc := range descriptions {
		low := strings.ToLower(desc)
		for _, word := range []string{"effect", "InGo", "InRenderer"} {
			if strings.Contains(low, strings.ToLower(word)) {
				t.Errorf("tool %q description carries %q: %s", name, word, desc)
			}
		}
	}
}

func catalogueParameters(t *testing.T, tools []agenttools.Tool) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, tool := range tools {
		var params map[string]any
		if err := json.Unmarshal(tool.ParamsSchema, &params); err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = params
	}
	return out
}

func schemaContainsKey(value any, want string) bool {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v[want]; ok {
			return true
		}
		for _, child := range v {
			if schemaContainsKey(child, want) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if schemaContainsKey(child, want) {
				return true
			}
		}
	}
	return false
}

// TestToolParameters_CarryNoReturnShape: a tool's contract document declares
// BOTH shapes — the parameters at the top level and what it returns under
// $defs/result — and only the first is addressed to the model. The registry
// lifts the result out for the kernel's checkResult and must not leave it in
// the params.
//
// It once did, and nothing caught it, because every test that could have was
// reading the schema FILE — where both shapes belong. The model was shown 355
// lines of return contract as though it were how to call the tool, eighty of
// them run's window-and-clamping rules on a schema whose only real parameter
// is `command` (nocx-ydu92). So this asserts on the request body: the thing
// the model receives, not the thing we store.
func TestToolParameters_CarryNoReturnShape(t *testing.T) {
	catalogue := catalogueWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive},
		Scopes: []content.GrantScope{
			{Kind: content.ResourcePath, ID: "/workspace"},
			{Kind: content.ResourceSession, ID: "lane-1"},
		},
	})
	params := catalogueParameters(t, catalogue)
	if len(params) == 0 {
		t.Fatal("no tools declared; the assertion would pass over an empty set")
	}
	for name, p := range params {
		if _, ok := p["$defs"]; ok {
			t.Errorf("tool %q parameters carry $defs: the return contract is being shown as a call parameter", name)
		}
		// Belt and braces against a future document that spells the return
		// shape differently: the sent parameters must describe ONLY what the
		// caller supplies, and every tool's real parameters are a short list.
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("tool %q: re-marshal parameters: %v", name, err)
		}
		for _, returnOnly := range []string{"RunResult", "exitCode", "returned", "entryId"} {
			if schemaContainsKey(p, returnOnly) {
				t.Errorf("tool %q parameters carry result-only key %q: %s", name, returnOnly, encoded)
			}
		}
	}
}
