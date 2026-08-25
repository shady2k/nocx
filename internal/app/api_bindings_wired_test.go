package app

// api.import.postman reaches the real importer through the composition root.
// The import writes only collection files; credential-shaped values are
// deliberately omitted and itemised, so no vault binding seam is needed.
//
// The check is over the REAL socket against the REAL composition root,
// because the wiring exists nowhere else and a test that built its own
// WSServer would be asserting its own option list.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage/storagetest"
)

func TestAPIImportPostman_IsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	missing := filepath.Join(t.TempDir(), "no-such-export.json")
	resp := callAppWS(t, conn, "api.import.postman", map[string]any{
		"path": missing,
		"dest": filepath.Join(t.TempDir(), "acme"),
	}, 1)

	if resp.Error == nil {
		t.Fatalf("importing a document that does not exist succeeded: %s", resp.Result)
	}
	if resp.Error.Code == -32601 {
		t.Fatalf("api.import.postman answers -32601 (%s): the importer is not available", resp.Error.Message)
	}
	// Reached the importer rather than an availability gate: the failure is
	// about the document, which is what a person who mistyped a path needs
	// to read.
	if !strings.Contains(resp.Error.Message, "no-such-export.json") {
		t.Errorf("api.import.postman failed with %q, which does not name the document that could not be read",
			resp.Error.Message)
	}
}
