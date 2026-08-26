package assistant

// What the model is told a tool is for. The description it reads used to be
// the ADR-0020 effect lattice rendered as a string — our vocabulary for
// authority, which says who may do a thing and nothing at all about what the
// thing does. These tests pin the replacement at both ends: the sentence the
// declaration carries reaches the model verbatim, and none of the authority
// vocabulary reaches it at all.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// toolDescriptions extracts name → description from the tools array the
// request actually carried, so the assertion is on what crossed the wire and
// not on what we intended to build.
func toolDescriptions(t *testing.T, body string) map[string]string {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("request body %q: %v", body, err)
	}
	out := make(map[string]string, len(req.Tools))
	for _, tl := range req.Tools {
		out[tl.Function.Name] = tl.Function.Description
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
		want[tl.Name] = tl.Description
	}

	f := askWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve, content.EffectMutateDestructive},
		Scopes: []content.GrantScope{
			{Kind: content.ResourcePath, ID: "/workspace"},
			{Kind: content.ResourceSession, ID: "lane-1"},
		},
	})
	got := toolDescriptions(t, f.body())
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
	f := askWithGrant(t, &content.Grant{
		Effects: []content.Effect{content.EffectObserve, content.EffectMutateDestructive},
		Scopes: []content.GrantScope{
			{Kind: content.ResourcePath, ID: "/workspace"},
			{Kind: content.ResourceSession, ID: "lane-1"},
		},
	})
	descriptions := toolDescriptions(t, f.body())
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
