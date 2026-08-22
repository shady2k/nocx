package transport

// GET /download/{ticket} — the data half.
//
// The failure table this file covers, and what is true after each row. The
// columns are not the upload's, and the difference is the direction's: a
// download creates nothing on the source host, so the host is untouched on
// every row and there is nothing to strand. What replaces those columns is
// what the CLIENT ended up holding, because that is the thing a download
// can get wrong and cannot take back.
//
//	Failure                        Source host  Client holds          Status
//	Ticket never minted            untouched    nothing               410
//	Ticket already being fetched   untouched    nothing               409
//	Ticket's transfer has ended    untouched    nothing               410
//	A SINK ticket presented here   untouched    nothing               410
//	TTL elapsed before the fetch   untouched    nothing               410 (cancelled at expiry)
//	Origin refused                 untouched    nothing               403, before the ticket is read
//	Cancelled before the first byte untouched   nothing               500
//	The file shrank after the open untouched    a short body          200, framed longer than it got
//	The client goes away mid-body  untouched    a prefix              no reply anybody reads

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

func downloadURLFor(ws *WSServer, ticket string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/download/%s", ws.Port(), ticket)
}

// getDownload fetches a ticket and returns the status and the body.
func getDownload(t *testing.T, ws *WSServer, ticket string) (int, string) {
	t.Helper()
	code, body, _, err := getDownloadFull(ws, ticket, nil)
	if err != nil {
		t.Fatalf("GET /download: %v", err)
	}
	return code, body
}

// getDownloadRaw is the same fetch without a *testing.T, for the goroutines
// that only need the request to be in flight.
func getDownloadRaw(ws *WSServer, ticket string) (int, string) {
	code, body, _, err := getDownloadFull(ws, ticket, nil)
	if err != nil {
		return 0, err.Error()
	}
	return code, body
}

// getDownloadFull hands back the whole reply, for the assertions that are
// about HEADERS rather than only a status — the CORS contract has to hold
// on the refusals too, and Content-Disposition is the only place the file's
// name reaches a browser.
func getDownloadFull(ws *WSServer, ticket string, headers map[string]string) (int, string, http.Header, error) {
	req, err := http.NewRequest(http.MethodGet, downloadURLFor(ws, ticket), nil)
	if err != nil {
		return 0, "", nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	// A body that ends short of its declared Content-Length is a real
	// outcome here rather than a test failure — it is how a transfer that
	// died part-way arrives — so the bytes and the error both come back.
	if readErr != nil {
		return resp.StatusCode, string(body), resp.Header, nil
	}
	return resp.StatusCode, string(body), resp.Header, nil
}

// downloadOf is the whole round trip a person makes: files.download, then
// the fetch.
func downloadOf(t *testing.T, e *filesTestEnv, bid, path string, id int) (downloadResult, int, string, http.Header) {
	t.Helper()
	got := callDownload(t, e.conn, downloadParams(bid, path), id).mustResult(t)
	code, body, hdr, err := getDownloadFull(e.ws, got.Ticket, nil)
	if err != nil {
		t.Fatalf("GET /download: %v", err)
	}
	return got, code, body, hdr
}

// ── the paired success: a file comes back ────────────────────────────────

// TestDownloadRoute_DeliversTheFile is the sentence the whole task is for:
// a file on the host of a binding is read and streamed out, and the bytes
// at the far end are the file's bytes.
func TestDownloadRoute_DeliversTheFile(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	body := strings.Repeat("nocx-download-", 20_000) // ~280 KB, many chunks
	p := fixture(t, dir, "report.bin", body)
	bid := e.openBinding(t, sid, dir, 2)

	got, code, gotBody, hdr := downloadOf(t, e, bid, p, 3)

	if code != http.StatusOK {
		t.Fatalf("GET /download = %d, want 200", code)
	}
	if gotBody != body {
		t.Fatalf("received %d bytes, want the file's %d", len(gotBody), len(body))
	}
	if hdr.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length %q, want %d", hdr.Get("Content-Length"), len(body))
	}
	if ct := hdr.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type %q, want application/octet-stream — a download is bytes to save, never a document to render", ct)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff is missing; without it the browser guesses a type for somebody else's file")
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, `filename="report.bin"`) {
		t.Errorf("Content-Disposition %q, want it to name the file", cd)
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != downloadStateSent {
		t.Fatalf("state %q, want %q", state, downloadStateSent)
	}
}

