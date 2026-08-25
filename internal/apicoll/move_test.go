package apicoll

// A request can be put into a folder, and taken out again — the act the
// whole api.request.move method exists for. The move is ONE operation on
// the backend: a rename, never a write-then-delete, so there is no instant
// at which the request exists at both paths or at neither, and a collision
// is REFUSED rather than overwritten (§13.1's rule applied to the one act
// that could otherwise replace a file).
//
// Every refusal below is paired with a success on the same service
// (AGENTS.md testing rule 3): a suite that only proves what is refused
// cannot report a feature that never worked.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The whole motion: root → folder, folder → root, and the bytes at the
// destination are the bytes that were at the source — the file is the
// truth (§6.4), so a move that rewrote or re-marshalled it would be a
// second act nobody asked for.
func TestMoveRequest_MovesBetweenRootAndFolder(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if _, err := svc.CreateFolder(h, "", "users"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if err := svc.WriteRequest(h, "ping.json",
		Request{ID: "r1", Name: "ping", Method: "POST", URL: "https://example.test/ping"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "ping.json")) //nolint:gosec // a request this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("read source: %v", err)
	}

	// Root → folder.
	moved, err := svc.MoveRequest(h, "ping.json", "users/ping.json")
	if err != nil {
		t.Fatalf("MoveRequest into users: %v", err)
	}
	if moved != "users/ping.json" {
		t.Errorf("result relPath = %q, want %q; the caller should not have to derive it",
			moved, "users/ping.json")
	}
	after, err := os.ReadFile(filepath.Join(root, "users", "ping.json")) //nolint:gosec // a request this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the bytes at the destination differ from the bytes at the source:\n%q\n%q",
			after, before)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "ping.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the source still exists after the move (err = %v)", statErr)
	}

	// And back to the root.
	back, err := svc.MoveRequest(h, "users/ping.json", "ping.json")
	if err != nil {
		t.Fatalf("MoveRequest back to the root: %v", err)
	}
	if back != "ping.json" {
		t.Errorf("result relPath = %q, want %q", back, "ping.json")
	}
	round, err := os.ReadFile(filepath.Join(root, "ping.json")) //nolint:gosec // a request this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("read after moving back: %v", err)
	}
	if string(round) != string(before) {
		t.Errorf("the bytes changed across the round trip:\n%q\n%q", round, before)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "users", "ping.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the folder copy still exists after moving back (err = %v)", statErr)
	}

	// The tree agrees with the disk afterwards — the reason the surface
	// re-reads the folder after a move.
	coll, err := svc.List(h)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(coll.Requests) != 1 || coll.Requests[0].RelPath != "ping.json" {
		t.Errorf("requests = %+v, want the one request at the root again", coll.Requests)
	}
}

// A destination that already holds a file of that name is REFUSED, nothing
// is overwritten, and the source is still there. The no-replace rename is
// what makes the collision atomic: a check-then-rename would report "free"
// and then replace a file somebody's pull had landed in between.
func TestMoveRequest_RefusesACollisionAndLeavesBothFiles(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if _, err := svc.CreateFolder(h, "", "users"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if err := svc.WriteRequest(h, "ping.json",
		Request{ID: "r1", Name: "ping", Method: "GET", URL: "https://example.test/ping"}); err != nil {
		t.Fatalf("WriteRequest ping: %v", err)
	}
	if err := svc.WriteRequest(h, "users/ping.json",
		Request{ID: "r2", Name: "other", Method: "GET", URL: "https://example.test/other"}); err != nil {
		t.Fatalf("WriteRequest users/ping: %v", err)
	}
	destBefore, err := os.ReadFile(filepath.Join(root, "users", "ping.json")) //nolint:gosec // a request this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}

	_, err = svc.MoveRequest(h, "ping.json", "users/ping.json")
	if !errors.Is(err, ErrRequestExists) {
		t.Fatalf("MoveRequest onto an existing file = %v, want ErrRequestExists", err)
	}
	destAfter, err := os.ReadFile(filepath.Join(root, "users", "ping.json")) //nolint:gosec // a request this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("read destination after the refusal: %v", err)
	}
	if string(destAfter) != string(destBefore) {
		t.Errorf("the destination was overwritten by the refused move:\n%q\n%q", destAfter, destBefore)
	}
	src, err := os.ReadFile(filepath.Join(root, "ping.json")) //nolint:gosec // a request this test just wrote under t.TempDir()
	if err != nil {
		t.Fatalf("the source vanished on the refused move: %v", err)
	}
	if len(src) == 0 {
		t.Error("the source is empty after the refused move")
	}
}

// A destination folder that does not exist is refused — this method moves,
// it does not create. Making a folder is api.collections.createFolder.
func TestMoveRequest_RefusesAFolderThatIsNotThere(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if err := svc.WriteRequest(h, "ping.json",
		Request{ID: "r1", Name: "ping", Method: "GET", URL: "https://example.test/ping"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	_, err := svc.MoveRequest(h, "ping.json", "nope/ping.json")
	if !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("MoveRequest into a missing folder = %v, want ErrFolderNotFound", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "nope", "ping.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("something was created under the missing folder (err = %v)", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "ping.json")); statErr != nil {
		t.Errorf("the source disappeared on the refusal: %v", statErr)
	}

	// The paired success: with the folder made first, the same move works.
	if _, err := svc.CreateFolder(h, "", "nope"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := svc.MoveRequest(h, "ping.json", "nope/ping.json"); err != nil {
		t.Fatalf("MoveRequest after the folder exists: %v", err)
	}
}

// A destination outside the collection is refused by the path rules that
// already own that question — the same sentinel every read and write
// answers with, reached through validateRequestPath rather than restated.
func TestMoveRequest_RefusesADestinationOutsideTheCollection(t *testing.T) {
	svc, h, root := openTestCollection(t)
	if err := svc.WriteRequest(h, "ping.json",
		Request{ID: "r1", Name: "ping", Method: "GET", URL: "https://example.test/ping"}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.json")

	for name, dest := range map[string]string{
		"absolute":    outside,
		"climbing":    "../elsewhere.json",
		"climbing on": "users/../../elsewhere.json",
		"not clean":   "users/./ping.json",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.MoveRequest(h, "ping.json", dest)
			if !errors.Is(err, ErrPathOutsideCollection) {
				t.Fatalf("MoveRequest to %q = %v, want ErrPathOutsideCollection", dest, err)
			}
			if _, statErr := os.Lstat(outside); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("the move wrote %s, outside the collection (err = %v)", outside, statErr)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(root, "ping.json")); err != nil {
		t.Errorf("the source disappeared on a refused destination: %v", err)
	}

	// The paired success: a destination inside the collection's own folder
	// is accepted by the same code path.
	if _, err := svc.CreateFolder(h, "", "users"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := svc.MoveRequest(h, "ping.json", "users/ping.json"); err != nil {
		t.Fatalf("MoveRequest into the collection's own folder: %v", err)
	}
}
