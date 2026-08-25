package apicoll

// A collection can be given a folder (§6.2), asserted as the thing a person
// does: they name a folder, it is there, and the tree can see it — including
// when there is nothing in it yet, which is the whole state a folder spends
// its first minute in.
//
// Every refusal below is paired with a success on the same service, because
// a test suite that only proves what is refused cannot report a feature
// that never worked (AGENTS.md testing rule 3).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/pathname"
)

func folderSet(t *testing.T, svc *service, h HandleID) map[string]bool {
	t.Helper()
	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := map[string]bool{}
	for _, f := range coll.Folders {
		out[f] = true
	}
	return out
}

// The happy path, and the whole motion: name a folder, get it back, see it
// in the listing, and put a request in it.
func TestCreateFolder_MakesAFolderTheTreeCanSee(t *testing.T) {
	svc, h, root := openTestCollection(t)

	made, err := svc.CreateFolder(h, "", "users")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if made.RelPath != "users" {
		t.Errorf("relPath = %q, want %q", made.RelPath, "users")
	}

	// It is in the collection the call itself answers with, so a caller
	// needs no second read to draw the tree.
	if !contains(made.Collection.Folders, "users") {
		t.Errorf("the collection CreateFolder answered lists %v, want it to contain users",
			made.Collection.Folders)
	}
	// And in the next listing, which is what api.collections.list reads.
	if !folderSet(t, svc, h)["users"] {
		t.Error("the folder is not in List's answer; a folder the tree cannot see does not exist to a person")
	}
	// It is a real directory on disk, with the collection's own posture.
	fi, err := os.Lstat(filepath.Join(root, "users"))
	if err != nil {
		t.Fatalf("stat the folder: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("users is %v, want a directory", fi.Mode())
	}

	// And it is a folder requests go in — the reason to make one at all.
	if err = svc.WriteRequest(h, "users/create.json",
		Request{ID: "r1", Name: "create", Method: "POST", URL: "https://example.test/users"}); err != nil {
		t.Fatalf("WriteRequest into the new folder: %v", err)
	}
	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(coll.Requests) != 1 || coll.Requests[0].RelPath != "users/create.json" {
		t.Errorf("requests = %+v, want the one written into the new folder", coll.Requests)
	}
	if !contains(coll.Folders, "users") {
		t.Errorf("folders = %v, want users to still be there once it holds a request", coll.Folders)
	}
}

// Nesting: a folder inside a folder, which is repeated calls rather than a
// path (createfolder.go says why).
func TestCreateFolder_MakesAFolderInsideAFolder(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	if _, err := svc.CreateFolder(h, "", "v1"); err != nil {
		t.Fatalf("CreateFolder v1: %v", err)
	}
	made, err := svc.CreateFolder(h, "v1", "users")
	if err != nil {
		t.Fatalf("CreateFolder v1/users: %v", err)
	}
	if made.RelPath != "v1/users" {
		t.Errorf("relPath = %q, want %q", made.RelPath, "v1/users")
	}
	// Three levels, each naming the one above by the relPath the call before
	// handed back — which is the grammar, stated as a test.
	deeper, err := svc.CreateFolder(h, made.RelPath, "admin")
	if err != nil {
		t.Fatalf("CreateFolder v1/users/admin: %v", err)
	}
	if deeper.RelPath != "v1/users/admin" {
		t.Errorf("relPath = %q, want %q", deeper.RelPath, "v1/users/admin")
	}

	folders := folderSet(t, svc, h)
	for _, want := range []string{"v1", "v1/users", "v1/users/admin"} {
		if !folders[want] {
			t.Errorf("List's folders = %v, want %q among them", folders, want)
		}
	}
}

// The parent has to be there. MkdirAll would have made `reports/janury`
// out of a misspelling and reported success.
func TestCreateFolder_RefusesAParentThatIsNotThere(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	_, err := svc.CreateFolder(h, "janury", "daily")
	if !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("CreateFolder under a missing parent = %v, want ErrFolderNotFound", err)
	}
	if folderSet(t, svc, h)["janury"] {
		t.Error("the missing parent was created anyway; nesting is repeated calls, not MkdirAll")
	}
	// And on an ordinary machine, with the parent made first, it succeeds.
	if _, err = svc.CreateFolder(h, "", "january"); err != nil {
		t.Fatalf("CreateFolder january: %v", err)
	}
	if _, err = svc.CreateFolder(h, "january", "daily"); err != nil {
		t.Fatalf("CreateFolder january/daily: %v", err)
	}
}

