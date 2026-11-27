package transport

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

type bindingContextOpener struct{ ctx context.Context }

func (o *bindingContextOpener) OpenHosted(ctx context.Context, _ session.Config, _ string) (HostedSessionOpen, bool, error) {
	o.ctx = ctx
	return HostedSessionOpen{}, true, context.Canceled
}

func TestOldOpenCannotBindReplacementPaneRecord(t *testing.T) {
	ctx, logger := logtest.New(t)
	helper := &bindingContextOpener{}
	ws := NewWSServer(logger, session.New(logger, nil), WithHelperSessionOpener(helper), WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
		return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
	}}))
	if err := ws.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	wc := &wsConn{}
	params := openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
	h := openHandlers{opener: ws.opener, sess: ws, log: logger}
	if _, err := h.openInto(ctx, wc, newConnState(), params, func(openResult) error { return nil }); err == nil {
		t.Fatal("expected open refusal")
	}
	if helper.ctx == nil {
		t.Fatal("open never reached helper")
	}
	BindPaneConnection(helper.ctx, 11)
	old := ws.paneRecordOf(helperOpenPane)
	if old.bindingGeneration != 11 {
		t.Fatal("open failed to associate its binding")
	}
	ws.bindPane(helperOpenPane, wc, newConnState(), params)
	current := ws.paneRecordOf(helperOpenPane)
	BindPaneConnection(helper.ctx, 12)
	if current.bindingGeneration != 0 {
		t.Fatal("obsolete open stamped the replacement record")
	}
}
