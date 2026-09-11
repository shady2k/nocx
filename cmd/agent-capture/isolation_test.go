package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
)

// setManagedDirs points the managed-configuration check at a directory the test
// owns, and puts the real list back afterwards.
func setManagedDirs(t *testing.T, dirs ...string) {
	t.Helper()
	saved := managedClaudeDirs
	managedClaudeDirs = dirs
	t.Cleanup(func() { managedClaudeDirs = saved })
}

func shellPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	return sh
}

func writeEnvFile(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "run.env")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestAnEnvFileIsTheWholeEnvironment(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "leaked-from-the-caller")
	root := t.TempDir()
	env, err := readEnvFile(writeEnvFile(t, root, "# isolated", "ONLY_THIS=1", "PATH="+filepath.Dir(shellPath(t))+":/usr/bin:/bin"))
	if err != nil {
		t.Fatalf("readEnvFile: %v", err)
	}
	out := filepath.Join(root, "capture.jsonl")
	meta := filepath.Join(root, "meta.json")
	var stderr bytes.Buffer
	err = captureProgram(captureOptions{
		OutPath: out, Argv: []string{"sh", "-c", "env"}, Cols: 80, Rows: 24,
		Timeout: 5 * time.Second, Env: env, Dir: root, MetaPath: meta,
	}, &stderr)
	if err != nil {
		t.Fatalf("captureProgram: %v (stderr %q)", err, stderr.String())
	}
	_, chunks, err := agentcapture.Read(out)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var printed strings.Builder
	for _, c := range chunks {
		printed.WriteString(c.Data)
	}
	for _, want := range []string{"ONLY_THIS=1", "TERM=xterm-256color", "COLUMNS=80"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("environment lacks %q:\n%s", want, printed.String())
		}
	}
	if strings.Contains(printed.String(), "CLAUDE_CODE_SESSION_ID") {
		t.Errorf("the caller's session reached the program:\n%s", printed.String())
	}
	raw, err := os.ReadFile(meta) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m struct {
		Program  string   `json:"program"`
		Resolved string   `json:"resolved"`
		SHA256   string   `json:"sha256"`
		EnvNames []string `json:"envNames"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if m.Program != "sh" || m.Resolved == "" || len(m.SHA256) != 64 {
		t.Errorf("meta = %+v, want sh resolved with a sha256", m)
	}
	if strings.Contains(string(raw), "=1") {
		t.Errorf("meta carries a variable's value: %s", raw)
	}
}

func TestIsolationRefusesClaudeConfigurationOutsideTheRun(t *testing.T) {
	cases := []struct {
		name  string
		place func(managed, parent, run string) string
	}{
		{"managed settings file", func(m, _, _ string) string { return filepath.Join(m, "managed-settings.json") }},
		{"managed settings directory", func(m, _, _ string) string { return filepath.Join(m, "managed-settings.d") }},
		{"managed instructions", func(m, _, _ string) string { return filepath.Join(m, "CLAUDE.md") }},
		{"managed rules", func(m, _, _ string) string { return filepath.Join(m, ".claude", "rules") }},
		{"CLAUDE.md in the run directory", func(_, _, r string) string { return filepath.Join(r, "CLAUDE.md") }},
		{"CLAUDE.local.md in an ancestor", func(_, p, _ string) string { return filepath.Join(p, "CLAUDE.local.md") }},
		{".claude in an ancestor", func(_, p, _ string) string { return filepath.Join(p, ".claude") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			managed := t.TempDir()
			setManagedDirs(t, managed)
			parent := t.TempDir()
			run := filepath.Join(parent, "run")
			if err := os.MkdirAll(run, 0o700); err != nil {
				t.Fatal(err)
			}
			source := tc.place(managed, parent, run)
			if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(parent, "started")
			out := filepath.Join(parent, "capture.jsonl")
			err := captureProgram(captureOptions{
				OutPath: out, Argv: []string{shellPath(t), "-c", "touch " + marker}, Cols: 80, Rows: 24,
				Timeout: 5 * time.Second, Env: []string{"PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin"}, Dir: run,
				MetaPath: filepath.Join(parent, "meta.json"),
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), source) {
				t.Fatalf("captureProgram error = %v, want a refusal naming %s", err, source)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("the program ran despite the refusal")
			}
			if _, statErr := os.Stat(out); statErr == nil {
				t.Fatal("a capture was written despite the refusal")
			}
		})
	}
}

func TestIsolationFailingCallsStartNothing(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	t.Run("an unreadable env file", func(t *testing.T) {
		err := run([]string{
			"capture", "-out", filepath.Join(t.TempDir(), "c.jsonl"),
			"-env-file", filepath.Join(t.TempDir(), "absent.env"), "--", "sh",
		}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "cannot read env file") {
			t.Fatalf("run error = %v, want an unreadable env file", err)
		}
	})
	t.Run("a malformed line", func(t *testing.T) {
		_, err := readEnvFile(writeEnvFile(t, t.TempDir(), "GOOD=1", "NOEQUALS"))
		if err == nil || !strings.Contains(err.Error(), "env file line 2") {
			t.Fatalf("readEnvFile error = %v, want line 2 named", err)
		}
	})
	t.Run("an ancestor that cannot be inspected", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root inspects every directory")
		}
		parent := t.TempDir()
		locked := filepath.Join(parent, "locked")
		run := filepath.Join(locked, "run")
		if err := os.MkdirAll(run, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // G302: restoring a directory this test created under t.TempDir() so cleanup can remove it
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		err := refuseOutsideClaudeConfig(run)
		if err == nil || !strings.Contains(err.Error(), "cannot inspect") {
			t.Fatalf("refuseOutsideClaudeConfig error = %v, want an uninspectable source", err)
		}
	})
	t.Run("an unwritable metadata file", func(t *testing.T) {
		root := t.TempDir()
		marker := filepath.Join(root, "started")
		err := captureProgram(captureOptions{
			OutPath: filepath.Join(root, "c.jsonl"), Argv: []string{shellPath(t), "-c", "touch " + marker},
			Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: []string{"PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin"}, Dir: root,
			MetaPath: filepath.Join(root, "no-such-dir", "meta.json"),
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "write run metadata") {
			t.Fatalf("captureProgram error = %v, want a metadata failure", err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("the program ran although its metadata could not be written")
		}
	})
}
