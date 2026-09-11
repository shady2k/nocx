package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
)

// setManagedDirs points the managed-configuration check at directories the test
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

// isolatedEnv is a minimal, valid -env-file environment: PATH (with pathDirs
// prepended), a fresh HOME and a fresh CLAUDE_CONFIG_DIR, both empty and both
// owned by the test. Every test that exercises a real capture under isolation
// uses this so a scenario under test isn't masked by the HOME/CLAUDE_CONFIG_DIR
// refusal added for nocx-nru89.7.
func isolatedEnv(t *testing.T, pathDirs ...string) []string {
	t.Helper()
	dirs := append(append([]string{}, pathDirs...), "/usr/bin", "/bin")
	return []string{
		"PATH=" + strings.Join(dirs, ":"),
		"HOME=" + t.TempDir(),
		"CLAUDE_CONFIG_DIR=" + t.TempDir(),
	}
}

func TestAnEnvFileIsTheWholeEnvironment(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "leaked-from-the-caller")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "claude-config")
	env, err := readEnvFile(writeEnvFile(t, root, "# isolated",
		"ONLY_THIS=1", "PATH=/usr/bin:/bin", "HOME="+home, "CLAUDE_CONFIG_DIR="+config))
	if err != nil {
		t.Fatalf("readEnvFile: %v", err)
	}
	out := filepath.Join(root, "capture.jsonl")
	meta := filepath.Join(root, "meta.json")
	var stderr bytes.Buffer
	// "env" is run directly, not through a shell: a shell (bash included, and
	// /bin/sh resolves to bash on this machine) auto-exports PWD, SHLVL and _
	// into a child's environment even under a replaced Env, which would make
	// an exact-set assertion depend on which shell happens to be /bin/sh.
	err = captureProgram(captureOptions{
		OutPath: out, Argv: []string{"env"}, Cols: 80, Rows: 24,
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
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(printed.String(), "\r\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("printed environment line %q has no '='", line)
		}
		got[key] = value
	}
	want := map[string]string{
		"ONLY_THIS":         "1",
		"PATH":              "/usr/bin:/bin",
		"HOME":              home,
		"CLAUDE_CONFIG_DIR": config,
		"TERM":              "xterm-256color",
		"LANG":              "en_US.UTF-8",
		"LC_ALL":            "en_US.UTF-8",
		"COLUMNS":           "80",
		"LINES":             "24",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("program environment = %#v, want exactly %#v", got, want)
	}
	for key := range got {
		if key != "CLAUDE_CONFIG_DIR" && strings.HasPrefix(key, "CLAUDE") {
			t.Errorf("a caller CLAUDE* variable reached the program: %s", key)
		}
	}
	raw, err := os.ReadFile(meta) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m struct {
		Program          string   `json:"program"`
		Resolved         string   `json:"resolved"`
		SHA256           string   `json:"sha256"`
		EnvNames         []string `json:"envNames"`
		LauncherEnvNames []string `json:"launcherEnvNames"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if m.Program != "env" || m.Resolved == "" || len(m.SHA256) != 64 {
		t.Errorf("meta = %+v, want env resolved with a sha256", m)
	}
	if m.LauncherEnvNames == nil {
		t.Errorf("meta lacks launcherEnvNames (even an empty list should decode)")
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
				Timeout: 5 * time.Second, Env: isolatedEnv(t, filepath.Dir(shellPath(t))), Dir: run,
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

// TestIsolationRefusesASymlinkedDir covers nocx-nru89.7 finding 4: a symlinked
// -dir whose physical target's parent holds CLAUDE.md must refuse exactly as
// the unsymlinked path does, because exec.Cmd passes no PWD and Claude's own
// cwd walk sees the OS's physical (symlink-resolved) working directory.
func TestIsolationRefusesASymlinkedDir(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(linkParent, "started")
	out := filepath.Join(linkParent, "capture.jsonl")
	err := captureProgram(captureOptions{
		OutPath: out, Argv: []string{shellPath(t), "-c", "touch " + marker}, Cols: 80, Rows: 24,
		Timeout: 5 * time.Second, Env: isolatedEnv(t, filepath.Dir(shellPath(t))), Dir: link,
		MetaPath: filepath.Join(linkParent, "meta.json"),
	}, &bytes.Buffer{})
	claudeMD := filepath.Join(repo, "CLAUDE.md")
	if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), claudeMD) {
		t.Fatalf("captureProgram error = %v, want a refusal naming %s", err, claudeMD)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the program ran despite the symlinked -dir")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("a capture was written despite the symlinked -dir")
	}
}

// TestIsolationRefusesASymlinkedAncestor covers the same finding for a
// symlink in the middle of the path, not just at the leaf: -dir itself is a
// real directory, but it is reached only through a symlinked ancestor whose
// physical parent holds CLAUDE.md.
func TestIsolationRefusesASymlinkedAncestor(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	run := filepath.Join(sub, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(link, "run")
	marker := filepath.Join(linkParent, "started")
	out := filepath.Join(linkParent, "capture.jsonl")
	err := captureProgram(captureOptions{
		OutPath: out, Argv: []string{shellPath(t), "-c", "touch " + marker}, Cols: 80, Rows: 24,
		Timeout: 5 * time.Second, Env: isolatedEnv(t, filepath.Dir(shellPath(t))), Dir: dir,
		MetaPath: filepath.Join(linkParent, "meta.json"),
	}, &bytes.Buffer{})
	claudeMD := filepath.Join(repo, "CLAUDE.md")
	if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), claudeMD) {
		t.Fatalf("captureProgram error = %v, want a refusal naming %s", err, claudeMD)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the program ran despite the symlinked ancestor")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("a capture was written despite the symlinked ancestor")
	}
}

// TestIsolationRefusesAnEnvFileMissingHomeOrConfigDir covers finding 8: an
// env file that omits HOME or CLAUDE_CONFIG_DIR lets Claude fall back to the
// passwd home and the person's real ~/.claude.
func TestIsolationRefusesAnEnvFileMissingHomeOrConfigDir(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	run := t.TempDir()
	pathLine := "PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin"
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"missing HOME", []string{pathLine, "CLAUDE_CONFIG_DIR=" + t.TempDir()}, "HOME"},
		{"missing CLAUDE_CONFIG_DIR", []string{pathLine, "HOME=" + t.TempDir()}, "CLAUDE_CONFIG_DIR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(run, strings.ReplaceAll(tc.name, " ", "-")+"-started")
			err := captureProgram(captureOptions{
				OutPath: filepath.Join(run, "c.jsonl"), Argv: []string{shellPath(t), "-c", "touch " + marker},
				Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: tc.env, Dir: run,
				MetaPath: filepath.Join(run, "meta.json"),
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("captureProgram error = %v, want a refusal naming %s", err, tc.want)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("the program ran without HOME/CLAUDE_CONFIG_DIR both set")
			}
		})
	}
}

// TestIsolationRefusesAnUninspectableManagedDirectory covers finding 11: the
// managed-directory check must refuse, not silently pass, when it cannot be
// inspected, exactly like the ancestor check already does.
func TestIsolationRefusesAnUninspectableManagedDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root inspects every directory")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G302: restoring a directory this test created under t.TempDir() so cleanup can remove it
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	setManagedDirs(t, locked)
	err := refuseOutsideClaudeConfig(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cannot inspect") {
		t.Fatalf("refuseOutsideClaudeConfig error = %v, want an uninspectable managed source", err)
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
			Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: isolatedEnv(t, filepath.Dir(shellPath(t))), Dir: root,
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

// TestInspectLauncherFindsWhatAWrapperScriptAdds covers finding 5/7: a Nix
// wrapProgram-style launcher exports and prefixes several variables, and only
// their names belong in the metadata.
func TestInspectLauncherFindsWhatAWrapperScriptAdds(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "claude-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\necho real\n"), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"export DISABLE_AUTOUPDATER=1\n" +
		"export DISABLE_INSTALLATION_CHECKS=1\n" +
		"export USE_BUILTIN_RIPGREP=1\n" +
		"export LD_LIBRARY_PATH='/nix/store/xxx-lib'\"${LD_LIBRARY_PATH:+':'$LD_LIBRARY_PATH}\"\n" +
		"export PATH='/nix/store/yyy-ripgrep/bin'\"${PATH:+':'$PATH}\"\n" +
		"exec -a \"$0\" \"" + real + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	got := inspectLauncher(wrapper)
	want := []string{"DISABLE_AUTOUPDATER", "DISABLE_INSTALLATION_CHECKS", "LD_LIBRARY_PATH", "PATH", "USE_BUILTIN_RIPGREP"}
	sort.Strings(got.Names)
	if !reflect.DeepEqual(got.Names, want) {
		t.Fatalf("inspectLauncher(wrapper).Names = %v, want %v", got.Names, want)
	}
	if got.Note != "" {
		t.Fatalf("inspectLauncher(wrapper).Note = %q, want none", got.Note)
	}
}

// TestInspectLauncherRecordsWhenItCannotReadABinary covers finding 5/7: a
// binary launcher (the common case — the resolved `claude` is a Node
// executable) cannot be parsed for its own additions, and that must be
// recorded rather than silently reported as "adds nothing".
func TestInspectLauncherRecordsWhenItCannotReadABinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	elfMagic := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 32)...)
	if err := os.WriteFile(bin, elfMagic, 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	got := inspectLauncher(bin)
	if len(got.Names) != 0 {
		t.Fatalf("inspectLauncher(binary).Names = %v, want none", got.Names)
	}
	if got.Note == "" {
		t.Fatalf("inspectLauncher(binary).Note is empty, want a could-not-read note")
	}
}

// TestCaptureRecordsTheProgramVersionWhenAsked covers finding 7's record.sh
// half: -version-arg runs the resolved program once, under the isolated
// environment, and the output lands in meta.json next to the capture.
func TestCaptureRecordsTheProgramVersionWhenAsked(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "fakeclaude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '9.9.9 (fake)'; exit 0; fi\n" +
		"echo ran\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	out := filepath.Join(root, "capture.jsonl")
	meta := filepath.Join(root, "meta.json")
	err := captureProgram(captureOptions{
		OutPath: out, Argv: []string{"fakeclaude"}, Cols: 80, Rows: 24,
		Timeout: 5 * time.Second, Env: isolatedEnv(t, root), Dir: root,
		MetaPath: meta, VersionArg: "--version",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("captureProgram: %v", err)
	}
	raw, err := os.ReadFile(meta) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m struct {
		ProgramVersion string `json:"programVersion"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if m.ProgramVersion != "9.9.9 (fake)" {
		t.Fatalf("meta.programVersion = %q, want %q", m.ProgramVersion, "9.9.9 (fake)")
	}
}
