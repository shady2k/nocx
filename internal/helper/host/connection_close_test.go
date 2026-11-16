package host_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
)

type observedService struct {
	*fakeService
	closed chan *host.Host
}

func (s *observedService) ConnectionClosed(h *host.Host) { s.closed <- h }

func TestServeEOFNotifiesConnectionObserverBeforeReturning(t *testing.T) {
	s := &observedService{fakeService: &fakeService{name: "observed", ops: map[string]any{}}, closed: make(chan *host.Host, 1)}
	h := host.New(bytes.NewReader(nil), io.Discard, "hash", "instance", nil)
	h.Register(s)
	if err := h.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-s.closed:
		if got != h {
			t.Fatal("observer received another connection")
		}
	default:
		t.Fatal("Serve returned without notifying its service")
	}
}
