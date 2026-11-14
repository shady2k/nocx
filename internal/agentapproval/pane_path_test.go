package agentapproval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityForExecutableInPanePath(t *testing.T) {
	cwd := t.TempDir()
	bin := filepath.Join(cwd, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(bin, "myagent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	want, err := IdentityForPath(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ name, command, path, cwd string }{
		{"absolute search", "myagent", bin, ""},
		{"relative search", "myagent", "bin", cwd},
		{"empty search", "myagent", "", bin},
		{"empty component", "myagent", ":/nonexistent", bin},
		{"relative command", "bin/myagent", "/nonexistent", cwd},
		{"absolute command", command, "/nonexistent", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := LookupExecutableInPath(tc.command, tc.path, tc.cwd)
			if err != nil || resolved != want.Path {
				t.Fatalf("lookup=%q, %v, want %q", resolved, err, want.Path)
			}
			got, err := IdentityForExecutableInPath(tc.command, tc.path, tc.cwd)
			if err != nil || got != want {
				t.Fatalf("identity=%+v, %v, want %+v", got, err, want)
			}
		})
	}
	t.Setenv("PATH", bin)
	for _, tc := range []struct{ name, command, path, cwd, reason string }{
		{"no GUI fallback", "myagent", t.TempDir(), cwd, "not on the pane's PATH"},
		{"empty path is not absent", "myagent", "", cwd, "not on the pane's PATH"},
		{"relative path needs cwd", "myagent", "bin", "", "pane working directory"},
		{"relative command needs cwd", "bin/myagent", bin, "", "pane working directory"},
		{"missing absolute command", filepath.Join(cwd, "missing"), bin, cwd, "no such file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IdentityForExecutableInPath(tc.command, tc.path, tc.cwd)
			if err == nil || !strings.Contains(err.Error(), tc.reason) || got != (Executable{}) {
				t.Fatalf("identity=%+v, err=%v, want refusal %q", got, err, tc.reason)
			}
		})
	}
	nonexec := filepath.Join(cwd, "not-executable")
	if err := os.WriteFile(nonexec, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := IdentityForExecutableInPath("not-executable", cwd, cwd); err == nil {
		t.Fatal("non-executable file was resolved")
	}
}

func TestPanePathFollowsSymlinksBeforeParentComponents(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(real, "bin", "agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(real, "sub"), link); err != nil {
		t.Fatal(err)
	}
	// The lexical spelling points at a competing binary; only filesystem
	// traversal names the executable the shell would actually find.
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "agent"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	want, err := IdentityForPath(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ command, path string }{
		{"agent", "../bin"},
		{"agent", link + "/../bin"},
		{link + "/../bin/agent", ""},
		{"../bin/agent", ""},
	} {
		got, err := IdentityForExecutableInPath(tc.command, tc.path, link)
		want.Path = link + "/../bin/agent"
		if err != nil || got != want {
			t.Fatalf("command=%q PATH=%q: %+v, %v, want %+v", tc.command, tc.path, got, err, want)
		}
	}
}

func TestPanePathIdentityDoesNotSkipUnreadableExecutable(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	program := filepath.Join(first, "agent")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o111); err != nil { //nolint:gosec // execute-only fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "agent"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	if _, err := os.ReadFile(program); err == nil { //nolint:gosec // test TempDir executable fixture
		t.Skip("requires a user without read access to execute-only files")
	}
	path := first + string(os.PathListSeparator) + second
	resolved, err := LookupExecutableInPath("agent", path, first)
	if err != nil || resolved != program {
		t.Fatalf("lookup=%q, %v, want %q", resolved, err, program)
	}
	got, err := IdentityForExecutableInPath("agent", path, first)
	if err == nil || !strings.Contains(err.Error(), "read executable") || got != (Executable{}) {
		t.Fatalf("identity=%+v, %v; want unreadable first executable refusal", got, err)
	}
}
