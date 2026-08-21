package transport

// POST /upload/{ticket} — the data half of the upload surface (spec §5.4).
//
// The four ticket states are the spine of this file and a reviewer checks
// them row by row. Note what the expiry test pins: 410 never means "cancel
// what this names". The mint-side timer already cancelled the transfer at
// expiry, so by the time a late POST arrives the ticket is simply unknown —
// which is what removed the first draft's contradiction, where 410 meant
// both "names nothing" and "cancel what it names".

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// uploadHTTPClient is deliberately given a generous timeout: it is a
// failsafe so a broken handler fails the test instead of hanging the
// package, never the thing under assertion.
var uploadHTTPClient = &http.Client{Timeout: 60 * time.Second}

func uploadURLFor(ws *WSServer, ticket string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/upload/%s", ws.Port(), ticket)
}

// postUpload sends a body with a correct Content-Length and returns the
// status and the response body.
func postUpload(t *testing.T, ws *WSServer, ticket string, body []byte) (int, string) {
	t.Helper()
	resp, err := uploadHTTPClient.Post(uploadURLFor(ws, ticket), "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /upload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, string(got)
}

// rawUpload speaks HTTP/1.1 by hand, which is the only way to reach the
// shapes a well-behaved client cannot produce: an absent Content-Length, a
// Content-Length that disagrees with the bytes sent, and headers followed
// by silence. writeBody is called with the connection after the headers, so
// a test decides exactly how much of the body arrives and when.
func rawUpload(t *testing.T, ws *WSServer, ticket, headers string, writeBody func(c *net.TCPConn)) (int, string) {
	t.Helper()
	c, dialErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", ws.Port()))
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatal("not a TCP connection")
	}
	defer func() { _ = tc.Close() }()
	req := "POST /upload/" + ticket + " HTTP/1.1\r\nHost: 127.0.0.1\r\n" + headers + "\r\n"
	if _, writeErr := tc.Write([]byte(req)); writeErr != nil {
		t.Fatalf("write request: %v", writeErr)
	}
	if writeBody != nil {
		writeBody(tc)
	}
	_ = tc.SetReadDeadline(time.Now().Add(60 * time.Second)) // failsafe, not the assertion
	// Exactly one response, not everything until EOF: the server keeps the
	// connection alive afterwards, so reading to EOF would wait out the
	// deadline on every passing case.
	resp, err := http.ReadResponse(bufio.NewReader(tc), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, string(body)
}

// rawUploadNoReply writes the request headers, hands control back with the
// connection still open, and closes it afterwards.
//
// It exists because there is one shape net/http will not answer at all: once
// a request-body read has timed out, the server cancels the connection and
// discards whatever the handler wrote, so a stalled upload gets no status
// line. That is not a defect to route around — a client that stopped
// talking is not listening either — and the observable that matters is the
// transfer's own terminal state, which is what during() waits for.
func rawUploadNoReply(t *testing.T, ws *WSServer, ticket, headers string, during func(c *net.TCPConn)) {
	t.Helper()
	c, dialErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", ws.Port()))
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	defer func() { _ = c.Close() }()
	tc, ok := c.(*net.TCPConn)
	if !ok {
		t.Fatal("not a TCP connection")
	}
	req := "POST /upload/" + ticket + " HTTP/1.1\r\nHost: 127.0.0.1\r\n" + headers + "\r\n"
	if _, writeErr := tc.Write([]byte(req)); writeErr != nil {
		t.Fatalf("write request: %v", writeErr)
	}
	during(tc)
}

// startStreamUpload does the control-plane half and returns the transfer id
// and the ticket the sink is waiting on.
func startStreamUpload(t *testing.T, e *filesTestEnv, bid, dir, name string, size int64, id int) (string, string) {
	t.Helper()
	got := callUpload(t, e.conn, uploadParams(bid, dir, name, size), id).mustResult(t)
	if got.Ticket == "" {
		t.Fatalf("want the stream branch, got %+v", got)
	}
	return got.TransferID, got.Ticket
}

// ── §5.4: the four ticket states, one per row ────────────────────────────

func TestUploadEndpoint_UnknownTicketIsGone(t *testing.T) {
	e := newUploadTestEnv(t)
	// Well-formed and never minted.
	status, _ := postUpload(t, e.ws, strings.Repeat("ab", uploadTicketHexLen/2), []byte("x"))
	if status != http.StatusGone {
		t.Fatalf("an unknown ticket names no transfer: want 410, got %d", status)
	}
}

// A malformed ticket is the same state as one that was never minted:
// answering it differently would tell a caller whether a well-formed guess
// existed.
func TestUploadEndpoint_MalformedTicketIsGone(t *testing.T) {
	e := newUploadTestEnv(t)
	for _, ticket := range []string{"short", strings.Repeat("A", uploadTicketHexLen), strings.Repeat("z", uploadTicketHexLen)} {
		if status, _ := postUpload(t, e.ws, ticket, []byte("x")); status != http.StatusGone {
			t.Fatalf("ticket %q: want 410, got %d", ticket, status)
		}
	}
}

// The paired success — without it every assertion above is satisfied by a
// handler that refuses everything.
func TestUploadEndpoint_MintedAndUnclaimedReadsTheBodyAndWritesTheFile(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	payload := bytes.Repeat([]byte("nocx"), 4096) // several chunks at the test chunk size
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", int64(len(payload)), 3)

	if status, body := postUpload(t, e.ws, ticket, payload); status != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}
	if state := awaitUploadState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	got, err := os.ReadFile(filepath.Join(dir, "a.txt")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("destination holds %d bytes, want %d", len(got), len(payload))
	}
	// Exactly one entry: the temp was consumed by the promote, not left.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want 1 (no temp left behind)", len(entries))
	}
	// And progress really advanced — the byte total is not decorative.
	rt := e.ws.uploads.get(tid)
	if _, _, n, _ := rt.snapshot(); n != int64(len(payload)) {
		t.Errorf("progress reported %d bytes, want %d", n, len(payload))
	}
}

