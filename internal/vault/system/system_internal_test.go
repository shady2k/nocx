package system

import "testing"

// TestNewWiresThePlatformReasonProbe proves the observation is reachable in
// production, not only from tests that inject it. Every assertion about locked
// versus no-service is worthless if the default provider never consults the
// platform — which is precisely how a well-tested subsystem ends up unreachable.
func TestNewWiresThePlatformReasonProbe(t *testing.T) {
	if New().reasonProbe == nil {
		t.Fatal("New() left reasonProbe nil: an unexplained keyring error would fall back to guessing")
	}
}
