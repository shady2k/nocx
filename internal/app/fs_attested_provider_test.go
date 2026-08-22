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
