package assistant

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestCommandEffect_LowersReadPipeline(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("du -h | sort -rh | head -20"), content.EffectMutateDestructive); got != content.EffectObserve {
		t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
	}
}

func TestCommandEffect_JoinsSubcommandsWorstWins(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("ls && rm -rf /tmp/x"), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation(command), content.EffectMutateDestructive); got != content.EffectObserve {
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
			if got := commandEffect(parseCanonicalInvocation(command), content.EffectMutateDestructive); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}

func TestCommandEffect_NewlineBackgroundStaysWorstCase(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("ls &\nhead file"), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
	}
}

func TestCommandEffect_RedirectionIsWrite(t *testing.T) {
	for _, command := range []string{
		"cat f > /etc/passwd",
		"cat f >> /etc/passwd",
	} {
		t.Run(command, func(t *testing.T) {
			if got := commandEffect(parseCanonicalInvocation(command), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation(command), content.EffectMutateDestructive); got != content.EffectObserve {
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
			if got := commandEffect(parseCanonicalInvocation(command), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
				t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
			}
		})
	}
}

func TestCommandEffect_UniqSecondOperandKeepsWorstCase(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("uniq input output"), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want %q", got, content.EffectMutateDestructive)
	}
}

func TestCommandEffect_LsBoundaryDoesNotMatchLsof(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("lsof"), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation(tc.command), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation(command), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation("ls -la"), declared); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}

func TestCommandResources_RedirectionNamesWriteTarget(t *testing.T) {
	inv := parseCanonicalInvocation("cat f > /etc/x")
	if len(inv.Resources.Resources) != 2 {
		t.Fatalf("resources = %+v, want source read and target write", inv.Resources)
	}
	seenRead, seenWrite := false, false
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "f" && resource.Verb == content.ResourceRead {
			seenRead = true
		}
		if resource.Path == "/etc/x" && resource.Verb == content.ResourceWrite {
			seenWrite = true
		}
	}
	if !seenRead || !seenWrite {
		t.Fatalf("resources = %+v, want read f and write /etc/x", inv.Resources.Resources)
	}
}

func TestCommandResources_RedirectionWithoutSpacesNamesWriteTarget(t *testing.T) {
	inv := parseCanonicalInvocation("cat f >/etc/x")
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "/etc/x" && resource.Verb == content.ResourceWrite {
			return
		}
	}
	t.Fatalf("resources = %+v, want write /etc/x", inv.Resources.Resources)
}

func TestCommandResources_CPReadsSourceAndWritesDestination(t *testing.T) {
	inv := parseCanonicalInvocation("cp a b")
	if len(inv.Resources.Resources) != 2 {
		t.Fatalf("resources = %+v, want source read and destination write", inv.Resources)
	}
	seenRead, seenWrite := false, false
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "a" && resource.Verb == content.ResourceRead {
			seenRead = true
		}
		if resource.Path == "b" && resource.Verb == content.ResourceWrite {
			seenWrite = true
		}
	}
	if !seenRead || !seenWrite {
		t.Fatalf("resources = %+v, want read a and write b", inv.Resources.Resources)
	}
}

func TestCommandResources_RMVariableIsUnresolvedDeletion(t *testing.T) {
	inv := parseCanonicalInvocation("rm -rf $BUILD")
	if len(inv.Resources.Unresolved) != 1 {
		t.Fatalf("unresolved = %+v, want one unresolved deletion", inv.Resources.Unresolved)
	}
	got := inv.Resources.Unresolved[0]
	if got.Path != "$BUILD" || got.Verb != content.ResourceDelete {
		t.Fatalf("unresolved resource = %+v, want delete $BUILD", got)
	}
	if got.Reason == "" {
		t.Fatal("unresolved deletion has no human-readable reason")
	}
	if got := commandEffect(inv, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want declared worst case", got)
	}
}

func TestCommandEffect_BackgroundWithFollowingCommandStaysWorstCase(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("ls & echo done"), content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want declared worst case", got)
	}
}

func TestCommandResources_ProgramWritesAndNetworkAreVisible(t *testing.T) {
	tests := []struct {
		command string
		path    string
		verb    content.ResourceVerb
	}{
		{command: "tee output", path: "output", verb: content.ResourceWrite},
		{command: "curl https://example.test/a", path: "https://example.test/a", verb: content.ResourceNetwork},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			inv := parseCanonicalInvocation(tc.command)
			if len(inv.Resources.Resources) != 1 || inv.Resources.Resources[0].Path != tc.path ||
				inv.Resources.Resources[0].Verb != tc.verb {
				t.Fatalf("resources = %+v, want %s %s", inv.Resources.Resources, tc.verb, tc.path)
			}
		})
	}
}

