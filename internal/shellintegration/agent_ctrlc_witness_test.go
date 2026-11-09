package shellintegration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A CTRL+C THAT THE TRAP'S OWN RUN MISSES STILL CANCELS (nocx-ouru5.1.11).
//
// CI captured a pane that said `^C` and then nothing, for as long as the test
// watched: the launch's open-question wait has no deadline, so it sat inside
// the wrapper. The pane's own bytes named what was missing —
//
//	bash: interrupt trap: line 2: unexpected EOF while looking for matching `)'
//
// — the window's INT trap was armed, the interrupt arrived, and the trap's run
// was lost, so nothing set the cancel fact. The wait's child died of the
// interrupt all the same, and that status is what this wrapper now reads
// (__nocx_agent_note_int): the trap is one witness, the child's death is the
// other, and the cancel no longer depends on the trap's fate.
//
// THE ORDER IS FORCED, not raced for. The stub that stands in for the
// enrolment prints WAITING and then waits in short children — the shape of the
// real loop's `sleep 1` — and the interrupt is sent to the shell's process
// GROUP, which is what a terminal does, so the child dies exactly as it does
// in a pane. The lost run is `trap ” INT`, which is precisely the state CI
// printed: children still die of the interrupt and no trap sets anything.
//
// The control is the same program with its trap left armed: the trap's door
// must keep working, and the two runs make "green" mean the cancel happened,
// not that something else ended the wait.
func TestBashAgentWrapperCancelsWhenTheInterruptsTrapRunIsLost(t *testing.T) {
	binary := requireShell(t, "bash")
	for _, tc := range []struct {
		name  string
		stub  string
		armed string
	}{
		{"trap run lost", "trap 'echo )' INT", "lost"},
		{"trap run arms", "", "armed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			binDir := t.TempDir()
			const agent = `#!/bin/sh
printf 'AGENT-RAN\n'
`
			// #nosec G306 -- a fake executable agent in the test's own directory.
			if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(agent), 0o755); err != nil {
				t.Fatal(err)
			}
			script := writeScriptFile(t, "nocx.bash", renderScript(bashScriptRaw, []string{"claude"}, DeliveryLocal))
			program := `script=$1
shift
source "$script"
__nocx_lc_active=1
# A Ctrl+C at a prompt is the prompt's business: outside a window the note buys
# nothing.
declare -F __nocx_agent_note_int > /dev/null && __nocx_agent_note_int 130
builtin printf 'OUTSIDE=%s\n' "${__nocx_agent_launch_cancelled:-unset}"
# The resolution window is not what this test is about.
__nocx_agent_launch_resolve() { __nocx_agent_launch_valid=1; return 0; }
__nocx_agent_handshake() {
    STUB_TRAP
    builtin printf 'WAITING\n'
    # The window's own wait, round for round: the product's helper and the
    # product's check, so what is under test is the real wait, not a copy.
    local i=0
    while (( i < 5 )); do
        i=$(( i + 1 ))
        __nocx_lc_wait_readable 1
        (( __nocx_agent_cancelled )) && return 130
    done
    builtin printf 'WAIT-ENDED-WITHOUT-A-CANCEL\n'
    return 0
}
__nocx_lc_send() { return 0; }
__nocx_agent_launch_cancel() { return 0; }
claude
builtin printf 'WRAPPER-RC=%s\n' "$?"
`
			program = strings.Replace(program, "STUB_TRAP", tc.stub, 1)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// #nosec G204 -- requireShell resolves the real shell under test.
			cmd := exec.CommandContext(ctx, binary, "-f", "-c", program, tc.armed, script)
			cmd.Env = cleanEnv(
				"HOME="+home, "TMPDIR="+t.TempDir(), "HISTFILE=/dev/null",
				"PATH="+binDir+":"+os.Getenv("PATH"), "NOCX_SHELL_INTEGRATION=1",
			)
			// Its own process group: the interrupt is sent to the GROUP, the way a
			// terminal delivers it.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			out := &lockedBuffer{}
			cmd.Stdout = out
			cmd.Stderr = out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if !out.waitFor("WAITING", 10*time.Second) {
				_ = cmd.Process.Kill()
				t.Fatalf("the stub never started waiting; output=%q", out.String())
			}
			// PRESS WHERE A PERSON PRESSES: inside the wait, while its child is
			// running, which is where the loop spends its time. Waiting for the
			// child to exist is waiting on the fact, not on a delay — the
			// preamble between WAITING and the first child is milliseconds, and
			// an interrupt there is the trap's business alone.
			if !waitForChild(cmd.Process.Pid, 10*time.Second) {
				_ = cmd.Process.Kill()
				t.Fatalf("the wait never started a child; output=%q", out.String())
			}
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
				t.Fatalf("press Ctrl+C on the group: %v", err)
			}
			err := cmd.Wait()
			text := out.String()
			if ctx.Err() != nil {
				t.Fatalf("the wrapper did not finish: %v; output=%q", ctx.Err(), text)
			}
			if strings.Contains(text, "OUTSIDE=1") {
				t.Errorf("a note outside a cancel window set the fact; output=%q", text)
			}
			if strings.Contains(text, "AGENT-RAN") {
				t.Errorf("a cancelled launch started the agent; output=%q", text)
			}
			if !strings.Contains(text, "nocx: cancelled — claude was not started") {
				t.Errorf("the pane did not say the launch was cancelled; output=%q", text)
			}
			if !strings.Contains(text, "WRAPPER-RC=130") {
				t.Errorf("the wrapper must report the interrupt, got output=%q (err=%v)", text, err)
			}
			if strings.Contains(text, "WAIT-ENDED-WITHOUT-A-CANCEL") {
				t.Errorf("the wait ran to its end after the interrupt; output=%q", text)
			}
		})
	}
}

// waitForChild reports whether the shell is running a child — the wait's own
// blocking command — within d. A zombie is not running: it is a child that has
// already exited and not yet been reaped, and an interrupt that lands after it
// exited is the trap's business alone.
func waitForChild(pid int, d time.Duration) bool {
	list := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		// #nosec G304 -- a /proc path this test composed from a pid it started.
		if data, err := os.ReadFile(list); err == nil {
			for _, field := range strings.Fields(string(data)) {
				// #nosec G304 -- the shell's own /proc entry, named by the pid.
				if stat, statErr := os.ReadFile("/proc/" + field + "/stat"); statErr == nil {
					// Field 3 of /proc/<pid>/stat is the state, after "pid (comm) ".
					text := string(stat)
					if i := strings.LastIndex(text, ") "); i >= 0 && len(text) > i+2 && text[i+2] != 'Z' {
						// And it must be the wait's own command, already exec'd:
						// the moment between fork and exec is bash's, and an
						// interrupt there is the trap's business alone.
						// #nosec G304 -- the same entry, read for its name.
						if comm, commErr := os.ReadFile("/proc/" + field + "/comm"); commErr == nil &&
							strings.TrimSpace(string(comm)) == "sleep" {
							return true
						}
					}
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// lockedBuffer is the pane: written by the shell's pipe, read by the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (b *lockedBuffer) waitFor(text string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), text) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
