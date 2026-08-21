package transport

// Behavioural tests for files.upload and files.uploadCancel — the upload
// surface's control plane (spec §5.3).
//
// Four of them are the rules rather than the features, and they are the
// reason the file exists: R2 (a request that names a source is refused),
// R1 (a binding with no sink is refused, proved from the WIRE and not from
// the handle), the path rules, and D8 (closing a binding cancels its
// transfers instead of waiting for them).

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// ── a binding that can be written to ──────────────────────────────────────

// osRemoteFS is transfer.RemoteFS over the local filesystem: the narrow
// write surface the sink declares, satisfied here by os instead of by an
// SFTP lease. The tests therefore drive the REAL sink — its temp naming,
// its O_EXCL create, its promote and its cleanup — rather than a stand-in
// that agrees with the transport by construction.
type osRemoteFS struct{}

func (osRemoteFS) Create(p string) (transfer.RemoteFile, error) {
	// O_EXCL, never os.Create: the sink's contract says so, and a
	// truncating create would silently destroy a concurrent transfer's
	// temp file (D5).
	// #nosec G304 — p is a path the test itself built under t.TempDir().
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // see above
}

func (osRemoteFS) PosixRename(old, dst string) error { return os.Rename(old, dst) }

// Rename is plain SFTP v3 rename, which refuses an existing destination.
func (osRemoteFS) Rename(old, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fs.ErrExist
	}
	return os.Rename(old, dst)
}

func (osRemoteFS) Remove(p string) error { return os.Remove(p) }

// uploadableProvider is a provider that implements filesystem.Uploader —
// what the composition root's sftp provider will be. The local provider
// deliberately does not, and that difference is rule R1.
type uploadableProvider struct {
	filesystem.Provider
	sink transfer.Sink
}

func (p *uploadableProvider) Sink() transfer.Sink { return p.sink }

// uploadableFactory is the writable half of the composition root: the same
// local provider the other files.* tests use, wrapped so the binding
// carries a sink. The endpoint attestation stays EMPTY on purpose — R1 must
// be a property of the sink and never of endpointId, and a writable binding
// with no attestation is what proves the two are not the same question.
func uploadableFactory(sess session.Session, rootPath string) (filesystem.Provider, error) {
	p, err := filesLocalFactory(sess, rootPath)
	if err != nil {
		return nil, err
	}
	return &uploadableProvider{
		Provider: p,
		// A small chunk so an ordinary test file crosses the copy loop
		// several times, which is where cancellation is observed.
		sink: transfer.NewSink(osRemoteFS{}, transfer.WithChunkSize(1024)),
	}, nil
}

// newUploadTestEnv boots a server whose bindings can be written to.
func newUploadTestEnv(t *testing.T, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	return newUploadTestEnvWithLogger(t, log.NewSlogAdapter(nil), opts...)
}

// newUploadTestEnvWithLogger is the same, with the logger injected — the
// ticket-never-logged assertion needs to read everything the server said.
func newUploadTestEnvWithLogger(t *testing.T, logger log.Logger, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	reg := newRegWithStub(logger)
	all := append([]WSServerOption{
		WithFilesystemRegistry(filesystem.New()),
		WithFilesystemProviderFactory(uploadableFactory),
	}, opts...)
	ws := NewWSServer(logger, reg, all...)
	ws.filesPollInterval = 20 * time.Millisecond
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &filesTestEnv{ws: ws, conn: conn}
}

// uploadResult is every field any branch of the union can carry, decoded
// together so a test can assert which branch actually arrived.
type uploadResult struct {
	Collision  string `json:"collision"`
	TransferID string `json:"transferId"`
	Ticket     string `json:"ticket"`
	URL        string `json:"url"`
}

type uploadEnvelope struct {
	Result json.RawMessage  `json:"result"`
	Error  *jsonrpcErrorObj `json:"error"`
}

