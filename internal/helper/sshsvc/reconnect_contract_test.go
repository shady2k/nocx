//go:build nocx_local_ssh

package sshsvc_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestConnectionRegistrationResultsConformOverTheWire(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := newStand(t, &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()})
	destination := destinationAt(t, f.addr)
	if _, err := stand.client.BindConnection(context.Background(), destination, "pane-contract", "never"); err != nil {
		t.Fatal(err)
	}
	if err := stand.client.ReleaseConnection(context.Background(), "pane-contract"); err != nil {
		t.Fatal(err)
	}
	// A second release is an ordinary, typed answer, not a refusal.
	var result proto.ReleaseConnectionResult
	if err := stand.client.Call(context.Background(), proto.ServiceSSH, proto.OpReleaseConnection, proto.ReleaseConnectionParams{PaneID: "pane-contract"}, &result); err != nil {
		t.Fatal(err)
	}

	schema := loadHelperSchema(t, "ssh.release-connection.schema.json")
	checked := 0
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeResponse) {
		var response proto.Response
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			continue
		}
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(response.Result, &shape); err != nil {
			continue
		}
		if _, ok := shape["released"]; !ok {
			continue
		}
		if err := validateHelperJSON(schema, response.Result); err != nil {
			t.Fatal(err)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("validated %d release replies, want 2", checked)
	}
}
