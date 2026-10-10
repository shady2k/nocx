package settings_test

// TerminalScrollbackLines is how far the live terminal scrolls back
// (nocx-zg3k3.10.1). It is deliberately NOT a History setting: History is
// what survives a restart, this is how far a person can scroll right now,
// and the two were separated on purpose. This file asserts the declaration
// arrives on the screen like every other number — bounds, unit, zero label —
// and that its description carries the live-surface meaning separately from
// durable capture, so zero remains a clear no-live-history choice.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/settings"
)

func terminalScrollbackDeclaration(t *testing.T) settings.Declaration {
	t.Helper()
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{data: map[credential.SecretID]string{}})
	for _, d := range reg.Declarations() {
		if d.Key == "terminal.scrollbackLines" {
			return d
		}
	}
	t.Fatalf("terminal.scrollbackLines is not declared — the setting has no screen")
	return settings.Declaration{}
}

func TestTerminalScrollback_IsDeclaredWithTheBeadsBounds(t *testing.T) {
	d := terminalScrollbackDeclaration(t)
	if d.Section != "Terminal" {
		t.Errorf("section %q, want Terminal — a new section of its own, not a History row", d.Section)
	}
	if d.Control != "number" {
		t.Errorf("control %q, want number", d.Control)
	}
	if d.Default != float64(10000) {
		t.Errorf("default %v, want 10000", d.Default)
	}
	if d.Unit != "lines" {
		t.Errorf("unit %q, want lines — the unit never lives in prose", d.Unit)
	}
	if d.Min == nil || *d.Min != 0 {
		t.Errorf("min %v, want 0 — zero is a value the setting means (no scrollback)", d.Min)
	}
	if d.Max == nil || *d.Max != 100000 {
		t.Errorf("max %v, want 100000", d.Max)
	}
	if d.ZeroLabel == "" {
		t.Errorf("zero label is empty — 0 is a sentinel here (no history at all) and the screen must say so")
	}
}

func TestTerminalScrollback_DescriptionNamesTheThreeQualifications(t *testing.T) {
	d := terminalScrollbackDeclaration(t)
	desc := d.Description
	for _, fragment := range []struct {
		what     string
		contains string
	}{
		{"page-granular pruning", "page"},
		{"retains more than asked", "more"},
		{"styled output retains fewer", "styled"},
		{"the byte ceiling", "ceiling"},
		{"lowering prunes at once", "immediately"},
		{"zero means no live scrollback", "no live scrollback"},
		{"durable capture remains available", "durable capture"},
	} {
		if !strings.Contains(desc, fragment.contains) {
			t.Errorf("description does not name %s (missing %q): %q", fragment.what, fragment.contains, desc)
		}
	}
}

func TestTerminalScrollback_IsSettableAcrossItsWholeRange(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{data: map[credential.SecretID]string{}})
	if err := reg.SetNumber(settings.TerminalScrollbackLines, 0); err != nil {
		t.Fatalf("SetNumber(0): %v — zero is a value the person can mean", err)
	}
	if err := reg.SetNumber(settings.TerminalScrollbackLines, 100000); err != nil {
		t.Fatalf("SetNumber(100000): %v", err)
	}
	if err := reg.SetNumber(settings.TerminalScrollbackLines, -1); err == nil {
		t.Fatal("a negative scrollback was accepted")
	}
	if err := reg.SetNumber(settings.TerminalScrollbackLines, 100001); err == nil {
		t.Fatal("a scrollback past the declared maximum was accepted")
	}
	if v, err := reg.GetNumber(settings.TerminalScrollbackLines); err != nil || v != 100000 {
		// A refused write changes nothing: the value in force is the last
		// accepted one, so the two refusals above left the 100000 set before
		// them standing and did not write their own refused value.
		t.Fatalf("after refused writes the value is %v (%v), want the accepted 100000", v, err)
	}
}
