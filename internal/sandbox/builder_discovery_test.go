//go:build linux

package sandbox

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoverDependenciesFindsLargeNixStyleGraphAndInterpreter(t *testing.T) {
	// Keep this graph within the independent aggregate-byte budget; long test
	// names must not consume the policy budget in place of package paths.
	root, err := os.MkdirTemp("", "nx-nix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	interpreter := filepath.Join(root, "runtime", "ld.so")
	if err = os.Mkdir(filepath.Dir(interpreter), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestELF(t, interpreter, nil, nil, "")
	neededA, neededB := make([]string, 0, 150), make([]string, 0, 150)
	rpathA, rpathB := make([]string, 0, 150), make([]string, 0, 150)
	packageFiles := make([]string, 0, 300)
	for i := range 300 {
		dir := filepath.Join(root, fmt.Sprintf("nix-%03d", i))
		if err = os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("lib%03d.so", i)
		file := filepath.Join(dir, name)
		writeTestELF(t, file, nil, nil, "")
		packageFiles = append(packageFiles, file)
		if i < 150 {
			neededA = append(neededA, name)
			rpathA = append(rpathA, "$ORIGIN/"+filepath.Base(dir))
		} else {
			neededB = append(neededB, name)
			rpathB = append(rpathB, "$ORIGIN/"+filepath.Base(dir))
		}
	}
	a, b := filepath.Join(root, "liba.so"), filepath.Join(root, "libb.so")
	writeTestELF(t, a, neededA, rpathA, "")
	writeTestELFPathTag(t, b, neededB, rpathB, "", 15)
	shell := filepath.Join(root, "shell")
	writeTestELF(t, shell, []string{"liba.so", "libb.so"}, []string{"$ORIGIN"}, interpreter)

	got, err := discoverDependencies(shell, root)
	if err != nil {
		t.Fatalf("discover large dependency graph: %v", err)
	}
	want := map[string]bool{}
	for _, path := range append(packageFiles, a, b, interpreter) {
		canonical, canonicalErr := filepath.EvalSymlinks(path)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		want[canonical] = true
	}
	if len(got) <= 256 {
		t.Fatalf("discovery returned %d roots, want over 256", len(got))
	}
	for _, path := range got {
		if !want[path] {
			t.Fatalf("unexpected dependency root outside explicit package graph: %s", path)
		}
		delete(want, path)
	}
	if len(want) != 0 {
		t.Fatalf("missing %d canonical package/interpreter roots", len(want))
	}
	if backendAvailable() != nil {
		return // Pure ELF validation above also runs on older Linux kernels.
	}
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, path := range []string{home, workspace} {
		if err = os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := Prepare(BuildRequest{
		WorkspaceID: "large-dependency-graph", Cwd: workspace, HostHome: home,
		Shell: shell, Runner: shell, StandardRevision: 1,
		ProfileProvenance: StandardRoot, RuntimeBase: filepath.Join(root, "private-runtime"),
	})
	if err != nil {
		t.Fatalf("freeze discovered package graph: %v", err)
	}
	defer func() {
		if cleanupErr := prepared.Cleanup(); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	}()
	retained := make(map[string]bool, len(got))
	for _, entry := range prepared.Policy.Roots {
		if entry.Provenance == DependencyRoot {
			retained[entry.Path] = true
		}
	}
	for _, path := range got {
		if !retained[path] {
			t.Fatalf("effective policy dropped a discovered package root: %s", path)
		}
		delete(retained, path)
	}
	if len(retained) != 0 {
		t.Fatalf("effective policy broadened dependency discovery by %d roots", len(retained))
	}
}

func TestDiscoverDependenciesFailsClosedOnMalformedAndUnresolvedELF(t *testing.T) {
	root := t.TempDir()
	malformed := filepath.Join(root, "malformed")
	if err := os.WriteFile(malformed, []byte("not an ELF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverDependencies(malformed, root); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("malformed input must fail with a path-free error, got %v", err)
	}
	unresolved := filepath.Join(root, "unresolved")
	writeTestELF(t, unresolved, []string{"missing.so"}, []string{"$ORIGIN"}, "")
	if _, err := discoverDependencies(unresolved, root); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("unresolved DT_NEEDED must fail with a path-free error, got %v", err)
	}
	missingInterpreter := filepath.Join(root, "missing-interpreter")
	writeTestELF(t, missingInterpreter, nil, nil, filepath.Join(root, "absent-loader"))
	if _, err := discoverDependencies(missingInterpreter, root); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("unresolved PT_INTERP must fail with a path-free error, got %v", err)
	}
	unsupported := filepath.Join(root, "unsupported")
	writeTestELF(t, unsupported, nil, nil, "")
	unsupportedBytes, err := os.ReadFile(unsupported) // #nosec G304 -- own temporary ELF fixture.
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint16(unsupportedBytes[18:], 0xffff)
	if err := os.WriteFile(unsupported, unsupportedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverDependencies(unsupported, root); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("unsupported machine must fail with a path-free error, got %v", err)
	}
}

func TestDiscoverDependenciesRejectsELFTagAndSectionBudgets(t *testing.T) {
	root := t.TempDir()
	manyTags := make([]string, maxELFTags+1)
	for i := range manyTags {
		manyTags[i] = fmt.Sprintf("missing-%03d.so", i)
	}
	tagFile := filepath.Join(root, "too-many-tags")
	writeTestELF(t, tagFile, manyTags, nil, "")
	if _, err := discoverDependencies(tagFile, root); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("oversized DT_NEEDED table should fail on its budget, got %v", err)
	}
	sectionFile := filepath.Join(root, "oversized-dynamic")
	writeTestELFWithDynamicPadding(t, sectionFile, maxELFSection+1)
	if _, err := discoverDependencies(sectionFile, root); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("oversized dynamic section should fail on its budget, got %v", err)
	}
	longString := filepath.Join(root, "oversized-string")
	writeTestELF(t, longString, []string{strings.Repeat("x", maxELFString+1)}, nil, "")
	if _, err := discoverDependencies(longString, root); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("oversized ELF string should fail on its budget, got %v", err)
	}
	chainRoot := filepath.Join(root, "chain")
	if err := os.Mkdir(chainRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxELFDepth + 2 {
		name := fmt.Sprintf("node-%02d", i)
		var needed []string
		if i < maxELFDepth+1 {
			needed = []string{fmt.Sprintf("node-%02d", i+1)}
		}
		writeTestELF(t, filepath.Join(chainRoot, name), needed, []string{"$ORIGIN"}, "")
	}
	if _, err := discoverDependencies(filepath.Join(chainRoot, "node-00"), root); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("over-depth ELF graph should fail on its graph budget, got %v", err)
	}
}

