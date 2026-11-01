package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/update"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/version"
)

func TestReleaseDiagnosticsBuildInfo(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	handled, err := releaseDiagnostic(context.Background(), []string{"--build-info"}, f)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var info version.BuildInfo
	if err := json.NewDecoder(f).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Version != version.Version {
		t.Fatalf("Version=%q", info.Version)
	}
}

func TestWindowUpdaterUsesProductionFetcher(t *testing.T) {
	_, logger := logtest.New(t)
	cfg, fetch, _, err := productionWindowUpdaterConfig(logger, coordinator.NewLaunchProbe())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = newWindowUpdater("/tmp/nocx", t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fetch.HTTPClient == nil {
		t.Fatal("production constructor stored nil HTTP client")
	}
}

// Through the SAME constructor as ServiceStartup, force a real HTTP exchange.
// An unsigned response must not be confused with an up-to-date result (nil,nil).
func TestWindowUpdaterRefusesUnsignedManifest(t *testing.T) {
	old := version.Version
	version.Version = "0.8.0"
	defer func() { version.Version = old }()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/manifest.json" {
			_, _ = w.Write([]byte(`{"version":"0.7.2"}`))
			return
		}
		_, _ = w.Write([]byte("bm90IGEgc2lnbmF0dXJl"))
	}))
	defer server.Close()
	_, logger := logtest.New(t)
	cfg, fetch, _, err := productionWindowUpdaterConfig(logger, coordinator.NewLaunchProbe())
	if err != nil {
		t.Fatal(err)
	}
	updater, _, err := newWindowUpdater("/tmp/nocx", t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	fetch.BaseURL = server.URL
	if _, err := updater.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("unsigned manifest accepted: %v", err)
	}
	if calls != 2 {
		t.Fatalf("HTTP calls=%d, want manifest and signature", calls)
	}
}

func TestWindowUpdaterReportsFetchFailure(t *testing.T) {
	old := version.Version
	version.Version = "0.8.0"
	defer func() { version.Version = old }()
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, logger := logtest.New(t)
	cfg, fetch, _, err := productionWindowUpdaterConfig(logger, coordinator.NewLaunchProbe())
	if err != nil {
		t.Fatal(err)
	}
	updater, _, err := newWindowUpdater("/tmp/nocx", t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	fetch.BaseURL = server.URL
	if _, err := updater.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("HTTP failure accepted: %v", err)
	}
}

// The production diagnostic wrapper must preserve the fetched channel, not
// silently turn a same-version nightly check back into a stable comparison.
func TestWindowUpdaterOffersLaterNightlyThroughProductionWrapper(t *testing.T) {
	oldV, oldC, oldD := version.Version, version.Commit, version.Date
	version.Version, version.Commit, version.Date = "0.8.0", strings.Repeat("a", 40), "2026-10-27T00:00:00Z"
	defer func() { version.Version, version.Commit, version.Date = oldV, oldC, oldD }()
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	body := []byte(fmt.Sprintf(`{"version":"0.8.0","released":"2026-10-29T00:00:00Z","notesUrl":"https://example.test/notes","build":{"commit":"%s","builtAt":"2026-10-28T00:00:00Z"},"artifacts":[{"os":"linux","arch":"amd64","format":"AppImage","url":"https://example.test/nocx.AppImage","sha256":"%s","size":123},{"os":"linux","arch":"arm64","format":"AppImage","url":"https://example.test/nocx-arm64.AppImage","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":123},{"os":"darwin","arch":"universal","format":"zip","url":"https://example.test/nocx.zip","sha256":"%s","size":123}],"helpers":[]}`, strings.Repeat("b", 40), strings.Repeat("c", 64), strings.Repeat("c", 64)))
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(private, body)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(body)
		case "/manifest.json.sig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx, logger := logtest.New(t)
	cfg, fetch, _, err := productionWindowUpdaterConfig(logger, coordinator.NewLaunchProbe())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Keyring = []ed25519.PublicKey{ed25519.PublicKey(private[ed25519.SeedSize:])}
	fetch.BaseURL, fetch.NightlyBaseURL = srv.URL, srv.URL
	fetch.SetChannel(update.ChannelNightly)
	updater, _, err := newWindowUpdater("/tmp/nocx", t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	info, err := updater.Check(ctx)
	if err != nil || info == nil {
		t.Fatalf("production wrapper lost nightly offer: %v, %v", info, err)
	}
	fetch.SetChannel(update.ChannelStable)
	if info, err := updater.Check(ctx); err != nil || info != nil {
		t.Fatalf("stable comparison changed: %v, %v", info, err)
	}
}

// The packaged diagnostic that drives the macOS proof must refuse before it
// touches a profile, a network or an install: a development build has no
// updater, and a misspelt argument must not silently become the default
// channel (nocx-1ebrs.35.5).
func TestApplyReleaseUpdateRefusesWhatItCannotHonour(t *testing.T) {
	old := version.Version
	defer func() { version.Version = old }()

	version.Version = "dev"
	handled, err := releaseDiagnostic(context.Background(),
		[]string{"--apply-release-update", "--channel", "nightly"}, io.Discard)
	if !handled || err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("development build: handled=%v err=%v", handled, err)
	}

	version.Version = "0.8.0"
	handled, err = releaseDiagnostic(context.Background(),
		[]string{"--apply-release-update", "--force"}, io.Discard)
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown argument") {
		t.Fatalf("unknown argument: handled=%v err=%v", handled, err)
	}

	handled, err = releaseDiagnostic(context.Background(),
		[]string{"--apply-release-update", "--channel", "beta"}, io.Discard)
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown update channel") {
		t.Fatalf("unknown channel: handled=%v err=%v", handled, err)
	}

	// An argument list the flag does not own is somebody else's business.
	if handled, _ := releaseDiagnostic(context.Background(),
		[]string{"--apply-release-update", "--channel"}, io.Discard); !handled {
		t.Fatal("the flag must handle its own argument list")
	}
}
