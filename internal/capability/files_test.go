package capability_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport/control"
)

// recordingSink stands in for a remote host's write half: it records the one
// call the tests make and answers success.
type recordingSink struct {
	puts []transfer.Upload
}

func (s *recordingSink) Put(_ context.Context, u transfer.Upload, r io.Reader, progress func(int64)) (transfer.Outcome, error) {
	b, _ := io.ReadAll(r)
	if progress != nil {
		progress(int64(len(b)))
	}
	s.puts = append(s.puts, u)
	return transfer.Outcome{State: transfer.StateWritten, FinalName: u.Name}, nil
}

// readOnlyProvider is a provider with no write half — the shape of any
// provider that has not implemented Uploader. Neither shipped provider is
// one any more (D7, as corrected: the local provider writes through os), so
// this stub is what keeps R1's refusal testable at the seam that decides it.
type readOnlyProvider struct{}

func (readOnlyProvider) Root(context.Context) (filesystem.Root, error) {
	return filesystem.Root{Path: "/home/u"}, nil
}

func (readOnlyProvider) List(context.Context, string, filesystem.Page) (filesystem.Listing, error) {
	return filesystem.Listing{Entries: []filesystem.Entry{}}, nil
}

func (readOnlyProvider) Read(context.Context, string, int64) (filesystem.Content, error) {
	return filesystem.Content{}, nil
}

func (readOnlyProvider) Watch(context.Context, string) (filesystem.Watch, error) {
	return nil, &filesystem.ErrWatchUnavailable{}
}
func (readOnlyProvider) Canonical(_ context.Context, p string) (string, error) { return p, nil }
func (readOnlyProvider) Close() error                                          { return nil }

// writableProvider is a provider that carries one — the shape of both
// providers the composition root builds, and the same name the composition
// root gives the pair of halves (internal/app/app.go).
type writableProvider struct {
	readOnlyProvider
	sink transfer.Sink
}

func (p writableProvider) Sink() transfer.Sink { return p.sink }

// callerOwningEverything is the transport's Caller seam for these tests. The
// ownership check itself is binding_test.go's subject; here it must simply
// not get in the way of reaching Upload.
type callerOwningEverything struct{}

func (callerOwningEverything) Owns(session.ID) bool { return true }

// openBindingHarness builds the real files.open operation over fake stores
// and returns a function that runs one callback inside it, the way the
// transport's handler does.
func openBindingHarness(t *testing.T, factory capability.ProviderFactory) (
	run func(func(context.Context, capability.FilesystemOpenService) error) error,
	sess session.Session,
) {
	t.Helper()
	gate := func(name string) control.Admission {
		return control.NewWaitingSemaphore(name, 1, 4, time.Second)
	}
	registry := newFakeSessionRegistry()
	s := &fakeSession{id: "s1", kind: session.KindRemote, host: "srv-01"}
	registry.sessions[s.id] = s
	op := capability.NewFilesystemOpenOperation(
		gate("session"), gate("filesystem"), gate("lane"),
		registry, factory, filesystem.New(),
	)
	return func(fn func(context.Context, capability.FilesystemOpenService) error) error {
		return op.Run(context.Background(), fn)
	}, s
}

// uploadThrough opens a binding for sess, asks its handle for the write half
// and runs one transfer on it, returning what came back. The two steps are
// the shape D8 requires: the guard covers obtaining the sink, and the
// transfer runs unguarded.
func uploadThrough(t *testing.T, run func(func(context.Context, capability.FilesystemOpenService) error) error, sess session.Session) (transfer.Outcome, error) {
	t.Helper()
	var (
		out    transfer.Outcome
		upErr  error
		opened bool
	)
	if err := run(func(ctx context.Context, svc capability.FilesystemOpenService) error {
		bid, _, err := svc.OpenBinding(ctx, sess, "")
		if err != nil {
			return err
		}
		opened = true
		h, release, err := svc.Acquire(bid, callerOwningEverything{})
		if err != nil {
			return err
		}
		defer release()
		sink, sinkErr := h.Uploader()
		if sinkErr != nil {
			upErr = sinkErr
			return nil
		}
		out, upErr = sink.Put(ctx,
			transfer.Upload{DestDir: "/home/u", Name: "a.txt", Size: 1, OnExists: transfer.Overwrite},
			strings.NewReader("x"), nil)
		return nil
	}); err != nil {
		t.Fatalf("files.open: %v", err)
	}
	if !opened {
		t.Fatal("the binding was never opened")
	}
	return out, upErr
}

