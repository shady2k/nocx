package app

import (
	"context"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/transfer"
)

// stubRemoteProvider is a remote provider that can write: the shape the
// factory wraps for attestation.
type stubRemoteProvider struct {
	sink   transfer.Sink
	source transfer.Source
}

func (p *stubRemoteProvider) Root(context.Context) (filesystem.Root, error) {
	return filesystem.Root{Path: "/home/u"}, nil
}

func (p *stubRemoteProvider) List(context.Context, string, filesystem.Page) (filesystem.Listing, error) {
	return filesystem.Listing{Entries: []filesystem.Entry{}}, nil
}

func (p *stubRemoteProvider) Read(context.Context, string, int64) (filesystem.Content, error) {
	return filesystem.Content{}, nil
}

func (p *stubRemoteProvider) Stat(context.Context, string) (filesystem.Stat, error) {
	return filesystem.Stat{Kind: filesystem.KindRegular}, nil
}

func (p *stubRemoteProvider) Watch(context.Context, string) (filesystem.Watch, error) {
	return nil, &filesystem.ErrWatchUnavailable{}
}

func (p *stubRemoteProvider) Canonical(_ context.Context, path string) (string, error) {
	return path, nil
}
func (p *stubRemoteProvider) Close() error            { return nil }
func (p *stubRemoteProvider) Sink() transfer.Sink     { return p.sink }
func (p *stubRemoteProvider) Source() transfer.Source { return p.source }

// nopSink is a Sink identity — the tests compare the pointer, not behaviour.
type nopSink struct{}

func (nopSink) Put(context.Context, transfer.Upload, io.Reader, func(int64)) (transfer.Outcome, error) {
	return transfer.Outcome{}, nil
}

// nopSource is a Source identity — the tests compare the pointer, not
// behaviour.
type nopSource struct{}

func (nopSource) Open(string) (*transfer.Download, error) { return nil, nil }

func (nopSource) Get(context.Context, *transfer.Download, io.Writer, func(int64)) (int64, error) {
	return 0, nil
}

// TestEndpointAttestedProviderCarriesTheWriteSeam is the test the upload plan
// asked for even though the answer looked free. It is not free: the wrapper
// holds its provider as an INTERFACE, and embedding an interface promotes
// exactly that interface's methods — so a wrapper embedding
// filesystem.Provider has no Sink at all, however writable the value inside
// it is. files.open asserts Uploader on what the factory returned, which is
// the wrapper, so the capability would be dropped there and the only symptom
// would be uploads refusing on a remote tab, with every other files.* call
// working perfectly.
func TestEndpointAttestedProviderCarriesTheWriteSeam(t *testing.T) {
	sink := nopSink{}
	wrapped := &endpointAttestedProvider{
		writableProvider: &stubRemoteProvider{sink: sink},
		endpointID:       "v1:abc",
	}

	up, ok := any(wrapped).(filesystem.Uploader)
	if !ok {
		t.Fatal("the attested wrapper dropped the write seam; a remote tab would refuse every upload")
	}
	if up.Sink() != transfer.Sink(sink) {
		t.Errorf("Sink() returned %v, want the wrapped provider's own", up.Sink())
	}
	if wrapped.EndpointID() != "v1:abc" {
		t.Errorf("EndpointID %q, want the attestation to survive alongside the seam", wrapped.EndpointID())
	}
	if _, ok := any(wrapped).(filesystem.Provider); !ok {
		t.Error("the wrapper stopped being a Provider")
	}
}

