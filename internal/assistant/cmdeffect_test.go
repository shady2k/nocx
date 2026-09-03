package assistant

import (
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

var runEffects = []content.Effect{
	content.EffectObserve, content.EffectMutateReversible,
	content.EffectMutateDestructive, content.EffectCrossBoundary,
	content.EffectDelegate,
}

func TestCommandEffect_LowersReadPipeline(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("du -h | sort -rh | head -20"), runEffects); got != content.EffectObserve {
		t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
	}
}

func TestCommandEffect_JoinsSubcommandsWorstWins(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("ls && rm -rf /tmp/x"), runEffects); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation(command), runEffects); got != content.EffectObserve {
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
			if got := commandEffect(parseCanonicalInvocation(command), runEffects); got != content.EffectObserve {
				t.Fatalf("effect = %q, want %q", got, content.EffectObserve)
			}
		})
	}
}

func TestCommandEffect_NewlineBackgroundStaysWorstCase(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("ls &\nhead file"), runEffects); got != content.EffectDelegate {
		t.Fatalf("effect = %q, want %q", got, content.EffectDelegate)
	}
}

func TestCommandEffect_RedirectionIsWrite(t *testing.T) {
	for _, command := range []string{
		"cat f > /etc/passwd",
		"cat f >> /etc/passwd",
	} {
		t.Run(command, func(t *testing.T) {
			if got := commandEffect(parseCanonicalInvocation(command), runEffects); got != content.EffectDelegate {
				t.Fatalf("effect = %q, want %q", got, content.EffectDelegate)
			}
		})
	}
}

func TestCommandEffect_RedirectionSinksAndWrites(t *testing.T) {
	tests := []struct {
		command string
		want    content.Effect
	}{
		{"du -sh /* 2>/dev/null", content.EffectObserve},   // stderr discarded; no file is mutated.
		{"du -sh /* >/dev/null", content.EffectObserve},    // stdout discarded; no file is mutated.
		{"du -sh /* &>/dev/null", content.EffectObserve},   // both streams discarded; no file is mutated.
		{"du -sh /* 2>&1", content.EffectObserve},          // stderr is duplicated to stdout; no file is mutated.
		{"du -sh /* > real-file", content.EffectDelegate},  // read plus write is mixed and takes the set's worst member.
		{"du -sh /* 2> real-file", content.EffectDelegate}, // read plus write is mixed and takes the set's worst member.
		{"du -sh /* > &1", content.EffectDelegate},         // spaced &1 is a filename, so read plus write is mixed.
		{"> /dev/null cat", content.EffectObserve},         // a leading null sink still discards without mutation.
		{"> /etc/passwd cat", content.EffectDelegate},      // read plus write is mixed and takes the set's worst member.
		{"du -sh /*", content.EffectObserve},               // no redirection leaves a read-only command.
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := commandEffect(parseCanonicalInvocation(test.command), runEffects); got != test.want {
				t.Fatalf("effect = %q, want %q", got, test.want)
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
			if got := commandEffect(parseCanonicalInvocation(command), runEffects); got != content.EffectObserve {
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
			if got := commandEffect(parseCanonicalInvocation(command), runEffects); got != content.EffectDelegate {
				t.Fatalf("effect = %q, want %q", got, content.EffectDelegate)
			}
		})
	}
}

func TestCommandEffect_UniqSecondOperandKeepsWorstCase(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("uniq input output"), runEffects); got != content.EffectDelegate {
		t.Fatalf("effect = %q, want %q", got, content.EffectDelegate)
	}
}

func TestCommandEffect_LsBoundaryDoesNotMatchLsof(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("lsof"), runEffects); got != content.EffectDelegate {
		t.Fatalf("effect = %q, want %q", got, content.EffectDelegate)
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
			if got := commandEffect(parseCanonicalInvocation(tc.command), runEffects); got != content.EffectDelegate {
				t.Fatalf("effect = %q, want %q", got, content.EffectDelegate)
			}
		})
	}
}