// TestOpenBinding_AWritableProviderContributesASink is the wiring the whole
// upload feature stands on: the write seam is asserted where the endpoint
// attester's already is, so the sink a remote provider carries reaches the
// binding and Upload goes through.
func TestOpenBinding_AWritableProviderContributesASink(t *testing.T) {
	sink := &recordingSink{}
	run, sess := openBindingHarness(t, func(session.Session, string) (filesystem.Provider, error) {
		return writableProvider{sink: sink}, nil
	})

	out, err := uploadThrough(t, run, sess)

	var unsupported *filesystem.ErrUploadUnsupported
	if errors.As(err, &unsupported) {
		t.Fatal("the provider implements Uploader; OpenBinding did not pick the sink up")
	}
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if out.State != transfer.StateWritten || out.FinalName != "a.txt" {
		t.Errorf("outcome %+v, want the sink's answer", out)
	}
	if len(sink.puts) != 1 {
		t.Fatalf("the sink saw %d puts, want 1", len(sink.puts))
	}
}

// TestOpenBinding_AReadOnlyProviderContributesNone is rule R1 at the seam
// that decides it: a provider with no write half leaves the binding with a
// nil sink, and the refusal that follows is that nil rather than a check.
func TestOpenBinding_AReadOnlyProviderContributesNone(t *testing.T) {
	run, sess := openBindingHarness(t, func(session.Session, string) (filesystem.Provider, error) {
		return readOnlyProvider{}, nil
	})

	_, err := uploadThrough(t, run, sess)

	var unsupported *filesystem.ErrUploadUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Upload on a binding whose provider cannot write: %v, want ErrUploadUnsupported", err)
	}
}

// TestOpenBinding_AttestationSurvivesBesideTheSeam pins that the two
// assertions coexist: adding the write one did not cost the endpoint
// attestation, which is what makes files.reveal local-only.
func TestOpenBinding_AttestationSurvivesBesideTheSeam(t *testing.T) {
	run, sess := openBindingHarness(t, func(session.Session, string) (filesystem.Provider, error) {
		return attestedWritableProvider{writableProvider: writableProvider{sink: &recordingSink{}}}, nil
	})

	var endpointID string
	if err := run(func(ctx context.Context, svc capability.FilesystemOpenService) error {
		_, eid, err := svc.OpenBinding(ctx, sess, "")
		endpointID = eid
		return err
	}); err != nil {
		t.Fatalf("files.open: %v", err)
	}
	if endpointID != "v1:abc" {
		t.Errorf("endpointID %q, want the provider's attestation", endpointID)
	}
}

// attestedWritableProvider carries both optional seams, like the composition
// root's wrapped sftp provider.
type attestedWritableProvider struct {
	writableProvider
}

func (attestedWritableProvider) EndpointID() string { return "v1:abc" }

// TestOpenBinding_ReportsAFactoryFailure is the failure path of the one
// external call OpenBinding makes: a provider that cannot be built — no
// route to the host, SFTP refused — is reported, and no binding is minted
// for a filesystem that does not exist.
func TestOpenBinding_ReportsAFactoryFailure(t *testing.T) {
	boom := errors.New("sftp provider for srv-01: connection refused")
	run, sess := openBindingHarness(t, func(session.Session, string) (filesystem.Provider, error) {
		return nil, boom
	})

	var bid string
	err := run(func(ctx context.Context, svc capability.FilesystemOpenService) error {
		var err error
		bid, _, err = svc.OpenBinding(ctx, sess, "")
		return err
	})
	if !errors.Is(err, boom) {
		t.Fatalf("files.open: %v, want the factory's failure", err)
	}
	if bid != "" {
		t.Errorf("a binding %q was minted for a provider that was never built", bid)
	}
}
