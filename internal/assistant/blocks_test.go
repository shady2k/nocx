package assistant

// Session-item tool execution and backend source fixtures.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
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
func toolsDirFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(realToolsFS)
}

// newClientWithTestToolsFS keeps filesystem-backed schema fixtures on the
// production client assembly path.
func TestMiddleware_BlocksWithoutSourceIsHonest(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, nil)
	_, err := wrappedEndpoint(mw, "session.list", "call-1", `{}`)
	if err == nil {
		t.Fatal("session.list with no session source succeeded; want an honest failure")
	}
	if !strings.Contains(err.Error(), "no session source") {
		t.Errorf("error = %v, want it to name the missing seam", err)
	}
}

// The session resource is resolved from the run's grant, so the model cannot
// select another pane. An explicit sessionId remains invalid input.
func TestMiddleware_BlocksUseGrantSessionAndRejectModelSessionID(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	src := &fakeBlocks{items: SessionItems{Items: []SessionItem{{ID: "blk-1", Command: "ls", State: "exited"}}}}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, &blocksOnlyRequester{blocks: src})

	out, err := wrappedEndpoint(mw, "session.list", "call-1", `{}`)
	if err != nil {
		t.Fatalf("session.list without sessionId: %v", err)
	}
	if !strings.Contains(out, "blk-1") {
		t.Fatalf("result %s does not carry the block", out)
	}
	if calls := src.listCalls(); len(calls) != 1 || calls[0].sessionID != "session-a" {
		t.Fatalf("the ledger calls were %v; want one call for session-a", calls)
	}

	if _, err := wrappedEndpoint(mw, "session.list", "call-2", `{"sessionId":"session-a"}`); err == nil {
		t.Fatal("session.list with sessionId succeeded; want schema refusal")
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

func newClientWithTestToolsFS(logger log.Logger, toolsFS fs.FS, floor content.Floor) (Client, error) {
	reg, err := assembleToolRegistry(toolsFS)
	if err != nil {
		return nil, err
	}
	return newClientWithRegistry(logger, reg, floor), nil
}
