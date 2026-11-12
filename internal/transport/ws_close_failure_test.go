package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
)

type failingEndChannel struct{ *exitEndableChannel }

func (c *failingEndChannel) EndSession(context.Context) error {
	return errors.New("helper termination refused")
}

type failingEndOpener struct{ reg *session.Reg }

func (o failingEndOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	sess, err := o.reg.Adopt(ctx, cfg, session.ID("0123456789abcdef0123456789abcdef"), &failingEndChannel{newExitEndableChannel()})
	return HostedSessionOpen{Session: sess}, true, err
}

func TestCloseReportsHelperTerminationFailureAfterCleanup(t *testing.T) {
	ctx, logger := logtest.New(t)
	reg := newRegWithStub(logger)
	ws := NewWSServer(logger, reg, WithHelperSessionOpener(failingEndOpener{reg}))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	opened := jsonrpcCall(t, conn, "open", map[string]any{"cols": 80, "rows": 24, "paneId": helperOpenPane})
	var result struct {
		SessionID string `json:"sessionId"`
	}
	decodeJSONRPCResult(t, opened, &result)
	awaitSubscriber(t, ws, session.ID(result.SessionID))
	response := jsonrpcCall(t, conn, "close", map[string]any{"sessionId": result.SessionID})
	var answer struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(response, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Error == nil || answer.Error.Code != -32603 {
		t.Fatalf("termination failure must be reported, got %s", response)
	}
	if _, err := reg.Get(session.ID(result.SessionID)); err == nil {
		t.Fatal("failed termination left coordinator registry claim alive")
	}
}
