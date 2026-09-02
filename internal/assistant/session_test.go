package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

func testToolBound() agenttools.ResultBound {
	return agenttools.ResultBound{MaxBytes: 64 << 10, Truncation: agenttools.TruncationDropTail}
}

func toolTestContext() context.Context {
	return withToolBound(context.Background(), testToolBound())
}

func testResultMaxBytes() int64 {
	return testToolBound().MaxBytes
}

type sessionSourceFake struct {
	items     SessionItems
	item      SessionItemRead
	err       error
	calls     int
	sessionID string
}

func (f *sessionSourceFake) ListSessionItems(_ context.Context, sessionID string, _ int) (SessionItems, error) {
	f.calls++
	f.sessionID = sessionID
	if f.err != nil {
		return SessionItems{}, f.err
	}
	return f.items, nil
}

func (f *sessionSourceFake) ReadSessionItem(context.Context, string, string, int, int) (SessionItemRead, error) {
	f.calls++
	if f.err != nil {
		return SessionItemRead{}, f.err
	}
	return f.item, nil
}

type sessionScreenRequester struct {
	body json.RawMessage
	err  error
}

func (r sessionScreenRequester) RequestScreen(context.Context, string, *FrameRegion) (json.RawMessage, error) {
	return r.body, r.err
}

func (r sessionScreenRequester) RequestRun(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func TestExecuteSessionList_EmptyPaneIsAnHonestEmptyResult(t *testing.T) {
	source := &sessionSourceFake{items: SessionItems{Items: []SessionItem{}}}
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)

	out, err := executeSessionList(toolTestContext(), reader, source, json.RawMessage(`{"sessionId":"pane-a"}`))
	if err != nil {
		t.Fatalf("executeSessionList: %v", err)
	}
	if !strings.Contains(out, `"items":[]`) {
		t.Fatalf("empty session result = %s, want items=[]", out)
	}
	if source.calls != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls)
	}
}

func TestMiddleware_SessionListUsesPaneSessionWithoutModelSessionID(t *testing.T) {
	grant := sessionGrant("pane-a", autonomousMatrix())
	source := &sessionSourceFake{items: SessionItems{Items: []SessionItem{}}}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, &blocksOnlyRequester{blocks: source})

	out, err := wrappedEndpoint(mw, "session.list", "call-1", `{}`)
	if err != nil {
		t.Fatalf("session.list without sessionId: %v", err)
	}
	if !strings.Contains(out, `"items":[]`) {
		t.Fatalf("session.list result = %s, want empty items", out)
	}
	if source.calls != 1 || source.sessionID != "pane-a" {
		t.Fatalf("session source saw calls=%d session=%q, want one call for pane-a", source.calls, source.sessionID)
	}
}

func TestExecuteSessionList_PropagatesSourceFailure(t *testing.T) {
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)
	_, err := executeSessionList(toolTestContext(), reader, &sessionSourceFake{err: errors.New("ledger unavailable")}, json.RawMessage(`{"sessionId":"pane-a"}`))
	if err == nil || !strings.Contains(err.Error(), "ledger unavailable") {
		t.Fatalf("list error = %v, want source failure", err)
	}
}