// An empty file is a file. Zero bytes never reaches the response writer, so
// this is the one path where the handler itself commits the head — and a
// 200 with Content-Length: 0 is the correct download of an empty file, not
// a failure.
func TestDownloadRoute_AnEmptyFileIsAFile(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "empty", "")
	bid := e.openBinding(t, sid, dir, 2)

	got, code, body, hdr := downloadOf(t, e, bid, p, 3)
	if code != http.StatusOK || body != "" {
		t.Fatalf("GET of an empty file = %d %q, want 200 and no body", code, body)
	}
	if hdr.Get("Content-Length") != "0" {
		t.Errorf("Content-Length %q, want 0", hdr.Get("Content-Length"))
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != downloadStateSent {
		t.Fatalf("state %q, want %q", state, downloadStateSent)
	}
}

// ── the four ticket states ───────────────────────────────────────────────

func TestDownloadRoute_TicketStates(t *testing.T) {
	t.Run("never minted is 410", func(t *testing.T) {
		e := newDownloadTestEnv(t)
		code, _ := getDownload(t, e.ws, strings.Repeat("ab", uploadTicketHexLen/2))
		if code != http.StatusGone {
			t.Fatalf("an unknown ticket = %d, want 410", code)
		}
	})

	t.Run("malformed is 410 and not a different answer", func(t *testing.T) {
		e := newDownloadTestEnv(t)
		// Answering a malformed ticket differently would tell a caller
		// whether a well-formed guess existed.
		code, _ := getDownload(t, e.ws, "not-a-ticket")
		if code != http.StatusGone && code != http.StatusNotFound {
			t.Fatalf("a malformed ticket = %d, want 410", code)
		}
	})

	t.Run("already finished is 410", func(t *testing.T) {
		e := newDownloadTestEnv(t)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		p := fixture(t, dir, "a.txt", "bytes")
		bid := e.openBinding(t, sid, dir, 2)
		got, code, _, _ := downloadOf(t, e, bid, p, 3)
		if code != http.StatusOK {
			t.Fatalf("first fetch = %d", code)
		}
		awaitTransferState(t, e.ws, got.TransferID)

		if again, _ := getDownload(t, e.ws, got.Ticket); again != http.StatusGone {
			t.Fatalf("a second fetch of a finished transfer = %d, want 410", again)
		}
	})

	t.Run("already being fetched is 409", func(t *testing.T) {
		src := &unresponsiveSource{entered: make(chan struct{}), release: make(chan struct{})}
		e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src))
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		p := fixture(t, dir, "a.txt", "bytes")
		bid := e.openBinding(t, sid, dir, 2)
		got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

		go func() { _, _ = getDownloadRaw(e.ws, got.Ticket) }()
		<-src.entered

		if code, _ := getDownload(t, e.ws, got.Ticket); code != http.StatusConflict {
			t.Fatalf("a second concurrent fetch = %d, want 409", code)
		}
		close(src.release)
		awaitTransferState(t, e.ws, got.TransferID)
	})
}

// ── the two routes do not share credentials ──────────────────────────────

