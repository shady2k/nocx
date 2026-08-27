package app

// The debounce window as a bounded, live setting (nocx-3mniv, plan task 3).
//
// Two halves, and they meet in the middle. Here: the number is declared where
// a person can find it, the registry refuses a value outside its bounds and
// accepts one at each, and the value the pipeline reads follows a
// settings.set over the real socket with no restart. In internal/notify:
// what the policy DOES with that number as it moves — an open window keeps
// the length it opened with, the next one uses the new value
// (TestPolicy_Window_*, driven on a manual clock so nothing waits).
//
// The seam between the two halves is the one closure: New builds it, hands it
// to notify.WithWindowSource and keeps it in App.notifyWindow, so the value
// asserted below is the value the running policy asks for.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/settings"
)

// debounceDeclaration fetches the window's declaration the way the Settings
// screen does: over the socket, from settings.describe. Nothing here spells
// a bound — the bounds under test are the ones the backend published.
func debounceDeclaration(t *testing.T, a *App) settings.Declaration {
	t.Helper()
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	resp := callAppWS(t, conn, "settings.describe", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.describe: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	var result struct {
		Declarations []settings.Declaration `json:"declarations"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode settings.describe: %v", err)
	}
	for _, d := range result.Declarations {
		if d.Key == notifyDebounceWindowSetting.Key() {
			return d
		}
	}
	t.Fatalf("the debounce window %q is declared nowhere the Settings screen can see it",
		notifyDebounceWindowSetting.Key())
	return settings.Declaration{}
}

// The number is declared, bounded, and in the same section as the routing
// matrix — the two are one subject and a person tuning notifications should
// not have to find them in two places.
func TestAppDeclaresTheDebounceWindowAsABoundedNumber(t *testing.T) {
	a, _ := routingApp(t)
	d := debounceDeclaration(t, a)

	if d.Control != settings.ControlNumber {
		t.Errorf("the debounce window renders as %q, want a number", d.Control)
	}
	if d.Section != notify.RouteSettingSection {
		t.Errorf("the debounce window is in section %q, want %q beside the routing matrix",
			d.Section, notify.RouteSettingSection)
	}
	if d.Unit == "" {
		t.Error("the debounce window has no unit, so the number on the screen counts nothing")
	}
	if d.Min == nil || d.Max == nil {
		t.Fatalf("the debounce window declares Min=%v Max=%v, want both bounds", d.Min, d.Max)
	}
	// The default is today's constant, so nobody's notifications change on
	// the day the setting lands.
	def, isNum := d.Default.(float64)
	if !isNum {
		t.Fatalf("the debounce window's default is %T, want a number", d.Default)
	}
	if want := notifyDebounceWindow.Seconds(); def != want {
		t.Errorf("the debounce window defaults to %v, want today's constant %v", def, want)
	}
	if def < *d.Min || def > *d.Max {
		t.Errorf("the default %v is outside the declared bounds [%v, %v]", def, *d.Min, *d.Max)
	}
}

// A value outside the bounds is refused BY THE REGISTRY, over the wire, and
// leaves the pipeline on the value it already had. A value AT each bound is
// accepted: a bound tested only from outside is half tested — it would pass
// against a registry that refused everything.
func TestNotifyDebounceWindowRefusesAValueOutsideItsBounds(t *testing.T) {
	a, _ := routingApp(t)
	d := debounceDeclaration(t, a)
	if d.Min == nil || d.Max == nil {
		t.Fatalf("the debounce window declares Min=%v Max=%v, want both bounds", d.Min, d.Max)
	}
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	key := notifyDebounceWindowSetting.Key()
	before := a.notifyWindow()

	for _, refused := range []struct {
		what  string
		value float64
	}{
		{"below the minimum", *d.Min - 1},
		{"above the maximum", *d.Max + 1},
	} {
		resp := callAppWS(t, conn, "settings.set", map[string]any{"key": key, "value": refused.value}, 1)
		if resp.Error == nil {
			t.Errorf("settings.set %v (%s) was accepted, want a refusal", refused.value, refused.what)
			continue
		}
		if got := a.notifyWindow(); got != before {
			t.Errorf("a refused %s left the pipeline's window at %v, want the unchanged %v",
				refused.what, got, before)
		}
	}

	for _, accepted := range []struct {
		what  string
		value float64
	}{
		{"at the minimum", *d.Min},
		{"at the maximum", *d.Max},
	} {
		resp := callAppWS(t, conn, "settings.set", map[string]any{"key": key, "value": accepted.value}, 2)
		if resp.Error != nil {
			t.Errorf("settings.set %v (%s) was refused: code=%d msg=%s",
				accepted.value, accepted.what, resp.Error.Code, resp.Error.Message)
			continue
		}
		want := time.Duration(accepted.value * float64(time.Second))
		if got := a.notifyWindow(); got != want {
			t.Errorf("after accepting the value %s the pipeline's window is %v, want %v",
				accepted.what, got, want)
		}
	}
}

// The stored value governs the live pipeline without a restart: the same
// running backend, the same socket, and the window the policy asks for on its
// next open has moved.
func TestNotifyDebounceWindowFollowsASettingsChangeWithoutARestart(t *testing.T) {
	a, _ := routingApp(t)
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	if got := a.notifyWindow(); got != notifyDebounceWindow {
		t.Fatalf("the pipeline's window starts at %v, want the declared default %v",
			got, notifyDebounceWindow)
	}

	resp := callAppWS(t, conn, "settings.set",
		map[string]any{"key": notifyDebounceWindowSetting.Key(), "value": 20}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.set %s: code=%d msg=%s",
			notifyDebounceWindowSetting.Key(), resp.Error.Code, resp.Error.Message)
	}
	if got, want := a.notifyWindow(), 20*time.Second; got != want {
		t.Fatalf("after the change the pipeline's window is %v, want %v with no restart in between", got, want)
	}

	// And back down, so the test proves a live read rather than a one-way
	// move: nothing is cached from the first answer.
	resp = callAppWS(t, conn, "settings.set",
		map[string]any{"key": notifyDebounceWindowSetting.Key(), "value": 5}, 2)
	if resp.Error != nil {
		t.Fatalf("settings.set back down: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	if got, want := a.notifyWindow(), 5*time.Second; got != want {
		t.Fatalf("after the second change the pipeline's window is %v, want %v", got, want)
	}
}
