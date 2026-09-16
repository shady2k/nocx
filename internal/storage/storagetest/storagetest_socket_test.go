package storagetest_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// A disposable home must be short enough for this machine's helper to bind
// its endpoint socket in it, on every platform the suite runs on: on the
// macOS runner a home under TMPDIR produced a 109-byte socket path against a
// 103-byte limit and refused every pane a test opened. The generation is the
// longest shape the endpoint accepts (a full 64-hex id).
func TestADisposableHomeLeavesRoomForTheHelperEndpointSocket(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	gen := proto.GenerationID("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if _, err := endpoint.Path(endpoint.Dir(home), gen); err != nil {
		t.Fatalf("the helper endpoint socket does not fit in the disposable home %q: %v", home, err)
	}
}
