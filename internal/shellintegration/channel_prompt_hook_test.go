package shellintegration

// A command the prompt cycle runs is not a command line (nocx-1ebrs.66.1.18).
//
// The integration installs its wrapper last and PROMPT_COMMAND is then
// exactly `__nocx_prompt_command` (ADR-0006), but a configuration loaded AFTER
// the install can still append to that list — `PROMPT_COMMAND+=(hook)` and
// `PROMPT_COMMAND="$PROMPT_COMMAND; hook"` are both ordinary. bash runs the
// list it read for this prompt, so the appended element runs after
// __nocx_prompt_command returned, where __nocx_in_prompt_command is 0 and the
// preexec latch has just been armed: byte for byte the state a typed line is
// in. Announced, the hook opens an attempt nothing ever completes — the lane
// stays running at a ready prompt, and the person's next line is attributed to
// the hook's attempt instead of getting one of its own. That is the live-sshd
// failure (an accepted `start` after prompt_ready, no `complete`, lane stuck
// running), so this test is the reproduction.

import (
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

func TestAPromptHookAppendedAfterTheInstallIsNotAUserCommand(t *testing.T) {
	for _, tc := range []struct{ name, append string }{
		// The two forms a later configuration actually uses: the array append
		// (bash turns our scalar into a two-element array) and the string
		// append.
		{"array", `PROMPT_COMMAND+=(__later_hook)`},
		{"string", `PROMPT_COMMAND="$PROMPT_COMMAND; __later_hook"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := startChannelShell(t, "bash", "nocx.bash", bashScript)
			defer sh.close()

			// A function definition is not announced (bash's DEBUG trap does
			// not fire for one), so the line starts with a real command.
			sh.run(`builtin printf 'LATER-DEFINED\n'; __later_hook() { builtin printf 'LATER-HOOK\n'; }`)
			sh.run(tc.append)

			// The hook really reaches the prompt cycle. This is the
			// phenomenon, not the defect, and it stays true after the fix.
			waittest.WaitForTimeout(t, "the appended prompt hook ran", 10*time.Second, func() bool {
				return strings.Contains(sh.output(), "LATER-HOOK")
			})

			// The only line a person types after the append. Whatever the
			// prompt cycle ran, this is the one thing that may be announced
			// for it.
			sh.run(`builtin printf 'USER-LINE\n'`)

			var announced []string
			typed := 0
			for _, e := range sh.kernel.events() {
				if e.Evt != "start" {
					continue
				}
				cmd, _ := e.Body["command"].(string)
				announced = append(announced, cmd)
				// The hook's own call and its body are what the prompt
				// cycle ran. (The line that INSTALLS the hook names it too,
				// and that one the person did type.)
				if cmd == "__later_hook" || strings.Contains(cmd, "LATER-HOOK") {
					t.Fatalf("a command the prompt cycle ran was announced as the user's: %q\naccepted=%v",
						cmd, sh.kernel.events())
				}
				if strings.Contains(cmd, "USER-LINE") {
					typed++
				}
			}
			if typed != 1 {
				t.Fatalf("the typed line was announced %d times, want once; announced=%q accepted=%v",
					typed, announced, sh.kernel.events())
			}
		})
	}
}
