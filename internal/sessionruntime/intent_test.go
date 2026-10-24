package sessionruntime

import (
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

// TestKeyNamesCarryTheKeypadsEnterAsItsOwnKey is the WIRE half of
// nocx-1ebrs.32. The pane reads a physical key and names it; this table is the
// whole of what the runtime accepts, and a name it does not carry is refused
// rather than guessed. Before the fix the table had "enter" only, so the
// keypad's Enter had no spelling to send and the program received nothing.
//
// The two names are asserted to be DIFFERENT identities on purpose. Folding
// the keypad's Enter into "enter" would make the pane's own silence into a
// plausible-looking CR and lose the case where the program enabled application
// keypad mode, which the emulator decides from the key it is given.
func TestKeyNamesCarryTheKeypadsEnterAsItsOwnKey(t *testing.T) {
	returnKey, err := keyEvent([]byte("enter"))
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if returnKey.Key != emulator.KeyEnter {
		t.Errorf("\"enter\" parsed as key %d, want KeyEnter", returnKey.Key)
	}

	keypad, err := keyEvent([]byte("numpadenter"))
	if err != nil {
		t.Fatalf("numpadenter: %v", err)
	}
	if keypad.Key != emulator.KeyNumpadEnter {
		t.Errorf("\"numpadenter\" parsed as key %d, want KeyNumpadEnter", keypad.Key)
	}
	if keypad.Key == returnKey.Key {
		t.Error("the keypad's Enter and Return parse to one key: the mode that tells them apart could never be applied")
	}

	// The name a person says, spelled the way they say it, is the same key: the
	// table matches case-insensitively and the modifiers are carried, not
	// swallowed.
	chorded, err := keyEvent([]byte("Shift+NumpadEnter"))
	if err != nil {
		t.Fatalf("Shift+NumpadEnter: %v", err)
	}
	if chorded.Key != emulator.KeyNumpadEnter || !chorded.Mods.Shift {
		t.Errorf("\"Shift+NumpadEnter\" parsed as key %d with mods %+v, want the keypad's Enter with Shift", chorded.Key, chorded.Mods)
	}
}

func TestMouseActionCarriesKeyStyleModifiers(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    emulator.MouseEvent
	}{
		{"press wheel-up 2 3", emulator.MouseEvent{Action: emulator.MousePress, Button: emulator.MouseWheelUp, X: 2, Y: 3}},
		{"Ctrl+Alt+press wheel-down 2 3", emulator.MouseEvent{Action: emulator.MousePress, Button: emulator.MouseWheelDown, Mods: emulator.Mods{Ctrl: true, Alt: true}, X: 2, Y: 3}},
		{"Shift+release left 2 3", emulator.MouseEvent{Action: emulator.MouseRelease, Button: emulator.MouseLeft, Mods: emulator.Mods{Shift: true}, X: 2, Y: 3}},
		{"sUpEr+ShIfT+MoTiOn none 2 3", emulator.MouseEvent{Action: emulator.MouseMotion, Button: emulator.MouseNone, Mods: emulator.Mods{Super: true, Shift: true}, X: 2, Y: 3}},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			got, err := mouseEvent([]byte(tc.payload))
			if err != nil || got != tc.want {
				t.Fatalf("mouseEvent = (%+v, %v), want (%+v, nil)", got, err, tc.want)
			}
		})
	}
}

func TestMalformedMouseModifierPayloadsAreRefused(t *testing.T) {
	for _, payload := range []string{
		"Meta+press wheel-up 2 3",
		"Ctrl++press wheel-up 2 3",
		"+press wheel-up 2 3",
		"Shift+ wheel-up 2 3",
		"Shift+click wheel-up 2 3",
		"press+Ctrl wheel-up 2 3",
		"Ctrl+press Shift+wheel-up 2 3",
		"Ctrl+press wheel-up two 3",
		"Ctrl+press wheel-up 2 three",
		"Ctrl+press wheel-up 2",
		"Ctrl+press wheel-up 2 3 extra",
	} {
		t.Run(payload, func(t *testing.T) {
			if got, err := mouseEvent([]byte(payload)); !errors.Is(err, ErrIntentUnsupported) || got != (emulator.MouseEvent{}) {
				t.Fatalf("mouseEvent = (%+v, %v), want empty event and ErrIntentUnsupported", got, err)
			}
		})
	}
}
