package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
)

// launcherTestResolver is a stub ssh.ConfigResolver for the footprint tests
// (same shape as the deleted P7 planner's): it resolves the destination
// positional against a static map, and can be told to fail (a failed or
// unavailable oracle) or to answer with a RemoteCommand. The status handler
// runs the same ssh -G oracle path to decide which facts a saved connection
// can remove, so the stub's ResolveConfig answers what the oracle would.
type launcherTestResolver struct {
	entries  map[string]ssh.HostConfig
	lastArgv []string
	fail     bool
}

func newLauncherTestResolver() *launcherTestResolver {
	return &launcherTestResolver{entries: make(map[string]ssh.HostConfig)}
}

func (r *launcherTestResolver) add(host string, cfg ssh.HostConfig) { r.entries[host] = cfg }

func (r *launcherTestResolver) ResolveHost(_ context.Context, host string) (string, error) {
	if e, ok := r.entries[host]; ok && e.HostName != "" {
		return e.HostName, nil
	}
	return host, nil
}

func (r *launcherTestResolver) ResolveConfig(_ context.Context, host string) (*ssh.HostConfig, error) {
	if r.fail {
		return nil, ssh.ErrSSHConfigFailed
	}
	if e, ok := r.entries[host]; ok {
		cfg := e
		return &cfg, nil
	}
	return &ssh.HostConfig{HostName: host, User: "testuser", Port: 22}, nil
}

func (r *launcherTestResolver) ResolveArgv(_ context.Context, argv []string) (*ssh.HostConfig, error) {
	r.lastArgv = append([]string(nil), argv...)
	if r.fail {
		return nil, ssh.ErrSSHConfigFailed
	}
	if len(argv) == 0 {
		return nil, ssh.ErrSSHConfigFailed
	}
	return r.ResolveConfig(context.Background(), argv[len(argv)-1])
}

// footprintTestProfileRepo is the smallest profile.ProfileRepository: the
// status handler reads profiles only to resolve which saved connections can
// remove a destination.
type footprintTestProfileRepo struct {
	profiles []profile.SSHProfile
}

func (r *footprintTestProfileRepo) CreateProfile(profile.SSHProfile) error { return nil }
func (r *footprintTestProfileRepo) UpdateProfile(profile.SSHProfile) error { return nil }
func (r *footprintTestProfileRepo) DeleteProfile(string) error             { return nil }

func (r *footprintTestProfileRepo) LoadProfiles() ([]profile.SSHProfile, error) {
	return r.profiles, nil
}

// recordingUninstaller is a transport-side double for the internal/ssh
// capability: it records the host and resolved ConnectConfig the handler
// passed, and answers the canned lists. The dial itself is internal/ssh's
// fixture; here the contract under test is the handler's.
type recordingUninstaller struct {
	host      string
	cfg       *ssh.ConnectConfig
	removed   []string
	conflicts []string
	err       error
}

func (r *recordingUninstaller) UninstallIntegration(_ context.Context, host string, opts ...ssh.ConnectOption) ([]string, []string, error) {
	r.host = host
	r.cfg = &ssh.ConnectConfig{}
	for _, o := range opts {
		o(r.cfg)
	}
	return r.removed, r.conflicts, r.err
}

// ── profileOracleArgv ───────────────────────────────────────────────────

func TestProfileOracleArgv(t *testing.T) {
	cases := []struct {
		name string
		host string
		user string
		port int
		want []string
	}{
		{"bare host, no user, no port", "example.com", "", 0, []string{"ssh", "-G", "example.com"}},
		{"default port omitted", "example.com", "", 22, []string{"ssh", "-G", "example.com"}},
		{"explicit user becomes -l", "example.com", "pi", 0, []string{"ssh", "-G", "-l", "pi", "example.com"}},
		{"explicit port becomes -p", "example.com", "", 2222, []string{"ssh", "-G", "-p", "2222", "example.com"}},
		{"user and port both", "example.com", "pi", 2222, []string{"ssh", "-G", "-p", "2222", "-l", "pi", "example.com"}},
		{"host already carries user@", "pi@example.com", "", 0, []string{"ssh", "-G", "pi@example.com"}},
		// A host that carries user@ AND an explicit user keeps both: -l wins
		// in OpenSSH, and ssh -G answers the same way a connect would.
		{"user@host plus -l", "pi@example.com", "root", 0, []string{"ssh", "-G", "-l", "root", "pi@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := profileOracleArgv(tc.host, tc.user, tc.port)
			if len(got) != len(tc.want) {
				t.Fatalf("profileOracleArgv(%q,%q,%d) = %v, want %v", tc.host, tc.user, tc.port, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("profileOracleArgv(%q,%q,%d) = %v, want %v", tc.host, tc.user, tc.port, got, tc.want)
				}
			}
		})
	}
}

