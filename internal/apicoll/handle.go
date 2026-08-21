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
	Open(root string) (HandleID, Collection, error)
	List(h HandleID) (Collection, error)
	ReadRequest(h HandleID, relPath string) (Request, error)
	WriteRequest(h HandleID, relPath string, r Request) error
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

	// newID and docStoreFor are the two calls this package makes that
	// cannot be made to fail by arranging a temp directory. They are fields
	// so each has a test where it fails, paired with one where it succeeds.
	newID       func() (HandleID, error)
	docStoreFor func(dir string) storage.DocumentStore
}

// NewService returns the collection service. It holds no state that outlives
// the process: the app remembers the LIST of opened folders, never their
// contents (§6.1), so handles are minted fresh each run and every read goes
// to disk.
func NewService() Service { return newService() }

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
// until the directory it was opened on stops being that directory — replaced,
// removed, or swapped for a symlink — and from that moment every method on it
// reports ErrRootChanged rather than answering out of whatever now sits at
// the path.
func (s *service) resolve(h HandleID) (*handle, error) {
	s.mu.Lock()
	hd, ok := s.handles[h]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownHandle, h)
	}

	// The chosen path must still lead to the folder it led to at Open. This
	// catches the case identity alone cannot: a symlinked root that has been
	// repointed. The directory we opened is still there and still itself, so
	// os.SameFile below would be satisfied while the user's collection had
	// been swapped underneath the name they chose.
	nowResolved, err := filepath.EvalSymlinks(hd.namedAs)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrRootChanged, hd.namedAs, err)
	}
	if nowResolved != hd.root {
		return nil, fmt.Errorf("%w: %s now resolves to %s, not %s",
			ErrRootChanged, hd.namedAs, nowResolved, hd.root)
	}

	// Lstat, not Stat: a root replaced by a symlink is a replaced root, even
	// when the symlink happens to point somewhere plausible.
	fi, err := os.Lstat(hd.root)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrRootChanged, hd.root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: %s is no longer a directory", ErrRootChanged, hd.root)
	}
	if !os.SameFile(fi, hd.openedAs) {
		return nil, fmt.Errorf("%w: %s is a different directory from the one that was opened", ErrRootChanged, hd.root)
	}
	return hd, nil
}
