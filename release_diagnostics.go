package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/update"
	"github.com/shady2k/nocx/internal/version"
)

// productionWindowUpdaterConfig is shared by startup and the packaged
// diagnostic. Both use the production HTTP client, endpoint and compiled trust
// root, then pass this configuration through newWindowUpdater for placement.
func productionWindowUpdaterConfig(logger log.Logger, probe *coordinator.LaunchProbe) (update.UpdaterConfig, *update.GitHubManifestFetcher, *recordedManifestFetcher, error) {
	keyring, err := update.ReleaseKeyring()
	fetch := update.NewGitHubManifestFetcher(nil)
	evidence := &recordedManifestFetcher{source: fetch}
	return update.UpdaterConfig{
		Platform: update.NewPlatform(), Fetcher: evidence, Keyring: keyring,
		CurrentVersion: version.Version, Coordinator: probe, Logger: logger,
	}, fetch, evidence, err
}

// The wrapper retains precisely the bytes Check fetched. This is not a second
// request that could succeed while the updater's own fetch is broken. Startup
// doesn't read these snapshots; only the one-shot diagnostic does.
type recordedManifestFetcher struct {
	source update.ManifestFetcher
	mu     sync.Mutex
	body   []byte
	sig    []byte
}

func (f *recordedManifestFetcher) Fetch(ctx context.Context) (update.FetchedManifest, error) {
	fetched, err := f.source.Fetch(ctx)
	f.mu.Lock()
	f.body, f.sig = fetched.Body, fetched.Signature
	f.mu.Unlock()
	return fetched, err
}

// releaseDiagnostic runs before Wails, GTK, a coordinator, a profile, or an OS
// keychain exists. It never reconciles or applies an update to the runner.
func releaseDiagnostic(ctx context.Context, args []string, out io.Writer) (bool, error) {
	if len(args) != 1 {
		return false, nil
	}
	switch args[0] {
	case "--build-info":
		return true, json.NewEncoder(out).Encode(version.Info())
	case "--verify-release-manifest":
		if version.Info().Development {
			return true, errors.New("updater: unstamped development build cannot prove manifest verification")
		}
		execPath, err := os.Executable()
		if err != nil {
			return true, err
		}
		cfg, fetch, evidence, err := productionWindowUpdaterConfig(log.NewSlogAdapter(nil), coordinator.NewLaunchProbe())
		if err != nil {
			return true, err
		}
		// Check never reconciles or applies, so it needs no persistent state.
		updater, _, err := newWindowUpdater(execPath, "", cfg)
		if err != nil {
			return true, err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if _, checkErr := updater.Check(checkCtx); checkErr != nil {
			return true, checkErr
		}
		evidence.mu.Lock()
		body, sig := evidence.body, evidence.sig
		evidence.mu.Unlock()
		if len(body) == 0 || len(sig) == 0 {
			return true, errors.New("updater: Check returned without fetching a signed manifest")
		}
		keys, err := update.ReleaseKeyring()
		if err != nil {
			return true, err
		}
		manifest, err := update.VerifyManifest(body, string(sig), keys)
		if err != nil {
			return true, fmt.Errorf("updater: %w", err)
		}
		sum := sha256.Sum256(body)
		return true, json.NewEncoder(out).Encode(struct {
			Manifest  *update.Manifest `json:"manifest"`
			SHA256    string           `json:"sha256"`
			Signature string           `json:"signature"`
			Source    string           `json:"source"`
		}{manifest, hex.EncodeToString(sum[:]), string(sig), fetch.BaseURL})
	default:
		return false, nil
	}
}
