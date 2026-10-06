package host

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestMetadataHintTimeoutDoesNotPoisonLaterCriticalCarrierWrite(t *testing.T) {
	connection, peer := net.Pipe()
	t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
	host := New(connection, connection, "generation", "instance", slog.New(slog.NewTextHandler(io.Discard, nil)))
	finished := make(chan bool, 1)
	go func() {
		finished <- host.TrySendNotification(proto.Notification{Service: proto.ServiceSession, Event: "metadata-hint", Params: struct{}{}}, time.Now().Add(20*time.Millisecond))
	}()
	select {
	case delivered := <-finished:
		if delivered {
			t.Fatal("hint delivered to an unread carrier")
		}
	case <-time.After(time.Second):
		t.Fatal("slow carrier trapped metadata owner")
	}
	read := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		_, err := peer.Read(buffer)
		read <- err
	}()
	if err := host.SendNotification(proto.Notification{Service: proto.ServiceSession, Event: proto.EventSessionExit, Params: struct{}{}}); err != nil {
		t.Fatalf("hint deadline poisoned critical delivery: %v", err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
}

func TestMetadataPartialFrameClosesOnlyItsCorruptedCarrier(t *testing.T) {
	connection, peer := net.Pipe()
	t.Cleanup(func() { _ = connection.Close(); _ = peer.Close() })
	host := New(connection, connection, "generation", "instance", slog.New(slog.NewTextHandler(io.Discard, nil)))
	partial := make(chan error, 1)
	go func() { var prefix [5]byte; _, err := io.ReadFull(peer, prefix[:]); partial <- err }()
	if host.TrySendNotification(proto.Notification{Service: proto.ServiceSession, Event: "metadata-hint", Params: struct{}{}}, time.Now().Add(50*time.Millisecond)) {
		t.Fatal("partial frame reported as delivered")
	}
	if err := <-partial; err != nil {
		t.Fatal(err)
	}
	var remaining [1]byte
	if _, err := peer.Read(remaining[:]); err != io.EOF {
		t.Fatalf("corrupted carrier remained writable: %v", err)
	}
}
