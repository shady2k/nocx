package transport

// Every failure before the real bind settles the opening the transport
// published for the pane (round 3, finding: publication without settlement).
// The three failures are the three doors before the helper names a
// connection: the helper will not start, the destination will not resolve,
// and the connection cannot be named. They all reach the same opener error,
// and each is staged here rather than trusting one representative.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

type failingHostedOpener struct{ err error }

func (o *failingHostedOpener) OpenHosted(context.Context, session.Config, string) (HostedSessionOpen, bool, error) {
	return HostedSessionOpen{}, false, o.err
}

type openingOwner struct{ driver *panedriver.Driver }

func (o openingOwner) PaneOpening(paneID string)       { o.driver.Bind(paneID, "pending:"+paneID) }
func (o openingOwner) PaneOpeningFailed(paneID string) { o.driver.FailOpening(paneID) }
func (o openingOwner) PaneClosed(paneID string)        { o.driver.PaneClosed(paneID) }

type snapshotSink struct {
	mu   sync.Mutex
	list []panedriver.Snapshot
}

func (s *snapshotSink) add(snap panedriver.Snapshot) {
	s.mu.Lock()
	s.list = append(s.list, snap)
	s.mu.Unlock()
}

func (s *snapshotSink) all() []panedriver.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]panedriver.Snapshot(nil), s.list...)
}

func TestAFailedOpenSettlesTheOpeningItPublished(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"helper startup", errors.New("helper: unreachable")},
		{"destination resolution", errors.New("resolve the ssh pane's destination: no route")},
		{"connection naming", errors.New("name the ssh pane's connection: helper refused")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, logger := logtest.New(t)
			helper := &failingHostedOpener{err: tc.err}
			driver := panedriver.New("instance-1", logger)
			sink := &snapshotSink{}
			driver.SetPublisher(sink.add)
			ws := NewWSServer(logger, session.New(logger, nil),
				WithHelperSessionOpener(helper),
				WithPaneConnectionPolicy(openingOwner{driver: driver}),
				WithProfileResolver(&fakeResolver{resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
					return "remote.example", &ssh.ConnectConfig{User: "alice"}, nil
				}}))
			if err := ws.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ws.Stop(ctx) }()
			h := openHandlers{opener: ws.opener, sess: ws, log: logger}
			params := openParams{PaneID: helperOpenPane, Kind: "ssh", ProfileID: "saved", Cols: 80, Rows: 24}
			if _, err := h.openInto(ctx, &wsConn{}, newConnState(), params, func(openResult) {}); err == nil {
				t.Fatal("expected the open to fail")
			}
			snaps := sink.all()
			if len(snaps) != 2 {
				t.Fatalf("%d observations, want the opening and its settlement", len(snaps))
			}
			if snaps[0].Transport.State != "opening" {
				t.Fatalf("first observation %+v, want opening", snaps[0].Transport)
			}
			last := snaps[len(snaps)-1]
			if last.Transport.State != "ended" || last.Transport.Cause != "lost-link" {
				t.Fatalf("settlement %+v, want ended/lost-link", last.Transport)
			}
			if last.Reconnect == nil || last.Reconnect.EndsShells != 0 {
				t.Fatalf("settlement offer %+v, want a reconnect offer", last.Reconnect)
			}
		})
	}
}
