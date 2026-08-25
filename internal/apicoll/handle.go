package apicoll

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/shady2k/nocx/internal/storage"
)

// Service is the collection folder's whole surface. Open is the only entry
// point that accepts a root; every later call names the backend-minted handle
// and a path relative to it, so the renderer cannot address a file by path
// twice (§13.1). That is the shape internal/filesystem already uses, and the
// reason is the same: backend-side parsing makes the CONTENTS safe to read
// and says nothing about WHICH file gets read.
//
// List returns the collection with its malformed files named on it rather
// than in an error — see Collection.Malformed.
type Service interface {
	Open(root string) (Opened, error)
	// Close ends one handle's interval. Every later call naming it is
	// refused, and the folder is no longer open — so the next Open of it
	// is an open rather than an already-open (Opened.AlreadyOpen).
	//
	// It is on the MINTER because the minter is what remembers: a list
	// kept anywhere else could forget a folder this table still answered
	// for, and the two would then disagree about whether a collection is
	// open — which is the same one-owner rule Open keeps.
	Close(h HandleID) error
	List(h HandleID) (Collection, error)
	ReadRequest(h HandleID, relPath string) (Request, error)
	// ReadFolderVariables reads the variables declared by one existing folder.
	// The collection root is named by an empty relPath.
	ReadFolderVariables(h HandleID, relPath string) ([]Param, error)
	// WriteFolderVariables replaces one folder's variables. An empty list
	// removes the reserved file, leaving the folder in its absent state.
	WriteFolderVariables(h HandleID, relPath string, variables []Param) ([]Param, error)
	WriteRequest(h HandleID, relPath string, r Request) error
	// DeleteRequest removes one request file. It is on Service rather than
	// in an interface of its own because it is addressed exactly as the two
	// accessors are — the handle plus a path inside it — and it is the same
	// property being asserted: a caller that cannot name a file cannot
	// delete one.
	DeleteRequest(h HandleID, relPath string) error
	// MoveRequest moves one request file to another path inside the SAME
	// collection, and answers the new relPath. It is on Service for
	// DeleteRequest's reason — it is addressed by the handle plus two paths
	// relative to it, so §13.1 holds — and it is ONE operation: a
	// no-replace rename, never a write-then-delete, so the file is at
	// exactly one of the two paths from before the call until after it.
	// The destination folder must already exist; making one is
	// CreateFolder's job.
	MoveRequest(h HandleID, fromRelPath, toRelPath string) (string, error)
	// CreateFolder makes one folder inside the collection: a NAME, and the
	// EXISTING folder to put it in ("" is the root). It is here for
	// DeleteRequest's reason — it is addressed by the handle plus a path
	// relative to it, so §13.1 holds — and its name is a component rather
	// than a path, which is api.collections.create's grammar one level
	// down (createfolder.go says why nesting is repeated calls).
	CreateFolder(h HandleID, parentRelPath, name string) (FolderCreated, error)
}

// Opened is one folder that is now open: the handle every later call names,
// what is in it, and whether this call is what opened it.
//
// AlreadyOpen is here because a folder can be asked for twice and there is
// only ever ONE handle for it — the import opens its destination, the person
// then reaches for "Open a collection folder…" out of habit, and the second
// call has to answer with the identity that exists. A surface deciding
// between "add a row" and "reveal the row that is there" needs to be told
// which happened; deriving it from the tree would make the renderer a second
// reader of collection identity, and identity has one owner: this package,
// which mints it (§13.1).
//
// It is a struct rather than a third return value for Created's reason
// (create.go): the two are one answer, and a caller that wants only the
// handle says so by name.
type Opened struct {
	Handle      HandleID
	Collection  Collection
	AlreadyOpen bool
}

// handle is one opened folder, held as three facts because a replaced root
// can break any one of them on its own:
//
//   - namedAs is the path the user chose, exactly as given.
//   - root is that path with every symlink resolved, so it is the folder's
//     canonical identity rather than the name that reached it — two symlinks
//     to one directory are one directory (`files.read`'s contract, §13.1).
//   - openedAs is that directory's identity at the moment it was opened, and
//     is what os.SameFile compares against on every later call.
type handle struct {
	root     string
	namedAs  string
	openedAs os.FileInfo
}

type service struct {
	mu      sync.Mutex
	handles map[HandleID]*handle

	// paths decides where a NEW collection goes (create.go). It is nil for
	// a service built by NewService, which reads folders the user chose and
	// mints none; Create then refuses by name rather than dereferencing it.
	paths storage.Paths

	// newID and docStoreFor are the two calls this package makes that
	// cannot be made to fail by arranging a temp directory. They are fields
	// so each has a test where it fails, paired with one where it succeeds.
	newID       func() (HandleID, error)
	docStoreFor func(dir string) storage.DocumentStore
}

