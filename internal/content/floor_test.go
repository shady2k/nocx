package content

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFloor_ProtectsInjectedNocxRoots(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "nocx")
	dataDir := filepath.Join(t.TempDir(), "data", "nocx")
	floor := NewFloor(configDir, dataDir)

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "policy document", path: filepath.Join(configDir, "agent-policy.json")},
		{name: "vault", path: filepath.Join(configDir, "vault.db")},
		{name: "ledger", path: filepath.Join(dataDir, "content.db")},
		{name: "shell manifest", path: filepath.Join(dataDir, "shell-integration", "manifest.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, denied := floor.Refusal(Invocation{Parsed: true}, []GrantScope{{Kind: ResourcePath, ID: tc.path}})
			if !denied {
				t.Fatalf("floor allowed %s", tc.path)
			}
			if !strings.Contains(strings.ToLower(reason), "never") || !strings.Contains(strings.ToLower(reason), "floor") {
				t.Fatalf("floor reason = %q, want a human sentence naming the non-overridable floor", reason)
			}
		})
	}
	for _, root := range []string{configDir, dataDir} {
		if _, denied := floor.Refusal(Invocation{Parsed: true}, []GrantScope{{Kind: ResourcePath, ID: root}}); !denied {
			t.Fatalf("floor did not protect exact root %s", root)
		}
	}

	if _, denied := floor.Refusal(Invocation{Parsed: true}, []GrantScope{{Kind: ResourcePath, ID: filepath.Join(configDir, "other", "file")}}); !denied {
		t.Fatal("floor did not protect a nested config path")
	}
	if _, denied := floor.Refusal(Invocation{Parsed: true}, []GrantScope{{Kind: ResourcePath, ID: filepath.Join(dataDir, "other", "file")}}); !denied {
		t.Fatal("floor did not protect a nested data path")
	}
	if _, denied := floor.Refusal(Invocation{Parsed: true}, []GrantScope{{Kind: ResourcePath, ID: filepath.Join(filepath.Dir(configDir), "nocx-other", "file")}}); denied {
		t.Fatal("floor protected a similarly named sibling path")
	}
}

func TestFloor_HomeRootOnlyProtectsHomeItself(t *testing.T) {
	floor := NewFloor("", "")
	for _, command := range [][]string{
		{"rm", "-rf", "~"},
		{"rm", "-rf", "$HOME"},
		{"rm", "-rf", "${HOME}"},
		{"rm", "-rf", "/"},
	} {
		if reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{command}}, nil); !denied {
			t.Fatalf("home root command %v allowed: %s", command, reason)
		}
	}
	if reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{{"rm", "-rf", "~/project/build"}}}, nil); denied {
		t.Fatalf("home descendant was floor-refused: %s", reason)
	}
}

func TestFloor_RecursiveFlagsAndBlockDevicesAreNarrow(t *testing.T) {
	floor := NewFloor("", "")
	for _, command := range [][]string{
		{"rm", "--preserve-root", "/"},
		{"rm", "--interactive", "/"},
		{"dd", "if=input", "of=/dev/null"},
		{"dd", "if=input", "of=/dev/tty"},
		{"rm"},
	} {
		if reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{command}}, nil); denied {
			t.Fatalf("ordinary command %v was floor-refused: %s", command, reason)
		}
	}
	for _, command := range [][]string{
		{"rm", "--recursive", "/"},
		{"dd", "if=input", "of=/dev/sda"},
	} {
		if reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{command}}, nil); !denied {
			t.Fatalf("dangerous command %v was allowed: %s", command, reason)
		}
	}
}

func TestFloor_ProtectsIrreversibleHumanNeverOperations(t *testing.T) {
	floor := NewFloor("", "")
	for _, tc := range []struct {
		name  string
		words []string
	}{
		{name: "erase home", words: []string{"rm", "-rf", "$HOME"}},
		{name: "format device", words: []string{"mkfs.ext4", "/dev/sda"}},
		{name: "partition device", words: []string{"fdisk", "/dev/sda"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{tc.words}}, nil)
			if !denied {
				t.Fatalf("floor allowed %s", tc.name)
			}
			if strings.TrimSpace(reason) == "" {
				t.Fatal("floor returned an empty refusal sentence")
			}
		})
	}
}

