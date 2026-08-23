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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		sink: transfer.NewSink(osRemoteFS{}, 1024),
	}, nil
}

// unresponsiveSink is a sink that does NOT honour cancellation: Put blocks
// until the test releases it, whatever the context says and whatever
// happens to the body.
//
// It is the shape of every provider call that can outlive a cancel — a
// wedged lane call, a server that has stopped answering, a cleanup mid
// round trip — and it is the only shape in which D8's teardown claim means
// anything. A transfer still waiting for its body, or one whose sink checks
// ctx.Done, unwinds on the cancel alone and would pass whatever the guard
// did.
type unresponsiveSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *unresponsiveSink) Put(context.Context, transfer.Upload, io.Reader, func(int64)) (transfer.Outcome, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return transfer.Outcome{}, context.Canceled
}

// withUploadUnwind shortens the teardown's bounded wait (D8). Not an
// exported option: production has no reason to turn this knob, and a test
// that must prove a close happens REGARDLESS of the bound should not spend
// the bound proving it.
func withUploadUnwind(d time.Duration) WSServerOption {
	return func(s *WSServer) { s.transfers.unwind = d }
}

// withUploadHeaderTimeout shortens the guard's bound on a header block, and
// with it the handler's own entry deadline. Not an exported option for the
// same reason as withUploadUnwind.
func withUploadHeaderTimeout(d time.Duration) WSServerOption {
	return func(s *WSServer) { s.transfers.header = d }
}

// uploadFactoryWithSink is uploadableFactory with the write half replaced,
// so one test can decide how the sink behaves.
func uploadFactoryWithSink(sink transfer.Sink) FilesystemProviderFactory {
	return func(sess session.Session, rootPath string) (filesystem.Provider, error) {
		p, err := filesLocalFactory(sess, rootPath)
		if err != nil {
			return nil, err
		}
		return &uploadableProvider{Provider: p, sink: sink}, nil
	}
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
	return newUploadTestEnvWith(t, logger, uploadableFactory, opts...)
}

// newUploadTestEnvWithSink is the same again, with the binding's write half
// chosen by the test.
func newUploadTestEnvWithSink(t *testing.T, sink transfer.Sink, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	return newUploadTestEnvWith(t, log.NewSlogAdapter(nil), uploadFactoryWithSink(sink), opts...)
}