// TestEndpointAttestedProviderCarriesTheReadStreamSeam is the same test one
// direction over, and it is not a copy for symmetry's sake: the wrapper
// embeds ONE interface, so the day somebody narrows writableProvider back to
// filesystem.Provider + filesystem.Uploader, this is the assertion that
// fails. Without it the symptom would be downloads refusing on a remote tab
// while every other files.* call, uploads included, worked perfectly.
func TestEndpointAttestedProviderCarriesTheReadStreamSeam(t *testing.T) {
	source := nopSource{}
	wrapped := &endpointAttestedProvider{
		writableProvider: &stubRemoteProvider{sink: nopSink{}, source: source},
		endpointID:       "v1:abc",
	}

	down, ok := any(wrapped).(filesystem.Downloader)
	if !ok {
		t.Fatal("the attested wrapper dropped the read-stream seam; a remote tab would refuse every download")
	}
	if down.Source() != transfer.Source(source) {
		t.Errorf("Source() returned %v, want the wrapped provider's own", down.Source())
	}
}

// stubConditionalWrite is an identity for the conditional-write seam: these
// tests compare the value the wrapper hands back, and the behaviour belongs to
// helperfs (versioned_test.go).
type stubConditionalWrite struct{}

func (*stubConditionalWrite) ReadVersion(context.Context, string, int64) ([]byte, filesystem.Version, error) {
	return nil, filesystem.Version{}, nil
}

func (*stubConditionalWrite) Commit(context.Context, string, []byte, filesystem.Expect) (filesystem.Version, error) {
	return filesystem.Version{}, nil
}

// TestEndpointAttestedProviderCarriesTheAxisAndTheConditionalWrite is the
// third and fourth instance of the wrapper's one trap: it holds its provider as
// an interface, so a seam it does not declare is a seam the composition site
// cannot see. The symptom of losing either is narrow and quiet — the panel
// would show no reason on a host whose helper is absent (C28), and the
// assistant's write would silently fall back to an unconditional one — which
// is exactly why the assertion is here rather than left to review.
func TestEndpointAttestedProviderCarriesTheAxisAndTheConditionalWrite(t *testing.T) {
	axis := filesystem.HelperAxis{State: "paused", Reason: "connection", Action: "none"}
	write := &stubConditionalWrite{}
	wrapped := &endpointAttestedProvider{
		writableProvider: &stubRemoteProvider{},
		endpointID:       "v1:abc",
		axis:             func() (filesystem.HelperAxis, bool) { return axis, true },
		versioned:        write,
	}

	provider, ok := any(wrapped).(filesystem.AxisProvider)
	if !ok {
		t.Fatal("the attested wrapper dropped the helper axis; a panel over an unavailable host would have no reason to draw")
	}
	if got, known := provider.HelperAxis(); !known || got != axis {
		t.Fatalf("HelperAxis = %+v, %v; want the wrapped axis and true", got, known)
	}
	conditional, ok := any(wrapped).(filesystem.VersionedProvider)
	if !ok {
		t.Fatal("the attested wrapper dropped the conditional write; a write would go out unchecked")
	}
	if conditional.Versioned() != filesystem.VersionedFiles(write) {
		t.Fatalf("Versioned returned %v, want the wrapped provider's own seam", conditional.Versioned())
	}
}

// TestEndpointAttestedProviderWithoutTheSeamsAnswersNeither is the other end:
// a local or SFTP-backed wrapper contributed no axis and no conditional write,
// and the wrapper says so — an axis that answers false and a nil seam the
// binding turns into *ErrVersionedUnsupported. It must not panic, which an
// embedded nil interface would.
func TestEndpointAttestedProviderWithoutTheSeamsAnswersNeither(t *testing.T) {
	wrapped := &endpointAttestedProvider{
		writableProvider: &stubRemoteProvider{},
		endpointID:       "v1:abc",
	}
	provider, ok := any(wrapped).(filesystem.AxisProvider)
	if !ok {
		t.Fatal("the wrapper stopped answering the axis interface at all")
	}
	if got, known := provider.HelperAxis(); known || got != (filesystem.HelperAxis{}) {
		t.Fatalf("HelperAxis = %+v, %v; want false and the zero axis", got, known)
	}
	if v, _ := any(wrapped).(filesystem.VersionedProvider); v.Versioned() != nil {
		t.Fatalf("Versioned returned %v; want nil for a provider with no conditional write", v.Versioned())
	}
}
