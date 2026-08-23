package apicoll

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A folder that is already open opens to the handle it already has. The
// second call is not a second collection: it answers with the identity that
// exists and says it did not open anything.
//
// The sequence this was measured on is the ordinary one — an import opens
// its destination, and the person then reaches for "Open a collection
// folder…" out of habit — and before this it put the same folder in the tree
// twice under two handles (nocx-ghuq3).
func TestOpen_OfAFolderAlreadyOpenAnswersWithTheHandleThatExists(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "ping.json", requestJSON("1", "ping", "GET", "http://x/ping"))
	svc := newService()

	first, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if first.AlreadyOpen {
		t.Error("the first open of a folder reported AlreadyOpen; it is what opened it")
	}

	second, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open again: %v", err)
	}
	if second.Handle != first.Handle {
		t.Errorf("re-opening minted %q beside %q; one folder has one handle", second.Handle, first.Handle)
	}
	if !second.AlreadyOpen {
		t.Error("re-opening reported AlreadyOpen=false; a surface cannot then tell an open from an already-open")
	}
	if len(second.Collection.Requests) != 1 {
		t.Errorf("the second open answered %+v, want the folder's one request", second.Collection.Requests)
	}
	if got := len(svc.handles); got != 1 {
		t.Errorf("the handle table holds %d entries for one folder, want 1", got)
	}
}

// Two DIFFERENT paths that lead to one directory are one collection. The
// path is what a caller can spell more than one way; the directory is not.
func TestOpen_TwoNamesForOneFolderAreOneCollection(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	svc := newService()
	first, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	for name, alias := range map[string]string{
		"a trailing slash": root + string(filepath.Separator),
		"a `.` segment":    filepath.Join(dir, ".", "coll"),
		"a `..` segment":   filepath.Join(root, "..", "coll"),
		"a symlink":        link,
	} {
		t.Run(name, func(t *testing.T) {
			again, err := svc.Open(alias)
			if err != nil {
				t.Fatalf("Open(%q): %v", alias, err)
			}
			if again.Handle != first.Handle {
				t.Errorf("Open(%q) minted %q beside %q; the two names lead to one directory",
					alias, again.Handle, first.Handle)
			}
			if !again.AlreadyOpen {
				t.Errorf("Open(%q) reported AlreadyOpen=false for a folder that is open", alias)
			}
		})
	}
	if got := len(svc.handles); got != 1 {
		t.Errorf("the handle table holds %d entries for one directory, want 1", got)
	}
}

// Two folders are two collections. The rule above is about ONE directory,
// and a service that answered every open with the first handle would pass
// every assertion in it.
func TestOpen_TwoFoldersAreTwoHandles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	writeFile(t, a, ManifestName, manifestJSON)
	writeFile(t, b, ManifestName, manifestJSON)

	svc := newService()
	first, err := svc.Open(a)
	if err != nil {
		t.Fatalf("Open(a): %v", err)
	}
	second, err := svc.Open(b)
	if err != nil {
		t.Fatalf("Open(b): %v", err)
	}
	if second.Handle == first.Handle {
		t.Fatalf("two folders share the handle %q", first.Handle)
	}
	if second.AlreadyOpen {
		t.Error("opening a second folder reported AlreadyOpen")
	}
}

// A handle whose NAME now leads somewhere else is not an answer. It matches
// the directory by identity and is still in the table, but every call on it
// is refused (resolve), so handing it back would answer an open that
// succeeded with a handle nothing can use.
func TestOpen_DoesNotAnswerWithAHandleWhoseNameNowLeadsElsewhere(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "coll")
	other := filepath.Join(dir, "other")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, other, ManifestName, manifestJSON)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	svc := newService()
	byLink, err := svc.Open(link)
	if err != nil {
		t.Fatalf("Open(link): %v", err)
	}
	// The name the first handle was opened under now leads to another
	// folder. The directory it opened is untouched and still itself.
	if err = os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err = os.Symlink(other, link); err != nil {
		t.Fatalf("re-point link: %v", err)
	}
	if _, err = svc.List(byLink.Handle); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("List on the re-pointed handle err = %v, want ErrRootChanged", err)
	}

	fresh, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open(root): %v", err)
	}
	if fresh.Handle == byLink.Handle {
		t.Error("the open answered with the handle whose name now leads elsewhere; every later call on it is refused")
	}
	if fresh.AlreadyOpen {
		t.Error("a folder whose only handle no longer resolves reported AlreadyOpen")
	}
	if _, err := svc.List(fresh.Handle); err != nil {
		t.Errorf("the fresh handle does not resolve: %v", err)
	}
}

