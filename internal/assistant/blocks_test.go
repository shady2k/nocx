package assistant

// The block tools' tests (nocx-5u3oz.6): the pair that lists the blocks a
// run was granted and reads a WINDOW of one. The narrowing (a session, and
// therefore a block, the grant does not name is refused BEFORE anything is
// read), the window contract of the return (design §4.4 — the model learns
// the total before it reads, and a window past the end is answered honestly
// rather than as an error), and the end-to-end: several hundred lines of
// output, a question about the END of it, answered.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// fakeBlocks is the session source with a call log and scripted answers.
type fakeBlocks struct {
	mu     sync.Mutex
	listed []listedBlocks
	read   []readBlock
	items  SessionItems
	item   SessionItemRead
	err    error
}

type listedBlocks struct {
	sessionID string
	limit     int
}

type readBlock struct {
	sessionID string
	blockID   string
	start     int
	count     int
}

func (f *fakeBlocks) ListSessionItems(_ context.Context, sessionID string, limit int) (SessionItems, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, listedBlocks{sessionID: sessionID, limit: limit})
	if f.err != nil {
		return SessionItems{}, f.err
	}
	return f.items, nil
}

func (f *fakeBlocks) ReadSessionItem(_ context.Context, sessionID, itemID string, start, count int) (SessionItemRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read = append(f.read, readBlock{sessionID: sessionID, blockID: itemID, start: start, count: count})
	if f.err != nil {
		return SessionItemRead{}, f.err
	}
	out := f.item
	out.ID = itemID
	out.Start, out.End = start, start+count
	return out, nil
}

func (f *fakeBlocks) listCalls() []listedBlocks {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]listedBlocks(nil), f.listed...)
}

func (f *fakeBlocks) readCalls() []readBlock {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readBlock(nil), f.read...)
}

// unscriptedBlocks is embedded by renderer/run fakes; any session-source call
// is a test defect rather than an empty successful answer.
type unscriptedBlocks struct{}

func (unscriptedBlocks) ListSessionItems(context.Context, string, int) (SessionItems, error) {
	return SessionItems{}, errors.New("test seam: ListSessionItems is not scripted")
}

func (unscriptedBlocks) ReadSessionItem(context.Context, string, string, int, int) (SessionItemRead, error) {
	return SessionItemRead{}, errors.New("test seam: ReadSessionItem is not scripted")
}

// ── test-local helpers ───────────────────────────────────────────────────

// requestBody reads the completion request the engine sent. The fake server
// puts the body back after recording it, so the handler reads the same bytes
// the transport wrote — which is what makes an assertion about what the model
// was HANDED an assertion about the engine, not about the test.
func requestBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return string(b)
}

// streamAnswer writes one streamed answer, the way a real completion arrives.
func streamAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON(text, ""))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON("", "stop"))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// totalFromListBody reads the line count out of the session.list result the
// engine put into the conversation — the model's own path to the total.
func totalFromListBody(t *testing.T, body string) int {
	t.Helper()
	const key = `\"lines\":`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatalf("no session.list result in the request the model was handed: %s", body)
	}
	rest := body[i+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("line count %q: %v", rest[:end], err)
	}
	return n
}

func ptrGrant(g content.Grant) *content.Grant { return &g }

func toolsDirFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(realToolsFS)
}

// ── the middleware ───────────────────────────────────────────────────────

// The wiring gap is honest: a run whose block seam is not wired refuses the
// tool with a sentence, never a silent empty list.
func TestMiddleware_BlocksWithoutSourceIsHonest(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, nil)
	_, err := wrappedEndpoint(mw, "session.list", "call-1", `{"sessionId":"session-a"}`)
	if err == nil {
		t.Fatal("session.list with no session source succeeded; want an honest failure")
	}
	if !strings.Contains(err.Error(), "no session source") {
		t.Errorf("error = %v, want it to name the missing seam", err)
	}
}

// The middleware's dispatch reaches the executors, and the narrowing holds
// through it: a call naming another session is refused — the refusal is the
// call's result, in our words (nocx-uvac6.1) — and the ledger is never
// asked.
func TestMiddleware_BlocksRefusedOutsideGrantIsAResult(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	src := &fakeBlocks{items: SessionItems{Items: []SessionItem{{ID: "blk-1", Command: "ls", State: "exited"}}}}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, &blocksOnlyRequester{blocks: src})

	out, err := wrappedEndpoint(mw, "session.list", "call-1", `{"sessionId":"session-b"}`)
	if err != nil {
		t.Fatalf("session.list on another session gave an error %v — want the refusal as a tool result", err)
	}
	if !strings.Contains(out, "REFUSED") {
		t.Fatalf("session.list on another session result = %q, want a refusal in our words", out)
	}
	if calls := src.listCalls(); len(calls) != 0 {
		t.Fatalf("the ledger was asked %v; want never asked", calls)
	}

	out, err = wrappedEndpoint(mw, "session.list", "call-2", `{"sessionId":"session-a"}`)
	if err != nil {
		t.Fatalf("session.list on the granted session: %v", err)
	}
	if !strings.Contains(out, "blk-1") {
		t.Errorf("result %s does not carry the block", out)
	}
}