// A parent that is a FILE is not a folder, and says so rather than
// reporting a filesystem error nobody can act on.
func TestCreateFolder_RefusesAParentThatIsAFile(t *testing.T) {
	svc, h, root := openTestCollection(t)
	writeFile(t, root, "ping.json", requestJSON("1", "ping", "GET", "http://x/"))

	_, err := svc.CreateFolder(h, "ping.json", "sub")
	if !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("CreateFolder under a request file = %v, want ErrFolderNotFound", err)
	}
}

// A name already occupied is refused, never merged — the rule the import
// follows for its destination and NewDefaultCollection for a collection.
func TestCreateFolder_RefusesANameThatIsTaken(t *testing.T) {
	svc, h, root := openTestCollection(t)

	if _, err := svc.CreateFolder(h, "", "users"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	// Something is put in it, so a merge would be visible rather than
	// theoretical.
	if err := svc.WriteRequest(h, "users/create.json",
		Request{ID: "r1", Name: "create", Method: "POST", URL: "https://example.test/users"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	_, err := svc.CreateFolder(h, "", "users")
	if !errors.Is(err, ErrFolderExists) {
		t.Fatalf("CreateFolder over an existing folder = %v, want ErrFolderExists", err)
	}
	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(coll.Requests) != 1 {
		t.Errorf("requests = %+v, want the one already there to be untouched", coll.Requests)
	}

	// A FILE at that name is occupied too: a folder that replaced it would
	// be a request nobody deleted.
	writeFile(t, root, "ping.json", requestJSON("1", "ping", "GET", "http://x/"))
	if _, err = svc.CreateFolder(h, "", "ping.json"); !errors.Is(err, ErrFolderExists) {
		t.Fatalf("CreateFolder over a file = %v, want ErrFolderExists", err)
	}
}

// Every name that is not a single path component, refused BY NAME rather
// than sanitised — and one that is, accepted, on the same service.
func TestCreateFolder_RefusesANameThatIsNotOneComponent(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	cases := map[string]string{
		"empty":            "",
		"a path":           "a/b",
		"a traversal":      "..",
		"this directory":   ".",
		"only dots":        "...",
		"a hidden folder":  ".git",
		"a leading slash":  "/etc",
		"a NUL byte":       "us\x00ers",
		"longer than 128B": strings.Repeat("x", pathname.MaxComponentBytes+1),
	}
	for name, folder := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateFolder(h, "", folder)
			if !errors.Is(err, ErrInvalidFolderName) {
				t.Fatalf("CreateFolder(%q) = %v, want ErrInvalidFolderName", folder, err)
			}
			// A sentence a surface can show, not a bare sentinel.
			if msg := err.Error(); len(msg) <= len(ErrInvalidFolderName.Error()) {
				t.Errorf("the refusal is %q, which says nothing beyond the sentinel", msg)
			}
			if folderSet(t, svc, h)[folder] {
				t.Errorf("%q was created anyway", folder)
			}
		})
	}
	// Nothing was created by any of them, and an ordinary name still works.
	if len(folderSet(t, svc, h)) != 0 {
		t.Errorf("folders = %v, want none of the refused names to have landed", folderSet(t, svc, h))
	}
	if _, err := svc.CreateFolder(h, "", "users"); err != nil {
		t.Fatalf("CreateFolder with an ordinary name: %v", err)
	}
}

