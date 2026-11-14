//go:build nocx_local_ssh

package sshsvc_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestConnectionLifecycleRequestsConformOverTheWire(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := newStand(t, &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()})
	destination := destinationAt(t, f.addr)
	if _, err := stand.client.BindConnection(context.Background(), destination, "pane-params", "never"); err != nil {
		t.Fatal(err)
	}
	// Registration can be refused before a first dial, but its actual request
	// still has to obey the same frozen ABI as an accepted registration.
	_ = stand.client.RegisterReconnect(context.Background(), destination, 7)
	if err := stand.client.ReleaseConnection(context.Background(), "pane-params"); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]string{
		proto.OpConnection:        "ssh.connection.params.schema.json",
		proto.OpReconnect:         "ssh.reconnect.params.schema.json",
		proto.OpReleaseConnection: "ssh.release-connection.params.schema.json",
	}
	requests := map[string][]byte{}
	for _, payload := range framesOf(t, stand.toCoord.bytes(), proto.TypeRequest) {
		req := decodeRequest(t, payload)
		if req.Service == proto.ServiceSSH {
			requests[req.Op] = req.Params
		}
	}
	for op, file := range schemas {
		t.Run(op, func(t *testing.T) {
			params, ok := requests[op]
			if !ok {
				t.Fatalf("no %s request recorded", op)
			}
			if err := validateHelperJSON(loadHelperSchema(t, file), params); err != nil {
				t.Fatalf("%s request violates contract: %v\npayload: %s", op, err, params)
			}
		})
	}
}
