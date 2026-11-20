package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/waittest"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// notifyingConnectionsService answers `connections` the way sshsvc does: the
// subscription is installed first and delivers each supervisor's current state
// as a notification BEFORE the op's response is written.
type notifyingConnectionsService struct {
	summary proto.ConnectionSummary
	event   *proto.ConnectionStateEvent
}

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
	event := s.summary.Connection
	if s.event != nil {
		event = *s.event
	}
	_ = conn.SendNotification(proto.Notification{
		Service: proto.ServiceSSH,
		Event:   proto.EventConnectionState,
		Params:  event,
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

// readerLaneService bridges a real far helper host over the local client's
// actual ChannelStream/LaneConn, including the ssh.close RPC on that client.
type readerLaneService struct {
	id       proto.ChannelID
	bridge   net.Conn
	closed   chan proto.ChannelID
	stop     sync.Once
	pumpDone chan struct{}
}

func (*readerLaneService) Name() string  { return proto.ServiceSSH }
func (*readerLaneService) Ops() []string { return []string{proto.OpLane, proto.OpClose, "reader-ping"} }

func (*readerLaneService) ParamsSchema(op string) *helperhost.Schema {
	switch op {
	case proto.OpLane:
		return helperhost.SchemaFor(proto.LaneParams{})
	case proto.OpClose:
		return helperhost.SchemaFor(proto.CloseChannelParams{})
	default:
		return helperhost.SchemaFor(struct{}{})
	}
}

func (s *readerLaneService) Call(_ context.Context, op string, raw json.RawMessage) (any, error) {
	switch op {
	case proto.OpLane:
		return proto.OpenChannelResult{Channel: s.id}, nil
	case proto.OpClose:
		var params proto.CloseChannelParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		s.closed <- params.Channel
		s.stop.Do(func() { _ = s.bridge.Close() })
	}
	return struct{}{}, nil
}

func (s *readerLaneService) ChannelData(_ context.Context, frame proto.ChannelFrame) {
	if frame.Channel == s.id {
		_, _ = s.bridge.Write(frame.Payload)
	}
}

func (s *readerLaneService) ResponseWritten(ctx context.Context, op string, _ any, _ helperhost.AnswerDelivery) {
	if op != proto.OpLane {
		return
	}
	peer, ok := helperhost.ConnectionFrom(ctx).(*helperhost.Host)
	if !ok {
		panic("reader lane fixture requires helper host connection")
	}
	go func() {
		defer close(s.pumpDone)
		buf := make([]byte, 32768)
		for {
			n, err := s.bridge.Read(buf)
			if n > 0 {
				if sendErr := peer.SendChannelData(proto.ChannelFrame{Channel: s.id, Payload: buf[:n]}); sendErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

type readerLaneDialer struct {
	local *helperclient.Client
	log   *slog.Logger
}

func (d readerLaneDialer) Dial(ctx context.Context, target farhelper.Target, installed farhelper.Installed) (*helperclient.Client, error) {
	lane, err := d.local.OpenLane(ctx, proto.LaneParams{Destination: target.Destination, Generation: proto.GenerationID(installed.Generation)})
	if err != nil {
		return nil, err
	}
	peer, err := helperclient.Dial(ctx, helperclient.Config{Exec: lane, ExpectHash: installed.Generation, Log: d.log})
	if err != nil {
		_ = lane.Close()
	}
	return peer, err
}

func TestFarHelperLaneLossDoesNotBlockLocalReader(t *testing.T) {
	for _, state := range []string{proto.ConnectionStalled, proto.ConnectionEnded} {
		t.Run(state, func(t *testing.T) {
			logger := logtest.Slog(t)
			source := newFarHelperConnections()
			channel, err := proto.ParseChannelID("0000000000000000000000000000000a")
			if err != nil {
				t.Fatal(err)
			}
			farConn, bridge := net.Pipe()
			farHost := helperhost.New(farConn, farConn, "installed", "far-instance", logger)
			farDone := make(chan error, 1)
			go func() { farDone <- farHost.Serve(context.Background()) }()
			service := &readerLaneService{id: channel, bridge: bridge, closed: make(chan proto.ChannelID, 1), pumpDone: make(chan struct{})}
			serverConn, clientConn := net.Pipe()
			localHost := helperhost.New(serverConn, serverConn, "local-generation", "local-instance", logger)
			localHost.Register(service)
			localDone := make(chan error, 1)
			go func() { localDone <- localHost.Serve(context.Background()) }()
			notified := make(chan struct{}, 2)
			local, err := helperclient.Dial(context.Background(), helperclient.Config{
				Exec: helperclient.NewSocketConn(clientConn), ExpectHash: "local-generation", Log: logger,
				OnConnectionEvent: func(instance string, ev proto.ConnectionStateEvent) {
					notified <- struct{}{}
					source.Apply(instance, ev)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = clientConn.Close()
				_ = local.Close()
				service.stop.Do(func() { _ = bridge.Close() })
				_ = farConn.Close()
				<-service.pumpDone
				if err := <-localDone; err != nil {
					t.Errorf("local host: %v", err)
				}
				if err := <-farDone; err != nil && err != io.ErrClosedPipe {
					t.Errorf("far host: %v", err)
				}
			})
			registry := farhelper.New(farhelper.Config{
				Connections: source, Modes: &reviewModeStore{mode: hostmode.Helper}, Consents: &reviewConsentStore{},
				Probe: reviewProber{}, Install: &reviewInstall{}, Dial: readerLaneDialer{local: local, log: logger}, Log: farHelperTestLogger(t),
			})
			defer registry.Close()
			source.Observe(farhelper.Connection{ID: "live", Destination: proto.SSHDestination{Host: "host.example", Port: 22, User: "dev"}, HostKeyFingerprint: "SHA256:live", State: hostconn.Connected, Generation: 1, Seq: 1})
			waittest.WaitFor(t, "far helper serving over local lane", func() bool { axis, _ := registry.Axis("live"); return axis.State == farhelper.StateServing })
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if err := localHost.SendNotification(proto.Notification{Service: proto.ServiceSSH, Event: proto.EventConnectionState, Params: proto.ConnectionStateEvent{ConnectionID: "live", Seq: 2, Generation: 1, State: state}}); err != nil {
				t.Fatal(err)
			}
			<-notified
			pinged := make(chan error, 1)
			go func() {
				var answer struct{}
				pinged <- local.Call(ctx, proto.ServiceSSH, "reader-ping", struct{}{}, &answer)
			}()
			select {
			case err := <-pinged:
				if err != nil {
					t.Fatalf("parallel local Call: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("local reader blocked while closing far lane")
			}
			select {
			case got := <-service.closed:
				if got != channel {
					t.Fatalf("closed %v, want lane %v", got, channel)
				}
			case <-ctx.Done():
				t.Fatal("ssh.close did not reach the local helper")
			}
			if state == proto.ConnectionStalled {
				if err := localHost.SendNotification(proto.Notification{Service: proto.ServiceSSH, Event: proto.EventConnectionState, Params: proto.ConnectionStateEvent{ConnectionID: "live", Seq: 3, Generation: 1, State: proto.ConnectionEnded}}); err != nil {
					t.Fatal(err)
				}
				<-notified
				go func() {
					var answer struct{}
					pinged <- local.Call(ctx, proto.ServiceSSH, "reader-ping", struct{}{}, &answer)
				}()
				select {
				case err := <-pinged:
					if err != nil {
						t.Fatalf("parallel Call after ended: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("local reader blocked after stalled then ended")
				}
				waittest.WaitFor(t, "stalled connection removed after ended", func() bool { _, ok := registry.Axis("live"); return !ok })
			}
		})
	}
}

func TestFarHelperNewerSubscriptionEventWinsOverUnknownTableEntry(t *testing.T) {
	for _, state := range []string{proto.ConnectionStalled, proto.ConnectionEnded} {
		t.Run(state, func(t *testing.T) {
			source := newFarHelperConnections()
			service := &notifyingConnectionsService{
				summary: farHelperSummary("unknown", 3, 1, proto.ConnectionConnected, 1, "far.example", "dev", "SHA256:far"),
				event:   &proto.ConnectionStateEvent{ConnectionID: "unknown", Seq: 4, Generation: 1, State: state},
			}
			logger := logtest.Slog(t)
			serverConn, clientConn := net.Pipe()
			peer := helperhost.New(serverConn, serverConn, "generation-under-test", "instance-1", logger)
			peer.Register(service)
			done := make(chan error, 1)
			go func() { done <- peer.Serve(context.Background()) }()
			client, err := helperclient.Dial(context.Background(), helperclient.Config{Exec: helperclient.NewSocketConn(clientConn), ExpectHash: "generation-under-test", Log: logger, OnConnectionEvent: source.Apply})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = clientConn.Close()
				_ = client.Close()
				if err := <-done; err != nil {
					t.Errorf("helper: %v", err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := source.Sync(ctx, client); err != nil {
				t.Fatal(err)
			}
			got, exists := farHelperListed(source)["unknown"]
			if state == proto.ConnectionEnded {
				if exists {
					t.Fatalf("ended unknown connection unfolded from stale table: %+v", got)
				}
				// A later table read need not repeat the subscription event.
				stale := &farHelperTableStub{instance: "instance-1", summaries: []proto.ConnectionSummary{service.summary}}
				if err := source.Sync(ctx, stale); err != nil {
					t.Fatal(err)
				}
				if resurrected, ok := farHelperListed(source)["unknown"]; ok {
					t.Fatalf("second stale table resurrected ended connection: %+v", resurrected)
				}
				return
			}
			if !exists || got.Seq != 4 || got.State != hostconn.Stalled || got.Destination.Host != "far.example" {
				t.Fatalf("connection = %+v exists=%v; want newer stalled event with table destination", got, exists)
			}
		})
	}
}
