package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	filesystemlocal "github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport/control"
)

type nativeDownloadPicker struct {
	mu     sync.Mutex
	calls  int
	target *DownloadSaveTarget
	err    error
}

func (p *nativeDownloadPicker) OpenFile(context.Context) (string, error)      { return "", nil }
func (p *nativeDownloadPicker) OpenDirectory(context.Context) (string, error) { return "", nil }
func (p *nativeDownloadPicker) PickDownloadSave(context.Context, string, int64) (*DownloadSaveTarget, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.target, p.err
}

func (p *nativeDownloadPicker) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type blockingDownloadSavePicker struct {
	nativeDownloadPicker
	started chan struct{}
	release chan struct{}
}

func (p *blockingDownloadSavePicker) PickDownloadSave(context.Context, string, int64) (*DownloadSaveTarget, error) {
	close(p.started)
	<-p.release // deliberately ignores context, like Wails v3's synchronous dialog
	return p.target, p.err
}

type countingNativeSink struct {
	mu    sync.Mutex
	calls int
}

func (s *countingNativeSink) Put(context.Context, transfer.Upload, io.Reader, func(int64)) (transfer.Outcome, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return transfer.Outcome{State: transfer.StateWritten}, nil
}

func (s *countingNativeSink) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type fixedDownloadSaveMachine struct {
	rt      *runningTransfer
	claims  int
	onClaim func()
}

func (m *fixedDownloadSaveMachine) claimDownloadSave(string, *connState) (*runningTransfer, transferClaim) {
	m.claims++
	if m.onClaim != nil {
		m.onClaim()
	}
	return m.rt, transferClaimOK
}

type rejectDownloadSaveSubmission struct{}

func (rejectDownloadSaveSubmission) TrySubmit(context.Context, control.Task) *control.Rejection {
	return &control.Rejection{Reason: "queue-full", Scope: "dialog"}
}

func startNativeDownload(t *testing.T, e *filesTestEnv, body string) downloadResult {
	t.Helper()
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	path := fixture(t, dir, "remote.bin", body)
	bid := e.openBinding(t, sid, dir, 2)
	return callDownload(t, e.conn, downloadParams(bid, path), 3).mustResult(t)
}

func callDownloadSave(t *testing.T, conn *websocket.Conn, transferID string, requestID int) downloadEnvelope {
	t.Helper()
	var envelope downloadEnvelope
	raw := jsonrpcCallWithID(t, conn, "files.downloadSave", map[string]any{"transferId": transferID}, requestID)
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestFilesDownloadSave_RefusesBeforeDialog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*testing.T, *filesTestEnv, downloadResult) *websocket.Conn
	}{
		{name: "unowned", alter: func(t *testing.T, e *filesTestEnv, _ downloadResult) *websocket.Conn {
			conn := connectWS(t, e.ws)
			t.Cleanup(func() { _ = conn.Close() })
			return conn
		}},
		{name: "already claimed", alter: func(t *testing.T, e *filesTestEnv, d downloadResult) *websocket.Conn {
			if _, claim := e.ws.transfers.claim(d.Ticket, dirDownload, -1); claim != transferClaimOK {
				t.Fatalf("claim = %v", claim)
			}
			return e.conn
		}},
		{name: "expired", alter: func(_ *testing.T, e *filesTestEnv, d downloadResult) *websocket.Conn {
			e.ws.transfers.expire(d.Ticket)
			return e.conn
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDownloadTestEnv(t)
			picker := &nativeDownloadPicker{}
			e.ws.SetDialogService(picker)
			started := startNativeDownload(t, e, "bytes")
			conn := tc.alter(t, e, started)
			if response := callDownloadSave(t, conn, started.TransferID, 4); response.Error == nil {
				t.Fatal("refused transfer opened native save")
			}
			if picker.callCount() != 0 {
				t.Fatalf("picker calls = %d, want 0", picker.callCount())
			}
		})
	}
}

