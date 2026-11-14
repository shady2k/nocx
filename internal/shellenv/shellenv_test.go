package shellenv

// The parser tests moved here from internal/git/local with the parser, and
// assert exactly what they asserted there: the shell's dump is the kernel's
// own NUL-delimited environment, so there is no quoting to undo and no
// record to lose.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envDump renders what `printf <marker>; exec env -0` puts on stdout, so the
// parser tests state their input in the shape the shell actually produces.
func envDump(records ...string) string {
	out := Marker
	for _, r := range records {
		out += r + "\x00"
	}
	return out
}

// TestParseEnvDumpKeepsValuesVerbatim: there is no quoting to undo, so a
// value carrying spaces, quotes, an equals sign or a newline survives exactly
// — the class of value the export -p parser this replaced had to reconstruct,
// and could get wrong (nocx-58gq).
func TestParseEnvDumpKeepsValuesVerbatim(t *testing.T) {
	env, err := parseEnvDump(envDump(
		"PATH=/usr/bin:/bin",
		"SPACES=a b c",
		"QUOTES=say \"hi\" and 'bye'",
		"EQUALS=k=v",
		"MULTILINE=line1\nline2",
	))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range env {
		p := strings.IndexByte(kv, '=')
		got[kv[:p]] = kv[p+1:]
	}
	want := map[string]string{
		"PATH":      "/usr/bin:/bin",
		"SPACES":    "a b c",
		"QUOTES":    "say \"hi\" and 'bye'",
		"EQUALS":    "k=v",
		"MULTILINE": "line1\nline2",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d entries, want %d: %v", len(got), len(want), got)
	}
}

func TestParseEnvDumpDropsPWD(t *testing.T) {
	env, err := parseEnvDump(envDump("PWD=/stale/path", "KEEP=v"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "PWD=") {
			t.Fatalf("PWD survived: %v", env)
		}
	}
}

// TestParseEnvDumpDiscardsWhatTheRcFilesPrinted: an rc file that greets the
// user writes to the same stdout, and the marker is what separates it from
// the dump. Nothing before the marker is parsed — not even a line that looks
// exactly like a record.
func TestParseEnvDumpDiscardsWhatTheRcFilesPrinted(t *testing.T) {
	env, err := parseEnvDump("welcome to the shell\nNOISE=not-an-entry\n" + envDump("PATH=/usr/bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[0] != "PATH=/usr/bin" {
		t.Fatalf("parsed = %v, want only the records after the marker", env)
	}
}

// TestParseEnvDumpWithoutMarkerDegrades: no marker means the shell never
// reached the dump — a degrade with a reason, never a silently empty
// environment that would read as "resolved, and it happens to be bare".
func TestParseEnvDumpWithoutMarkerDegrades(t *testing.T) {
	if _, err := parseEnvDump("PATH=/usr/bin\x00"); err == nil {
		t.Fatal("a dump with no marker parsed as an environment")
	}
}

func TestParseEnvDumpDropsMalformedRecords(t *testing.T) {
	env, err := parseEnvDump(envDump("PATH=/usr/bin", "=novalue", "bad name=x", "1LEADING=x"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[0] != "PATH=/usr/bin" {
		t.Fatalf("parsed = %v, want only the well-formed record", env)
	}
}

// fakeShell writes the arguments it was run with to $ARGS_FILE and then prints
// the dump, so a test can read the mode the caller asked for without a real
// login shell.
func fakeShell(t *testing.T) (path, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	shell := filepath.Join(dir, "fake-shell")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '" + Marker + "'\nexec /usr/bin/env -0\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	return shell, argsFile
}

// The one difference between the two callers is the mode: git asks for the
// interactive shell a pane starts, detection asks for the login shell too.
func TestEnvRunsTheShellInTheModeTheCallerAsksFor(t *testing.T) {
	for _, test := range []struct {
		name  string
		login bool
		want  []string
	}{
		{name: "interactive", login: false, want: []string{"-i", "-c"}},
		{name: "login interactive", login: true, want: []string{"-l", "-i", "-c"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell, argsFile := fakeShell(t)
			env, err := Env(context.Background(), Spec{
				Shell:   shell,
				Env:     append(os.Environ(), "ARGS_FILE="+argsFile),
				Login:   test.login,
				Timeout: 5 * time.Second,
				MaxOut:  256 << 10,
			})
			if err != nil {
				t.Fatalf("Env: %v", err)
			}
			if _, ok := Path(env); !ok {
				t.Fatalf("resolved environment has no PATH: %v", env)
			}
			raw, err := os.ReadFile(argsFile) //nolint:gosec // test TempDir
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			if len(args) < len(test.want) || strings.Join(args[:len(test.want)], " ") != strings.Join(test.want, " ") {
				t.Fatalf("shell args = %q, want prefix %q", args, test.want)
			}
		})
	}
}

// A shell that keeps printing is stopped at the output cap, and what it wrote
// before that is still the answer — a timeout would have answered with the
// context's error instead, so the two outcomes are told apart by the reason.
func TestEnvStopsAtTheOutputCap(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "chatty-shell")
	script := "#!/bin/sh\nprintf '" + Marker + "'\nwhile :; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; done\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	_, err := Env(context.Background(), Spec{
		Shell:   shell,
		Env:     os.Environ(),
		Timeout: 5 * time.Second,
		MaxOut:  64,
	})
	if err == nil {
		t.Fatal("a shell that overflowed the cap resolved an environment")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the cap did not stop the shell; it ran to its timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "no PATH") {
		t.Fatalf("err = %v, want the truncated dump's reason", err)
	}
}