// ── shell.footprint.status ──────────────────────────────────────────────

// footprintStatusHarness builds a WSServer with the status surface's seams:
// a real fact store, the stub resolver (same shape as the planner's), a
// profile resolver, and a profile repo.
func footprintStatusHarness(t *testing.T, facts *ssh.InstalledFactStore, profs []profile.SSHProfile) (*WSServer, *launcherTestResolver) {
	t.Helper()
	ctx := context.Background()
	resolver := newLauncherTestResolver()
	resolver.add("pi@192.168.0.93", ssh.HostConfig{User: "pi", HostName: "192.168.0.93", Port: 22})

	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithInstalledFactStore(facts),
		WithSSHConfigResolver(resolver, "/nonexistent/config"),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
		WithProfileRepository(&footprintTestProfileRepo{profiles: profs}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, resolver
}

func footprintFact(identity string) ssh.InstalledFact {
	return ssh.InstalledFact{
		Identity:      identity,
		Protocol:      "1",
		ScriptVersion: "0.6.0",
		Generation:    "v10",
		ObservedAt:    time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}
}

// TestFootprintStatus_ReportsFactsAndRemovability: a destination a saved
// profile resolves to is removable and names the profile; a direct-host
// destination (no profile) shows the footprint and NO removableProfileId —
// absence of the field IS the explanation, and the surface must not offer a
// button that would fail at click time.
func TestFootprintStatus_ReportsFactsAndRemovability(t *testing.T) {
	facts := ssh.NewInstalledFactStore(
		log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "installed-facts.json")
	if err := facts.Record(footprintFact("pi@192.168.0.93:22")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := facts.Record(footprintFact("root@10.0.0.7:22")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	profs := []profile.SSHProfile{{
		Base:    profile.Base{ID: "p_01"},
		Options: profile.StoredSSHProfileOptions{Host: "pi@192.168.0.93"},
	}}
	ws, _ := footprintStatusHarness(t, facts, profs)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.status", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.status: %+v", resp.Error)
	}
	var got shellFootprintStatusResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Destinations) != 2 {
		t.Fatalf("destinations = %d, want 2", len(got.Destinations))
	}
	// All() orders by identity: "pi@…" sorts before "root@…".
	first, second := got.Destinations[0], got.Destinations[1]
	if first.Identity != "pi@192.168.0.93:22" || second.Identity != "root@10.0.0.7:22" {
		t.Fatalf("destination order = %q, %q; want pi@…, root@…", first.Identity, second.Identity)
	}
	if first.RemovableProfileID == nil || *first.RemovableProfileID != "p_01" {
		t.Errorf("saved-profile destination removableProfileId = %v, want p_01", first.RemovableProfileID)
	}
	if second.RemovableProfileID != nil {
		t.Errorf("direct-host destination removableProfileId = %v, want null (no saved connection)", *second.RemovableProfileID)
	}
	if first.Path != footprintPath {
		t.Errorf("path = %q, want %q", first.Path, footprintPath)
	}
	if first.Generation != "v10" || first.ProtocolVersion != "1" || first.ScriptVersion != "0.6.0" {
		t.Errorf("footprint fields = %+v, want v10/1/0.6.0", first)
	}
	if !first.LastObservedAt.Equal(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("lastObservedAt = %v, want the recorded observation", first.LastObservedAt)
	}

	// The oracle saw the profile resolved through the SAME argv shape the
	// fact-writer uses (ssh -G with the profile's user).
	ws2, resolver := footprintStatusHarness(t, facts, profs)
	defer func() { _ = ws2.Stop(context.Background()) }()
	conn2 := connectWS(t, ws2)
	defer func() { _ = conn2.Close() }()
	if resp := vaultCall(t, conn2, "shell.footprint.status", map[string]any{}, 2); resp.Error != nil {
		t.Fatalf("second status: %+v", resp.Error)
	}
	argv := append([]string(nil), resolver.lastArgv...)
	wantArgv := []string{"ssh", "-G", "-l", "test", "pi@192.168.0.93"}
	if len(argv) != len(wantArgv) {
		t.Fatalf("oracle argv = %v, want %v", argv, wantArgv)
	}
	for i := range argv {
		if argv[i] != wantArgv[i] {
			t.Fatalf("oracle argv = %v, want %v", argv, wantArgv)
		}
	}
}

