package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// ── the source-ticket store ────────────────────────────────────────────────

// seedFile writes n bytes into the test's temp dir and returns the absolute
// path. The DIRECTORY is the secret R2 protects: every assertion below that
// says "no path" means "not this directory, and not this separator".
func seedFile(t *testing.T, name string, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return p
}

var hexTicket = regexp.MustCompile(`^[0-9a-f]{32}$`)

// A ticket is a credential, so it is crypto/rand and at least 128 bits, and
// two mints never collide.
func TestSourceTicketStore_TicketIsRandomAndBoundInShape(t *testing.T) {
	st := NewSourceTicketStore(nil)
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		pick, err := st.Mint(seedFile(t, "f.bin", 3))
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if !hexTicket.MatchString(pick.Ticket) {
			t.Fatalf("ticket = %q, want 32 lowercase hex characters (128 bits)", pick.Ticket)
		}
		if seen[pick.Ticket] {
			t.Fatalf("ticket %q was minted twice", pick.Ticket)
		}
		seen[pick.Ticket] = true
	}
}

// What the renderer learns is a base name and a size. The directory the file
// came from is the thing R2 withholds.
func TestSourceTicketStore_MintYieldsBaseNameAndSizeOnly(t *testing.T) {
	path := seedFile(t, "notes.txt", 11)
	st := NewSourceTicketStore(nil)
	pick, err := st.Mint(path)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if pick.Name != "notes.txt" {
		t.Errorf("name = %q, want the base name", pick.Name)
	}
	if pick.Size != 11 {
		t.Errorf("size = %d, want 11", pick.Size)
	}
	if strings.ContainsAny(pick.Name, `/\`) {
		t.Errorf("name %q is path-shaped", pick.Name)
	}
	if strings.Contains(pick.Name, filepath.Dir(path)) {
		t.Errorf("name %q carries the source directory", pick.Name)
	}
}

// Claim is one-shot: the second attempt finds nothing, so a leaked ticket
// cannot be replayed against a second transfer.
func TestSourceTicketStore_ClaimIsOneShot(t *testing.T) {
	path := seedFile(t, "one.bin", 4)
	st := NewSourceTicketStore(nil)
	pick, err := st.Mint(path)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	src, ok := st.Claim(pick.Ticket)
	if !ok {
		t.Fatalf("first Claim failed")
	}
	// What a claim answers with is the OPEN FILE, never a name somebody
	// else would have to resolve — see SourceFile. So the assertion is on
	// the bytes it reads.
	defer src.File.Close() //nolint:errcheck // the test owns it from here
	got, err := io.ReadAll(src.File)
	if err != nil {
		t.Fatalf("read the claimed handle: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("claimed handle read %d bytes, want the 4 the file holds", len(got))
	}
	if src.Name != filepath.Base(path) || src.Size != 4 {
		t.Errorf("claimed %+v, want name %q and size 4", src, filepath.Base(path))
	}
	if _, ok := st.Claim(pick.Ticket); ok {
		t.Errorf("second Claim succeeded; the ticket must be one-shot")
	}
	if st.Len() != 0 {
		t.Errorf("Len after claim = %d, want 0", st.Len())
	}
}

// An unknown ticket names nothing — a renderer that guesses gets no file.
func TestSourceTicketStore_UnknownTicketClaimsNothing(t *testing.T) {
	st := NewSourceTicketStore(nil)
	if _, ok := st.Claim("00000000000000000000000000000000"); ok {
		t.Errorf("an unminted ticket was claimable")
	}
	if _, ok := st.Claim(""); ok {
		t.Errorf("the empty ticket was claimable")
	}
}

// The TTL is an interval with both ends: the ticket exists from the mint
// until the TTL passes, and after it the ticket names nothing AND is gone
// from the map.
func TestSourceTicketStore_ExpiresAndIsEvicted(t *testing.T) {
	path := seedFile(t, "old.bin", 2)
	st := NewSourceTicketStore(nil)
	base := time.Now()
	st.now = func() time.Time { return base }
	pick, err := st.Mint(path)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	st.now = func() time.Time { return base.Add(sourceTicketTTL - time.Second) }
	if _, ok := st.Claim(pick.Ticket); !ok {
		t.Fatalf("ticket expired before its TTL")
	}

	pick2, err := st.Mint(path)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	st.now = func() time.Time { return base.Add(2 * sourceTicketTTL) }
	if _, ok := st.Claim(pick2.Ticket); ok {
		t.Errorf("an expired ticket was claimable")
	}
	// And the eviction pass, not only the claim-time check: minting again
	// must not find the expired entry still occupying the map.
	if _, err := st.Mint(path); err != nil {
		t.Fatalf("Mint after expiry: %v", err)
	}
	if st.Len() != 1 {
		t.Errorf("Len = %d, want 1 — the expired entry was not evicted", st.Len())
	}
}

// The map is bounded. A refusal is a refusal, not an eviction of somebody
// else's live ticket.
func TestSourceTicketStore_IsBounded(t *testing.T) {
	path := seedFile(t, "many.bin", 1)
	st := NewSourceTicketStore(nil)
	for i := 0; i < maxSourceTickets; i++ {
		if _, err := st.Mint(path); err != nil {
			t.Fatalf("Mint %d: %v", i, err)
		}
	}
	if _, err := st.Mint(path); err == nil {
		t.Fatalf("Mint past the bound succeeded; want a refusal")
	}
	if st.Len() != maxSourceTickets {
		t.Errorf("Len = %d, want %d", st.Len(), maxSourceTickets)
	}
}

// Every refusal reaches the renderer as an error message. A message naming
// the path would hand back the directory the ticket exists to withhold —
// which is R2 lost through the error channel instead of the result.
func TestSourceTicketStore_MintErrorsNameNoPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "real.bin")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cases := map[string]string{
		"a directory":     dir,
		"a missing file":  filepath.Join(dir, "absent.bin"),
		"a relative path": "relative/path.bin",
	}
	st := NewSourceTicketStore(nil)
	for what, path := range cases {
		_, err := st.Mint(path)
		if err == nil {
			t.Errorf("%s was minted; want a refusal", what)
			continue
		}
		if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), path) {
			t.Errorf("%s: error %q carries the path", what, err)
		}
	}
}

// ── the window drop ────────────────────────────────────────────────────────

// recordingEmitter is the transport the drop reaches: it answers what kind
// of tab the drop landed on and records what was sent to it. Remote by
// default, because that is the tab an upload is even possible on.
type recordingEmitter struct {
	kind     session.Kind
	unknown  bool // the session is not open at all
	sessions []string
	picks    [][]SourcePick
}

func (r *recordingEmitter) TabKind(string) (session.Kind, bool) {
	if r.unknown {
		return 0, false
	}
	return r.kind, true
}

func (r *recordingEmitter) EmitFilesDropped(sessionID string, picks []SourcePick) error {
	r.sessions = append(r.sessions, sessionID)
	r.picks = append(r.picks, picks)
	return nil
}

func remoteEmitter() *recordingEmitter { return &recordingEmitter{kind: session.KindRemote} }

const dropSessionID = "0123456789abcdef0123456789abcdef"

// The drop mints one ticket per file and emits them against the session the
// drop target named. It resolves NO destination: the renderer calls
// files.upload with its own bindingId, over the same authorised route as
// every other caller, so the native gesture never becomes a second
// addressing scheme (design §5.5).
func TestFilesDropped_MintsPerFileAndNamesNoDestination(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, n := range []string{"a.txt", "b.txt"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("hi"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		paths = append(paths, p)
	}
	em := remoteEmitter()
	st := NewSourceTicketStore(em)

	if err := st.Dropped(paths, map[string]string{
		"data-file-drop-target": "",
		"data-session-id":       dropSessionID,
	}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}

	if len(em.picks) != 1 {
		t.Fatalf("emitted %d notifications, want 1", len(em.picks))
	}
	if em.sessions[0] != dropSessionID {
		t.Errorf("sessionId = %q, want the drop target's data-session-id", em.sessions[0])
	}
	got := em.picks[0]
	if len(got) != 2 {
		t.Fatalf("emitted %d picks, want 2", len(got))
	}
	names := []string{got[0].Name, got[1].Name}
	sort.Strings(names)
	if names[0] != "a.txt" || names[1] != "b.txt" {
		t.Errorf("names = %v, want the two base names", names)
	}
	for _, p := range got {
		if !hexTicket.MatchString(p.Ticket) {
			t.Errorf("ticket %q is not a minted ticket", p.Ticket)
		}
		if strings.ContainsAny(p.Name, `/\`) || strings.Contains(p.Name, dir) {
			t.Errorf("pick %+v carries a path", p)
		}
		if p.Size != 2 {
			t.Errorf("size = %d, want 2", p.Size)
		}
	}
	// The tickets are real: each claims back an open handle on the bytes it
	// was minted for.
	for _, p := range got {
		src, ok := st.Claim(p.Ticket)
		if !ok {
			t.Fatalf("ticket %q claimed nothing", p.Ticket)
		}
		body, err := io.ReadAll(src.File)
		_ = src.File.Close()
		if err != nil {
			t.Fatalf("read the claimed handle: %v", err)
		}
		if string(body) != "hi" {
			t.Errorf("claimed handle read %q, want the dropped file's bytes", body)
		}
	}
}

// A drop target that names no session is refused whole: nothing is minted,
// so a drop the renderer could not route never leaves a ticket behind.
func TestFilesDropped_RefusesATargetThatNamesNoSession(t *testing.T) {
	em := remoteEmitter()
	st := NewSourceTicketStore(em)
	err := st.Dropped([]string{seedFile(t, "x.bin", 1)}, map[string]string{
		"data-file-drop-target": "",
	})
	if err == nil {
		t.Fatalf("a drop with no data-session-id was accepted")
	}
	if st.Len() != 0 {
		t.Errorf("Len = %d, want 0 — a refused drop minted a ticket", st.Len())
	}
	if len(em.picks) != 0 {
		t.Errorf("a refused drop emitted %d notifications", len(em.picks))
	}
}

// The attribute is renderer-authored DOM, so it is held to the shape a
// server-minted session id has before it goes back out on the wire.
func TestFilesDropped_RefusesASessionIDThatIsNotOne(t *testing.T) {
	em := remoteEmitter()
	st := NewSourceTicketStore(em)
	err := st.Dropped([]string{seedFile(t, "x.bin", 1)}, map[string]string{
		"data-session-id": "../../etc/passwd",
	})
	if err == nil {
		t.Fatalf("a hostile data-session-id was accepted")
	}
	if st.Len() != 0 || len(em.picks) != 0 {
		t.Errorf("a refused drop minted %d tickets and emitted %d notifications", st.Len(), len(em.picks))
	}
}

// A drop of a directory (out of scope, §4) alongside a file uploads the
// file and says how many were refused — the refusal is visible, not silent.
func TestFilesDropped_SkipsWhatCannotBeMintedAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	em := remoteEmitter()
	st := NewSourceTicketStore(em)
	err := st.Dropped([]string{file, sub}, map[string]string{"data-session-id": dropSessionID})
	if err == nil {
		t.Fatalf("a drop with an unusable member reported success")
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("error %q carries the source directory", err)
	}
	if len(em.picks) != 1 || len(em.picks[0]) != 1 {
		t.Fatalf("emitted %v, want the one file that could be minted", em.picks)
	}
	if em.picks[0][0].Name != "ok.txt" {
		t.Errorf("emitted %q, want ok.txt", em.picks[0][0].Name)
	}
}

// ── the wire ───────────────────────────────────────────────────────────────

// fakeUploadPicker stands in for the human at the native picker: it opens
// only when the transport asks, and it returns the file THE PICKER chose,
// never one the caller named.
type fakeUploadPicker struct {
	fakeDialogService
	store *SourceTicketStore
	path  string
	opens int
	fail  error
}

func (f *fakeUploadPicker) OpenFileForUpload(_ context.Context) (SourcePick, error) {
	f.opens++
	if f.fail != nil {
		return SourcePick{}, f.fail
	}
	if f.path == "" {
		return SourcePick{}, nil // cancelled
	}
	return f.store.Mint(f.path)
}

// THE HEADLINE, and the whole point of the task: a source ticket cannot be
// minted from the wire. Every registered control method is called twice —
// once with params that name a path, once with none — and across all 302
// calls the only mint in the whole sweep is the one the native picker
// handler made, because the picker was OPENED. Opening a picker is a human
// at a dialog, not a parameter a renderer can spell.
func TestSourceTicket_CannotBeMintedFromTheWire(t *testing.T) {
	h := newInventoryHarness(t)
	picker := &fakeUploadPicker{path: seedFile(t, "chosen.bin", 7)}
	picker.store = NewSourceTicketStore(nil)
	h.ws.SetDialogService(picker)

	names := make([]string, 0, len(h.ws.methods))
	for name := range h.ws.methods {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) < 100 {
		t.Fatalf("only %d methods registered; the sweep is not sweeping", len(names))
	}

	// Sweep one: every method, asked for a file on the backend's disk under
	// each of the four names such a parameter would plausibly have. If a
	// source path were reachable from the wire at all, this is the shape
	// that would reach it. `localPath` is in the list because it is a name
	// the wire already uses — outbound, on a local tab's files.dropped — and
	// a field a renderer has SEEN is the likeliest one for it to try
	// sending back.
	id := 0
	for _, name := range names {
		id++
		jsonrpcCallWithID(t, h.conn, name, map[string]any{
			"sourcePath": "/home/dev/.ssh/id_ed25519",
			"source":     "/home/dev/.ssh/id_ed25519",
			"path":       "/home/dev/.ssh/id_ed25519",
			"localPath":  "/home/dev/.ssh/id_ed25519",
		}, id)
	}
	if picker.opens != 0 {
		t.Errorf("a call naming a path opened the picker %d times", picker.opens)
	}
	if n := picker.store.Len(); n != 0 {
		t.Fatalf("%d tickets exist after asking every one of %d methods for a path, want 0", n, len(names))
	}
	// And the SERVER's own mint is empty too. The picker's store is the one
	// a legitimate mint lands in; s.sources is the one a wire-reachable mint
	// would land in, because it is the store the upload flow claims from —
	// so watching only the picker's store leaves the attack it is guarding
	// against unobserved. Measured, not assumed: with a mint wired into the
	// dispatcher for every frame naming `sourcePath`, this sweep stayed
	// green until this assertion existed (nocx-9le.8.2).
	if n := h.ws.UploadSources().Len(); n != 0 {
		t.Fatalf("%d tickets exist in the SERVER's store after asking every one of %d methods for a path, want 0", n, len(names))
	}

	// Sweep two: every method again with no params at all — the shape that
	// DOES reach the picker handler. Exactly one method opens the picker,
	// and exactly one ticket exists afterwards.
	for _, name := range names {
		id++
		jsonrpcCallWithID(t, h.conn, name, map[string]any{}, id)
	}
	if picker.opens != 1 {
		t.Errorf("the picker opened %d times over %d methods, want exactly once (dialog.openFileForUpload)", picker.opens, len(names))
	}
	if n := picker.store.Len(); n != 1 {
		t.Fatalf("%d tickets exist after sweeping %d methods twice, want 1 — the one the picker minted", n, len(names))
	}
	if n := h.ws.UploadSources().Len(); n != 0 {
		t.Fatalf("%d tickets exist in the SERVER's store after sweeping %d methods twice, want 0", n, len(names))
	}
	// And the one ticket that does exist names the PICKER's file, not the
	// path the first sweep asked every method for.
	for tk := range picker.store.entries {
		src, ok := picker.store.Claim(tk)
		if !ok {
			t.Fatalf("the minted ticket claimed nothing")
		}
		_ = src.File.Close()
		if src.Name != "chosen.bin" {
			t.Errorf("the ticket names %q; the wire chose the file", src.Name)
		}
	}
}

// The result carries a ticket, a base name and a size. It never carries the
// directory the file came from — that is R2 on this method.
func TestDialogOpenFileForUpload_NamesNoSource(t *testing.T) {
	path := seedFile(t, "report.pdf", 42)
	picker := &fakeUploadPicker{path: path, store: NewSourceTicketStore(nil)}
	h := newInventoryHarness(t)
	h.ws.SetDialogService(picker)

	resp := jsonrpcCall(t, h.conn, "dialog.openFileForUpload", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("dialog.openFileForUpload: %+v", envelope.Error)
	}
	var got struct {
		SourceTicket string `json:"sourceTicket"`
		Name         string `json:"name"`
		Size         int64  `json:"size"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !hexTicket.MatchString(got.SourceTicket) {
		t.Errorf("sourceTicket = %q, want 32 lowercase hex characters", got.SourceTicket)
	}
	if got.Name != "report.pdf" || got.Size != 42 {
		t.Errorf("got name=%q size=%d, want report.pdf/42", got.Name, got.Size)
	}
	// Nothing path-shaped anywhere in the raw result — no separator, no
	// directory, no second field somebody added later.
	raw := string(envelope.Result)
	if strings.Contains(raw, filepath.Dir(path)) || strings.ContainsAny(strings.ReplaceAll(raw, `\"`, ""), `/\`) {
		t.Errorf("result %s is path-shaped", raw)
	}
}

// Cancel is an empty ticket, never an error — the renderer treats it as "no
// change", the way dialog.openFile's empty path already works.
func TestDialogOpenFileForUpload_CancelYieldsAnEmptyTicket(t *testing.T) {
	picker := &fakeUploadPicker{store: NewSourceTicketStore(nil)}
	h := newInventoryHarness(t)
	h.ws.SetDialogService(picker)

	resp := jsonrpcCall(t, h.conn, "dialog.openFileForUpload", map[string]any{})
	var envelope struct {
		Result struct {
			SourceTicket string `json:"sourceTicket"`
			Name         string `json:"name"`
			Size         int64  `json:"size"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("cancel reported an error: %+v", envelope.Error)
	}
	if envelope.Result.SourceTicket != "" {
		t.Errorf("sourceTicket = %q, want empty on cancel", envelope.Result.SourceTicket)
	}
	if picker.store.Len() != 0 {
		t.Errorf("a cancelled pick minted %d tickets", picker.store.Len())
	}
}

// No Wails means no native picker: the method reports itself unavailable
// the way dialog.openFile already does, and nothing is invented in its
// place. This is the dev-web configuration, which is where this app is
// developed and tested.
func TestDialogOpenFileForUpload_UnavailableWithoutWails(t *testing.T) {
	h := newInventoryHarness(t)
	resp := jsonrpcCall(t, h.conn, "dialog.openFileForUpload", map[string]any{})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil {
		t.Fatalf("expected 'not available', got %s", resp)
	}
	if envelope.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601", envelope.Error.Code)
	}
}

