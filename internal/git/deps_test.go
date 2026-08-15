package git

import (
	"bytes"
	"os/exec"
	"testing"
)

// TestDomainPackageIsLinkableStandalone is the whole point of the split: the
// helper binary links internal/git for its domain types, and internal/session
// drags pty, ssh and storage in behind it.
func TestDomainPackageIsLinkableStandalone(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/shady2k/nocx/internal/git").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, forbidden := range []string{
		"github.com/shady2k/nocx/internal/session",
		"github.com/shady2k/nocx/internal/pty",
		"github.com/shady2k/nocx/internal/ssh",
		"github.com/shady2k/nocx/internal/storage",
	} {
		if bytes.Contains(out, []byte(forbidden+"\n")) {
			t.Errorf("internal/git must not depend on %s", forbidden)
		}
	}
}
