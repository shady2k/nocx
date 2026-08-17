package sandbox

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeExecutable creates a private executable fixture.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	// #nosec G306 -- execute permission is the behavior this fixture exercises.
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatalf("writeExecutable(%q): %v", path, err)
	}
}

// writeShell creates a file that elf.Open will reject as FormatError (shebang
// script), so addExecutableRoots treats it as a non-ELF executable.
func writeShell(t *testing.T, path string) {
	t.Helper()
	// #nosec G306 -- execute permission is the behavior this fixture exercises.
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("writeShell(%q): %v", path, err)
	}
}

func openELFTestExecutable(t *testing.T) *elf.File {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := elf.Open(exe)
	if err == nil {
		return file
	}
	if runtime.GOOS != "linux" {
		t.Skipf("ELF parser bound is Linux-specific: %v", err)
	}
	t.Fatal(err)
	return nil
}

func TestRuntimePathEntryBudget(t *testing.T) {
	oldEntryBudget := runtimePathEntryBudget
	runtimePathEntryBudget = 5
	t.Cleanup(func() { runtimePathEntryBudget = oldEntryBudget })

	base := t.TempDir()
	shell := filepath.Join(base, "shell")
	writeExecutable(t, shell)

	pathDir := filepath.Join(base, "pathdir")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Create 6 entries — exceeds budget of 5. Content and permissions
	// don't matter for the entry budget; every dirent counts.
	for i := range 6 {
		f := filepath.Join(pathDir, fmt.Sprintf("entry%d", i))
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{"PATH=" + pathDir}
	_, err := runtimeReadOnlyRoots(shell, env)
	if !errors.Is(err, errRuntimePathEntryBudget) {
		t.Fatalf("expected errRuntimePathEntryBudget, got %v", err)
	}
}

func TestRuntimePathCandidateBudget(t *testing.T) {
	oldCandidateBudget := runtimePathCandidateBudget
	runtimePathCandidateBudget = 3
	t.Cleanup(func() { runtimePathCandidateBudget = oldCandidateBudget })

	base := t.TempDir()
	shell := filepath.Join(base, "shell")
	writeExecutable(t, shell)

	pathDir := filepath.Join(base, "pathdir")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Create 4 executable files — exceeds candidate budget of 3.
	for i := range 4 {
		f := filepath.Join(pathDir, fmt.Sprintf("prog%d", i))
		writeExecutable(t, f)
	}

	env := []string{"PATH=" + pathDir}
	_, err := runtimeReadOnlyRoots(shell, env)
	if !errors.Is(err, errRuntimePathCandidateBudget) {
		t.Fatalf("expected errRuntimePathCandidateBudget, got %v", err)
	}
}

func TestRuntimePathDeduplication(t *testing.T) {
	// Use a budget that would fail if the same directory is scanned twice.
	oldCandidateBudget := runtimePathCandidateBudget
	runtimePathCandidateBudget = 5
	t.Cleanup(func() { runtimePathCandidateBudget = oldCandidateBudget })

	base := t.TempDir()
	shell := filepath.Join(base, "shell")
	writeShell(t, shell)

	pathDir := filepath.Join(base, "pathdir")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Create 3 executables. If scanned twice they'd be 6 candidates, exceeding
	// the budget of 5. A single scan gives 3, which fits.
	for i := range 3 {
		f := filepath.Join(pathDir, fmt.Sprintf("prog%d", i))
		writeExecutable(t, f)
	}

	// Put the same directory in PATH twice via a symlink alias.
	aliasDir := filepath.Join(base, "alias")
	if err := os.Symlink(pathDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + pathDir + string(os.PathListSeparator) + aliasDir}
	_, err := runtimeReadOnlyRoots(shell, env)
	if err != nil {
		t.Fatalf("deduplicated PATH should not exceed budget: %v", err)
	}
}

func TestRuntimePathRepeatedEntry(t *testing.T) {
	// Same entry repeated verbatim in PATH must be scanned only once.
	oldCandidateBudget := runtimePathCandidateBudget
	runtimePathCandidateBudget = 5
	t.Cleanup(func() { runtimePathCandidateBudget = oldCandidateBudget })

	base := t.TempDir()
	shell := filepath.Join(base, "shell")
	writeShell(t, shell)

	pathDir := filepath.Join(base, "pathdir")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		f := filepath.Join(pathDir, fmt.Sprintf("prog%d", i))
		writeExecutable(t, f)
	}

	env := []string{"PATH=" + pathDir + string(os.PathListSeparator) + pathDir}
	_, err := runtimeReadOnlyRoots(shell, env)
	if err != nil {
		t.Fatalf("repeated PATH entry should be deduplicated: %v", err)
	}
}

func TestRuntimePathBudgetErrorsArePathFree(t *testing.T) {
	oldEntryBudget := runtimePathEntryBudget
	runtimePathEntryBudget = 1
	t.Cleanup(func() { runtimePathEntryBudget = oldEntryBudget })

	base := t.TempDir()
	shell := filepath.Join(base, "shell")
	writeExecutable(t, shell)

	pathDir := filepath.Join(base, "pathdir")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		f := filepath.Join(pathDir, fmt.Sprintf("entry%d", i))
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{"PATH=" + pathDir}
	_, err := runtimeReadOnlyRoots(shell, env)
	if err == nil {
		t.Fatal("expected error")
	}
	// The error message must not contain either filesystem path.
	errMsg := err.Error()
	for _, path := range []string{base, pathDir} {
		if strings.Contains(errMsg, path) {
			t.Fatalf("budget error must not contain filesystem paths, got: %s", errMsg)
		}
	}
}

