package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/settings"
)

func TestSessionIdleDaysFollowSettingsForNewSpawns(t *testing.T) {
	reg := settings.New(&appFakeDoc{}, nil)
	lifetime := newSessionLifetimeSetting(reg)
	if got := *lifetime.daysPtr(); got != 3 {
		t.Fatalf("default days = %d, want 3", got)
	}
	if err := reg.SetNumber(settings.SessionIdleDays, 0); err != nil {
		t.Fatal(err)
	}
	if got := *lifetime.daysPtr(); got != 0 {
		t.Fatalf("disabled days = %d, want 0", got)
	}
	if err := reg.SetNumber(settings.SessionIdleDays, 7); err != nil {
		t.Fatal(err)
	}
	if got := *lifetime.daysPtr(); got != 7 {
		t.Fatalf("new spawn days = %d, want 7", got)
	}
}