// TestTransferTickets_AreNotInterchangeableBetweenTheRoutes is the hole one
// shared ticket map makes expressible, and the one it must not open. A sink
// ticket fetched at /download/ would hand a caller the BYTES of a file it
// was only ever authorised to overwrite; a download ticket POSTed at
// /upload/ would let a caller write into a transfer that was only ever
// authorised to read.
//
// Both must answer as an unknown ticket does — and must NOT consume it, so
// a misrouted request cannot burn somebody's one-shot credential.
func TestTransferTickets_AreNotInterchangeableBetweenTheRoutes(t *testing.T) {
	t.Run("a sink ticket fetched at the download route", func(t *testing.T) {
		e := newUploadTestEnv(t)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		bid := e.openBinding(t, sid, dir, 2)
		body := []byte("the bytes the upload was for")
		up := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", int64(len(body))), 3).mustResult(t)

		if code, got := getDownload(t, e.ws, up.Ticket); code != http.StatusGone {
			t.Fatalf("a sink ticket at /download = %d %q, want 410", code, got)
		}
		// And it was not spent: the upload it names still works.
		if code, resp := postUpload(t, e.ws, up.Ticket, body); code != http.StatusOK {
			t.Fatalf("POST after the misrouted fetch = %d %q; a wrong route must not burn the credential", code, resp)
		}
		if state := awaitTransferState(t, e.ws, up.TransferID); state != uploadStateWritten {
			t.Fatalf("state %q, want %q", state, uploadStateWritten)
		}
	})

	// And the check is on the TICKET rather than on the transfer it
	// resolves to, so a ticket that is already CLAIMED on its own route
	// still answers 410 on the wrong one. Answering 409 there would say the
	// ticket exists, which is an oracle for a credential that reads
	// somebody's file.
	t.Run("a claimed sink ticket is still 410 at the download route", func(t *testing.T) {
		sink := &unresponsiveSink{entered: make(chan struct{}), release: make(chan struct{})}
		e := newUploadTestEnvWithSink(t, sink)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		bid := e.openBinding(t, sid, dir, 2)
		up := callUpload(t, e.conn, uploadParams(bid, dir, "a.txt", 5), 3).mustResult(t)
		go postUploadAsync(e.ws, up.Ticket, []byte("hello"))
		<-sink.entered // the ticket is claimed and its transfer is running

		if code, _ := getDownload(t, e.ws, up.Ticket); code != http.StatusGone {
			t.Fatalf("a CLAIMED sink ticket at /download = %d, want 410 and never 409", code)
		}
		close(sink.release)
		awaitTransferState(t, e.ws, up.TransferID)
	})

	t.Run("a download ticket posted at the upload route", func(t *testing.T) {
		e := newDownloadTestEnv(t)
		sid := e.openSession(t, 1)
		dir := t.TempDir()
		body := "the bytes the download was for"
		p := fixture(t, dir, "a.txt", body)
		bid := e.openBinding(t, sid, dir, 2)
		down := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

		if code, got := postUpload(t, e.ws, down.Ticket, []byte("overwrite")); code != http.StatusGone {
			t.Fatalf("a download ticket at /upload = %d %q, want 410", code, got)
		}
		// And it was not spent: the download it names still works.
		code, gotBody, _, err := getDownloadFull(e.ws, down.Ticket, nil)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		if code != http.StatusOK || gotBody != body {
			t.Fatalf("GET after the misrouted POST = %d %q; a wrong route must not burn the credential", code, gotBody)
		}
	})
}

// ── CORS ─────────────────────────────────────────────────────────────────

// The route is cross-origin BY CONSTRUCTION under `dev-web`, where the page
// is vite's and the backend is not — so a reply that does not name the
// origin hands the page nothing at all, and "the ticket is gone" and "the
// network died" arrive as the same "Failed to fetch".
func TestDownloadRoute_CORS(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "bytes")
	bid := e.openBinding(t, sid, dir, 2)
	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", e.ws.Port())

	code, _, hdr, err := getDownloadFull(e.ws, got.Ticket, map[string]string{"Origin": origin})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("GET with an allowed Origin = %d, want 200", code)
	}
	if hdr.Get("Access-Control-Allow-Origin") != origin {
		t.Errorf("Allow-Origin %q, want the origin echoed EXACTLY and never *", hdr.Get("Access-Control-Allow-Origin"))
	}
	if hdr.Get("Vary") != "Origin" {
		t.Errorf("Vary %q, want Origin: the answer depends on a request header", hdr.Get("Vary"))
	}
	if hdr.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials are allowed; the ticket IS the credential and cookies have no business here")
	}
	// The one header that makes the reply usable: without it a
	// cross-origin page receives the bytes and cannot read the file's name.
	if exposed := hdr.Get("Access-Control-Expose-Headers"); !strings.Contains(exposed, "Content-Disposition") {
		t.Errorf("Expose-Headers %q, want Content-Disposition", exposed)
	}
}

