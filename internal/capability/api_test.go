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

// apiPaths is a storage.Paths whose three roles land under one test root, so
// a created collection goes there rather than into the developer's own app
// directory.
type apiPaths struct{ root string }

func (p apiPaths) ConfigDir() string { return filepath.Join(p.root, "config") }
func (p apiPaths) DataDir() string   { return filepath.Join(p.root, "data") }
func (p apiPaths) CacheDir() string  { return filepath.Join(p.root, "cache") }

// newAPIOperation builds a collection operation over a real folder service
// with generous gates — this file is about the operation's contract, not
// about saturation.
func newAPIOperation(t *testing.T) capability.APICollectionOperation {
	t.Helper()
	return capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewCollections(apiPaths{root: t.TempDir()}),
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
	if _, err := escaped.Snapshot(handle, "ping.json", ""); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Snapshot outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, err := escaped.Create("later"); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Create outside the operation = %v, want ErrOperationInactive", err)
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
		if _, err := svc.Snapshot(h, "ping.json", ""); !errors.Is(err, apicoll.ErrUnknownHandle) {
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

// ─── creating one ──────────────────────────────────────────────────────────

// "Just make one" (§6.1) as the user does it: they name a collection and it
// is OPEN afterwards — in the list, addressable by the handle they were
// given, with somewhere to put a request. Before this the app could open a
// collection folder and never make one, so the first thing a new user could
// do with the pane was nothing.
func TestAPICollectionService_CreateLeavesTheCollectionOpen(t *testing.T) {
	op := newAPIOperation(t)

	var made apicoll.Created
	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		var err error
		made, err = svc.Create("acme")
		return err
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if made.Handle == "" || made.Root == "" {
		t.Fatalf("Create returned %+v, want a handle and a root", made)
	}
	if made.Collection.Name != "acme" {
		t.Errorf("name = %q, want acme", made.Collection.Name)
	}

	// It is in the opened-folder list, under the path it was created at —
	// which is what the renderer lists and what keys the cookie jar.
	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		open, err := svc.ListOpen()
		if err != nil {
			return err
		}
		if len(open) != 1 {
			t.Fatalf("opened folders = %+v, want the one just created", open)
		}
		if open[0].Handle != made.Handle || open[0].Path != made.Root {
			t.Errorf("listed %+v, want handle %q at %q", open[0], made.Handle, made.Root)
		}
		if open[0].Err != nil {
			t.Errorf("the created folder reports %v; it must be readable", open[0].Err)
		}
		// And the handle works for the next thing a user does.
		return svc.WriteRequest(made.Handle, "ping.json",
			apicoll.Request{ID: "r1", Name: "ping", Method: "GET", URL: "https://example.test/"})
	}); err != nil {
		t.Fatalf("after Create: %v", err)
	}
}

