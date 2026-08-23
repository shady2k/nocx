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
	resp, body := rawUploadResponse(t, ws, ticket, headers, writeBody)
	return resp.StatusCode, body
}

// rawUploadResponse is the same request with the whole reply handed back,
// for the assertions that are about HEADERS rather than only a status —
// the CORS contract has to hold on the refusals a well-behaved client
// cannot even provoke. The body is drained before the connection closes,
// so the returned response is safe to read after this returns.
func rawUploadResponse(t *testing.T, ws *WSServer, ticket, headers string, writeBody func(c *net.TCPConn)) (*http.Response, string) {
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
	return resp, string(body)
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

// TestFilesUpload_ALocalBindingWritesOntoTheBackendsOwnMachine is what
// nocx-9le.5.22 exists for, watched end to end over the real wire: a
// browser drop on a LOCAL tab uploads into that tab's directory.
//
// The factory here is filesLocalFactory — the composition root's own local
// branch, unwrapped and with no test sink in the way — so what this
// exercises is the provider the product builds, its transfer.Sink over os,
// the collision probe, the streamed POST and the promote. The remote path's
// round trip is asserted a few tests above through uploadableFactory; the
// two now differ only in which filesystem the sink writes to, which is D1
// ("one sink, two sources") holding at the other end as well.
//
// R1 is satisfied rather than bent: the bytes land on the backend's own
// machine, which is the machine that tab's shell is on.
func TestFilesUpload_ALocalBindingWritesOntoTheBackendsOwnMachine(t *testing.T) {
	e := newUploadTestEnvWith(t, log.NewSlogAdapter(nil), filesLocalFactory)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	payload := bytes.Repeat([]byte("nocx"), 4096)

	tid, ticket := startStreamUpload(t, e, bid, dir, "dropped.txt", int64(len(payload)), 3)
	if status, body := postUpload(t, e.ws, ticket, payload); status != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", status, body)
	}

	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	got, err := os.ReadFile(filepath.Join(dir, "dropped.txt")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("destination holds %d bytes, want %d", len(got), len(payload))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want 1 (no temp left behind)", len(entries))
	}
}

