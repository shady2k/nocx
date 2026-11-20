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

// THE INTERRUPT THAT ARRIVES AS THE RESOLUTION'S ANSWER IS READ IS STILL THE
// PERSON'S CANCELLATION (nocx-pd55v).
//
// The resolution's wait arms its own INT trap and clears ITS window's flag
// when the wait ends, deliberately, so a stale flag from an earlier window
// cannot leak into this one. The wrapper's own flag is a different fact and it
// survives: an interrupt delivered while the answer was being consumed, or in
// the instant after, is the same gesture as one delivered a millisecond
// earlier. Without a check of that flag after the resolution succeeds, the
// wrapper went on to enrol — spending the launch ticket for an agent that the
// last door then refused to run — so the pane said "cancelled" while the
// backend had already been asked to admit a launch nobody would perform.
//
// THE ORDER IS FORCED, not raced for. The stub standing in for the resolution
// returns exactly what a successful one returns (0, with the record parsed)
// and records the interrupt that landed inside its window; the stub standing
// in for the enrolment is the one thing this wrapper must not reach. A real
// shell cannot be made to deliver a signal at that seam on demand, so the seam
// is what is stubbed — and what is asserted is what the person sees and what
// the backend is asked, both of which the shell alone decides.
func TestAgentWrapperCancelsWhenTheInterruptLandedAsTheResolutionWasRead(t *testing.T) {
	for _, shell := range []struct {
		name string
		raw  string
	}{
		{"bash", bashScriptRaw},
		{"zsh", zshScriptRaw},
	} {
		t.Run(shell.name, func(t *testing.T) {
			text := runWrapperScript(t, shell.name, shell.raw, `__nocx_agent_launch_resolve() {
    __nocx_agent_launch_agent=claude
    __nocx_agent_launch_ticket=ticket-1
    __nocx_agent_launch_cancelled=1
    return 0
}
__nocx_agent_enrol() {
    __nocx_agent_cancelled=0
    __nocx_agent_rid=a-1
    builtin printf 'ENROL-CALLED\n'
    return 0
}
__nocx_lc_send() { builtin printf 'SEND %s\n' "$*"; return 0; }
`)
			if strings.Contains(text, "AGENT-RAN") {
				t.Fatalf("a cancelled launch started the agent; output=%q", text)
			}
			if strings.Contains(text, "ENROL-CALLED") {
				t.Errorf("a launch cancelled as its resolution was read enrolled anyway; output=%q", text)
			}
			if !strings.Contains(text, "nocx: cancelled — claude was not started") {
				t.Errorf("the pane did not say the launch was cancelled; output=%q", text)
			}
			if !strings.Contains(text, "SEND agent_launch_cancel") {
				t.Errorf("the launch's ticket was not cancelled; output=%q", text)
			}
			if !strings.Contains(text, "WRAPPER-RC=130") {
				t.Errorf("the cancelled launch must report the interrupt; output=%q", text)
			}
		})
	}
}

// AN ENROLMENT THAT WAS ADMITTED BEFORE THE CANCEL LANDED LEAVES AN INTERVAL
// NOBODY WILL RUN IN (nocx-pd55v).
//
// The enrolment asks the backend to admit this pane, and an admitted enrolment
// opens the interval that the pane's own session closes. When the wrapper
// cancels between the enrolment and the agent, no agent ever runs inside that
// interval and the code that would have closed it — at the far end of the
// agent's life — is never reached. The interval then hangs open until the
// session ends, which is not the same thing: a live interval is what the
// backend reads as "an agent is running in this pane".
//
// The enrolment is stubbed to be admitted and then to record the interrupt, so
// the state under test is the one the real wrapper reaches: rc 0 from an
// accepted enrolment, and the person's cancel already recorded.
func TestAgentWrapperWithdrawsAnIntervalItAdmittedButWillNotRunIn(t *testing.T) {
	for _, shell := range []struct {
		name string
		raw  string
	}{
		{"bash", bashScriptRaw},
		{"zsh", zshScriptRaw},
	} {
		t.Run(shell.name, func(t *testing.T) {
			text := runWrapperScript(t, shell.name, shell.raw, `__nocx_agent_launch_resolve() {
    __nocx_agent_launch_cancelled=0
    return 0
}
__nocx_agent_enrol() {
    __nocx_agent_cancelled=0
    __nocx_agent_rid=a-1
    __nocx_agent_launch_cancelled=1
    return 0
}
__nocx_lc_send() { builtin printf 'SEND %s\n' "$*"; return 0; }
__nocx_lc_read_agent_answer() { return 0; }
`)
			if strings.Contains(text, "AGENT-RAN") {
				t.Fatalf("a cancelled launch started the agent; output=%q", text)
			}
			if !strings.Contains(text, "SEND agent_withdraw") {
				t.Fatalf("the interval the enrolment opened was never withdrawn; output=%q", text)
			}
			if !strings.Contains(text, `,"request":"a-1"`) {
				t.Errorf("the withdrawal did not name the enrolment's interval; output=%q", text)
			}
			if !strings.Contains(text, "nocx: cancelled — claude was not started") {
				t.Errorf("the pane did not say the launch was cancelled; output=%q", text)
			}
			if !strings.Contains(text, "WRAPPER-RC=130") {
				t.Errorf("the cancelled launch must report the interrupt; output=%q", text)
			}
		})
	}
}

// runWrapperScript sources the rendered script in the real shell with the
// caller's stubs defined after it, types the agent's name, and returns
// everything the pane printed.
func runWrapperScript(t *testing.T, shell, raw, stubs string) string {
	t.Helper()
	binary := requireShell(t, shell)
	home := t.TempDir()
	binDir := t.TempDir()
	const agent = `#!/bin/sh
printf 'AGENT-RAN\n'
`
	// #nosec G306 -- a fake executable agent in the test's own directory.
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	script := writeScriptFile(t, "nocx."+shell, renderScript(raw, []string{"claude"}, DeliveryLocal))
	program := "script=$1\nshift\nsource \"$script\"\n__nocx_lc_active=1\n" + stubs +
		"claude\nbuiltin printf 'WRAPPER-RC=%s\\n' \"$?\"\n"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// #nosec G204 -- requireShell resolves the real shell under test.
	cmd := exec.CommandContext(ctx, binary, "-f", "-c", program, shell, script)
	cmd.Env = cleanEnv(
		"HOME="+home, "ZDOTDIR="+home, "TMPDIR="+t.TempDir(), "HISTFILE=/dev/null",
		"PATH="+binDir+":"+os.Getenv("PATH"), "NOCX_SHELL_INTEGRATION=1",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the wrapper did not finish: %v; output=%q", ctx.Err(), out)
	}
	return string(out) + func() string {
		if err != nil {
			return " EXIT-ERR=" + err.Error()
		}
		return ""
	}()
}
