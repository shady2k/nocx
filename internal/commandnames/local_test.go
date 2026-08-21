package commandnames_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/commandnames"
)

// mkexec writes an executable file and returns its name.
func mkexec(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a fixture executable
		t.Fatalf("write %s: %v", p, err)
	}
	return name
}

// localEnv is the environment a local session would get: the fixture PATH
// first, then the machine's own, because the probe's stamp ladder and the
// scan both run real programs (`stat`, `ls`) that the session resolves on
// PATH like anything else.
func localEnv(path string) []string {
	return []string{"PATH=" + path + ":" + os.Getenv("PATH"), "SHELL=/bin/bash", "HOME=" + os.TempDir()}
}

// The paired "and on a normal machine it succeeds" for every failure path
// below: the probe answers, and its answer is the far side's own truth.
func TestLocalSource_ProbeReportsThePathTheShellHas(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	src := commandnames.NewLocalSource("v39", localEnv(dir+":"+other))

	p, err := src.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.HasPrefix(p.Path, dir+":"+other) {
		t.Fatalf("path = %q, want it to start with the fixture entries", p.Path)
	}
	if p.ShellFamily != "bash" {
		t.Fatalf("shell family = %q, want the login shell's basename", p.ShellFamily)
	}
	if p.User == "" {
		t.Fatalf("probe reported no user")
	}
	if len(p.Stamps) < 2 || p.Stamps[0].Dir != dir || p.Stamps[1].Dir != other {
		t.Fatalf("stamps = %+v", p.Stamps)
	}
	if p.Stamps[0].Stamp == "" || p.Stamps[0].Stamp == "unstamped" {
		t.Fatalf("the fixture directory was not stamped: %+v", p.Stamps[0])
	}
	if !p.Stamped {
		t.Fatalf("Stamped = false on a machine whose stat works")
	}
}

// The invalidation signal itself: adding an entry to a PATH directory moves
// that directory's stamp. Without this the whole cache is a guess.
func TestLocalSource_AddingAnExecutableMovesTheDirectoryStamp(t *testing.T) {
	dir := t.TempDir()
	src := commandnames.NewLocalSource("v39", localEnv(dir))

	before, err := src.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	mkexec(t, dir, "nocx-fixture-tool")
	after, err := src.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if before.Stamps[0].Stamp == after.Stamps[0].Stamp {
		t.Fatalf("the stamp did not move when a file was added: %q", after.Stamps[0].Stamp)
	}
}

// The scan enumerates executables and nothing else — a directory and a
// non-executable file in the same PATH entry are not commands.
func TestLocalSource_ScanFindsExecutablesAndOnlyThose(t *testing.T) {
	dir := t.TempDir()
	mkexec(t, dir, "nocx-alpha")
	mkexec(t, dir, "nocx-beta")
	if err := os.WriteFile(filepath.Join(dir, "nocx-readme"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nocx-subdir"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	src := commandnames.NewLocalSource("v39", localEnv(dir))
	scan, err := src.Scan(context.Background(), commandnames.Probe{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := strings.Join(scan.Names, ",")
	if !strings.Contains(got, "nocx-alpha") || !strings.Contains(got, "nocx-beta") {
		t.Fatalf("names = %v", scan.Names)
	}
	if strings.Contains(got, "nocx-readme") {
		t.Fatalf("a non-executable file was offered as a command: %v", scan.Names)
	}
	if strings.Contains(got, "nocx-subdir") {
		t.Fatalf("a directory was offered as a command: %v", scan.Names)
	}
}

// A PATH with more than the bound's worth of directories stops at the bound
// on BOTH halves — the probe stamps 32 and the scan reads 32. If they
// differed, a name could come from a directory nothing ever invalidates.
func TestLocalSource_BothHalvesStopAtTheSameDirectoryBound(t *testing.T) {
	root := t.TempDir()
	dirs := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		d := filepath.Join(root, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mkexec(t, d, "tool"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		dirs = append(dirs, d)
	}
	src := commandnames.NewLocalSource("v39", localEnv(strings.Join(dirs, ":")))

	p, err := src.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(p.Stamps) != commandnames.MaxPathDirs {
		t.Fatalf("stamps = %d, want %d", len(p.Stamps), commandnames.MaxPathDirs)
	}
	scan, err := src.Scan(context.Background(), p)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Names) < commandnames.MaxPathDirs {
		t.Fatalf("names = %d, want at least one per stamped directory (%d)", len(scan.Names), commandnames.MaxPathDirs)
	}
	for i := commandnames.MaxPathDirs; i < len(dirs); i++ {
		beyond := "tool" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if strings.Contains(strings.Join(scan.Names, ","), beyond) {
			t.Fatalf("%s came from PATH entry %d, past the %d-directory bound", beyond, i, commandnames.MaxPathDirs)
		}
	}
}

// A PATH with nothing on it produces no snapshot at all rather than an empty
// one: "this shell can run nothing" is the same lie as "it can run
// everything", pointing the other way.
func TestLocalSource_AnEmptyEnumerationIsRefusedRatherThanPublished(t *testing.T) {
	src := commandnames.NewLocalSource("v39", []string{"PATH=" + t.TempDir(), "SHELL=/bin/sh"})
	if _, err := src.Scan(context.Background(), commandnames.Probe{}); err == nil {
		t.Fatalf("an empty enumeration was published")
	}
}

// The service and a real local source, together: two sessions, one scan.
func TestLocalSource_ThroughTheServiceTwoSessionsRunOneScan(t *testing.T) {
	dir := t.TempDir()
	mkexec(t, dir, "nocx-shared")
	src := commandnames.NewLocalSource("v39", localEnv(dir))
	svc := commandnames.New(nil, nil)

	first := svc.Names(context.Background(), src)
	if first.State != commandnames.StateReady {
		t.Fatalf("first: %q %q", first.State, first.Reason)
	}
	second := svc.Names(context.Background(), src)
	if second.State != commandnames.StateReady {
		t.Fatalf("second: %q", second.State)
	}
	if !contains(second.Names, "nocx-shared") {
		t.Fatalf("names = %v", second.Names)
	}

	// And the invalidation is real end to end: a new executable in a PATH
	// directory moves the stamp, so the next session sees it.
	mkexec(t, dir, "nocx-installed-later")
	third := svc.Names(context.Background(), src)
	if third.State != commandnames.StateReady {
		t.Fatalf("third: %q", third.State)
	}
	if !contains(third.Names, "nocx-installed-later") {
		t.Fatalf("names = %v — an installed package must be picked up", third.Names)
	}
}

// A context the caller cancels stops the run and publishes nothing.
func TestLocalSource_ACancelledScanPublishesNothing(t *testing.T) {
	dir := t.TempDir()
	mkexec(t, dir, "nocx-alpha")
	src := commandnames.NewLocalSource("v39", localEnv(dir))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.Scan(ctx, commandnames.Probe{}); err == nil {
		t.Fatalf("a cancelled scan returned a result")
	} else if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
		return
	} else {
		t.Fatalf("err = %v, want the cancellation", err)
	}
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
