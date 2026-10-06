//go:build darwin

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinCanonicalActualCaseProtectsReservedAlias(t *testing.T) {
	parent := t.TempDir()
	reserved := filepath.Join(parent, ".NoCxPrivate")
	if err := os.Mkdir(reserved, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, strings.ToLower(filepath.Base(reserved)))
	if alias == reserved {
		t.Fatal("case-alias fixture did not change spelling")
	}
	resolved, err := canonicalDir(alias)
	if err != nil {
		t.Skipf("case-sensitive test volume: %v", err)
	}
	if resolved != reserved {
		t.Fatalf("canonical path retained alias spelling: got %q, want %q", resolved, reserved)
	}
	protected, err := canonicalReserved(alias)
	if err != nil {
		t.Fatal(err)
	}
	if protected != reserved {
		t.Fatalf("reserved alias bypassed canonical spelling: got %q, want %q", protected, reserved)
	}
}
