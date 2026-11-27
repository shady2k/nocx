package hostsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/transfer"
)

func TestOldConnectionLeavesTheTransferredWriteAlone(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "note")
	id := writeID(t.Name())
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)
	opener := host.New(nil, nil, "hash", "instance", discardLog())
	replay := host.New(nil, nil, "hash", "instance", discardLog())
	if _, err := svc.Call(host.WithConnection(t.Context(), opener), proto.OpWriteOpen,
		callParams(t, proto.FSWriteOpenParams{ID: id, Path: target})); err != nil {
		t.Fatal(err)
	}

	// The failed install has published writeOpen and still holds the lock.
	// The opener's teardown publishes ownerClosed and can clean up only
	// after the replay gets its chance to adopt the session.
	sess := svc.session(id)
	sess.mu.Lock()
	done := make(chan struct{})
	go func() {
		svc.ConnectionClosed(opener)
		close(done)
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for !sess.ownerClosed.Load() {
		if err := ctx.Err(); err != nil {
			sess.mu.Unlock()
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	replayCtx := host.WithConnection(ctx, replay)
	svc.afterFailedInstallLocked(replayCtx, sess)
	sess.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	mustExist(t, filepath.Join(dir, writeTempPrefix+id))
	if got := admission.outstanding(t); got != 1 {
		t.Fatalf("transferred write holds %d admissions, want 1", got)
	}
	if _, err := svc.Call(replayCtx, proto.OpWrite,
		callParams(t, proto.FSWriteParams{ID: id, Data: []byte("new")})); err != nil {
		t.Fatalf("write from the new owner: %v", err)
	}
	answer, err := svc.Call(replayCtx, proto.OpWriteCommit,
		callParams(t, proto.FSWriteCommitParams{ID: id, Path: target, Size: 3}))
	if err != nil {
		t.Fatalf("commit from the new owner: %v", err)
	}
	svc.ResponseWritten(replayCtx, proto.OpWriteCommit, answer, host.AnswerDelivery{})
	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("completed write holds %d admissions, want 0", got)
	}
}

type closeOnRelease struct {
	server   net.Conn
	released chan struct{}
}

func (a *closeOnRelease) Admit() (func(), error) {
	return func() {
		_ = a.server.Close()
		close(a.released)
	}, nil
}

func TestAbortRefusalReachesTheClientBeforeTheDrain(t *testing.T) {
	dir := tempDir(t)
	server, wire := net.Pipe()
	admission := &closeOnRelease{server: server, released: make(chan struct{})}
	svc := newAdmissionService(t, dir, admission)
	svc.seams.removeTemp = func(string) error { return syscall.EACCES }
	t.Cleanup(func() { svc.seams.removeTemp = nil })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer := dialWriteTestPeer(t, ctx, server, wire, svc)
	id := writeID(t.Name())
	if err := peer.Call(ctx, proto.ServiceFS, proto.OpWriteOpen,
		proto.FSWriteOpenParams{ID: id, Path: filepath.Join(dir, "note")}, &proto.FSWriteOpenResult{}); err != nil {
		t.Fatal(err)
	}
	// Releasing this reservation closes the transport, as the daemon does
	// when the last session has exited and this write is its last admission.
	err := peer.Call(ctx, proto.ServiceFS, proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id}, nil)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) || refusal.Code != proto.FSCodePermissionDenied {
		t.Fatalf("abort answered %v, want a permission-denied refusal before drain", err)
	}
	select {
	case <-admission.released:
	case <-ctx.Done():
		t.Fatal("the refused abort kept its admission")
	}
}

