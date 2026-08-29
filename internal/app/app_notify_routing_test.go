package app

// The routing matrix through production wiring (nocx-3mniv, plan D1/D2/D4).
//
// The wiring is the thing that can be missing: internal/content shipped a
// whole store whose write path had no caller outside its own tests, with every
// gate green (nocx-rtg0). So these tests turn the toggle the way a person does
// — settings.set over the real socket — and then raise the way a program does,
// through the composition root's own ingress.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// routingApp starts the composition root with a toast surface bound in place
// of the transport's, and returns it with a live socket.
func routingApp(t *testing.T) (*App, *recordingToast) {
	t.Helper()
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	p := &recordingToast{}
	a.notifyToast.Set(p)
	return a, p
}

// programEvent takes the session id because the attention policy debounces
// per (session, kind) for eight seconds: two raises from one session inside
// that window collapse into one delivery, which would make a routing test
// green for the wrong reason. Distinct sessions keep the only variable under
// test the routing table.
func programEvent(session string) notify.Event {
	return notify.Event{
		SessionID: session, Title: "build finished", Body: "42 targets",
		Kind: notify.KindProgramNotify, Trust: notify.TrustProgramRequest, Level: notify.LevelInfo,
	}
}

// Every cell of the matrix is declared, in one section, and the section is
// grouped like every other generated section. Nothing here enumerates kinds or
// channels: the expectation is the catalogue's own list.
func describeNotificationSettings(t *testing.T, a *App) []settings.Declaration {
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
	return result.Declarations
}

func notificationCentreDeclarations(t *testing.T, a *App) []settings.Declaration {
	t.Helper()
	const prefix = "notifications.centre."
	var centre []settings.Declaration
	for _, declaration := range describeNotificationSettings(t, a) {
		if len(declaration.Key) >= len(prefix) && declaration.Key[:len(prefix)] == prefix {
			centre = append(centre, declaration)
		}
	}
	return centre
}

func TestNotificationCentreSettingKeysMatchPresentedKindsExactly(t *testing.T) {
	a, _ := routingApp(t)
	centre := notificationCentreDeclarations(t, a)

	got := make(map[string]struct{}, len(centre))
	for _, declaration := range centre {
		got[declaration.Key] = struct{}{}
	}
	want := make(map[string]struct{})
	for _, kind := range notify.DefaultCatalogue().PresentedKinds() {
		want[notify.CentreSettingKey(kind.ID)] = struct{}{}
	}
	if len(got) != len(want) {
		t.Errorf("centre setting count = %d, want exactly %d", len(got), len(want))
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("presented kind key %q has no registered centre toggle", key)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("registered centre toggle %q has no presented kind", key)
		}
	}
}

func TestNotificationCentreSettingsUseRoutingSection(t *testing.T) {
	a, _ := routingApp(t)
	centre := notificationCentreDeclarations(t, a)
	if len(centre) == 0 {
		t.Fatal("settings.describe returned no notification centre toggles")
	}
	for _, declaration := range centre {
		if declaration.Section != notify.RouteSettingSection {
			t.Errorf("centre toggle %q is in section %q, want %q", declaration.Key, declaration.Section, notify.RouteSettingSection)
		}
	}
}

func TestNotificationCentreSettingsUseKindLabels(t *testing.T) {
	a, _ := routingApp(t)
	byKey := make(map[string]settings.Declaration)
	for _, declaration := range notificationCentreDeclarations(t, a) {
		byKey[declaration.Key] = declaration
	}
	for _, kind := range notify.DefaultCatalogue().PresentedKinds() {
		key := notify.CentreSettingKey(kind.ID)
		declaration, ok := byKey[key]
		if !ok {
			t.Errorf("presented kind key %q has no registered centre toggle", key)
			continue
		}
		if want := kind.Label + " → Notification centre"; declaration.Label != want {
			t.Errorf("centre toggle %q label = %q, want %q", key, declaration.Label, want)
		}
	}
}

func TestNotificationCentreSettingsFollowRoutingDeclarations(t *testing.T) {
	a, _ := routingApp(t)
	declarations := describeNotificationSettings(t, a)
	positions := make(map[string]int, len(declarations))
	for i, declaration := range declarations {
		positions[declaration.Key] = i
	}

	routePairs := notify.DefaultCatalogue().Pairs()
	if len(routePairs) == 0 {
		t.Fatal("the catalogue offers no routing pairs")
	}
	presentedKinds := notify.DefaultCatalogue().PresentedKinds()
	if len(presentedKinds) == 0 {
		t.Fatal("the catalogue presents no notification kinds")
	}
	maxRoute := -1
	for _, pair := range routePairs {
		position, ok := positions[pair.SettingKey()]
		if !ok {
			t.Errorf("routing key %q is absent from settings.describe", pair.SettingKey())
			continue
		}
		if position > maxRoute {
			maxRoute = position
		}
	}
	minCentre := len(declarations)
	for _, kind := range presentedKinds {
		key := notify.CentreSettingKey(kind.ID)
		position, ok := positions[key]
		if !ok {
			t.Errorf("centre key %q is absent from settings.describe", key)
			continue
		}
		if position < minCentre {
			minCentre = position
		}
	}
	if maxRoute >= minCentre {
		t.Errorf("centre declarations begin at %d, but a routing declaration appears at %d; centre must follow every routing key", minCentre, maxRoute)
	}
}

