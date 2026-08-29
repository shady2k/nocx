package agenttools

import (
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestPresentationProjectionRebuildsEligibleSet(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	cfg := PresentationConfig{
		Lazy:             true,
		Essential:        []string{"files.read"},
		Loaded:           []string{"files.edit"},
		SchemaTokenLimit: 1,
	}
	mutating := content.Grant{
		Effects: []content.Effect{content.EffectMutateReversible},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	p := reg.Project(mutating, cfg)
	if got := toolNames(p.Visible); !reflect.DeepEqual(got, []string{"files.edit"}) {
		t.Fatalf("visible = %v, want loaded eligible tool", got)
	}
	if got := toolNames(p.Catalog); !reflect.DeepEqual(got, []string{"files.create"}) {
		t.Fatalf("catalog = %v, want other eligible tool", got)
	}

	observe := content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourcePath, ID: "/workspace"}},
	}
	p = reg.Project(observe, cfg)
	if got := toolNames(p.Visible); !reflect.DeepEqual(got, []string{"files.read"}) {
		t.Fatalf("visible after grant shrink = %v, want only current essential tool", got)
	}
	if got := toolNames(p.Catalog); len(got) != 0 {
		t.Fatalf("catalog after grant shrink = %v, want no hidden current tools", got)
	}
}

func TestPresentationSearchNeverEscapesGrant(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	grant := content.Grant{
		Effects: []content.Effect{content.EffectObserve},
		Scopes:  []content.GrantScope{{Kind: content.ResourceSession, ID: "lane-1"}},
	}
	cfg := PresentationConfig{Lazy: true, Essential: []string{}, SchemaTokenLimit: 1}
	p := reg.Project(grant, cfg)
	got := toolNames(reg.Search(grant, "read", p.Visible))
	want := []string{"session.read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("search = %v, want only eligible hidden tools %v", got, want)
	}
}
