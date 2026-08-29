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