// blocksOnlyRequester is the run's seam with the block half scripted and the
// renderer half unscripted: the block tools never ask the renderer for
// anything.
type blocksOnlyRequester struct {
	blocks SessionSource
}

func (r *blocksOnlyRequester) RequestScreen(context.Context, string, *FrameRegion) (json.RawMessage, error) {
	return nil, errors.New("blocks test: RequestScreen is not scripted")
}

func (r *blocksOnlyRequester) RequestRun(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("blocks test: RequestRun is not scripted")
}

func (r *blocksOnlyRequester) ListSessionItems(ctx context.Context, sessionID string, limit int) (SessionItems, error) {
	return r.blocks.ListSessionItems(ctx, sessionID, limit)
}

func (r *blocksOnlyRequester) ReadSessionItem(ctx context.Context, sessionID, itemID string, start, count int) (SessionItemRead, error) {
	return r.blocks.ReadSessionItem(ctx, sessionID, itemID, start, count)
}

// there. The answer is the marker that lives on line 397 and nowhere else,
// so a run that read the head of the block cannot pass this.
func TestAsk_LongOutputIsAnsweredFromTheEnd(t *testing.T) {
	const marker = "Error: disk quota exceeded on /dev/sda9"
	lines := make([]string, 400)
	for i := range lines {
		lines[i] = fmt.Sprintf("filesystem-%03d      1.0T   400G   600G  40%%", i)
	}
	lines[397] = marker

	src := &fakeBlocks{
		items: SessionItems{Items: []SessionItem{{
			ID: "blk-df", Command: "df -h", State: "exited", Lines: len(lines),
		}}},
		item: SessionItemRead{
			Command: "df -h", State: "exited", Total: len(lines), Start: 0, End: len(lines), Text: strings.Join(lines, "\n"),
		},
	}
	var turn int
	f, srv := newFakeOpenAI(func(w http.ResponseWriter, r *http.Request) {
		turn++
		switch turn {
		case 1:
			streamToolCalls(w, toolCallSpec{name: "session.list", args: `{"sessionId":"session-a"}`, id: "call_list"})
		case 2:
			// The model read the total off the list result and aims at the end.
			total := totalFromListBody(t, requestBody(r))
			args := fmt.Sprintf(`{"sessionId":"session-a","id":"blk-df","start":%d,"count":20}`, total-20)
			streamToolCalls(w, toolCallSpec{name: "session.read", args: args, id: "call_read"})
		default:
			answer := "the output ends with something else"
			if strings.Contains(requestBody(r), marker) {
				answer = marker
			}
			streamAnswer(w, answer)
		}
	})
	defer srv.Close()

	p := askParams(srv.URL, ptrGrant(sessionGrant("session-a", autonomousMatrix())), &fakeLedger{}, nil)
	p.Requester = &blocksOnlyRequester{blocks: src}
	p.Messages = []Message{{Role: "user", Content: "did df fail, and why?"}}

	cl, err := newClient(nil, toolsDirFS(t))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	var answer strings.Builder
	// Only the ANSWER, deliberately: since nocx-bshm2 a tool's return value
	// no longer travels the delta path, so this asserts the model NAMED the
	// marker in its prose — not that the tool's window happened to contain
	// it. That is the criterion the epic actually wants.
	if err := cl.Ask(context.Background(), p, func(ev AskEvent) error {
		if ev.Kind == AskAnswer {
			answer.WriteString(ev.Text)
		}
		return nil
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(answer.String(), marker) {
		t.Fatalf("answer = %q, want it to name the line at the END of the output", answer.String())
	}
	// And the read the model made was the END of the block, not the head:
	// the window it asked for starts past line 300.
	calls := src.readCalls()
	if len(calls) != 1 {
		t.Fatalf("read %d windows, want exactly 1", len(calls))
	}
	if calls[0].start < 300 {
		t.Errorf("the model read from line %d; the marker is at 397 — the total did not reach it", calls[0].start)
	}
	_ = f
}