func TestUploadEndpoint_SecondClaimWhileRunningIs409AndLeavesTheFirstAlone(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	const size = 4096
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", size, 3)

	// The first claimant, held mid-write: a pipe the test feeds.
	pr, pw := io.Pipe()
	req, reqErr := http.NewRequest(http.MethodPost, uploadURLFor(e.ws, ticket), pr)
	if reqErr != nil {
		t.Fatalf("new request: %v", reqErr)
	}
	req.ContentLength = size
	var wg sync.WaitGroup
	var firstStatus int
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := uploadHTTPClient.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		firstStatus = resp.StatusCode
	}()

	half := bytes.Repeat([]byte("a"), size/2)
	if _, writeErr := pw.Write(half); writeErr != nil {
		t.Fatalf("write first half: %v", writeErr)
	}
	rt := e.ws.uploads.get(tid)
	waitFor(t, "the first claimant to be mid-write", 30*time.Second, func() bool {
		_, _, n, _ := rt.snapshot()
		return n > 0
	})

	if status, _ := postUpload(t, e.ws, ticket, bytes.Repeat([]byte("b"), size)); status != http.StatusConflict {
		t.Fatalf("a second claim while the transfer runs: want 409, got %d", status)
	}

	if _, writeErr := pw.Write(bytes.Repeat([]byte("a"), size/2)); writeErr != nil {
		t.Fatalf("write second half: %v", writeErr)
	}
	_ = pw.Close()
	wg.Wait()

	if state := awaitUploadState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("the FIRST claimant must keep its transfer; state = %q", state)
	}
	if firstStatus != http.StatusOK {
		t.Fatalf("the first claimant's POST answered %d, want 200", firstStatus)
	}
	// #nosec G304 — under this test's own t.TempDir().
	got, err := os.ReadFile(filepath.Join(dir, "a.txt")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, bytes.Repeat([]byte("a"), size)) {
		t.Fatal("the destination holds the SECOND claimant's bytes; the first must be untouched")
	}
}

func TestUploadEndpoint_ClaimAfterCompletionIsGone(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	if status, body := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusOK {
		t.Fatalf("first POST: %d (%s)", status, body)
	}
	if state := awaitUploadState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	if status, _ := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusGone {
		t.Fatalf("a finished ticket is gone, not conflicted: got %d", status)
	}
}