// `environments/` is the collection's, not a folder of requests (§6.2).
func TestCreateFolder_RefusesTheEnvironmentsDirectory(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	if _, err := svc.CreateFolder(h, "", environmentsDirName); !errors.Is(err, ErrInvalidFolderName) {
		t.Fatalf("CreateFolder(environments) = %v, want ErrInvalidFolderName", err)
	}
	// And nothing may be created UNDER it either — an environment is a file
	// in one flat directory, and a folder there would be a place the
	// environment listing never looks.
	if err := os.MkdirAll(filepath.Join(mustRoot(t, svc, h), environmentsDirName), 0o700); err != nil {
		t.Fatalf("mkdir environments: %v", err)
	}
	if _, err := svc.CreateFolder(h, environmentsDirName, "nested"); !errors.Is(err, ErrInvalidFolderName) {
		t.Fatalf("CreateFolder under environments = %v, want ErrInvalidFolderName", err)
	}
	// The same name one level down is an ordinary folder, because there it
	// means nothing special.
	if _, err := svc.CreateFolder(h, "", "v1"); err != nil {
		t.Fatalf("CreateFolder v1: %v", err)
	}
	if _, err := svc.CreateFolder(h, "v1", environmentsDirName); err != nil {
		t.Fatalf("CreateFolder v1/environments: %v", err)
	}
}

// A parent that leaves the collection is refused by the rule that already
// owns that question — the same sentinel every read and write answers with.
func TestCreateFolder_RefusesAParentOutsideTheCollection(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if err := os.MkdirAll(filepath.Join(root, "v1"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for name, parent := range map[string]string{
		"absolute":    "/etc",
		"climbing":    "../..",
		"climbing on": "v1/../../..",
		"not clean":   "v1/./sub",
		"a NUL byte":  "v\x001",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateFolder(h, parent, "planted")
			if !errors.Is(err, ErrPathOutsideCollection) && !errors.Is(err, ErrInvalidFolderName) {
				t.Fatalf("CreateFolder under %q = %v, want a refusal", parent, err)
			}
		})
	}
	if _, err := os.Lstat("/etc/planted"); err == nil {
		t.Fatal("a folder was created outside the collection")
	}
	// And the ordinary parent still works.
	if _, err := svc.CreateFolder(h, "v1", "planted"); err != nil {
		t.Fatalf("CreateFolder v1/planted: %v", err)
	}
}

// A parent reached through a symlink is refused, which is the guarantee
// resolveWithin already gives every read and write here: `dir ->
// /home/you/.ssh` would otherwise make a folder in somebody's home.
func TestCreateFolder_RefusesAParentThroughASymlink(t *testing.T) {
	svc, h, root := openTestCollection(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "away")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	_, err := svc.CreateFolder(h, "away", "planted")
	if !errors.Is(err, ErrPathOutsideCollection) {
		t.Fatalf("CreateFolder through a symlink = %v, want ErrPathOutsideCollection", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "planted")); statErr == nil {
		t.Fatal("the folder was created on the other side of the symlink")
	}
}

// The folder listing is the tree's, so it carries the user's folders and
// nothing else: `environments/` is the collection's own and a dot-directory
// is git's.
func TestList_FoldersExcludeEnvironmentsAndDotDirectories(t *testing.T) {
	svc, h, root := openTestCollection(t)
	for _, d := range []string{environmentsDirName, ".git", ".git/refs", "users", "users/admin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"users", "users/admin"}
	if len(coll.Folders) != len(want) {
		t.Fatalf("folders = %v, want exactly %v", coll.Folders, want)
	}
	for i, w := range want {
		if coll.Folders[i] != w {
			t.Errorf("folders[%d] = %q, want %q — parents before their children", i, coll.Folders[i], w)
		}
	}
}

// An empty collection says [] rather than null: a renderer walking null is
// a crash rather than an empty tree, which is the rule the other two lists
// on Collection already keep.
func TestList_FoldersAreEmptyRatherThanNull(t *testing.T) {
	svc, h, _ := openTestCollection(t)

	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if coll.Folders == nil {
		t.Fatal("folders is nil; a collection with no folders is [], never null")
	}
}

func contains(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// mustRoot is the folder behind a handle, for the test that has to arrange
// the filesystem underneath the surface.
func mustRoot(t *testing.T, svc *service, h HandleID) string {
	t.Helper()
	hd, err := svc.resolve(h)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return hd.root
}