func callUpload(t *testing.T, conn *websocket.Conn, params any, id int) uploadEnvelope {
	t.Helper()
	var env uploadEnvelope
	if err := json.Unmarshal(jsonrpcCallWithID(t, conn, "files.upload", params, id), &env); err != nil {
		t.Fatalf("files.upload: unmarshal: %v", err)
	}
	return env
}

func (e uploadEnvelope) mustResult(t *testing.T) uploadResult {
	t.Helper()
	if e.Error != nil {
		t.Fatalf("files.upload: %+v", e.Error)
	}
	var got uploadResult
	if err := json.Unmarshal(e.Result, &got); err != nil {
		t.Fatalf("files.upload: decode result: %v", err)
	}
	return got
}

func uploadParams(bid, dir, name string, size int64) map[string]any {
	return map[string]any{"bindingId": bid, "destDir": dir, "name": name, "size": size}
}

// awaitUploadState blocks until the transfer settles and returns its
// terminal state. It waits on the transfer's own done channel — an
// observable state change, never a duration.
func awaitUploadState(t *testing.T, ws *WSServer, transferID string) string {
	t.Helper()
	rt := ws.uploads.get(transferID)
	if rt == nil {
		t.Fatalf("no transfer %s in the registry", transferID)
	}
	select {
	case <-rt.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the transfer never settled") // failsafe, not the assertion
	}
	state, _, _, _ := rt.snapshot()
	return state
}

// ── R2: the renderer may never name the source ───────────────────────────

// TestFilesUpload_RejectsAnythingNamingASource is the most important test
// in this file. A renderer that could spell a path on the BACKEND's disk
// could ask the backend to read ~/.ssh/id_ed25519 or the vault and send it
// to a host of the renderer's choosing — binding ownership proves which
// terminal the caller owns and proves nothing about the backend's
// filesystem.
//
// The refusal must come from the shape of the params struct plus
// DisallowUnknownFields, never from a list of forbidden names somebody
// maintains: the third case below is a name nobody would think to add to
// such a list, and it is refused for exactly the same reason as the first.
func TestFilesUpload_RejectsAnythingNamingASource(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	for i, raw := range []string{
		`{"bindingId":"%s","destDir":"%s","name":"a.txt","size":1,"sourcePath":"/etc/passwd"}`,
		`{"bindingId":"%s","destDir":"%s","name":"a.txt","size":1,"source":"path"}`,
		`{"bindingId":"%s","destDir":"%s","name":"a.txt","size":1,"localPath":"/etc/shadow"}`,
	} {
		params := json.RawMessage(strings.Replace(strings.Replace(raw, "%s", bid, 1), "%s", dir, 1))
		env := callUpload(t, e.conn, params, 10+i)
		if env.Error == nil {
			t.Fatalf("R2: the renderer may never name a source; %s was ACCEPTED", params)
		}
		if env.Error.Code != -32602 {
			t.Fatalf("R2: %s answered %d, want -32602", params, env.Error.Code)
		}
	}
}

// TestFilesUpload_TheParamsStructNamesNoSource is the same rule read off
// the type rather than off the wire: a field added to filesUploadParams is
// a diff somebody has to defend, and this is what makes it one.
func TestFilesUpload_TheParamsStructNamesNoSource(t *testing.T) {
	raw, err := json.Marshal(filesUploadParams{
		BindingID: "b", DestDir: "/tmp", Name: "a", Size: 1,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, forbidden := range []string{"sourcePath", "source", "path", "localPath", "from"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("files.upload params carry %q — R2 says the renderer names the destination and never the source", forbidden)
		}
	}
}

// ── R1: only the machine the tab is on ───────────────────────────────────

// TestFilesUpload_RefusesALocalBinding is R1 from the wire. A tab where
// somebody typed `ssh srv-01` by hand is a KindLocal session, its binding
// carries no sink, and the refusal is that missing field — not a check a
// handler remembered to perform, and not a reading of endpointId.
func TestFilesUpload_RefusesALocalBinding(t *testing.T) {
	// The ordinary local factory: a provider that does NOT implement
	// filesystem.Uploader, so Register receives a nil sink.
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	env := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 5), 3)
	if env.Error == nil {
		t.Fatal("R1: a tab with no remote cannot be uploaded to — a hand-typed ssh is KindLocal and its binding has no sink")
	}
	if env.Error.Code != -32602 {
		t.Fatalf("code = %d, want -32602 (a request-shaped refusal)", env.Error.Code)
	}
	// The message carries the domain error's own words, which is what the
	// panel surfaces — not a transport paraphrase of them.
	if want := (&filesystem.ErrUploadUnsupported{BindingID: bid}).Error(); env.Error.Message != want {
		t.Errorf("message = %q, want %q", env.Error.Message, want)
	}
	// And nothing was started: a refusal costs no transfer.
	if n := len(e.ws.uploads.pick(func(*runningTransfer) bool { return true })); n != 0 {
		t.Fatalf("%d transfers registered after a refused upload, want 0", n)
	}
}

