package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/waittest"
)

var errSpawnFailed = errors.New("spawn failed")

// stubPTY is a localPTY that starts nothing: the failure-injection seam the
// tests below need, because Spawn cannot be made to fail at the PTY any other
// way.
type stubPTY struct {
	fakeProcess
}

func (p *stubPTY) Write(b []byte) (int, error) { return len(b), nil }

func (p *stubPTY) Dir() string { return "/" }

func (p *stubPTY) SignalProcessGroup(int, syscall.Signal) error { return nil }

// InterruptWrite, WaitReadable and RawReadChunk satisfy localPTY's owner
// seam (nocx-6q1uh.3). stubPTY starts nothing and nothing ever spawns a session
// I/O owner over it, so these are never called; they exist only so *stubPTY
// still satisfies localPTY, the way Dir and SignalProcessGroup already had to.
func (p *stubPTY) InterruptWrite() error { return nil }

func (p *stubPTY) WaitReadable(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (p *stubPTY) RawReadChunk([]byte) (int, bool, error) { return 0, false, io.EOF }

// openDescriptors is how many descriptors this PROCESS holds. /dev/fd is the
// one spelling both targets answer — on Linux it is a symlink to
// /proc/self/fd, on macOS it is the fdesc filesystem — so the count needs no
// build tag. It is read the same way on both sides of the measurement, so the
// directory's own descriptor cancels out and only the DELTA is claimed.
func openDescriptors(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("this platform does not publish /dev/fd: %v", err)
	}
	return len(entries)
}

// The invariant, stated as the interval it actually is: a descriptor Spawn
// opens, and any artefact it writes, exists from the moment it opens it until
// either the caller receives a process that owns it or Spawn returns an error —
// and NOTHING this function opened outlives a failed spawn, on any arm.
//
// The helper is a daemon that lives as long as the host session, so an arm
// that leaks one descriptor per occurrence leaks for the life of the machine's
// login. That is nocx-k6p18.28: the zsh bootstrap-write arm closed the parent
// end of the lifecycle socketpair and returned with the child end open. That
// arm is gone with the typed bootstrap line (nocx-1ebrs.8), and the artefact
// this test had to grow is the zsh tier's transient ZDOTDIR: a failed spawn
// must not leave one in the temporary directory either.
//
// One case remains, and it stands for every arm: the socketpair and the
// launch-build arms cannot be provoked here (they fail only when the kernel
// refuses a socketpair or a pipe), and all of them run against ONE unwind —
// the check that keeps them honest is that there is no second place to forget.
func TestLocalSpawner_NoDescriptorOrArtefactOutlivesAFailedSpawn(t *testing.T) {
	tmp := t.TempDir()
	// os.TempDir() reads TMPDIR at call time, so the zsh tier writes its
	// transient directory here and nowhere else.
	t.Setenv("TMPDIR", tmp)

	s := &LocalSpawner{
		log:   log.NewSlogAdapter(nil),
		shell: Shell{Path: "/bin/zsh"},
		openPTY: func(log.Logger, pty.Config) (localPTY, error) {
			return nil, errSpawnFailed
		},
	}
	req := SpawnRequest{
		SessionID: "sess-1",
		Cols:      80,
		Rows:      24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane:       "lane-1",
			Domain:     "dom-1",
			Epoch:      7,
			Capability: strings.Repeat("a", 64),
		},
	}

	// One failed spawn before the measurement, so that whatever the runtime
	// opens lazily on the first pass is already open and the delta is this
	// function's own.
	mustFailSpawn(t, s, req)

	before := openDescriptors(t)
	mustFailSpawn(t, s, req)
	after := openDescriptors(t)

	if after != before {
		t.Fatalf("failed spawn leaked %d descriptor(s): %d open before, %d after", after-before, before, after)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read the temporary directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a failed spawn left %d artefact(s) behind: %v", len(entries), names)
	}
}

func mustFailSpawn(t *testing.T, s *LocalSpawner, req SpawnRequest) {
	t.Helper()
	proc, err := s.Spawn(req)
	if err == nil {
		t.Fatal("spawn succeeded; this test needs the failure arm")
	}
	if proc != nil {
		t.Fatalf("a failed spawn returned a process: %#v", proc)
	}
}