// Close ends the handle's interval. The table forgets it, so the next open
// of that folder is an open rather than an already-open — otherwise a
// surface told "already open" would reveal a row it had just removed.
func TestClose_ForgetsTheHandleSoTheNextOpenIsAnOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	svc := newService()

	first, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err = svc.Close(first.Handle); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err = svc.List(first.Handle); !errors.Is(err, ErrUnknownHandle) {
		t.Errorf("List after Close err = %v, want ErrUnknownHandle", err)
	}
	if got := len(svc.handles); got != 0 {
		t.Errorf("the handle table holds %d entries after the only folder was closed, want 0", got)
	}

	again, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	if again.AlreadyOpen {
		t.Error("opening a folder that was closed reported AlreadyOpen")
	}
	if again.Handle == first.Handle {
		t.Error("the closed handle was minted again; a handle's interval ends at Close and does not resume")
	}
}

func TestClose_RefusesAHandleItNeverMinted(t *testing.T) {
	svc := newService()
	if err := svc.Close("0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrUnknownHandle) {
		t.Errorf("Close of an unminted handle err = %v, want ErrUnknownHandle", err)
	}
}

// The partial failures, enumerated. The one step that can fail between
// "this folder is not open" and "it is open under this id" is the mint, and
// the two halves of the table are what this names:
//
//   - A folder that is ALREADY open does not reach the mint at all, so a
//     re-open answers even when no id could be minted. That is the state a
//     surface must not be lied to about: the folder IS open, and the answer
//     says so.
//   - A folder that is NOT open and cannot be minted a handle leaves the
//     table exactly as it was — no row for it in any list, and every handle
//     that existed still works.
func TestOpen_AMintThatFailsChangesNothingThatWasAlreadyOpen(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "coll")
	fresh := filepath.Join(dir, "fresh")
	writeFile(t, root, ManifestName, manifestJSON)
	writeFile(t, root, "ping.json", requestJSON("1", "ping", "GET", "http://x/ping"))
	writeFile(t, fresh, ManifestName, manifestJSON)
	svc := newService()

	first, err := svc.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	boom := errors.New("no entropy")
	svc.newID = func() (HandleID, error) { return "", boom }

	again, err := svc.Open(root)
	if err != nil {
		t.Fatalf("re-opening an open folder needed a new id: %v", err)
	}
	if again.Handle != first.Handle || !again.AlreadyOpen {
		t.Errorf("re-open answered %+v, want the handle that exists and AlreadyOpen", again)
	}

	if _, err = svc.Open(fresh); !errors.Is(err, boom) {
		t.Fatalf("Open of a folder that is not open: err = %v, want the id source's error", err)
	}
	if got := len(svc.handles); got != 1 {
		t.Errorf("the handle table holds %d entries after a mint that failed, want the 1 that existed", got)
	}
	coll, err := svc.List(first.Handle)
	if err != nil {
		t.Fatalf("the handle that existed stopped working: %v", err)
	}
	if len(coll.Requests) != 1 {
		t.Errorf("the collection lists %+v, want its one request", coll.Requests)
	}
}

// Two opens of one folder at the same moment are one collection too. The
// check for an existing handle and the mint that may follow it are one
// decision under one lock; if they were two, both callers would find the
// folder unopened and both would mint — which is the defect, arrived at from
// the other direction.
//
// The api gate serialises collection callers at capacity 1 today. This must
// not depend on that: the gate's grain belongs to internal/capability and
// this table has to stay correct if it is ever raised.
func TestOpen_ConcurrentOpensOfOneFolderAreOneHandle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "coll")
	writeFile(t, root, ManifestName, manifestJSON)
	svc := newService()

	const callers = 8
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		out   = make([]Opened, callers)
		errs  = make([]error, callers)
	)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i], errs[i] = svc.Open(root)
		}()
	}
	close(start)
	wg.Wait()

	opens := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Open from caller %d: %v", i, err)
		}
		if out[i].Handle != out[0].Handle {
			t.Fatalf("caller %d got handle %q, caller 0 got %q; one folder has one handle",
				i, out[i].Handle, out[0].Handle)
		}
		if !out[i].AlreadyOpen {
			opens++
		}
	}
	if opens != 1 {
		t.Errorf("%d of %d callers reported opening the folder, want exactly 1", opens, callers)
	}
	if got := len(svc.handles); got != 1 {
		t.Errorf("the handle table holds %d entries after %d concurrent opens, want 1", got, callers)
	}
}
