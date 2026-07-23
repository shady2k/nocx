package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// ---------------------------------------------------------------------------
// ManifestFetcher — injected for testability
// ---------------------------------------------------------------------------

// ManifestFetcher fetches the signed release manifest from a remote source.
// The production implementation hits GitHub Releases; tests inject a fake.
type ManifestFetcher interface {
	Fetch(ctx context.Context) (body []byte, sig []byte, err error)
}

// ---------------------------------------------------------------------------
// UpdateInfo — the result of a successful Check
// ---------------------------------------------------------------------------

// UpdateInfo describes an available update. It is returned by [Updater.Check]
// after manifest verification, semver comparison, and artefact matching.
// The caller passes it to [Updater.Apply].
type UpdateInfo struct {
	Version     string
	NotesURL    string
	URL         string
	SHA256      string
	Size        int64
	manifestRaw []byte // retained so Apply can re-verify
}

// ---------------------------------------------------------------------------
// Updater interface
// ---------------------------------------------------------------------------

// Updater is the platform-agnostic auto-update mechanism.
//
// It orchestrates the Platform seam, manifest verification, and the
// crash-consistency transaction (§7 of the distribution-and-updates design).
type Updater interface {
	// Check fetches the signed manifest, verifies it against the
	// keyring, compares semver, and matches an artefact for the
	// current platform. It returns nil, nil when already current.
	Check(ctx context.Context) (*UpdateInfo, error)

	// Apply downloads and installs the update described by info
	// (which must have been returned by a previous Check call).
	Apply(ctx context.Context, info *UpdateInfo) error

	// Reconcile settles any transaction in flight from a previous
	// launch. Call at startup before Check or Apply.
	Reconcile(ctx context.Context) error

	// ReportHealthy signals that the frontend is running correctly.
	// This is the gate that prevents an auto-rollback after a
	// successful update. Must only be called after the initial tab's
	// renderer mounted and its PTY session opened.
	ReportHealthy(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Concrete implementation
// ---------------------------------------------------------------------------

// UpdaterConfig holds the parameters for NewUpdater.
type UpdaterConfig struct {
	// Platform provides the OS-specific seam (darwin/linux impl).
	Platform Platform

	// Fetcher retrieves the signed manifest. Use NewGitHubManifestFetcher
	// for production; inject a fake for tests.
	Fetcher ManifestFetcher

	// Keyring is the set of trusted ed25519 public keys. Any key
	// in the keyring may sign the manifest.
	Keyring []ed25519.PublicKey

	// CurrentVersion is the version of the running build
	// (internal/version.Version).
	CurrentVersion string

	// InstallPath is the absolute filesystem path to the currently
	// installed bundle — the .app directory on darwin, the AppImage
	// file on linux.
	InstallPath string

	// HTTPClient is used for artefact downloads. If nil, a
	// 30-second-timeout default is used.
	HTTPClient *http.Client

	// Logger receives structured operational logs.
	Logger log.Logger
}

// updater is the concrete [Updater].
type updater struct {
	platform       Platform
	fetcher        ManifestFetcher
	keyring        []ed25519.PublicKey
	currentVersion string
	installPath    string
	httpClient     *http.Client
	log            log.Logger
	lockPath       string // path to the flock file
}

// NewUpdater constructs a concrete Updater.
func NewUpdater(cfg UpdaterConfig) Updater {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	l := cfg.Logger
	if l == nil {
		l = log.NewSlogAdapter(nil) // uses slog.Default()
	}
	// The lock file lives next to the install path (same directory).
	lockPath := filepath.Join(filepath.Dir(cfg.InstallPath), ".nocx-update.lock")
	return &updater{
		platform:       cfg.Platform,
		fetcher:        cfg.Fetcher,
		keyring:        cfg.Keyring,
		currentVersion: cfg.CurrentVersion,
		installPath:    cfg.InstallPath,
		httpClient:     hc,
		log:            l,
		lockPath:       lockPath,
	}
}

// ---------------------------------------------------------------------------
// GitHub manifest fetcher (production)
// ---------------------------------------------------------------------------

// GitHubManifestFetcher fetches the signed manifest from GitHub Releases'
// "latest" redirect. It makes two GET requests: one for manifest.json and
// one for manifest.json.sig.
type GitHubManifestFetcher struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewGitHubManifestFetcher returns a fetcher pointed at the nocx releases.
func NewGitHubManifestFetcher(client *http.Client) *GitHubManifestFetcher {
	return &GitHubManifestFetcher{
		BaseURL:    "https://github.com/shady2k/nocx/releases/latest/download",
		HTTPClient: client,
	}
}

func (f *GitHubManifestFetcher) Fetch(ctx context.Context) ([]byte, []byte, error) {
	body, err := f.get(ctx, f.BaseURL+"/manifest.json")
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest: %w", err)
	}
	sig, err := f.get(ctx, f.BaseURL+"/manifest.json.sig")
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest signature: %w", err)
	}
	return body, sig, nil
}

func (f *GitHubManifestFetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB limit
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Download helper with sha256 + size verification
// ---------------------------------------------------------------------------

// downloadVerified fetches url, writes to destPath, and verifies the
// declared sha256 and size against the downloaded bytes.
func (u *updater) downloadVerified(ctx context.Context, url, sha256Hex string, size int64, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}

	// Bound the response size to the declared size + 1 MiB headroom.
	limit := size + 1<<20
	if limit < 0 {
		limit = size
	}
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create download file %s: %w", destPath, err)
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(destPath)
		}
	}()

	h := sha256.New()
	written, err := io.Copy(f, io.TeeReader(io.LimitReader(resp.Body, limit), h))
	if err != nil {
		f.Close()
		return fmt.Errorf("download write: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close download file: %w", err)
	}

	if written != size {
		return fmt.Errorf("download size mismatch: declared %d bytes, got %d", size, written)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != sha256Hex {
		return fmt.Errorf("download sha256 mismatch: declared %s, got %s", sha256Hex, got)
	}

	ok = true
	return nil
}