// TestLocalSpawner_CarriesEachSpawnsOwnToolEndpointIntoItsLaunchScript is
// nocx-2tesu's rendering half, re-shaped by nocx-50w7p.18: every enhanced
// local pane carries ITS OWN request's tool endpoint as NOCX_TOOL_SOCKET, and
// the pane's endpoint is never a value this spawner holds.
//
// The script is read twice from ONE spawner, and that repetition IS the
// assertion — it is what the daemon-scoped field could not satisfy. A helper
// endpoint socket is keyed by the generation rather than by a caller, so one
// account's daemon serves several coordinators at once (D12); a value held
// once (the old constructor argument, fed from this process's own environment)
// rendered the first coordinator's socket into a pane another one asked for.
//
// The assertion reads the rendered launch SCRIPT rather than the process
// environment: for the in-memory local tier the variable travels as an
// `export` line inside the rcfile/script delivered over the inherited pipe
// (LaunchOptions.AgentToolSocketPath -> launcherEnvBlock), exactly like the
// remote tier's carrier already does — never as a second, ad hoc route
// through the raw exec environment.
func TestLocalSpawner_CarriesEachSpawnsOwnToolEndpointIntoItsLaunchScript(t *testing.T) {
	cases := []struct {
		name     string
		session  string
		endpoint string
	}{
		{"the app that started the daemon", "sess-tool-socket-a", "/run/user/1000/nocx/tool.sock"},
		{"another coordinator of the same account on the same daemon", "sess-tool-socket-b", "/run/user/1000/nocx-dev/tool.sock"},
	}
	var script string
	s := &LocalSpawner{
		log:   log.NewSlogAdapter(nil),
		shell: Shell{Path: "/bin/bash"},
		openPTY: func(l log.Logger, cfg pty.Config) (localPTY, error) {
			if len(cfg.ExtraFiles) == 0 {
				t.Fatal("no script pipe was attached for the enhanced launch")
			}
			data, err := io.ReadAll(cfg.ExtraFiles[0])
			if err != nil {
				t.Fatalf("read the rendered launch script: %v", err)
			}
			script = string(data)
			return &stubPTY{}, nil
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script = ""
			req := SpawnRequest{SessionID: tc.session, Cols: 80, Rows: 24, AgentToolEndpoint: tc.endpoint}
			if _, err := s.Spawn(req); err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if !strings.Contains(script, "NOCX_TOOL_SOCKET='"+tc.endpoint+"'") {
				t.Fatalf("the launch script did not export this spawn's own tool endpoint %s: %q", tc.endpoint, script)
			}
		})
	}
}