// TestUploadEndpoint_ExpiredTicketWasAlreadyCancelledAndReadsAsUnknown is
// the row that is NOT in the table, and the reason it is not. Expiry is not
// a ticket state: the mint-side timer forgets the ticket and cancels the
// transfer it named, at that moment. A late POST therefore finds an unknown
// ticket, and 410 never has to mean "cancel what this names".
//
// The TTL is zero through the same option production reads, so expiry has
// already happened by the time the request arrives — no sleep, and the wait
// below is on the transfer's own settled state.
func TestUploadEndpoint_ExpiredTicketWasAlreadyCancelledAndReadsAsUnknown(t *testing.T) {
	e := newUploadTestEnv(t, WithUploadTicketTTL(0))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	if state := awaitUploadState(t, e.ws, tid); state != uploadStateCancelled {
		t.Fatalf("expiry cancels the transfer AT EXPIRY; state = %q", state)
	}
	if status, _ := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusGone {
		t.Fatalf("want 410, got %d", status)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0", len(entries), err)
	}
}

// ── the body bounds ───────────────────────────────────────────────────────

func TestUploadEndpoint_RequiresContentLength(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	// Chunked: no Content-Length at all.
	status, _ := rawUpload(t, e.ws, ticket, "Transfer-Encoding: chunked\r\n", func(c *net.TCPConn) {
		_, _ = c.Write([]byte("5\r\nhello\r\n0\r\n\r\n"))
	})
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", status)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0 — refused before a byte moved", len(entries), err)
	}
	// The malformed request did NOT burn the one-shot ticket.
	if status, body := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusOK {
		t.Fatalf("a refused request must not consume the ticket: got %d (%s)", status, body)
	}
}

// A Content-Length that disagrees with the declared size is refused BEFORE
// the body is read — in either direction. The longer direction is how "a
// body longer than the bound fails" is actually reached over HTTP: a client
// that means to send more has to say so in Content-Length, and saying so is
// what gets it refused.
func TestUploadEndpoint_RefusesAContentLengthThatDisagreesWithTheDeclaredSize(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	for _, declared := range []int{4, 6} {
		status, _ := rawUpload(t, e.ws, ticket,
			fmt.Sprintf("Content-Length: %d\r\n", declared),
			func(c *net.TCPConn) { _, _ = c.Write(bytes.Repeat([]byte("x"), declared)) })
		if status != http.StatusBadRequest {
			t.Fatalf("Content-Length %d against a declared size of 5: want 400, got %d", declared, status)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("directory holds %d entries (%v), want 0 — the refusal is before the body is read", len(entries), err)
		}
	}
	// The paired success on the same ticket: a matching length is accepted,
	// so neither refusal above consumed it.
	if status, body := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusOK {
		t.Fatalf("a matching Content-Length must be accepted: got %d (%s)", status, body)
	}
}

// A body ending short of Content-Length fails the transfer; the temp is
// removed and the destination is never touched.
func TestUploadEndpoint_AShortBodyFailsTheTransferAndLeavesTheDestinationAlone(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)
	params := uploadParams(bid, dir, "a.txt", 10)
	params["onExists"] = "overwrite"
	got := callUpload(t, e.conn, params, 3).mustResult(t)

	rawUpload(t, e.ws, got.Ticket, "Content-Length: 10\r\n", func(c *net.TCPConn) {
		_, _ = c.Write([]byte("abcd"))
		_ = c.CloseWrite() // the body ends four bytes in
	})

	if state := awaitUploadState(t, e.ws, got.TransferID); state != uploadStateFailed {
		t.Fatalf("state = %q, want %q", state, uploadStateFailed)
	}
	// #nosec G304 — under this test's own t.TempDir().
	body, err := os.ReadFile(dest) //nolint:gosec // see above
	if err != nil || string(body) != "original" {
		t.Fatalf("destination = %q, %v; a failed transfer never touches it", body, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want 1 — the temp must have been removed", len(entries))
	}
	rt := e.ws.uploads.get(got.TransferID)
	if _, out, _, _ := rt.snapshot(); len(out.Stranded) != 0 {
		t.Errorf("stranded = %v, want none: the temp was removable", out.Stranded)
	}
}