func TestCommandEffect_PathPrefixedDisqualifiersMatchBareNames(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{name: "sudo", command: "sudo rm -rf /home/me/x"},
		{name: "env", command: "env rm -rf /home/me/x"},
		{name: "xargs", command: "xargs rm"},
		{name: "watch", command: "watch ls"},
		{name: "setsid", command: "setsid ls"},
		{name: "ionice", command: "ionice ls"},
		{name: "flock", command: "flock /tmp/l ls"},
		{name: "nohup", command: "nohup ls"},
		{name: "timeout", command: "timeout 5 rm -rf /home/me/x"},
		{name: "tee", command: "tee /home/me/x"},
		{name: "bash -c", command: "bash -c 'rm -rf /home/me/x'"},
		{name: "sh -c", command: "sh -c 'rm -rf /home/me/x'"},
		{name: "find -delete", command: "find . -delete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bare := parseCanonicalInvocation(tc.command)
			bareEffect := commandEffect(bare, runEffects)
			prefixed := parseCanonicalInvocation("/usr/bin/" + tc.command)
			prefixedEffect := commandEffect(prefixed, runEffects)

			bareUnresolved := len(bare.Resources.Unresolved) > 0
			prefixedUnresolved := len(prefixed.Resources.Unresolved) > 0
			if prefixed.Disqualified != bare.Disqualified ||
				prefixedUnresolved != bareUnresolved ||
				prefixedEffect != bareEffect {
				t.Fatalf("bare = %+v (effect %q), prefixed = %+v (effect %q)",
					bare, bareEffect, prefixed, prefixedEffect)
			}
			if !bare.Disqualified || !bareUnresolved {
				t.Fatalf("bare = %+v, want disqualified with unresolved resources", bare)
			}
		})
	}
}