// TestFootprintStatus_NoFactsIsAnEmptyList: nothing recorded answers
// {"destinations":[]} — an empty array, never null (the renderer maps it).
func TestFootprintStatus_NoFactsIsAnEmptyList(t *testing.T) {
	facts := ssh.NewInstalledFactStore(
		log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "installed-facts.json")
	ws, _ := footprintStatusHarness(t, facts, nil)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.status", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.status: %+v", resp.Error)
	}
	var got shellFootprintStatusResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Destinations == nil || len(got.Destinations) != 0 {
		t.Fatalf("destinations = %#v, want an empty (non-nil) list", got.Destinations)
	}
}

// TestFootprintStatus_NoFactStoreIsStillAnEmptyList: without the fact store
// wired the surface answers nothing recorded rather than failing — the
// honest state of a host nocx has never integrated.
func TestFootprintStatus_NoFactStoreIsStillAnEmptyList(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.status", map[string]any{}, 1)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.status: %+v", resp.Error)
	}
	if !json.Valid(resp.Result) {
		t.Fatalf("result is not valid JSON: %s", resp.Result)
	}
}

// ── shell.footprint.uninstall ───────────────────────────────────────────

// TestFootprintUninstall_DelegatesToCapability: the handler resolves the
// profile, passes the resolved config as the dial options, and returns the
// two lists the capability produced — removed and conflicts both reported,
// because a conflict is information the user acts on.
func TestFootprintUninstall_DelegatesToCapability(t *testing.T) {
	ctx := context.Background()
	rec := &recordingUninstaller{
		removed:   []string{"integration/v10/nocx.zsh", "manifest.json"},
		conflicts: []string{"integration/v10/nocx.bash"},
	}
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithRemoteUninstaller(rec),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.uninstall", map[string]any{"profileId": "p_01"}, 1)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.uninstall: %+v", resp.Error)
	}
	var got shellFootprintUninstallResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Removed) != 2 || got.Removed[0] != "integration/v10/nocx.zsh" || got.Removed[1] != "manifest.json" {
		t.Errorf("removed = %v, want the capability's lists verbatim", got.Removed)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != "integration/v10/nocx.bash" {
		t.Errorf("conflicts = %v, want the capability's lists verbatim", got.Conflicts)
	}
	if rec.host != "pi@192.168.0.93" {
		t.Errorf("capability host = %q, want the resolved profile host", rec.host)
	}
	if rec.cfg == nil || rec.cfg.User != "test" {
		t.Errorf("capability config = %+v, want the resolved ConnectConfig", rec.cfg)
	}
}

// TestFootprintUninstall_Refusals: missing profileId, an unresolvable
// profile, and an unwired capability all refuse loudly — an uninstall that
// is offered must be valid from the state the user is in.
func TestFootprintUninstall_Refusals(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// No capability wired: -32603.
	resp := vaultCall(t, conn, "shell.footprint.uninstall", map[string]any{"profileId": "p_01"}, 1)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("unwired capability: got %+v, want -32603", resp.Error)
	}
	// Missing profileId: -32602.
	resp = vaultCall(t, conn, "shell.footprint.uninstall", map[string]any{}, 2)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("missing profileId: got %+v, want -32602", resp.Error)
	}
}

// TestFootprintUninstall_UnresolvableProfileRefuses: a profile the resolver
// cannot build is refused before any dial — the capability never runs with
// a guessed configuration.
func TestFootprintUninstall_UnresolvableProfileRefuses(t *testing.T) {
	ctx := context.Background()
	rec := &recordingUninstaller{}
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithRemoteUninstaller(rec),
		WithProfileResolver(&failingProfileResolver{}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.uninstall", map[string]any{"profileId": "p_99"}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("unresolvable profile: got %+v, want -32602", resp.Error)
	}
	if rec.host != "" {
		t.Errorf("capability was called (host=%q) although the profile did not resolve", rec.host)
	}
}

// failingProfileResolver refuses every profile.
type failingProfileResolver struct{}

func (f *failingProfileResolver) Resolve(string) (string, *ssh.ConnectConfig, error) {
	return "", nil, errors.New("profile not found")
}

// ── shell.footprint.helperUninstall ───────────────────────────────────────

// recordingHelperCloser is a transport-side double for the composition
// root's helper registry: it records the fingerprints it was asked to
// close, and appends to the shared events slice so a test can assert the
// D25 order against the remover's events — an observable sequence, never a
// sleep.
type recordingHelperCloser struct {
	mu          sync.Mutex
	events      *[]string
	fingerprint string
}