// A dialog service that predates the upload picker (or a platform whose
// picker cannot mint) is the same absence: -32601, not a panic and not a
// half-answer.
func TestDialogOpenFileForUpload_UnavailableWhenTheServiceCannotMint(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{path: "/home/dev/.ssh/id_ed25519"})
	resp := jsonrpcCall(t, h.conn, "dialog.openFileForUpload", map[string]any{})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil || envelope.Error.Code != -32601 {
		t.Fatalf("want -32601 from a service with no picker, got %s", resp)
	}
}

// The picker's own failure is reported, and the message must not carry the
// path either — an adapter that wrapped an *os.PathError would leak the
// directory through the error channel.
func TestDialogOpenFileForUpload_PickerFailureNamesNoPath(t *testing.T) {
	picker := &fakeUploadPicker{store: NewSourceTicketStore(nil)}
	dir := t.TempDir()
	picker.path = filepath.Join(dir, "absent.bin")
	h := newInventoryHarness(t)
	h.ws.SetDialogService(picker)

	resp := jsonrpcCall(t, h.conn, "dialog.openFileForUpload", map[string]any{})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil {
		t.Fatalf("a picker that could not mint reported success: %s", resp)
	}
	if strings.Contains(envelope.Error.Message, dir) {
		t.Errorf("error %q carries the source directory", envelope.Error.Message)
	}
}

