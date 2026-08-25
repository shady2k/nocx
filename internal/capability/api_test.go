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
		// No binding store: a secret variable resolves to nothing, which is
		// what a build wired without one does.
		nil,
	)
}

// WHERE A NEW COLLECTION WOULD GO, so a surface can show a person before
// they commit to it — and the pair that makes it evidence: a service built
// with no app directory answers "", which is the same state Create refuses
// by name (apicoll.ErrNoDefaultLocation). Without the second half, a
// DefaultRoot that always answered "" would pass the first.
func TestAPICollectionService_DefaultRootIsWhereACreatedCollectionLands(t *testing.T) {
	root := t.TempDir()
	op := capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewCollections(apiPaths{root: root}),
		nil,
	)

	var got string
	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		// Create it, and ask where new ones go: the answer has to be the
		// directory the folder actually landed in, not a second opinion
		// about where they should.
		made, err := svc.Create("orders-api")
		if err != nil {
			return err
		}
		where, err := svc.DefaultRoot()
		if err != nil {
			return err
		}
		got = where
		if dir := filepath.Dir(made.Root); dir != where {
			t.Errorf("Create put the collection in %q while DefaultRoot says %q", dir, where)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got == "" {
		t.Fatal("DefaultRoot is empty on a service built with an app directory")
	}
	if filepath.Base(got) != apicoll.DefaultCollectionsDirName {
		t.Errorf("DefaultRoot = %q, want the collections directory", got)
	}

	// And the pair: no app directory, no default location, "" said plainly.
	none := capability.NewAPICollectionOperation(
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apicoll.NewCollections(nil),
		nil,
	)
	if err := none.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		where, err := svc.DefaultRoot()
		if err != nil {
			return err
		}
		if where != "" {
			t.Errorf("DefaultRoot = %q on a service with no app directory, want \"\"", where)
		}
		if _, createErr := svc.Create("orders-api"); !errors.Is(createErr, apicoll.ErrNoDefaultLocation) {
			t.Errorf("Create = %v, want ErrNoDefaultLocation — the same state DefaultRoot reports as \"\"", createErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
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
		opened, err := svc.Open(root)
		h := opened.Handle
		handle = h
		return err
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := escaped.ListOpen(); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("ListOpen outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, err := escaped.Open(root); !errors.Is(err, capability.ErrOperationInactive) {
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
	if _, err := escaped.Snapshot(context.Background(), handle, "ping.json", ""); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Snapshot outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, err := escaped.Create("later"); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("Create outside the operation = %v, want ErrOperationInactive", err)
	}
	if _, err := escaped.CreateFolder(handle, "", "later"); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("CreateFolder outside the operation = %v, want ErrOperationInactive", err)
	}
	// It reaches no folder and answers a derived path, and it is still
	// refused: one method that kept answering would be an exception to "a
	// service that escaped is useless" that somebody has to remember.
	if _, err := escaped.DefaultRoot(); !errors.Is(err, capability.ErrOperationInactive) {
		t.Errorf("DefaultRoot outside the operation = %v, want ErrOperationInactive", err)
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
		opened, openErr := svc.Open(root)
		if openErr != nil {
			return openErr
		}
		h, coll := opened.Handle, opened.Collection
		if coll.Name != "acme" {
			t.Errorf("collection name = %q, want acme", coll.Name)
		}
		// Open: the handle works and the folder is listed.
		if _, err := svc.ReadRequest(h, "ping.json"); err != nil {
			t.Fatalf("ReadRequest on an open collection: %v", err)
		}
		listed, listErr := svc.ListOpen()
		open := chosenFolders(listed)
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
		if _, err := svc.Snapshot(context.Background(), h, "ping.json", ""); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("Snapshot after Close = %v, want ErrUnknownHandle", err)
		}
		if _, err := svc.CreateFolder(h, "", "users"); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("CreateFolder after Close = %v, want ErrUnknownHandle", err)
		}
		if err := svc.Close(h); !errors.Is(err, apicoll.ErrUnknownHandle) {
			t.Errorf("second Close = %v, want ErrUnknownHandle", err)
		}
		listedAfter, afterErr := svc.ListOpen()
		after := chosenFolders(listedAfter)
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

// One folder is one entry however many times it is opened, and re-opening
// it answers with the handle it already has. The list does not grow, the
// tree does not draw the collection twice, and the answer SAYS it was
// already open so a surface can reveal the row it has rather than guess.
//
// The sequence is the ordinary one: the importer opens its destination, and
// the person then reaches for "Open a collection folder…" out of habit
// (nocx-ghuq3).
func TestAPICollectionService_ReopeningAnsweredWithTheHandleThatExists(t *testing.T) {
	op := newAPIOperation(t)
	root := apiFolder(t)
	// The SAME directory named a second way, which is what a dialog and an
	// importer legitimately disagree about.
	alias := filepath.Join(root, ".") + string(filepath.Separator)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		first, err := svc.Open(root)
		if err != nil {
			return err
		}
		if first.AlreadyOpen {
			t.Error("the first open reported AlreadyOpen; it is what opened the folder")
		}
		second, err := svc.Open(alias)
		if err != nil {
			return err
		}
		if second.Handle != first.Handle {
			t.Fatalf("re-opening minted %q beside %q; one folder has one handle", second.Handle, first.Handle)
		}
		if !second.AlreadyOpen {
			t.Error("re-opening reported AlreadyOpen=false; the surface cannot then tell an open from an already-open")
		}
		listed, err := svc.ListOpen()
		open := chosenFolders(listed)
		if err != nil {
			return err
		}
		if len(open) != 1 {
			t.Errorf("opened-folder list = %+v, want one entry for one folder", open)
		}
		if _, err := svc.ReadRequest(first.Handle, "ping.json"); err != nil {
			t.Errorf("the handle the folder was opened under does not resolve: %v", err)
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
		if _, err := svc.Open(doomed); err != nil {
			return err
		}
		if _, err := svc.Open(live); err != nil {
			return err
		}
		if err := os.RemoveAll(doomed); err != nil {
			t.Fatalf("remove: %v", err)
		}
		listed, err := svc.ListOpen()
		open := chosenFolders(listed)
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
		// No fetcher: this test is about the two entrances that reach no
		// network, and a build without one is a coherent build (it refuses
		// the URL entrance by name — api_import_url_test.go).
		nil,
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

// The document route: the same import from bytes the caller already holds.
//
// It exists because srcPath names a file on the machine running THIS
// process, which is the person's machine only in the desktop app — over a
// forwarded port it is the server's disk, and the export a person just
// downloaded is not on it. Its failure half is the one external call it
// makes: a document apiimport cannot parse.
func TestAPIImportService_ImportsADocumentItWasHandedTheBytesOf(t *testing.T) {
	op := capability.NewAPIImportOperation(
		capability.Gate(capability.GateVault, 1, 64, 5*time.Second),
		capability.Gate(capability.GateAPI, 1, 64, 5*time.Second),
		capability.Gate("lane", 8, 64, 5*time.Second),
		apiimport.NewOSFS(),
		stubBindWriter{},
		// No fetcher: this test is about the two entrances that reach no
		// network, and a build without one is a coherent build (it refuses
		// the URL entrance by name — api_import_url_test.go).
		nil,
	)
	const export = `{"info":{"name":"acme",` +
		`"schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},` +
		`"item":[{"name":"ping","request":{"method":"GET","url":"https://example.test/ping"}}]}`

	if err := op.Run(context.Background(), func(ctx context.Context, svc capability.APIImportService) error {
		dest := filepath.Join(t.TempDir(), "dest")
		unsup, err := svc.ImportPostmanDocument(ctx, export, dest)
		if err != nil {
			t.Fatalf("ImportPostmanDocument(an ordinary export): %v", err)
		}
		if len(unsup) != 0 {
			t.Errorf("unsupported = %+v, want nothing itemised for an export that converts whole", unsup)
		}
		if _, statErr := os.Lstat(filepath.Join(dest, "nocx-collection.json")); statErr != nil {
			t.Errorf("Lstat(the manifest) = %v, want the imported collection", statErr)
		}

		// And the failure: bytes that are not a Postman export. The
		// destination must not survive it — an import is one atomic
		// arrival on this route too.
		bad := filepath.Join(t.TempDir(), "bad")
		if _, err := svc.ImportPostmanDocument(ctx, "not a postman export", bad); err == nil {
			t.Error("ImportPostmanDocument(bytes that are not an export) succeeded")
		}
		if _, statErr := os.Lstat(bad); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want not-exist — a failed import leaves nothing behind", bad, statErr)
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
		listed, err := svc.ListOpen()
		open := chosenFolders(listed)
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

// A folder inside a collection somebody made in nocx, as a person reaches
// it: they name it, and it is in the listing the panel draws from — which
// is the difference between "a directory was created" and "the user has a
// folder". Before this the only folders in existence were the Postman
// importer's.
func TestAPICollectionService_CreateFolderShowsUpInTheListing(t *testing.T) {
	op := newAPIOperation(t)

	var made apicoll.Created
	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		var err error
		made, err = svc.Create("acme")
		if err != nil {
			return err
		}
		folder, err := svc.CreateFolder(made.Handle, "", "users")
		if err != nil {
			return err
		}
		if folder.RelPath != "users" {
			t.Errorf("relPath = %q, want users", folder.RelPath)
		}
		// And one inside it, which is what makes the tree a tree.
		nested, err := svc.CreateFolder(made.Handle, "users", "admin")
		if err != nil {
			return err
		}
		if nested.RelPath != "users/admin" {
			t.Errorf("nested relPath = %q, want users/admin", nested.RelPath)
		}
		return nil
	}); err != nil {
		t.Fatalf("Create then CreateFolder: %v", err)
	}

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		listed, err := svc.ListOpen()
		if err != nil {
			return err
		}
		open := chosenFolders(listed)
		if len(open) != 1 {
			t.Fatalf("opened folders = %+v, want the one created", open)
		}
		want := map[string]bool{"users": true, "users/admin": true}
		got := map[string]bool{}
		for _, f := range open[0].Collection.Folders {
			got[f] = true
		}
		for f := range want {
			if !got[f] {
				t.Errorf("the listing's folders = %v, want %q among them — a folder the tree "+
					"cannot see does not exist to a person", open[0].Collection.Folders, f)
			}
		}
		// Empty, both of them: the folders are visible because they are
		// LISTED, not because anything is in them.
		if len(open[0].Collection.Requests) != 0 {
			t.Errorf("requests = %+v, want none", open[0].Collection.Requests)
		}
		return nil
	}); err != nil {
		t.Fatalf("ListOpen: %v", err)
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
		listed, err := svc.ListOpen()
		open := chosenFolders(listed)
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
				opened, err := svc.Open(root)
				if err != nil {
					return err
				}
				h := opened.Handle
				in, err := svc.Snapshot(context.Background(), h, "users.json", tc.env)
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
		opened, err := svc.Open(root)
		if err != nil {
			return err
		}
		h := opened.Handle
		in, err := svc.Snapshot(context.Background(), h, "ping.json", "")
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
		opened, err := svc.Open(root)
		if err != nil {
			return err
		}
		h := opened.Handle
		in, snapErr := svc.Snapshot(context.Background(), h, "users.json", "environments/broken.json")
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
				opened, err := svc.Open(root)
				if err != nil {
					return err
				}
				h := opened.Handle
				if in, snapErr := svc.Snapshot(context.Background(), h, "users.json", rel); snapErr == nil {
					t.Fatalf("Snapshot with envRelPath %q succeeded: %+v", rel, in)
				}
				return nil
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}

// chosenFolders drops the BUILT-IN collection from a listing.
//
// `ListOpen` opens the Playground once per process before it answers
// (apiCollectionService.ensureStarter), because a panel with nothing in it
// asks a person to do administration before it will show them anything. Every
// test below is about the opened-folder LIST — what Open adds, what Close
// removes, what a re-open replaces — and the built-in row is not part of the
// question any of them asks. It has a test of its own instead, which is where
// that behaviour belongs.
func chosenFolders(open []capability.OpenCollection) []capability.OpenCollection {
	out := make([]capability.OpenCollection, 0, len(open))
	for _, c := range open {
		if filepath.Base(c.Path) == apicoll.StarterName {
			continue
		}
		out = append(out, c)
	}
	return out
}

// TestAPICollectionService_ListOpenOpensTheBuiltInCollection is that test.
//
// Both ends of the interval: the Playground is in the list from the first
// ListOpen of the process, and it stays there until somebody closes it — a
// close that must STICK, because the ensure runs once and a person who closed
// it is not to be argued with until the next start.
func TestAPICollectionService_ListOpenOpensTheBuiltInCollection(t *testing.T) {
	op := newAPIOperation(t)

	if err := op.Run(context.Background(), func(_ context.Context, svc capability.APICollectionService) error {
		open, err := svc.ListOpen()
		if err != nil {
			t.Fatalf("ListOpen: %v", err)
		}
		var starter *capability.OpenCollection
		for i := range open {
			if filepath.Base(open[i].Path) == apicoll.StarterName {
				starter = &open[i]
			}
		}
		if starter == nil {
			t.Fatalf("opened folders = %+v, want the built-in collection among them", open)
		}
		if starter.Err != nil {
			t.Fatalf("the built-in collection did not read: %v", starter.Err)
		}
		// It is seeded rather than empty: an empty collection is the same
		// blank surface one folder deeper.
		if len(starter.Collection.Requests) == 0 {
			t.Errorf("the built-in collection has no requests in it")
		}

		if closeErr := svc.Close(starter.Handle); closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
		after, afterErr := svc.ListOpen()
		if afterErr != nil {
			t.Fatalf("ListOpen after close: %v", afterErr)
		}
		if len(chosenFolders(after)) != len(after) {
			t.Errorf("opened folders = %+v, want the built-in one to stay closed", after)
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
