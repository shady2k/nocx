package apicoll

// Moving a request file inside one collection — the act the tree's row menu
// offers, and the half of "rename a file" that is a MOVE rather than a
// change to the name the file declares (request-crumbs.tsx says the two are
// different acts; this is the move half, filed as nocx-8aczn.1).
//
// # ONE OPERATION, never write-then-delete
//
// A move built as WriteRequest to the destination followed by DeleteRequest
// of the source has a window in which the request exists twice and a window
// in which it exists not at all, and a failure between them leaves whichever
// half landed. The file is the truth (§6.4), so "which half landed" is
// exactly the question a collection shared through git must never leave
// open. This is a rename on the backend: one syscall, from before which the
// file is at the source to after which it is at the destination, with no
// instant at which it is at both.
//
// The no-replace flag is the other half of the same property. A plain
// rename would overwrite an existing destination silently — a second file
// lost wearing the word "move" — and a check-then-rename would report "free"
// and then replace a file somebody's git pull landed in between. The
// syscall's own EEXIST is the refusal (rename_linux.go says how each
// platform spells it), which is the same posture CreateFolder takes for its
// names.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MoveRequest moves one request file from one relative path to another
// INSIDE the same collection. It answers the new relPath, which is the
// caller's next address for the file — never re-derived, because a renderer
// joining a folder and a stem itself would be a second answer to "where is
// this request now".
//
// The interval, both ends named: the file is readable at EXACTLY ONE of
// the two paths from before the call until after it. There is no instant
// at which it is at both (rename(2) is atomic) and no instant at which it
// is at neither; the closing event of the interval is the rename's return.
// A refused collision, a missing folder or an outside path leaves the
// source exactly where it was, which is the naming of the other end.
//
// Moving between collections is out of scope: both paths are validated by
// the path rules that already own them (validateRequestPath,
// resolveWithin), so a destination outside this root is refused by
// ErrPathOutsideCollection.
//
// This method MOVES, it does not create. A destination folder that does not
// exist is refused (ErrFolderNotFound) rather than made — making a folder
// is api.collections.createFolder, and a move that minted its own folder
// would never be able to say "that folder is not there".
func (s *service) MoveRequest(h HandleID, fromRel, toRel string) (string, error) {
	hd, err := s.resolve(h)
	if err != nil {
		return "", err
	}
	// Both paths go through validateRequestPath, the rule that already owns
	// "may a path name a request file at all" — with the assignment rather
	// than a fresh declaration, so the method-level err is reused and there
	// is no shadow to keep straight.
	if err = validateRequestPath(fromRel); err != nil {
		return "", err
	}
	if err = validateRequestPath(toRel); err != nil {
		return "", err
	}
	// A move to the path it is already at is refused rather than answered
	// with a no-op: the caller asked for a move, and "nothing happened,
	// successfully" is how a second answer to the same question starts.
	if fromRel == toRel {
		return "", fmt.Errorf("%w: %q is where it already is", ErrRequestExists, toRel)
	}

	// The source, held to the same rules every read and write on this
	// surface keeps: no symlink on the way, and a regular request file at
	// the end of it. A move of a file that is not there is a move of
	// nothing, and a move of something that is not a request would be
	// DeleteRequest's refusal reached through a rename.
	fromFull, err := resolveWithin(hd.root, fromRel)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(fromFull)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%w: %q", ErrRequestNotFound, fromRel)
	case err != nil:
		return "", fmt.Errorf("apicoll: stat request %q: %w", fromRel, err)
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%w: %q is not a regular file", ErrNotARequestPath, fromRel)
	}

	// The destination side of the same walk: every component of it must
	// stay inside the collection, and the DESTINATION FOLDER has to be
	// there. A rename into a missing directory answers ENOENT, which is
	// true and useless; ErrFolderNotFound is the sentence a person can act
	// on, the same one CreateFolder uses for the same parent question.
	toFull, err := resolveWithin(hd.root, toRel)
	if err != nil {
		return "", err
	}
	if err := requireFolder(hd.root, toRel); err != nil {
		return "", err
	}

	// ONE SYSCALL. The refusal is the kernel's: renameat2(RENAME_NOREPLACE)
	// answers EEXIST when toFull already holds something, so "is it free"
	// and "move it" cannot come apart in the middle (rename_linux.go).
	if err := renameNoReplace(fromFull, toFull); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: %q", ErrRequestExists, toRel)
		}
		return "", fmt.Errorf("apicoll: move request %q to %q: %w", fromRel, toRel, err)
	}
	return toRel, nil
}

// requireFolder checks that the DIRECTORY holding rel is present and really
// a directory. resolveWithin has already refused any symlink on the way, so
// this is the stat that distinguishes "folder exists" from "the
// destination's parent is missing" from "something is in the way".
func requireFolder(root, rel string) error {
	parent := filepath.Dir(rel)
	full, err := resolveWithin(root, parent)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %q", ErrFolderNotFound, parent)
	case err != nil:
		return fmt.Errorf("apicoll: stat folder %q: %w", parent, err)
	case !fi.IsDir():
		return fmt.Errorf("%w: %q is not a folder", ErrFolderNotFound, parent)
	}
	return nil
}