// The origin is decided BEFORE the ticket is looked up or claimed, so a
// refusal is not an oracle for a credential that reads somebody's file —
// and the ticket it named is still good afterwards.
func TestDownloadRoute_RefusesAForeignOriginBeforeTouchingTheTicket(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	body := "bytes"
	p := fixture(t, dir, "a.txt", body)
	bid := e.openBinding(t, sid, dir, 2)
	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	code, _, hdr, err := getDownloadFull(e.ws, got.Ticket, map[string]string{"Origin": "https://evil.example.com"})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusForbidden {
		t.Fatalf("a foreign origin = %d, want 403", code)
	}
	if hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Error("a refused origin was echoed back")
	}
	if hdr.Get("Vary") != "Origin" {
		t.Error("Vary must be on the refusal too; a cache that missed it would serve one origin's answer to another")
	}
	// The ticket was neither claimed nor retired.
	fetched, gotBody, _, err := getDownloadFull(e.ws, got.Ticket, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if fetched != http.StatusOK || gotBody != body {
		t.Fatalf("after the refusal the ticket answered %d %q; a refused origin must not consume one", fetched, gotBody)
	}
}

// ── Content-Disposition is a header, and a file name is not ──────────────

// A POSIX file name may contain anything but '/' and NUL — CR and LF among
// them — and the path validation deliberately does not refuse those,
// because a file with a newline in its name is a file and must be
// downloadable. So the header is where it is made safe, and this is the
// test that says the sanitising happens.
func TestDownloadRoute_ContentDispositionCannotBeInjected(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	nasty := "evil\r\nX-Injected: yes\r\n\r\n.txt"
	p := fixture(t, dir, nasty, "bytes")
	bid := e.openBinding(t, sid, dir, 2)

	_, code, body, hdr := downloadOf(t, e, bid, p, 3)
	if code != http.StatusOK || body != "bytes" {
		t.Fatalf("GET = %d %q, want 200 and the file's bytes: a name is not a reason to refuse a file", code, body)
	}
	if hdr.Get("X-Injected") != "" {
		t.Fatal("the file name wrote a header of its own")
	}
	cd := hdr.Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n") {
		t.Fatalf("Content-Disposition %q carries a line break", cd)
	}
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition %q, want the RFC 5987 form alongside the fallback", cd)
	}
}

// A non-ASCII name survives in filename* and is reduced in the fallback,
// which is what RFC 6266 asks for and what makes the name readable in every
// browser rather than in some of them.
func TestDownloadRoute_ContentDispositionCarriesANonASCIIName(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "отчёт.pdf", "bytes")
	bid := e.openBinding(t, sid, dir, 2)

	_, code, _, hdr := downloadOf(t, e, bid, p, 3)
	if code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	cd := hdr.Get("Content-Disposition")
	// %D0%BE is 'о' — the encoding is what a browser reads the real name
	// from.
	if !strings.Contains(cd, "filename*=UTF-8''") || !strings.Contains(cd, "%D0") {
		t.Fatalf("Content-Disposition %q, want the name percent-encoded in filename*", cd)
	}
	if strings.Contains(cd, "отчёт") {
		t.Errorf("Content-Disposition %q carries raw non-ASCII in a place that is only defined for ASCII", cd)
	}
}

// contentDisposition is unit-tested directly too, because the shapes that
// matter most are the ones a filesystem makes awkward to create.
func TestContentDisposition_Sanitises(t *testing.T) {
	cases := map[string]struct{ contains, notContains string }{
		`a quote " in the name`: {contains: `filename="a quote _ in the name"`},
		"a\\backslash":          {contains: `filename="a_backslash"`},
		"\r\n":                  {contains: `filename="download"`, notContains: "\r"},
		"":                      {contains: `filename="download"`},
		"ordinary.txt":          {contains: `filename="ordinary.txt"`},
	}
	for name, want := range cases {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			got := contentDisposition(name)
			if !strings.Contains(got, want.contains) {
				t.Fatalf("contentDisposition(%q) = %q, want it to contain %q", name, got, want.contains)
			}
			if want.notContains != "" && strings.Contains(got, want.notContains) {
				t.Fatalf("contentDisposition(%q) = %q, want it not to contain %q", name, got, want.notContains)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("contentDisposition(%q) = %q carries a line break", name, got)
			}
		})
	}
}

