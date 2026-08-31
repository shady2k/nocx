package snippet_test

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/snippet"
)

type memStore struct {
	list    []snippet.Snippet
	existed bool
	loadErr error
	saveErr error
	saves   int
}

func (m *memStore) LoadAll() ([]snippet.Snippet, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return append([]snippet.Snippet(nil), m.list...), nil
}

func (m *memStore) SaveAll(s []snippet.Snippet) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saves++
	m.list = append([]snippet.Snippet(nil), s...)
	m.existed = true
	return nil
}

func (m *memStore) Exists() (bool, error) { return m.existed, nil }

// counter is the injected id source: deterministic, and able to fail.
func counter() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("id-%d", n) }
}

func TestCreateMintsDistinctIDs(t *testing.T) {
	svc := snippet.NewService(&memStore{existed: true}, counter())
	a, err := svc.Create("one", "body one")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b, err := svc.Create("two", "body two")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("ids not distinct: %q %q", a.ID, b.ID)
	}
}

func TestReorderRejectsNonPermutationAndWritesNothing(t *testing.T) {
	m := &memStore{existed: true, list: []snippet.Snippet{{ID: "a"}, {ID: "b"}}}
	svc := snippet.NewService(m, counter())
	before := m.saves
	for name, ids := range map[string][]string{
		"missing an id": {"a"},
		"extra id":      {"a", "b", "c"},
		"duplicate":     {"a", "a"},
	} {
		if _, err := svc.Reorder(ids); !errors.Is(err, snippet.ErrNotAPermutation) {
			t.Fatalf("%s: want ErrNotAPermutation, got %v", name, err)
		}
	}
	if m.saves != before {
		t.Fatalf("a rejected reorder wrote %d times", m.saves-before)
	}
}

