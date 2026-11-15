package panedriver

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
)

func TestConsentRetryDoesNotReplaceItsOwnOpeningShell(t *testing.T) {
	for _, releaseBeforeBind := range []bool{true, false} {
		name := "release after retry bind"
		if releaseBeforeBind {
			name = "release before retry bind"
		}
		t.Run(name, func(t *testing.T) {
			_, logger := logtest.New(t)
			d := New("coordinator", logger)
			d.Bind("pane", "connection")
			d.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "connection", Seq: 1, State: proto.ConnectionConnected})
			release := func() {
				d.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "connection", Seq: 2, State: proto.ConnectionEnded, Cause: "released"})
			}
			if releaseBeforeBind {
				release()
			}
			d.Bind("pane", "pending:pane")
			d.Bind("pane", "connection")
			if !releaseBeforeBind {
				release()
			}
			d.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "connection", Seq: 3, State: proto.ConnectionConnected})
			if d.panes["pane"].replacing {
				t.Fatal("consent retry launched a replacement alongside its foreground open")
			}
		})
	}
}

func TestForegroundRetryDoesNotInheritEarlierConnectionFailure(t *testing.T) {
	_, logger := logtest.New(t)
	d := New("coordinator", logger)
	d.Bind("pane", "connection")
	d.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "connection", Seq: 1, State: proto.ConnectionFailed, Cause: "auth-rejected"})
	d.Bind("pane", "connection")
	d.ApplyFrom("helper", proto.ConnectionStateEvent{ConnectionID: "connection", Seq: 2, State: proto.ConnectionConnected})
	if d.panes["pane"].replacing {
		t.Fatal("foreground retry inherited a previous attempt's replacement obligation")
	}
}
