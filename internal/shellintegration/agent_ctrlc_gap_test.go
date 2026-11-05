package shellintegration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Ctrl+C THAT LANDS BETWEEN THE TWO WINDOWS STILL CANCELS THE LAUNCH
// (nocx-atpw5.1.42).
//
// The resolution and the enrolment each arm their own INT trap, and each arm
// RESETS its window's flag — deliberately, so a stale flag from an earlier
// window cannot leak into this one. The interrupt of the gap between them is
// therefore set by the resolution's trap and forgotten by the enrolment's, and
// the wrapper went on to run the agent: the CI report's `^C` followed by
// AGENT-RAN, and the cancel sentence never printed.
//
// THE ORDER IS FORCED, not raced for. The stub standing in for the resolution
// takes its answer and then sets the wrapper's flag (the interrupt that landed
// right after it); the stub standing in for the enrolment clears the window
// flag exactly as the next arm does and succeeds. That is the gap, in one line
// each, and it is the same in both shells.
func TestAgentWrapperCancelsWhenTheInterruptLandedBetweenTheWindows(t *testing.T) {
	for _, shell := range []struct {
		name string
		raw  string
	}{
		{"bash", bashScriptRaw},
		{"zsh", zshScriptRaw},
	} {
		t.Run(shell.name, func(t *testing.T) {
			binary := requireShell(t, shell.name)
			home := t.TempDir()
			binDir := t.TempDir()
			const agent = `#!/bin/sh
printf 'AGENT-RAN\n'
`
			// #nosec G306 -- a fake executable agent in the test's own directory.
			if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(agent), 0o755); err != nil {
				t.Fatal(err)
			}
			script := writeScriptFile(t, "nocx."+shell.name, renderScript(shell.raw, []string{"claude"}, DeliveryLocal))
			const program = `script=$1
shift
source "$script"
__nocx_lc_active=1
# The resolution took its answer, and the interrupt landed right after it.
__nocx_agent_launch_resolve() { __nocx_agent_launch_cancelled=1; return 0; }
# The enrolment's own arm: it resets ITS window's flag, which is what forgot
# the interrupt, and it succeeds.
__nocx_agent_enrol() { __nocx_agent_cancelled=0; __nocx_agent_rid=a-1; return 0; }
__nocx_lc_send() { return 0; }
__nocx_agent_launch_cancel() { return 0; }
claude
builtin printf 'WRAPPER-RC=%s\n' "$?"
`
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// #nosec G204 -- requireShell resolves the real shell under test.
			cmd := exec.CommandContext(ctx, binary, "-f", "-c", program, shell.name, script)
			cmd.Env = cleanEnv(
				"HOME="+home, "ZDOTDIR="+home, "TMPDIR="+t.TempDir(), "HISTFILE=/dev/null",
				"PATH="+binDir+":"+os.Getenv("PATH"), "NOCX_SHELL_INTEGRATION=1",
			)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("the wrapper did not finish: %v; output=%q", ctx.Err(), out)
			}
			text := string(out)
			if strings.Contains(text, "AGENT-RAN") {
				t.Fatalf("an interrupt between the windows started the agent; output=%q", text)
			}
			if !strings.Contains(text, "nocx: cancelled — claude was not started") {
				t.Errorf("the pane did not say the launch was cancelled; output=%q", text)
			}
			if !strings.Contains(text, "WRAPPER-RC=130") {
				t.Errorf("the wrapper must report the interrupt, got output=%q (err=%v)", text, err)
			}
		})
	}
}
