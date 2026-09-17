package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

// TestInspectLauncherFindsWhatAWrapperScriptAdds covers finding 5/7 (nocx-nru89.10
// finding 4): a Nix makeShellWrapper-style launcher, in the shape
// nixpkgs/pkgs/build-support/setup-hooks/make-wrapper.sh actually emits, not an
// invented one. `--set`/`--set-default` become `export VAR=...` lines (matched
// by exportPattern's `=` arm); `--prefix`/`--suffix` (addValue) become five
// plain `VAR=...` assignment lines with NO `export` keyword, followed by one
// bare `export VAR` line with no `=` at all — the line the old exportPattern
// missed entirely, because the assignments before it never say "export" and
// the one that does has no `=`.
func TestInspectLauncherFindsWhatAWrapperScriptAdds(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "claude-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\necho real\n"), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "claude")
	// The exact shape of nixpkgs' makeShellWrapper output: `--set`/`--set-default`
	// as `export VAR=...`; `--prefix VAR ':' VAL` (addValue) as five bare
	// `VAR=...` lines ending in a plain `export VAR`.
	script := "#!/nix/store/xxx-bash/bin/bash -e\n" +
		"export DISABLE_AUTOUPDATER='1'\n" +
		"export FORCE_AUTOUPDATE_PLUGINS=${FORCE_AUTOUPDATE_PLUGINS-'1'}\n" +
		"LD_LIBRARY_PATH=${LD_LIBRARY_PATH:+':'$LD_LIBRARY_PATH':'}\n" +
		"LD_LIBRARY_PATH=${LD_LIBRARY_PATH/':''/nix/store/xxx-lib'':'/':'}\n" +
		"LD_LIBRARY_PATH='/nix/store/xxx-lib'$LD_LIBRARY_PATH\n" +
		"LD_LIBRARY_PATH=${LD_LIBRARY_PATH#':'}\n" +
		"LD_LIBRARY_PATH=${LD_LIBRARY_PATH%':'}\n" +
		"export LD_LIBRARY_PATH\n" +
		"PATH=${PATH:+':'$PATH':'}\n" +
		"PATH=${PATH/':''/nix/store/yyy-ripgrep/bin'':'/':'}\n" +
		"PATH='/nix/store/yyy-ripgrep/bin'$PATH\n" +
		"PATH=${PATH#':'}\n" +
		"PATH=${PATH%':'}\n" +
		"export PATH\n" +
		"exec -a \"$0\" \"" + real + "\"  \"$@\" \n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	got := inspectLauncher(wrapper)
	want := []string{"DISABLE_AUTOUPDATER", "FORCE_AUTOUPDATE_PLUGINS", "LD_LIBRARY_PATH", "PATH"}
	sort.Strings(got.Names)
	if !reflect.DeepEqual(got.Names, want) {
		t.Fatalf("inspectLauncher(wrapper).Names = %v, want %v", got.Names, want)
	}
	if got.Note != "" {
		t.Fatalf("inspectLauncher(wrapper).Note = %q, want none", got.Note)
	}
}

// TestInspectLauncherKeepsNamesWhenALaterHopIsUnreadable covers nocx-nru89.10
// finding 4's second gap: hop 0 exports a variable and execs a hop that
// cannot be read (missing here; permission-denied on the real machine).
// Names already found must survive, with a note identifying the unreadable
// hop -- not the pre-fix behaviour of discarding hop 0's names entirely.
func TestInspectLauncherKeepsNamesWhenALaterHopIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	wrapper := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"export FOO=1\n" +
		"exec \"" + missing + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	got := inspectLauncher(wrapper)
	if !reflect.DeepEqual(got.Names, []string{"FOO"}) {
		t.Fatalf("inspectLauncher.Names = %v, want [FOO] (hop 0's names must survive)", got.Names)
	}
	if got.Note == "" || !strings.Contains(got.Note, missing) {
		t.Fatalf("inspectLauncher.Note = %q, want it to name the unreadable hop %s", got.Note, missing)
	}
}