func TestCommandEffect_AllowListProgramNamesRemainCaseSensitive(t *testing.T) {
	tests := []struct {
		name      string
		known     string
		upperCase string
	}{
		{name: "ls", known: "ls /etc", upperCase: "LS /etc"},
		{name: "cat", known: "cat /etc/passwd", upperCase: "CAT /etc/passwd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			known := parseCanonicalInvocation(tc.known)
			if got := commandEffect(known, runEffects); got != content.EffectObserve ||
				len(known.Resources.Unresolved) != 0 {
				t.Fatalf("known = %+v (effect %q), want resolved observe", known, got)
			}

			upperCase := parseCanonicalInvocation(tc.upperCase)
			if got := commandEffect(upperCase, runEffects); got != content.EffectDelegate ||
				len(upperCase.Resources.Unresolved) == 0 {
				t.Fatalf("upper-case = %+v (effect %q), want unresolved delegate", upperCase, got)
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		inv := parseCanonicalInvocation("NOTAPROGRAM /etc")
		if got := commandEffect(inv, runEffects); got != content.EffectDelegate ||
			len(inv.Resources.Unresolved) == 0 {
			t.Fatalf("unknown = %+v (effect %q), want unresolved delegate", inv, got)
		}
	})
}

func TestCommandEffect_UnparseableAndUnknownAreWorstCase(t *testing.T) {
	for _, command := range []string{
		"ls 'unterminated",
		"something-unknown file",
		"ls &&",
	} {
		t.Run(command, func(t *testing.T) {
			if got := commandEffect(parseCanonicalInvocation(command), []content.Effect{content.EffectMutateDestructive}); got != content.EffectMutateDestructive {
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
			if got := commandEffect(parseCanonicalInvocation("ls -la"), []content.Effect{content.EffectObserve, declared}); got != content.EffectObserve {
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

func TestCommandResources_LeadingRedirectionNamesWriteTarget(t *testing.T) {
	inv := parseCanonicalInvocation("> /etc/x cat")
	if !inv.Disqualified {
		t.Fatalf("invocation = %+v, want leading file redirection disqualified", inv)
	}
	for _, resource := range inv.Resources.Resources {
		if resource.Path == "/etc/x" && resource.Verb == content.ResourceWrite {
			return
		}
	}
	t.Fatalf("resources = %+v, want leading write /etc/x", inv.Resources.Resources)
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
	if got := commandEffect(inv, []content.Effect{content.EffectMutateDestructive}); got != content.EffectMutateDestructive {
		t.Fatalf("effect = %q, want declared worst case", got)
	}
}

func TestStandingRuleRejectsDynamicTokensInAnyPosition(t *testing.T) {
	for _, command := range []string{"cat $LOGFILE", "df --output=$FORMAT /"} {
		t.Run(command, func(t *testing.T) {
			inv := parseCanonicalInvocation(command)
			if len(inv.Resources.Unresolved) == 0 {
				t.Fatalf("unresolved = %+v, want the dynamic token recorded", inv.Resources)
			}
			if _, reason := content.StandingRule(inv); reason == "" {
				t.Fatalf("standing rule for %q was not refused", command)
			}
		})
	}
}

func TestCommandEffect_BackgroundWithFollowingCommandStaysWorstCase(t *testing.T) {
	if got := commandEffect(parseCanonicalInvocation("ls & echo done"), runEffects); got != content.EffectDelegate {
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
	if got := commandEffect(inv, runEffects); got != content.EffectDelegate {
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
	if got := commandEffect(inv, runEffects); got != content.EffectDelegate {
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

func TestCommandResources_DistinguishesExecutionFromSourcing(t *testing.T) {
	tests := []struct {
		command string
		path    string
		verb    content.ResourceVerb
	}{
		{command: "./deploy.sh", path: "./deploy.sh", verb: content.ResourceExecute},
		{command: "bash deploy.sh", path: "deploy.sh", verb: content.ResourceExecute},
		{command: "source env.sh", path: "env.sh", verb: content.ResourceSource},
		{command: ". env.sh", path: "env.sh", verb: content.ResourceSource},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			inv := parseCanonicalInvocation(tc.command)
			if len(inv.Resources.Unresolved) != 0 {
				t.Fatalf("unresolved = %+v, want resolved resource", inv.Resources.Unresolved)
			}
			if len(inv.Resources.Resources) != 1 ||
				inv.Resources.Resources[0].Path != tc.path ||
				inv.Resources.Resources[0].Verb != tc.verb {
				t.Fatalf("resources = %+v, want %s %s", inv.Resources.Resources, tc.path, tc.verb)
			}
		})
	}
}

func TestCommandResources_ShellCommandStringRemainsUnresolved(t *testing.T) {
	for _, command := range []string{"bash -c 'cat deploy.sh'", "sh -ec 'cat deploy.sh'"} {
		t.Run(command, func(t *testing.T) {
			inv := parseCanonicalInvocation(command)
			if !inv.Disqualified || len(inv.Resources.Unresolved) == 0 {
				t.Fatalf("invocation = %+v, want disqualified unresolved command", inv)
			}
			for _, resource := range inv.Resources.Resources {
				if resource.Verb == content.ResourceExecute {
					t.Fatalf("resources = %+v, shell command string must not be an executable path", inv.Resources.Resources)
				}
			}
		})
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
			if got := commandEffect(inv, runEffects); got != content.EffectDelegate {
				t.Fatalf("effect = %q, want declared worst case", got)
			}
		})
	}
}

func hasResource(r content.ResourceReport, path string, verb content.ResourceVerb) bool {
	for _, res := range r.Resources {
		if res.Path == path && res.Verb == verb {
			return true
		}
	}
	return false
}

func hasFeature(r content.ResourceReport, feature string) bool {
	for _, f := range r.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// A path curl writes through -o used to appear in no resource, under no verb,
// in no row: optionTakesNextValue("curl", "-o") swallowed the option together
// with its value, so the only resource left was the URL, whose verb is
// ResourceNetwork and whose row is "Reach another host". Answering that row
// with Allowed therefore allowed writing any file on the machine.
func TestCommandResources_CurlOutputOptionIsAWrittenResource(t *testing.T) {
	inv := parseCanonicalInvocation("curl -o /tmp/proof https://example.com")

	if !inv.Parsed || inv.Disqualified {
		t.Fatalf("expected a parsed, qualified invocation, got parsed=%v disqualified=%v",
			inv.Parsed, inv.Disqualified)
	}
	if !hasResource(inv.Resources, "/tmp/proof", content.ResourceWrite) {
		t.Errorf("the file curl writes is not in the report: %+v", inv.Resources)
	}
	if !hasResource(inv.Resources, "https://example.com", content.ResourceNetwork) {
		t.Errorf("the URL is no longer in the report: %+v", inv.Resources)
	}
	if !hasFeature(inv.Resources, featureWritesOptionNamedPath) {
		t.Errorf("the feature a refusal has to match is absent: %+v", inv.Resources.Features)
	}

	// The report now mixes ResourceNetwork with ResourceWrite, so Effect
	// takes the declared set's worst member rather than the network mapping.
	// For session.run's set that member is `delegate` (effectOrder 6), NOT
	// `mutate-destructive` (2) as the brief's parenthetical says — the
	// binding half of the criterion is WorstEffect(declared), and the
	// security property holds either way because both outrank the
	// cross-boundary row this command used to be filed under.
	got := commandEffect(inv, runEffects)
	if got == content.EffectCrossBoundary {
		t.Errorf("a curl that writes a file is still filed under the network row: %q", got)
	}
	if want := content.WorstEffect(runEffects); got != want {
		t.Errorf("effect = %q, want the declared set's worst member %q", got, want)
	}
}

// A dynamic target cannot be resolved without running the shell, so the
// invocation is disqualified rather than classified as harmless.
func TestCommandResources_CurlOutputOptionWithDynamicTargetIsUnresolved(t *testing.T) {
	inv := parseCanonicalInvocation(`curl -o "$OUT" https://example.com`)

	if len(inv.Resources.Unresolved) == 0 {
		t.Fatalf("a dynamic output path resolved: %+v", inv.Resources)
	}
	if hasResource(inv.Resources, "$OUT", content.ResourceWrite) {
		t.Errorf("a dynamic output path was recorded as a resolved write: %+v", inv.Resources)
	}
	if got := commandEffect(inv, runEffects); got != content.WorstEffect(runEffects) {
		t.Errorf("effect = %q, want the declared set's worst member", got)
	}
}

// The plain form is untouched: one network resource, the network row.
func TestCommandResources_CurlWithoutOutputOptionIsUnchanged(t *testing.T) {
	inv := parseCanonicalInvocation("curl https://example.com")

	if len(inv.Resources.Resources) != 1 ||
		inv.Resources.Resources[0].Path != "https://example.com" ||
		inv.Resources.Resources[0].Verb != content.ResourceNetwork {
		t.Fatalf("resources = %+v, want one network resource", inv.Resources.Resources)
	}
	if len(inv.Resources.Features) != 0 {
		t.Errorf("features = %+v, want none", inv.Resources.Features)
	}
	if got := commandEffect(inv, runEffects); got != content.EffectCrossBoundary {
		t.Errorf("effect = %q, want cross-boundary", got)
	}
}

// -ofile, --output file and --output=file are one fact written three ways.
func TestCommandResources_WrittenOptionSpellings(t *testing.T) {
	for _, command := range []string{
		"curl -o /tmp/p https://example.com",
		"curl -o/tmp/p https://example.com",
		"curl --output /tmp/p https://example.com",
		"curl --output=/tmp/p https://example.com",
	} {
		t.Run(command, func(t *testing.T) {
			inv := parseCanonicalInvocation(command)
			if !hasResource(inv.Resources, "/tmp/p", content.ResourceWrite) {
				t.Errorf("%q did not record its output file: %+v", command, inv.Resources)
			}
			if !hasFeature(inv.Resources, featureWritesOptionNamedPath) {
				t.Errorf("%q did not carry the feature: %+v", command, inv.Resources.Features)
			}
			if !hasResource(inv.Resources, "https://example.com", content.ResourceNetwork) {
				t.Errorf("%q lost its URL: %+v", command, inv.Resources)
			}
		})
	}
}

// The audit of optionTakesNextValue, stated as a test. Every entry whose value
// is NOT a written path stays out of the resource report: the distinction is
// per program and cannot be guessed from the letter.
func TestCommandResources_OptionValuesThatAreNotWrittenPaths(t *testing.T) {
	// value is the option value that must not become a resource. install -o
	// is the case that shows why the assertion is per value rather than
	// "records no write at all": `install -o root /src /dst` genuinely does
	// write /dst, and `root` is still not a path.
	for _, tc := range []struct{ name, command, value string }{
		{"ssh -o is a config keyword", "ssh -o StrictHostKeyChecking=no host", "StrictHostKeyChecking=no"},
		{"ssh -oAttached is a config keyword", "ssh -oStrictHostKeyChecking=no host", "StrictHostKeyChecking=no"},
		{"ssh -i is an identity file it reads", "ssh -i /home/dev/.ssh/id_ed25519 host", "/home/dev/.ssh/id_ed25519"},
		{"bash -o is a shell option name", "bash -o pipefail script.sh", "pipefail"},
		{"install -o is an owner", "install -o root /src /dst", "root"},
		{"grep -f reads a pattern file", "grep -f patterns.txt file", "patterns.txt"},
		{"grep -e is a pattern", "grep -e out file", "out"},
		{"cut -f is a field list", "cut -f 2 file", "2"},
		{"head -n is a line count", "head -n 20 file", "20"},
		{"uniq -f is a field count", "uniq -f 2 file", "2"},
		{"du -d is a depth", "du -d 2 /tmp", "2"},
		{"curl -H is a header", "curl -H out https://example.com", "out"},
		{"curl -X is a method", "curl -X POST https://example.com", "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := parseCanonicalInvocation(tc.command)
			if hasFeature(inv.Resources, featureWritesOptionNamedPath) {
				t.Errorf("%q was read as writing a file through an option: %+v",
					tc.command, inv.Resources)
			}
			for _, res := range inv.Resources.Resources {
				if res.Path == tc.value {
					t.Errorf("%q recorded its option value %q as a %s resource: %+v",
						tc.command, tc.value, res.Verb, inv.Resources)
				}
			}
		})
	}
}

// The other option in the table whose value IS a written path: install -t and
// cp -t name a target DIRECTORY, and targetDirectoryOperands has owned that
// since before this change. Named here so the audit is complete.
func TestCommandResources_TargetDirectoryOptionsWriteTheirValue(t *testing.T) {
	for _, tc := range []struct{ command, target string }{
		{"install -t /dest source", "/dest"},
		{"install --target-directory=/dest source", "/dest"},
		{"cp -t /dest source", "/dest"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			inv := parseCanonicalInvocation(tc.command)
			if !hasResource(inv.Resources, tc.target, content.ResourceWrite) {
				t.Errorf("%q did not record %q as written: %+v",
					tc.command, tc.target, inv.Resources)
			}
		})
	}
}

// sort -o is the one other write-bearing option value, and it shares the
// table so that "which option value is a written path" has a single owner.
func TestCommandResources_SortOutputOptionSpellings(t *testing.T) {
	for _, command := range []string{
		"sort -o /tmp/p file",
		"sort -o/tmp/p file",
		"sort --output /tmp/p file",
		"sort --output=/tmp/p file",
	} {
		t.Run(command, func(t *testing.T) {
			inv := parseCanonicalInvocation(command)
			if !hasResource(inv.Resources, "/tmp/p", content.ResourceWrite) {
				t.Errorf("%q did not record its output file: %+v", command, inv.Resources)
			}
			if !hasResource(inv.Resources, "file", content.ResourceRead) {
				t.Errorf("%q lost its input file: %+v", command, inv.Resources)
			}
			if !hasFeature(inv.Resources, featureWritesOptionNamedPath) {
				t.Errorf("%q did not carry the feature: %+v", command, inv.Resources.Features)
			}
		})
	}
}

// A stored approval is re-checked against its clone, so a clone that loses the
// features loses the fact a narrowing refusal matches on.
func TestCloneInvocation_KeepsFeatures(t *testing.T) {
	inv := parseCanonicalInvocation("curl -o /tmp/proof https://example.com")
	if !hasFeature(inv.Resources, featureWritesOptionNamedPath) {
		t.Fatalf("precondition: the parse did not record the feature: %+v", inv.Resources)
	}
	if !hasFeature(cloneInvocation(inv).Resources, featureWritesOptionNamedPath) {
		t.Errorf("the clone dropped the feature: %+v", cloneInvocation(inv).Resources)
	}
}
