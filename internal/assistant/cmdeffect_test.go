package assistant

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestCommandEffect_LowersReadPipeline(t *testing.T) {
	if got := CommandEffect("du -h | sort -rh | head -20", content.EffectMutateDestructive); got != content.EffectObserve {
		t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
	}
}

func TestCommandEffect_JoinsSubcommandsWorstWins(t *testing.T) {
	if got := CommandEffect("ls && rm -rf /tmp/x", content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
	}
}

func TestCommandEffect_SplitsOnlyTheShellSeparators(t *testing.T) {
	for _, command := range []string{
		"ls || head file",
		"ls ; head file",
		"ls\nhead file",
		"ls |& head file",
	} {
		t.Run(command, func(t *testing.T) {
			if got := CommandEffect(command, content.EffectMutateDestructive); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}

func TestCommandEffect_QuotedArgumentsAreNotSyntax(t *testing.T) {
	for _, command := range []string{
		`ls "a>b"`,
		`ls '$(pwd)'`,
	} {
		t.Run(command, func(t *testing.T) {
			if got := CommandEffect(command, content.EffectMutateDestructive); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}

func TestCommandEffect_NewlineBackgroundStaysWorstCase(t *testing.T) {
	if got := CommandEffect("ls &\nhead file", content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
	}
}

func TestCommandEffect_RedirectionIsWrite(t *testing.T) {
	for _, command := range []string{
		"cat f > /etc/passwd",
		"cat f >> /etc/passwd",
	} {
		t.Run(command, func(t *testing.T) {
			if got := CommandEffect(command, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
				t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
			}
		})
	}
}

func TestCommandEffect_EveryReadAllowlistEntryLowers(t *testing.T) {
	for _, command := range []string{
		"cat file",
		"cut -d: -f1 /etc/passwd",
		"df -h",
		"du -h .",
		"file image.bin",
		"free -h",
		"grep -n needle file",
		"head -20 file",
		"id",
		"ls -la",
		"ps aux",
		"pwd",
		"rg needle .",
		"sort -rh file",
		"stat file",
		"tail -20 file",
		"uname -a",
		"uniq file",
		"uptime",
		"wc -l file",
		"whoami",
	} {
		t.Run(command, func(t *testing.T) {
			if got := CommandEffect(command, content.EffectMutateDestructive); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}

func TestCommandEffect_SortOutputFormsKeepWorstCase(t *testing.T) {
	for _, command := range []string{
		"sort -o /etc/passwd file",
		"sort -o/etc/passwd file",
		"sort --output /etc/passwd file",
		"sort --output=/etc/passwd file",
	} {
		t.Run(command, func(t *testing.T) {
			if got := CommandEffect(command, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
				t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
			}
		})
	}
}

func TestCommandEffect_UniqSecondOperandKeepsWorstCase(t *testing.T) {
	if got := CommandEffect("uniq input output", content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
	}
}

func TestCommandEffect_LsBoundaryDoesNotMatchLsof(t *testing.T) {
	if got := CommandEffect("lsof", content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
	}
}

func TestCommandEffect_DisqualifiersKeepDeclaredWorstCase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "variable program", command: "$CMD"},
		{name: "braced variable program", command: "${CMD}"},
		{name: "sudo", command: "sudo ls"},
		{name: "env", command: "env ls"},
		{name: "xargs", command: "xargs ls"},
		{name: "sh c", command: "sh -c 'ls'"},
		{name: "bash c", command: "bash -c 'ls'"},
		{name: "find exec", command: "find . -exec cat {} \\;"},
		{name: "find delete", command: "find . -delete"},
		{name: "git pager", command: "git -c core.pager=cat log"},
		{name: "watch", command: "watch ls"},
		{name: "setsid", command: "setsid ls"},
		{name: "ionice", command: "ionice ls"},
		{name: "flock", command: "flock lock ls"},
		{name: "nohup", command: "nohup ls"},
		{name: "timeout", command: "timeout 10 ls"},
		{name: "command substitution", command: "ls $(pwd)"},
		{name: "backticks", command: "ls `pwd`"},
		{name: "process substitution input", command: "cat <(ls)"},
		{name: "process substitution output", command: "cat >(ls)"},
		{name: "background", command: "ls &"},
		{name: "tee", command: "ls | tee output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CommandEffect(tc.command, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
				t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
			}
		})
	}
}

func TestCommandEffect_UnparseableAndUnknownAreWorstCase(t *testing.T) {
	for _, command := range []string{
		"ls 'unterminated",
		"something-unknown file",
		"ls &&",
	} {
		t.Run(command, func(t *testing.T) {
			if got := CommandEffect(command, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
				t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
			}
		})
	}
}

func TestCommandEffect_LowersBelowAnyDeclaredWorstCaseWhenSafe(t *testing.T) {
	for _, declared := range []content.Effect{
		content.EffectMutateReversible,
		content.EffectMutateDestructive,
		content.EffectPrivilegeChange,
		content.EffectDisclose,
		content.EffectCrossBoundary,
		content.EffectDelegate,
	} {
		t.Run(string(declared), func(t *testing.T) {
			if got := CommandEffect("ls -la", declared); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}