// The drop reaches the renderer as a files.dropped notification off the
// real socket, carrying the session, the tickets, the names and the sizes —
// and no destination, because the renderer resolves that itself.
func TestFilesDropped_ReachesTheRendererOverTheSocket(t *testing.T) {
	e := newDropEnv(t)
	path := seedFile(t, "dropped.bin", 5)

	if err := e.ws.UploadSources().Dropped([]string{path},
		map[string]string{"data-session-id": e.sid}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}

	params := readNotification(t, e.conn, "files.dropped", 5*time.Second)
	var got struct {
		SessionID string         `json:"sessionId"`
		Sources   []SourcePick   `json:"sources"`
		Extra     map[string]any `json:"-"`
	}
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v\nraw: %s", err, params)
	}
	if got.SessionID != e.sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, e.sid)
	}
	if len(got.Sources) != 1 || got.Sources[0].Name != "dropped.bin" || got.Sources[0].Size != 5 {
		t.Fatalf("sources = %+v, want one dropped.bin of 5 bytes", got.Sources)
	}
	if strings.Contains(string(params), filepath.Dir(path)) {
		t.Errorf("the notification carries the source directory: %s", params)
	}
	// No destination on the native side: the notification says nothing
	// about where the bytes go.
	var loose map[string]any
	if err := json.Unmarshal(params, &loose); err != nil {
		t.Fatalf("unmarshal loose: %v", err)
	}
	for _, forbidden := range []string{"bindingId", "destDir", "path", "sourcePath"} {
		if _, ok := loose[forbidden]; ok {
			t.Errorf("the drop notification carries %q; the native side must resolve no destination", forbidden)
		}
	}
}

