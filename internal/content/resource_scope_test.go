package content_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestGrantScopeContainsContentSubScopes(t *testing.T) {
	contentRoot := content.GrantScope{Kind: content.ResourceContent, ID: "content"}
	noteA := content.GrantScope{Kind: content.ResourceContent, ID: "note/note-a"}
	noteB := content.GrantScope{Kind: content.ResourceContent, ID: "note/note-b"}
	snippet := content.GrantScope{Kind: content.ResourceContent, ID: "snippet/snippet-a"}

	if !contentRoot.Contains(noteA) {
		t.Fatal("content scope does not contain a note sub-scope")
	}
	if noteA.Contains(contentRoot) {
		t.Fatal("note sub-scope contains its content parent")
	}
	if noteA.Contains(noteB) {
		t.Fatal("note sub-scope contains its sibling")
	}
	if noteA.Contains(snippet) {
		t.Fatal("note sub-scope contains a snippet sibling")
	}
}

func TestGrantScopeContainsPaths(t *testing.T) {
	path := func(id string) content.GrantScope {
		return content.GrantScope{Kind: content.ResourcePath, ID: id}
	}
	contentScope := content.GrantScope{Kind: content.ResourceContent, ID: "note/note-a"}

	cases := map[string]struct {
		parent content.GrantScope
		child  content.GrantScope
		want   bool
	}{
		"root contains descendant":             {path("/"), path("/etc"), true},
		"directory contains descendant":        {path("/a"), path("/a/b"), true},
		"directory contains itself":            {path("/a"), path("/a"), true},
		"prefix without separator is excluded": {path("/a"), path("/ab"), false},
		"child does not contain parent":        {path("/a/b"), path("/a"), false},
		"path does not contain content":        {path("/a"), contentScope, false},
		"content does not contain path":        {contentScope, path("/a"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.parent.Contains(tc.child); got != tc.want {
				t.Fatalf("%v.Contains(%v) = %v, want %v", tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

func TestGrantScopeContainsWorkspaceTabPaneSubScopes(t *testing.T) {
	workspace := content.GrantScope{Kind: content.ResourceWorkspace, ID: "workspace/ws-a"}
	tab := content.GrantScope{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-a"}
	pane := content.GrantScope{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-a/pane/pane-a"}
	siblingTab := content.GrantScope{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-b"}
	siblingPane := content.GrantScope{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-a/pane/pane-b"}
	otherWorkspace := content.GrantScope{Kind: content.ResourceWorkspace, ID: "workspace/ws-b"}

	cases := map[string]struct {
		parent content.GrantScope
		child  content.GrantScope
		want   bool
	}{
		"workspace contains tab":        {workspace, tab, true},
		"workspace contains pane":       {workspace, pane, true},
		"tab contains pane":             {tab, pane, true},
		"tab contains second pane":      {tab, siblingPane, true},
		"tab does not contain sibling":  {tab, siblingTab, false},
		"pane does not contain sibling": {pane, siblingPane, false},
		"pane does not contain parent":  {pane, tab, false},
		"tab does not contain parent":   {tab, workspace, false},
		"workspace does not cross":      {workspace, otherWorkspace, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.parent.Contains(tc.child); got != tc.want {
				t.Fatalf("%v.Contains(%v) = %v, want %v", tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

func TestGrantScopeCanonicalIDs(t *testing.T) {
	valid := []content.GrantScope{
		{Kind: content.ResourceContent, ID: "content"},
		{Kind: content.ResourceContent, ID: "note/note-a"},
		{Kind: content.ResourceContent, ID: "snippet/snippet-a"},
		{Kind: content.ResourceWorkspace, ID: "workspace/ws-a"},
		{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-a"},
		{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-a/pane/pane-a"},
	}
	for _, scope := range valid {
		if err := content.ValidateGrantScope(scope); err != nil {
			t.Errorf("ValidateGrantScope(%v): %v", scope, err)
		}
	}

	invalid := []content.GrantScope{
		{Kind: content.ResourceContent, ID: ""},
		{Kind: content.ResourceContent, ID: "note/"},
		{Kind: content.ResourceContent, ID: "document/note-a"},
		{Kind: content.ResourceWorkspace, ID: "ws-a"},
		{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/pane/pane-a"},
		{Kind: content.ResourceWorkspace, ID: "workspace/ws-a/tab/tab-a/pane/"},
	}
	for _, scope := range invalid {
		if err := content.ValidateGrantScope(scope); err == nil {
			t.Errorf("ValidateGrantScope(%v) accepted malformed canonical id", scope)
		}
	}
}

func TestScopedNoteGrantPermitsOnlyThatNote(t *testing.T) {
	grant := content.GrantScope{Kind: content.ResourceContent, ID: "note/note-a"}
	noteA := content.GrantScope{Kind: content.ResourceContent, ID: "note/note-a"}
	noteB := content.GrantScope{Kind: content.ResourceContent, ID: "note/note-b"}

	if !grant.Contains(noteA) {
		t.Fatal("a note grant refused its own note")
	}
	if grant.Contains(noteB) {
		t.Fatal("a note grant permitted its sibling note")
	}
}

func TestContentRootContainsASkill(t *testing.T) {
	root := content.GrantScope{Kind: content.ResourceContent, ID: "content"}
	child := content.GrantScope{Kind: content.ResourceContent, ID: "skill/deploy"}
	if !root.Contains(child) {
		t.Fatal("the content root must contain a skill sub-scope")
	}
	if err := content.ValidateGrantScope(child); err != nil {
		t.Fatalf("ValidateGrantScope(skill/deploy) = %v", err)
	}
}

func TestGrantScopeContainsContentFamilyRoots(t *testing.T) {
	family := func(id string) content.GrantScope {
		return content.GrantScope{Kind: content.ResourceContent, ID: id}
	}
	cases := map[string]struct {
		parent content.GrantScope
		child  content.GrantScope
		want   bool
	}{
		"note root contains note":            {family("note"), family("note/note-a"), true},
		"snippet root contains snippet":      {family("snippet"), family("snippet/snippet-a"), true},
		"skill root contains skill":          {family("skill"), family("skill/deploy"), true},
		"note root excludes snippet":         {family("note"), family("snippet/snippet-a"), false},
		"skill root excludes note":           {family("skill"), family("note/note-a"), false},
		"item does not contain family root":  {family("skill/deploy"), family("skill"), false},
		"item does not contain sibling item": {family("skill/deploy"), family("skill/backup"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.parent.Contains(tc.child); got != tc.want {
				t.Fatalf("%v.Contains(%v) = %v, want %v", tc.parent, tc.child, got, tc.want)
			}
		})
	}
	for _, id := range []string{"note", "snippet", "skill"} {
		if err := content.ValidateGrantScope(family(id)); err != nil {
			t.Fatalf("ValidateGrantScope(%q) = %v", id, err)
		}
	}
}

// ── the destination endpoint scope (design §5.4; bead nocx-67byy) ────────

func endpoint(id string, includeSubdomains bool) content.GrantScope {
	return content.GrantScope{Kind: content.ResourceDestination, ID: id, IncludeSubdomains: includeSubdomains}
}

func destination(rawURL string) content.GrantScope {
	return content.GrantScope{Kind: content.ResourceDestination, ID: rawURL}
}

// The nine rows are the whole of what "everything on github.com" means. The
// two suffix-alikes are why matching is label-wise and never a string
// suffix: both end in the parent's name and neither is inside it.
func TestDestinationEndpointContainment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  content.GrantScope
		url    string
		inside bool
	}{
		{"a path under the endpoint", endpoint("https://github.com", false), "https://github.com/owner/repo", true},
		{"a subdomain without the marker", endpoint("https://github.com", false), "https://api.github.com/x", false},
		{"a subdomain with the marker", endpoint("https://github.com", true), "https://api.github.com/x", true},
		{"a name that merely shares a suffix", endpoint("https://github.com", true), "https://notgithub.com/x", false},
		{"a longer name ending elsewhere", endpoint("https://github.com", true), "https://github.com.evil.example/x", false},
		{"a scheme downgrade", endpoint("https://github.com", true), "http://github.com/x", false},
		{"the default port, spelled", endpoint("https://github.com", false), "https://github.com:443/x", true},
		{"a non-default port", endpoint("https://github.com:8443", false), "https://github.com/x", false},
		{"case and a trailing dot", endpoint("https://GitHub.com.", false), "https://github.com/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.scope.Contains(destination(tc.url))
			if got != tc.inside {
				t.Errorf("Contains(%q) = %v, want %v", tc.url, got, tc.inside)
			}
		})
	}
}

// A deeper subdomain is still inside, and the endpoint itself is inside
// itself with the marker on — a grant that named the host must not stop
// covering the host.
func TestDestinationSubdomainMarkerCoversDepthAndTheHostItself(t *testing.T) {
	scope := endpoint("https://github.com", true)
	for _, in := range []string{
		"https://github.com/x",
		"https://api.github.com/x",
		"https://a.b.api.github.com/x",
		"https://api.github.com:443/x",
	} {
		if !scope.Contains(destination(in)) {
			t.Errorf("Contains(%q) = false, want true", in)
		}
	}
	for _, out := range []string{
		"https://github.com.evil.example/x",
		"https://xgithub.com/x",
		"https://api.github.com:8443/x",
		"http://api.github.com/x",
	} {
		if scope.Contains(destination(out)) {
			t.Errorf("Contains(%q) = true, want false", out)
		}
	}
}

// Host normalization is the same on both ends of the comparison: case, a
// trailing dot, IDNA and IPv6 brackets all name one place.
func TestDestinationHostNormalization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  content.GrantScope
		url    string
		inside bool
	}{
		{"punycode scope, unicode url", endpoint("https://xn--e1afmkfd.xn--p1ai", false), "https://пример.рф/x", true},
		{"unicode scope, punycode url", endpoint("https://пример.рф", false), "https://xn--e1afmkfd.xn--p1ai/x", true},
		{"unicode scope, other host", endpoint("https://пример.рф", false), "https://example.com/x", false},
		{"ipv6 brackets", endpoint("https://[::1]:8443", false), "https://[0:0:0:0:0:0:0:1]:8443/x", true},
		{"ipv4 literal", endpoint("http://127.0.0.1:8080", false), "http://127.0.0.1:8080/x", true},
		{"ipv4 literal, other port", endpoint("http://127.0.0.1:8080", false), "http://127.0.0.1:9090/x", false},
		{"trailing dot on the url", endpoint("https://github.com", false), "https://GitHub.com./x", true},
		{"http default port, spelled", endpoint("http://127.0.0.1", false), "http://127.0.0.1:80/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Contains(destination(tc.url)); got != tc.inside {
				t.Errorf("Contains(%q) = %v, want %v", tc.url, got, tc.inside)
			}
		})
	}
}

// * keeps its meaning, and an endpoint grant is not it. The asymmetry is the
// point: * contains an endpoint, and no endpoint contains *.
func TestDestinationStarStaysDistinguishableFromAnEndpoint(t *testing.T) {
	star := endpoint("*", false)
	gh := endpoint("https://github.com", true)

	if !star.Contains(destination("https://anything.example/x")) {
		t.Error("* stopped meaning every address")
	}
	if !star.Contains(gh) {
		t.Error("* does not contain an endpoint grant")
	}
	if gh.Contains(star) {
		t.Error("an endpoint grant contains *; it would be indistinguishable from the whole internet")
	}
	if err := content.ValidateGrantScope(endpoint("*", true)); err == nil {
		t.Error("* with includeSubdomains was accepted; it is incoherent, not a wider *")
	}
}

// A child that itself claims subdomains is only inside a parent that claims
// them too — otherwise a narrowing comparison would read a wider scope as
// contained in a narrower one.
func TestDestinationSubdomainMarkedChildNeedsAMarkedParent(t *testing.T) {
	if endpoint("https://github.com", false).Contains(endpoint("https://github.com", true)) {
		t.Error("a bare host scope contains the same host WITH its subdomains")
	}
	if !endpoint("https://github.com", true).Contains(endpoint("https://api.github.com", true)) {
		t.Error("a subdomain-marked scope does not contain a marked sub-scope")
	}
}

func TestDestinationScopeCanonicalIDs(t *testing.T) {
	valid := []content.GrantScope{
		endpoint("*", false),
		endpoint("https://github.com", false),
		endpoint("https://github.com", true),
		endpoint("https://github.com/", false),
		endpoint("https://GitHub.com.", true),
		endpoint("https://github.com:8443", false),
		endpoint("http://127.0.0.1:8080", false),
		endpoint("https://[::1]:8443", false),
		endpoint("https://пример.рф", true),
	}
	for _, scope := range valid {
		if err := content.ValidateGrantScope(scope); err != nil {
			t.Errorf("ValidateGrantScope(%v): %v", scope, err)
		}
	}

	invalid := map[string]content.GrantScope{
		"userinfo":              endpoint("https://user:token@github.com", false),
		"bare user":             endpoint("https://user@github.com", false),
		"a path":                endpoint("https://github.com/owner/repo", false),
		"a query":               endpoint("https://github.com?a=b", false),
		"a fragment":            endpoint("https://github.com#frag", false),
		"no scheme":             endpoint("github.com", false),
		"a scheme we never GET": endpoint("ftp://github.com", false),
		"no host":               endpoint("https://", false),
		"a port that is not":    endpoint("https://github.com:https", false),
		"ipv4 with subdomains":  endpoint("http://127.0.0.1:8080", true),
		"ipv6 with subdomains":  endpoint("https://[::1]", true),
		"star with subdomains":  endpoint("*", true),
	}
	for name, scope := range invalid {
		if err := content.ValidateGrantScope(scope); err == nil {
			t.Errorf("ValidateGrantScope(%s: %v) accepted a scope that cannot be enforced", name, scope)
		}
	}
}

// A malformed scope contains nothing — the predicate fails toward refusing,
// never toward the exact-identity fallback it used to share with the other
// singleton kinds.
func TestDestinationMalformedScopeContainsNothing(t *testing.T) {
	bad := endpoint("https://user@github.com", false)
	if bad.Contains(destination("https://user@github.com")) {
		t.Error("a scope ValidateGrantScope refuses still contained a resource")
	}
	if endpoint("https://github.com", false).Contains(destination("not a url")) {
		t.Error("an unparseable resource was contained")
	}
	if endpoint("https://github.com", false).Contains(content.GrantScope{Kind: content.ResourcePath, ID: "/etc"}) {
		t.Error("a destination scope contained a path")
	}
}