// A second collection under a name already taken is REFUSED, and the
// opened-folder list does not grow: a create that half-happened would leave
// a row naming a folder nobody made.
func TestAPICollectionService_CreateRefusesAnExistingNameAndListsNothingExtra(t *testing.T) {
	op := newAPIOperation(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		if _, err := svc.Create("acme"); err != nil {
			return err
		}
		if _, err := svc.Create("acme"); !errors.Is(err, apicoll.ErrCollectionExists) {
			t.Fatalf("second Create: err = %v, want ErrCollectionExists", err)
		}
		open, err := svc.ListOpen()
		if err != nil {
			return err
		}
		if len(open) != 1 {
			t.Errorf("opened folders = %+v, want exactly the one that was created", open)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// ─── the environment reaches the send ──────────────────────────────────────

// apiFolderWithEnvironments writes a collection whose request is written in
// variables and whose two environments answer WHERE and HOW TO GET THERE in
// one record (§6.5).
func apiFolderWithEnvironments(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(apicoll.ManifestName, `{"schemaVersion":1,"name":"acme"}`)
	write("users.json", `{"id":"r1","name":"users","method":"GET","url":"{{baseUrl}}/users",`+
		`"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json",
		`{"name":"dev","values":{"baseUrl":"http://localhost:3000"},"route":{"kind":"direct"}}`)
	write("environments/prod.json",
		`{"name":"prod","values":{"baseUrl":"https://api.internal"},"route":{"kind":"connection","profileId":"ssh:bastion:1"}}`)
	write("environments/broken.json",
		`{"name":"broken","values":{},"route":{"kind":"direct"}}`)
	return root
}

// Switching environment moves the ADDRESS and the ROUTE together, in one
// motion (§6.5's second consequence). One request, two environments, and
// both facts change at once — which is the property that cannot hold if the
// route lives anywhere but on the environment.
func TestAPICollectionService_SnapshotMovesTheAddressAndTheRouteTogether(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolderWithEnvironments(t)

	for name, tc := range map[string]struct {
		env      string
		wantURL  string
		wantKind string
		wantID   string
	}{
		"dev is direct":       {"environments/dev.json", "http://localhost:3000/users", apicoll.RouteDirect, ""},
		"prod is the bastion": {"environments/prod.json", "https://api.internal/users", apicoll.RouteConnection, "ssh:bastion:1"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
				h, _, err := svc.Open(root)
				if err != nil {
					return err
				}
				in, err := svc.Snapshot(h, "users.json", tc.env)
				if err != nil {
					return err
				}
				if in.Request.URL != tc.wantURL {
					t.Errorf("URL = %q, want %q — the address did not move with the environment", in.Request.URL, tc.wantURL)
				}
				if in.Route.Kind != tc.wantKind || in.Route.ProfileID != tc.wantID {
					t.Errorf("route = %+v, want kind %q profile %q — the route did not move with the environment",
						in.Route, tc.wantKind, tc.wantID)
				}
				if in.CookieScope != root {
					t.Errorf("cookie scope = %q, want the collection %q", in.CookieScope, root)
				}
				return nil
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}

// No environment named is the direct route and the request exactly as the
// file has it: the pane can send before anybody has configured anything.
func TestAPICollectionService_SnapshotWithNoEnvironmentIsTheDirectRoute(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolder(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		h, _, err := svc.Open(root)
		if err != nil {
			return err
		}
		in, err := svc.Snapshot(h, "ping.json", "")
		if err != nil {
			return err
		}
		if in.Request.URL != "https://example.test" {
			t.Errorf("URL = %q, want the file's own", in.Request.URL)
		}
		if in.Route.Kind != apicoll.RouteDirect || in.Route.ProfileID != "" {
			t.Errorf("route = %+v, want the direct route", in.Route)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// An unresolved variable BLOCKS the send and names itself (§6.5). Not the
// literal braces on the wire, not an empty string quietly substituted — the
// snapshot never happens, so there is nothing plausible to send.
func TestAPICollectionService_SnapshotBlocksOnAnUnresolvedVariable(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolderWithEnvironments(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		h, _, err := svc.Open(root)
		if err != nil {
			return err
		}
		in, snapErr := svc.Snapshot(h, "users.json", "environments/broken.json")
		if !errors.Is(snapErr, apicoll.ErrUnresolvedVariable) {
			t.Fatalf("Snapshot = (%+v, %v), want ErrUnresolvedVariable", in, snapErr)
		}
		var unresolved *apicoll.UnresolvedError
		if !errors.As(snapErr, &unresolved) || len(unresolved.Uses) == 0 {
			t.Fatalf("err = %v, want it to name every unresolved variable", snapErr)
		}
		if unresolved.Uses[0].Name != "baseUrl" {
			t.Errorf("named %q, want baseUrl", unresolved.Uses[0].Name)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// The environment is addressed by handle plus a path inside the collection,
// like everything else (§13.1): a path that leaves the folder is refused by
// apicoll and the send never happens.
func TestAPICollectionService_SnapshotRefusesAnEnvironmentPathOutsideTheCollection(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolderWithEnvironments(t)

	for name, rel := range map[string]string{
		"escaping the folder": "../secrets.json",
		"not an environment":  "users.json",
		"not there at all":    "environments/nope.json",
	} {
		t.Run(name, func(t *testing.T) {
			if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
				h, _, err := svc.Open(root)
				if err != nil {
					return err
				}
				if in, snapErr := svc.Snapshot(h, "users.json", rel); snapErr == nil {
					t.Fatalf("Snapshot with envRelPath %q succeeded: %+v", rel, in)
				}
				return nil
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}
