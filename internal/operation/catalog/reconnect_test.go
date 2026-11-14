package catalog

import (
	"testing"
	"time"
)

func TestReconnectRegistrationDoesNotOwnTheDialLifetime(t *testing.T) {
	registration := declarationByID(t, "pane.reconnect")
	attempt := declarationByID(t, "internal.ssh.attempt")
	if got := registration.Max.Duration(); got != 10*time.Second {
		t.Fatalf("registration bound = %v, want 10s", got)
	}
	if got := attempt.Max.Duration(); got != 10*time.Minute {
		t.Fatalf("attempt bound = %v, want the design's 10min handshake budget", got)
	}
	if registration.Owner != OwnerRequest || attempt.Owner != OwnerBinding {
		t.Fatalf("registration/attempt owners = %v/%v", registration.Owner, attempt.Owner)
	}
}