// TestFilesUpload_AcceptsABindingWhoseProviderCanWrite is R1's paired
// success — "for every 'returns an error when…' there is a test that on an
// ordinary machine it succeeds". Without it the refusal above is satisfied
// by a handler that refuses everything.
func TestFilesUpload_AcceptsABindingWhoseProviderCanWrite(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 5), 3).mustResult(t)
	if got.TransferID == "" || got.Ticket == "" || got.URL == "" {
		t.Fatalf("want the stream branch (transferId+ticket+url), got %+v", got)
	}
	if !isLowerHex(got.TransferID, 32) {
		t.Errorf("transferId = %q, want 32 lowercase hex", got.TransferID)
	}
	if !isLowerHex(got.Ticket, uploadTicketHexLen) {
		t.Errorf("ticket is %d chars, want %d lowercase hex (≥128 bits)", len(got.Ticket), uploadTicketHexLen)
	}
	if got.URL != "/upload/"+got.Ticket {
		t.Errorf("url = %q, want /upload/<ticket>", got.URL)
	}
}

// ── §5.3 path validation: refused before anything is stat'd ──────────────

func TestFilesUpload_RefusesANameThatIsNotOnePathComponent(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	cases := map[string]string{
		"empty":               "",
		"posix separator":     "sub/a.txt",
		"windows separator":   `sub\a.txt`,
		"escaping upward":     "..",
		"the directory dot":   ".",
		"leading separator":   "/a.txt",
		"embedded traversal":  "../../etc/passwd",
		"over the name bound": strings.Repeat("n", maxUploadNameRunes+1),
	}
	id := 3
	for what, name := range cases {
		id++
		t.Run(what, func(t *testing.T) {
			env := callUpload(t, e.conn, uploadParams(bid, dir, name, 1), id)
			if env.Error == nil {
				t.Fatalf("name %q was accepted; it is not one path component", name)
			}
			if env.Error.Code != -32602 {
				t.Fatalf("code = %d, want -32602", env.Error.Code)
			}
		})
	}

	// The paired success, and the proof that nothing above was rejected by
	// accident: an ordinary name in the same directory is accepted.
	if got := callUpload(t, e.conn, uploadParams(bid, dir, "ordinary.txt", 1), 99).mustResult(t); got.TransferID == "" {
		t.Fatal("an ordinary one-component name must be accepted")
	}
}

func TestFilesUpload_RefusesADestDirThePathContractRejects(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	cases := map[string]string{
		"empty":     "",
		"relative":  "relative/dir",
		"unclean":   dir + "/./sub",
		"traversal": dir + "/../..",
	}
	id := 3
	for what, destDir := range cases {
		id++
		t.Run(what, func(t *testing.T) {
			env := callUpload(t, e.conn, uploadParams(bid, destDir, "a.txt", 1), id)
			if env.Error == nil {
				t.Fatalf("destDir %q was accepted", destDir)
			}
			if env.Error.Code != -32602 {
				t.Fatalf("code = %d, want -32602", env.Error.Code)
			}
		})
	}
}