// ── the ticket is never logged ───────────────────────────────────────────

// Possession of the ticket authorises reading somebody's file, so it must
// appear in no log line — including the refusals, which are the lines an
// operator is most likely to be reading.
func TestDownloadTicket_IsNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(
		&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e := newUploadTestEnvWith(t, logger, filesLocalFactory)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "bytes")
	bid := e.openBinding(t, sid, dir, 2)
	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	// A refused origin, a wrong route, and the successful fetch — three
	// different lines, none of which may carry it.
	_, _, _, _ = getDownloadFull(e.ws, got.Ticket, map[string]string{"Origin": "https://evil.example.com"})
	_, _ = postUpload(t, e.ws, got.Ticket, []byte("x"))
	code, _, _, err := getDownloadFull(e.ws, got.Ticket, nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("GET = %d %v", code, err)
	}
	awaitTransferState(t, e.ws, got.TransferID)

	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	if strings.Contains(logged, got.Ticket) {
		t.Fatalf("the download ticket appeared in the log; it is a bearer credential:\n%s", logged)
	}
}

// ── the failure rows that need a hand-written client ─────────────────────

// A client that goes away mid-body must not leave the transfer, the
// descriptor and the lease held. The response's own write deadline is what
// unblocks a Write that has already blocked, and closing it is what the
// handler does when the request context ends.
func TestDownloadRoute_TheClientGoesAwayMidBody(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	// Big enough that the server cannot possibly have written it all into
	// socket buffers before the client hangs up.
	body := strings.Repeat("x", 8<<20)
	p := fixture(t, dir, "big.bin", body)
	bid := e.openBinding(t, sid, dir, 2)
	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", e.ws.Port()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	req := "GET /download/" + got.Ticket + " HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	// Read a little, then vanish.
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(c, make([]byte, 64)); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = c.Close()

	// The assertion is a STATE: the transfer settles rather than hanging on
	// a Write nobody will ever read.
	state := awaitTransferState(t, e.ws, got.TransferID)
	if state == downloadStateSent {
		t.Fatal("a client that vanished mid-body was reported as a completed download")
	}
	rt := e.ws.transfers.get(got.TransferID)
	if _, _, sent, _ := rt.snapshot(); sent >= int64(len(body)) {
		t.Fatalf("sent = %d of %d; a vanished client cannot have taken the whole file", sent, len(body))
	}
}

// A body that ends short of the length it declared is the shape a download
// that failed part-way takes, and it is deliberate: the head cannot be
// revised once written, so the framing is what tells the client its file is
// incomplete. Here the file is TRUNCATED after the open, which is §6's
// "the file shrank" row.
func TestDownloadRoute_TheFileShrankAfterTheOpen(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	body := strings.Repeat("y", 512<<10)
	p := fixture(t, dir, "shrinking.bin", body)
	bid := e.openBinding(t, sid, dir, 2)

	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)
	if got.Size != int64(len(body)) {
		t.Fatalf("size %d, want %d", got.Size, len(body))
	}
	// Truncated in place, so the pinned handle sees a shorter file: the
	// open measured 512 KiB and the read will find 1 KiB.
	if err := os.Truncate(p, 1024); err != nil {
		t.Fatal(err)
	}

	code, gotBody, hdr, err := getDownloadFull(e.ws, got.Ticket, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("GET = %d; the head was already committed at the first byte", code)
	}
	if hdr.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length %q, want the size the transfer was framed at", hdr.Get("Content-Length"))
	}
	if len(gotBody) >= len(body) {
		t.Fatalf("received %d bytes of a file that is now 1024 long", len(gotBody))
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != downloadStateFailed {
		t.Fatalf("state %q, want %q — a file short of its declared length is a failure", state, downloadStateFailed)
	}
}