func TestFloor_AllowsOrdinaryOperationOutsideRoots(t *testing.T) {
	floor := NewFloor(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{{"ls", "/tmp"}}}, []GrantScope{{Kind: ResourcePath, ID: "/tmp"}}); denied {
		t.Fatalf("ordinary operation refused: %s", reason)
	}
}

func TestFloor_SurvivesWorkspaceAndSessionPolicyResolution(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "nocx")
	dataDir := filepath.Join(t.TempDir(), "data", "nocx")
	floor := NewFloor(configDir, dataDir)
	global := EffectPolicy{}.WithFloor(floor)
	workspace := EffectPolicy{Observe: EffectRow{Decision: DecisionPermit, Scopes: []GrantScope{{Kind: ResourcePath, ID: "/"}}}}
	resolved := ResolvePolicy(global, &workspace, SessionOverrides{
		Decisions: map[Effect]Decision{EffectObserve: DecisionPermit},
		Rules:     []InvocationRule{{Selector: InvocationSelector{Exact: [][]string{{"*"}}}, Decision: DecisionPermit}},
	})

	reason, denied := resolved.FloorRefusal(Invocation{Parsed: true}, []GrantScope{{
		Kind: ResourcePath,
		ID:   filepath.Join(configDir, "agent-policy.json"),
	}})
	if !denied || !strings.Contains(reason, "floor") {
		t.Fatalf("resolved policy floor refusal = (%q, %v), want immutable floor refusal", reason, denied)
	}
	grant := resolved.AsGrant([]GrantScope{{
		Kind: ResourcePath,
		ID:   "/workspace",
	}})
	reason, denied = grant.Policy.FloorRefusal(Invocation{Parsed: true}, []GrantScope{{
		Kind: ResourcePath,
		ID:   filepath.Join(configDir, "agent-policy.json"),
	}})
	if !denied || !strings.Contains(reason, "floor") {
		t.Fatalf("minted policy floor refusal = (%q, %v), want immutable floor refusal", reason, denied)
	}
}

func TestFloor_DoesNotRefuseUnrelatedDisqualifiedInvocation(t *testing.T) {
	floor := NewFloor("", "")
	if reason, denied := floor.Refusal(Invocation{
		Parsed:       true,
		Disqualified: true,
		Commands:     [][]string{{"echo", "hello", ">", "out"}},
	}, nil); denied {
		t.Fatalf("unrelated disqualified invocation refused: %s", reason)
	}
}

func TestFloor_RawCommandRefusalNormalizesWhitespace(t *testing.T) {
	floor := NewFloor("", "")
	for _, command := range []string{
		":(){ :|:& };:",
		":(){:|:&};:",
		"echo ok; :(){\t:|:&\n};:",
	} {
		t.Run(command, func(t *testing.T) {
			if reason, denied := floor.RawCommandRefusal(command); !denied || !strings.Contains(reason, "never") {
				t.Fatalf("raw fork bomb = (%q, %v), want floor refusal", reason, denied)
			}
		})
	}
	if reason, denied := floor.RawCommandRefusal("rm -rf /tmp/config"); denied {
		t.Fatalf("raw path command refused by fork-bomb rule: %s", reason)
	}
}

func TestZeroFloor_DisablesEveryFloorRule(t *testing.T) {
	floor := Floor{}
	if reason, denied := floor.Refusal(Invocation{Parsed: true, Commands: [][]string{{"rm", "-rf", "/"}}}, nil); denied {
		t.Fatalf("the zero Floor refused dangerous invocation: %s", reason)
	}
	if reason, denied := floor.RawCommandRefusal(":(){ :|:& };:"); denied {
		t.Fatalf("the zero Floor refused raw fork bomb: %s", reason)
	}
}