func TestCommandResources_DoubleQuotedSubstitutionIsUnresolved(t *testing.T) {
	inv := parseCanonicalInvocation(`ls "--color=$(touch /tmp/pwn)" /tmp`)
	if len(inv.Resources.Unresolved) == 0 {
		t.Fatal("unresolved = empty, want a reason for double-quoted command substitution")
	}
	if got := commandEffect(inv, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want declared worst case", got)
	}
}

func TestCommandResources_ReadWriteRedirectionNamesWriteTarget(t *testing.T) {
	inv := parseCanonicalInvocation("cat /tmp/source 1<>/tmp/state")
	seenWrite := false
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "/tmp/state" && resource.Verb == content.ResourceWrite {
			seenWrite = true
		}
	}
	if !seenWrite {
		t.Fatalf("resources = %+v, want write /tmp/state", inv.Resources.Resources)
	}
	if got := commandEffect(inv, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want declared worst case", got)
	}
}

func TestCommandResources_MVReportsSourceDeletion(t *testing.T) {
	inv := parseCanonicalInvocation("mv a b")
	seenDelete := false
	seenWrite := false
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "a" && resource.Verb == content.ResourceDelete {
			seenDelete = true
		}
		if resource.Path == "b" && resource.Verb == content.ResourceWrite {
			seenWrite = true
		}
	}
	if !seenDelete || !seenWrite {
		t.Fatalf("resources = %+v, want delete a and write b", inv.Resources.Resources)
	}
}

func TestCommandResources_CPTargetDirectoryOptionKeepsRoles(t *testing.T) {
	inv := parseCanonicalInvocation("cp -t /dest source")
	seenRead := false
	seenWrite := false
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "source" && resource.Verb == content.ResourceRead {
			seenRead = true
		}
		if resource.Path == "/dest" && resource.Verb == content.ResourceWrite {
			seenWrite = true
		}
	}
	if !seenRead || !seenWrite {
		t.Fatalf("resources = %+v, want read source and write /dest", inv.Resources.Resources)
	}
}

func TestCommandResources_RedirectionAlsoExplainsUnresolvedSyntax(t *testing.T) {
	inv := parseCanonicalInvocation("cat f > /etc/x")
	if len(inv.Resources.Unresolved) == 0 || inv.Resources.Unresolved[0].Reason == "" {
		t.Fatalf("unresolved = %+v, want a human-readable redirection reason", inv.Resources.Unresolved)
	}
}

func TestCommandResources_InstallModeOptionConsumesValue(t *testing.T) {
	inv := parseCanonicalInvocation("install -m 755 source dest")
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "755" {
			t.Fatalf("resources = %+v, option value 755 must not be a path", inv.Resources.Resources)
		}
	}
}

func TestCommandResources_InstallCompareFlagKeepsRoles(t *testing.T) {
	inv := parseCanonicalInvocation("install -C source dest")
	seenRead := false
	seenWrite := false
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "source" && resource.Verb == content.ResourceRead {
			seenRead = true
		}
		if resource.Path == "dest" && resource.Verb == content.ResourceWrite {
			seenWrite = true
		}
	}
	if !seenRead || !seenWrite {
		t.Fatalf("resources = %+v, want read source and write dest", inv.Resources.Resources)
	}
}

func TestCommandEffect_DisqualifiedAlwaysHasUnresolvedCause(t *testing.T) {
	commands := []string{
		`ls "$(pwd)"`,
		`ls 'quoted'$(pwd)`,
		"ls `pwd`",
		"cat f > /etc/x",
		"cat f >> /etc/x",
		"cat <(ls)",
		"ls & echo done",
		"sudo ls",
		"env ls",
		"xargs ls",
		"sh -c 'ls'",
		"find . -exec cat {} \\;",
		"tee output",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			inv := parseCanonicalInvocation(command)
			if !inv.Disqualified {
				t.Fatalf("command was not disqualified: %+v", inv)
			}
			if len(inv.Resources.Unresolved) == 0 {
				t.Fatalf("unresolved = empty for disqualified command")
			}
			if got := commandEffect(inv, content.EffectMutateDestructive); got != content.EffectMutateDestructive {
				t.Fatalf("effect = %q, want declared worst case", got)
			}
		})
	}
}