func TestReorderAppliesAPermutation(t *testing.T) {
	m := &memStore{existed: true, list: []snippet.Snippet{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	svc := snippet.NewService(m, counter())
	got, err := svc.Reorder([]string{"c", "a", "b"})
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if len(got) != 3 || got[0].ID != "c" || got[1].ID != "a" || got[2].ID != "b" {
		t.Fatalf("order not applied: %+v", got)
	}
}

func TestUpdateAndDeleteUnknownIDAreNotFound(t *testing.T) {
	m := &memStore{existed: true, list: []snippet.Snippet{{ID: "a"}}}
	svc := snippet.NewService(m, counter())
	before := m.saves
	if _, err := svc.Update("nope", "t", "b"); !errors.Is(err, snippet.ErrNotFound) {
		t.Fatalf("Update: want ErrNotFound, got %v", err)
	}
	if err := svc.Delete("nope"); !errors.Is(err, snippet.ErrNotFound) {
		t.Fatalf("Delete: want ErrNotFound, got %v", err)
	}
	if m.saves != before {
		t.Fatalf("a not-found mutation wrote %d times", m.saves-before)
	}
}

// Every external call has a failing case on every method (AGENTS.md rule 3).
// A table, because the interesting part is that none of them writes.
func TestEveryMutationPropagatesStoreFailures(t *testing.T) {
	boom := errors.New("store down")
	ops := map[string]func(*snippet.Service) error{
		"List":    func(s *snippet.Service) error { _, err := s.List(); return err },
		"Create":  func(s *snippet.Service) error { _, err := s.Create("t", "b"); return err },
		"Update":  func(s *snippet.Service) error { _, err := s.Update("a", "t", "b"); return err },
		"Delete":  func(s *snippet.Service) error { return s.Delete("a") },
		"Reorder": func(s *snippet.Service) error { _, err := s.Reorder([]string{"a"}); return err },
	}
	for name, op := range ops {
		load := &memStore{existed: true, list: []snippet.Snippet{{ID: "a"}}, loadErr: boom}
		if err := op(snippet.NewService(load, counter())); !errors.Is(err, boom) {
			t.Fatalf("%s on a failing load: want the store error, got %v", name, err)
		}
		if name == "List" {
			continue
		}
		save := &memStore{existed: true, list: []snippet.Snippet{{ID: "a"}}, saveErr: boom}
		if err := op(snippet.NewService(save, counter())); !errors.Is(err, boom) {
			t.Fatalf("%s on a failing save: want the store error, got %v", name, err)
		}
		if save.saves != 0 {
			t.Fatalf("%s: a failed save was counted as a write", name)
		}
	}
}

func TestSeedsOnFirstCreationOnly(t *testing.T) {
	m := &memStore{existed: false}
	svc := snippet.NewService(m, counter())
	first, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("want 2 seeds on a fresh document, got %d", len(first))
	}
	for _, s := range first {
		if err = svc.Delete(s.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	again, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("seeds came back: %+v", again)
	}
}

func TestCreateUpdateListRoundTrip(t *testing.T) {
	svc := snippet.NewService(&memStore{existed: true}, counter())
	created, err := svc.Create("title", "body")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err = svc.Update(created.ID, "new title", "new body"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Title != "new title" || list[0].Body != "new body" {
		t.Fatalf("round trip lost the edit: %+v", list)
	}
}

// TestSeedsFireInAnOrdinaryLocalPane is the rule the seeds' comment states,
// as an assertion: a seed may use {{env:cwd}} and parameters, and nothing
// else. The first pair used {{env:branch}} and {{env:host}} — null and
// empty respectively in a plain local shell — so the two examples a new
// user is handed both refused when they tried them (owner review). Nothing
// caught it, because a seed's CONTENT had no test at all.
func TestSeedsFireInAnOrdinaryLocalPane(t *testing.T) {
	svc := snippet.NewService(&memStore{existed: false}, counter())
	seeded, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(seeded) == 0 {
		t.Fatal("no seeds to check")
	}
	env := regexp.MustCompile(`\{\{env:([^}]*)\}\}`)
	// The second way a seed stops firing, and the one a condition
	// introduced: a body with a newline in it is refused outright by any
	// program that has not enabled bracketed paste. Text a condition can
	// switch off is allowed to be multi-line — the person asked for the
	// paragraph — but what is left when every tick is off must be one line,
	// or the seed refuses in an ordinary pane for a paragraph nobody chose.
	// Writing the tags on their own lines is what breaks this: a standalone
	// tag line takes its own newline and leaves the previous one behind.
	block := regexp.MustCompile(`(?s)\{%\s*if\b.*?\{%\s*endif\s*%\}`)
	for _, s := range seeded {
		for _, m := range env.FindAllStringSubmatch(s.Body, -1) {
			if m[1] != "cwd" {
				t.Errorf("seed %q uses {{env:%s}}, which a local pane cannot answer; "+
					"a seed may use {{env:cwd}} and parameters only", s.Title, m[1])
			}
		}
		if always := block.ReplaceAllString(s.Body, ""); strings.Contains(always, "\n") {
			t.Errorf("seed %q is multi-line with every condition off, so a program without "+
				"bracketed paste refuses it: %q", s.Title, always)
		}
	}
}

// TestSeedsUseNoRetiredSyntax guards the OTHER way a seed stops firing, and
// it is the way that actually happened. The ask: namespace was retired when
// a colon became what decides who owns a span (nocx-9xu1j): a question is
// written {{port=8080}} now, and {{ask:port=8080}} is ordinary literal text.
// The seeds were data, so no compiler and no type could notice — the second
// seed went on shipping a body that inserts its own placeholders verbatim.
//
// Written as a ban on the retired spellings rather than as an expected body,
// so it keeps its meaning when somebody rewrites a seed for a better reason.
func TestSeedsUseNoRetiredSyntax(t *testing.T) {
	svc := snippet.NewService(&memStore{existed: false}, counter())
	seeded, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(seeded) == 0 {
		t.Fatal("no seeds to check")
	}
	for _, s := range seeded {
		for _, retired := range []string{"{{ask:", "{{env:host}}", "{{env:user}}", "{{env:branch}}"} {
			if strings.Contains(s.Body, retired) {
				t.Errorf("seed %q uses %s, which no longer fires", s.Title, retired)
			}
		}
	}
}

// TestSeedsTeachTheSyntaxThatExists is the seeds' other job. They are the
// only documentation of the template language a person meets before they
// write one — Settings shows a preview of what you typed, not a grammar —
// so a form of the syntax that appears in no seed is a form nobody
// discovers. An option list and a condition were added by nocx-92kes and
// were in neither.
//
// It asserts the SHAPES rather than the bodies, so a seed can be rewritten
// for a better reason without this failing for the wrong one.
func TestSeedsTeachTheSyntaxThatExists(t *testing.T) {
	svc := snippet.NewService(&memStore{existed: false}, counter())
	seeded, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	all := ""
	for _, s := range seeded {
		all += s.Body + "\n"
	}
	for _, want := range []struct {
		what string
		re   string
	}{
		{"a parameter with a default", `\{\{[a-z_]+=[^|}]+\}\}`},
		{"an option list", `\{\{[a-z_]+=[^}]*\|[^}]*\}\}`},
		{"a condition", `\{%\s*if\s+[a-z_]+\s*%\}`},
		{"a condition that closes", `\{%\s*endif\s*%\}`},
	} {
		if !regexp.MustCompile(want.re).MatchString(all) {
			t.Errorf("no seed demonstrates %s (%s)", want.what, want.re)
		}
	}
}
