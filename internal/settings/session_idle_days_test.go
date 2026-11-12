package settings_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/settings"
)

func TestSessionIdleDays(t *testing.T) {
	doc := &fakeDoc{}
	secrets := &fakeSecretStore{data: map[credential.SecretID]string{}}
	reg := settings.New(doc, secrets)
	value, err := reg.GetNumber(settings.SessionIdleDays)
	if err != nil || value != 3 {
		t.Fatalf("default = %v (%v), want 3", value, err)
	}
	found := false
	for _, d := range reg.Declarations() {
		if d.Key != "sessions.idleDays" {
			continue
		}
		found = true
		if d.Control != "number" || d.Unit != "days" || d.Min == nil || *d.Min != 0 || d.ZeroLabel == "" {
			t.Fatalf("invalid declaration: %+v", d)
		}
	}
	if !found {
		t.Fatal("sessions.idleDays has no Settings field")
	}
	for _, value := range []float64{0, 7, 1000} {
		if err := reg.SetNumber(settings.SessionIdleDays, value); err != nil {
			t.Fatal(err)
		}
		reopened := settings.New(doc, secrets)
		got, err := reopened.GetNumber(settings.SessionIdleDays)
		if err != nil || got != value {
			t.Fatalf("restart = %v (%v), want %v", got, err, value)
		}
	}
	if err := reg.ValidateSetting("sessions.idleDays", 1.5); err == nil {
		t.Fatal("validation accepted fractional days")
	}
	if err := reg.ApplyValues(map[string]any{"sessions.idleDays": 1.5}); err == nil {
		t.Fatal("restore accepted fractional days")
	}
	for _, value := range []float64{-1, 1.5} {
		if err := reg.SetNumber(settings.SessionIdleDays, value); err == nil {
			t.Fatalf("accepted invalid value %v", value)
		}
	}
}
