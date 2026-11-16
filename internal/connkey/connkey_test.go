package connkey_test

// The public name of a connection (nocx-oalo1.5.3).
//
// Three properties are load-bearing: it is stable, it separates what the pool
// separates, and it publishes nothing a renderer should not read.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/connkey"
)

func TestAConnectionsIDIsStableAndSeparatesWhatThePoolSeparates(t *testing.T) {
	base := connkey.Key{Host: "host.example", Port: 22, User: "dev", Identity: "vault:key-1"}
	if base.ID() != (connkey.Key{Host: "host.example", Port: 22, User: "dev", Identity: "vault:key-1"}).ID() {
		t.Fatal("the same connection answered two different ids")
	}
	if len(base.ID()) < 16 {
		t.Fatalf("id %q is too short to be a name", base.ID())
	}

	// Every component the pool keys by must move the id: two of these
	// differing but sharing an id would be one row in the supervisor for two
	// connections — the authorization mistake AD-4's key exists to close.
	variants := map[string]connkey.Key{
		"another host":       {Host: "other.example", Port: 22, User: "dev", Identity: "vault:key-1"},
		"another port":       {Host: "host.example", Port: 2222, User: "dev", Identity: "vault:key-1"},
		"another account":    {Host: "host.example", Port: 22, User: "ops", Identity: "vault:key-1"},
		"another credential": {Host: "host.example", Port: 22, User: "dev", Identity: "vault:key-2"},
		"another route":      {Host: "host.example", Port: 22, User: "dev", Identity: "vault:key-1", Route: "bastion"},
	}
	for name, key := range variants {
		if key.ID() == base.ID() {
			t.Errorf("%s shares the id of the base connection", name)
		}
	}

	// Default ports may not be dropped: 22 and 022 are the same port written
	// two ways, and the id is derived from the resolved number.
	ports := connkey.Key{Host: "host.example", Port: 2200, User: "dev", Identity: "vault:key-1"}
	if strings.Contains(ports.ID(), "2200") || strings.Contains(base.ID(), "host.example") {
		t.Fatal("the id spells the connection's details rather than a digest of them")
	}
}