func TestAbortAccountsForTheTemporaryAfterCloseFails(t *testing.T) {
	for _, removalFails := range []bool{false, true} {
		name := "removable"
		if removalFails {
			name = "stranded"
		}
		t.Run(name, func(t *testing.T) {
			dir := tempDir(t)
			admission := &recordingAdmission{}
			svc := newAdmissionService(t, dir, admission)
			id := writeID(t.Name())
			temp := filepath.Join(dir, writeTempPrefix+id)
			writeOpenOK(t, svc, id, filepath.Join(dir, "note"))
			// A close can report an error even though it ends the handle.
			if err := svc.session(id).file.Close(); err != nil {
				t.Fatal(err)
			}
			if removalFails {
				svc.seams.removeTemp = func(string) error { return syscall.EACCES }
				t.Cleanup(func() { svc.seams.removeTemp = nil })
			}
			_, err := callAs[proto.FSWriteAbortResult](t, svc, proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id})
			if !errors.Is(err, os.ErrClosed) {
				t.Fatalf("abort answered %v, want the close error", err)
			}
			if got := admission.outstanding(t); got != 0 {
				t.Fatalf("failed close left %d admissions held", got)
			}
			if removalFails {
				mustExist(t, temp)
				if got := journalEntries(t, svc.journal.dir); len(got) != 1 {
					t.Fatalf("journal = %v, want the stranded temporary", got)
				}
				if got := inventory(t, svc); len(got) != 1 || got[0].Path != temp {
					t.Fatalf("inventory = %v, want %s", got, temp)
				}
				svc.seams.removeTemp = nil
				abortOK(t, svc, id)
			}
			mustNotExist(t, temp)
			if len(journalEntries(t, svc.journal.dir)) != 0 || len(inventory(t, svc)) != 0 {
				t.Fatal("removed temporary remains in the journal or inventory")
			}
		})
	}
}

