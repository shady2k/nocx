package settings_test

// The two ceilings a command the assistant runs is bound by (nocx-6dzxq).
// Before this they were constants in internal/transport, and the owner met
// the consequence: `df` against a stuck mount printed nothing for two
// minutes and was killed, while a chatty eight-minute build survived. Both
// numbers are the person's now, and this asserts they arrive on the screen
// like every other number — with a unit, a range, and a default of ten
// minutes each.

import (
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/settings"
)

func agentRunDeclaration(t *testing.T, key string) settings.Declaration {
	t.Helper()
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{data: map[credential.SecretID]string{}})
	for _, d := range reg.Declarations() {
		if d.Key == key {
			return d
		}
	}
	t.Fatalf("%s is not declared — the setting has no screen", key)
	return settings.Declaration{}
}

func TestAgentRunBounds_BothCeilingsAreDeclaredNumbersWithAUnitAndARange(t *testing.T) {
	for _, key := range []string{"assistant.runWallClockMinutes", "assistant.runQuietMinutes"} {
		d := agentRunDeclaration(t, key)
		if d.Section != "Answers" {
			t.Errorf("%s section %q, want Answers", key, d.Section)
		}
		if d.Control != "number" {
			t.Errorf("%s control %q, want number — the generated screen renders it like every other number", key, d.Control)
		}
		// TEN MINUTES EACH. The quiet bound used to be two, which is what
		// killed a healthy command for being normally silent.
		if d.Default != float64(10) {
			t.Errorf("%s default %v, want 10", key, d.Default)
		}
		if d.Unit != "minutes" {
			t.Errorf("%s unit %q, want minutes — the unit never lives in prose", key, d.Unit)
		}
		if d.Min == nil || *d.Min != 1 {
			t.Errorf("%s min %v, want 1 — a bound of zero is not a bound a person can mean", key, d.Min)
		}
		if d.Max == nil || *d.Max != 240 {
			t.Errorf("%s max %v, want 240", key, d.Max)
		}
		if d.Description == "" {
			t.Errorf("%s has no description", key)
		}
	}
}

// The declared defaults are readable in the type they were declared with,
// so the transport's registry-less fallback reads them rather than keeping a
// second copy of the same two numbers.
func TestAgentRunBounds_DefaultsAreReadableInTheirOwnType(t *testing.T) {
	if got := settings.AgentRunWallClockMinutes.DefaultValue(); got != 10 {
		t.Fatalf("wall-clock default = %v, want 10", got)
	}
	if got := settings.AgentRunQuietMinutes.DefaultValue(); got != 10 {
		t.Fatalf("quiet default = %v, want 10", got)
	}
}

// Both are ordinary numbers a person sets and reads back, and setting one
// leaves the other alone: they are two ceilings, not one with two names.
func TestAgentRunBounds_AreSetAndReadIndependently(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{data: map[credential.SecretID]string{}})
	if err := reg.SetNumber(settings.AgentRunQuietMinutes, 3); err != nil {
		t.Fatalf("SetNumber(quiet): %v", err)
	}
	quiet, err := reg.GetNumber(settings.AgentRunQuietMinutes)
	if err != nil || quiet != 3 {
		t.Fatalf("quiet = %v (%v), want 3", quiet, err)
	}
	wall, err := reg.GetNumber(settings.AgentRunWallClockMinutes)
	if err != nil || wall != 10 {
		t.Fatalf("wall clock = %v (%v), want its own default 10", wall, err)
	}
	// The range is enforced, so the model's clamp is never the only guard.
	if err := reg.SetNumber(settings.AgentRunWallClockMinutes, 0); err == nil {
		t.Fatal("a wall clock of zero was accepted; a run with no ceiling is what the ceiling exists to prevent")
	}
	if err := reg.SetNumber(settings.AgentRunQuietMinutes, 1000); err == nil {
		t.Fatal("a quiet bound past the declared maximum was accepted")
	}
}
