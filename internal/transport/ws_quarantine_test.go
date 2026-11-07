package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/profile"
)

// A QUARANTINED STORE IS UNAVAILABLE, NOT BUSY (ADR-8eej3e66 §5).
//
// The record's trade: when a committed restore cannot be rolled back, the
// stores stop answering until restart. The sentence a person reads has to name
// what happened and the way out, and it must NOT be the saturation error —
// "control plane busy" would send them to retry something that cannot work
// until the app restarts.
//
// The pair matters: the same call succeeds on a normal store, so the refusal
// below is the quarantine and not a broken fixture.
func TestConfigCall_OverAQuarantinedStoreIsANamedFailure(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	server := &WSServer{
		log:    logger,
		groups: profile.NewJSONStore(filepath.Join(t.TempDir(), "profiles.json")),
	}
	guard := capability.NewStoreGuard(catalog.Declarations(), time.Second)
	configGate := guard.Shared(capability.StoreConfig)
	vaultGate := guard.Shared(capability.StoreVault)
	op, endpointWired := server.buildConfigOp(configGate, vaultGate)

	var groupsList methodSpec
	for _, spec := range server.configSpecs(configGate, vaultGate, op, endpointWired, nil, nil) {
		if spec.method == "groups.list" {
			groupsList = spec
			break
		}
	}
	if groupsList.method == "" {
		t.Fatal("groups.list was not registered")
	}

	call := func() *recordingResponder {
		r := &recordingResponder{}
		h := validated(groupsList, groupsList.build(&wsConn{}, &connState{}, r), r)
		h(context.Background(), jsonrpcRequest{ID: json.RawMessage(`1`), Method: "groups.list", Params: json.RawMessage(`{}`)})
		return r
	}

	// On a normal store the same call answers a result.
	if r := call(); r.code != 0 {
		t.Fatalf("groups.list on a healthy store answered %d %q", r.code, r.msg)
	}

	guard.Quarantine([]string{capability.StoreConfig}, "the rollback did not finish within 10m0s")

	r := call()
	if r.code == 0 {
		t.Fatal("groups.list answered a result over a quarantined store")
	}
	if r.code == RemovedCapacityCode {
		t.Fatalf("a quarantined store answered the saturation error (%d), which tells a person to retry", r.code)
	}
	if !strings.Contains(r.msg, "Restart nocx") || !strings.Contains(r.msg, "settings and connection") {
		t.Fatalf("the failure names neither the store nor the way out: %q", r.msg)
	}
}