func dialWriteTestPeer(t *testing.T, ctx context.Context, server, wire net.Conn, svc host.Service) *client.Client {
	t.Helper()
	logger := logtest.Slog(t)
	h := host.New(server, server, "write-test", crashedInstance, logger)
	h.Register(svc)
	served := make(chan struct{})
	go func() {
		defer close(served)
		defer func() { _ = server.Close() }()
		_ = h.Serve(ctx)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = wire.Close()
		<-served
	})
	c, err := client.Dial(ctx, client.Config{Exec: client.NewSocketConn(wire), ExpectHash: "write-test", Log: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// cutWritePeer drops A's first commit reply. Its teardown reaches the FS
// service while B's replay is in install, with the session still committing.
type cutWritePeer struct {
	*Service
	server           net.Conn
	replayInstalling <-chan struct{}
	openerClosed     chan<- struct{}
	ctx              context.Context
}

func (s *cutWritePeer) Call(ctx context.Context, op string, raw json.RawMessage) (any, error) {
	if op == proto.OpWriteCommit {
		_ = s.server.Close()
		return nil, syscall.EACCES
	}
	return s.Service.Call(ctx, op, raw)
}

func (s *cutWritePeer) ConnectionClosed(h *host.Host) {
	select {
	case <-s.replayInstalling:
	case <-s.ctx.Done():
	}
	s.Service.ConnectionClosed(h)
	close(s.openerClosed)
}

func TestFailedCommitReplayAbortsOnTheServingReplacement(t *testing.T) {
	for _, name := range []string{"permission denied", "keep both", "changed since read"} {
		t.Run(name, func(t *testing.T) {
			dir := tempDir(t)
			target := filepath.Join(dir, "note")
			mustWrite(t, target, []byte("old"))
			released := make(chan struct{})
			admission := &recordingAdmission{onRelease: func() { close(released) }}
			svc := newAdmissionService(t, dir, admission)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			replayInstalling, openerClosed := make(chan struct{}), make(chan struct{})
			svc.seams.syncTemp = func(f *os.File) error {
				close(replayInstalling)
				select {
				case <-openerClosed:
					return f.Sync()
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			svc.seams.rename = func(string, string, bool) error { return syscall.EACCES }
			policy := transfer.Overwrite
			if name == "keep both" {
				policy = transfer.KeepBoth
				mustWrite(t, filepath.Join(dir, "note (1)"), []byte("already there"))
			}
			a, aw := net.Pipe()
			first := dialWriteTestPeer(t, ctx, a, aw, &cutWritePeer{svc, a, replayInstalling, openerClosed, ctx})
			b, bw := net.Pipe()
			second := dialWriteTestPeer(t, ctx, b, bw, svc)
			reg, clock := replayTestRegistry(t, first, second)
			provider := helperfs.New(reg, "files", dir)
			data := []byte("new")
			var version filesystem.Version
			if name == "changed since read" {
				var err error
				_, version, err = provider.ReadVersion(ctx, target, 100)
				if err != nil {
					t.Fatal(err)
				}
				mustWrite(t, target, []byte("edited elsewhere"))
			}
			finished := make(chan error, 1)
			go func() {
				if name == "changed since read" {
					_, err := provider.Commit(ctx, target, data, filesystem.Expect{Version: &version})
					finished <- err
					return
				}
				_, err := provider.Sink().Put(ctx, transfer.Upload{
					DestDir: dir, Name: "note", Size: int64(len(data)), OnExists: policy,
				}, bytes.NewReader(data), nil)
				finished <- err
			}()
			select {
			case retry := <-clock.armed:
				clock.Advance(retry)
			case <-ctx.Done():
				t.Fatal("the registry did not schedule a replacement")
			}
			var err error
			select {
			case err = <-finished:
			case <-ctx.Done():
				t.Fatal("the upload did not return")
			}
			switch name {
			case "keep both":
				var unavailable *filesystem.ErrUnavailable
				if !errors.As(err, &unavailable) {
					t.Fatalf("retry against a different opener = %v, want unavailability after collision", err)
				}
			case "changed since read":
				var changed *filesystem.ErrChangedSinceRead
				if !errors.As(err, &changed) {
					t.Fatalf("replayed commit = %v, want changed since read", err)
				}
			default:
				var denied *filesystem.ErrPermission
				if !errors.As(err, &denied) {
					t.Fatalf("replayed commit = %v, want permission denied", err)
				}
			}
			select {
			case <-second.Done():
				t.Fatal("the replacement connection closed during cleanup")
			default:
			}
			if got := temps(t, dir); len(got) != 0 {
				t.Fatalf("upload returned before aborting the replacement's temporary: %v", got)
			}
			select {
			case <-released:
			case <-ctx.Done():
				t.Fatal("the replacement's abort kept the admission")
			}
			if got := admission.outstanding(t); got != 0 {
				t.Fatalf("upload left %d admissions on the serving replacement", got)
			}
		})
	}
}

type writeRetryClock struct {
	*hostconntest.Clock
	armed chan time.Duration
}

func (c writeRetryClock) After(d time.Duration) <-chan monoclock.Nanos {
	alarm := c.Clock.After(d)
	c.armed <- d
	return alarm
}

type writePeerSource struct {
	peers  [2]*client.Client
	dialed atomic.Uint32
}

func (*writePeerSource) List() []farhelper.Connection {
	return []farhelper.Connection{{ID: "files", HostKeyFingerprint: "files", State: hostconn.Connected, Generation: 1, Shells: 1}}
}

func (*writePeerSource) Watch(func(farhelper.Connection)) func() { return func() {} }

func (*writePeerSource) Probe(context.Context, farhelper.Target) (farhelper.Platform, error) {
	return farhelper.Platform{GOOS: "linux", GOARCH: "amd64"}, nil
}

func (*writePeerSource) Find(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, bool, error) {
	return farhelper.Installed{Dir: "/fixture", Generation: "write-test"}, true, nil
}

func (*writePeerSource) Install(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, error) {
	return farhelper.Installed{}, errors.New("fixture helper already installed")
}

func (s *writePeerSource) Dial(context.Context, farhelper.Target, farhelper.Installed) (*client.Client, error) {
	if i := s.dialed.Add(1) - 1; i < uint32(len(s.peers)) {
		return s.peers[i], nil
	}
	return nil, errors.New("no more fixture peers")
}

type writeConsent struct{}

func (writeConsent) Lookup(string) (consent.Answer, bool) { return consent.Granted, true }
func (writeConsent) Watch(func()) func()                  { return func() {} }

func replayTestRegistry(t *testing.T, first, second *client.Client) (*farhelper.Registry, writeRetryClock) {
	t.Helper()
	clock := writeRetryClock{Clock: hostconntest.New(), armed: make(chan time.Duration, 2)}
	peers := &writePeerSource{peers: [2]*client.Client{first, second}}
	_, logger := logtest.New(t)
	reg := farhelper.New(farhelper.Config{
		Connections: peers, Consents: writeConsent{}, Probe: peers, Install: peers, Dial: peers, Clock: clock, Log: logger,
	})
	t.Cleanup(reg.Close)
	return reg, clock
}
