package assistant

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/snippet"
)

type assistantNoteService struct {
	mu    sync.Mutex
	notes map[string]note.Note
	calls []string
}

func (s *assistantNoteService) List(context.Context) ([]note.Row, error) { return s.searchRows("") }
func (s *assistantNoteService) Get(_ context.Context, id string) (note.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	if !ok {
		return note.Note{}, note.ErrNotFound
	}
	return n, nil
}

func (s *assistantNoteService) Create(_ context.Context, body string) (note.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := note.Note{ID: "note-a", Body: body}
	s.notes[n.ID] = n
	s.calls = append(s.calls, "create")
	return n, nil
}

func (s *assistantNoteService) Update(_ context.Context, id, body string) (note.Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notes[id]
	if !ok {
		return note.Note{}, note.ErrNotFound
	}
	n.Body = body
	s.notes[id] = n
	s.calls = append(s.calls, "update")
	return n, nil
}

func (s *assistantNoteService) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.notes[id]; !ok {
		return note.ErrNotFound
	}
	delete(s.notes, id)
	s.calls = append(s.calls, "delete")
	return nil
}

func (s *assistantNoteService) Search(_ context.Context, query string) ([]note.Row, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "search")
	s.mu.Unlock()
	return s.searchRows(query)
}

func (s *assistantNoteService) searchRows(query string) ([]note.Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []note.Row
	for _, n := range s.notes {
		if query != "" && !strings.Contains(n.Body, query) {
			continue
		}
		rows = append(rows, note.Row{ID: n.ID, Excerpt: n.Body})
	}
	return rows, nil
}

type assistantSnippetService struct {
	mu        sync.Mutex
	snippets  map[string]snippet.Snippet
	order     []string
	reordered []string
	calls     []string
}

func (s *assistantSnippetService) List() ([]snippet.Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "list")
	out := make([]snippet.Snippet, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.snippets[id])
	}
	return out, nil
}

func (s *assistantSnippetService) Create(title, body string) (snippet.Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := snippet.Snippet{ID: "snippet-a", Title: title, Body: body}
	s.snippets[n.ID] = n
	s.order = append(s.order, n.ID)
	s.calls = append(s.calls, "create")
	return n, nil
}

func (s *assistantSnippetService) Update(id, title, body string) (snippet.Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.snippets[id]
	if !ok {
		return snippet.Snippet{}, snippet.ErrNotFound
	}
	n.Title, n.Body = title, body
	s.snippets[id] = n
	s.calls = append(s.calls, "update")
	return n, nil
}

func (s *assistantSnippetService) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snippets[id]; !ok {
		return snippet.ErrNotFound
	}
	delete(s.snippets, id)
	for i, candidate := range s.order {
		if candidate == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.calls = append(s.calls, "delete")
	return nil
}

func (s *assistantSnippetService) Reorder(ids []string) ([]snippet.Snippet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]snippet.Snippet, 0, len(ids))
	for _, id := range ids {
		item, ok := s.snippets[id]
		if !ok {
			return nil, snippet.ErrNotFound
		}
		out = append(out, item)
	}
	s.order = append([]string(nil), ids...)
	s.reordered = append([]string(nil), ids...)
	s.calls = append(s.calls, "reorder")
	return out, nil
}

type assistantNoteOperation struct{ service capability.NoteService }

func (o assistantNoteOperation) Disposition() capability.Disposition {
	return capability.Direct("NoteOperation")
}

func (o assistantNoteOperation) Run(ctx context.Context, fn func(context.Context, capability.NoteService) error) error {
	return fn(ctx, o.service)
}

type assistantSnippetOperation struct{ service capability.SnippetService }

func (o assistantSnippetOperation) Disposition() capability.Disposition {
	return capability.Direct("SnippetOperation")
}

func (o assistantSnippetOperation) Run(ctx context.Context, fn func(context.Context, capability.SnippetService) error) error {
	return fn(ctx, o.service)
}

