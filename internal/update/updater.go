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
	"github.com/shady2k/nocx/internal/version"
)

// ---------------------------------------------------------------------------
// ManifestFetcher — injected for testability
// ---------------------------------------------------------------------------

// ManifestFetcher fetches the signed release manifest from a remote source.
// The production implementation hits GitHub Releases; tests inject a fake.
type ManifestFetcher interface {
	Fetch(ctx context.Context) (FetchedManifest, error)
}

// FetchedManifest binds signed bytes to the channel selected before either HTTP
// request. A settings change affects the next check, never the one in flight.
type FetchedManifest struct {
	Body      []byte
	Signature []byte
	Channel   Channel
}

// ---------------------------------------------------------------------------
// UpdateInfo — the result of a successful Check
// ---------------------------------------------------------------------------

// UpdateInfo describes an available update. It is returned by [Updater.Check]
// after manifest verification, channel-aware build comparison, and artefact matching.
// The caller passes it to [Updater.Apply].
type UpdateInfo struct {
	Version string
	// NotesURL is the link the notice offers. Stable keeps the manifest's own
	// notes page; the nightly channel has no notes of its own, so it is the
	// commit range between the installed build and the offered one — or empty
	// when that range cannot be named (nocx-vh2hr.1.5).
	NotesURL string
	// Channel is the channel whose signed manifest offered this build. The
	// notice's words depend on it: a nightly of the version a person already
	// runs is still a different build, and saying so takes more than the
	// version (ADR-0097, ADR-79b6cy61, nocx-vh2hr.1.5).
	Channel string
	// Commit and BuiltAt are the OFFERED build's stamped identity, from the
	// verified manifest — what the notice names and what the nightly channel
	// compares. They are the only renderer-visible description of a build the
	// person does not have yet.
	Commit  string
	BuiltAt string
	URL     string
	SHA256  string
	Size    int64
}

// buildIdentity is the offered build as the journal records it.
func (i *UpdateInfo) buildIdentity() version.BuildIdentity {
	return version.BuildIdentity{Commit: i.Commit, BuiltAt: i.BuiltAt}
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
	// keyring, compares semver (or a later distinct nightly build), and matches an artefact for the
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
	//
	// Health is a claim about a PAIR: it finalises only when the UI
	// making the claim matches the installed version and build identity.
	// The coordinator must match the version; same-version different-commit
	// reuse remains allowed (ADR-0104). A report against a protocol-
	// compatible OLDER coordinator is the accepted held state
	// retained for already attached ADR-0101 windows (new launches take
	// over across Versions, ADR-0104): refused as certification, held, and
	// the rollback stays suspended while reports keep arriving. Anything
	// else returns an error and leaves the rollback journal intact
	// (pair.go).
	ReportHealthy(ctx context.Context) error

	// State reports what is in flight for a window to tell a person
	// about. Read-only; call after [Updater.Reconcile] has settled the
	// launch, so a journal that reconciliation is about to clean up is
	// not read as pending work.
	State(ctx context.Context) UpdateState
}

// UpdateState is what a window can say out loud about an update
// transaction. It carries no remedy and takes no action: the words and the
// button are the update surface's; these are the facts underneath them.
type UpdateState struct {
	// Pending: a transaction is in flight — the exchange happened and
	// nothing has certified or rolled it back yet.
	Pending bool
	// Version is the version the pending update installs. "" when not
	// pending.
	Version string
	// HeldByOlderCoordinator: a healthy window has reported against a
	// protocol-compatible OLDER coordinator kept under ADR-0101. New
	// launches take over across Versions (ADR-0104). The update finishes
	// when that coordinator turns over —
	// a full restart finishes it now — and auto-rollback is suspended
	// while reports keep arriving. This is the state the update surface
	// must offer to finish, never force.
	HeldByOlderCoordinator bool
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

	// InstallPath is the destination bundle, independent of the running copy.
	InstallPath string

	// StateDir holds the profile's update lock and transaction journal.
	StateDir string

	// Coordinator answers which backend build is actually serving this
	// client. [Updater.ReportHealthy] refuses to finalise without it:
	// certifying an update means certifying a UI/server PAIR, and a
	// composition root that cannot name the backend cannot certify one
	// (see pair.go). The production implementation is the launcher's,
	// coordinator.LaunchProbe.
	Coordinator CoordinatorProbe

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
	currentBuild   version.BuildIdentity
	installPath    string
	stateDir       string
	coordinator    CoordinatorProbe
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
	lockPath := filepath.Join(cfg.StateDir, ".nocx-update.lock")
	return &updater{
		platform:       cfg.Platform,
		fetcher:        cfg.Fetcher,
		keyring:        cfg.Keyring,
		currentVersion: cfg.CurrentVersion,
		currentBuild:   version.Identity(),
		installPath:    cfg.InstallPath,
		stateDir:       cfg.StateDir,
		coordinator:    cfg.Coordinator,
		httpClient:     hc,
		log:            l,
		lockPath:       lockPath,
	}
}

