package shellintegration

import (
	"testing"
)

// TestLocalBashRcfile_RequiresEnhancedSession pins the precondition: a
// conventional (non-enhanced) session has no session id anchor and no
// lifecycle config to embed; refusing beats rendering a rcfile that claims
// an authenticated channel that cannot exist.
func TestLocalBashRcfile_RequiresEnhancedSession(t *testing.T) {
	if _, err := LocalBashRcfile(LaunchOptions{Enhanced: false}); err == nil {
		t.Fatal("a conventional session must not render a lifecycle rcfile")
	}
}
