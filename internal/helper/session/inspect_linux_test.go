//go:build linux

package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The /proc reads themselves, against a procfs laid out in a temporary
// directory. The `root` field exists for exactly this: the reads are checkable
// without the OS agreeing to produce a process in the state the test wants,
// and the failure paths below — a cwd link that cannot be resolved, a cmdline
// that is not there — are otherwise unreachable on a machine we control.

func procEntry(t *testing.T, pid int) (*procfsSource, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "77")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return &procfsSource{root: root}, dir
}

// TestProcfsReadsWhatAnOrdinaryEntryHolds is the succeeds-on-a-normal-machine
// half of the pair; the failure half is below it.
func TestProcfsReadsWhatAnOrdinaryEntryHolds(t *testing.T) {
	src, dir := procEntry(t, 77)
	where := t.TempDir()
	if err := os.Symlink(where, filepath.Join(dir, "cwd")); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("/bin/zsh\x00-l\x00"), 0o600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("zsh\n"), 0o600); err != nil {
		t.Fatalf("write comm: %v", err)
	}

	obs := (&osInspector{src: src}).Observe(77, 0)
	if obs == nil {
		t.Fatal("an existing /proc entry produced no observation at all")
	}
	if obs.Source != "procfs" {
		t.Errorf("Source = %q, want procfs", obs.Source)
	}
	if obs.Cwd != where {
		t.Errorf("Cwd = %q, want %q", obs.Cwd, where)
	}
	if len(obs.Argv) != 2 {
		t.Errorf("Argv = %q, want the two arguments cmdline holds", obs.Argv)
	}
	if len(obs.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want empty: everything asked for was answered", obs.Unavailable)
	}
}

// TestProcfsNamesWhatItCouldNotRead: the same entry with nothing readable in
// it. A refused cwd link is not hypothetical — a process that changed uid, a
// hardened container, a pid that exited between the liveness check and the
// read — and reported as a blank it is indistinguishable from a shell that has
// not moved.
func TestProcfsNamesWhatItCouldNotRead(t *testing.T) {
	src, _ := procEntry(t, 77)

	obs := (&osInspector{src: src}).Observe(77, 0)
	if obs == nil {
		t.Fatal("an existing /proc entry produced no observation at all")
	}
	for _, want := range []proto.Diagnostic{proto.DiagnosticCwd, proto.DiagnosticArgv} {
		if !slices.Contains(obs.Unavailable, want) {
			t.Errorf("Unavailable = %v, want it to name %q", obs.Unavailable, want)
		}
	}
}

// TestProcfsSaysNothingAboutAPidItDoesNotHave — a process that exited leaves
// no entry, and the answer is no observation rather than an empty one.
func TestProcfsSaysNothingAboutAPidItDoesNotHave(t *testing.T) {
	src, _ := procEntry(t, 77)
	if obs := (&osInspector{src: src}).Observe(9999, 0); obs != nil {
		t.Fatalf("a pid with no /proc entry produced %+v", obs)
	}
}
