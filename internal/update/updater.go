package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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
	// BuildNumber is the OFFERED build's number beside the version —
	// YYYYMMDDHHMM, derived from the signed builtAt by the one derivation
	// (version.BuildNumberOf). It is what the notice names on a nightly, where
	// the version alone would describe the build the person already runs
	// (nocx-vh2hr.1.5). Empty when the manifest carried no usable instant.
	BuildNumber string
	URL         string
	SHA256      string
	Size        int64
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
	// Downloading is true while an artefact body is arriving, and Downloaded
	// and Total are its bytes against the signed manifest's size. The notice
	// reads them as percent and size (nocx-vh2hr.1.15).
	Downloading bool
	Downloaded  int64
	Total       int64
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

	// Progress, when set, is called as the artefact's bytes arrive: how many
	// are on disk and how many the signed manifest declared. It is called from
	// the download goroutine and must not block; the window turns it into the
	// number a person reads (nocx-vh2hr.1.15).
	Progress func(done, total int64)

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
	progress       func(done, total int64)
	stallOverride  time.Duration
	progressMu     sync.Mutex
	downloading    bool
	downloaded     int64
	downloadTotal  int64
}

// NewUpdater constructs a concrete Updater.
func NewUpdater(cfg UpdaterConfig) Updater {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = DownloadClient()
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
		progress:       cfg.Progress,
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
//
// IT RESUMES (nocx-vh2hr.1.15): a body that stops arriving, or a connection
// that drops mid-body, leaves what was written on disk and asks again with a
// Range request. The server's own `ETag` is sent back with `If-Range`, so a
// file that changed under us is re-downloaded rather than stitched into a
// corrupt whole, and a server that ignores Range (answering 200) restarts the
// transfer from zero. The declared SHA-256 is still the only acceptance: a
// resume that produced one wrong byte fails here.
func (u *updater) downloadVerified(ctx context.Context, url, sha256Hex string, size int64, destPath string) error {
	u.recordProgress(0, size)
	defer u.endDownload()
	var lastErr error
	for attempt := 0; attempt < downloadAttempts; attempt++ {
		written, etag, err := u.downloadAttempt(ctx, url, size, destPath)
		if err == nil {
			if written != size {
				return fmt.Errorf("download size mismatch: declared %d bytes, got %d", size, written)
			}
			got, hashErr := hashFile(destPath)
			if hashErr != nil {
				return hashErr
			}
			if got != sha256Hex {
				return fmt.Errorf("download sha256 mismatch: declared %s, got %s", sha256Hex, got)
			}
			return nil
		}
		lastErr = err
		// A stall and a dropped body are the same thing to a resume: bytes on
		// disk, more to come. Anything else (a status, a write fault) is the
		// answer.
		if !errors.Is(err, ErrDownloadStalled) && !errors.Is(err, io.ErrUnexpectedEOF) && !isTruncated(err) {
			return err
		}
		if written == 0 || etag == "" {
			// Nothing to resume from, or nothing to validate a resume
			// against: the next attempt starts the body again.
			continue
		}
	}
	if lastErr == nil {
		lastErr = errors.New("download: no attempt was made")
	}
	return fmt.Errorf("download %s: %w", url, lastErr)
}

// downloadAttempt performs one GET (or one ranged continuation) and reports how
// many bytes sit on disk and the entity tag that describes them.
func (u *updater) downloadAttempt(ctx context.Context, url string, size int64, destPath string) (int64, string, error) {
	have, err := fileSize(destPath)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("download request: %w", err)
	}
	etag := readETag(destPath)
	if have > 0 && etag != "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		req.Header.Set("If-Range", etag)
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return have, etag, fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	resumed := resp.StatusCode == http.StatusPartialContent && have > 0
	if resp.StatusCode != http.StatusOK && !resumed {
		return have, "", fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}
	offset := int64(0)
	if resumed {
		offset = have
	}
	if !resumed {
		// The server ignored the range, or this is the first attempt: the
		// body is the whole file.
		offset = 0
	}
	var flag int
	if resumed {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	} else {
		flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(destPath, flag, 0o600) //nolint:gosec // destPath is inside the extraction dir
	if err != nil {
		return offset, "", fmt.Errorf("create download file %s: %w", destPath, err)
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok && resumed {
			// Keep what arrived: the next attempt continues from it.
			return
		}
	}()

	limit := size - offset + 1<<20
	if limit < 0 {
		limit = size + 1<<20
	}
	body := &stallReader{body: io.LimitReader(resp.Body, limit), stall: u.stallWindow()}
	// THE COUNT IS ALWAYS KEPT (nocx-vh2hr.1.25). This reader used to be
	// installed only when a Progress callback was configured, so the updater's
	// own state — the numbers GetUpdateState hands the window — stayed at zero
	// for the whole transfer on every build that passed no callback, which is
	// every shipped one. The state is the surface's source of truth
	// (nocx-vh2hr.1.15); a callback is an optional second reader of the same
	// fact, never the reason the fact exists.
	src := &progressReader{r: body, done: offset, total: size, report: u.recordProgress}
	written, copyErr := io.Copy(f, src)
	ok = true
	if entity := resp.Header.Get("ETag"); entity != "" {
		writeETag(destPath, entity)
	}
	if copyErr != nil {
		return offset + written, etag, fmt.Errorf("download write: %w", copyErr)
	}
	return offset + written, etag, nil
}

func isTruncated(err error) bool {
	// A body cut short by the server is io.ErrUnexpectedEOF (or a bare EOF
	// when nothing arrived). Anything else is a real fault.
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// progressReader reports the running total as bytes pass.
type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.report(p.done, p.total)
	}
	return n, err
}

// recordProgress is the download's own progress state: the same numbers the
// optional callback carries, kept so a surface that asks LATER (the notice
// polls every tick and is rebuilt on every render) can still say where the
// transfer is. It is the ONLY writer of those fields, so the state a person
// sees and any callback cannot disagree.
func (u *updater) recordProgress(done, total int64) {
	u.progressMu.Lock()
	u.downloading, u.downloaded, u.downloadTotal = true, done, total
	u.progressMu.Unlock()
	if u.progress != nil {
		u.progress(done, total)
	}
}

func (u *updater) endDownload() {
	u.progressMu.Lock()
	u.downloading = false
	u.progressMu.Unlock()
}

// stallWindow is the silence this updater calls a stall: the build's default,
// or the shorter one a test states explicitly.
func (u *updater) stallWindow() time.Duration {
	if u.stallOverride > 0 {
		return u.stallOverride
	}
	return downloadStallTimeout
}