func TestAppDeclaresTheNotificationRoutingMatrix(t *testing.T) {
	a, _ := routingApp(t)
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	resp := callAppWS(t, conn, "settings.describe", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.describe: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	var result struct {
		Declarations  []settings.Declaration `json:"declarations"`
		SectionGroups map[string]string      `json:"sectionGroups"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode settings.describe: %v", err)
	}
	declared := map[string]settings.Declaration{}
	for _, d := range result.Declarations {
		declared[d.Key] = d
	}

	pairs := notify.DefaultCatalogue().Pairs()
	if len(pairs) == 0 {
		t.Fatal("the catalogue offers no pairs")
	}
	for _, p := range pairs {
		d, ok := declared[p.SettingKey()]
		if !ok {
			t.Errorf("cell %q is offered by the catalogue and declared nowhere", p.SettingKey())
			continue
		}
		if d.Section != notify.RouteSettingSection {
			t.Errorf("cell %q is in section %q, want %q", p.SettingKey(), d.Section, notify.RouteSettingSection)
		}
		if d.Control != settings.ControlToggle {
			t.Errorf("cell %q renders as %q, want a toggle", p.SettingKey(), d.Control)
		}
		if got, want := d.Default, any(p.DefaultOn); p.DefaultOn && got != want {
			t.Errorf("cell %q defaults to %v, want %v", p.SettingKey(), got, want)
		}
	}
	if got := result.SectionGroups[notify.RouteSettingSection]; got == "" {
		t.Errorf("section %q belongs to no rail group", notify.RouteSettingSection)
	}
}

// The default table is the one the composition root carried by hand before
// this task: a program's notification reaches the toast with nothing ticked.
func TestNotifyRoutingShipsTodaysTable(t *testing.T) {
	a, p := routingApp(t)
	if out := a.notifyIngress.Raise(context.Background(), programEvent("s1")); out.Err != nil {
		t.Fatalf("the raise was refused before acceptance: %v", out.Err)
	}
	if got := len(p.seen()); got != 1 {
		t.Fatalf("the toast saw %d events with the shipped defaults, want 1", got)
	}
}

// The acceptance shape of the epic, minus the browser: turn one cell off in
// Settings and the channel stops receiving, with no restart in between.
func TestNotifyRoutingFollowsASettingsChangeWithoutARestart(t *testing.T) {
	a, p := routingApp(t)
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	if out := a.notifyIngress.Raise(context.Background(), programEvent("s1")); out.Err != nil {
		t.Fatalf("the first raise was refused before acceptance: %v", out.Err)
	}
	if got := len(p.seen()); got != 1 {
		t.Fatalf("the toast saw %d events before the change, want 1", got)
	}

	key := notify.RouteSettingKey("programNotify", notify.ChannelToast)
	resp := callAppWS(t, conn, "settings.set", map[string]any{"key": key, "value": false}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.set %s: code=%d msg=%s", key, resp.Error.Code, resp.Error.Message)
	}

	if out := a.notifyIngress.Raise(context.Background(), programEvent("s2")); out.Err != nil {
		t.Fatalf("the second raise was refused before acceptance: %v", out.Err)
	}
	if got := len(p.seen()); got != 1 {
		t.Fatalf("the toast saw %d events after the cell was turned off, want the same 1", got)
	}

	// And back on, so the test proves a live table rather than a one-way
	// demolition: the same socket, the same running backend.
	resp = callAppWS(t, conn, "settings.set", map[string]any{"key": key, "value": true}, 2)
	if resp.Error != nil {
		t.Fatalf("settings.set %s back on: code=%d msg=%s", key, resp.Error.Code, resp.Error.Message)
	}
	if out := a.notifyIngress.Raise(context.Background(), programEvent("s3")); out.Err != nil {
		t.Fatalf("the third raise was refused before acceptance: %v", out.Err)
	}
	if got := len(p.seen()); got != 2 {
		t.Fatalf("the toast saw %d events after the cell went back on, want 2", got)
	}
}

// Turning every cell of a kind off makes that kind reach nothing — the
// mechanical half of default-deny, through the real registry rather than a
// lookup a test wrote.
func TestNotifyRoutingWithEveryCellOfAKindOffReachesNothing(t *testing.T) {
	a, p := routingApp(t)
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	id := 1
	for _, pair := range notify.DefaultCatalogue().Pairs() {
		if pair.Kind.Kind != notify.KindProgramNotify {
			continue
		}
		resp := callAppWS(t, conn, "settings.set", map[string]any{"key": pair.SettingKey(), "value": false}, id)
		if resp.Error != nil {
			t.Fatalf("settings.set %s: code=%d msg=%s", pair.SettingKey(), resp.Error.Code, resp.Error.Message)
		}
		id++
	}

	out := a.notifyIngress.Raise(context.Background(), programEvent("s1"))
	if out.Err != nil {
		t.Fatalf("the raise was refused before acceptance: %v", out.Err)
	}
	if len(out.Resolved) != 0 {
		t.Errorf("with every cell off the kind still resolved to %d routes", len(out.Resolved))
	}
	if got := len(p.seen()); got != 0 {
		t.Errorf("the toast saw %d events with every cell of the kind off, want 0", got)
	}
	// The feed still remembers it: membership is not delivery (epic 1).
	if snap := a.notifyFeed.Snapshot(); len(snap.Occurrences) != 1 {
		t.Errorf("the feed holds %d occurrences, want the 1 that reached no channel", len(snap.Occurrences))
	}
}
