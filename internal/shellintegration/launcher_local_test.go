package shellintegration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestLocalBashRcfile_SecretRidesTextNotEnv is the nocx-u7uh.21 acceptance
// assertion in its strongest form: the rendered LOCAL rcfile must carry the
// capability and the recovery fence as substituted TEXT (@CAP@/@RECOVERY@),
// and the environment block inside it must NOT — a value in the env block
// reaches /proc/<pid>/environ of every child, which is exactly the leak
// ADR-0024 decision 2 exists to prevent.
func TestLocalBashRcfile_SecretRidesTextNotEnv(t *testing.T) {
	opts := LaunchOptions{
		SessionID:   "sid-local-1",
		Enhanced:    true,
		Capability:  strings.Repeat("ab", 32), // 64 hex chars
		Recovery:    strings.Repeat("cd", 32),
		Lane:        "lane-1",
		Domain:      "dom-1",
		Epoch:       7,
		LifecycleFD: 3,
	}
	rc, err := LocalBashRcfile(opts)
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	// The secrets are in the rcfile text, assigned exactly once.
	if !strings.Contains(rc, "__nocx_cap='"+opts.Capability+"'") {
		t.Fatalf("rcfile must carry the capability as substituted text")
	}
	if !strings.Contains(rc, "__nocx_lc_recovery='"+opts.Recovery+"'") {
		t.Fatalf("rcfile must carry the recovery fence as substituted text")
	}
	// The env block (exported NOCX_* lines) must never contain them.
	env := launcherEnvBlock(opts)
	if strings.Contains(env, opts.Capability) {
		t.Fatalf("capability leaked into the exported environment block")
	}
	if strings.Contains(env, opts.Recovery) {
		t.Fatalf("recovery fence leaked into the exported environment block")
	}
	// The non-secret addressing does travel in the env block.
	for _, want := range []string{"NOCX_LIFECYCLE_LANE='lane-1'", "NOCX_LIFECYCLE_DOMAIN='dom-1'", "NOCX_LIFECYCLE_EPOCH=7", "NOCX_LIFECYCLE_FD=3", "NOCX_SESSION_ID='sid-local-1'"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env block must carry %s, got:\n%s", want, env)
		}
	}
}

// TestLocalBashRcfile_RequiresEnhancedSession pins the precondition: a
// conventional (non-enhanced) session has no session id anchor and no
// lifecycle config to embed; refusing beats rendering a rcfile that claims
// an authenticated channel that cannot exist.
func TestLocalBashRcfile_RequiresEnhancedSession(t *testing.T) {
	if _, err := LocalBashRcfile(LaunchOptions{Enhanced: false}); err == nil {
		t.Fatal("a conventional session must not render a lifecycle rcfile")
	}
}

// TestWriteLocalRcfile_MatchesSelfDeleteGuard pins the file naming: the bash
// rcfile template self-deletes on a BASH_SOURCE matching */nocx-bash.??????
// (exactly six characters). A longer random suffix would never be removed
// and every session would leave a file containing the capability in TMPDIR.
// The file is created 0600 with O_EXCL from the start.
func TestWriteLocalRcfile_MatchesSelfDeleteGuard(t *testing.T) {
	path, err := writeLocalRcfileIn("# test rcfile\n", t.TempDir())
	if err != nil {
		t.Fatalf("writeLocalRcfileIn: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	name := filepath.Base(path)
	if !regexp.MustCompile(`^nocx-bash\.[0-9a-f]{6}$`).MatchString(name) {
		t.Fatalf("rcfile name %q must match the template's */nocx-bash.?????? self-delete guard", name)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("rcfile mode = %o, want 0600 (it carries the capability)", st.Mode().Perm())
	}
}

func TestLocalEnhancedLaunch_AgentExecReplacesBootstrapShellBeforePrompt(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "open'code")
	opts := LaunchOptions{
		SessionID:   "sid-agent",
		Enhanced:    true,
		Capability:  strings.Repeat("ab", 32),
		Recovery:    strings.Repeat("cd", 32),
		Lane:        "lane-agent",
		Domain:      "dom-agent",
		Epoch:       1,
		LifecycleFD: 3,
		ArtifactDir: t.TempDir(),
		AgentExec:   agent,
	}

	for _, tc := range []struct {
		name  string
		shell string
		kind  ShellKind
	}{
		{name: "bash", shell: "/bin/bash", kind: ShellBash},
		{name: "zsh", shell: "/bin/zsh", kind: ShellZsh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch, err := LocalEnhancedLaunch(tc.shell, tc.kind, opts)
			if err != nil {
				t.Fatalf("LocalEnhancedLaunch: %v", err)
			}
			defer launch.Cleanup()

			artifact := launch.Args[1]
			if tc.kind == ShellZsh {
				for _, kv := range launch.Env {
					if dir, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
						artifact = filepath.Join(dir, ".zshrc")
						break
					}
				}
			}
			rc, err := os.ReadFile(artifact) //nolint:gosec // launcher-created path in this test's private directory
			if err != nil {
				t.Fatalf("read bootstrap artifact: %v", err)
			}
			quoted := ShellQuote(agent)
			text := string(rc)
			if !strings.Contains(text, "exec "+quoted) {
				t.Fatalf("bootstrap does not exec fixed agent %q", quoted)
			}
			if strings.Index(text, "exec "+quoted) < strings.Index(text, "__nocx_lc_init") {
				t.Fatal("agent exec must follow lifecycle bootstrap")
			}
			if strings.Contains(text, "\ncommand "+quoted) {
				t.Fatal("agent must replace the shell, not return to a prompt after it exits")
			}
		})
	}
}