// TestLocalSpawner_OmitsToolSocketWhenTheRequestNamesNone is criterion 2's
// half that belongs to this seam: a request that names no endpoint (the
// zero value — the coordinator that asked runs no tool endpoint, which
// cmd/nocx-server answers nil, nil for) renders a script that asks for
// nothing, so the shell's own refusal text is what a user sees, rather than a
// pane silently pointed at an empty path.
func TestLocalSpawner_OmitsToolSocketWhenTheRequestNamesNone(t *testing.T) {
	var script string
	s := &LocalSpawner{
		log:   log.NewSlogAdapter(nil),
		shell: Shell{Path: "/bin/bash"},
		openPTY: func(l log.Logger, cfg pty.Config) (localPTY, error) {
			if len(cfg.ExtraFiles) == 0 {
				t.Fatal("no script pipe was attached for the enhanced launch")
			}
			data, err := io.ReadAll(cfg.ExtraFiles[0])
			if err != nil {
				t.Fatalf("read the rendered launch script: %v", err)
			}
			script = string(data)
			return &stubPTY{}, nil
		},
	}
	req := SpawnRequest{SessionID: "sess-no-tool-socket", Cols: 80, Rows: 24}
	if _, err := s.Spawn(req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// NOCX_TOOL_SOCKET= (an assignment) is what launcherEnvBlock renders when
	// AgentToolSocketPath is set; the bare name still appears elsewhere in
	// the embedded script, which READS the variable (nocx.bash's own
	// __nocx_agent_tool_socket="${NOCX_TOOL_SOCKET:-}"), so a plain substring
	// check for the name would fail on the script's own logic rather than on
	// what this spawner rendered.
	if strings.Contains(script, "NOCX_TOOL_SOCKET=") {
		t.Fatalf("the launch script exported a tool socket no request named: %q", script)
	}
}

// A localProcess must keep answering the three questions service.go asks it by
// TYPE ASSERTION. They are unchecked at compile time there, so they are
// checked here: a localPTY that omitted Dir or SignalProcessGroup would leave
// the helper unable to signal a job on the host and nothing would say so.
func TestLocalProcess_KeepsTheAssertedSeams(t *testing.T) {
	var proc Process = &localProcess{localPTY: &stubPTY{}}
	if _, ok := proc.(ProcessGroupSignaller); !ok {
		t.Error("localProcess no longer satisfies ProcessGroupSignaller")
	}
	if _, ok := proc.(interface{ Cwd() string }); !ok {
		t.Error("localProcess no longer answers Cwd")
	}
	if _, ok := proc.(interface{ ProcessGroup() int }); !ok {
		t.Error("localProcess no longer answers ProcessGroup")
	}
	if _, ok := proc.(LifecycleProcess); !ok {
		t.Error("localProcess no longer satisfies LifecycleProcess")
	}
}

// THE PANE MUST BE GIVEN A RUNNABLE HELPER, NOT A BARE NAME (nocx-o36tr).
//
// The shell wrapper's fallback is `${NOCX_AGENT_HELPER_PATH:-nocx-helper}`, and
// nothing in this repository puts a `nocx-helper` on PATH: `make helpers`
// writes gzipped per-platform artifacts for remote deployment. So an
// unconfigured pane stages an MCP server whose command cannot be executed, and
// Claude Code shows "Nocx MCP Server — failed" with every other fact correct.
//
// The daemon that forks the shell IS the installed helper, so the path it
// hands down is its own, and the generation the pane execs is the generation
// that owns it.
func TestLocalSpawner_CarriesTheHelperPathIntoTheLaunchScript(t *testing.T) {
	const helper = "/home/dev/.nocx/helper/2-linux-amd64-abc/nocx-helper"
	var script string
	s := &LocalSpawner{
		log:             log.NewSlogAdapter(nil),
		shell:           Shell{Path: "/bin/bash"},
		agentHelperPath: helper,
		openPTY: func(l log.Logger, cfg pty.Config) (localPTY, error) {
			if len(cfg.ExtraFiles) == 0 {
				t.Fatal("no script pipe was attached for the enhanced launch")
			}
			data, err := io.ReadAll(cfg.ExtraFiles[0])
			if err != nil {
				t.Fatalf("read the rendered launch script: %v", err)
			}
			script = string(data)
			return &stubPTY{}, nil
		},
	}
	req := SpawnRequest{SessionID: "sess-helper-path", Cols: 80, Rows: 24}
	if _, err := s.Spawn(req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !strings.Contains(script, "NOCX_AGENT_HELPER_PATH='"+helper+"'") {
		t.Fatalf("the launch script did not export the helper path: %q", script)
	}
}

// And a spawner nobody told one exports nothing, so the shell's own PATH
// fallback still stands rather than the pane being pointed at an empty path.
func TestLocalSpawner_OmitsTheHelperPathWhenNoneIsConfigured(t *testing.T) {
	var script string
	s := &LocalSpawner{
		log:   log.NewSlogAdapter(nil),
		shell: Shell{Path: "/bin/bash"},
		openPTY: func(l log.Logger, cfg pty.Config) (localPTY, error) {
			data, err := io.ReadAll(cfg.ExtraFiles[0])
			if err != nil {
				t.Fatalf("read the rendered launch script: %v", err)
			}
			script = string(data)
			return &stubPTY{}, nil
		},
	}
	req := SpawnRequest{SessionID: "sess-no-helper", Cols: 80, Rows: 24}
	if _, err := s.Spawn(req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// The EXPORT line, not the name: the wrapper's own fallback
	// `${NOCX_AGENT_HELPER_PATH:-nocx-helper}` is in the embedded script, so
	// a test for the bare name finds the thing it is checking is absent.
	if strings.Contains(script, "NOCX_AGENT_HELPER_PATH='") {
		t.Fatalf("an unconfigured spawner exported a helper path: %q", script)
	}
}

// realPTY adapts internal/pty's constructor to the seam Spawn holds, for the
// two tests that need a real terminal. A nil *pty.LocalPty returned through
// the interface would not be nil, so the error is what is checked first.
func realPTY(l log.Logger, cfg pty.Config) (localPTY, error) {
	lp, err := pty.NewLocal(l, cfg)
	if err != nil {
		return nil, err
	}
	return lp, nil
}

// TestLocalSpawner_AnIntegratedShellKnowsItBeforeItsStartupFilesRun is
// nocx-1ebrs.8's second half, and it lives here because this is the only
// place the environment is decided (nocx-1ebrs.8's report names the seam).
//
// THE SHAPE IT PINS is the one an ordinary machine lost integration to: a
// terminal WRAPPER installs a pre block in a login startup file that RE-EXECS
// the login shell inside a pty of the wrapper's own, keeping descriptors 0-2
// and nothing else — so a script nocx delivered on a descriptor is gone in
// the inner shell, and the typed bootstrap line is echoed by the outer one and
// run again in the inner one. Users guard those blocks with the variable this
// integration publishes, so the guard is only true if the variable is in the
// SPAWNED SHELL'S ENVIRONMENT — before any startup file runs. An export inside
// the rcfile happens at the .zshrc phase, which is after ~/.zshenv and
// ~/.zprofile, and is therefore too late for exactly this case.
//
// The fixture writes what it saw to a file rather than to the terminal: the
// question is about the environment of the process, and reading it off a pty
// would make the assertion a race with a pump.
func TestLocalSpawner_AnIntegratedShellKnowsItBeforeItsStartupFilesRun(t *testing.T) {
	zsh := requireIntegrationZsh(t)
	home := t.TempDir()
	// The shape, with no program named: a login-phase pre block that stands
	// aside for a terminal which already integrated the shell and takes it
	// over otherwise.
	profile := `if [[ -n "$NOCX_SHELL_INTEGRATION" ]]; then
	print -r -- set > "$HOME/marker"
else
	print -r -- unset > "$HOME/marker"
fi
`
	if err := os.WriteFile(filepath.Join(home, ".zprofile"), []byte(profile), 0o600); err != nil {
		t.Fatalf("write fixture .zprofile: %v", err)
	}

	s := &LocalSpawner{
		log:     log.NewSlogAdapter(nil),
		shell:   Shell{Path: zsh},
		openPTY: realPTY,
	}
	proc, err := s.Spawn(SpawnRequest{
		SessionID: "sess-marker", Cwd: home, Cols: 80, Rows: 24,
		Env: map[string]string{"HOME": home},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() { _ = proc.Close() }()

	if got := waitForMarker(t, home); got != "set" {
		t.Fatalf("a login startup file saw the activation marker as %q: an integrated pane must announce itself before any startup file runs", got)
	}
}

// TestLocalSpawner_APlainShellNeverClaimsToBeIntegrated is the pair the rule
// above needs: the same marker, asked of a shell this launch did NOT integrate
// (an explicit command, so the spawner takes its plain arm). A startup file
// that guards on the marker must not stand aside for an integration that does
// not exist.
func TestLocalSpawner_APlainShellNeverClaimsToBeIntegrated(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "marker")

	s := &LocalSpawner{
		log: log.NewSlogAdapter(nil),
		// An explicit command is the plain arm: the spawner integrates
		// nothing, so nothing may say it did. `sh` is on every target and
		// needs no rc file to answer one question.
		shell: Shell{Path: "/bin/sh", Args: []string{
			"-c",
			`if [ -n "${NOCX_SHELL_INTEGRATION:-}" ]; then echo set; else echo unset; fi > ` + marker,
		}},
		openPTY: realPTY,
	}
	proc, err := s.Spawn(SpawnRequest{SessionID: "sess-plain", Cwd: home, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() { _ = proc.Close() }()

	if got := waitForMarker(t, home); got != "unset" {
		t.Fatalf("a shell this launch did not integrate started with the activation marker %q", got)
	}
}

// requireIntegrationZsh is requireShell's rule for this package: a missing
// shell must stop the test loudly rather than skip it.
func requireIntegrationZsh(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatalf("zsh is required by this test and missing from PATH: %v", err)
	}
	return path
}

// waitForMarker waits until the fixture startup file has written its verdict.
// It is a file appearing, not a duration: the shell writes it during startup
// and the test has no other signal that the login phase ran.
func waitForMarker(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "marker")
	var got string
	waittest.WaitForTimeoutDetail(t, "the fixture startup file to report", 20*time.Second,
		func() string { return fmt.Sprintf("%s = %q", path, got) },
		func() bool {
			body, err := os.ReadFile(path) //nolint:gosec // the test's own fixture directory.
			if err != nil {
				return false
			}
			got = strings.TrimSpace(string(body))
			return got != ""
		})
	return got
}

func TestLocalSpawner_FishStartupRefusalPreservesFactsWithoutCredentials(t *testing.T) {
	_, logger := logtest.New(t)
	shell := filepath.Join(t.TempDir(), "fish")
	body := "#!/bin/sh\n[ \"$1\" = --no-config ] && [ \"$2\" = --command ] && [ \"$#\" = 3 ] || exit 91\nprintf '4.8.0\\ne# incompatible native startup\\n'\n"
	if err := os.WriteFile(shell, []byte(body), 0o700); err != nil { //nolint:gosec // executable discovery fixture owned by t.TempDir
		t.Fatal(err)
	}
	var opened bool
	s := &LocalSpawner{
		log: logger, shell: Shell{Path: shell},
		agentHelperPath: "/public/helper",
		openPTY: func(_ log.Logger, cfg pty.Config) (localPTY, error) {
			opened = true
			if cfg.Command != shell || len(cfg.Args) != 1 || cfg.Args[0] != "-i" {
				t.Fatalf("fallback launch = %q %q, want native interactive Fish", cfg.Command, cfg.Args)
			}
			if len(cfg.ExtraFiles) != 0 {
				t.Fatal("fallback inherited a private credential or lifecycle descriptor")
			}
			for _, entry := range cfg.Env {
				if strings.HasPrefix(entry, "NOCX_SHELL_INTEGRATION=") ||
					strings.HasPrefix(entry, "NOCX_TOOL_SOCKET=") ||
					strings.Contains(entry, "private-tool-token") ||
					strings.Contains(entry, strings.Repeat("a", 64)) {
					t.Fatalf("fallback inherited private integration environment: %q", entry)
				}
			}
			return &stubPTY{fakeProcess: fakeProcess{shell: shell}}, nil
		},
	}
	proc, err := s.Spawn(SpawnRequest{
		SessionID: "fish-refusal", Cols: 80, Rows: 24, AgentToolToken: "private-tool-token",
		AgentToolEndpoint: "/private/tool.sock",
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "fish-lane", Domain: "fish-domain", Epoch: 1,
			Capability: strings.Repeat("a", 64), Recovery: strings.Repeat("b", 64),
			RecoveryEpisodeID: "rec-0123456789abcdef0123456789abcdef",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := proc.Close(); err != nil {
			t.Errorf("closing conventional Fish: %v", err)
		}
	})
	local, ok := proc.(*localProcess)
	if !opened || !ok {
		t.Fatal("conventional fallback never opened the shell")
	}
	if local.Lifecycle() != nil {
		t.Fatal("conventional Fish retained a private lifecycle transport")
	}
	refusal, version := local.IntegrationLaunchFacts()
	if refusal != "unsupported-fish-startup" || version != "4.8.0" {
		t.Fatalf("launch facts lost the startup compatibility reason: %q/%q", refusal, version)
	}
}
