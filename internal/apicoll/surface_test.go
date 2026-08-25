package apicoll

import (
	"reflect"
	"testing"
)

// §13.1: the renderer must never be able to address a file by path twice.
// That is a property of the SURFACE, not of any one implementation, so it is
// asserted against the interface: Open is the only method that takes a root,
// and every other method's first parameter is the backend-minted handle.
//
// The method count is part of the assertion. Adding a method that takes a
// path fails here, which is the whole point — the guard has to break when
// somebody widens the surface, not when somebody misuses it.
//
// It has been raised, deliberately, four times. DeleteRequest arrived when
// the panel grew a way to remove a request (before it, a file made by
// mistake could only be removed with a file manager). It takes the handle
// and a path RELATIVE to it, like the two accessors beside it, so §13.1
// holds — a caller that cannot name a file still cannot delete one.
//
// CreateFolder arrived when a collection built inside nocx gained the
// structure §6.2 always described and only the importer could write. Its
// first parameter is the handle; the folder it creates IN is a path
// relative to that handle, and the folder it creates is a NAME — a single
// component, refused if it is anything else — so no caller can name a
// location twice here either.
//
// Close arrived when Open stopped minting a second handle for a folder that
// is already open: one folder has one handle, so something has to be able to
// END that, and the thing that mints identity is the thing that forgets it.
// It takes the handle and nothing else, so it widens the surface by a method
// that cannot name a location at all.
//
// MoveRequest arrived when a request gained a home inside the collection
// other than the one it was created in. It takes the handle and TWO paths
// relative to it — where the file is, and where it is going — and no caller
// can name a location twice: both are inside the collection the handle
// names, and the move is refused the moment either is not (§13.1).
func TestService_OpenIsTheOnlyEntryPointThatTakesARoot(t *testing.T) {
	svcType := reflect.TypeOf((*Service)(nil)).Elem()
	handleType := reflect.TypeOf(HandleID(""))
	stringType := reflect.TypeOf("")

	if got := svcType.NumMethod(); got != 10 {
		t.Fatalf("Service has %d methods, want 10 — a new method must be checked against §13.1 "+
			"before this count is raised", got)
	}

	for i := 0; i < svcType.NumMethod(); i++ {
		m := svcType.Method(i)
		ft := m.Type
		if m.Name == "Open" {
			if ft.NumIn() != 1 || ft.In(0) != stringType {
				t.Errorf("Open takes %v, want exactly one string root", ft)
			}
			continue
		}
		if ft.NumIn() == 0 || ft.In(0) != handleType {
			t.Errorf("%s's first parameter is %v, want HandleID", m.Name, ft)
			continue
		}
		// Any further string is a path RELATIVE to the handle; no method
		// other than Open may take a bare root as its first argument.
		for j := 1; j < ft.NumIn(); j++ {
			if ft.In(j) == handleType {
				t.Errorf("%s takes a second HandleID at %d — one call, one handle", m.Name, j)
			}
		}
	}
}

// The concrete service satisfies the interface the other two tasks are
// written against. Without this the package can compile while nothing in it
// is the Service anybody imported.
func TestTheFolderService_SatisfiesService(t *testing.T) {
	var _ Service = NewCollections(nil)
	var _ Service = newService()
}