func TestExecuteSessionRead_ExitedCarriesStateAndCode(t *testing.T) {
	source := &sessionSourceFake{item: SessionItemRead{ID: "item-1", State: "exited", ExitCode: intPtr(7), Text: "done"}}
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)

	out, err := executeSessionRead(toolTestContext(), reader, source, sessionScreenRequester{}, json.RawMessage(`{"sessionId":"pane-a","id":"item-1"}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	if !strings.Contains(out, `"state":"exited"`) || !strings.Contains(out, `"exitCode":7`) || !strings.Contains(out, `"text":"done"`) {
		t.Fatalf("exited result = %s, want state, code and text", out)
	}
}

func TestExecuteSessionRead_ExitedNoBodyCarriesRetentionNote(t *testing.T) {
	source := &sessionSourceFake{item: SessionItemRead{ID: "item-1", State: "exited", Note: "output was not kept"}}
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)

	out, err := executeSessionRead(toolTestContext(), reader, source, sessionScreenRequester{}, json.RawMessage(`{"sessionId":"pane-a","id":"item-1"}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	if !strings.Contains(out, `"state":"exited"`) || !strings.Contains(out, `"note":"output was not kept"`) {
		t.Fatalf("exited result = %s, want state and retention note", out)
	}
}

func TestExecuteSessionRead_RunningUsesRendererAndCarriesState(t *testing.T) {
	source := &sessionSourceFake{item: SessionItemRead{ID: "item-1", State: "running"}}
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)
	req := sessionScreenRequester{body: liveFrameBody("current")}

	out, err := executeSessionRead(toolTestContext(), reader, source, req, json.RawMessage(`{"sessionId":"pane-a","id":"item-1"}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	if !strings.Contains(out, `"state":"running"`) || !strings.Contains(out, `"text":"current"`) {
		t.Fatalf("running result = %s, want running state and live text", out)
	}
}

func TestExecuteSessionRead_AutomaticItemUsesRendererWithoutLedgerRow(t *testing.T) {
	source := &sessionSourceFake{err: errors.New("item not found")}
	reader := agenttools.NewSessionReader(
		[]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}},
		[]string{"att-shell"},
		nil,
	)
	req := sessionScreenRequester{body: liveFrameBody("current screen")}

	out, err := executeSessionRead(toolTestContext(), reader, source, req, json.RawMessage(`{"id":"att-shell"}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	if !strings.Contains(out, `"state":"running"`) || !strings.Contains(out, `"text":"current screen"`) {
		t.Fatalf("automatic result = %s, want renderer screen", out)
	}
	if source.calls != 0 {
		t.Fatalf("source calls = %d, want no ledger read for renderer-owned item", source.calls)
	}
}

func TestExecuteSessionRead_NoIDReturnsCurrentScreenAndAlternateCaveat(t *testing.T) {
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)
	body := liveFrameBody("fullscreen")
	var frame map[string]any
	if err := json.Unmarshal(body, &frame); err != nil {
		t.Fatal(err)
	}
	identity, ok := frame["identity"].(map[string]any)
	if !ok {
		t.Fatal("frame identity is not an object")
	}
	buffer, ok := identity["buffer"].(map[string]any)
	if !ok {
		t.Fatal("frame buffer is not an object")
	}
	buffer["kind"] = "alternate"
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal alternate frame: %v", err)
	}
	body = encoded

	out, err := executeSessionRead(toolTestContext(), reader, nil, sessionScreenRequester{body: body}, json.RawMessage(`{"sessionId":"pane-a"}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	if !strings.Contains(out, `"state":"screen"`) || !strings.Contains(out, "current screen, not accumulated output") {
		t.Fatalf("screen result = %s, want screen state and alternate-buffer caveat", out)
	}
}

func TestExecuteSessionRead_PropagatesLedgerAndRendererFailures(t *testing.T) {
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)
	ledgerErr := errors.New("ledger unavailable")
	if _, err := executeSessionRead(toolTestContext(), reader, &sessionSourceFake{err: ledgerErr}, sessionScreenRequester{}, json.RawMessage(`{"sessionId":"pane-a","id":"item-1"}`)); !strings.Contains(err.Error(), "ledger unavailable") {
		t.Fatalf("ledger error = %v, want source failure", err)
	}
	rendererErr := errors.New("renderer disappeared")
	source := &sessionSourceFake{item: SessionItemRead{ID: "item-1", State: "running"}}
	if _, err := executeSessionRead(toolTestContext(), reader, source, sessionScreenRequester{err: rendererErr}, json.RawMessage(`{"sessionId":"pane-a","id":"item-1"}`)); !strings.Contains(err.Error(), "renderer disappeared") {
		t.Fatalf("renderer error = %v, want renderer failure", err)
	}
}

func TestExecuteSessionRead_ExitedBoundsTextAndReturnedEnd(t *testing.T) {
	const line = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = line
	}
	text := strings.Join(lines, "\n")
	expectedLines := int(testResultMaxBytes()) / (len(line) + 1)
	expectedText := strings.Join(lines[:expectedLines], "\n")
	source := &sessionSourceFake{item: SessionItemRead{
		ID: "item-1", State: "exited", Total: len(lines), Start: 0, End: len(lines), Text: text,
	}}
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)

	out, err := executeSessionRead(toolTestContext(), reader, source, sessionScreenRequester{}, json.RawMessage(`{"sessionId":"pane-a","id":"item-1","count":2000}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	var result sessionReadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.Truncated || result.Dropped <= 0 || result.Remaining != result.Dropped {
		t.Fatalf("truncation metadata = truncated:%v dropped:%d remaining:%d, want omitted bytes reported consistently", result.Truncated, result.Dropped, result.Remaining)
	}
	if result.Text != expectedText {
		t.Fatalf("text has %d bytes, want %d bytes ending on line %d", len(result.Text), len(expectedText), expectedLines)
	}
	if result.Returned.End != expectedLines {
		t.Fatalf("returned end = %d, want %d for %d carried lines", result.Returned.End, expectedLines, expectedLines)
	}
}

func TestExecuteSessionRead_LiveScreenBoundsTextAndReturnedEnd(t *testing.T) {
	const line = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = line
	}
	expectedLines := int(testResultMaxBytes()) / (len(line) + 1)
	expectedText := strings.Join(lines[:expectedLines], "\n")
	reader := agenttools.NewSessionReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}}, nil, nil)

	out, err := executeSessionRead(toolTestContext(), reader, nil, sessionScreenRequester{body: liveFrameBody(lines...)}, json.RawMessage(`{"sessionId":"pane-a"}`))
	if err != nil {
		t.Fatalf("executeSessionRead: %v", err)
	}
	var result sessionReadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.Truncated || result.Dropped <= 0 || result.Remaining != result.Dropped {
		t.Fatalf("truncation metadata = truncated:%v dropped:%d remaining:%d, want omitted bytes reported consistently", result.Truncated, result.Dropped, result.Remaining)
	}
	if result.Text != expectedText {
		t.Fatalf("text has %d bytes, want %d bytes ending on line %d", len(result.Text), len(expectedText), expectedLines)
	}
	if result.Returned.End != expectedLines {
		t.Fatalf("returned end = %d, want %d for %d carried lines", result.Returned.End, expectedLines, expectedLines)
	}
}

