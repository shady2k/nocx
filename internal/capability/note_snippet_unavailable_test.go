package capability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/transport/control"
)

func TestNoteOperation_NilServiceReturnsError(t *testing.T) {
	op := NewNoteOperation(testUnavailableGate(), testUnavailableGate(), nil)
	err := op.Run(context.Background(), func(ctx context.Context, svc NoteService) error {
		_, err := svc.Create(ctx, "body")
		return err
	})
	if !errors.Is(err, ErrOperationUnavailable) {
		t.Fatalf("nil note service error = %v, want ErrOperationUnavailable", err)
	}
}

func TestSnippetOperation_NilServiceReturnsError(t *testing.T) {
	op := NewSnippetOperation(testUnavailableGate(), testUnavailableGate(), nil)
	err := op.Run(context.Background(), func(_ context.Context, svc SnippetService) error {
		_, err := svc.Create("title", "body")
		return err
	})
	if !errors.Is(err, ErrOperationUnavailable) {
		t.Fatalf("nil snippet service error = %v, want ErrOperationUnavailable", err)
	}
}

func testUnavailableGate() control.Admission {
	return Gate("unavailable-test", 1, 8, time.Second)
}

func TestNoteOperation_NonNilServiceReachesStore(t *testing.T) {
	op := NewNoteOperation(testUnavailableGate(), testUnavailableGate(),
		note.NewService(notePassthroughStore{}, func() string { return "note-1" }, func() time.Time { return time.Unix(1, 0) }))
	err := op.Run(context.Background(), func(ctx context.Context, svc NoteService) error {
		rows, err := svc.List(ctx)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ID != "note-1" {
			t.Fatalf("rows = %+v, want note-1", rows)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestSnippetOperation_NonNilServiceReachesStore(t *testing.T) {
	op := NewSnippetOperation(testUnavailableGate(), testUnavailableGate(),
		snippet.NewService(snippetPassthroughStore{}, func() string { return "snippet-1" }))
	err := op.Run(context.Background(), func(_ context.Context, svc SnippetService) error {
		rows, err := svc.List()
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ID != "snippet-1" {
			t.Fatalf("rows = %+v, want snippet-1", rows)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

type notePassthroughStore struct{}

func (notePassthroughStore) List(context.Context) ([]note.Row, error) {
	return []note.Row{{ID: "note-1"}}, nil
}
func (notePassthroughStore) LoadAll(context.Context) ([]note.Note, error)  { return nil, nil }
func (notePassthroughStore) ReplaceAll(context.Context, []note.Note) error { return nil }
func (notePassthroughStore) Get(context.Context, string) (note.Note, error) {
	return note.Note{ID: "note-1"}, nil
}

func (notePassthroughStore) Create(_ context.Context, n note.Note) (note.Note, error) {
	return n, nil
}

func (notePassthroughStore) Update(_ context.Context, n note.Note) (note.Note, error) {
	return n, nil
}
func (notePassthroughStore) Delete(context.Context, string) error { return nil }
func (notePassthroughStore) Search(context.Context, string) ([]note.Row, error) {
	return []note.Row{{ID: "note-1"}}, nil
}
func (notePassthroughStore) Close() error { return nil }

type snippetPassthroughStore struct{}

func (snippetPassthroughStore) LoadAll() ([]snippet.Snippet, error) {
	return []snippet.Snippet{{ID: "snippet-1"}}, nil
}
func (snippetPassthroughStore) SaveAll([]snippet.Snippet) error { return nil }
func (snippetPassthroughStore) Exists() (bool, error)           { return true, nil }
