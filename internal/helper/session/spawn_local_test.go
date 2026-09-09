package session

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
)

var errSpawnFailed = errors.New("spawn failed")

// stubPTY is a localPTY that starts nothing. It exists for one reason: the
// bootstrap write is the only external call Spawn makes that a real terminal
// cannot be made to fail on demand — a PTY master rejects a write only once
// its slave has no openers left, which is a race against the shell's exec and
// never a fact a test may wait on (AGENTS.md: a test may not depend on timing).
type stubPTY struct {
	fakeProcess
	writeErr error
}

func (p *stubPTY) Write(b []byte) (int, error) {
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	return len(b), nil
}

func (p *stubPTY) Dir() string { return "/" }

func (p *stubPTY) SignalProcessGroup(int, syscall.Signal) error { return nil }

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
// opens exists from the moment it opens it until either the caller receives a
// process that owns it or Spawn returns an error — and NOTHING this function
// opened outlives a failed spawn, on any arm.
//
// The helper is a daemon that lives as long as the host session, so an arm
// that leaks one descriptor per occurrence leaks for the life of the machine's
// login. That is nocx-k6p18.28: the zsh bootstrap-write arm closed the parent
// end of the lifecycle socketpair and returned with the child end open.
//
// Both arms below run against ONE unwind, which is why two cases can stand for
// four: the socketpair arm and the launch-build arm are the same call. Neither
// of those two can be provoked here — they fail only when the kernel refuses a
// socketpair or a pipe — so the check that keeps them honest is that there is
// no second place left to forget.
func TestLocalSpawner_NoDescriptorOutlivesAFailedSpawn(t *testing.T) {
	cases := []struct {
		name string
		open func(log.Logger, pty.Config) (localPTY, error)
	}{
		{
			name: "the PTY never starts",
			open: func(log.Logger, pty.Config) (localPTY, error) {
				return nil, errSpawnFailed
			},
		},
		{
			name: "the bootstrap write fails",
			open: func(log.Logger, pty.Config) (localPTY, error) {
				return &stubPTY{writeErr: errSpawnFailed}, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &LocalSpawner{
				log: log.NewSlogAdapter(nil),
				// zsh is the only kind with a non-empty Bootstrap, so it is
				// the only kind that reaches the write arm at all.
				shell:   Shell{Path: "/bin/zsh"},
				openPTY: tc.open,
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

			// One failed spawn before the measurement, so that whatever the
			// runtime opens lazily on the first pass is already open and the
			// delta is this function's alone.
			mustFailSpawn(t, s, req)

			before := openDescriptors(t)
			mustFailSpawn(t, s, req)
			after := openDescriptors(t)

			if after != before {
				t.Fatalf("failed spawn leaked %d descriptor(s): %d open before, %d after", after-before, before, after)
			}
		})
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

// TestLocalSpawner_CarriesTheConfiguredToolSocketIntoTheLaunchScript is
// nocx-2tesu's acceptance criterion 1, at the seam LocalSpawner actually
// owns: given this backend told the spawner its tool endpoint's socket path
// (agentToolSocketPath, set once at NewLocalSpawner — a property of this
// daemon's whole life, never of one request), every enhanced local pane it
// starts carries NOCX_TOOL_SOCKET.
//
// The assertion reads the rendered launch SCRIPT rather than the process
// environment: for the in-memory local tier the variable travels as an
// `export` line inside the rcfile/script delivered over the inherited pipe
// (LaunchOptions.AgentToolSocketPath -> launcherEnvBlock), exactly like the
// remote tier's carrier already does — never as a second, ad hoc route
// through the raw exec environment.
func TestLocalSpawner_CarriesTheConfiguredToolSocketIntoTheLaunchScript(t *testing.T) {
	const sock = "/run/nocx/tool.sock"
	var script string
	s := &LocalSpawner{
		log:                 log.NewSlogAdapter(nil),
		shell:               Shell{Path: "/bin/bash"},
		agentToolSocketPath: sock,
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
	req := SpawnRequest{SessionID: "sess-tool-socket", Cols: 80, Rows: 24}
	if _, err := s.Spawn(req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !strings.Contains(script, "NOCX_TOOL_SOCKET='"+sock+"'") {
		t.Fatalf("the launch script did not export the configured tool socket: %q", script)
	}
}

// TestLocalSpawner_OmitsToolSocketWhenNoneIsConfigured is criterion 2's
// half that belongs to this seam: a spawner nobody told a socket path (the
// zero value — startToolEndpoint answered nil, nil) renders a script that
// asks for nothing, so the shell's own refusal text is what a user sees,
// rather than a pane silently pointed at an empty path.
func TestLocalSpawner_OmitsToolSocketWhenNoneIsConfigured(t *testing.T) {
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
		t.Fatalf("the launch script exported a tool socket nobody configured: %q", script)
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
		log:                 log.NewSlogAdapter(nil),
		shell:               Shell{Path: "/bin/bash"},
		agentToolSocketPath: "/run/nocx/tool.sock",
		agentHelperPath:     helper,
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
		log:                 log.NewSlogAdapter(nil),
		shell:               Shell{Path: "/bin/bash"},
		agentToolSocketPath: "/run/nocx/tool.sock",
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
