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
	"github.com/shady2k/nocx/internal/storage"
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
	if len(args) == 0 {
		return false, nil
	}
	// Every other diagnostic takes no arguments; the update driver takes its
	// own (--channel), so it is handed the list and parses it itself.
	if len(args) != 1 && args[0] != "--apply-release-update" {
		return false, nil
	}
	switch args[0] {
	case "--build-info":
		return true, json.NewEncoder(out).Encode(version.Info())
	case "--apply-release-update":
		return true, applyReleaseUpdate(ctx, args[1:], out)
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

// applyReleaseUpdate runs the WINDOW's own updater — the same placement, the
// same compiled trust root, the same transaction — without a window, for the
// packaged update proof (nocx-1ebrs.35.5).
//
// It exists because the proof may only run where the product runs, and the only
// other trigger is a person pressing Install in the webview: macOS grants no
// scripted Accessibility permission to a hosted runner, so a driver that
// clicked the button could never be the gate. What is proved is therefore the
// same updater, in the same bundle, driven by this binary instead of by the
// renderer — never a substitute for it. The RELAUNCH and the health report are
// still the real ones: the driver starts the installed app afterwards, its
// renderer reports healthy, and finalisation certifies the pair
// (internal/update/pair.go) exactly as after a person's restart.
//
// It applies only a manifest the compiled keyring accepts, and refuses a
// development build before touching anything. The channel defaults to nightly
// because that is the transition the proof is about; `--channel stable` runs
// the same command against the stable channel.
func applyReleaseUpdate(ctx context.Context, args []string, out io.Writer) error {
	if version.Info().Development {
		return errors.New("updater: an unstamped development build has no updates to apply")
	}
	channel := update.ChannelNightly
	for len(args) > 0 {
		if len(args) < 2 || args[0] != "--channel" {
			return fmt.Errorf("updater: unknown argument %q; usage: --apply-release-update [--channel stable|nightly]", args[0])
		}
		parsed, err := update.ParseChannel(args[1])
		if err != nil {
			return err
		}
		channel = parsed
		args = args[2:]
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("updater: cannot determine executable path: %w", err)
	}
	cfg, fetch, _, err := productionWindowUpdaterConfig(log.NewSlogAdapter(nil), coordinator.NewLaunchProbe())
	if err != nil {
		return err
	}
	// The renderer's SetUpdateChannel is the window's owner of this; here the
	// caller names the channel, because a diagnostic has no settings store.
	fetch.SetChannel(channel)
	paths, err := storage.NewAppPaths()
	if err != nil {
		return fmt.Errorf("updater: resolving the profile directories: %w", err)
	}
	updater, installPath, err := newWindowUpdater(execPath, paths.DataDir(), cfg)
	if err != nil {
		return err
	}
	// Settle anything a previous launch left, exactly as ServiceStartup does.
	if reconcileErr := updater.Reconcile(ctx); reconcileErr != nil {
		return fmt.Errorf("updater: reconcile before apply: %w", reconcileErr)
	}
	info, err := updater.Check(ctx)
	if err != nil {
		return err
	}
	result := struct {
		Step        string `json:"step"`
		Channel     string `json:"channel"`
		InstallPath string `json:"installPath"`
		FromVersion string `json:"fromVersion"`
		ToVersion   string `json:"toVersion"`
		Accepted    bool   `json:"accepted"`
	}{Step: "applied", Channel: channel.String(), InstallPath: installPath, FromVersion: version.Version}
	if info == nil {
		// Already current is not a failure: it is the answer the same check
		// gives a person, and the driver decides whether the run needed a move.
		result.Step, result.ToVersion = "current", version.Version
		return json.NewEncoder(out).Encode(result)
	}
	if err := updater.Apply(ctx, info); err != nil {
		return err
	}
	result.ToVersion, result.Accepted = info.Version, true
	return json.NewEncoder(out).Encode(result)
}