// With no renderer attached to THAT TAB there is nobody to hand a ticket
// to, and the drop says so rather than leaving tickets in the store for
// nobody. The session is still open — it is the subscriber that went away,
// which is the ordinary AD-9 state between a socket dropping and the next
// one attaching.
func TestFilesDropped_WithNoRendererMintsNothing(t *testing.T) {
	e := newDropEnv(t)
	if err := e.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitFor(t, "the connection to be forgotten", 5*time.Second, func() bool {
		e.ws.connsMu.Lock()
		defer e.ws.connsMu.Unlock()
		return len(e.ws.conns) == 0
	})

	st := e.ws.UploadSources()
	err := st.Dropped([]string{seedFile(t, "nobody.bin", 1)}, map[string]string{"data-session-id": e.sid})
	if err == nil {
		t.Fatalf("a drop with no renderer attached reported success")
	}
	if st.Len() != 0 {
		t.Errorf("Len = %d, want 0 — undeliverable tickets must not accumulate", st.Len())
	}
}

// ── a stand with a tab that can be dropped on ────────────────────────────

// dropEnv is a server with a REMOTE session open over the wire, which is
// the only kind of tab a drop mints for: a local tab does not copy (D9),
// so it mints nothing and there would be no ticket to look at.
//
// The SSH factory and the resolver are stubs; nothing dials. What is real
// is the path that matters here — the session is opened by the `open`
// method, so it is in the registry with the kind the registry recorded and
// this connection is its subscriber, which is what the notification is
// addressed to.
type dropEnv struct {
	ws   *WSServer
	conn *websocket.Conn
	sid  string
}