func TestAsk_NotesAndSnippetsUseOperationsForCreateFindEditDelete(t *testing.T) {
	notes := &assistantNoteService{notes: make(map[string]note.Note)}
	snippets := &assistantSnippetService{snippets: make(map[string]snippet.Snippet)}
	sequence := []toolCallSpec{
		{name: "notes.create", args: `{"body":"remember the red train"}`},
		{name: "notes.search", args: `{"query":"red train"}`},
		{name: "notes.update", args: `{"id":"note-a","body":"remember the blue train"}`},
		{name: "notes.delete", args: `{"id":"note-a"}`},
		{name: "snippets.create", args: `{"title":"launch","body":"go run ./cmd"}`},
		{name: "snippets.list", args: `{}`},
		{name: "snippets.reorder", args: `{"ids":["snippet-a"]}`},
		{name: "snippets.update", args: `{"id":"snippet-a","title":"launch","body":"go test ./..."}`},
		{name: "snippets.delete", args: `{"id":"snippet-a"}`},
	}
	var requests []string
	var requestsMu sync.Mutex
	requestNumber := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestsMu.Lock()
		requests = append(requests, string(body))
		requestNumber++
		n := requestNumber
		requestsMu.Unlock()
		if n == 1 {
			streamToolCalls(w, sequence...)
			return
		}
		streamOK(w)
	}
	_, srv := newFakeOpenAI(handler)
	defer srv.Close()

	cl, err := newClient(nil, os.DirFS(realToolsFS), nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourceContent, ID: "content"}})
	p := testAskParams(srv.URL)
	p.Grant = &g
	p.KnownMaterial = &fakeKnownMaterial{}
	p.AttemptLedger = &fakeLedger{}
	p.NoteOperation = assistantNoteOperation{service: notes}
	p.SnippetOperation = assistantSnippetOperation{service: snippets}
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got, want := strings.Join(notes.calls, ","), "create,search,update,delete"; got != want {
		t.Fatalf("note operation calls = %q, want %q", got, want)
	}
	if got, want := strings.Join(snippets.calls, ","), "create,list,reorder,update,delete"; got != want {
		t.Fatalf("snippet operation calls = %q, want %q", got, want)
	}
	if got, want := strings.Join(snippets.reordered, ","), "snippet-a"; got != want {
		t.Fatalf("reordered snippet ids = %q, want %q", got, want)
	}
	if len(notes.notes) != 0 || len(snippets.snippets) != 0 {
		t.Fatalf("libraries after create/find/edit/delete: notes=%v snippets=%v", notes.notes, snippets.snippets)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("model requests = %d, want tool proposal and result follow-up", len(requests))
	}
	followUp := requests[1]
	for _, want := range []string{"remember the red train", "remember the blue train", "go run ./cmd", "go test ./...", `\"status\":\"deleted\"`, `\"status\":\"updated\"`} {
		if !strings.Contains(followUp, want) {
			t.Fatalf("follow-up model request omitted tool result %q: %s", want, followUp)
		}
	}
}

func TestAsk_NoteScopeRefusesSibling(t *testing.T) {
	ran := false
	notes := &assistantNoteService{notes: map[string]note.Note{
		"note-a": {ID: "note-a", Body: "a"},
		"note-b": {ID: "note-b", Body: "b"},
	}}
	op := assistantNoteOperation{service: noteServiceSpy{assistantNoteService: notes, ran: &ran}}
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{name: "notes.update", args: `{"id":"note-b","body":"changed"}`}))
	defer srv.Close()
	cl, err := newClient(nil, os.DirFS(realToolsFS), nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := autonomousMatrix().AsGrant([]content.GrantScope{{Kind: content.ResourceContent, ID: "note/note-a"}})
	p := testAskParams(srv.URL)
	p.Grant = &g
	p.KnownMaterial = &fakeKnownMaterial{}
	p.AttemptLedger = &fakeLedger{}
	p.NoteOperation = op
	if err := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ran {
		t.Fatal("sibling note operation ran despite note-a-only grant")
	}
}

func TestCreateAndUpdateSchemasRejectAmbiguousIDShapes(t *testing.T) {
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	cases := []struct {
		name     string
		toolName string
		good     map[string]any
		bad      map[string]any
	}{
		{"notes.create with id", "notes.create", map[string]any{"body": "body"}, map[string]any{"id": "note-a", "body": "body"}},
		{"notes.update without id", "notes.update", map[string]any{"id": "note-a", "body": "body"}, map[string]any{"body": "body"}},
		{"snippets.create with id", "snippets.create", map[string]any{"title": "title", "body": "body"}, map[string]any{"id": "snippet-a", "title": "title", "body": "body"}},
		{"snippets.update without id", "snippets.update", map[string]any{"id": "snippet-a", "title": "title", "body": "body"}, map[string]any{"title": "title", "body": "body"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := tc.toolName
			tool, ok := reg.Lookup(toolName)
			if !ok {
				t.Fatalf("tool %q is not registered", toolName)
			}
			schema, err := compileToolSchema(tool)
			if err != nil {
				t.Fatalf("compileToolSchema: %v", err)
			}
			if err := schema.Validate(tc.good); err != nil {
				t.Fatalf("%s rejected valid arguments: %v", toolName, err)
			}
			if err := schema.Validate(tc.bad); err == nil {
				t.Fatalf("%s accepted ambiguous arguments", toolName)
			}
		})
	}
}

type noteServiceSpy struct {
	*assistantNoteService
	ran *bool
}

func (s noteServiceSpy) Update(ctx context.Context, id, body string) (note.Note, error) {
	*s.ran = true
	return s.assistantNoteService.Update(ctx, id, body)
}