func TestFilesDownloadSave_QueueRejectionSettlesClaimedTransferImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &runningTransfer{
		id: strings.Repeat("a", 32), dir: dirDownload, ctx: ctx, cancel: cancel,
		download: &transfer.Download{Name: "remote.bin", Size: 1},
		dest:     make(chan downloadDestination, 1), done: make(chan struct{}),
	}
	picker := &nativeDownloadPicker{}
	var service DialogService = picker
	var dialogMu sync.RWMutex
	responder := &spyResponder{}
	machine := &fixedDownloadSaveMachine{rt: rt}
	h := downloadSaveHandlers{
		dialog: &dialogServiceHolder{mu: &dialogMu, svc: &service},
		submit: rejectDownloadSaveSubmission{}, machine: machine, r: responder,
	}
	h.handleDownloadSave(context.Background(), &connState{}, jsonrpcRequest{
		ID: json.RawMessage(`1`), Method: "files.downloadSave",
		Params: mustMarshal(filesDownloadSaveParams{TransferID: rt.id}),
	})
	if machine.claims != 1 {
		t.Fatalf("claims = %d, want one before queue submission", machine.claims)
	}
	if len(responder.errors) != 1 {
		t.Fatalf("RPC errors = %#v, want saturation refusal", responder.errors)
	}
	select {
	case destination := <-rt.dest:
		result := destination.receive(context.Background(), nil, nil, nil)
		if result.state != downloadStateFailed || !errors.Is(result.wireErr, errNativeDownloadSaveWire) {
			t.Fatalf("result = %+v, want immediate path-free failure", result)
		}
	default:
		t.Fatal("queue rejection left the claimed transfer waiting for TTL")
	}
}