func TestFilesUpload_RefusesANegativeSizeAndAnUnknownDecision(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	if env := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", -1), 3); env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("a negative size must be -32602, got %+v", env.Error)
	}
	p := uploadParams(bid, dir, "a.txt", 1)
	p["onExists"] = "clobber"
	if env := callUpload(t, e.conn, p, 4); env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("an unknown collision decision must be -32602, got %+v", env.Error)
	}
	// A zero-byte file is a legitimate upload, so size 0 is accepted.
	if got := callUpload(t, e.conn, uploadParams(bid, dir, "empty.txt", 0), 5).mustResult(t); got.TransferID == "" {
		t.Fatal("size 0 is a legitimate upload of an empty file")
	}
}

// TestFilesUpload_RefusesASourceTicketNothingMinted pins the honest answer
// while the mint side does not exist (Task 8): an id nothing minted names
// nothing, and treating it as a stream upload would silently change what
// the caller asked for.
func TestFilesUpload_RefusesASourceTicketNothingMinted(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	p := uploadParams(bid, dir, "a.txt", 1)
	p["sourceTicket"] = strings.Repeat("ab", uploadTicketHexLen/2)
	env := callUpload(t, e.conn, p, 3)
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("an unmintable sourceTicket must be -32602, got %+v", env.Error)
	}
}

// ── D5: the collision question, asked before a byte moves ────────────────

func TestFilesUpload_AnExistingDestinationWithNoDecisionCreatesNothing(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 5), 3).mustResult(t)
	if got.Collision != "exists" {
		t.Fatalf("want the collision branch, got %+v", got)
	}
	if got.TransferID != "" || got.Ticket != "" {
		t.Fatalf("the collision branch must carry nothing else: %+v", got)
	}
	if n := len(e.ws.uploads.pick(func(*runningTransfer) bool { return true })); n != 0 {
		t.Fatalf("%d transfers registered, want 0 — nothing starts before the person answers", n)
	}
	// Nothing was created and nothing was touched.
	// #nosec G304 — dest is under this test's own t.TempDir().
	body, err := os.ReadFile(dest) //nolint:gosec // see above
	if err != nil || string(body) != "original" {
		t.Fatalf("destination = %q, %v; want it untouched", body, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want 1 — no temp may exist yet", len(entries))
	}
}

// TestFilesUpload_AFreeNameIsNotACollision is the paired success: the probe
// must be able to say "no", or the collision answer above is satisfied by a
// handler that always collides.
func TestFilesUpload_AFreeNameIsNotACollision(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "fresh.txt", 5), 3).mustResult(t)
	if got.Collision != "" {
		t.Fatalf("a free name is not a collision: %+v", got)
	}
	if got.Ticket == "" {
		t.Fatalf("want the stream branch, got %+v", got)
	}
}

// TestFilesUpload_ADirectoryAtTheDestinationIsACollision: a stat that
// answers "not a regular file" still answers "the name is taken". The
// person is asked rather than shown an error they cannot act on.
func TestFilesUpload_ADirectoryAtTheDestinationIsACollision(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "taken"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "taken", 5), 3).mustResult(t)
	if got.Collision != "exists" {
		t.Fatalf("a directory occupying the name is a collision, got %+v", got)
	}
}

// TestFilesUpload_SkipNeedsNoBodyAndLeavesTheDestinationAlone: skip is the
// one decision that moves no bytes, so it takes the no-ticket branch — the
// transfer exists and is already over.
func TestFilesUpload_SkipNeedsNoBodyAndLeavesTheDestinationAlone(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	p := uploadParams(bid, dir, "a.txt", 5)
	p["onExists"] = "skip"
	got := callUpload(t, e.conn, p, 3).mustResult(t)
	if got.TransferID == "" {
		t.Fatalf("skip still mints a transfer so the outcome can be reported: %+v", got)
	}
	if got.Ticket != "" || got.URL != "" {
		t.Fatalf("skip needs no body and must mint no ticket: %+v", got)
	}
	if state := awaitUploadState(t, e.ws, got.TransferID); state != uploadStateSkipped {
		t.Fatalf("state = %q, want %q", state, uploadStateSkipped)
	}
	// #nosec G304 — dest is under this test's own t.TempDir().
	body, err := os.ReadFile(dest) //nolint:gosec // see above
	if err != nil || string(body) != "original" {
		t.Fatalf("destination = %q, %v; skip leaves it alone", body, err)
	}
}