// A body that sends MORE than its own Content-Length is cut at that bound
// and FAILS there (§5.4), leaving the destination untouched.
//
// Nothing above the handler can see this. Content-Length was already
// required to equal the declared size, so net/http hands the sink exactly
// the declared number of bytes and reports a clean EOF; the excess is left
// on the connection to be misparsed as the next request, and the transfer
// reports itself written. The connection guard is what sees it, because it
// counts what the socket delivered and knows where the header block ended.
func TestUploadEndpoint_ABodyLongerThanContentLengthFailsAtTheBound(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	rawUpload(t, e.ws, ticket, "Content-Length: 5\r\n", func(c *net.TCPConn) {
		_, _ = c.Write([]byte("helloAND MORE THAN IT SAID"))
		_ = c.CloseWrite()
	})
	if state := awaitUploadState(t, e.ws, tid); state != uploadStateFailed {
		t.Fatalf("state = %q, want %q", state, uploadStateFailed)
	}
	// Nothing at the destination and no temp: a body that overran its own
	// framing is not a partial upload to be kept, it is a request whose
	// bytes cannot be trusted to be the ones that were declared.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory holds %d entries, want 0", len(entries))
	}
	rt := e.ws.uploads.get(tid)
	if _, out, _, _ := rt.snapshot(); len(out.Stranded) != 0 {
		t.Errorf("stranded = %v, want none: the temp was removable", out.Stranded)
	}
}

// The paired success, on the same shape: a body that ends exactly at its
// Content-Length is not an overrun. Without it the rule above is satisfied
// by a handler that fails every streamed upload.
func TestUploadEndpoint_ABodyThatEndsExactlyAtTheBoundIsNotAnOverrun(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	rawUpload(t, e.ws, ticket, "Content-Length: 5\r\n", func(c *net.TCPConn) {
		_, _ = c.Write([]byte("hello"))
		_ = c.CloseWrite()
	})
	if state := awaitUploadState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	body, err := os.ReadFile(filepath.Join(dir, "a.txt")) //nolint:gosec // see above
	if err != nil || string(body) != "hello" {
		t.Fatalf("destination = %q, %v", body, err)
	}
}

// TestUploadEndpoint_HeadersThenSilenceEndsTheTransfer is the failure the
// route's own deadlines exist for: valid headers followed by nothing would
// otherwise hold a transfer, a temp file and a lease open indefinitely. The
// stall deadline is re-armed before every read, so the bound is "no
// progress", never "the whole transfer inside one duration".
func TestUploadEndpoint_HeadersThenSilenceEndsTheTransfer(t *testing.T) {
	e := newUploadTestEnv(t, WithUploadStallTimeout(150*time.Millisecond))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 10, 3)

	var state string
	rawUploadNoReply(t, e.ws, ticket, "Content-Length: 10\r\n", func(*net.TCPConn) {
		// Headers, then silence. Nothing is ever written on this
		// connection again; the transfer must end anyway.
		state = awaitUploadState(t, e.ws, tid)
	})
	if state != uploadStateFailed {
		t.Fatalf("state = %q, want %q", state, uploadStateFailed)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0", len(entries), err)
	}
}

// TestUploadEndpoint_CancellingAStalledBodyUnwindsTheTransfer is the
// contract transfer.Sink.Put puts on its caller, tested rather than
// assumed. Put checks cancellation BETWEEN chunks and can never abandon a
// Read already in flight, so cancelling the context alone would leave this
// transfer waiting on a body that never speaks again.
//
// The stall deadline is set far beyond the test's own patience, so it
// cannot be what rescues this: if closing the body did not unblock the
// read, the wait below would not fail an assertion — it would hang, which
// is the production symptom (a stalled upload holding a temp file and a
// lease open indefinitely).
func TestUploadEndpoint_CancellingAStalledBodyUnwindsTheTransfer(t *testing.T) {
	e := newUploadTestEnv(t, WithUploadStallTimeout(10*time.Minute))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	const size = 4096
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", size, 3)

	pr, pw := io.Pipe()
	req, reqErr := http.NewRequest(http.MethodPost, uploadURLFor(e.ws, ticket), pr)
	if reqErr != nil {
		t.Fatalf("new request: %v", reqErr)
	}
	req.ContentLength = size
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := uploadHTTPClient.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if _, writeErr := pw.Write(bytes.Repeat([]byte("a"), size/2)); writeErr != nil {
		t.Fatalf("write half: %v", writeErr)
	}
	rt := e.ws.uploads.get(tid)
	waitFor(t, "the sink to be reading the body", 30*time.Second, func() bool {
		_, _, n, _ := rt.snapshot()
		return n > 0
	})

	// The body now says nothing more. Cancel.
	_ = jsonrpcCallWithID(t, e.conn, "files.uploadCancel", map[string]any{"transferId": tid}, 4)
	if state := awaitUploadState(t, e.ws, tid); state != uploadStateCancelled {
		t.Fatalf("state = %q, want %q", state, uploadStateCancelled)
	}
	_ = pw.Close()
	wg.Wait()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0 — the temp goes with the cancel", len(entries), err)
	}
}