func TestRuntimeRelativePATHEntrySkipped(t *testing.T) {
	base := t.TempDir()
	shell := filepath.Join(base, "shell")
	writeShell(t, shell)
	relativeDir := filepath.Join(base, "relative")
	if err := os.MkdirAll(relativeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeShell(t, filepath.Join(relativeDir, "prog"))

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if chdirErr := os.Chdir(base); chdirErr != nil {
		t.Fatal(chdirErr)
	}

	roots, err := runtimeReadOnlyRoots(shell, []string{"PATH=relative"})
	if err != nil {
		t.Fatalf("relative PATH entry should be ignored: %v", err)
	}
	for _, root := range roots {
		if root == relativeDir {
			t.Fatalf("relative PATH directory was scanned: %q", root)
		}
	}
}

func TestRuntimeELFInterpreterBound(t *testing.T) {
	file := openELFTestExecutable(t)
	defer func() { _ = file.Close() }()
	section := file.Section(".interp")
	if section == nil || section.Size == 0 {
		t.Skip("test executable has no PT_INTERP section")
	}

	oldLimit := runtimeELFMaxInterp
	t.Cleanup(func() { runtimeELFMaxInterp = oldLimit })
	runtimeELFMaxInterp = section.Size - 1
	var fileBytes, aggregateBytes uint64
	var resolveAttempts int
	if _, err := elfInterpreter(file, &fileBytes, &aggregateBytes, &resolveAttempts); !errors.Is(err, errRuntimeELFInterp) {
		t.Fatalf("expected bounded interpreter error, got %v", err)
	}

	runtimeELFMaxInterp = section.Size
	fileBytes, aggregateBytes = 0, 0
	resolveAttempts = 0
	if _, err := elfInterpreter(file, &fileBytes, &aggregateBytes, &resolveAttempts); err != nil {
		t.Fatalf("ordinary interpreter should parse: %v", err)
	}
}

func TestRuntimeELFDynamicMetadataBounds(t *testing.T) {
	file := openELFTestExecutable(t)
	defer func() { _ = file.Close() }()
	dynamic := file.SectionByType(elf.SHT_DYNAMIC)
	if dynamic == nil || dynamic.Size == 0 {
		t.Skip("test executable has no dynamic section")
	}
	oldSection, oldAggregate := runtimeELFMaxSectionBytes, runtimeELFMaxAggregateBytes
	t.Cleanup(func() {
		runtimeELFMaxSectionBytes = oldSection
		runtimeELFMaxAggregateBytes = oldAggregate
	})
	runtimeELFMaxSectionBytes = dynamic.Size - 1
	var fileBytes, aggregateBytes uint64
	if _, err := elfDynamicStrings(file, elf.DT_NEEDED, errRuntimeELFNeeded, &fileBytes, &aggregateBytes); !errors.Is(err, errRuntimeELFMetadataBudget) {
		t.Fatalf("expected metadata section bound, got %v", err)
	}

	runtimeELFMaxSectionBytes = oldSection
	runtimeELFMaxAggregateBytes = 0
	fileBytes, aggregateBytes = 0, 0
	if _, err := elfDynamicStrings(file, elf.DT_NEEDED, errRuntimeELFNeeded, &fileBytes, &aggregateBytes); !errors.Is(err, errRuntimeELFWorkBudget) {
		t.Fatalf("expected aggregate metadata bound, got %v", err)
	}
}

func TestRuntimeELFDepthBudget(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	var elfCount, resolveAttempts int
	var elfBytes uint64
	err = addELFRoots(
		exe,
		&roots,
		make(map[string]bool),
		make(map[string]bool),
		&elfCount,
		&elfBytes,
		&resolveAttempts,
		runtimeELFMaxDepth,
	)
	if !errors.Is(err, errRuntimeELFWorkBudget) {
		t.Fatalf("expected dependency depth bound, got %v", err)
	}
}

func TestRuntimeELFResolutionAttemptBudget(t *testing.T) {
	oldLimit := runtimeELFMaxResolveAttempts
	runtimeELFMaxResolveAttempts = 1
	t.Cleanup(func() { runtimeELFMaxResolveAttempts = oldLimit })

	attempts := 0
	_, _, err := resolveELFDependency(
		"missing-library.so",
		[]string{t.TempDir(), t.TempDir()},
		&attempts,
	)
	if !errors.Is(err, errRuntimeELFWorkBudget) {
		t.Fatalf("expected resolution attempt bound, got %v", err)
	}
}

func TestRuntimeELFSearchDirectoryBudget(t *testing.T) {
	oldLimit := runtimeELFMaxResolveAttempts
	runtimeELFMaxResolveAttempts = 1
	t.Cleanup(func() { runtimeELFMaxResolveAttempts = oldLimit })

	attempts := 0
	_, err := canonicalELFSearchDirs(
		t.TempDir(),
		[]string{t.TempDir(), t.TempDir()},
		&attempts,
	)
	if !errors.Is(err, errRuntimeELFWorkBudget) {
		t.Fatalf("expected search-directory attempt bound, got %v", err)
	}
}