func (c *recordingHelperCloser) CloseHelpersFor(fp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fingerprint = fp
	if c.events != nil {
		*c.events = append(*c.events, "close")
	}
}

// recordingHelperUninstaller is a transport-side double for the internal/ssh
// capability: it records the host and resolved config the handler passed,
// answers the canned removed flag, and appends to the shared events slice.
type recordingHelperUninstaller struct {
	mu      sync.Mutex
	events  *[]string
	host    string
	cfg     *ssh.ConnectConfig
	removed bool
	err     error
}

func (r *recordingHelperUninstaller) UninstallHelper(_ context.Context, host string, opts ...ssh.ConnectOption) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.host = host
	cfg := &ssh.ConnectConfig{}
	for _, o := range opts {
		o(cfg)
	}
	r.cfg = cfg
	if r.events != nil {
		*r.events = append(*r.events, "remove")
	}
	return r.removed, r.err
}

// footprintHelperUninstallHarness wires the helper-uninstall surface with
// the store and both seams, and returns the WSServer plus the event log the
// two recording fakes share.
func footprintHelperUninstallHarness(t *testing.T, installs *consent.InstallStore, closer *recordingHelperCloser, remover *recordingHelperUninstaller) (*WSServer, *[]string) {
	t.Helper()
	events := &[]string{}
	closer.events = events
	remover.events = events
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithHelperInstallStore(installs),
		WithHelperChannelCloser(closer),
		WithRemoteHelperUninstaller(remover),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
	)
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })
	return ws, events
}

// TestFootprintHelperUninstall_ClosesBeforeRemoves is D25, asserted as an
// order: the close event precedes the remove event — the exec channel is
// closed before the install directory is removed — and the observed row is
// forgotten only after the removal succeeded, so the never-connect surface
// stops advertising a helper that is gone.
func TestFootprintHelperUninstall_ClosesBeforeRemoves(t *testing.T) {
	installs := consent.NewInstallStore(
		log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "helper-installs.json")
	if err := installs.Record(consent.Install{
		Fingerprint: "SHA256:deadbeef",
		Identity:    "u@db01:22",
		Path:        "~/.nocx/helper/1-linux-amd64-abc/",
		Hash:        "abc",
		InstalledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	closer := &recordingHelperCloser{}
	remover := &recordingHelperUninstaller{removed: true}
	ws, events := footprintHelperUninstallHarness(t, installs, closer, remover)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.helperUninstall", map[string]any{
		"profileId":   "p_01",
		"fingerprint": "SHA256:deadbeef",
		"path":        "~/.nocx/helper/1-linux-amd64-abc/",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("shell.footprint.helperUninstall: %+v", resp.Error)
	}
	var got shellFootprintHelperUninstallResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Removed {
		t.Fatal("removed = false, want true (a tree existed)")
	}
	// D25's order, as an observable sequence: close, then remove.
	want := []string{"close", "remove"}
	if len(*events) != len(want) {
		t.Fatalf("events = %v, want %v — the close must precede the removal", *events, want)
	}
	for i := range want {
		if (*events)[i] != want[i] {
			t.Fatalf("events = %v, want %v — the close must precede the removal", *events, want)
		}
	}
	if closer.fingerprint != "SHA256:deadbeef" {
		t.Errorf("closer fingerprint = %q, want the row's machine", closer.fingerprint)
	}
	if remover.host != "pi@192.168.0.93" || remover.cfg == nil || remover.cfg.User != "test" {
		t.Errorf("remover = host %q cfg %+v, want the resolved profile's host and config", remover.host, remover.cfg)
	}
	// The inventory row is gone: the surface's next status call will not
	// advertise a helper that was removed.
	if got := installs.All(); len(got) != 0 {
		t.Fatalf("install observations after a successful uninstall = %v, want none", got)
	}
}

// TestFootprintHelperUninstall_NothingInstalledIsANoOp: a host with no
// helper tree uninstalls cleanly — removed=false, no error — and the
// observed row is still forgotten, because the row's whole purpose is to
// describe an install that no longer exists.
func TestFootprintHelperUninstall_NothingInstalledIsANoOp(t *testing.T) {
	installs := consent.NewInstallStore(
		log.NewSlogAdapter(nil), storage.NewDocumentStore(t.TempDir()), "helper-installs.json")
	if err := installs.Record(consent.Install{
		Fingerprint: "SHA256:deadbeef",
		Identity:    "u@db01:22",
		Path:        "~/.nocx/helper/1-linux-amd64-abc/",
		Hash:        "abc",
		InstalledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	remover := &recordingHelperUninstaller{removed: false} // nothing was there
	ws, _ := footprintHelperUninstallHarness(t, installs, &recordingHelperCloser{}, remover)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.helperUninstall", map[string]any{
		"profileId":   "p_01",
		"fingerprint": "SHA256:deadbeef",
		"path":        "~/.nocx/helper/1-linux-amd64-abc/",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("second uninstall of a bare host must succeed: %+v", resp.Error)
	}
	var got shellFootprintHelperUninstallResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Removed {
		t.Fatal("removed = true on a bare host, want false")
	}
	if got := installs.All(); len(got) != 0 {
		t.Fatalf("observations after the no-op uninstall = %v, want none", got)
	}
}

// TestFootprintHelperUninstall_Refusals: missing params, an unwired
// capability or closer, and an unresolvable profile all refuse loudly —
// an uninstall that is offered must be valid from the state the user is in.
func TestFootprintHelperUninstall_Refusals(t *testing.T) {
	ctx := context.Background()
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileResolver(&openProfileResolver{host: "pi@192.168.0.93"}),
	)
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// No capability, no closer: -32603 — D25 cannot be proven, so nothing
	// is removed.
	resp := vaultCall(t, conn, "shell.footprint.helperUninstall", map[string]any{
		"profileId": "p_01", "fingerprint": "SHA256:deadbeef", "path": "~/.nocx/helper/x/",
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("unwired seams: got %+v, want -32603", resp.Error)
	}
	// Missing params: -32602.
	resp = vaultCall(t, conn, "shell.footprint.helperUninstall", map[string]any{"profileId": "p_01"}, 2)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("missing params: got %+v, want -32602", resp.Error)
	}
}

// TestFootprintHelperUninstall_UnresolvableProfileRefuses: the capability
// never runs with a guessed configuration; the dial happens only after the
// profile resolved.
func TestFootprintHelperUninstall_UnresolvableProfileRefuses(t *testing.T) {
	closer := &recordingHelperCloser{}
	remover := &recordingHelperUninstaller{}
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithHelperChannelCloser(closer),
		WithRemoteHelperUninstaller(remover),
		WithProfileResolver(&failingProfileResolver{}),
	)
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(context.Background()) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.helperUninstall", map[string]any{
		"profileId": "p_99", "fingerprint": "SHA256:deadbeef", "path": "~/.nocx/helper/x/",
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("unresolvable profile: got %+v, want -32602", resp.Error)
	}
	if remover.host != "" {
		t.Errorf("capability was called (host=%q) although the profile did not resolve", remover.host)
	}
}