// TestFilesUpload_ALocalUploadAsksTheSameCollisionQuestion: the collision
// decision is transport-agnostic and is REUSED on this path rather than
// re-implemented — a destination that is already taken stops the upload
// before a byte moves and before a ticket is minted, exactly as on a remote
// tab, and the file the person already had is untouched.
func TestFilesUpload_ALocalUploadAsksTheSameCollisionQuestion(t *testing.T) {
	e := newUploadTestEnvWith(t, log.NewSlogAdapter(nil), filesLocalFactory)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	dest := filepath.Join(dir, "taken.txt")
	if err := os.WriteFile(dest, []byte("mine"), 0o600); err != nil {
		t.Fatalf("seed the destination: %v", err)
	}
	bid := e.openBinding(t, sid, dir, 2)

	got := callUpload(t, e.conn, uploadParams(bid, dir, "taken.txt", 5), 3).mustResult(t)

	if got.Collision != "exists" {
		t.Fatalf("collision = %q, want %q — the question is asked before a byte moves", got.Collision, "exists")
	}
	if got.TransferID != "" || got.Ticket != "" {
		t.Errorf("result %+v, want nothing started until the person answers", got)
	}
	// #nosec G304 — under this test's own t.TempDir().
	if b, err := os.ReadFile(dest); err != nil || string(b) != "mine" {
		t.Errorf("the existing file is now %q (%v), want it untouched", b, err)
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
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
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
	rt := e.ws.transfers.get(tid)
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
	rt := e.ws.transfers.get(tid)
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

	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
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
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	if status, _ := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusGone {
		t.Fatalf("a finished ticket is gone, not conflicted: got %d", status)
	}

	// 410 alone does not tell this row of §5.4's table from its neighbour.
	// Deleting the ticket at completion would answer 410 too, through the
	// UNKNOWN branch, and every assertion above would pass unchanged — so
	// one of the four normative rows would have no test of its own. The
	// branch is what is asserted, and the contrast is what makes it
	// evidence: the same call on a ticket nothing ever minted answers
	// differently.
	rt, claim := e.ws.transfers.claim(ticket, dirUpload, 5)
	if claim != transferClaimFinished {
		t.Fatalf("claim = %d, want transferClaimFinished (%d): the finished ticket must be RETAINED", claim, transferClaimFinished)
	}
	if rt != nil {
		t.Fatal("the finished branch handed back a transfer; it names nothing to claim")
	}
	if _, other := e.ws.transfers.claim(strings.Repeat("ab", uploadTicketHexLen/2), dirUpload, 5); other != transferClaimUnknown {
		t.Fatalf("a never-minted ticket claims as %d, want transferClaimUnknown (%d)", other, transferClaimUnknown)
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
	e := newUploadTestEnv(t, WithTransferTicketTTL(0))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	if state := awaitTransferState(t, e.ws, tid); state != uploadStateCancelled {
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

	// Chunked: no Content-Length at all — and not one byte of body is ever
	// sent. That is the assertion, not the empty directory: a handler that
	// claimed the ticket and drained the body before answering 400 would
	// leave the directory empty too, and would pass. With nothing on the
	// wire to read, such a handler blocks and never answers, so the
	// response arriving at all is what proves the refusal came first.
	status, _ := rawUpload(t, e.ws, ticket, "Transfer-Encoding: chunked\r\n", nil)
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
		// The headers, and then silence. §5.4 puts this refusal BEFORE the
		// body is read, so the handler must answer without a byte of body
		// on the wire; one that read first would block here for ever
		// instead of leaving an empty directory behind and passing.
		status, _ := rawUpload(t, e.ws, ticket, fmt.Sprintf("Content-Length: %d\r\n", declared), nil)
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

	if state := awaitTransferState(t, e.ws, got.TransferID); state != uploadStateFailed {
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
	rt := e.ws.transfers.get(got.TransferID)
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
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateFailed {
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
	rt := e.ws.transfers.get(tid)
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
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
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
	e := newUploadTestEnv(t, WithTransferStallTimeout(150*time.Millisecond))
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 10, 3)

	var state string
	rawUploadNoReply(t, e.ws, ticket, "Content-Length: 10\r\n", func(*net.TCPConn) {
		// Headers, then silence. Nothing is ever written on this
		// connection again; the transfer must end anyway.
		state = awaitTransferState(t, e.ws, tid)
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
	e := newUploadTestEnv(t, WithTransferStallTimeout(10*time.Minute))
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
	rt := e.ws.transfers.get(tid)
	waitFor(t, "the sink to be reading the body", 30*time.Second, func() bool {
		_, _, n, _ := rt.snapshot()
		return n > 0
	})

	// The body now says nothing more. Cancel.
	_ = jsonrpcCallWithID(t, e.conn, "files.uploadCancel", map[string]any{"transferId": tid}, 4)
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateCancelled {
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
	awaitTransferState(t, e.ws, tid)

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

// ── CORS: the route is cross-origin by construction (nocx-9le.5.19) ──────
//
// Every assertion in this section is about a reply a BROWSER has to be able
// to read. The route is reached from a page that vite serves on its own
// port, against a backend listening on another, so a POST here is a
// cross-origin request in every browser configuration the product has —
// which is why the whole feature was unreachable from a browser while every
// test above was green: they are all non-browser callers, and a non-browser
// caller never asks for these headers.

const (
	// browserOrigin is the shape `dev-web` actually sends: an http loopback
	// origin on vite's port, which LoopbackOriginPolicy allows.
	browserOrigin = "http://localhost:5173"
	// foreignOrigin is a page that must never drive this route.
	foreignOrigin = "https://evil.example.com"
)

// uploadBrowserRequest addresses the route the way a browser does — a
// method, an Origin, and, for a preflight, the two headers a real preflight
// carries — and hands back the whole reply so a test can read its headers.
func uploadBrowserRequest(t *testing.T, ws *WSServer, method, ticket, origin string, body []byte) (*http.Response, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, uploadURLFor(ws, ticket), r)
	if err != nil {
		t.Fatalf("new %s request: %v", method, err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "content-type")
	}
	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("%s /upload: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, string(raw)
}

// assertNamesTheOrigin is the whole positive half of the contract, in one
// place because it must hold identically on a 200 and on a 410 — the reply
// a browser can read is what keeps those two apart.
func assertNamesTheOrigin(t *testing.T, h http.Header, origin string) {
	t.Helper()
	if got := h.Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("Access-Control-Allow-Origin = %q, want the requesting origin echoed exactly (%q)", got, origin)
	}
	if got := h.Get("Access-Control-Allow-Origin"); got == "*" {
		t.Errorf("Access-Control-Allow-Origin is a wildcard; the ticket authorises a filesystem write and any page could then read the reply")
	}
	if got := h.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want it to name Origin: the answer depends on that request header", got)
	}
	if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want it absent: the ticket is the credential and cookies have no business here", got)
	}
}

func TestUploadCORS_PreflightFromAnAllowedOriginIs204AndNamesTheContract(t *testing.T) {
	e := newUploadTestEnv(t)
	// A ticket nothing ever minted, deliberately: the preflight answers
	// without looking one up, so a well-formed guess learns nothing.
	resp, body := uploadBrowserRequest(t, e.ws, http.MethodOptions, strings.Repeat("ab", uploadTicketHexLen/2), browserOrigin, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight: want 204, got %d (%s)", resp.StatusCode, body)
	}
	if body != "" {
		t.Errorf("preflight answered with a body %q; 204 carries none", body)
	}
	assertNamesTheOrigin(t, resp.Header, browserOrigin)
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != http.MethodPost {
		t.Errorf("Access-Control-Allow-Methods = %q, want exactly %q", got, http.MethodPost)
	}
	// Exactly the header the handler needs. A blanket Authorization or an
	// X-* wildcard would widen what a page may send at a route whose only
	// credential is in the URL.
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.EqualFold(got, "Content-Type") {
		t.Errorf("Access-Control-Allow-Headers = %q, want exactly Content-Type", got)
	}
	// Omitted on purpose: a cached preflight is an origin decision the
	// server cannot withdraw for as long as the cache holds it.
	if got := resp.Header.Get("Access-Control-Max-Age"); got != "" {
		t.Errorf("Access-Control-Max-Age = %q, want it omitted", got)
	}
}

// The preflight is not a claim. A browser sends OPTIONS before every POST
// it cannot safelist, so a preflight that consumed, extended or revealed a
// one-shot ticket would make the upload impossible from the only client
// that needs the route at all.
func TestUploadCORS_PreflightDoesNotConsumeTheTicket(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	// Twice: once is not evidence against a handler that claims on the
	// second look, and a browser really can preflight more than once.
	for i := 0; i < 2; i++ {
		resp, _ := uploadBrowserRequest(t, e.ws, http.MethodOptions, ticket, browserOrigin, nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("preflight %d: want 204, got %d", i, resp.StatusCode)
		}
	}
	// The ticket is still redeemable, which is the assertion. A handler
	// that claimed on OPTIONS would answer 409 or 410 here.
	if status, body := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusOK {
		t.Fatalf("the preflight consumed the ticket: POST answered %d (%s)", status, body)
	}
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
	// #nosec G304 — under this test's own t.TempDir().
	got, err := os.ReadFile(filepath.Join(dir, "a.txt")) //nolint:gosec // see above
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("destination holds %q, want %q", got, "hello")
	}
}

// A refused origin gets no origin back — on either method — and its refusal
// costs the ticket nothing.
func TestUploadCORS_ARefusedOriginIsNamedBackToNobodyAndKeepsItsHandsOffTheTicket(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	for _, method := range []string{http.MethodOptions, http.MethodPost} {
		var body []byte
		if method == http.MethodPost {
			body = []byte("hello")
		}
		resp, _ := uploadBrowserRequest(t, e.ws, method, ticket, foreignOrigin, body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s from %s: want 403, got %d", method, foreignOrigin, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: a refused origin was handed %q back; the browser would then let the page read the reply", method, got)
		}
		// Vary still, even on the refusal: a cache that missed it would
		// serve this 403 to an origin the policy allows.
		if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
			t.Errorf("%s: Vary = %q on the refusal, want it to name Origin", method, got)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("directory holds %d entries (%v), want 0", len(entries), err)
	}
	// And the ticket survived both refusals.
	if status, body := postUpload(t, e.ws, ticket, []byte("hello")); status != http.StatusOK {
		t.Fatalf("a refused origin consumed the ticket: got %d (%s)", status, body)
	}
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
}

// The origin check runs BEFORE the ticket is looked up, and the evidence is
// that a refused origin cannot tell a live ticket from a guess: three
// tickets in three different states, one identical answer. A handler that
// looked first would answer 410 for the unknown one and 403 for the live
// one, which is an oracle for a credential that authorises a write.
func TestUploadCORS_ARefusedOriginCannotTellALiveTicketFromAGuess(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, live := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	unknown := strings.Repeat("cd", uploadTicketHexLen/2)
	malformed := "short"
	var seen []string
	for _, ticket := range []string{live, unknown, malformed} {
		resp, body := uploadBrowserRequest(t, e.ws, http.MethodPost, ticket, foreignOrigin, []byte("hello"))
		seen = append(seen, fmt.Sprintf("%d|%s", resp.StatusCode, body))
	}
	for i := range seen {
		if seen[i] != seen[0] {
			t.Fatalf("a refused origin distinguished ticket %d from the first: %q vs %q", i, seen[i], seen[0])
		}
	}
	if !strings.HasPrefix(seen[0], fmt.Sprintf("%d|", http.StatusForbidden)) {
		t.Fatalf("want every answer to be 403, got %q", seen[0])
	}
}

// The headers are on the ERROR replies, and that is the point of the task:
// a browser hands the page nothing at all from a cross-origin reply that
// does not name it, so "the ticket is gone" and "the network died" arrive
// as the same "Failed to fetch". The renderer was taught to treat those
// two differently (nocx-9le.5.18); without this, it cannot.
func TestUploadCORS_TheHeadersAreOnEveryReplyIncludingTheRefusals(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)

	// 410 — a ticket that names nothing.
	resp, _ := uploadBrowserRequest(t, e.ws, http.MethodPost, strings.Repeat("ab", uploadTicketHexLen/2), browserOrigin, []byte("x"))
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("unknown ticket: want 410, got %d", resp.StatusCode)
	}
	assertNamesTheOrigin(t, resp.Header, browserOrigin)

	// 400 — a Content-Length that disagrees with the declared size.
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)
	resp, _ = uploadBrowserRequest(t, e.ws, http.MethodPost, ticket, browserOrigin, []byte("hello there"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("size mismatch: want 400, got %d", resp.StatusCode)
	}
	assertNamesTheOrigin(t, resp.Header, browserOrigin)

	// 400 again, the other shape — no Content-Length at all, which no
	// browser sends and which a raw client is needed to produce.
	rawResp, _ := rawUploadResponse(t, e.ws, ticket, "Origin: "+browserOrigin+"\r\nTransfer-Encoding: chunked\r\n", nil)
	if rawResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("absent Content-Length: want 400, got %d", rawResp.StatusCode)
	}
	assertNamesTheOrigin(t, rawResp.Header, browserOrigin)

	// 200 — the paired success, without which every assertion above is
	// satisfied by a handler that refuses everything.
	tid, ok := startStreamUpload(t, e, bid, dir, "b.txt", 5, 4)
	resp, _ = uploadBrowserRequest(t, e.ws, http.MethodPost, ok, browserOrigin, []byte("hello"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	assertNamesTheOrigin(t, resp.Header, browserOrigin)
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("state = %q, want %q", state, uploadStateWritten)
	}
}

// 5xx carries them too. A body that ends short of its own Content-Length
// fails the transfer, and the handler answers "the transfer failed" — an
// answer the renderer must be able to read as a status rather than as a
// network fault.
func TestUploadCORS_TheHeadersAreOnAFailedTransferToo(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	tid, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 8, 3)

	resp, _ := rawUploadResponse(t, e.ws, ticket, "Origin: "+browserOrigin+"\r\nContent-Length: 8\r\n", func(c *net.TCPConn) {
		if _, err := c.Write([]byte("hell")); err != nil {
			t.Errorf("write short body: %v", err)
		}
		// A half-close is the source stopping, which is what makes this a
		// failure rather than a stall the deadline has to find.
		if err := c.CloseWrite(); err != nil {
			t.Errorf("close write: %v", err)
		}
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a short body: want 500, got %d", resp.StatusCode)
	}
	assertNamesTheOrigin(t, resp.Header, browserOrigin)
	if state := awaitTransferState(t, e.ws, tid); state == uploadStateWritten {
		t.Fatal("a short body must not write the destination")
	}
}

// 409 carries them too — and this is the status the renderer most needs to
// read, because it is the one that means somebody else's transfer is alive
// and not ours to mourn.
func TestUploadCORS_TheHeadersAreOnAConflictToo(t *testing.T) {
	e := newUploadTestEnv(t)
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
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	if _, err := pw.Write(bytes.Repeat([]byte("a"), size/2)); err != nil {
		t.Fatalf("write first half: %v", err)
	}
	rt := e.ws.transfers.get(tid)
	waitFor(t, "the first claimant to be mid-write", 30*time.Second, func() bool {
		_, _, n, _ := rt.snapshot()
		return n > 0
	})

	resp, _ := uploadBrowserRequest(t, e.ws, http.MethodPost, ticket, browserOrigin, bytes.Repeat([]byte("b"), size))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	assertNamesTheOrigin(t, resp.Header, browserOrigin)

	if _, err := pw.Write(bytes.Repeat([]byte("a"), size/2)); err != nil {
		t.Fatalf("write second half: %v", err)
	}
	_ = pw.Close()
	wg.Wait()
	if state := awaitTransferState(t, e.ws, tid); state != uploadStateWritten {
		t.Fatalf("the first claimant must keep its transfer; state = %q", state)
	}
}

// An Origin the policy does not allow is refused whatever it says, and
// "null" is the one worth pinning: it is what a sandboxed iframe, a
// data: document and a file:// page all send, and reading it as "no
// origin, therefore not a browser" would hand exactly those the route.
func TestUploadCORS_ANullOriginIsRefused(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	for _, method := range []string{http.MethodOptions, http.MethodPost} {
		resp, _ := uploadBrowserRequest(t, e.ws, method, ticket, "null", nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s with Origin: null: want 403, got %d", method, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want none", method, got)
		}
	}
}

