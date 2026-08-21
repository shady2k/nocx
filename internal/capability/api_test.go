package capability_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/capability"
)

// stubBindWriter is the narrow consumer contract the importer declares: one
// method, and the import test is not about where a secret ends up.
type stubBindWriter struct{}

func (stubBindWriter) Bind(context.Context, apibind.Key, []byte) error { return nil }

// newAPIOperation builds a collection operation over a real folder service
// with generous gates — this file is about the operation's contract, not
// about saturation.
func newAPIOperation(t *testing.T) capability.APICollectionOperation {
	t.Helper()
	return capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewService(),
	)
}

// apiFolder writes a real collection folder and returns its root.
func apiFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, apicoll.ManifestName),
		[]byte(`{"schemaVersion":1,"name":"acme"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ping.json"),
		[]byte(`{"id":"r1","name":"ping","method":"GET","url":"https://example.test",`+
			`"body":{"kind":"none"},"auth":{"kind":"none"}}`), 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return root
}

// A service that escapes its callback is dead on arrival. This is the whole
// reason the operation hands the service to a callback rather than
// returning it: a handler that stashed one could otherwise reach the folder
// with no gate held at all, at any later moment.
func TestAPICollectionService_IsUselessOutsideItsOperation(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolder(t)

	var escaped capability.APICollectionService
	var handle apicoll.HandleID
	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		escaped = svc
		h, _, err := svc.Open(root)
		handle = h
		return err
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := escaped.ListOpen(); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("ListOpen outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, _, err := escaped.Open(root); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Open outside the operation = %v, want ErrOperationInactive", err)
	}
	if err := escaped.Close(handle); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Close outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, err := escaped.ReadRequest(handle, "ping.json"); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("ReadRequest outside the operation = %v, want ErrOperationInactive", err)
	}
	if err := escaped.WriteRequest(handle, "ping.json", apicoll.Request{}); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("WriteRequest outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, err := escaped.Snapshot(handle, "ping.json"); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Snapshot outside the operation = %v, want ErrOperationInactive", err)
	}
}

// Close is a CLOSING EVENT, and the interval has both ends: a handle is
// usable from Open until the collection is closed, and from that moment
// every method naming it is refused. Without this the registry would be a
// list the UI reads and nothing enforces — apicoll's own table would still
// resolve the handle happily.
func TestAPICollectionService_CloseEndsTheHandlesInterval(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolder(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		h, coll, openErr := svc.Open(root)
		if openErr != nil {
			return openErr
		}
		if coll.Name != "acme" {
			t.Errorf("collection name = %q, want acme", coll.Name)
		}
		// Open: the handle works and the folder is listed.
		if _, err := svc.ReadRequest(h, "ping.json"); err != nil {
			t.Fatalf("ReadRequest on an open collection: %v", err)
		}
		open, listErr := svc.ListOpen()
		if listErr != nil {
			t.Fatalf("ListOpen: %v", listErr)
		}
		if len(open) != 1 || open[0].Handle != h || open[0].Path != root {
			t.Fatalf("opened-folder list = %+v, want the one folder", open)
		}

		if err := svc.Close(h); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Closed: every method naming it is refused, and the folder is gone
		// from the list.
		if _, err := svc.ReadRequest(h, "ping.json"); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("ReadRequest after Close = %v, want ErrUnknownHandle", err)
		}
		if err := svc.WriteRequest(h, "ping.json", apicoll.Request{}); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("WriteRequest after Close = %v, want ErrUnknownHandle", err)
		}
		if _, err := svc.Snapshot(h, "ping.json"); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("Snapshot after Close = %v, want ErrUnknownHandle", err)
		}
		if err := svc.Close(h); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("second Close = %v, want ErrUnknownHandle", err)
		}
		after, afterErr := svc.ListOpen()
		if afterErr != nil {
			t.Fatalf("ListOpen after Close: %v", afterErr)
		}
		if len(after) != 0 {
			t.Errorf("opened-folder list after Close = %+v, want empty", after)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// One folder is one entry however many times it is opened, and the previous
// handle stops resolving — otherwise re-opening the collection a user
// already has open would grow the list without bound.
func TestAPICollectionService_ReopeningReplacesTheEntry(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolder(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		first, _, err := svc.Open(root)
		if err != nil {
			return err
		}
		second, _, err := svc.Open(root)
		if err != nil {
			return err
		}
		if first == second {
			t.Fatal("re-opening minted the same handle; each open mints its own")
		}
		open, err := svc.ListOpen()
		if err != nil {
			return err
		}
		if len(open) != 1 {
			t.Errorf("opened-folder list = %+v, want one entry for one folder", open)
		}
		if _, err := svc.ReadRequest(first, "ping.json"); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("the superseded handle still resolves: %v", err)
		}
		if _, err := svc.ReadRequest(second, "ping.json"); err != nil {
			t.Errorf("the current handle does not resolve: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A folder whose root goes away is REPORTED on its entry, not dropped from
// the listing. A folder silently vanishing from the list is the soft
// degrade that makes a broken feature look absent.
func TestAPICollectionService_ListOpenReportsADeadFolderBesideALiveOne(t *testing.T) {
	op := newAPIOperation(t)
	live := apiFolder(t)
	doomed := apiFolder(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		if _, _, err := svc.Open(doomed); err != nil {
			return err
		}
		if _, _, err := svc.Open(live); err != nil {
			return err
		}
		if err := os.RemoveAll(doomed); err != nil {
			t.Fatalf("remove: %v", err)
		}
		open, err := svc.ListOpen()
		if err != nil {
			return err
		}
		if len(open) != 2 {
			t.Fatalf("opened-folder list = %+v, want both folders — one bad folder must not hide the good one", open)
		}
		if open[0].Path != doomed || open[0].Err == nil {
			t.Errorf("the removed folder listed as %+v, want its failure named", open[0])
		}
		if open[1].Path != live || open[1].Err != nil {
			t.Errorf("the live folder listed as %+v, want it read cleanly", open[1])
		}
		if open[1].Collection.Name != "acme" {
			t.Errorf("live collection name = %q, want acme", open[1].Collection.Name)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// The import's own external call: the document it is told to read. A path
// that is not a regular file is refused BY NAME rather than opened — a fifo
// would block the read for as long as nothing wrote to it, while holding
// both the vault and api gates.
func TestAPIImportService_RefusesADocumentThatIsNotAFile(t *testing.T) {
	op := capability.NewAPIImportOperation(
		capability.Gate(capability.GateVault, 1, 64, 5*time.Second),
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apiimport.NewOSFS(),
		stubBindWriter{},
	)
	dir := t.TempDir()

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		// A directory: nothing to parse.
		if _, err := svc.ImportPostman(ctx, dir, filepath.Join(t.TempDir(), "dest")); !errors.Is(err, capability.ErrImportNotAFile) {
			t.Errorf("ImportPostman(a directory) = %v, want ErrImportNotAFile", err)
		}
		// A document that is not there at all.
		missing := filepath.Join(dir, "not-there.json")
		if _, err := svc.ImportPostman(ctx, missing, filepath.Join(t.TempDir(), "dest")); err == nil {
			t.Error("ImportPostman(a missing document) succeeded")
		} else if errors.Is(err, capability.ErrImportNotAFile) {
			t.Errorf("a missing document was reported as a non-file: %v", err)
		}
		// And the success it is paired with: an ordinary regular file
		// parses and lands.
		doc := filepath.Join(dir, "export.json")
		if err := os.WriteFile(doc, []byte(`{"info":{"name":"acme",`+
			`"schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},`+
			`"item":[{"name":"ping","request":{"method":"GET","url":"https://example.test/ping"}}]}`),
			0o600); err != nil {
			t.Fatalf("write export: %v", err)
		}
		dest := filepath.Join(t.TempDir(), "dest")
		unsup, err := svc.ImportPostman(ctx, doc, dest)
		if err != nil {
			t.Fatalf("ImportPostman(an ordinary export): %v", err)
		}
		// A nil slice here is correct and deliberate: forcing [] is a WIRE
		// property, applied by the transport's wireUnsupported so the
		// renderer's first .map has something to walk. The domain layer
		// says "nothing was unsupported" the way Go says it.
		if len(unsup) != 0 {
			t.Errorf("unsupported = %+v, want nothing itemised for an export that converts whole", unsup)
		}
		if _, statErr := os.Lstat(dest); statErr != nil {
			t.Errorf("Lstat(%s) = %v, want the imported folder", dest, statErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