// ── D8: a transfer does not make files.close wait ────────────────────────

// TestFilesUpload_ClosingTheBindingCancelsTheTransferInsteadOfWaitingForIt
// is D8 on the wire.
//
// The transfer below is waiting for a body that will never arrive, so it
// would run forever. Binding.close waits for every use-guard to drain and
// the transfer holds one, so if files.close did NOT cancel first this test
// would not fail an assertion — it would hang, which is exactly the
// production symptom: closing a tab blocks for as long as the upload runs.
// The assertion is therefore that the call returns at all, plus the
// transfer's terminal state.
func TestFilesUpload_ClosingTheBindingCancelsTheTransferInsteadOfWaitingForIt(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 1<<20), 3).mustResult(t)
	if got.TransferID == "" {
		t.Fatalf("want a running transfer, got %+v", got)
	}

	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(jsonrpcCallWithID(t, e.conn, "files.close", map[string]any{"bindingId": bid}, 4), &env); err != nil {
		t.Fatalf("files.close: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.close: %+v", env.Error)
	}
	if state := awaitUploadState(t, e.ws, got.TransferID); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}
	// The destination was never created.
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0", len(entries), err)
	}
}

// TestFilesUpload_ClosingTheSessionCancelsItsTransfers is the same rule on
// the other teardown path: a binding is bounded by its session (spec §5.1),
// so the session's close drains the same guards.
func TestFilesUpload_ClosingTheSessionCancelsItsTransfers(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 1<<20), 3).mustResult(t)
	e.ws.filesSessionClosed(session.ID(sid))
	if state := awaitUploadState(t, e.ws, got.TransferID); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}
}

// ── files.uploadCancel ────────────────────────────────────────────────────

func TestFilesUploadCancel_CancelsAndIsIdempotent(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 1<<20), 3).mustResult(t)

	cancel := func(id int, transferID string) {
		t.Helper()
		var env struct {
			Result json.RawMessage  `json:"result"`
			Error  *jsonrpcErrorObj `json:"error"`
		}
		if err := json.Unmarshal(jsonrpcCallWithID(t, e.conn, "files.uploadCancel", map[string]any{"transferId": transferID}, id), &env); err != nil {
			t.Fatalf("files.uploadCancel: unmarshal: %v", err)
		}
		if env.Error != nil {
			t.Fatalf("files.uploadCancel: %+v", env.Error)
		}
	}
	cancel(4, got.TransferID)
	if state := awaitUploadState(t, e.ws, got.TransferID); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}
	// Cancelling a finished transfer is not an error, and neither is
	// cancelling one that never existed: the person's cancel races the
	// transfer's own completion every time.
	cancel(5, got.TransferID)
	cancel(6, strings.Repeat("0", 32))
}

// TestFilesUploadCancel_RefusesATransferAnotherConnectionOwns: the id is
// not a credential. A second connection that learned it cannot stop
// somebody else's upload.
func TestFilesUploadCancel_RefusesATransferAnotherConnectionOwns(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	got := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 1<<20), 3).mustResult(t)

	connB := connectWS(t, e.ws)
	defer func() { _ = connB.Close() }()
	_ = jsonrpcCallWithID(t, connB, "files.uploadCancel", map[string]any{"transferId": got.TransferID}, 4)

	rt := e.ws.uploads.get(got.TransferID)
	if rt == nil {
		t.Fatal("the transfer vanished")
	}
	select {
	case <-rt.done:
		t.Fatal("a connection that does not own the transfer's session cancelled it")
	default:
	}
}