// newService builds the one implementation. NewCollections (environment.go)
// is the only constructor this package exports: there is one folder service,
// it holds no state that outlives the process — the app remembers the LIST
// of opened folders, never their contents (§6.1), so handles are minted
// fresh each run and every read goes to disk — and Service remains a
// separate interface because §13.1's property, "Open is the only entry point
// that accepts a root", is asserted against its method set.
func newService() *service {
	return &service{
		handles:     make(map[HandleID]*handle),
		newID:       newHandleID,
		docStoreFor: storage.NewDocumentStore,
	}
}

// newHandleID mints an unguessable id from crypto/rand, the way
// internal/filesystem mints a binding id. It is not a bearer token — the
// handle is re-validated against the filesystem on every call — but an
// enumerable one would still let a renderer bug reach a folder it never
// opened.
func newHandleID() (HandleID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("apicoll: mint handle: %w", err)
	}
	return HandleID(hex.EncodeToString(b[:])), nil
}

// resolve turns a handle into the folder it names, and re-checks that the
// folder is still the one that was opened. §13.1's fourth rule: the root may
// be replaced between open and read, so open time is not a fact that stays
// true and the check belongs on every operation.
//
// The interval this closes, both ends named: a handle is usable from Open
// until either of two events, whichever comes first — the directory it was
// opened on stops being that directory (replaced, removed, or swapped for a
// symlink), from which moment every method on it reports ErrRootChanged
// rather than answering out of whatever now sits at the path; or Close
// forgets it, from which moment every method reports ErrUnknownHandle. The
// second end is why Close is on this package at all: while the table went on
// answering for a folder somebody had closed, the next Open of it came back
// "already open" naming a handle no list held.
func (s *service) resolve(h HandleID) (*handle, error) {
	s.mu.Lock()
	hd, ok := s.handles[h]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownHandle, h)
	}
	if err := hd.check(); err != nil {
		return nil, err
	}
	return hd, nil
}

// openHandleFor answers which open handle already names this directory, so
// Open hands back the handle that exists instead of minting a second
// identity for one folder.
//
// IDENTITY IS THE DIRECTORY, NOT THE PATH. os.SameFile compares the device
// and inode the directory was opened as, which is what makes two spellings
// of one folder one collection: a trailing slash, a `.` or `..` segment, a
// symlink, a second mount of the same filesystem. The path is exactly the
// thing a caller can write more than one way, and the pair that reaches this
// in practice is an importer's destination and the same folder chosen by
// hand in a file dialog.
//
// A handle that no longer resolves is not an answer. It can match by
// identity and still be dead — the name it was opened under repointed
// elsewhere — and answering an open that SUCCEEDED with a handle every later
// call refuses would be worse than minting a fresh one. check() is the same
// question resolve() asks, asked here rather than restated, because "is this
// handle still good" has one owner too.
//
// The caller holds s.mu: this scan and the mint that may follow it are one
// decision, and a second Open landing between them would register the second
// identity this exists to prevent. That holds the mutex across a few
// syscalls per open handle, which is the price of the decision being atomic;
// the alternative is the defect.
func (s *service) openHandleFor(fi os.FileInfo) (HandleID, bool) {
	for id, hd := range s.handles {
		if os.SameFile(fi, hd.openedAs) && hd.check() == nil {
			return id, true
		}
	}
	return "", false
}

// Close forgets one handle, which is the closing event named in resolve's
// interval below.
func (s *service) Close(h HandleID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.handles[h]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHandle, h)
	}
	delete(s.handles, h)
	return nil
}

// check is the whole of "does this handle still name the folder it was
// opened on". It is a method on the handle rather than inline in resolve
// because Open asks it too — a handle that fails it is not a folder anybody
// can be handed back — and two copies of it would be two answers.
func (hd *handle) check() error {
	// The chosen path must still lead to the folder it led to at Open. This
	// catches the case identity alone cannot: a symlinked root that has been
	// repointed. The directory we opened is still there and still itself, so
	// os.SameFile below would be satisfied while the user's collection had
	// been swapped underneath the name they chose.
	nowResolved, err := filepath.EvalSymlinks(hd.namedAs)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrRootChanged, hd.namedAs, err)
	}
	if nowResolved != hd.root {
		return fmt.Errorf("%w: %s now resolves to %s, not %s",
			ErrRootChanged, hd.namedAs, nowResolved, hd.root)
	}

	// Lstat, not Stat: a root replaced by a symlink is a replaced root, even
	// when the symlink happens to point somewhere plausible.
	fi, err := os.Lstat(hd.root)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrRootChanged, hd.root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is no longer a directory", ErrRootChanged, hd.root)
	}
	if !os.SameFile(fi, hd.openedAs) {
		return fmt.Errorf("%w: %s is a different directory from the one that was opened", ErrRootChanged, hd.root)
	}
	return nil
}