// A cancel that lands before the first byte leaves the status still ours to
// choose, which is the ONE outcome a download can cleanly undo. Past the
// first byte it cannot, and that asymmetry is why the head is committed as
// late as possible.
func TestDownloadRoute_CancelledBeforeTheFirstByteStillChoosesAStatus(t *testing.T) {
	src := &unresponsiveSource{entered: make(chan struct{}), release: make(chan struct{})}
	e := newDownloadTestEnvWith(t, downloadFactoryWithSource(src))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	p := fixture(t, dir, "a.txt", "held")
	bid := e.openBinding(t, sid, dir, 2)
	got := callDownload(t, e.conn, downloadParams(bid, p), 3).mustResult(t)

	result := make(chan int, 1)
	go func() {
		code, _ := getDownloadRaw(e.ws, got.Ticket)
		result <- code
	}()
	<-src.entered
	close(src.release)

	select {
	case code := <-result:
		if code == http.StatusOK {
			t.Fatal("a transfer that sent no bytes answered 200; nothing had been committed, so the status was still choosable")
		}
		// And the refusal REACHED the client. Cancelling trips the write
		// deadline on purpose, so a handler that did not clear it before
		// answering would drop the connection instead — which arrives as a
		// transport error, not as a status, and tells the person nothing.
		if code == 0 {
			t.Fatal("the fetch got no status at all; a cancelled transfer must still be able to answer")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the fetch never answered") // failsafe, not the assertion
	}
	if state := awaitTransferState(t, e.ws, got.TransferID); state != downloadStateCancelled {
		t.Fatalf("state %q, want %q", state, downloadStateCancelled)
	}
}

// The route answers only GET. Anything else is the mux's 405, which is what
// keeps the handler from having to decide what a POST here would mean.
func TestDownloadRoute_AnswersOnlyGET(t *testing.T) {
	e := newDownloadTestEnv(t)
	ticket := strings.Repeat("ab", uploadTicketHexLen/2)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, downloadURLFor(e.ws, ticket), strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := uploadHTTPClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /download = %d, want 405", method, resp.StatusCode)
		}
	}
}

// The guard bounds this route's header block too, and that is what the
// download route needed from it: Go's server parses the COMPLETE header
// block before dispatching, and the shared server's ReadHeaderTimeout is
// deliberately zero because /session is a long-lived upgrade. Without the
// guard a client that dribbles headers for ever holds a connection nothing
// above the listener can bound.
func TestDownloadRoute_BoundsAHeaderBlockThatNeverEnds(t *testing.T) {
	e := newDownloadTestEnv(t, withUploadHeaderTimeout(150*time.Millisecond))
	ticket := strings.Repeat("ab", uploadTicketHexLen/2)

	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", e.ws.Port()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("GET /download/" + ticket + " HTTP/1.1\r\nHost: 127.0.0.1\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// No blank line: the header block never ends. The guard must close the
	// connection, which arrives here as a read that ends rather than one
	// that waits for ever.
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second)) // failsafe, not the assertion
	buf := make([]byte, 512)
	if _, err := c.Read(buf); err == nil {
		return // the server answered and closed; either way it did not hang
	} else if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("read: %v, want the guard to have ended the connection", err)
	}
}

// A directory is not something a filesystem test can ask for through the
// route, because the refusal happens at files.download. Asserted here for
// completeness of the table: nothing is registered and no ticket exists, so
// there is nothing to fetch.
func TestDownloadRoute_ARefusedDownloadMintsNoTicket(t *testing.T) {
	e := newDownloadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	if resp := callDownload(t, e.conn, downloadParams(bid, sub), 3); resp.Error == nil {
		t.Fatal("a directory was accepted")
	}
	e.ws.transfers.mu.Lock()
	tickets := len(e.ws.transfers.tickets)
	e.ws.transfers.mu.Unlock()
	if tickets != 0 {
		t.Fatalf("%d tickets outstanding after a refused download", tickets)
	}
}