// ── §5.4: this route sets its own deadlines, and the first one binds
// before the handler exists ───────────────────────────────────────────────

// TestUploadEndpoint_HeadersThatNeverEndAreDropped is the hole the handler's
// own deadline cannot reach. Go's server parses the COMPLETE header block
// before it dispatches, so a request whose headers never end never arrives
// at handleUpload and nothing inside it can bound the wait. An
// unauthenticated local process could therefore open loopback connections,
// send a partial POST /upload/... header block and hold them for ever,
// holding neither the WebSocket capability token nor a ticket.
//
// The ticket below is well-formed and names nothing, which is the point: the
// guard acts before anything has looked a ticket up.
func TestUploadEndpoint_HeadersThatNeverEndAreDropped(t *testing.T) {
	e := newUploadTestEnv(t, withUploadHeaderTimeout(150*time.Millisecond))
	c, dialErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", e.ws.Port()))
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write([]byte("POST /upload/" + strings.Repeat("ab", uploadTicketHexLen/2) +
		" HTTP/1.1\r\nHost: 127.0.0.1\r\n")); err != nil {
		t.Fatalf("write partial headers: %v", err)
	}
	// The header block never ends. The failsafe is generous and is NOT the
	// assertion: it is told apart from the answer, because a read that ends
	// on our own deadline is precisely the failing case dressed as a pass.
	// What passes is the connection going — an EOF or a reset.
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	var buf [1]byte
	_, err := c.Read(buf[:])
	switch {
	case err == nil:
		t.Fatal("the server answered a header block that never ended")
	case errors.Is(err, os.ErrDeadlineExceeded):
		t.Fatal("a header block that never ends held the connection open")
	}
}

// scriptedConn is a net.Conn that hands out prepared chunks and records
// every read deadline set on it. The embedded nil interface is deliberate:
// anything the guard reaches for beyond Read and SetReadDeadline panics
// rather than silently doing nothing.
type scriptedConn struct {
	net.Conn
	chunks    [][]byte
	deadlines []time.Time
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks = c.chunks[1:]
	return n, nil
}

func (c *scriptedConn) SetReadDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	return nil
}

func (c *scriptedConn) armed() bool {
	return len(c.deadlines) > 0 && !c.deadlines[len(c.deadlines)-1].IsZero()
}

