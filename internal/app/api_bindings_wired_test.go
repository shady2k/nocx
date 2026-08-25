package app

// The binding document at the composition root (design §8.1).
//
// api.import.postman is the one api.* method that writes a secret VALUE, and
// the transport answers -32601 for it unless it was handed a binding store.
// That is not a detail of the transport: apibind.NewStore is real and
// complete, and until app.New constructs it the whole import path is
// unreachable from main() — which is what the deadcode ratchet reported for
// all seventeen of that package's functions plus apiimport.FromPostman.
//
// The check is over the REAL socket against the REAL composition root,
// because the wiring exists nowhere else and a test that built its own
// WSServer would be asserting its own option list. It asserts the method is
// THERE and reaches the importer — not that an import succeeds: the happy
// path, a person choosing an export and the token landing in the vault, is
// the epic's own end-to-end check and needs an unsealed vault that this test
// deliberately does not stand up.

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
		t.Fatalf("api.import.postman answers -32601 (%s): the binding store is not wired, "+
			"so an imported token has nowhere to go but the collection folder", resp.Error.Message)
	}
	// Reached the importer rather than an availability gate: the failure is
	// about the document, which is what a person who mistyped a path needs
	// to read.
	if !strings.Contains(resp.Error.Message, "no-such-export.json") {
		t.Errorf("api.import.postman failed with %q, which does not name the document that could not be read",
			resp.Error.Message)
	}
}