func intPtr(v int) *int { return &v }

// A ROW MARK INSIDE THE FROZEN SCREEN BOUNDS ITS READ (nocx-hp8p2.7).
// The screen a summon attaches is a renderer-owned item; marking rows in it
// narrows THAT item — the same id plus a span — so the model reads the band
// a person chose rather than the whole screen. The mark is authority, the
// same way it is for a block (nocx-hp8p2.15), and a window the model asks
// for itself is still honoured.
func TestExecuteSessionRead_AutomaticItemIsBoundedByItsMark(t *testing.T) {
	tests := []struct {
		name string
		args string
		want *FrameRegion
	}{
		{
			name: "a read naming only the item is answered inside the mark",
			args: `{"id":"att-shell"}`,
			want: &FrameRegion{Start: 3, End: 5},
		},
		{
			name: "a window the model asked for is honoured",
			args: `{"id":"att-shell","start":10,"count":1}`,
			want: &FrameRegion{Start: 10, End: 11},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := agenttools.NewSessionReader(
				[]content.GrantScope{{Kind: content.ResourceSession, ID: "pane-a"}},
				[]string{"att-shell"},
				[]agenttools.MarkedSessionWindow{{ItemID: "att-shell", Start: 3, Count: 2}},
			)
			req := &recordingRequester{body: liveFrameBody("marked band")}
			source := &sessionSourceFake{err: errors.New("item not found")}

			if _, err := executeSessionRead(toolTestContext(), reader, source, req, json.RawMessage(tc.args)); err != nil {
				t.Fatalf("executeSessionRead: %v", err)
			}
			calls := req.calls()
			if len(calls) != 1 {
				t.Fatalf("renderer calls = %d, want 1", len(calls))
			}
			got := calls[0].region
			if got == nil || *got != *tc.want {
				t.Fatalf("region = %v, want %v", got, tc.want)
			}
		})
	}
}