// ---------------------------------------------------------------------------
// GitHub manifest fetcher (production)
// ---------------------------------------------------------------------------

// GitHubManifestFetcher fetches the signed manifest from GitHub Releases.
// Which release it follows is the CHANNEL (ADR-0097): stable — the default —
// rides the "latest" redirect, nightly rides the moving `nightly` prerelease
// tag. It makes two GET requests: one for manifest.json and one for
// manifest.json.sig, from whichever base the selected channel names.
type GitHubManifestFetcher struct {
	// BaseURL is the STABLE channel's base. The field stays settable so a
	// test can point the fetch at its own server; production gets the
	// constant.
	BaseURL string
	// NightlyBaseURL is the NIGHTLY channel's base, overridable for the
	// same reason.
	NightlyBaseURL string
	HTTPClient     *http.Client

	selected channelState
}

// NewGitHubManifestFetcher returns a fetcher pointed at the nocx releases,
// on the stable channel — the default every install keeps until the person
// opts into nightly.
//
// A nil client means "no client was chosen", not "no client exists": it
// falls back to the same 30-second-timeout default NewUpdater applies to
// its download client. One package, one rule for the same missing input —
// before nocx-1ebrs.36 the nil was stored instead and panicked on every
// check, because main.go (the one production caller) passes nil.
func NewGitHubManifestFetcher(client *http.Client) *GitHubManifestFetcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &GitHubManifestFetcher{
		BaseURL:        stableManifestBaseURL,
		NightlyBaseURL: nightlyManifestBaseURL,
		HTTPClient:     client,
	}
}

// SetChannel selects the manifest the NEXT fetch reads. Safe beside a check
// in flight; the one that is running keeps the channel it started with.
func (f *GitHubManifestFetcher) SetChannel(c Channel) { f.selected.set(c) }

func (f *GitHubManifestFetcher) Fetch(ctx context.Context) (FetchedManifest, error) {
	selected := f.selected.get()
	base := f.BaseURL
	if selected == ChannelNightly {
		base = f.NightlyBaseURL
	}
	body, err := f.get(ctx, base+"/manifest.json")
	if err != nil {
		return FetchedManifest{}, fmt.Errorf("fetch manifest: %w", err)
	}
	sig, err := f.get(ctx, base+"/manifest.json.sig")
	if err != nil {
		return FetchedManifest{}, fmt.Errorf("fetch manifest signature: %w", err)
	}
	return FetchedManifest{Body: body, Signature: sig, Channel: selected}, nil
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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}

	// Bound the response size to the declared size + 1 MiB headroom.
	limit := size + 1<<20
	if limit < 0 {
		limit = size
	}
	f, err := os.Create(destPath) //nolint:gosec // destPath is constructed in the extraction dir
	if err != nil {
		return fmt.Errorf("create download file %s: %w", destPath, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(destPath)
		}
	}()

	h := sha256.New()
	written, err := io.Copy(f, io.TeeReader(io.LimitReader(resp.Body, limit), h))
	if err != nil {
		_ = f.Close()
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