func newUploadTestEnvWith(t *testing.T, logger log.Logger, factory FilesystemProviderFactory, opts ...WSServerOption) *filesTestEnv {
	t.Helper()
	reg := newRegWithStub(logger)
	all := append([]WSServerOption{
		WithFilesystemRegistry(filesystem.New()),
		WithFilesystemProviderFactory(factory),
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

// awaitTransferState blocks until the transfer settles and returns its
// terminal state. It waits on the transfer's own done channel — an
// observable state change, never a duration.
func awaitTransferState(t *testing.T, ws *WSServer, transferID string) string {
	t.Helper()
	rt := ws.transfers.get(transferID)
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

// readOnlyLocalProvider is a provider with no write half. It embeds the
// Provider INTERFACE, which promotes exactly that interface's methods, so
// whatever writable value is inside it the wrapper has no Sink at all —
// which is the shape this test needs and, on the remote side, the exact
// mistake internal/app's endpointAttestedProvider is documented to avoid.
type readOnlyLocalProvider struct{ filesystem.Provider }

// readOnlyFactory is the composition-root shape for a provider that cannot
// write. Neither shipped provider is one since D7 was corrected — both the
// local and the sftp provider implement filesystem.Uploader — so R1's
// structural refusal needs a provider built for the purpose to stay
// testable from the wire. That is the point rather than a workaround: the
// refusal is a property of having no sink, and the next provider that
// cannot write inherits it without anybody adding a check.
func readOnlyFactory(sess session.Session, rootPath string) (filesystem.Provider, error) {
	p, err := filesLocalFactory(sess, rootPath)
	if err != nil {
		return nil, err
	}
	return readOnlyLocalProvider{Provider: p}, nil
}

// TestFilesUpload_RefusesABindingWhoseProviderCannotWrite is R1 from the
// wire: the binding carries no sink and the refusal is that missing field —
// not a check a handler remembered to perform, and not a reading of
// endpointId.
//
// It drove the ORDINARY local factory until D7 was corrected, on the
// reasoning that a local tab inserts a path and never copies. A browser drop
// has bytes and no path, so a local tab does upload — onto the backend's own
// machine, which is the machine that tab's shell is on. The paired success
// for that is TestFilesUpload_ALocalBindingWritesOntoTheBackendsOwnMachine
// below; this one keeps the refusal itself under test.
func TestFilesUpload_RefusesABindingWhoseProviderCannotWrite(t *testing.T) {
	e := newUploadTestEnvWith(t, log.NewSlogAdapter(nil), readOnlyFactory)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	env := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 5), 3)
	if env.Error == nil {
		t.Fatal("R1: a binding whose provider cannot write has no sink, and Uploader must refuse on that")
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
	if n := len(e.ws.transfers.pick(func(*runningTransfer) bool { return true })); n != 0 {
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

// TestFilesUpload_RefusesASourceTicketNothingMinted: an id nothing minted
// names nothing, and treating it as a stream upload would silently change
// what the caller asked for. Both shapes are refused and both are -32602 —
// a ticket of the right width that was never minted, and one of the wrong
// width, which is what every sourceTicket used to be held to.
func TestFilesUpload_RefusesASourceTicketNothingMinted(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	for i, ticket := range []string{
		strings.Repeat("ab", sourceTicketHexLen/2), // well-shaped, never minted
		strings.Repeat("ab", uploadTicketHexLen/2), // the SINK ticket's width
		"NOTHEX0123456789abcdef0123456789",
	} {
		p := uploadParams(bid, dir, "a.txt", 1)
		p["sourceTicket"] = ticket
		env := callUpload(t, e.conn, p, 3+i)
		if env.Error == nil || env.Error.Code != -32602 {
			t.Fatalf("sourceTicket %q must be -32602, got %+v", ticket, env.Error)
		}
	}
	// And nothing started: a refused redemption costs no transfer.
	if n := len(e.ws.transfers.pick(func(*runningTransfer) bool { return true })); n != 0 {
		t.Fatalf("%d transfers registered after refused redemptions, want 0", n)
	}
}

// TestFilesUpload_ACollisionDoesNotSpendTheTicket is the interval the claim
// sits inside, stated with both ends: the ticket is live from the mint
// until the request that actually starts a transfer, and a collision answer
// starts none. The renderer's whole collision protocol is "ask the person,
// then call again with onExists" — a claim taken before the probe would
// spend the ticket on the answer that asks for the second call, so the
// second call could never succeed and the native picker would be unusable
// against any name that already exists.
func TestFilesUpload_ACollisionDoesNotSpendTheTicket(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "taken.txt"), []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	src := filepath.Join(t.TempDir(), "taken.txt")
	want := []byte("the replacement")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pick, err := e.ws.UploadSources().Mint(src)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	first := uploadParams(bid, dir, "taken.txt", pick.Size)
	first["sourceTicket"] = pick.Ticket
	if got := callUpload(t, e.conn, first, 3).mustResult(t); got.Collision != "exists" {
		t.Fatalf("want the collision branch, got %+v", got)
	}

	second := uploadParams(bid, dir, "taken.txt", pick.Size)
	second["sourceTicket"] = pick.Ticket
	second["onExists"] = "overwrite"
	got := callUpload(t, e.conn, second, 4).mustResult(t)
	if got.TransferID == "" {
		t.Fatalf("the ticket did not survive the collision question: %+v", got)
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	landed, err := os.ReadFile(filepath.Join(dir, "taken.txt")) //nolint:gosec // see above
	if err != nil || !bytes.Equal(landed, want) {
		t.Fatalf("destination = %q, %v; want the replacement", landed, err)
	}
}

// Skip moves no bytes, so a redeemed ticket's reader is opened and closed
// without being read — and the ticket is still spent, because the person
// answered the question this gesture asked.
func TestFilesUpload_ASourceTicketWithSkipMovesNothing(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pick, err := e.ws.UploadSources().Mint(src)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	p := uploadParams(bid, dir, "a.txt", pick.Size)
	p["sourceTicket"] = pick.Ticket
	p["onExists"] = "skip"
	got := callUpload(t, e.conn, p, 3).mustResult(t)
	if got.Ticket != "" || got.URL != "" {
		t.Fatalf("skip needs no body: %+v", got)
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateSkipped {
		t.Fatalf("state = %q, want %q", state, uploadStateSkipped)
	}
	// #nosec G304 — under this test's own t.TempDir().
	body, err := os.ReadFile(dest) //nolint:gosec // see above
	if err != nil || string(body) != "original" {
		t.Fatalf("destination = %q, %v; skip leaves it alone", body, err)
	}
	// The ticket was spent all the same: the gesture is over.
	again := uploadParams(bid, dir, "b.txt", pick.Size)
	again["sourceTicket"] = pick.Ticket
	if env := callUpload(t, e.conn, again, 4); env.Error == nil {
		t.Fatal("a skipped redemption left the ticket live")
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
	if n := len(e.ws.transfers.pick(func(*runningTransfer) bool { return true })); n != 0 {
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
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateSkipped {
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
// would run forever if nothing stopped it. What this pins is the cancel
// half: files.close reaches the transfer and settles it. It does NOT pin
// the "never waits" half — this transfer is still selecting on rt.body, so
// it unwinds on the cancel alone; the test that reaches a sink which will
// not unwind is TeardownClosesTheBindingWhileASinkIgnoresTheCancel below.
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
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}
	// The destination was never created.
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0", len(entries), err)
	}
}

// TestFilesUpload_TeardownClosesTheBindingWhileASinkIgnoresTheCancel is the
// case the two tests above cannot reach, and the one D8 exists for.
//
// They cancel a transfer that has not entered the sink at all: it is still
// selecting on rt.body, so the cancel is observed by ctx.Done and it unwinds
// at once. Every arrangement of the guard passes that. Here the transfer is
// INSIDE a sink call that will not return — the shape of a wedged lane call
// or a server that stopped answering — so the only thing that can let
// files.close finish is that the transfer holds no use-guard for
// Binding.close to drain. Before D8 was honoured this did not fail an
// assertion; it deadlocked, which is the production symptom: cancelTransfersFor
// gives up at its bound, logs "closing anyway", and then blocks for ever
// inside a close that cannot proceed.
//
// The unwind bound is shortened through the same option production reads, so
// the test spends milliseconds proving a close happens REGARDLESS rather
// than five seconds. It is not what makes the test pass: with the guard held
// the close blocks after the bound expires, not before it.
func TestFilesUpload_TeardownClosesTheBindingWhileASinkIgnoresTheCancel(t *testing.T) {
	sink := &unresponsiveSink{entered: make(chan struct{}), release: make(chan struct{})}
	e := newUploadTestEnvWithSink(t, sink, withUploadUnwind(50*time.Millisecond))
	// Registered after the env's own cleanups, so it runs BEFORE them: the
	// server's Stop would otherwise wait on the same wedged sink.
	t.Cleanup(func() { close(sink.release) })

	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	// The POST hands the body over and then waits for the transfer, so it
	// cannot run on this goroutine. Its own outcome is not the assertion.
	go func() {
		resp, err := uploadHTTPClient.Post(
			uploadURLFor(e.ws, ticket), "application/octet-stream", bytes.NewReader([]byte("hello")))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-sink.entered // the transfer is inside the sink and will not come out

	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(jsonrpcCallWithID(t, e.conn, "files.close", map[string]any{"bindingId": bid}, 4), &env); err != nil {
		t.Fatalf("files.close: unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("files.close: %+v", env.Error)
	}

	// And it really closed, rather than answering ahead of a close still
	// waiting: the binding is gone from the registry.
	var after struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{"bindingId": bid, "path": dir}, 5), &after); err != nil {
		t.Fatalf("files.list: unmarshal: %v", err)
	}
	if after.Error == nil {
		t.Fatal("the binding still answers after files.close returned; it was never closed")
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
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateCancelled {
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
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateCancelled {
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

	rt := e.ws.transfers.get(got.TransferID)
	if rt == nil {
		t.Fatal("the transfer vanished")
	}
	select {
	case <-rt.done:
		t.Fatal("a connection that does not own the transfer's session cancelled it")
	default:
	}
}

// ── D1's other source: a ticket, redeemed ────────────────────────────────

// TestFilesUpload_ASourceTicketMovesTheBytesToTheSink is THE test this
// surface never had, and the reason the seam was never built.
//
// Task 8 minted source tickets and Task 5 refused every one of them, so the
// two halves of one mechanism were each green alone: the mint tests stop at
// Claim, and every files.upload test drives the STREAM source. Nothing on
// either side ever asked "and then what moves the bytes?" — which is
// exactly the question a person picking a file in the native picker is
// asking, and on the desktop build the answer was -32602.
//
// So this drives the whole path: a human's gesture mints a ticket behind
// the seam, the renderer echoes it on files.upload with its own bindingId,
// and the bytes land at the destination. It asserts the CONTENT, because a
// transfer that reports "written" and wrote nothing is the failure this
// test is here to catch.
func TestFilesUpload_ASourceTicketMovesTheBytesToTheSink(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// The mint happens where a human chose the file — behind the picker
	// seam, never on the wire. The test stands in for the human.
	want := []byte("the bytes a person chose, all of them\n")
	src := filepath.Join(t.TempDir(), "chosen.txt")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pick, err := e.ws.UploadSources().Mint(src)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	p := uploadParams(bid, dir, pick.Name, pick.Size)
	p["sourceTicket"] = pick.Ticket
	got := callUpload(t, e.conn, p, 3).mustResult(t)

	// A request WITH a ticket is a path upload: it sends no body, so it
	// takes the started branch and gets no url (§5.3).
	if got.TransferID == "" {
		t.Fatalf("a redeemed ticket must start a transfer, got %+v", got)
	}
	if got.Ticket != "" || got.URL != "" {
		t.Fatalf("a path upload needs no body and must mint no sink ticket: %+v", got)
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}

	// #nosec G304 — under this test's own t.TempDir().
	landed, err := os.ReadFile(filepath.Join(dir, "chosen.txt")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("the destination does not exist: %v", err)
	}
	if !bytes.Equal(landed, want) {
		t.Fatalf("destination = %q, want %q", landed, want)
	}
}

// A source ticket is one-shot on this path too: the second files.upload
// naming it finds nothing, so a ticket that leaked to a second caller
// cannot be replayed into a second transfer.
func TestFilesUpload_ASourceTicketIsRedeemedExactlyOnce(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	src := filepath.Join(t.TempDir(), "once.txt")
	if err := os.WriteFile(src, []byte("once"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pick, err := e.ws.UploadSources().Mint(src)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	first := uploadParams(bid, dir, "a.txt", pick.Size)
	first["sourceTicket"] = pick.Ticket
	if id := callUpload(t, e.conn, first, 3).mustResult(t).TransferID; id == "" {
		t.Fatal("the first redemption did not start a transfer")
	}

	second := uploadParams(bid, dir, "b.txt", pick.Size)
	second["sourceTicket"] = pick.Ticket
	env := callUpload(t, e.conn, second, 4)
	if env.Error == nil || env.Error.Code != -32602 {
		t.Fatalf("a second redemption must be refused with -32602, got %+v", env)
	}
}

// ── R1 on the source side: a ticket is redeemed by the tab it names ──────

// TestFilesUpload_RefusesATicketDroppedOnAnotherTab is R1 read as a
// property of the addressing rather than as renderer discipline.
//
// Binding authorisation proves the caller owns the DESTINATION. It proves
// nothing about where the file was dropped — so a ticket that named no tab
// could be paired with any remote binding the connection owned, and a file
// dropped on tab A could be sent to host B by a renderer that simply
// quoted the other bindingId. Both bindings below are legitimately this
// connection's; the pairing is what is refused.
func TestFilesUpload_RefusesATicketDroppedOnAnotherTab(t *testing.T) {
	e := newUploadTestEnv(t)
	sidA := e.openSession(t, 1)
	sidB := e.openSession(t, 2)
	dirB := t.TempDir()
	bidB := e.openBinding(t, sidB, dirB, 3)

	src := filepath.Join(t.TempDir(), "for-a.txt")
	if err := os.WriteFile(src, []byte("meant for tab A"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Minted the way a window drop mints: bound to the tab it landed on.
	pick, err := e.ws.UploadSources().mint(src, session.ID(sidA))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	p := uploadParams(bidB, dirB, "for-a.txt", pick.Size)
	p["sourceTicket"] = pick.Ticket
	env := callUpload(t, e.conn, p, 4)
	if env.Error == nil {
		t.Fatal("a ticket dropped on tab A was redeemed against tab B's binding")
	}
	if env.Error.Code != -32602 {
		t.Fatalf("code = %d, want -32602", env.Error.Code)
	}
	if _, statErr := os.Stat(filepath.Join(dirB, "for-a.txt")); statErr == nil {
		t.Fatal("the refused pairing still put a file on tab B's machine")
	}
	// The ticket is spent either way: it named a gesture, and the gesture
	// has now been answered wrongly once. Nothing may retry it.
	if n := e.ws.UploadSources().Len(); n != 0 {
		t.Errorf("%d tickets survive a refused pairing, want 0", n)
	}
}

// The paired success, without which the refusal above is satisfied by a
// handler that refuses everything: the same ticket, redeemed against the
// tab it was dropped on, moves the bytes.
func TestFilesUpload_ATicketIsRedeemedByTheTabItWasDroppedOn(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	want := []byte("dropped on this very tab")
	src := filepath.Join(t.TempDir(), "here.txt")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pick, err := e.ws.UploadSources().mint(src, session.ID(sid))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	p := uploadParams(bid, dir, "here.txt", pick.Size)
	p["sourceTicket"] = pick.Ticket
	got := callUpload(t, e.conn, p, 3).mustResult(t)
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	landed, err := os.ReadFile(filepath.Join(dir, "here.txt")) //nolint:gosec // see above
	if err != nil || !bytes.Equal(landed, want) {
		t.Fatalf("destination = %q, %v; want the dropped bytes", landed, err)
	}
}

// The picker's ticket names no tab, and that is a decision rather than an
// omission (see SourceTicketStore.Mint): the picker gesture is "choose a
// file", not "drop it there", so there is no tab it could name that the
// renderer would not itself have authored. It is bound by the tab that
// redeems it, once — which the one-shot claim already guarantees.
func TestFilesUpload_APickerTicketIsBoundByTheTabThatRedeemsIt(t *testing.T) {
	e := newUploadTestEnv(t)
	sidA := e.openSession(t, 1)
	sidB := e.openSession(t, 2)
	dirB := t.TempDir()
	bidB := e.openBinding(t, sidB, dirB, 3)
	_ = sidA

	want := []byte("chosen in the picker")
	src := filepath.Join(t.TempDir(), "picked.txt")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Mint, not mint(path, sid): this is the picker seam's call.
	pick, err := e.ws.UploadSources().Mint(src)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	p := uploadParams(bidB, dirB, "picked.txt", pick.Size)
	p["sourceTicket"] = pick.Ticket
	got := callUpload(t, e.conn, p, 4).mustResult(t)
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	landed, err := os.ReadFile(filepath.Join(dirB, "picked.txt")) //nolint:gosec // see above
	if err != nil || !bytes.Equal(landed, want) {
		t.Fatalf("destination = %q, %v", landed, err)
	}
}
