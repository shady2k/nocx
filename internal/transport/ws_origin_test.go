package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/operation/catalog"
)

// Origin travels on the wire and defaults to person (ADR-8eej3e66 §2): the
// bulkhead's person reserve depends on the backend reading it.
func TestInvocationOrigin(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want catalog.Origin
	}{
		{"absent", `{"jsonrpc":"2.0","id":1,"method":"git.status"}`, catalog.OriginPerson},
		{"person", `{"jsonrpc":"2.0","id":1,"method":"git.status","origin":"person"}`, catalog.OriginPerson},
		{"background", `{"jsonrpc":"2.0","id":1,"method":"git.status","origin":"background"}`, catalog.OriginBackground},
		{"system", `{"jsonrpc":"2.0","id":1,"method":"git.status","origin":"system"}`, catalog.OriginSystem},
		{"unknown", `{"jsonrpc":"2.0","id":1,"method":"git.status","origin":"robot"}`, catalog.OriginPerson},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req jsonrpcRequest
			if err := json.Unmarshal([]byte(tc.raw), &req); err != nil {
				t.Fatal(err)
			}
			if got := invocationOrigin(req); got != tc.want {
				t.Fatalf("origin = %q, want %q", got, tc.want)
			}
		})
	}
}