// TestFootprintHelperUninstall_ForgetFailureIsSurfaced: the directory was
// removed but the local observation could not be cleared — the RPC says so
// rather than silently advertising an install that no longer exists; the
// retry heals it (the remote half is then the idempotent no-op).
func TestFootprintHelperUninstall_ForgetFailureIsSurfaced(t *testing.T) {
	dir := t.TempDir()
	installs := consent.NewInstallStore(
		log.NewSlogAdapter(nil), &failOnSecondWrite{DocumentStore: storage.NewDocumentStore(dir)}, "helper-installs.json")
	if err := installs.Record(consent.Install{
		Fingerprint: "SHA256:deadbeef",
		Identity:    "u@db01:22",
		Path:        "~/.nocx/helper/1-linux-amd64-abc/",
		Hash:        "abc",
		InstalledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	remover := &recordingHelperUninstaller{removed: true}
	ws, _ := footprintHelperUninstallHarness(t, installs, &recordingHelperCloser{}, remover)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := vaultCall(t, conn, "shell.footprint.helperUninstall", map[string]any{
		"profileId": "p_01", "fingerprint": "SHA256:deadbeef", "path": "~/.nocx/helper/1-linux-amd64-abc/",
	}, 1)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("unclearable observation: got %+v, want -32603", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "removed") {
		t.Errorf("error = %q, want it to say the helper WAS removed and only the listing failed", resp.Error.Message)
	}
}

// failOnSecondWrite is a DocumentStore whose Write fails once, after a
// successful Record — the shape of a Remove persist failure.
type failOnSecondWrite struct {
	storage.DocumentStore
	writes int
}

func (f *failOnSecondWrite) Write(name string, doc any) error {
	f.writes++
	if f.writes == 2 {
		return errors.New("disk full")
	}
	return f.DocumentStore.Write(name, doc)
}
