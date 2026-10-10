package content

import (
	"os"
	"strings"
	"testing"
)

// THE DETECTOR NEVER SHIPS (ADR-0077). The goroutine-identity check exists
// only in the nocx_framecheck build; a shipped build compiles
// framecheck_off.go instead. Pinned from the source, so it holds in every
// build this test runs in: every file of this package that reads a
// goroutine's identity from the runtime's stack carries the tag's build
// constraint, and the no-op twin carries its negation.
func TestTheFrameCheckIsOnlyInTheTaggedBuild(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, readErr := os.ReadFile(name) //nolint:gosec // a file of this package, named by ReadDir(".")
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", name, readErr)
		}
		src := string(raw)
		if strings.Contains(src, "runtime.Stack(") && !strings.HasPrefix(src, "//go:build nocx_framecheck\n") {
			t.Errorf("%s reads the runtime's stack without the nocx_framecheck build constraint: it would ship", name)
		}
	}
	off, err := os.ReadFile("framecheck_off.go")
	if err != nil || !strings.HasPrefix(string(off), "//go:build !nocx_framecheck\n") {
		t.Errorf("framecheck_off.go must be the shipped build's no-op, constrained to !nocx_framecheck (err %v)", err)
	}
}
