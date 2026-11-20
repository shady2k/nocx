//go:build nocx_local_ssh

package sshsvc

// A FAILED WRITE ENDS THE SUBSCRIPTION IT BELONGED TO, AND NOTHING ELSE
// (nocx-oalo1.6.2, the plan's second risk).
//
// The `connections` op changed what a subscription covers: one `connection`
// subscription is one connection, the table's is every connection this
// coordinator has. The failed write is the one event that says nobody is
// listening, so it releases holds — and which holds it may release is exactly
// the set the subscription covered. Releasing another connection's hold ends a
// connection a person's pane is still riding, and the driver reads that as
// `ended`: the pane's binding is dropped and a connection the person never lost
// is reported as lost.

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/connkey"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

// brokenWire is the coordinator connection whose writes always fail: the state
// of a client that went away between two observations.
type brokenWire struct{}

func (brokenWire) Read([]byte) (int, error)  { return 0, io.EOF }
func (brokenWire) Write([]byte) (int, error) { return 0, errors.New("the coordinator is gone") }

// holdClient answers the two calls these tests use, and refuses nothing: the
// embedded interface is the production one, so a call this fake does not expect
// panics rather than pretending to work.
type holdClient struct {
	Client
	released []string
}

func (c *holdClient) WatchConnection(_ connkey.Key, fn func(hostconn.Facts)) func() {
	// THE FIRST OBSERVATION IS DELIVERED SYNCHRONOUSLY, as the production
	// carrier does, so a write that fails fails inside the installation.
	fn(hostconn.Facts{Key: testKey(), Seq: 1})
	return func() {}
}

func (c *holdClient) WatchConnections(fn func(hostconn.Facts)) func() {
	fn(hostconn.Facts{Key: testKey(), Seq: 1})
	return func() {}
}

func (c *holdClient) RetainConnection(key connkey.Key, _ string) func() {
	return func() { c.released = append(c.released, key.ID()) }
}

func testKey() connkey.Key {
	return ssh.PooledSpec{Host: "host.example", Port: 22, User: "dev", Identity: "principal"}.Key()
}

func otherKey() connkey.Key {
	return ssh.PooledSpec{Host: "host.example", Port: 22, User: "dev", Identity: "another"}.Key()
}

// TestAFailedConnectionObservationReleasesOnlyThatConnectionsHolds is the
// per-connection half.
func TestAFailedConnectionObservationReleasesOnlyThatConnectionsHolds(t *testing.T) {
	client := &holdClient{}
	s := New(client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conn := host.New(brokenWire{}, brokenWire{}, "hash", "instance", slog.New(slog.NewTextHandler(io.Discard, nil)))

	s.retainPane(conn, "pane-a", testKey(), "auto")
	s.retainPane(conn, "pane-b", otherKey(), "auto")

	s.ensureConnectionWatch(conn, testKey())

	if len(client.released) != 1 || client.released[0] != testKey().ID() {
		t.Fatalf("a failed observation released %v, want only %s", client.released, testKey().ID())
	}
	s.mu.Lock()
	_, otherHeld := s.paneHolds[paneHoldKey{conn, "pane-b"}]
	_, ownHeld := s.paneHolds[paneHoldKey{conn, "pane-a"}]
	s.mu.Unlock()
	if ownHeld {
		t.Fatal("the hold whose subscription failed is still held: nobody is listening for it any more")
	}
	if !otherHeld {
		t.Fatal("a hold on ANOTHER connection was released because this one's event could not be written")
	}
}

// TestAFailedTableObservationReleasesEveryHoldThatCoordinatorHad is the other
// half, and it is deliberately broader: the subscription covered every
// connection, so a write that fails is a coordinator that is gone.
func TestAFailedTableObservationReleasesEveryHoldThatCoordinatorHad(t *testing.T) {
	client := &holdClient{}
	s := New(client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	conn := host.New(brokenWire{}, brokenWire{}, "hash", "instance", slog.New(slog.NewTextHandler(io.Discard, nil)))

	s.retainPane(conn, "pane-a", testKey(), "auto")
	s.retainPane(conn, "pane-b", otherKey(), "auto")

	s.ensureConnectionsWatch(conn)

	if len(client.released) != 2 {
		t.Fatalf("a failed table observation released %v, want both holds: the subscription covered both", client.released)
	}
	s.mu.Lock()
	remaining := len(s.paneHolds)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d hold(s) survived a coordinator nobody can write to", remaining)
	}
}