// A caller that sends no Origin is not a browser — LoopbackOriginPolicy
// allows it deliberately, and it is how every other test in this file
// reaches the route. It gets no Access-Control-Allow-Origin, because there
// is nothing to echo and a wildcard is never the answer.
func TestUploadCORS_ACallerWithNoOriginIsAnsweredWithNoAllowOrigin(t *testing.T) {
	e := newUploadTestEnv(t)
	sid := e.openSession(t, 1)
	dir := t.TempDir()
	bid := e.openBinding(t, sid, dir, 2)
	_, ticket := startStreamUpload(t, e, bid, dir, "a.txt", 5, 3)

	resp, body := uploadBrowserRequest(t, e.ws, http.MethodPost, ticket, "", []byte("hello"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none: there is no origin to echo", got)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want it to name Origin even here", got)
	}
}

// CORS is scoped to this route and to nothing else on the mux. /session is
// a WebSocket upgrade whose origin check already refuses a foreign page,
// and a browser cannot be given a way to fetch it cross-origin.
func TestUploadCORS_IsScopedToTheUploadRoute(t *testing.T) {
	e := newUploadTestEnv(t)
	req, err := http.NewRequest(http.MethodOptions, fmt.Sprintf("http://127.0.0.1:%d/session", e.ws.Port()), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", browserOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	resp, err := uploadHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /session: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("/session answered Access-Control-Allow-Origin %q; CORS belongs to the upload route alone", got)
	}
}
