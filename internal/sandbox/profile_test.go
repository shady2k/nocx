package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturePolicy builds a Policy with one writable root, one read-only root,
// and a distinct shell file path.
func fixturePolicy(t *testing.T) *Policy {
	t.Helper()
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	rt := filepath.Join(base, "runtime")
	for _, d := range []string{ws, filepath.Join(rt, "home"), filepath.Join(rt, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	p, err := BuildPolicy(Request{Workspace: ws}, "/bin/sh", rt, nil)
	if err != nil {
		t.Fatalf("BuildPolicy: %v", err)
	}
	return p
}

func TestRenderProfile_Deterministic(t *testing.T) {
	p := fixturePolicy(t)
	a, err := renderProfile(p)
	if err != nil {
		t.Fatalf("renderProfile: %v", err)
	}
	b, err := renderProfile(p)
	if err != nil {
		t.Fatalf("renderProfile: %v", err)
	}
	if a != b {
		t.Fatal("renderProfile is not deterministic")
	}
}

func TestRenderProfile_Clauses(t *testing.T) {
	p := fixturePolicy(t)
	profile, err := renderProfile(p)
	if err != nil {
		t.Fatalf("renderProfile: %v", err)
	}

	if !strings.HasPrefix(profile, "(version 1)\n(deny default)\n") {
		t.Error("profile must begin with (version 1) and (deny default)")
	}
	if !strings.Contains(profile, "(allow network*)") {
		t.Error("profile must contain (allow network*) — network is out of scope")
	}
	for _, clause := range []string{
		"(allow user-preference-read)",
		"(allow ipc-posix-shm)",
		"(allow ipc-posix-sem)",
		"(allow ipc-sysv-sem)",
		"(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))",
		"(allow pseudo-tty)",
	} {
		if !strings.Contains(profile, clause) {
			t.Errorf("profile missing runtime clause %q", clause)
		}
	}
	for _, root := range p.WritableRoots {
		read := `(allow file-read* (subpath "` + root + `"))`
		write := `(allow file-write* (subpath "` + root + `"))`
		if !strings.Contains(profile, read) || !strings.Contains(profile, write) {
			t.Errorf("profile missing read-write clauses for %q", root)
		}
	}
	for _, root := range p.ReadOnlyRoots {
		want := `(allow file-read* (subpath "` + root + `"))`
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing read-only clause for %q", root)
		}
	}
	for _, dir := range p.WritableDirs {
		read := `(allow file-read* (subpath "` + dir + `"))`
		write := `(allow file-write* (subpath "` + dir + `"))`
		if !strings.Contains(profile, read) || !strings.Contains(profile, write) {
			t.Errorf("profile missing read-write directory clauses for %q", dir)
		}
	}
	for _, file := range p.ReadOnlyFiles {
		want := `(allow file-read* (literal "` + file + `"))`
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing read-only file clause for %q", file)
		}
	}
	for _, file := range p.WritableFiles {
		read := `(allow file-read* (literal "` + file + `"))`
		write := `(allow file-write* (literal "` + file + `"))`
		ioctl := `(allow file-ioctl (literal "` + file + `"))`
		if !strings.Contains(profile, read) || !strings.Contains(profile, write) || !strings.Contains(profile, ioctl) {
			t.Errorf("profile missing exact read-write device clauses for %q", file)
		}
	}
	if strings.Contains(profile, "vnode-type CHARACTER-DEVICE") {
		t.Error("profile must not grant every character device")
	}
}

func TestRenderProfile_RejectsInjection(t *testing.T) {
	p := fixturePolicy(t)
	// A malicious path with a newline must fail closed at render time.
	p.WritableRoots = append(p.WritableRoots, "/tmp/evil\x07root")
	if _, err := renderProfile(p); err == nil {
		t.Fatal("expected renderProfile to reject control-character path")
	}
}

func TestEscapeSBPL(t *testing.T) {
	// Control characters, including newline and NUL, are rejected.
	for _, bad := range []string{"a\nb", "a\x00b", "\x07", "\x1b[31m"} {
		if _, err := escapeSBPL(bad); err == nil {
			t.Errorf("escapeSBPL(%q): expected rejection", bad)
		}
	}
	// Backslash and quote are escaped; everything else passes through.
	got, err := escapeSBPL(`/tmp/a"b\c`)
	if err != nil {
		t.Fatalf("escapeSBPL: %v", err)
	}
	if got != `/tmp/a\"b\\c` {
		t.Errorf("escapeSBPL = %q, want %q", got, `/tmp/a\"b\\c`)
	}
	if _, err := escapeSBPL(""); err == nil {
		t.Error("escapeSBPL(\"\"): expected rejection")
	}
}