func newDropEnv(t *testing.T) *dropEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return ssh.NewStubChannel(logger), nil
		},
	})
	ws := NewWSServer(logger, reg, WithProfileResolver(&fakeResolver{
		resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
			return "host.example", &ssh.ConnectConfig{User: "test", Port: 22}, nil
		},
	}))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &dropEnv{ws: ws, conn: conn, sid: openRemoteSessionOnConn(t, ws, conn, 1)}
}

// openRemoteSessionOnConn opens a KindRemote session over a connection and
// waits for the subscriber install, exactly as openSessionOnConn does for a
// local one and for the same reason (see its comment).
func openRemoteSessionOnConn(t *testing.T, ws *WSServer, conn *websocket.Conn, id int) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:drop:1",
	}, id)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("open ssh: unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("open ssh: %+v", envelope.Error)
	}
	var got struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("open ssh: decode: %v", err)
	}
	if got.SessionID == "" {
		t.Fatal("open ssh returned an empty sessionId")
	}
	awaitSubscriber(t, ws, session.ID(got.SessionID))
	return got.SessionID
}

// ── the ticket names bytes, not a name ────────────────────────────────────

// THE POINT OF A TICKET. Between a human choosing a file and the upload
// reading it, a name can be renamed, replaced, or be a symlink whose target
// moved. A ticket that stored the PATHNAME and handed it back for somebody
// else to open would then name different bytes from the ones the person
// chose — and on a machine where the renderer is not trusted with paths at
// all, which is the whole premise of R2, a re-resolvable name gives back
// most of what the rule took away.
//
// So the ticket carries the open handle, and this is the test that can tell
// the two designs apart: the file is REPLACED at the same path after the
// mint, and what the ticket reads is still what the person chose.
func TestSourceTicketStore_ATicketNamesBytesNotAName(t *testing.T) {
	chosen := []byte("the bytes the person chose")
	dir := t.TempDir()
	path := filepath.Join(dir, "chosen.bin")
	if err := os.WriteFile(path, chosen, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st := NewSourceTicketStore(nil)
	pick, err := st.Mint(path)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	// The classic replace: unlink and write a new file at the same name.
	// A rename() over the top is the same shape and the same defect.
	if rmErr := os.Remove(path); rmErr != nil {
		t.Fatalf("remove: %v", rmErr)
	}
	if wErr := os.WriteFile(path, []byte("bytes nobody chose, from somewhere else entirely"), 0o600); wErr != nil {
		t.Fatalf("replace: %v", wErr)
	}

	src, ok := st.Claim(pick.Ticket)
	if !ok {
		t.Fatalf("Claim: the ticket named nothing")
	}
	defer src.File.Close() //nolint:errcheck // the test owns it from here
	got, err := io.ReadAll(src.File)
	if err != nil {
		t.Fatalf("read the claimed handle: %v", err)
	}
	if !bytes.Equal(got, chosen) {
		t.Fatalf("the ticket read %q; it must read the bytes the person chose, %q", got, chosen)
	}
	// And the size it declared is the size of THOSE bytes.
	if pick.Size != int64(len(chosen)) {
		t.Errorf("size = %d, want %d", pick.Size, len(chosen))
	}
}

// The same rule from the wire, which is where it matters: a file replaced
// between the human's gesture and the transfer uploads the bytes that were
// chosen, not whatever now answers to that name.
func TestFilesUpload_ARedeemedTicketUploadsTheBytesThatWereChosen(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	destDir := t.TempDir()
	bid := e.openBinding(t, sid, destDir, 2)

	chosen := []byte("chosen at the picker")
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "doc.txt")
	if err := os.WriteFile(src, chosen, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pick, err := e.ws.UploadSources().Mint(src)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if rmErr := os.Remove(src); rmErr != nil {
		t.Fatalf("remove: %v", rmErr)
	}
	if wErr := os.WriteFile(src, []byte("swapped in afterwards"), 0o600); wErr != nil {
		t.Fatalf("replace: %v", wErr)
	}

	p := uploadParams(bid, destDir, "doc.txt", pick.Size)
	p["sourceTicket"] = pick.Ticket
	got := callUpload(t, e.conn, p, 3).mustResult(t)
	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	landed, err := os.ReadFile(filepath.Join(destDir, "doc.txt")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("destination: %v", err)
	}
	if !bytes.Equal(landed, chosen) {
		t.Fatalf("uploaded %q; the ticket must name the bytes the person chose, %q", landed, chosen)
	}
}

// An open handle is a resource, so its interval has to close at every end.
// A ticket that times out has no claimant to hand the fd to, so the sweep
// that forgets it is what must let it go.
func TestSourceTicketStore_ExpiryClosesTheOpenHandle(t *testing.T) {
	st := NewSourceTicketStore(nil)
	base := time.Now()
	st.now = func() time.Time { return base }
	pick, err := st.Mint(seedFile(t, "expiring.bin", 4))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	st.mu.Lock()
	held := st.entries[pick.Ticket].file
	st.mu.Unlock()

	st.now = func() time.Time { return base.Add(2 * sourceTicketTTL) }
	// Any mint runs the sweep; this one is the ordinary path to it.
	if _, err := st.Mint(seedFile(t, "later.bin", 1)); err != nil {
		t.Fatalf("Mint after expiry: %v", err)
	}
	if err := held.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closing the evicted handle returned %v; the sweep must have closed it already", err)
	}
}