// TestWriteRunMetaListsBinaryWrapperAdditions covers nocx-nru89.10 finding 1:
// the real /run/current-system/sw/bin/claude on this machine resolves to a
// compiled nixpkgs makeBinaryWrapper binary, not a text script. That binary's
// own embedded docstring (makeCWrapper's docstring() function, compiled in by
// -x c) is exactly this shape, verified with `strings -n8` against the real
// store path: a `makeCWrapper '<exec>' \` line followed by one `--set`/
// `--set-default`/`--prefix`/`--suffix` line per flag, each carrying the
// variable name single-quoted. meta.json (via writeRunMeta) must list those
// names even though looksLikeScript reports the file as binary (it starts
// with the ELF magic, exactly like the real file).
func TestWriteRunMetaListsBinaryWrapperAdditions(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "claude-real")
	// The chain's next hop: a plain terminal binary with no wrapper
	// docstring of its own -- the shape of the real 215MB .claude-wrapped
	// bundle this wrapper execs -- so the walk ends cleanly with no note.
	if err := os.WriteFile(real, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 32)...), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	docstring := "\n\n" +
		"# ------------------------------------------------------------------------------------\n" +
		"# The C-code for this binary wrapper has been generated using the following command:\n\n\n" +
		"makeCWrapper '" + real + "' \\\n" +
		"    --set 'DISABLE_AUTOUPDATER' '1' \\\n" +
		"    --set-default 'FORCE_AUTOUPDATE_PLUGINS' '1' \\\n" +
		"    --set 'DISABLE_INSTALLATION_CHECKS' '1' \\\n" +
		"    --set 'USE_BUILTIN_RIPGREP' '0' \\\n" +
		"    --prefix 'LD_LIBRARY_PATH' ':' '/nix/store/gvgz0nm1mwj50k0xd2pr78bx9m5mjxvj-alsa-lib-1.2.16.1/lib' \\\n" +
		"    --prefix 'PATH' ':' '/nix/store/symzmzrb5qzcwz9mpsrcppwq0r90mq5w-procps-4.0.7/bin'\n\n\n" +
		"# (Use `nix-shell -p makeBinaryWrapper` to get access to makeCWrapper in your shell)\n" +
		"# ------------------------------------------------------------------------------------\n\n\n"
	content := append([]byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}, make([]byte, 24)...)
	content = append(content, []byte(docstring)...)
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, content, 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	metaPath := filepath.Join(t.TempDir(), "meta.json")
	if err := writeRunMeta(metaPath, "claude", bin, []string{"PATH=/usr/bin"}, ""); err != nil {
		t.Fatalf("writeRunMeta: %v", err)
	}
	raw, err := os.ReadFile(metaPath) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m struct {
		LauncherEnvNames []string `json:"launcherEnvNames"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	sort.Strings(m.LauncherEnvNames)
	want := []string{"DISABLE_AUTOUPDATER", "DISABLE_INSTALLATION_CHECKS", "FORCE_AUTOUPDATE_PLUGINS", "LD_LIBRARY_PATH", "PATH", "USE_BUILTIN_RIPGREP"}
	if !reflect.DeepEqual(m.LauncherEnvNames, want) {
		t.Fatalf("meta.launcherEnvNames = %v, want %v", m.LauncherEnvNames, want)
	}
}

// TestInspectLauncherAgainstTheRealNixClaudeLauncher runs the parser against
// whatever `claude` actually resolves to on this machine, never executing it
// -- only os.ReadFile, exactly like inspectLauncher itself. It skips when
// claude is absent or does not resolve into the Nix store, so CI (which has
// neither) stays on the mocks above; this is the one test that would have
// caught nocx-nru89.10 finding 1 (every committed .meta.json had
// launcherEnvNames: [] on the machine that produced them).
func TestInspectLauncherAgainstTheRealNixClaudeLauncher(t *testing.T) {
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("no claude on PATH")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Skipf("cannot resolve claude symlink: %v", err)
	}
	if !strings.Contains(resolved, "/nix/store/") {
		t.Skip("claude does not resolve into the Nix store on this machine")
	}
	got := inspectLauncher(resolved)
	sort.Strings(got.Names)
	t.Logf("inspectLauncher(%s) = %+v", resolved, got)
	if len(got.Names) == 0 {
		t.Fatalf("inspectLauncher(%s).Names is empty, want the real wrapper's variable names (Note: %q)", resolved, got.Note)
	}
	for _, want := range []string{"DISABLE_AUTOUPDATER", "DISABLE_INSTALLATION_CHECKS", "USE_BUILTIN_RIPGREP", "LD_LIBRARY_PATH"} {
		found := false
		for _, n := range got.Names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("inspectLauncher(%s).Names = %v, missing %s (measured by `strings -n8` on this machine)", resolved, got.Names, want)
		}
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

// TestIsolationRefusesWhenTheRealHomeCannotBeDetermined covers the fail-open
// found in review: refuseEnvOutsideClaudeConfig used to treat an
// os.UserHomeDir error, or an empty result, as "nothing to check against" and
// let the run proceed. A source that cannot be inspected is a refusal, the
// same rule refuseIfPresent already applies to every other check in this
// file — this closes the one place that instead failed open.
func TestIsolationRefusesWhenTheRealHomeCannotBeDetermined(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	saved := userHomeDir
	t.Cleanup(func() { userHomeDir = saved })

	env := isolatedEnv(t)
	t.Run("UserHomeDir errors", func(t *testing.T) {
		userHomeDir = func() (string, error) { return "", errors.New("no passwd entry") }
		err := refuseEnvOutsideClaudeConfig(env)
		if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), "real home directory") {
			t.Fatalf("refuseEnvOutsideClaudeConfig error = %v, want a refusal naming the real home directory", err)
		}
	})
	t.Run("UserHomeDir returns empty", func(t *testing.T) {
		userHomeDir = func() (string, error) { return "", nil }
		err := refuseEnvOutsideClaudeConfig(env)
		if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), "real home directory") {
			t.Fatalf("refuseEnvOutsideClaudeConfig error = %v, want a refusal naming the real home directory", err)
		}
	})
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

// TestIsolationRefusesARelativeHomeOrConfigDir covers nocx-nru89.10 finding 2's
// first gap: a relative HOME or CLAUDE_CONFIG_DIR used to be resolved against
// agent-capture's own cwd (filepath.Abs), not against -dir, so it could point
// anywhere depending on where the tool happened to be invoked from. The fix
// refuses outright rather than picking a base to resolve against.
func TestIsolationRefusesARelativeHomeOrConfigDir(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	pathLine := "PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin"
	cases := []struct {
		name       string
		relativeAt string // "HOME" or "CLAUDE_CONFIG_DIR"
	}{
		{"relative HOME", "HOME"},
		{"relative CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := t.TempDir()
			// The variable under test gets a relative value; the other one
			// gets a fresh, run-owned absolute value, so its own refusal
			// (missing, populated, etc.) can't mask the relative-path case.
			home := "HOME=" + t.TempDir()
			config := "CLAUDE_CONFIG_DIR=" + t.TempDir()
			if tc.relativeAt == "HOME" {
				home = "HOME=relative/home"
			} else {
				config = "CLAUDE_CONFIG_DIR=relative/config"
			}
			env := []string{pathLine, home, config}
			marker := filepath.Join(run, "started")
			err := captureProgram(captureOptions{
				OutPath: filepath.Join(run, "c.jsonl"), Argv: []string{shellPath(t), "-c", "touch " + marker},
				Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: env, Dir: run,
				MetaPath: filepath.Join(run, "meta.json"),
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), tc.relativeAt) {
				t.Fatalf("captureProgram error = %v, want a refusal naming %s as not absolute", err, tc.relativeAt)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("the program ran despite a relative HOME/CLAUDE_CONFIG_DIR")
			}
		})
	}
}

// TestIsolationRefusesASymlinkedConfigDirIntoTheRealClaude covers nocx-nru89.10
// finding 2's second gap: CLAUDE_CONFIG_DIR pointed, via a symlink, at a
// subdirectory of the operator's real ~/.claude used to pass whenever that
// subdirectory did not exist yet, because resolveMaybe fell back to the
// unresolved lexical path the instant EvalSymlinks saw a missing component --
// never resolving the symlinked parent that does exist. The fix resolves
// through the nearest existing ancestor's physical path and rejoins the
// not-yet-created suffix.
func TestIsolationRefusesASymlinkedConfigDirIntoTheRealClaude(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	saved := userHomeDir
	t.Cleanup(func() { userHomeDir = saved })
	realHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	userHomeDir = func() (string, error) { return realHome, nil }

	cases := []struct {
		name  string
		setup func(t *testing.T, run string) string // returns CLAUDE_CONFIG_DIR
	}{
		{
			name: "existing subdirectory",
			setup: func(t *testing.T, run string) string {
				t.Helper()
				sub := filepath.Join(realHome, ".claude", "existing")
				if err := os.MkdirAll(sub, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(run, "link-existing")
				if err := os.Symlink(sub, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "not-yet-existing subdirectory",
			setup: func(t *testing.T, run string) string {
				t.Helper()
				link := filepath.Join(run, "link-dangling")
				if err := os.Symlink(filepath.Join(realHome, ".claude"), link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "newsub")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := t.TempDir()
			configDir := tc.setup(t, run)
			marker := filepath.Join(run, "started")
			env := []string{
				"PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin",
				"HOME=" + t.TempDir(),
				"CLAUDE_CONFIG_DIR=" + configDir,
			}
			err := captureProgram(captureOptions{
				OutPath: filepath.Join(run, "c.jsonl"), Argv: []string{shellPath(t), "-c", "touch " + marker},
				Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: env, Dir: run,
				MetaPath: filepath.Join(run, "meta.json"),
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR") {
				t.Fatalf("captureProgram error = %v, want a refusal naming CLAUDE_CONFIG_DIR", err)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("the program ran despite CLAUDE_CONFIG_DIR reaching the real ~/.claude through a symlink")
			}
		})
	}
}

// TestIsolationRefusesANonEmptyConfigDir covers nocx-nru89.10 finding 2's
// third gap: an already-populated CLAUDE_CONFIG_DIR used to be accepted
// outright, anywhere on disk, as long as it did not resolve inside the real
// ~/.claude -- against the flag's promise that Claude reads only
// configuration this run creates.
func TestIsolationRefusesANonEmptyConfigDir(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	run := t.TempDir()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(run, "started")
	env := []string{
		"PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"CLAUDE_CONFIG_DIR=" + configDir,
	}
	err := captureProgram(captureOptions{
		OutPath: filepath.Join(run, "c.jsonl"), Argv: []string{shellPath(t), "-c", "touch " + marker},
		Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: env, Dir: run,
		MetaPath: filepath.Join(run, "meta.json"),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR") {
		t.Fatalf("captureProgram error = %v, want a refusal naming CLAUDE_CONFIG_DIR as already populated", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the program ran despite a pre-populated CLAUDE_CONFIG_DIR")
	}
}

// TestVersionProbeRunsInDir covers nocx-nru89.10 finding 3 (epic review finding
// 9): the version probe used to run in agent-capture's own, uninspected cwd --
// under record.sh, the nocx checkout with its own CLAUDE.md above it -- rather
// than -dir, which is the directory every refusal above actually inspected.
func TestVersionProbeRunsInDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workdir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "fakeclaude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then pwd; exit 0; fi\n" +
		"echo ran\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	out := filepath.Join(dir, "capture.jsonl")
	meta := filepath.Join(dir, "meta.json")
	err := captureProgram(captureOptions{
		OutPath: out, Argv: []string{"fakeclaude"}, Cols: 80, Rows: 24,
		Timeout: 5 * time.Second, Env: isolatedEnv(t, root), Dir: dir,
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
	if unmarshalErr := json.Unmarshal(raw, &m); unmarshalErr != nil {
		t.Fatalf("decode meta: %v", unmarshalErr)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(dir): %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(m.ProgramVersion))
	if err != nil || gotDir != wantDir {
		t.Fatalf("version probe ran in %q, want -dir %q", m.ProgramVersion, dir)
	}
}

// TestWriteRunMetaRedactsHomeInResolved covers nocx-nru89.10 finding 4 (epic
// review finding 12): a default `~/.local/bin/claude` install puts a home
// path into `resolved`, and nothing redacted it. The home prefix is now
// replaced with `~`, matching how SKILL.md already says secrets are handled
// (names, never values, never a path identifying the operator's account).
func TestWriteRunMetaRedactsHomeInResolved(t *testing.T) {
	saved := userHomeDir
	t.Cleanup(func() { userHomeDir = saved })
	fakeHome := t.TempDir()
	userHomeDir = func() (string, error) { return fakeHome, nil }
	binDir := filepath.Join(fakeHome, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	metaPath := filepath.Join(t.TempDir(), "meta.json")
	if err := writeRunMeta(metaPath, "claude", bin, []string{"PATH=/usr/bin"}, ""); err != nil {
		t.Fatalf("writeRunMeta: %v", err)
	}
	raw, err := os.ReadFile(metaPath) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m struct {
		Resolved string `json:"resolved"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	want := filepath.Join("~", ".local", "bin", "claude")
	if m.Resolved != want {
		t.Fatalf("meta.resolved = %q, want %q", m.Resolved, want)
	}
	if strings.Contains(string(raw), fakeHome) {
		t.Fatalf("meta carries the real home path verbatim: %s", raw)
	}
}
