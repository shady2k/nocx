package apicoll

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// failingStore is a storage.DocumentStore whose write always fails. Every
// external call this package makes has a test where that call fails
// (AGENTS.md testing rule 3); the atomic write is the one that cannot be made
// to fail by arranging the filesystem alone.
type failingStore struct{ err error }

func (s failingStore) Read(string, any) (bool, error) { return false, s.err }
func (s failingStore) Write(string, any) error        { return s.err }
func (s failingStore) Delete(string) error            { return s.err }

func TestWriteRequest_ReportsAFailingStore(t *testing.T) {
	svc, h, root := openTestCollection(t)
	boom := errors.New("disk went away")
	svc.docStoreFor = func(string) storage.DocumentStore { return failingStore{err: boom} }

	err := svc.WriteRequest(h, "r.json", Request{ID: "1", Name: "A", Method: "GET", URL: "http://x/"})
	if !errors.Is(err, boom) {
		t.Fatalf("WriteRequest: err = %v, want the store's error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "r.json")); statErr == nil {
		t.Error("the file exists although the write was reported as failed")
	}
}

func TestWriteFolderVariables_ReportsAFailingStore(t *testing.T) {
	svc, h, root := openTestCollection(t)
	boom := errors.New("disk went away")
	svc.docStoreFor = func(string) storage.DocumentStore { return failingStore{err: boom} }

	_, err := svc.WriteFolderVariables(h, "", []Param{{Name: "baseUrl", Value: "https://x", Enabled: true}})
	if !errors.Is(err, boom) {
		t.Fatalf("WriteFolderVariables: err = %v, want the store's error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, folderVariablesFileName)); statErr == nil {
		t.Error("the file exists although the write was reported as failed")
	}
}

// The paired success: the same call against the real store writes the file.
func TestWriteRequest_WritesTheFileOnTheHappyPath(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if err := svc.WriteRequest(h, "sub/r.json", Request{ID: "1", Name: "A", Method: "GET", URL: "http://x/"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(root, "sub", "r.json"))
	if err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("mode = %v, want a regular file", fi.Mode())
	}
}

// A parent that is a regular file: MkdirAll fails, and the failure is
// reported rather than swallowed.
func TestWriteRequest_ReportsAParentThatIsAFile(t *testing.T) {
	svc, h, root := openTestCollection(t)
	writeFile(t, root, "sub", "I am a file, not a directory")
	if err := svc.WriteRequest(h, "sub/r.json", Request{ID: "1"}); err == nil {
		t.Fatal("WriteRequest succeeded with a regular file where the parent directory must be")
	}
}

// The handle id comes from crypto/rand; when that fails Open reports it and
// mints nothing.
func TestOpen_ReportsAFailingIDSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	boom := errors.New("no entropy")
	svc := newService()
	svc.newID = func() (HandleID, error) { return "", boom }

	op, err := svc.Open(root)
	h := op.Handle
	if !errors.Is(err, boom) {
		t.Fatalf("Open: err = %v, want the id source's error", err)
	}
	if h != "" {
		t.Errorf("Open returned handle %q although the id could not be minted", h)
	}
	svc.mu.Lock()
	n := len(svc.handles)
	svc.mu.Unlock()
	if n != 0 {
		t.Errorf("%d handles were registered although Open failed", n)
	}
}

// A path that names a directory is not a request file, and reading it says so
// rather than surfacing a raw EISDIR.
func TestReadRequest_RefusesAPathThatIsNotARegularFile(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if err := os.MkdirAll(filepath.Join(root, "adir.json"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := svc.ReadRequest(h, "adir.json"); !errors.Is(err, ErrNotARequestPath) {
		t.Errorf("ReadRequest on a directory: err = %v, want ErrNotARequestPath", err)
	}
}

// An unreadable file is reported, not reported as absent: "there is nothing
// here" and "you may not look" are different answers and the product must not
// merge them.
func TestReadRequest_ReportsAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny this process")
	}
	svc, h, root := openTestCollection(t)
	p := filepath.Join(root, "locked.json")
	writeFile(t, root, "locked.json", requestJSON("1", "A", "GET", "http://x/a"))
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	_, err := svc.ReadRequest(h, "locked.json")
	if err == nil {
		t.Fatal("ReadRequest succeeded on an unreadable file")
	}
	if errors.Is(err, ErrRequestNotFound) {
		t.Errorf("an unreadable file was reported as missing: %v", err)
	}
}

// An unreadable manifest is refused, and not as "no manifest": the folder IS
// a collection, and saying otherwise invites the caller to offer to create one
// over the top of it.
func TestOpen_ReportsAnUnreadableManifest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny this process")
	}
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	p := filepath.Join(root, ManifestName)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	svc := newService()
	_, err := svc.Open(root)
	if err == nil {
		t.Fatal("Open succeeded with an unreadable manifest")
	}
	if errors.Is(err, ErrNoManifest) {
		t.Errorf("an unreadable manifest was reported as a missing one: %v", err)
	}
}

// The handle table is shared state; -race is the assertion.
func TestService_IsSafeUnderConcurrentUse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	svc := newService()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op, err := svc.Open(root)
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			h := op.Handle
			rel := filepath.Join("concurrent", string(rune('a'+i))+".json")
			if err := svc.WriteRequest(h, rel, Request{ID: "1", Name: "A", Method: "GET", URL: "http://x/"}); err != nil {
				t.Errorf("WriteRequest: %v", err)
				return
			}
			if _, err := svc.ReadRequest(h, rel); err != nil {
				t.Errorf("ReadRequest: %v", err)
			}
			if _, err := svc.List(h); err != nil {
				t.Errorf("List: %v", err)
			}
		}(i)
	}
	wg.Wait()
}