func writeTestELF(t *testing.T, path string, needed, rpaths []string, interpreter string) {
	writeTestELFPathTag(t, path, needed, rpaths, interpreter, 29)
}

func writeTestELFPathTag(t *testing.T, path string, needed, rpaths []string, interpreter string, pathTag uint64) {
	t.Helper()
	var dynstr []byte
	addString := func(s string) uint64 {
		offset := uint64(len(dynstr))
		dynstr = append(dynstr, s...)
		dynstr = append(dynstr, 0)
		return offset
	}
	dynstr = append(dynstr, 0)
	dynamic := make([]byte, 0, (len(needed)+len(rpaths)+1)*16)
	addTag := func(tag uint64, value uint64) {
		entry := make([]byte, 16)
		binary.LittleEndian.PutUint64(entry, tag)
		binary.LittleEndian.PutUint64(entry[8:], value)
		dynamic = append(dynamic, entry...)
	}
	for _, name := range needed {
		addTag(1, addString(name))
	}
	if len(rpaths) != 0 {
		addTag(pathTag, addString(strings.Join(rpaths, ":")))
	}
	addTag(0, 0)
	writeELFSections(t, path, dynstr, dynamic, interpreter)
}

func writeTestELFWithDynamicPadding(t *testing.T, path string, size int) {
	dynstr := make([]byte, size+1)
	dynstr[0] = 0
	dynamic := make([]byte, 16)
	writeELFSections(t, path, dynstr, dynamic, "")
}

