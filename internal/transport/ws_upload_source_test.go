package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
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
	if src.Path != path {
		t.Errorf("claimed path = %q, want %q", src.Path, path)
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

type recordingEmitter struct {
	sessions []string
	picks    [][]SourcePick
}

func (r *recordingEmitter) EmitFilesDropped(sessionID string, picks []SourcePick) error {
	r.sessions = append(r.sessions, sessionID)
	r.picks = append(r.picks, picks)
	return nil
}

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
	em := &recordingEmitter{}
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
	// The tickets are real: each claims back to the file it was minted for.
	for _, p := range got {
		src, ok := st.Claim(p.Ticket)
		if !ok {
			t.Fatalf("ticket %q claimed nothing", p.Ticket)
		}
		if filepath.Dir(src.Path) != dir {
			t.Errorf("claimed %q, want a file in %q", src.Path, dir)
		}
	}
}

// A drop target that names no session is refused whole: nothing is minted,
// so a drop the renderer could not route never leaves a ticket behind.
func TestFilesDropped_RefusesATargetThatNamesNoSession(t *testing.T) {
	em := &recordingEmitter{}
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
	em := &recordingEmitter{}
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
	em := &recordingEmitter{}
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
	// each of the three names such a parameter would plausibly have. If a
	// source path were reachable from the wire at all, this is the shape
	// that would reach it.
	id := 0
	for _, name := range names {
		id++
		jsonrpcCallWithID(t, h.conn, name, map[string]any{
			"sourcePath": "/home/dev/.ssh/id_ed25519",
			"source":     "/home/dev/.ssh/id_ed25519",
			"path":       "/home/dev/.ssh/id_ed25519",
		}, id)
	}
	if picker.opens != 0 {
		t.Errorf("a call naming a path opened the picker %d times", picker.opens)
	}
	if n := picker.store.Len(); n != 0 {
		t.Fatalf("%d tickets exist after asking every one of %d methods for a path, want 0", n, len(names))
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
	// And the one ticket that does exist names the PICKER's file, not the
	// path the first sweep asked every method for.
	for tk := range picker.store.entries {
		src, ok := picker.store.Claim(tk)
		if !ok {
			t.Fatalf("the minted ticket claimed nothing")
		}
		if filepath.Base(src.Path) != "chosen.bin" {
			t.Errorf("the ticket names %q; the wire chose the file", src.Path)
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
	h := newInventoryHarness(t)
	st := NewSourceTicketStore(h.ws)
	path := seedFile(t, "dropped.bin", 5)

	if err := st.Dropped([]string{path}, map[string]string{"data-session-id": dropSessionID}); err != nil {
		t.Fatalf("Dropped: %v", err)
	}

	params := readNotification(t, h.conn, "files.dropped", 5*time.Second)
	var got struct {
		SessionID string         `json:"sessionId"`
		Sources   []SourcePick   `json:"sources"`
		Extra     map[string]any `json:"-"`
	}
	if err := json.Unmarshal(params, &got); err != nil {
		t.Fatalf("unmarshal params: %v\nraw: %s", err, params)
	}
	if got.SessionID != dropSessionID {
		t.Errorf("sessionId = %q, want %q", got.SessionID, dropSessionID)
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

// With no renderer attached there is nobody to hand a ticket to, and the
// drop says so rather than leaving tickets in the store for nobody.
func TestFilesDropped_WithNoRendererMintsNothing(t *testing.T) {
	h := newInventoryHarness(t)
	if err := h.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitFor(t, "the connection to be forgotten", 5*time.Second, func() bool {
		h.ws.connsMu.Lock()
		defer h.ws.connsMu.Unlock()
		return len(h.ws.conns) == 0
	})

	st := NewSourceTicketStore(h.ws)
	err := st.Dropped([]string{seedFile(t, "nobody.bin", 1)}, map[string]string{"data-session-id": dropSessionID})
	if err == nil {
		t.Fatalf("a drop with no renderer attached reported success")
	}
	if st.Len() != 0 {
		t.Errorf("Len = %d, want 0 — undeliverable tickets must not accumulate", st.Len())
	}
}