func TestLocalEnhancedLaunch_AgentExecStartsAfterAuthenticatedHandshakeWithoutPrompt(t *testing.T) {
	for _, shellName := range []string{"bash", "zsh"} {
		t.Run(shellName, func(t *testing.T) {
			shellPath := requireShell(t, shellName)
			home := t.TempDir()
			workspace := t.TempDir()
			agent := filepath.Join(t.TempDir(), "opencode")
			if err := os.WriteFile(agent, []byte("#!/bin/sh\nif [ -e /dev/fd/3 ]; then fd=open; else fd=closed; fi\nprintf 'OPENCODE_STARTED cwd=[%s] lifecycle_fd=[%s]\\n' \"$PWD\" \"$fd\"\n"), 0o700); err != nil { //nolint:gosec // executable fixture
				t.Fatalf("write opencode fixture: %v", err)
			}

			opts := localTestOpts()
			opts.AgentExec = agent
			opts.ArtifactDir = t.TempDir()
			launch, err := LocalEnhancedLaunch(shellPath, LocalShellKind(shellPath), opts)
			if err != nil {
				t.Fatalf("LocalEnhancedLaunch: %v", err)
			}
			kernelFile, shellFile := lifecycleSocketpair(t)
			k := newFakeKernel(t, testCap)
			go k.serveFile(kernelFile)

			cmd := exec.Command(launch.Command, launch.Args...) //nolint:gosec // requireShell-resolved fixture
			cmd.Dir = workspace
			cmd.ExtraFiles = []*os.File{shellFile}
			cmd.Env = append(
				cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null"),
				append(launch.Env, "NOCX_LIFECYCLE_TIMEOUT_MS=5000")...,
			)
			ptmx, err := pty.Start(cmd)
			if err != nil {
				t.Fatalf("pty start: %v", err)
			}
			_ = shellFile.Close()
			session := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
			go session.readPump()
			t.Cleanup(func() {
				_ = ptmx.Close()
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				launch.Cleanup()
			})

			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				out := session.output()
				if strings.Contains(out, "OPENCODE_STARTED") {
					if !strings.Contains(out, "cwd=["+workspace+"]") {
						t.Fatalf("agent cwd output = %q, want %q", out, workspace)
					}
					if !strings.Contains(out, "lifecycle_fd=[open]") {
						t.Fatalf("agent did not inherit the accepted lifecycle descriptor: %q", out)
					}
					if strings.Contains(out, "\x1b]133;B") {
						t.Fatalf("a shell prompt appeared before opencode: %q", out)
					}
					if k.count("hello") != 1 {
						t.Fatalf("lifecycle hello count = %d, want 1 before agent launch", k.count("hello"))
					}
					select {
					case <-time.After(2 * time.Second):
						t.Fatal("bootstrap shell stayed alive after opencode exited")
					case <-processDone(cmd):
					}
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatalf("opencode never started; lifecycle=%v output=%q", k.events(), session.output())
		})
	}
}

func processDone(cmd *exec.Cmd) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return done
}