// TestUploadGuard_BindsTheUploadRouteAndLetsEveryOtherGo is why the
// mechanism can be a wrapped listener without touching /session.
//
// Both halves matter. A guard that armed everything and cleared at the
// header terminator would be ReadHeaderTimeout by hand — which ws.go
// deliberately leaves at zero — so the interval for a request that is not an
// upload has to close at the request LINE: /session is bounded until its
// target is known and never afterwards, so the upgrade, the handler and the
// hijacked connection are all provably outside it. For the upload route the
// interval runs to the end of the header block, which is what §5.4 asks for.
func TestUploadGuard_BindsTheUploadRouteAndLetsEveryOtherGo(t *testing.T) {
	const ticket = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("a request that is not an upload is released at the request line", func(t *testing.T) {
		raw := &scriptedConn{chunks: [][]byte{
			[]byte("GET /session HTTP/1.1\r\n"),
			[]byte("Host: 127.0.0.1\r\nUpgrade: websocket\r\n"),
		}}
		g := newUploadGuardConn(raw, func() time.Duration { return time.Minute })
		if !raw.armed() {
			t.Fatal("the guard must arm before the first byte is read")
		}
		drain(t, g)
		if raw.armed() {
			t.Fatal("/session is still bound after its request line; the interval must close there")
		}
		cleared := len(raw.deadlines)
		drain(t, g)
		if len(raw.deadlines) != cleared {
			t.Fatal("/session had a deadline set after its target was known")
		}
		if end := g.headerBlockEnd(); end >= 0 {
			t.Fatalf("headerBlockEnd = %d for a route the guard released; want unknown", end)
		}
	})

	t.Run("an upload is bound to the end of its header block", func(t *testing.T) {
		head := "POST /upload/" + ticket + " HTTP/1.1\r\nHost: 127.0.0.1\r\n"
		raw := &scriptedConn{chunks: [][]byte{
			[]byte(head),
			[]byte("Content-Length: 5\r\n\r\n"),
			[]byte("hello"),
		}}
		g := newUploadGuardConn(raw, func() time.Duration { return time.Minute })
		readOnce(t, g)
		if !raw.armed() {
			t.Fatal("the upload route was released at its request line; §5.4 bounds the whole header block")
		}
		readOnce(t, g)
		if raw.armed() {
			t.Fatal("the guard is still bound after the header block ended")
		}
		want := int64(len(head) + len("Content-Length: 5\r\n\r\n"))
		if got := g.headerBlockEnd(); got != want {
			t.Fatalf("headerBlockEnd = %d, want %d", got, want)
		}
		drain(t, g)
		if n := g.bytesRead(); n != want+5 {
			t.Fatalf("bytesRead = %d, want %d", n, want+5)
		}
	})
}

func readOnce(t *testing.T, r io.Reader) {
	t.Helper()
	buf := make([]byte, 4096)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func drain(t *testing.T, r io.Reader) {
	t.Helper()
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// ── the guards ────────────────────────────────────────────────────────────

// The same OriginPolicy that guards /session guards this route.
func TestUploadEndpoint_RefusesADisallowedOrigin(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	req, err := http.NewRequest(http.MethodPost, uploadURLFor(e.ws, ticket), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0", len(entries), err)
	}
}

func TestUploadEndpoint_RefusesAnythingButPost(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	resp, err := uploadHTTPClient.Get(uploadURLFor(e.ws, ticket))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

// ── D4: the ticket is a credential, so it is never written down ──────────

// TestUploadEndpoint_NeverLogsOrEchoesTheTicket. Possession of a sink
// ticket authorises both the destination and the bytes written to it, so a
// ticket in a log line, a crash report or an error message is an integrity
// hole and not merely untidy. The check is a grep over everything the
// server said — logs and response bodies — across every path a ticket
// travels: a successful upload, a rejected origin (whose warning is the
// one that most wants to quote the request path), an unknown ticket, and a
// double claim.
func TestUploadEndpoint_NeverLogsOrEchoesTheTicket(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug})))

	e := newUploadTestEnvWithLogger(t, logger)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	var said []string
	_, body := postUpload(t, e.ws, ticket, []byte("hello"))
	said = append(said, body)
	awaitUploadState(t, e.ws, tid)

	_, body = postUpload(t, e.ws, ticket, []byte("hello")) // now finished → 410
	said = append(said, body)

	unknown := strings.Repeat("cd", uploadTicketHexLen/2)
	_, body = postUpload(t, e.ws, unknown, []byte("hello"))
	said = append(said, body)

	req, err := http.NewRequest(http.MethodPost, uploadURLFor(e.ws, ticket), bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	said = append(said, string(raw))

	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	if strings.Contains(logged, ticket) {
		t.Fatalf("the sink ticket appears in the log; it is a bearer credential:\n%s", logged)
	}
	if strings.Contains(logged, unknown) {
		t.Fatalf("a rejected ticket appears in the log:\n%s", logged)
	}
	for i, s := range said {
		if strings.Contains(s, ticket) || strings.Contains(s, unknown) {
			t.Fatalf("response %d echoes a ticket: %q", i, s)
		}
	}
	// The grep only means something if the logger actually captured
	// something — an empty buffer would pass every assertion above.
	if !strings.Contains(logged, "upload rejected") {
		t.Fatalf("the capturing logger recorded nothing about the refused origin:\n%s", logged)
	}
}

// lockedWriter serialises the capturing logger's writes: slog handlers are
// safe for concurrent use, but a bytes.Buffer is not, and the transfer
// goroutines log alongside the test.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