func writeELFSections(t *testing.T, path string, dynstr, dynamic []byte, interpreter string) {
	const ehdr, phdr = 64, 56
	data := make([]byte, ehdr)
	copy(data[:4], []byte{0x7f, 'E', 'L', 'F'})
	data[4], data[5], data[6] = 2, 1, 1
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	} else if runtime.GOARCH != "amd64" {
		t.Fatalf("synthetic ELF fixture does not support GOARCH=%s", runtime.GOARCH)
	}
	binary.LittleEndian.PutUint16(data[16:], 3)
	binary.LittleEndian.PutUint16(data[18:], machine)
	binary.LittleEndian.PutUint32(data[20:], 1)
	phoff, phnum := uint64(0), uint16(0)
	if interpreter != "" {
		phoff, phnum = ehdr, 1
		data = append(data, make([]byte, phdr)...)
		interpOffset := uint64(len(data))
		data = append(data, interpreter...)
		data = append(data, 0)
		p := data[ehdr : ehdr+phdr]
		binary.LittleEndian.PutUint32(p, 3)
		binary.LittleEndian.PutUint64(p[8:], interpOffset)
		interpreterSize := uint64(len(interpreter)) + 1
		binary.LittleEndian.PutUint64(p[32:], interpreterSize)
		binary.LittleEndian.PutUint64(p[40:], interpreterSize)
	}
	for len(data)%8 != 0 {
		data = append(data, 0)
	}
	dynstrOffset := uint64(len(data))
	data = append(data, dynstr...)
	dynamicOffset := uint64(len(data))
	data = append(data, dynamic...)
	shstr := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")
	shstrOffset := uint64(len(data))
	data = append(data, shstr...)
	for len(data)%8 != 0 {
		data = append(data, 0)
	}
	shoff := uint64(len(data))
	data = append(data, make([]byte, 4*64)...)
	sh := data[shoff:]
	section := func(index int, name uint32, typ uint32, offset, size uint64, link uint32, entsize uint64) {
		p := sh[index*64 : (index+1)*64]
		binary.LittleEndian.PutUint32(p, name)
		binary.LittleEndian.PutUint32(p[4:], typ)
		binary.LittleEndian.PutUint64(p[24:], offset)
		binary.LittleEndian.PutUint64(p[32:], size)
		binary.LittleEndian.PutUint32(p[40:], link)
		binary.LittleEndian.PutUint64(p[48:], 8)
		binary.LittleEndian.PutUint64(p[56:], entsize)
	}
	section(1, 1, 3, dynstrOffset, uint64(len(dynstr)), 0, 0)
	section(2, 9, 6, dynamicOffset, uint64(len(dynamic)), 1, 16)
	section(3, 18, 3, shstrOffset, uint64(len(shstr)), 0, 0)
	binary.LittleEndian.PutUint64(data[32:], phoff)
	binary.LittleEndian.PutUint64(data[40:], shoff)
	binary.LittleEndian.PutUint16(data[52:], ehdr)
	binary.LittleEndian.PutUint16(data[54:], phdr)
	binary.LittleEndian.PutUint16(data[56:], phnum)
	binary.LittleEndian.PutUint16(data[58:], 64)
	binary.LittleEndian.PutUint16(data[60:], 4)
	binary.LittleEndian.PutUint16(data[62:], 3)
	// #nosec G306 -- own synthetic executable ELF fixture.
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedWorktreeRequiresReciprocalBoundedMetadata(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "checkout")
	common := filepath.Join(root, "repo.git")
	gitdir := filepath.Join(common, "worktrees", "checkout")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitdir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitfile := filepath.Join(work, ".git")
	if err := os.WriteFile(gitfile, []byte("gitdir: "+gitdir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backlink := filepath.Join(gitdir, "gitdir")
	if err := os.WriteFile(backlink, []byte(gitfile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := linkedGitRoots(work)
	if err != nil {
		t.Fatalf("reciprocal metadata rejected: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(common)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != canonical {
		t.Fatalf("common roots = %v, want one canonical common directory", got)
	}
	if err := os.WriteFile(backlink, []byte(filepath.Join(root, "other", ".git")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := linkedGitRoots(work); err == nil {
		t.Fatal("non-reciprocal metadata unexpectedly granted a common-dir root")
	}
}

func TestRuntimeEnvironmentAndProjectionExposeOnlyExplicitRoots(t *testing.T) {
	home := filepath.Join(t.TempDir(), "host-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, "work")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".secret"), []byte("not copied"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := createRuntime(filepath.Join(t.TempDir(), "private-base"))
	if err != nil {
		t.Fatal(err)
	}
	prepared := &Prepared{Runtime: runtime, ownedRuntime: true}
	defer func() {
		if cleanupErr := prepared.Cleanup(); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	}()
	if err = createProjections(prepared, home, workspace, ProfileRoots{}, ProfileRoots{}); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(runtime.Home, "work")
	info, err := os.Lstat(projection)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("workspace projection is not an explicit symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runtime.Home, ".secret")); !os.IsNotExist(err) {
		t.Fatalf("host secret was projected: %v", err)
	}
	env := prepared.Environment([]string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "TMPDIR=/tmp", "PATH=/usr/bin", "TOKEN=kept-by-current-scrubber"})
	for _, required := range []string{"HOME=" + runtime.Home, "XDG_CONFIG_HOME=" + runtime.Config, "XDG_DATA_HOME=" + runtime.Data, "XDG_CACHE_HOME=" + runtime.Cache, "XDG_STATE_HOME=" + runtime.State, "TMPDIR=" + runtime.Temp, "TMP=" + runtime.Temp, "TEMP=" + runtime.Temp, "NOCX_SANDBOX=filesystem", "TOKEN=kept-by-current-scrubber"} {
		found := false
		for _, got := range env {
			if got == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("environment lacks %q", required)
		}
	}
	for _, forbidden := range []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "TMPDIR=/tmp"} {
		for _, got := range env {
			if got == forbidden {
				t.Fatalf("environment retained host path %q", forbidden)
			}
		}
	}
}

func TestRuntimeClosePreservesTreeAndCleanupIsIdempotent(t *testing.T) {
	runtime, err := createRuntime(filepath.Join(t.TempDir(), "private-base"))
	if err != nil {
		t.Fatal(err)
	}
	prepared := &Prepared{Runtime: runtime, ownedRuntime: true}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtime.Root); err != nil {
		t.Fatalf("Close removed launch runtime: %v", err)
	}
	if err := prepared.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Cleanup(); err != nil {
		t.Fatalf("second Cleanup was not idempotent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime.Root, "home")); !os.IsNotExist(err) {
		t.Fatalf("Cleanup retained runtime tree: %v", err)
	}
}
