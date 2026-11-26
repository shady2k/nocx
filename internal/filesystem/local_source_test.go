package filesystem_test

// One real binding, held for a test package: the shape a capability holds in
// the product. Every capability test used to be built over a *local.Provider
// directly; the capabilities now take a HandleSource, so the fixture is the
// package's own Registry with one local provider registered in it — the same
// two objects the composition root wires for the Files panel and for the
// assistant.

import (
	"context"
	"testing"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/sessionid"
)

const scopeSessionID = sessionid.ID("scope-test")

type localSource struct {
	reg *filesystem.Registry
	id  string
}

func newLocalSource(t *testing.T) *localSource {
	t.Helper()
	provider := local.New()
	return newSourceWithProvider(t, provider, filesystem.Capabilities{Versioned: provider.Versioned()})
}

// newSourceWithProvider registers any provider with the seams the caller names,
// so a test can wrap the local one without losing its conditional write.
func newSourceWithProvider(t *testing.T, provider filesystem.Provider, caps filesystem.Capabilities) *localSource {
	t.Helper()
	reg := filesystem.New()
	id, err := reg.Register(provider, scopeSessionID, "", caps)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(id) })
	return &localSource{reg: reg, id: id}
}

func (s *localSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return s.reg.Acquire(s.id, ownsOne{id: scopeSessionID})
}

// ownsOne is filesystem.Caller for a caller that owns exactly one session,
// which is every caller in these tests and every assistant run in the product.
type ownsOne struct{ id sessionid.ID }

func (c ownsOne) Owns(id sessionid.ID) bool { return id == c.id }
