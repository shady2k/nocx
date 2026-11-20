package app

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// notifyingConnectionsService answers `connections` the way sshsvc does: the
// subscription is installed first and delivers each supervisor's current state
// as a notification BEFORE the op's response is written.
type notifyingConnectionsService struct{ summary proto.ConnectionSummary }

func (s *notifyingConnectionsService) Name() string { return proto.ServiceSSH }
func (s *notifyingConnectionsService) Ops() []string {
	return []string{proto.OpConnections, "reader-ping"}
}
func (s *notifyingConnectionsService) ParamsSchema(string) *helperhost.Schema {
	return helperhost.SchemaFor(proto.ConnectionsParams{})
}

func (s *notifyingConnectionsService) Call(ctx context.Context, op string, _ json.RawMessage) (any, error) {
	if op == "reader-ping" {
		return struct{}{}, nil
	}
	conn, _ := helperhost.ConnectionFrom(ctx).(*helperhost.Host)
	_ = conn.SendNotification(proto.Notification{
		Service: proto.ServiceSSH,
		Event:   proto.EventConnectionState,
		Params:  s.summary.Connection,
	})
	return proto.ConnectionsResult{Connections: []proto.ConnectionSummary{s.summary}}, nil
}

func TestReviewALX687SyncWithSubscriptionEventsDoesNotDeadlock(t *testing.T) {
	logger := logtest.Slog(t)
	source := newFarHelperConnections()
	service := &notifyingConnectionsService{summary: farHelperSummary("live", 3, 1, proto.ConnectionConnected, 1, "live.example", "dev", "SHA256:live")}
	const generation = "generation-under-test"
	serverConn, clientConn := net.Pipe()
	peer := helperhost.New(serverConn, serverConn, generation, "instance-1", logger)
	peer.Register(service)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background()) }()
	notified := make(chan struct{}, 1)
	client, err := helperclient.Dial(context.Background(), helperclient.Config{
		Exec:        helperclient.NewSocketConn(clientConn),
		ExpectHash:  generation,
		SentinelTTL: time.Second,
		Log:         logger,
		OnConnectionEvent: func(instance string, ev proto.ConnectionStateEvent) {
			notified <- struct{}{}
			source.Apply(instance, ev)
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = client.Close()
		if err := <-serveDone; err != nil {
			t.Errorf("helper serve: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	synced := make(chan error, 1)
	go func() { synced <- source.Sync(ctx, client) }()
	select {
	case <-notified:
	case <-ctx.Done():
		t.Fatal("helper did not send the pre-response observation")
	}
	// A reader blocked in Apply cannot dispatch either the table response
	// or this independent request. This budget is below ssh.close's 2s
	// courtesy timeout: passing must prove progress, not timeout recovery.
	pingCtx, stopPing := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stopPing()
	var answer struct{}
	pinged := make(chan error, 1)
	go func() {
		pinged <- client.Call(pingCtx, proto.ServiceSSH, "reader-ping", struct{}{}, &answer)
	}()
	select {
	case err := <-pinged:
		if err != nil {
			t.Fatalf("parallel Call while the table subscribes: %v", err)
		}
	case <-pingCtx.Done():
		t.Fatal("parallel Call blocked behind the pre-response observation")
	}
	if err := <-synced; err != nil {
		t.Fatalf("Sync with the helper's subscription events in flight: %v", err)
	}
	if got := farHelperListed(source)["live"]; got.Seq != 3 || got.Destination.Host != "live.example" {
		t.Fatalf("synced connection = %+v, want table destination and seq 3", got)
	}
}
