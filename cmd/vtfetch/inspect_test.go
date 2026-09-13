package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The probe script's two assertions — "this build is static" and "the archive
// is in it" — are only as good as this command, so both directions are tested
// against binaries the test builds for itself: a pure-Go build is the static
// case on Linux, and a cgo build is the dynamic one on every platform.
//
// NOT os.Executable(): `go test` links its binary without a symbol table
// (measured on go1.26.7 — `elf.Open(...).Symbols()` answers "no symbol
// section"), so a test binary cannot stand in for the probe's artifact.
func TestInspectReportsFormatAndSymbols(t *testing.T) {
	exe := buildTiny(t, false)
	row, err := inspectFile(exe)
	if err != nil {
		t.Fatalf("inspectFile(%s): %v", exe, err)
	}
	wantFormat := "elf"
	if runtime.GOOS == "darwin" {
		wantFormat = "macho"
	}
	if row.Format != wantFormat {
		t.Fatalf("a native build is %q on %s, want %q", row.Format, runtime.GOOS, wantFormat)
	}
	if !row.SymbolTable {
		t.Fatal("a plain go build reports no symbol table; nothing is stripped here")
	}
	found, err := definedSymbol(exe, "main.main")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("main.main is not reported as defined in a Go binary that has one")
	}
	missing, err := definedSymbol(exe, "this_symbol_does_not_exist_anywhere")
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("a symbol that does not exist was reported as defined")
	}
}

func TestInspectRequiresStaticAndFailsOnDynamic(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("a Mach-O is never static, so the positive case is a Linux one; the negative is covered by TestInspectRequiresStaticRefusesADynamicBuild")
	}
	static := buildTiny(t, false)
	if err := cmdInspect([]string{"--require-static", static}); err != nil {
		t.Fatalf("--require-static refused a pure-Go Linux build: %v", err)
	}
	row, err := inspectFile(static)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Static {
		t.Fatalf("a pure-Go Linux build reports static=%v (interpreter %v, libraries %v)", row.Static, row.Interpreter, row.Libraries)
	}
}

func TestInspectRequiresStaticRefusesADynamicBuild(t *testing.T) {
	dynamic := buildTiny(t, true)
	row, err := inspectFile(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	if row.Static {
		t.Fatalf("a cgo build reports static=true; this test proves nothing then")
	}
	if err := cmdInspect([]string{"--require-static", dynamic}); err == nil {
		t.Fatal("--require-static accepted a dynamically linked build — the helper's static property would be assumed, not asserted")
	}
	if err := cmdInspect([]string{"--symbol", "definitely_absent", dynamic}); err == nil {
		t.Fatal("--symbol accepted a file that does not define the symbol")
	}
	if err := cmdInspect([]string{"--symbol", "main.main", dynamic}); err != nil {
		t.Fatalf("--symbol refused a symbol the binary does define: %v", err)
	}
}

func TestInspectRefusesSomethingThatIsNeither(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("not a binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectFile(path); err == nil {
		t.Fatal("inspectFile accepted a text file")
	}
	if err := cmdInspect([]string{path}); err == nil {
		t.Fatal("inspect accepted a text file")
	}
}

// buildTiny compiles a two-line program with cgo off (static on Linux) or on
// (dynamically linked against libc on every platform).
func buildTiny(t *testing.T, cgo bool) string {
	t.Helper()
	dir := t.TempDir()
	source := "package main\n\nfunc main() {}\n"
	if cgo {
		source = "package main\n\n/* int nothing(void) { return 0; } */\nimport \"C\"\n\nfunc main() { _ = C.nothing() }\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tiny\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "tiny")
	cmd := exec.Command("go", "build", "-o", out, ".") //nolint:gosec // the test builds a two-line program it just wrote
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if cgo {
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOFLAGS=")
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build (cgo=%v): %v\n%s", cgo, err, output)
	}
	return out
}
