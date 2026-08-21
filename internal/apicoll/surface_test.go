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
// The method count is part of the assertion. Adding a fifth method that takes
// a path fails here, which is the whole point — the guard has to break when
// somebody widens the surface, not when somebody misuses it.
func TestService_OpenIsTheOnlyEntryPointThatTakesARoot(t *testing.T) {
	svcType := reflect.TypeOf((*Service)(nil)).Elem()
	handleType := reflect.TypeOf(HandleID(""))
	stringType := reflect.TypeOf("")

	if got := svcType.NumMethod(); got != 4 {
		t.Fatalf("Service has %d methods, want 4 — a new method must be checked against §13.1 "+
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
func TestNewService_SatisfiesService(t *testing.T) {
	var _ Service = NewService()
	var _ Service = newService()
}