// failingEmitter is the renderer that was not there. It grabs the handles
// the mint just opened, out of the store, at the one instant they exist —
// then refuses the delivery, so the test can ask whether the take-back let
// them go.
type failingEmitter struct {
	st   *SourceTicketStore
	held []*os.File
}

func (f *failingEmitter) TabKind(string) (session.Kind, bool) { return session.KindRemote, true }

func (f *failingEmitter) EmitFilesDropped(_ string, picks []SourcePick) error {
	f.st.mu.Lock()
	for _, p := range picks {
		if e := f.st.entries[p.Ticket]; e != nil {
			f.held = append(f.held, e.file)
		}
	}
	f.st.mu.Unlock()
	return errDropNoRenderer
}

// The same at the other undelivered end: a drop whose notification does not
// reach a renderer takes its tickets back, and the fds with them.
func TestFilesDropped_AnUndeliveredDropClosesItsHandles(t *testing.T) {
	em := &failingEmitter{}
	st := NewSourceTicketStore(em)
	em.st = st
	err := st.Dropped([]string{seedFile(t, "undelivered.bin", 3)},
		map[string]string{"data-session-id": dropSessionID})
	if err == nil {
		t.Fatal("a drop whose emit failed reported success")
	}
	if st.Len() != 0 {
		t.Fatalf("Len = %d, want 0", st.Len())
	}
	if len(em.held) != 1 {
		t.Fatalf("the emitter saw %d handles, want 1", len(em.held))
	}
	for _, f := range em.held {
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("an undelivered ticket's handle is still open (Close: %v)", err)
		}
	}
}