type blockingCloseReadFS struct {
	closeStarted chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (f *blockingCloseReadFS) Open(string) (transfer.RemoteReader, int64, error) {
	return &blockingCloseReader{fs: f}, 0, nil
}

type blockingCloseReader struct{ fs *blockingCloseReadFS }

func (*blockingCloseReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *blockingCloseReader) Close() error {
	r.fs.once.Do(func() { close(r.fs.closeStarted) })
	<-r.fs.release
	return nil
}

func TestFilesDownloadSave_DisconnectAfterClaimNeverClosesRemoteOnReadLoop(t *testing.T) {
	fsys := &blockingCloseReadFS{closeStarted: make(chan struct{}), release: make(chan struct{})}
	download, err := transfer.NewSource(fsys, transfer.DefaultChunk).Open("/srv/remote.bin")
	if err != nil {
		t.Fatal(err)
	}
	transferCtx, cancelTransfer := context.WithCancel(context.Background())
	rt := &runningTransfer{
		id: strings.Repeat("a", 32), dir: dirDownload, ctx: transferCtx, cancel: cancelTransfer,
		download: download, dest: make(chan downloadDestination, 1), done: make(chan struct{}),
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	machine := &fixedDownloadSaveMachine{rt: rt, onClaim: cancelRequest}
	h := downloadSaveHandlers{machine: machine, r: &spyResponder{}}
	returned := make(chan struct{})
	go func() {
		h.handleDownloadSave(requestCtx, &connState{}, jsonrpcRequest{
			ID: json.RawMessage(`1`), Method: "files.downloadSave",
			Params: mustMarshal(filesDownloadSaveParams{TransferID: rt.id}),
		})
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("immediate handler blocked in remote Close after disconnect")
	}
	select {
	case <-fsys.closeStarted:
		t.Fatal("immediate handler performed remote Close I/O")
	default:
	}
	close(fsys.release)
	if err := download.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFilesDownloadSave_RefusesWrongDirectionAndExtraParams(t *testing.T) {
	e := newDownloadTestEnv(t)
	picker := &nativeDownloadPicker{}
	e.ws.SetDialogService(picker)
	sid := e.openSession(t, 1)
	tctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upload := &runningTransfer{
		id: strings.Repeat("b", 32), dir: dirUpload, sessionID: session.ID(sid),
		upload: transfer.Upload{Size: 1}, ctx: tctx, cancel: cancel,
		body: make(chan io.ReadCloser, 1), done: make(chan struct{}), progressWake: make(chan struct{}, 1),
	}
	if err := e.ws.registerAndMint(upload, true); err != nil {
		t.Fatal(err)
	}
	if response := callDownloadSave(t, e.conn, upload.id, 2); response.Error == nil {
		t.Fatal("upload transfer was accepted by files.downloadSave")
	}
	if picker.callCount() != 0 {
		t.Fatal("wrong-direction refusal reached picker")
	}

	raw := jsonrpcCallWithID(t, e.conn, "files.downloadSave", map[string]any{
		"transferId": strings.Repeat("a", 32),
		"path":       "/tmp/renderer-must-not-name-this",
	}, 1)
	var envelope downloadEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32602 {
		t.Fatalf("response = %+v, want invalid params", envelope)
	}
	if picker.callCount() != 0 {
		t.Fatal("strict-parameter refusal reached picker")
	}
}

func TestFilesDownloadSave_MultiChunkAtomicReplacement(t *testing.T) {
	e := newDownloadTestEnv(t)
	body := strings.Repeat("0123456789abcdef", transfer.DefaultChunk/8+17)
	started := startNativeDownload(t, e, body)
	destinationDir := t.TempDir()
	destination := filepath.Join(destinationDir, "chosen.bin")
	if err := os.WriteFile(destination, []byte("old destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	picker := &nativeDownloadPicker{target: &DownloadSaveTarget{
		Sink:   filesystemlocal.New().DurableSink(),
		Upload: transfer.Upload{DestDir: destinationDir, Name: "chosen.bin", Size: int64(len(body)), OnExists: transfer.Overwrite},
	}}
	e.ws.SetDialogService(picker)

	if response := callDownloadSave(t, e.conn, started.TransferID, 4); response.Error != nil {
		t.Fatalf("files.downloadSave: %+v", response.Error)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateSent {
		t.Fatalf("state = %q, want sent", state)
	}
	got, err := os.ReadFile(destination) //nolint:gosec // destination is under the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("destination bytes differ: got %d want %d", len(got), len(body))
	}
	entries, err := os.ReadDir(destinationDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "chosen.bin" {
		t.Fatalf("destination residue = %#v", entries)
	}
}

func TestFilesDownloadSave_RemoteFailurePreservesExistingDestinationAndCleansTemp(t *testing.T) {
	remoteErr := errors.New("remote read failed")
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(&failingSource{err: remoteErr, sent: 0}))
	sid := e.openSession(t, 1)
	remoteDir := t.TempDir()
	remotePath := fixture(t, remoteDir, "remote.bin", "remote bytes")
	bid := e.openBinding(t, sid, remoteDir, 2)
	started := callDownload(t, e.conn, downloadParams(bid, remotePath), 3).mustResult(t)

	destinationDir := t.TempDir()
	destination := filepath.Join(destinationDir, "chosen.bin")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.ws.SetDialogService(&nativeDownloadPicker{target: &DownloadSaveTarget{
		Sink: filesystemlocal.New().DurableSink(),
		Upload: transfer.Upload{
			DestDir: destinationDir, Name: "chosen.bin", Size: started.Size, OnExists: transfer.Overwrite,
		},
	}})
	if response := callDownloadSave(t, e.conn, started.TransferID, 4); response.Error != nil {
		t.Fatalf("files.downloadSave: %+v", response.Error)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateFailed {
		t.Fatalf("state = %q, want failed", state)
	}
	got, err := os.ReadFile(destination) //nolint:gosec // destination is under the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("destination = %q, want previous bytes", got)
	}
	entries, err := os.ReadDir(destinationDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "chosen.bin" {
		t.Fatalf("temporary residue = %#v", entries)
	}
}

func TestFilesDownloadSave_NativeClaimWinsTheHTTPRaceExactlyOnce(t *testing.T) {
	e := newDownloadTestEnv(t)
	started := startNativeDownload(t, e, "race bytes")
	release := make(chan struct{})
	e.ws.SetDialogService(&nativeDownloadPicker{target: &DownloadSaveTarget{
		Sink: gatedSink{release: release},
		Upload: transfer.Upload{
			DestDir: "/tmp", Name: "chosen.bin", Size: int64(len("race bytes")), OnExists: transfer.Overwrite,
		},
	}})
	if response := callDownloadSave(t, e.conn, started.TransferID, 4); response.Error != nil {
		t.Fatalf("files.downloadSave: %+v", response.Error)
	}
	if status, _ := getDownloadRaw(e.ws, started.Ticket); status != 409 {
		t.Fatalf("HTTP claimant status = %d, want 409 while native owns the transfer", status)
	}
	close(release)
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateSent {
		t.Fatalf("state = %q, want sent", state)
	}
}

func TestFilesDownloadSave_CancelAndPickerFailureSettleImmediately(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pick  *nativeDownloadPicker
		state string
		err   bool
	}{
		{name: "cancel", pick: &nativeDownloadPicker{}, state: downloadStateCancelled},
		{name: "failure", pick: &nativeDownloadPicker{err: errors.New("picker failed")}, state: downloadStateFailed, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDownloadTestEnv(t)
			e.ws.SetDialogService(tc.pick)
			started := startNativeDownload(t, e, "bytes")
			response := callDownloadSave(t, e.conn, started.TransferID, 4)
			if (response.Error != nil) != tc.err {
				t.Fatalf("error = %+v, want error %v", response.Error, tc.err)
			}
			if state := awaitTransferState(t, e.ws, started.TransferID); state != tc.state {
				t.Fatalf("state = %q, want %q", state, tc.state)
			}
		})
	}
}

func TestFilesDownloadSave_CancelWhileWailsDialogIsOpenPreventsLaterPromotion(t *testing.T) {
	e := newDownloadTestEnv(t)
	started := startNativeDownload(t, e, "bytes")
	sink := &countingNativeSink{}
	picker := &blockingDownloadSavePicker{
		nativeDownloadPicker: nativeDownloadPicker{target: &DownloadSaveTarget{
			Sink:   sink,
			Upload: transfer.Upload{DestDir: "/tmp", Name: "chosen.bin", Size: started.Size, OnExists: transfer.Overwrite},
		}},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	e.ws.SetDialogService(picker)
	sendControl(t, e.conn, "files.downloadSave", map[string]any{"transferId": started.TransferID}, 4)
	<-picker.started

	if raw := jsonrpcCallWithID(t, e.conn, "files.downloadCancel", map[string]any{"transferId": started.TransferID}, 5); len(raw) == 0 {
		t.Fatal("files.downloadCancel answered nothing")
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateCancelled {
		t.Fatalf("state = %q, want cancelled while dialog remains open", state)
	}
	if sink.callCount() != 0 {
		t.Fatal("destination started before the noncooperative dialog returned")
	}

	close(picker.release)
	if _, err := awaitFrame(e.conn, time.Now().Add(wantWithin), isResponseTo(4)); err != nil {
		t.Fatalf("files.downloadSave did not answer after dialog dismissal: %v", err)
	}
	if sink.callCount() != 0 {
		t.Fatal("cancelled dialog result promoted a destination after returning")
	}
}

func TestFilesDownloadSave_LocalPathNeverCrossesTheWireOnFailure(t *testing.T) {
	const privatePath = "/private/home/alice/chosen.bin.nocx-upload-secret"
	e := newDownloadTestEnv(t)
	e.ws.SetDialogService(&nativeDownloadPicker{err: errors.New("create " + privatePath + ": denied")})
	started := startNativeDownload(t, e, "bytes")
	response := callDownloadSave(t, e.conn, started.TransferID, 4)
	if response.Error == nil {
		t.Fatal("picker failure returned success")
	}
	if strings.Contains(response.Error.Message, privatePath) {
		t.Fatalf("RPC leaked native path: %q", response.Error.Message)
	}
	raw := readNotification(t, e.conn, "files.downloadDone", wantWithin)
	var done filesDownloadDoneParams
	if err := json.Unmarshal(raw, &done); err != nil {
		t.Fatal(err)
	}
	if done.Outcome != downloadStateFailed || done.Error != errNativeDownloadSaveWire.Error() {
		t.Fatalf("done = %+v, want path-free native failure", done)
	}
	if strings.Contains(string(raw), privatePath) {
		t.Fatalf("files.downloadDone leaked native path: %s", raw)
	}
}

func TestFilesDownloadSave_UnavailableCapabilityFailsWithoutWaitingForTTL(t *testing.T) {
	e := newDownloadTestEnv(t)
	started := startNativeDownload(t, e, "bytes")
	response := callDownloadSave(t, e.conn, started.TransferID, 4)
	if response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("response = %+v, want unavailable", response)
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateFailed {
		t.Fatalf("state = %q, want failed", state)
	}
}

func TestFilesDownloadSave_InvalidTargetFailsBeforeSinkStarts(t *testing.T) {
	e := newDownloadTestEnv(t)
	started := startNativeDownload(t, e, "bytes")
	sink := &countingNativeSink{}
	e.ws.SetDialogService(&nativeDownloadPicker{target: &DownloadSaveTarget{
		Sink:   sink,
		Upload: transfer.Upload{Name: "chosen.bin", Size: started.Size, OnExists: transfer.Overwrite},
	}})
	response := callDownloadSave(t, e.conn, started.TransferID, 4)
	if response.Error == nil {
		t.Fatal("invalid target returned start success")
	}
	if state := awaitTransferState(t, e.ws, started.TransferID); state != downloadStateFailed {
		t.Fatalf("state = %q, want failed", state)
	}
	if sink.callCount() != 0 {
		t.Fatal("invalid target reached Sink.Put")
	}
}

type backpressureSource struct {
	writeStarted chan struct{}
	writeDone    chan struct{}
}

func (*backpressureSource) Open(string) (*transfer.Download, error) { return nil, errors.New("unused") }

func (s *backpressureSource) Get(_ context.Context, _ *transfer.Download, w io.Writer, progress func(int64)) (int64, error) {
	close(s.writeStarted)
	n, err := w.Write(make([]byte, transfer.DefaultChunk*2))
	if progress != nil {
		progress(int64(n))
	}
	close(s.writeDone)
	return int64(n), err
}

type gatedSink struct{ release <-chan struct{} }

func (s gatedSink) Put(_ context.Context, _ transfer.Upload, r io.Reader, _ func(int64)) (transfer.Outcome, error) {
	<-s.release
	_, err := io.Copy(io.Discard, r)
	return transfer.Outcome{}, err
}

func TestNativeDownloadPipe_AppliesBackpressure(t *testing.T) {
	release := make(chan struct{})
	source := &backpressureSource{writeStarted: make(chan struct{}), writeDone: make(chan struct{})}
	destination := sinkDownloadDestination{target: DownloadSaveTarget{
		Sink:   gatedSink{release: release},
		Upload: transfer.Upload{Name: "x", Size: transfer.DefaultChunk * 2, OnExists: transfer.Overwrite},
	}}
	done := make(chan downloadDestinationResult, 1)
	go func() { done <- destination.receive(context.Background(), source, &transfer.Download{}, nil) }()
	<-source.writeStarted
	select {
	case <-source.writeDone:
		t.Fatal("source wrote through a sink that had not read: pipe was not backpressured")
	default:
	}
	close(release)
	if result := <-done; result.err != nil {
		t.Fatal(result.err)
	}
}

var errImmediateNativeSink = errors.New("native sink create failed")

type blockingNativeReadFS struct {
	started chan struct{}
	closed  chan struct{}
	start   sync.Once
	close   sync.Once
}

func (f *blockingNativeReadFS) Open(string) (transfer.RemoteReader, int64, error) {
	return &blockingNativeReader{fs: f}, 1, nil
}

type blockingNativeReader struct{ fs *blockingNativeReadFS }

func (r *blockingNativeReader) Read([]byte) (int, error) {
	r.fs.start.Do(func() { close(r.fs.started) })
	<-r.fs.closed
	return 0, errors.New("remote reader was closed")
}

func (r *blockingNativeReader) Close() error {
	r.fs.close.Do(func() { close(r.fs.closed) })
	return nil
}

type immediateFailNativeSink struct{ sourceStarted <-chan struct{} }

func (s immediateFailNativeSink) Put(context.Context, transfer.Upload, io.Reader, func(int64)) (transfer.Outcome, error) {
	<-s.sourceStarted
	return transfer.Outcome{Stranded: []string{"/private/native-temp"}}, errImmediateNativeSink
}

func TestNativeDownloadPipe_SinkFailureInterruptsTheFirstRemoteRead(t *testing.T) {
	fsys := &blockingNativeReadFS{started: make(chan struct{}), closed: make(chan struct{})}
	source := transfer.NewSource(fsys, transfer.DefaultChunk)
	download, err := source.Open("/srv/blocked")
	if err != nil {
		t.Fatal(err)
	}
	destination := sinkDownloadDestination{target: DownloadSaveTarget{
		Sink:   immediateFailNativeSink{sourceStarted: fsys.started},
		Upload: transfer.Upload{DestDir: "/private", Name: "chosen.bin", Size: 1, OnExists: transfer.Overwrite},
	}}
	done := make(chan downloadDestinationResult, 1)
	go func() { done <- destination.receive(context.Background(), source, download, nil) }()

	select {
	case result := <-done:
		if result.state != downloadStateFailed || !errors.Is(result.err, errImmediateNativeSink) {
			t.Fatalf("result = %+v, want causal sink failure", result)
		}
		if len(result.outcome.Stranded) != 1 {
			t.Fatalf("stranded = %#v, want backend accounting", result.outcome.Stranded)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sink failure left the first remote Read blocked")
	}
}

type cancelAwareSink struct{ started chan struct{} }

func (s cancelAwareSink) Put(ctx context.Context, _ transfer.Upload, _ io.Reader, _ func(int64)) (transfer.Outcome, error) {
	close(s.started)
	<-ctx.Done()
	return transfer.Outcome{}, ctx.Err()
}

func TestNativeDownloadPipe_CancellationUnblocksBothCopyLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &backpressureSource{writeStarted: make(chan struct{}), writeDone: make(chan struct{})}
	sinkStarted := make(chan struct{})
	destination := sinkDownloadDestination{target: DownloadSaveTarget{
		Sink:   cancelAwareSink{started: sinkStarted},
		Upload: transfer.Upload{Name: "x", Size: transfer.DefaultChunk * 2, OnExists: transfer.Overwrite},
	}}
	done := make(chan downloadDestinationResult, 1)
	go func() { done <- destination.receive(ctx, source, &transfer.Download{}, nil) }()
	<-source.writeStarted
	<-sinkStarted
	cancel()

	select {
	case result := <-done:
		if result.state != downloadStateCancelled || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("result = %+v, want cancelled", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native source/sink pipe stayed blocked after cancellation")
	}
	select {
	case <-source.writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("source writer stayed blocked after cancellation")
	}
}

func TestFilesDownloadSave_ContractAndRealSocketConform(t *testing.T) {
	schema := loadSchema(t, "files.downloadSave.schema.json")
	dto, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	validateJSON(t, schema, dto, "files.downloadSave DTO")

	e := newDownloadTestEnv(t)
	e.ws.SetDialogService(&nativeDownloadPicker{}) // cancel is a successful empty result
	started := startNativeDownload(t, e, "contract bytes")
	response := callDownloadSave(t, e.conn, started.TransferID, 4)
	if response.Error != nil {
		t.Fatalf("files.downloadSave: %+v", response.Error)
	}
	validateJSON(t, schema, response.Result, "files.downloadSave over the wire")
}