// ── the ticket is bound to the tab it was dropped on ──────────────────────

// R1 says the wrong pairing is NOT EXPRESSIBLE. Dropped recorded only the
// source file; the session id rode on the broadcast, not on the ticket, so
// once redemption existed any recipient could pair any ticket with any
// remote binding its connection owned, and a file dropped on tab A could be
// sent to host B. Binding authorisation proves the caller owns B; it proves
// nothing about where the file was dropped.
func TestSourceTicketStore_ADroppedTicketCarriesItsTab(t *testing.T) {
	em := remoteEmitter()
	st := NewSourceTicketStore(em)
	if err := st.Dropped([]string{seedFile(t, "bound.bin", 2)},
		map[string]string{"data-session-id": dropSessionID}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	src, ok := st.Claim(em.picks[0][0].Ticket)
	if !ok {
		t.Fatal("the dropped ticket claimed nothing")
	}
	defer src.File.Close() //nolint:errcheck // the test owns it from here
	if src.Session != session.ID(dropSessionID) {
		t.Fatalf("the ticket names session %q, want %q — a ticket that names no tab can be paired with any tab", src.Session, dropSessionID)
	}
}

// D9 on the native side. A local tab does not copy — every terminal inserts
// the dropped name at the prompt instead — so there is nothing to upload
// and no credential to mint. Today one was minted and then abandoned live
// by the renderer, and a credential nobody redeems is still a credential
// that exists.
//
// The renderer is still TOLD, because that is what makes the prompt insert
// work in the Wails window, where the drop never becomes a DOM event: it
// learns the names and the sizes and no ticket at all.
func TestFilesDropped_ALocalTabMintsNoTicket(t *testing.T) {
	em := &recordingEmitter{kind: session.KindLocal}
	st := NewSourceTicketStore(em)
	if err := st.Dropped([]string{seedFile(t, "typed.txt", 3)},
		map[string]string{"data-session-id": dropSessionID}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	if st.Len() != 0 {
		t.Fatalf("%d tickets minted for a local tab, want 0", st.Len())
	}
	if len(em.picks) != 1 || len(em.picks[0]) != 1 {
		t.Fatalf("emitted %v, want the one dropped file", em.picks)
	}
	got := em.picks[0][0]
	if got.Ticket != "" {
		t.Errorf("a local tab's drop carries ticket %q, want none", got.Ticket)
	}
	if got.Name != "typed.txt" || got.Size != 3 {
		t.Errorf("got %+v, want typed.txt of 3 bytes — the prompt insert needs the name", got)
	}
}

// D9's other half, and the defect that reopened it: a local tab's drop
// inserts the PATH at the prompt, and the renderer was being handed a bare
// base name that looks like one and is not. `report.pdf` at a prompt runs
// against whatever `report.pdf` means in the shell's cwd, which is a
// different file or no file at all.
//
// So the local branch carries the absolute path in a field of its own. This
// does NOT weaken R2. R2's threat is the renderer NAMING a source inbound —
// files.upload has no such parameter and its decoder refuses unknown fields,
// so the request cannot express one, and that shape is untouched. Telling
// the renderer a path outbound, for the same human's own command line, runs
// the other way: they just chose the file, and there is no wire field that
// takes it back.
func TestFilesDropped_ALocalTabCarriesTheAbsolutePath(t *testing.T) {
	em := &recordingEmitter{kind: session.KindLocal}
	st := NewSourceTicketStore(em)
	path := seedFile(t, "typed.txt", 3)
	if err := st.Dropped([]string{path},
		map[string]string{"data-session-id": dropSessionID}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	if len(em.picks) != 1 || len(em.picks[0]) != 1 {
		t.Fatalf("emitted %v, want the one dropped file", em.picks)
	}
	got := em.picks[0][0]
	if got.LocalPath != path {
		t.Errorf("the prompt insert is offered %q, want the absolute path %q", got.LocalPath, path)
	}
	// And the name is still the base name, because the two are different
	// questions and the panel row asks the other one.
	if got.Name != "typed.txt" {
		t.Errorf("name = %q, want the base name", got.Name)
	}
}

// The regression that would matter. A REMOTE tab is where a credential is
// minted and where an upload actually happens, and it learns nothing about
// the backend's filesystem beyond a base name and a size — the whole of R2's
// outbound half. The local branch's new field must not follow the ticket out
// onto the tab that has one.
func TestFilesDropped_ARemoteTabCarriesNoPath(t *testing.T) {
	em := remoteEmitter()
	st := NewSourceTicketStore(em)
	path := seedFile(t, "secret.pem", 5)
	if err := st.Dropped([]string{path},
		map[string]string{"data-session-id": dropSessionID}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	if len(em.picks) != 1 || len(em.picks[0]) != 1 {
		t.Fatalf("emitted %v, want the one dropped file", em.picks)
	}
	if got := em.picks[0][0]; got.LocalPath != "" {
		t.Errorf("a remote tab's drop carries path %q; it may learn a name and a size and nothing else", got.LocalPath)
	}
	// Marshalled, the key is absent rather than empty: what a remote pick
	// cannot spell, a reader of the wire cannot mistake for a path.
	raw, err := json.Marshal(em.picks[0][0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte("localPath")) {
		t.Errorf("a remote pick marshals to %s; the key itself has no business being there", raw)
	}
}

// The picker mints, so it is the same branch as a remote drop and answers
// the same way: a ticket, a name, a size, and no path. dialog.openFileForUpload's
// contract says additionalProperties:false, so a path here would fail the
// conformance test too — this one says it in the mint itself.
func TestSourceTicket_AMintedPickCarriesNoPath(t *testing.T) {
	st := NewSourceTicketStore(nil)
	pick, err := st.Mint(seedFile(t, "picked.bin", 4))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if pick.LocalPath != "" {
		t.Errorf("a minted pick carries path %q, want none", pick.LocalPath)
	}
}

// The other direction, which is the one R2 is about. The field the local
// branch now sends OUTBOUND is not a field files.upload will accept INBOUND:
// the params decoder runs DisallowUnknownFields, so a request naming a path
// under the new field's own name is refused before any of it is read.
//
// The params are otherwise well formed — a bindingId this connection owns
// and a real destination — so the refusal is the unknown field and nothing
// else about the call.
func TestFilesUpload_RefusesAPathUnderTheOutboundFieldsName(t *testing.T) {
	e := newFilesTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bindingID := e.openBinding(t, sid, dir, 2)

	resp := jsonrpcCall(t, e.conn, "files.upload", map[string]any{
		"bindingId": bindingID,
		"destDir":   dir,
		"name":      "innocent.txt",
		"size":      1,
		"localPath": "/home/dev/.ssh/id_ed25519",
	})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error == nil {
		t.Fatalf("files.upload accepted a source path under the outbound field's name: %s", resp)
	}
}

// A drop target naming a session that is not open is refused whole. The
// attribute is renderer-authored DOM: a well-shaped id for a tab that does
// not exist must not mint a ticket bound to nothing.
func TestFilesDropped_RefusesATabThatIsNotOpen(t *testing.T) {
	em := &recordingEmitter{unknown: true}
	st := NewSourceTicketStore(em)
	err := st.Dropped([]string{seedFile(t, "ghost.bin", 1)},
		map[string]string{"data-session-id": dropSessionID})
	if err == nil {
		t.Fatal("a drop on a session that is not open was accepted")
	}
	if st.Len() != 0 || len(em.picks) != 0 {
		t.Errorf("minted %d tickets and emitted %d notifications for a tab that does not exist", st.Len(), len(em.picks))
	}
}

// The notification is addressed to the tab it was dropped on, not
// broadcast — the addressing files.changed already uses (ws_files.go).
//
// The assertion is an ORDER rather than an absence, because "conn B never
// received it" can only be measured by waiting, and a test that waits for a
// duration is broken on a fast machine too. Tab A is dropped on first: if
// the notification were broadcast, the FIRST files.dropped conn B ever sees
// would be A's. It sees B's, so it never saw A's.
func TestFilesDropped_IsAddressedToTheTabItWasDroppedOn(t *testing.T) {
	e := newDropEnv(t)
	connB := connectWS(t, e.ws)
	t.Cleanup(func() { _ = connB.Close() })
	sidB := openRemoteSessionOnConn(t, e.ws, connB, 1)

	st := e.ws.UploadSources()
	if err := st.Dropped([]string{seedFile(t, "for-a.bin", 1)},
		map[string]string{"data-session-id": e.sid}); err != nil {
		t.Fatalf("Dropped on A: %v", err)
	}
	if err := st.Dropped([]string{seedFile(t, "for-b.bin", 2)},
		map[string]string{"data-session-id": sidB}); err != nil {
		t.Fatalf("Dropped on B: %v", err)
	}

	params := readNotification(t, connB, "files.dropped", 5*time.Second)
	var got struct {
		SessionID string       `json:"sessionId"`
		Sources   []SourcePick `json:"sources"`
	}
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, params)
	}
	if got.SessionID != sidB {
		t.Fatalf("the first drop tab B heard about was session %q; a drop is addressed to the tab it landed on, never broadcast", got.SessionID)
	}
	if len(got.Sources) != 1 || got.Sources[0].Name != "for-b.bin" {
		t.Fatalf("tab B was told about %+v, want for-b.bin", got.Sources)
	}
	// And tab A did get its own, so the addressing delivers rather than
	// merely withholding.
	paramsA := readNotification(t, e.conn, "files.dropped", 5*time.Second)
	var gotA struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(paramsA, &gotA); err != nil {
		t.Fatalf("unmarshal A: %v", err)
	}
	if gotA.SessionID != e.sid {
		t.Fatalf("tab A heard about session %q, want its own %q", gotA.SessionID, e.sid)
	}
}
