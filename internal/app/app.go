package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/app/clienthost"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/bootstrapprogress"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/connection"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/contentkey"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/filesystem/sftp"
	gitlocal "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/git/registry"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/lifecycleremote"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/loginshell"
	"github.com/shady2k/nocx/internal/nativeports"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/procwatch"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/reveal"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/skill"
	"github.com/shady2k/nocx/internal/skill/builtin"
	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/uistate"
	"github.com/shady2k/nocx/internal/update"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"github.com/shady2k/nocx/internal/vaultreset"
	"github.com/shady2k/nocx/internal/version"
)

// noteBackupAdapter is the backup's view of the notes store. The store takes
// a context (it is a database); the backup's interfaces do not, because they
// are the shape ADR-0027 settled on. The adapter supplies the background
// context and nothing else — no policy lives here.
type noteBackupAdapter struct {
	store note.Store
}

func (a *noteBackupAdapter) LoadAllNotes() ([]note.Note, error) {
	return a.store.LoadAll(context.Background())
}

func (a *noteBackupAdapter) ReplaceNotes(notes []note.Note) error {
	return a.store.ReplaceAll(context.Background(), notes)
}

type App struct {
	Logger           log.Logger
	Pty              session.PTYFactory
	Session          *session.Reg
	Transport        *transport.WSServer
	ShellIntegration shellintegration.ShellIntegration
	Updater          update.Updater
	Profiles         profile.ProfileRepository
	Credentials      credential.SecretStore

	// vaultCloser releases the vault's background worker and seals it at
	// shutdown. Held as a minimal interface rather than *vault.Vault so the
	// composition root keeps depending on behaviour instead of a type.
	vaultCloser interface{ Close() }
	// noteCloser closes the notes database on shutdown; nil when the store
	// never opened.
	noteCloser interface{ Close() error }

	// discoverySched owns the port-discovery cadence (nocx-wzc4.2); closed
	// at shutdown so no timer outlives the process.
	discoverySched *discovery.Scheduler

	// gitFactory owns the background git-environment resolution
	// (nocx-6pz0); stopped at shutdown so no resolution child outlives
	// the process.
	gitFactory *gitlocal.Factory

	// procs owns the process observation (nocx-cgzc); closed at shutdown so
	// its kernel queue and its goroutine do not outlive the process.
	procs procwatch.Watcher
	// logFilePath is where the backend log file lives — the stable,
	// findable copy of the log the delivery-path decisions are written
	// to (the P0 that had to be diagnosed from a JSON file's mtime). ""
	// means file logging is unavailable and only stderr carries the log.
	logFilePath string
	// logFile is the open append handle, closed at shutdown after the
	// final line. nil when file logging is unavailable.
	logFile *os.File

	// attentionHost is the late-bound implementation behind the notify
	// router's banner route (ADR-0047). The route itself was decided when
	// the table was built; this is only the surface it reaches, and it stays
	// UnavailableHost on every host that never calls SetAttentionHost.
	attentionHost *notify.HostHolder

	// notifyToast is the late-bound implementation behind the notify
	// router's toast route (ADR-0047, plan D2). Same shape as attentionHost
	// and for the same reason: the route was decided when the table was
	// built, and only the surface it reaches arrives later — here, the
	// WebSocket server, which New constructs after the router.
	notifyToast *notify.ToastHolder

	// notifyFeed and notifyIngress are the notification centre: ingress is
	// the pipeline's one entry point (it stamps, records, then submits) and
	// the feed is the bounded in-memory record the bell reads. Held here so
	// the composition root's wiring is assertable — a feature whose write
	// path is reachable only from its own tests is the shape nocx-rtg0
	// shipped.
	notifyFeed    *notify.Feed
	notifyIngress *notify.Ingress

	// notifyWindow is the live read of the debounce-window setting: the very
	// closure the attention policy was given (notify.WithWindowSource), kept
	// here so a test can watch the running pipeline's window follow a
	// settings.set without waiting out a real window. It answers on every
	// call — nothing caches the number, so nothing has to be invalidated
	// when it moves.
	notifyWindow notify.WindowSource

	// UploadSources is the mint for upload SOURCE tickets (design R2): a
	// file that lives on THIS machine is named to the renderer by an opaque
	// backend-minted id, never by a path. Exported for the same reason
	// UIState is — the two gestures that mint one (the native picker and a
	// drop on the window) only exist where a Wails context does, which is
	// main.go. A host with no Wails never mints, and dialog.openFileForUpload
	// then reports itself unavailable.
	UploadSources *transport.SourceTicketStore

	// UIState owns what the app must remember without being asked
	// (ADR-0048) — window geometry and the shell's layout. Exported because
	// main.go is the only place a Wails context exists, and the window half
	// of this document can only be sampled and applied there.
	UIState *uistate.Store

	// slogger is the same logger Logger wraps, kept so an adapter built
	// outside this package (the client-host attention surface) writes to the
	// log file rather than to slog.Default(), which nothing here installs.
	slogger *slog.Logger
}

// Slog returns the backend's structured logger, for adapters built outside
// this package that take a *slog.Logger directly — the client-host attention
// surface is the one that does. Prefer the Logger interface everywhere else.
func (a *App) Slog() *slog.Logger { return a.slogger }

// contentCompactionFloor is the hysteresis fraction of the disk ceiling at
// which an in-progress compaction stops (design §5.4 names hysteresis as
// part of the ceiling). A mechanism parameter, not a user knob.
const contentCompactionFloor = 0.8

// logLevelEnvVar turns the backend's log level up without a rebuild. Read
// from the environment rather than from settings because the sessions worth
// debugging are the ones failing during startup, before any store is open.
const logLevelEnvVar = "NOCX_LOG_LEVEL"

// budgetFromSettings builds the store's two-number budget from the History
// settings (nocx-rtg0.11): the user's retention size and disk ceiling, in
// MiB, become the logical and physical byte budgets. A zero or inverted
// budget is refused, so an unavailable or invalid configuration keeps the
// store closed rather than shipping an unbounded database.
func budgetFromSettings(reg *settings.Registry) (content.Budget, error) {
	retentionMiB, err := reg.GetNumber(settings.HistoryRetentionMiB)
	if err != nil {
		return content.Budget{}, fmt.Errorf("history retention size: %w", err)
	}
	ceilingMiB, err := reg.GetNumber(settings.HistoryDiskCeilingMiB)
	if err != nil {
		return content.Budget{}, fmt.Errorf("history disk ceiling: %w", err)
	}
	b := content.Budget{
		RetentionBytes:   int64(retentionMiB) << 20,
		DiskCeilingBytes: int64(ceilingMiB) << 20,
		CompactionFloor:  contentCompactionFloor,
	}
	if err := b.Validate(); err != nil {
		return content.Budget{}, err
	}
	return b, nil
}

// policyFromSettings builds the live History policy from the settings. The
// composition root updates the same *content.Policy from the settings
// change notifier, so a toggle applies without a restart.
func policyFromSettings(reg *settings.Registry) *content.Policy {
	p := content.NewPolicy()
	if v, err := reg.GetBool(settings.HistoryEnabled); err == nil {
		p.SetEnabled(v)
	}
	if v, err := reg.GetNumber(settings.HistoryRetentionDays); err == nil {
		p.SetRetentionDays(int(v))
	}
	if v, err := reg.GetBool(settings.HistoryOutputEnabled); err == nil {
		p.SetOutputEnabled(v)
	}
	if v, err := reg.GetNumber(settings.HistoryOutputCapKB); err == nil {
		p.SetOutputCapBytes(int(v) << 10)
	}
	return p
}

// clearWindowOnCleanStart is the whole of "reopen tabs and panes on startup"
// being OFF (settings.RestoreOnStartup, nocx-l21ib.4): everything the last
// session left in the window is marked closed, once, before the transport can
// serve a single layout.read.
//
// IT IS THE BACKEND'S ACT AND NOT THE RENDERER'S, though the renderer is what
// decides not to draw the rows, and the choice is deliberate. A renderer
// sweeping through the existing write path would issue one close per leftover
// tab: N transactions where the act is one, so a backend that died halfway
// would leave half a session open and reopen exactly it on the next launch
// with restore back on — the defect this exists to end, one launch later. It
// would also mint a replacement tab on the last close (that is what a close
// does when the application is left with none) and then have to reconcile a
// row nobody asked for. Here the same act is one transaction, and it lands
// where the store's other startup reconciliations already live: closeOpenEntries
// and dropDeadSessions run on the same argument — a backend start IS an
// application start (D5), and this Open is the new one.
//
// The POLICY stays in the composition root and the MECHANISM in the store,
// which is why the setting is read here rather than in content.Config: the
// store has no business knowing what a person ticked in Settings.
//
// A failure is a WARN and nothing more. The window still opens, on the
// leftovers the sweep did not reach — worse than a clean start and much
// better than a backend that refuses to start over a preference.
func clearWindowOnCleanStart(ctx context.Context, reg *settings.Registry, db content.ContentDB, logger *slog.Logger) {
	restore, err := reg.GetBool(settings.RestoreOnStartup)
	if err != nil {
		logger.Warn("restore-on-startup setting unreadable; opening on what was left", "error", err)
		return
	}
	if restore {
		return
	}
	if err := db.Layout().ClearWindow(ctx); err != nil {
		logger.Warn("clean start could not close the last session's tabs; they may reopen later", "error", err)
	}
}

// SetDialogService attaches the native dialog capability (dialog.openFile and
// dialog.openDirectory — one service, because one native dialog is open at a
// time). It is wired by New, after the transport was built and before Start,
// so no renderer request can observe the unset state.
//
// THE CALLER MOVED AND THE CONTRACT DID NOT. It used to be main.go's
// WailsApp.startup, because the Wails context the picker needed existed only
// there and main.go was this process. main.go is another process now (design
// D3), so the implementation New wires is the client-backed one — it asks an
// attached client for the picker — and the seam is still what a host without
// one leaves unset: dialog.* then answers -32601, which is the dev-web
// harness and the headless suites.
func (a *App) SetDialogService(ds transport.DialogService) {
	a.Transport.SetDialogService(ds)
}

// SetUrlOpener attaches the native URL-open capability (shell.openUrl RPCs).
// Like SetDialogService it is wired by New, after the transport was built and
// before Start, so no renderer request can observe the unset state, and for
// the same reason it is now a client-backed implementation rather than a
// Wails-backed one.
func (a *App) SetUrlOpener(opener transport.UrlOpener) {
	a.Transport.SetUrlOpener(opener)
}

// SetAttentionHost binds the desktop attention surface behind the notify
// router's banner route (ADR-0047). Like SetDialogService it is wired by New,
// after the router was built and before Start, so no raise can observe the
// unset state, and it binds the client-backed surface: the coordinator has no
// desktop of its own to raise a banner on.
//
// It binds an implementation, never a destination. The route was decided when
// the routing table was built and is not reachable from here; a host that
// never calls this keeps UnavailableHost, and its raises are visible failed
// deliveries rather than silent drops.
func (a *App) SetAttentionHost(host notify.AttentionHost) {
	a.attentionHost.Set(host)
}

// FocusSession asks the renderer to bring the pane holding sessionID to the
// front (nocx-jiwq.1, plan D1). It is the composition root's half of a banner
// click: the desktop shell raises the window, and the tab is the part only the
// renderer can do.
//
// It carries a SESSION and nothing else. The renderer owns session -> tab
// (PaneManager.findBySession) and the backend cannot do it at all — a tab id
// here would be a second addressing identity no part of the backend can own
// (nocx-wyp3p). Nothing is returned because there is nothing a caller could do
// with a failure: with no renderer attached the push is dropped, and stalling
// the click callback to report that would be worse than dropping it.
func (a *App) FocusSession(sessionID string) {
	a.Transport.FocusSession(sessionID)
}

type Option func(*optionSet)

type optionSet struct {
	// logFilePath overrides where the backend log file lives. Test-only:
	// without it New() resolves the profile's data directory, and a test
	// must not write into the developer's real profile (nocx-ti8w).
	logFilePath *string
	// keystore is what the caller has decided about the OS keystore; see
	// keystoreStance.
	keystore keystoreStance
	// keystoreReason is why a test asked for the real store back. Required
	// with keystoreReal, and logged, so a keychain prompt during a run can
	// be traced to the test that asked for it.
	keystoreReason string
}

// WithRealSystemKeystore reaches the real OS keystore, and says why.
//
// The exception rather than the rule, in both the places that may use it. In
// a TEST it writes into the login keychain of whoever is running the suite —
// on macOS a modal dialog per backend start (nocx-o4hg). In a LAUNCHER it is
// the desktop declaration D10 describes: a composition root that knows it is
// starting a backend inside a login session says so here, and the build
// property (keystore.go) is what answers for everything that does not.
//
// It no longer describes what saying nothing means: an undeclared stance off
// `go test` takes buildKeystoreStance, which by default is "out of play",
// because a coordinator that lives for days on a headless host must not
// discover its keystore by writing to one.
//
// The reason is required and is logged at startup, so a keychain prompt seen
// during a run leads back to whatever asked for it.
func WithRealSystemKeystore(reason string) Option {
	return func(o *optionSet) {
		o.keystore = keystoreReal
		o.keystoreReason = reason
	}
}

// WithLogFilePath pins the backend log file path instead of the app-dir
// default. Test-only: an empty path disables file logging (stderr only);
// any other path must be under a disposable directory the test owns.
func WithLogFilePath(path string) Option {
	return func(o *optionSet) { o.logFilePath = &path }
}

// notifyDebounceWindow is how long one session and kind is held quiet AFTER a
// notification has gone out. The debounce is leading-edge (notify.Policy):
// the first event is delivered at once, and the window suppresses what follows
// it, closing with one summary naming how many were held. So this number is
// not a delay on anything — it is how much of a burst collapses into that one
// summary. Eight seconds is termic's number for the same job (design §6.2),
// long enough to absorb a build's chatter.
//
// It is no longer what the pipeline runs on — it is the DEFAULT of the
// setting below, and the floor the policy falls back to if that setting is
// ever unreadable. The two roles are the same number on purpose: a person who
// never opens Settings gets exactly the behaviour this constant described
// before it had a control beside it.
const notifyDebounceWindow = 8 * time.Second

// The bounds of that setting, as durations, because a duration is what the
// thing IS — the seconds the registry stores are derived from these and never
// spelled a second time.
//
// A second is the floor because below it the debounce stops being one: the
// window exists so a loop cannot produce a notification per iteration, and at
// a tenth of a second it very nearly can. Five minutes is the ceiling because
// past it the leading edge is all anyone would ever see — a summary five
// minutes after the thing it summarises is about a build the person has
// already gone to look at. Neither bound is a safety limit; both are the
// range in which the control still does the job it is named for.
const (
	notifyDebounceWindowMin = 1 * time.Second
	notifyDebounceWindowMax = 5 * time.Minute
)

// notifyDebounceWindowSetting is that window, as the bounded number a person
// can move. It is declared at package init beside the routing matrix and in
// the same section, because the two are one subject: which notifications
// reach you, and how much of a burst collapses before they do.
//
// The stored unit is SECONDS rather than a Duration, because the registry's
// numbers are float64 and a control that says "8" with "seconds" beside it is
// the only spelling a person has to read. Every conversion back to a Duration
// happens in one place (the window source New builds), so there is no second
// answer to what the number means.
var notifyDebounceWindowSetting = settings.MustRegisterNumber(settings.NumberSpec{
	Key:         "notifications.debounceSeconds",
	Section:     notify.RouteSettingSection,
	Label:       "Collapse repeats for",
	Description: "After a notification goes out, further notifications of the same kind from the same tab are held back for this long and arrive as one summary naming how many there were. The first notification is never delayed — this is how much of a burst collapses behind it, not a wait before it. A change applies to the next burst: one already collapsing keeps the length it started with.",
	DataClass:   settings.PublicConfig,
	Default:     notifyDebounceWindow.Seconds(),
	Min:         notifyWindowBound(notifyDebounceWindowMin),
	Max:         notifyWindowBound(notifyDebounceWindowMax),
	Unit:        "seconds",
})

// notifyWindowBound is a NumberSpec bound expressed as the duration it means.
func notifyWindowBound(d time.Duration) *float64 {
	seconds := d.Seconds()
	return &seconds
}

// The feed's budgets. Rows alone are not a usable unit of account — a single
// occurrence may carry 8 KiB of title and body — so both bind, and eviction is
// always recorded in the dropped row rather than being silent.
//
// The collapse window is deliberately much shorter than any read lifetime. If
// it were not, two separate acts sharing a session, kind and level (two
// deploys an hour apart) would merge into one row simply because nobody had
// cleared the inbox.
const (
	notifyFeedMaxOccurrences   = 200
	notifyFeedMaxRetainedBytes = 1 << 20
	notifyFeedCollapseWindow   = 30 * time.Second
	// The tail a collapsed row retains so the panel can expand it (D2).
	// Twenty: enough that an expansion is worth opening, small enough that
	// a runaway session's row costs a bounded amount — the rest are
	// counted and the expansion says so.
	notifyFeedMaxRunRetained = 20
)

// notifyBannerTarget and notifyToastTarget are the resolved
// Destination.Target of the two local routes: a local sink's target is its own
// name (notify.Destination), the router carries it into every outcome, and a
// failed delivery repeats it to say which channel failed.
//
// The WORD is the catalogue's, not this file's (AD-8). It used to be spelled
// here because the table was written here; now the table is built from the
// catalogue, which also derives the settings key of every cell from the same
// id — so a second spelling in the composition root would be a second answer
// to "what is this channel called". These stay as the names the composition
// root and its tests reach for, bound to the one declaration.
const (
	notifyBannerTarget = notify.ChannelBanner
	notifyToastTarget  = notify.ChannelToast
)

// notifyRouteToggles is the routing matrix as settings: one Bool per offered
// (kind, channel) cell, declared from the catalogue.
//
// It is declared at package init, like every other setting, because that is
// what a settings declaration is — MustRegisterBool appends to a list read
// once and panics on a duplicate key, so declaring the matrix inside New would
// panic the second time a process built an App. What New does with it is READ
// it, which is a different thing and happens per composition root.
//
// Nothing here enumerates a kind or a channel: the loop is over the
// catalogue's offered pairs, so a cell the trust bound forbids has no toggle
// to turn on, and a kind added to the catalogue tomorrow gets its row of
// toggles with no edit in this file (D1, D3).
var notifyRouteToggles = registerNotifyRouteToggles()

func registerNotifyRouteToggles() map[string]*settings.Bool {
	settings.RegisterSectionGroup(notify.RouteSettingSection, "application")
	pairs := notify.DefaultCatalogue().Pairs()
	toggles := make(map[string]*settings.Bool, len(pairs))
	for _, pair := range pairs {
		toggles[pair.SettingKey()] = settings.MustRegisterBool(settings.BoolSpec{
			Key:         pair.SettingKey(),
			Section:     notify.RouteSettingSection,
			Label:       pair.SettingLabel(),
			Description: pair.SettingDescription(),
			DataClass:   settings.PublicConfig,
			Default:     pair.DefaultOn,
		})
	}
	return toggles
}

// The centre visibility settings are intentionally unread by Go: the renderer
// reads them for presentation, while the backend records every event regardless.
// A Go reader would violate that invariant by making recording depend on display.
func init() {
	for _, kind := range notify.DefaultCatalogue().PresentedKinds() {
		key := notify.CentreSettingKey(kind.ID)
		settings.MustRegisterBool(settings.BoolSpec{
			Key:         key,
			Section:     notify.RouteSettingSection,
			Label:       kind.Label + " → Notification centre",
			Description: "The event is recorded either way; this governs whether the panel shows it and whether the bell counts it; turning it back on brings back what the feed still holds.",
			DataClass:   settings.PublicConfig,
			Default:     true,
		})
	}
}

func New(opts ...Option) (*App, error) {
	var o optionSet
	for _, opt := range opts {
		opt(&o)
	}
	// Before anything is built: what is this backend's stance on the OS
	// keystore, and what follows from it? Declared, never discovered
	// (keystore.go, design D10) — the probe that would discover it is a
	// keychain write, and on a host with no keychain that write is a modal
	// nobody can dismiss. A test that has not said is refused here rather
	// than silently writing to the login keychain of whoever is running the
	// suite (nocx-o4hg).
	keystore, stanceErr := decideKeystore(testing.Testing(), &o)
	if stanceErr != nil {
		return nil, stanceErr
	}

	// Resolve the profile paths FIRST: the backend log file lives in the
	// data directory of the profile THIS build owns (appdir.go's dev/release
	// split is decided by the build tag), so the dev stand and the shipped
	// app never write one file and a dev run never touches the shipped
	// profile (nocx-ti8w).
	paths, err := storage.NewAppPaths()
	if err != nil {
		return nil, fmt.Errorf("storage paths: %w", err)
	}

	skillRoots := []skill.Root{
		{Dir: filepath.Join(paths.ConfigDir(), "skills"), Provenance: skill.ProvenanceAuthored},
		{FS: builtin.FS, Provenance: skill.ProvenanceBuiltin},
		{Dir: filepath.Join(paths.ConfigDir(), "managed-skills"), Provenance: skill.ProvenanceManaged},
	}
	skills := skill.NewLibrary(skillRoots)

	logFilePath := filepath.Join(paths.DataDir(), "nocx.log")
	if o.logFilePath != nil {
		logFilePath = *o.logFilePath // test override; empty disables file logging
	}
	var logFile *os.File
	// The level is a knob, not a constant. It was slog.LevelInfo in both
	// handlers below, so every Debug line in this codebase was unreachable
	// without editing a source file and rebuilding — which is why debugging a
	// live session meant adding console.log to the renderer and a temporary
	// Warn to the backend, and why the cause of a whole class of e2e failures
	// stayed "unknown" across three triage rounds (nocx-cbtc, nocx-xplc).
	//
	// An env var rather than a setting: the thing you need to turn up is the
	// startup of a session that is already going wrong, and a setting is read
	// from a store this runs before. Unrecognised values fall back to info
	// rather than failing — a mistyped level must never stop the app starting,
	// and the fallback says so in the log.
	logLevel := slog.LevelInfo
	levelName := strings.ToLower(strings.TrimSpace(os.Getenv(logLevelEnvVar)))
	badLevel := ""
	switch levelName {
	case "":
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
	case "warn", "warning":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		badLevel = levelName
	}
	// AddSource everywhere: every line names the module, function and line
	// that wrote it (internal/log says why the adapter is what makes this
	// true rather than pointing every record at the adapter itself).
	handlerOpts := &slog.HandlerOptions{Level: logLevel, AddSource: true}
	slogger := slog.New(slog.NewTextHandler(os.Stderr, handlerOpts))
	if logFilePath != "" {
		if mkErr := os.MkdirAll(filepath.Dir(logFilePath), 0o700); mkErr != nil {
			slogger.Warn("backend log file unavailable; logging to stderr only",
				"path", logFilePath, "error", mkErr)
			logFilePath = ""
			// #nosec G304 — the path is the app data dir plus a fixed name,
			// or a test override; never external input.
		} else if f, openErr := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); openErr != nil {
			slogger.Warn("backend log file unavailable; logging to stderr only",
				"path", logFilePath, "error", openErr)
			logFilePath = ""
		} else {
			// Keep stderr AND the file: the log must survive wherever the
			// launcher redirected stderr (the P0 that landed in a temp dir
			// nobody would look in), and still be visible on the console.
			logFile = f
			slogger = slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f), handlerOpts))
		}
	}
	logger := log.NewSlogAdapter(slogger)
	// The log names itself, first: a running session can say where the
	// file is by reading its own first line.
	if logFilePath != "" {
		logger.Info("backend log file", "path", logFilePath)
	}
	// Said after the logger exists, so it lands in the file too — and said at
	// all, because a level that silently did not apply is worse than no knob.
	if badLevel != "" {
		logger.Warn("unrecognised log level; using info",
			"var", logLevelEnvVar, "value", badLevel, "known", "debug, info, warn, error")
	}
	if logLevel == slog.LevelDebug {
		logger.Info("log level", "level", "debug", "var", logLevelEnvVar)
	}

	// The logrus containment (design §4.1): logrus arrives compiled-in
	// through eino's dependency graph (compose → schema → gonja/exec), and
	// nothing from it reaches stderr around our one slog-backed interface.
	// The redirect is process-global and idempotent — see
	// logrus_containment.go, and the receipt test that pins it.
	installLogrusContainment(logger)

	shint := shellintegration.New(logger)
	// The child-domain registries (nocx-u7uh.11): the grant builder needs
	// to know each lifecycle transport's kind (fd vs forwarded port) and
	// each lane's owning session before it can compose a child bootstrap.
	// They are fed by the two adapter factories below and by the same
	// registerLane closure that binds lanes to the transport's session
	// registry.
	childTransports := newTransportRegistry()
	childSessions := newSessionRegistry()
	// One process observer for the whole backend, built here and injected:
	// "is the shell we started still the process running there" is one
	// question with one owner (nocx-cgzc), the platform half is per-OS, and
	// a per-session observer would mean a kernel queue and a goroutine per
	// tab.
	procs := procwatch.New(logger)
	// One login-shell resolver, built here and injected: "which shell is this
	// user's login shell" is one question with one owner (nocx-wwz0), and the
	// composition root is where the platform half gets wired in.
	ptf := &localPTYFactory{
		log: logger, shint: shint, transports: childTransports,
		shells: loginshell.New(), procs: procs,
	}
	sess := session.New(logger, ptf)

	// SSH config resolver: shared by both the SSH client and the profile
	// resolver so the authorization comparison matches canonical hostnames.
	// AD-4: nocx asks OpenSSH via ssh -G; the injected resolver is the sole
	// path through which ~/.ssh/config is read.
	home, _ := os.UserHomeDir()
	sshConfigPath := filepath.Join(home, ".ssh", "config")
	sshCfgResolver := ssh.NewSSHConfigResolver(logger, sshConfigPath, "")

	// The typed-`ssh` delivery (ADR-0049, design §4.3): the one owner of
	// "does nocx interpose on a line the user typed, and on which socket".
	// Assembled here because every part of it is a product decision — which
	// oracle answers for the user's configuration, where our control sockets
	// live, how a multiplex master is proven, and who publishes over it —
	// and the composition root is where product decisions belong.
	typedSSH := &typedRunner{
		log:      logger,
		wrapper:  ssh.NewTypedWrapper(logger, sshCfgResolver, ssh.DefaultControlRoot()),
		dial:     DialTypedMux,
		publish:  shint,
		sessions: sessionWindows{reg: sess},
		probes:   defaultMasterProbes,
	}

	// SSH client (AD-4): real client on x/crypto/ssh, honors ~/.ssh/config.
	sshClient, err := ssh.NewReal(logger, ssh.WithConfigResolver(sshCfgResolver))
	if err != nil {
		return nil, fmt.Errorf("ssh client: %w", err)
	}
	sess = sess.WithSSHFactory(&sshFactoryAdapter{client: sshClient})

	// Vault (ADR-0011 as amended): owns provider routing, key material and
	// the seal lifecycle. Two providers are compiled on every platform:
	// system (OS keychain) and file (encrypted document).
	docStore := storage.NewDocumentStore(paths.ConfigDir())
	profileStore := profile.NewJSONStoreWithDocStore(docStore, "profiles.json")
	// The snippet library is the same document family: one versioned
	// document under the profile directory, sharing the docStore. The id
	// source is injected rather than called inline so tests can force
	// collisions and this composition root is the one place that decides
	// what a rand failure means; the fallback keeps Create returning a
	// non-empty id instead of panicking inside a handler (design §5.1).
	snippetStore := snippet.NewJSONStore(docStore, snippet.DocumentName)
	snippetSvc := snippet.NewService(snippetStore, func() string {
		var raw [16]byte
		if _, rerr := rand.Read(raw[:]); rerr != nil {
			return fmt.Sprintf("snip-%d", time.Now().UnixNano())
		}
		return hex.EncodeToString(raw[:])
	})

	// The API-testing collection (design §6, §7). Both are constructed here
	// and handed to the transport, which is what makes internal/apicoll and
	// internal/apisend reachable from main() at all — before this they were
	// reachable from their own tests and nowhere else (AGENTS.md check 5).
	//
	// The collection service holds no state that outlives the process: the
	// app remembers the LIST of opened folders, never their contents, so
	// handles are minted fresh each run and every read goes to disk. It is
	// given the app paths because a collection created with no place named
	// goes to a default folder under the app directory (§6.1) — the location
	// is derived inside apicoll, so no caller names a path in order to get
	// one, and the build tag decides which app directory that is.
	//
	// The sender is given the ROUTE TABLE, which is what makes an
	// environment's "how to get there" reach the dialer (§6.5, §7.1): a
	// direct environment sends from this machine, and one naming a
	// connection sends through a lease on that profile's pooled SSH
	// connection. A connection that cannot be leased FAILS the send — it is
	// never quietly downgraded to a local dial, which would put a production
	// request on this machine's own interface, around the bastion the
	// environment named.
	//
	// The binding document itself is built further down, once the vault
	// exists: it is the one thing in this feature that holds an identifier
	// for stored credential material (design §8.1), so it cannot be
	// constructed before the store that holds the values.
	apiCollections := apicoll.NewCollections(paths)
	apiRoutes := &apiRouteLeaser{client: sshClient}
	// The import's URL entrance gets the SAME route table the sender has,
	// so "through prod-bastion" means one thing in this product: a fetch
	// and a send that name one connection lease the same pooled SSH
	// connection, and a connection that cannot be leased refuses both. A
	// second table here would be a second answer to "how do I get there",
	// agreeing until the day one of them was edited.
	apiRouteTable := apisend.NewRoutes(apiRoutes)
	apiSender := apisend.New(
		apisend.WithLogger(logger),
		apisend.WithRoutes(apiRouteTable),
	)
	apiFetcher := apifetch.New(apiRouteTable, logger)

	// The UI-state document (ADR-0048): the same document family again, and
	// deliberately NOT the settings registry — a drag is not a decision. It
	// never fails to open, because an absent document is an ordinary state
	// and an unreadable one costs the user their window size, not their
	// launch.
	uiStateStore := uistate.New(docStore, slogger)

	// The installed fact (nocx-mlm7 P7, design §5.4): backend-owned,
	// persisted across restarts, keyed by the resolved destination
	// identity, written only from a passport the renderer accepted and
	// invalidated when a connection that expected installed-script
	// produces no passport. The delivery planner reads it to choose the
	// compact installed line; without it every host bootstraps.
	installedFacts := ssh.NewInstalledFactStore(logger, docStore, "installed-facts.json")
	// The helper consent (remote-helper design D8; the 2026-08-10 consent
	// design): the per-machine relay-tier answer, keyed by the remote
	// host's public-key fingerprint, and the observed helper installs the
	// footprint surface lists. Both are backend-owned and persisted; the
	// consent decision at git.open and the footprint listing read them, so
	// without these lines the consent path is reachable from its own tests
	// and nowhere else (AGENTS.md check 5).
	helperConsent := consent.NewStore(logger, docStore, "helper-consent.json")
	helperInstalls := consent.NewInstallStore(logger, docStore, "helper-installs.json")
	// The helper-backed git factory and the registry that owns its live
	// helper channels (remote-helper design D8, D25): one registry serves
	// both the factory's per-session helpers and the uninstall surface's
	// close-before-remove, so a machine's channels are closed by the same
	// bookkeeping that started them.
	helperFactory, helperReg := helperGitFactory(sshClient, helperConsent, helperInstalls, slogger)
	// ContentDB (ADR-0018, amended 2026-08-01): the one SQLite database for
	// unbounded private content, encrypted at rest by the adiantum VFS
	// (ncruces/go-sqlite3 — no cgo). The real store is constructed below,
	// after the vault exists, via the content key lifecycle (nocx-rtg0.9);
	// the stub is the null implementation per AD-8 and the fallback when
	// the key cannot be read (the terminal starts without durable history
	// and history.query answers source=session, which the overlay labels
	// honestly).
	var contentDB content.ContentDB = content.NewStub(logger)

	// The provider comes from the stance and from nowhere else: building one
	// here would be the second opinion keystore.go exists to make
	// impossible.
	sysProv := keystore.provider
	fileProv := file.New(docStore, "vault-file.json")
	reg, err := vault.NewRegistry(sysProv, fileProv)
	if err != nil {
		return nil, fmt.Errorf("vault registry: %w", err)
	}

	ctx := context.Background()
	// Probe the system provider once at startup and log the outcome. A
	// machine with no Secret Service says so in the log rather than
	// failing mysteriously later. Only a caller that may reach the real
	// store probes: a probe is a real keystore call, so it must not run on
	// a host the caller has declared to have no keystore, and must not run
	// at all from a test that has not asked for it (nocx-o4hg).
	probeStatus := vault.Status{}
	systemReady := false
	if keystore.probe {
		slogger.Info("the OS keystore is in play",
			"stance", keystore.stance.String(),
			"declaredBy", keystore.source,
			"reason", keystore.reason)
		probeStatus = sysProv.Probe(ctx)
		systemReady = probeStatus.Ready
	} else {
		// Nothing is asked. The provider answers "excluded" without touching
		// the keyring, which is what makes "no modal on a headless host" a
		// property of construction rather than of error handling.
		slogger.Info("the OS keystore is out of play; the file provider is the vault's store",
			"stance", keystore.stance.String(),
			"declaredBy", keystore.source)
		probeStatus = sysProv.Status(ctx)
	}
	slogger.Info("vault system-provider availability probe",
		"ready", systemReady, "reason", probeStatus.Reason)

	v, err := vault.New(docStore, reg, slogger)
	if err != nil {
		return nil, fmt.Errorf("vault init: %w", err)
	}
	// One stanced material resolver serves both endpoint connections and the
	// egress gate. Its unsealer is the vault itself; the requester is attached
	// to the vault below once the transport exists.
	credResolver := credential.NewResolver(v, func(err error) bool {
		return errors.Is(err, vault.ErrVaultSealed)
	}, v)

	// API requests resolve only opaque secrow handles through the capability
	// seam. The terminal's ResolveLine remains name-based; this adapter is
	// deliberately restricted to collection-file references.
	apiSecretRefs := capability.NewSecretRefs(v, apiSecretMaterial{credResolver}, profileStore, profileStore)

	settingsRegistry := settings.New(docStore, v)

	// The content key opens BOTH encrypted stores — the history database
	// and the notes one. One key, one lifecycle, two files: they differ in
	// their UPGRADE rule, not in their secrecy. History rebuilds its file
	// when the schema moves (a log can be re-made by living); notes migrate
	// theirs and never discard, because text somebody wrote cannot
	// (.internal/specs/2026-08-16-notes-design.md §4.2).
	var contentKey []byte
	// The key's own failure text, kept rather than only logged: it is the
	// second line of the notice the History settings raise when durable
	// history is not running (nocx-rtg0.15). A reason without a detail is a
	// dead end for whoever has to act on it.
	var contentKeyErr string
	if key, keyErr := contentkey.LoadOrCreate(ctx, contentkey.Config{
		Registry:    reg,
		KeyID:       vault.ContentKeyID,
		SystemReady: systemReady,
		DBPath:      filepath.Join(paths.DataDir(), "content.db"),
		// The salt lives in the CONFIG directory — never in the data
		// directory beside content.db: a copy of the data directory must
		// carry nothing that opens it (nocx-rtg0.14).
		SaltPath: filepath.Join(paths.ConfigDir(), "contentkey.salt"),
		Logger:   logger,
	}); keyErr != nil {
		slogger.Warn("encrypted local stores unavailable; starting without them", "reason", keyErr)
		contentKeyErr = keyErr.Error()
	} else {
		contentKey = key
	}

	// The notes library. A store that cannot be opened leaves noteSvc nil,
	// the notes.* methods answer -32601, and the panel says the library is
	// unavailable — never an empty list, which would tell somebody their
	// notes are gone (spec §8).
	var noteSvc *note.Service
	// The notes database is closed on the way out, after the transport has
	// stopped: an open file handle outliving the process is how a database
	// gets left in a state its next open has to recover from.
	var noteCloser interface{ Close() error }
	// The backup's view of the same store. nil when notes are unavailable,
	// and the backup then carries no notes section — which restore reads as
	// "this backup says nothing about notes" rather than "you had none".
	var noteBackup backup.NoteStore
	if contentKey != nil {
		if noteStore, noteErr := note.Open(ctx, note.Config{
			Path: filepath.Join(paths.DataDir(), "notes.db"),
			Key:  contentKey,
		}); noteErr != nil {
			slogger.Warn("notes unavailable; starting without them", "reason", noteErr)
		} else {
			noteSvc = note.NewService(noteStore, func() string {
				var raw [16]byte
				if _, rerr := rand.Read(raw[:]); rerr != nil {
					return fmt.Sprintf("note-%d", time.Now().UnixNano())
				}
				return hex.EncodeToString(raw[:])
			}, time.Now)
			noteCloser = noteStore
			noteBackup = &noteBackupAdapter{store: noteStore}
		}
	}

	backupService := backup.NewService(profileStore, settingsRegistry, docStore, snippetStore, noteBackup)
	if recoverErr := backupService.Recover(); recoverErr != nil {
		return nil, fmt.Errorf("backup recovery: %w", recoverErr)
	}

	// The ContentDB key, once at startup (nocx-rtg0.9 as amended by
	// nocx-rtg0.14): the OS keystore's derived slot when one exists, else
	// DERIVED at startup from a per-machine salt — the vault and its seal
	// are irrelevant to it, and no passphrase is ever requested for history.
	// The key is held here for the life of the process, so a vault
	// auto-seal can never make history unreadable. The History settings
	// (keep on/off, age, the two-number budget, output) wire into the store
	// the same way: read here, live-updated below. On every failure path
	// the app starts WITHOUT durable history (the stub) and says so: a
	// terminal that refuses to start because its history database could not
	// open a key is worse than one that starts and admits the gap.
	//
	// "Says so" is a product statement, not a log line, and until
	// nocx-rtg0.15 it was only ever the latter: every path below warned and
	// the Settings screen went on offering a keep-history toggle, a
	// retention age and a two-number budget that governed nothing.
	// historyStatus is what makes the sentence true. It is deliberately the
	// SAME surface a runtime write failure will raise through
	// (nocx-rtg0.10) — raise/clear, not one-shot, and not named after
	// startup — so the product has one way to say "durable history is not
	// running, here is why" and never grows a second (ws_history_status.go
	// carries the argument in full).
	historyStatus := transport.NewHistoryStatus()
	historyPolicy := policyFromSettings(settingsRegistry)
	budget, budgetErr := budgetFromSettings(settingsRegistry)
	if budgetErr != nil {
		slogger.Warn("durable command history unavailable; starting without it", "reason", budgetErr)
		historyStatus.Raise(transport.HistoryDegradeInvalidBudget, budgetErr.Error())
	} else if contentKey == nil {
		// The key already said why, once, above.
		slogger.Warn("durable command history unavailable; starting without it", "reason", "no content key")
		historyStatus.Raise(transport.HistoryDegradeNoKey, contentKeyErr)
	} else if db, openErr := content.Open(ctx, content.Config{
		Path:   filepath.Join(paths.DataDir(), "content.db"),
		Key:    contentKey,
		Budget: budget,
		Policy: historyPolicy,
		Logger: logger,
		// The rebuild's own announcement (nocx-rtg0.19). A schema change
		// discards the file, and the store says so in a slog.Warn nobody
		// reads — while the symptom a person actually sees, an empty
		// history, is indistinguishable from a fresh install. This is the
		// composition root handing that fact to the surface that already
		// exists to say when history is not what the settings promise.
		OnDiscard: func(rows int) { historyStatus.Discarded(rows) },
	}); openErr != nil {
		slogger.Warn("durable command history unavailable; starting without it", "reason", openErr)
		historyStatus.Raise(transport.HistoryDegradeOpenFailed, openErr.Error())
	} else {
		contentDB = db
		// The closing event, named. Nothing has raised on this path today —
		// Clear is a no-op on a status that starts available — but the
		// interval is stated at both ends here rather than left to be
		// inferred, and this is the line a retry (or nocx-rtg0.10's queue
		// draining) closes its episode on.
		historyStatus.Clear()
		clearWindowOnCleanStart(ctx, settingsRegistry, db, slogger)
	}

	// Live History policy: a Settings toggle applies without a restart. The
	// transport's own notifier broadcasts settings.changed to the renderer;
	// this second listener keeps the store's policy in sync.
	settingsRegistry.AddNotifier(func(_ int, keys []string) {
		for _, k := range keys {
			switch k {
			case settings.HistoryEnabled.Key(), settings.HistoryRetentionDays.Key(),
				settings.HistoryOutputEnabled.Key(), settings.HistoryOutputCapKB.Key():
				if v, getErr := settingsRegistry.GetBool(settings.HistoryEnabled); getErr == nil {
					historyPolicy.SetEnabled(v)
				}
				if v, getErr := settingsRegistry.GetNumber(settings.HistoryRetentionDays); getErr == nil {
					historyPolicy.SetRetentionDays(int(v))
				}
				if v, getErr := settingsRegistry.GetBool(settings.HistoryOutputEnabled); getErr == nil {
					historyPolicy.SetOutputEnabled(v)
				}
				if v, getErr := settingsRegistry.GetNumber(settings.HistoryOutputCapKB); getErr == nil {
					historyPolicy.SetOutputCapBytes(int(v) << 10)
				}
				// AFTER the policy, and in this order deliberately: what
				// history.status says about a detached session's output is
				// read live off the policy, so announcing first would carry
				// the value the person just replaced. Two of these switches
				// decide whether a session with no window keeps running at
				// all, and a person flipping one is owed the consequence on
				// the screen in front of them rather than in a log
				// (ws_history_status.go, Restate).
				historyStatus.Restate()
			}
		}
	})
	// Profile usage tracker for the sessions.status RPC (nocx-uxs5.4).
	usageStore := session.NewDocumentUsageStore(docStore)
	sess = sess.WithProfileUsageTracker(usageStore)
	// Port discovery (nocx-wzc4.2): cadence owner for discovery, keyed by
	// profile. *ssh.RealClient satisfies discovery.Connector without an
	// adapter — the same shape that satisfies tunnel.Connector. The local
	// machine (nocx-wzc4.8) samples through the native kernel reader,
	// wired here at the composition root (AD-8) behind the same Provider
	// seam the remote ladder implements. The cadence timers are named
	// here, at the composition root (spec §4): one settle sample 1 s after
	// the connection comes up, prompt hints debounced 1 s, periodic
	// sampling every 10 s while the panel is visible and nothing is
	// paused.
	discoverySched := discovery.NewScheduler(
		sshClient, logger,
		discovery.WithLocalProvider(func(l log.Logger) discovery.Provider {
			return nativeports.NewProvider(l)
		}),
		discovery.WithSettleDelay(1*time.Second),
		discovery.WithPromptDebounce(time.Second),
		discovery.WithSampleInterval(10*time.Second),
	)
	// The remote shell launcher, built here so its bootstrap-outcome sink
	// can be wired once the server exists (below, beside the other
	// integration-axis seams). Without that sink the whole closed outcome
	// set — the far host having no hasher, a digest that did not match, no
	// generation installed — reaches the user as "cannot say why".
	remoteLauncher := &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: logger}

	// Probe result store: operational evidence for connections.test.
	// Process-lifetime only (not persisted across restarts).
	probeResultStore := transport.NewProbeResultStore()
	// Profile service: single validated write path for profiles and groups.
	// Used by the import handlers and version transitions.
	profileSvc := profile.NewProfileService(profileStore)

	// Git factory (spec §5.1): probe capability, resolve the environment,
	// run rev-parse — the local implementation, and the only route that
	// makes internal/git reachable from main() at all. Retained on the App
	// so shutdown can stop the background environment resolution
	// (nocx-6pz0).
	gitFactory := gitlocal.NewFactory()

	// The ONE global agent policy (ADR-0020 §7 as amended 2026-08-16,
	// accepted): the matrix every run's grant is minted from. Persisted as a
	// JSON document beside the settings; the run mint and the
	// policy.get/set RPCs read the same store live, so a
	// Settings save applies without a restart. An unset or unreadable
	// store IS a policy — the zero matrix, which asks — never an error.
	policyStore := assistant.NewGlobalPolicyStore(docStore, "agent-policy.json")

	// The file-manager revealer (nocx-ngf3u): per-OS behind the interface
	// the transport already declares (FilesRevealer). macOS: `open -R`;
	// Linux: xdg-open on the containing directory; other platforms: nil
	// (files.reveal answers -32601, the menu item does not render). This
	// is the same shape contentkey uses for the per-OS identity question.
	filesRevealer, filesRevealerErr := reveal.New()
	if filesRevealerErr != nil {
		slogger.Warn("file-manager reveal unavailable", "reason", filesRevealerErr)
	}

	tpOpts := []transport.WSServerOption{
		transport.WithProfileRepository(profileStore),
		transport.WithBackupService(backupService),
		transport.WithBackupFileSaver(backup.SaveToFile),
		transport.WithGroupRepository(profileStore),
		transport.WithCredentialStore(v),
		// The vault raises its own unlock, and it is SAID here rather than
		// discovered from the store's method set: a resolver built without an
		// unsealer simply never prompts, and that is too quiet a difference to
		// leave to a type assertion (nocx-o3606). The same stanced resolver
		// serves endpoint material and egress screening, so both operations
		// share the vault's unlock semantics.
		transport.WithVaultUnsealer(v),
		transport.WithVaultLifecycle(v),
		// D9, and the whole of its plumbing: the transport counts attached
		// clients, the vault decides what a count of zero means. Without
		// this line quitting the window leaves the root key in a
		// coordinator that outlives it by days — an exposure that did not
		// exist while quitting the window WAS stopping the backend.
		transport.WithClientPresence(v),
		transport.WithAgentKnownMaterial(transport.NewVaultKnownMaterial(v, credResolver, v)),
		transport.WithVaultReset(vaultreset.New(v, profileStore, slogger)),
		transport.WithAgentPolicy(policyStore),
		// Which of that policy's seven rows govern anything at all: the
		// effect classes at least one DECLARED tool carries. Read HERE, off
		// the tool declaration table, for the same reason WithBuildInfo
		// reads internal/version here — it is compile-time state, and the
		// composition root is where state becomes a dependency. The
		// settings surface needs it so five controls that govern nothing do
		// not look like the two that do.
		transport.WithLiveEffects(agenttools.LiveEffects()),
		transport.WithSettingsRegistry(settingsRegistry),
		transport.WithContentDB(contentDB),
		// The durable sink for what a session prints while nothing is
		// attached (nocx-22k1c.1). It is the replay ring's consumer in that
		// interval, so a session whose window is closed keeps running past
		// the ring's 256 KiB instead of throttling on acks that will never
		// come. With the store stubbed — no content key — the stub records
		// nothing and says so, and history.status carries the consequence.
		transport.WithSessionOutputRecorder(contentDB.SessionOutput()),
		transport.WithHistoryStatus(historyStatus),
		transport.WithProber(&proberAdapter{client: sshClient}),
		transport.WithProfileService(profileSvc),
		transport.WithSnippets(snippetSvc),
		transport.WithNotes(noteSvc),
		transport.WithUIState(uiStateStore),
		transport.WithAPI(apiCollections, apiSender, apiSecretRefs),
		transport.WithAPIImportFetcher(apiFetcher),
		// What this binary is, for app.about (nocx-8bbp). Read here rather
		// than inside the transport: internal/version's vars are link-time
		// state, and the composition root is where state becomes a
		// dependency.
		transport.WithBuildInfo(version.Info()),
		transport.WithHostKeyTruster(&proberAdapter{client: sshClient}),
		// The remote shell launcher (nocx-xs1d), adapted across the two
		// identically-named declarations and wired into every ConnectConfig
		// the transport builds. Before this line the launcher was reachable
		// from its own tests and nowhere else (AGENTS.md check 5).
		transport.WithRemoteLauncher(remoteLauncher),
		// The SFTP publisher on the DIRECT-HOST path. A saved profile gets
		// one from the connection resolver; a typed host had none, which
		// did not matter while the remote command installed the bundle
		// itself. The carrier carries no payload, so this is now the only
		// thing that puts a launch carrier on the far host, and without it
		// a direct-host session can never integrate (design §4.1).
		transport.WithRemoteInstaller(shint),
		// The installed fact (nocx-mlm7 P7, design §5.4): the persisted
		// memory of which resolved destinations carry a committed
		// integration. The footprint status surface reads it; the
		// observation RPC that used to write it was severed (ADR-0024 §1).
		transport.WithInstalledFactStore(installedFacts),
		// The helper footprint row (remote-helper design D8): the observed
		// helper installs behind shell.footprint.status. Wired here so the
		// listing and the consent path share the composition root.
		transport.WithHelperConsentStore(helperConsent),
		transport.WithHelperInstallStore(helperInstalls),
		// The uninstall capability (nocx-mlm7 P10, design §9): *ssh.RealClient
		// satisfies transport.RemoteUninstaller without an adapter — the
		// signatures are identical. The capability owns the dial-and-call
		// (acquire the pooled connection, ask the carrier for the remote
		// home, run Publisher.Uninstall over SFTP); the raw SSH client
		// never leaves internal/ssh. Wired beside the installer P8 added:
		// a saved connection that publishes can also remove.
		transport.WithRemoteUninstaller(sshClient),
		// The tunnel connector (nocx-8gix): *ssh.RealClient satisfies
		// tunnel.Connector without an adapter — the signatures are
		// identical — so a forward acquires its OWN pooled connection
		// lease through the same client a tab uses, authorized and
		// pool-keyed exactly like a tab (spec §7.3, AD-4). Before this
		// line the whole forward model was reachable from its own tests
		// and nowhere else (AGENTS.md check 5).
		transport.WithTunnelConnector(sshClient),
		// Port discovery (nocx-wzc4.2): the scheduler owns the cadence
		// (settle sample, prompt debounce, hidden-tab pause, one-in-flight
		// — spec §4) and acquires its OWN pooled discovery lease per
		// profile. *ssh.RealClient satisfies discovery.Connector without
		// an adapter, the same way it satisfies tunnel.Connector. Before
		// this line the whole discovery package was reachable from its own
		// tests and nowhere else (AGENTS.md check 5).
		transport.WithDiscoveryScheduler(discoverySched),
		// The in-band bootstrap builder (nocx-ynsx): *shellintegration.Impl
		// satisfies transport.InBandBootstrapper without an adapter — the
		// signatures are identical. Before this line the in-band plan was
		// reachable from its own tests and nowhere else (AGENTS.md check 5).
		transport.WithInBandBootstrapper(shint),
		// The completion adapter (nocx-w7h.15): the handler routes by
		// session kind. Local completion reads the backend filesystem;
		// remote completion uses the live session's exact SSH options, so
		// a jump route reuses the same pooled connection instead of
		// silently dialing the target directly.
		transport.WithCompleters(
			completion.NewLocal(),
			&routedSSHCompleter{client: sshClient},
		),
		// Command discovery's shared half (carrier design §8, nocx-m8jwn.6).
		// One backend-owned, in-memory cache serves every tab: the PATH
		// enumeration is identical for every session to one target, so it
		// is computed once per cache key and invalidated on the mtime of
		// each PATH directory rather than re-run per session. Without this
		// line the whole package is reachable from its own tests and
		// nowhere else (AGENTS.md check 5).
		transport.WithCommandNames(&commandNamesRouter{
			svc:    commandnames.New(time.Now, logger),
			client: sshClient,
		}),

		transport.WithProbeResultStore(probeResultStore),
		transport.WithSSHConfigResolver(sshCfgResolver, sshConfigPath),

		// The file-tree control plane (fm-w8): the binding registry is
		// constructed here — without this line the whole filesystem
		// package is reachable from its own tests and nowhere else
		// (AGENTS.md check 5) — and the provider factory decides which
		// sessions get which providers. Local sessions get a real local
		// provider rooted at the caller's verified cwd when one is sent;
		// remote sessions get the sftp provider over an ssh.FSConn lease
		// acquired with the session's OWN connect options, so it resolves
		// to the same destination the shell did (spec D3). The sftp
		// provider and its lease were the last dead branch of fm-w8: this
		// factory is the caller that makes the package reachable from
		// main() (AGENTS.md check 5).
		transport.WithFilesystemRegistry(filesystem.New()),
		transport.WithFilesystemProviderFactory(filesystemProviderFactory(sshClient)),
		// Git (spec §5.1). The registry is the only route to a bound
		// repository; the factory is the local one, and it is what makes
		// internal/git reachable from main() at all — until this line
		// existed the whole package was unreachable, which is the state
		// AGENTS.md check 5 exists to catch.
		transport.WithGitRegistry(registry.New()),
		// The factory resolves the shell environment in the background
		// from construction (nocx-6pz0) and is stopped at shutdown.
		transport.WithGitRepoFactory(gitFactory),
		// The helper-backed factory selection (remote-helper design D8):
		// SSH sessions get a repository served over the helper when the
		// machine's consent resolves to relay, and the refusal (or the
		// consentRequired ask) stands otherwise. The helper client, the
		// git factory over it and the consent path are reachable from
		// main() only through this line (AGENTS.md check 5). The second
		// return is the registry that OWNS the live helper channels; the
		// uninstall surface needs it to close them before removing an
		// install directory (D25), so the same registry is wired there.
		transport.WithGitHelperFactory(helperFactory),
		// The D25 channel closer (remote-helper design D25): the registry
		// closes every live helper channel on a machine before
		// shell.footprint.helperUninstall removes its install directory —
		// no helper may be running out of a directory being deleted.
		transport.WithHelperChannelCloser(helperReg),
		// The helper-removal capability (remote-helper design D25):
		// *ssh.RealClient satisfies transport.RemoteHelperUninstaller
		// without an adapter — the signatures are identical. The
		// capability owns the dial-and-remove (acquire the write-capable
		// install lease, discover the remote home, run deploy.Uninstall
		// over SFTP); the raw SSH client never leaves internal/ssh.
		transport.WithRemoteHelperUninstaller(sshClient),
		// The file-manager reveal (nocx-ngf3u): the OS-specific revealer
		// behind the interface that already exists (FilesRevealer, one
		// method). This is the same per-OS problem internal/contentkey
		// already solved — one behaviour, chosen at the composition root.
		// On macOS the reveal is `open -R`; on Linux xdg-open opens the
		// containing directory; on other platforms New returns nil and
		// files.reveal answers -32601 (the menu item does not render).
		// Before this line the revealer was nil in the shipped app and
		// "Show in Finder" raised a danger toast on every use.
		transport.WithFilesRevealer(filesRevealer),
	}
	// The lifecycle publication boundary (ADR-0024 decision 7, bead
	// nocx-u7uh.5): one kernel, one publisher, and the transport as its
	// emitter. The kernel authenticates; only schema-checked facts cross
	// the control plane. The publisher wraps the kernel so every mutation
	// an adapter drives is projected into a published fact, and it is what
	// the shell-spawn path creates lifecycle adapters against (read from
	// the transport via WithLifecyclePublisher). Before this line the whole
	// lifecycle stack was reachable from its own tests and nowhere else
	// (AGENTS.md check 5); the adapter creation itself lands with the
	// shell-spawn wiring in internal/transport/ws_shell.go.
	lifecycleKernel := lifecycle.New(lifecycle.Options{})
	// The backend's VT grid for ENROLLED panes (nocx-szb40.2, the AD-6
	// amendment "A live grid for an enrolled pane"). Constructed empty and
	// process-lifetime: nothing is observed until an agent_enrol names a pane,
	// which is what keeps enrolment an act rather than an inference. It is
	// built HERE, ahead of the publisher, because both ends need it — the
	// publisher opens and closes intervals through it, and the transport feeds
	// it from the session read path.
	paneGrid := panegrid.New(logger)
	// One driver per agent (AD-8), validated once, here. NewRegistry fails
	// only on a wiring mistake — a driver that cannot name its agent, or two
	// for one agent — and a wiring mistake belongs to process start rather
	// than to the first frame off a pane.
	// Its own error name rather than the function's `err`: reusing that one
	// here extends its live range past a dozen `if err := …` blocks above,
	// and govet's shadow check then reports every one of them.
	paneDrivers, driversErr := agentdriver.NewRegistry(agentdriver.Claude())
	if driversErr != nil {
		return nil, fmt.Errorf("pane drivers: %w", driversErr)
	}
	// What turns a grid into something a person or a wave can act on
	// (nocx-szb40.3): it classifies a watched pane and reports only the
	// CHANGES. Built here because both ends need it — the enroller opens an
	// observation beside the grid's interval, and the transport touches it
	// from the session read path and is where its reports go.
	paneWatch := paneobserve.New(logger, paneGrid, paneDrivers)
	// The establishment bound is stated here for the same reason the hello
	// timeouts below are: how long a minted accept may wait for the
	// renderer's acknowledgement before the domain is rolled back and the
	// session falls back to a conventional terminal (ADR-0024 decision 9) is
	// a product decision, and the composition root is where product
	// decisions belong. It is the shell's own handshake budget, so the
	// backend never outwaits the shell it is gating.
	var lifecyclePub *lifecyclepub.Publisher
	lifecyclePub = lifecyclepub.New(lifecycleKernel,
		lifecyclepub.WithEstablishmentTimeout(lifecycle.HelloTimeout),
		// The child-domain bootstrap builder (nocx-u7uh.11): the single
		// owner of "how do we reach a host" (ADR-0022) behind the
		// domain_grant outbound. The kernel stays the sole minter; this
		// closure mints through the publisher and composes the opaque
		// launch text the parent executes.
		lifecyclepub.WithGrantBuilder(newChildGrantBuilder(logger,
			func() *lifecyclepub.Publisher { return lifecyclePub }, childTransports, childSessions, typedSSH)),
		// The enrolment act (nocx-szb40.5): the agent wrapper in the shell
		// bundle asks over this same authenticated channel, and this is what
		// answers. Wired here rather than defaulted anywhere, because an
		// unwired enroller refuses every enrolment — the fail-closed half of
		// D4, and the opposite of the grant builder above it.
		lifecyclepub.WithAgentEnroller(newPaneEnroller(logger, childSessions, paneGrid, paneWatch)))
	// The pty factory drives the channel against the PUBLISHER, not the raw
	// kernel: every mutation an adapter causes must reach the renderer as a
	// published fact, and the publisher is the only thing that projects them.
	ptf.kernel = lifecyclePub
	// The remote lifecycle transport (ADR-0024 decision 2 "Over SSH",
	// bead nocx-u7uh.4): the composition root implements the ssh layer's
	// RemoteLifecycle seam with the lifecycle kernel and the ssh client —
	// the channel rides the SAME pooled connection the session uses
	// (AD-4), and refusal (the remote sshd will not forward) leaves the
	// session conventional. Before this line the remote adapter was
	// reachable from its own tests and nowhere else (AGENTS.md check 5).
	remoteLifecycle := &remoteLifecycleProvider{client: sshClient, kernel: lifecyclePub, logger: logger, transports: childTransports}
	tpOpts = append(tpOpts, transport.WithRemoteLifecycle(remoteLifecycle))
	tpOpts = append(tpOpts, transport.WithLifecyclePublisher(lifecyclePub))

	// The ordinary control lane's permit count, named here the way the D14
	// bounds are named here: the number of control tasks that may run
	// concurrently before new work is refused with the saturation error.
	tpOpts = append(tpOpts, transport.WithControlLaneCapacity(transport.DefaultControlLaneCapacity))
	// The domain-conflict wait bound, named here like the lane capacity: a
	// conflict WAITS (bounded) rather than refusing instantly, so a
	// sequential client's back-to-back requests are never told the control
	// plane is busy; exhausting the wait is the only refusal.
	tpOpts = append(tpOpts, transport.WithDomainConflictWaitTimeout(transport.DefaultDomainConflictWaitTimeout))
	// The run lease (ADR-0020 decision 2) every agent run is supervised
	// under, named here the way the lane capacity is: the wall-clock
	// deadline, the inactivity deadline, the output budget and the
	// escalation grace. The transport's default IS the production value —
	// this line names it, so the seam stays reachable from production and
	// a future settings surface flips one option here, not a default.
	tpOpts = append(tpOpts, transport.WithRunLease(transport.DefaultRunLeaseConfig()))
	// The notification router (ADR-0047): the only holder of "where" a raised
	// notification goes. Before this line the whole notify package was
	// reachable from its own tests and nowhere else (AGENTS.md check 5).
	//
	// The banner row is the table's first, and it is decided here, once. A
	// program that asks for a notification (notify.raise, trust
	// programRequest) reaches the OS banner and nothing else — no network
	// sink, no subscription route, and no way for the request to name a
	// destination of its own. The holder behind it binds late (see
	// notify.HostHolder): the Wails runtime needs a context that exists only
	// in main.go's startup, so the implementation arrives after this line
	// while the route does not.
	//
	// Hosts that never bind one — cmd/devharness, the dev-web harness, an e2e
	// run — keep UnavailableHost, and a raise there is a visible failed
	// delivery rather than a silent drop.
	attentionHost := &notify.HostHolder{}
	// The toast is the second attention surface and it is a SINK, not a
	// special case in the renderer (plan D2): the router resolves it here,
	// once, and the sink hands the event to a port the transport satisfies.
	// A toast that the renderer chose for itself would put "where" somewhere
	// other than the router, which is the one thing ADR-0047 §2.3 forbids.
	//
	// Its holder binds late for the same ordering reason the host's does, and
	// the late half is nearer than it looks: the implementation is the
	// WebSocket server, which is constructed BELOW this line because it is
	// built with the pipeline already wired into it. The route is decided
	// here; the surface arrives a few lines later.
	notifyToast := &notify.ToastHolder{}
	// The table is no longer written here. It is BUILT, from the catalogue and
	// the toggles the person ticked, and swapped into the live router whenever
	// one of them moves (D1, D4). What this composition root still decides —
	// and the only thing it may decide — is which SINK sits behind each
	// catalogue channel: the route is the router's, the surface is ours.
	//
	// Each row still names its destination, which is what makes a failed
	// delivery able to say WHICH channel failed. The word comes from the
	// catalogue's channel id now, so the toggle key, the routed target and the
	// failure row all spell the surface once (AD-8).
	//
	// With nothing ticked the built table is exactly the four rows this file
	// used to carry by hand — program.notify and session.ended, each to the
	// banner and the toast — so nobody's notifications change the day the
	// matrix lands (D2, notify.DefaultCatalogue).
	notifyRoutes, routesErr := notify.NewRoutingSource(notify.RoutingConfig{
		Catalogue: notify.DefaultCatalogue(),
		Sinks: map[string]notify.Sink{
			notifyBannerTarget: notify.HostSink{Host: attentionHost},
			notifyToastTarget:  notify.ToastSink{Presenter: notifyToast},
		},
		Enabled: func(kindID, channelID string) bool {
			toggle, declared := notifyRouteToggles[notify.RouteSettingKey(kindID, channelID)]
			if !declared {
				// A cell with no declaration is a cell nobody can turn on.
				// Default-deny is the answer to every question this lookup
				// cannot answer, including this one.
				return false
			}
			on, readErr := settingsRegistry.GetBool(toggle)
			if readErr != nil {
				logger.Warn("notification routing cell unreadable; treating it as off",
					"key", toggle.Key(), "error", readErr)
				return false
			}
			return on
		},
		Limits: notify.Limits{
			MaxInFlight:     4,
			MaxQueued:       32,
			MaxRetained:     1 << 20,
			DeliveryTimeout: 10 * time.Second,
		},
		// A refused table has to be visible in the PRODUCT and not only in a
		// log (AGENTS.md): the previous routing stays live, so from the
		// Settings screen the change looks accepted while nothing about
		// delivery moved — the silent degrade the UI contradicts.
		//
		// It goes to the toast DIRECTLY, not through the router, for the same
		// reason a failed delivery's feed row is admitted directly (D3,
		// notify.Feed.RecordDeliveryFailure): a complaint about the routing
		// table cannot travel through the routing table that was just refused.
		// The toast is the right surface rather than the feed because this is
		// a fact about an action the person took a moment ago in this window,
		// and because the feed's kind is a closed enum on the wire
		// (contracts/notify.feed.read.schema.json) that has no honest value
		// for it — smuggling one past the schema is what rule 5 forbids.
		OnRefused: func(err error) {
			logger.Warn("notification routing table refused; the previous routing stays live", "error", err)
			toastCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = notifyToast.Toast(toastCtx, notify.Event{
				Title: "Notification routing unchanged",
				Body:  "That change was refused, so the previous routing is still in effect: " + err.Error(),
				Level: notify.LevelWarning,
			})
		},
	})
	if routesErr != nil {
		return nil, fmt.Errorf("notify routing: %w", routesErr)
	}
	notifyRouter := notifyRoutes.Router()
	// Live routing: a Settings toggle applies without a restart, through the
	// registry's own notifier — the same seam the History policy uses, and for
	// the same reason (there is one change mechanism and this subscribes to
	// it rather than growing a second).
	//
	// One rebuild per notification, not per key: a batch that moved three
	// cells produces one table, and the table is a whole-value swap anyway.
	settingsRegistry.AddNotifier(func(_ int, keys []string) {
		for _, k := range keys {
			if _, ours := notifyRouteToggles[k]; ours {
				_ = notifyRoutes.Rebuild()
				return
			}
		}
	})

	// The feed: the centre's memory, built BEFORE the policy because the
	// policy's result handler records into it (see below).
	notifyFeed, feedErr := notify.NewFeed(notify.FeedLimits{
		MaxOccurrences:   notifyFeedMaxOccurrences,
		MaxRetainedBytes: notifyFeedMaxRetainedBytes,
		CollapseWindow:   notifyFeedCollapseWindow,
		MaxRunRetained:   notifyFeedMaxRunRetained,
	}, notify.RealClock{})
	if feedErr != nil {
		return nil, fmt.Errorf("notify feed: %w", feedErr)
	}

	// The attention policy sits IN FRONT of the router, so notify.raise
	// reaches the pipeline through it rather than around it. Without this a
	// loop that writes OSC 9 a hundred times produces a hundred banners; the
	// policy collapses a burst per (session, kind) into one notification
	// naming the count, which is the whole point of the debounce window.
	//
	// Focus binds late and its unbound answer is "nothing is focused", so
	// suppression never suppresses until something reports what the user is
	// looking at (nocx-jiwq.2). That is the safe direction: the cost is a
	// notification the user did not strictly need, where the other direction
	// silently swallows one they did. Debounce and coalescing need no focus
	// and work in full from the first raise.
	notifyFocus := &notify.FocusHolder{}
	// The window is the user's, and it is PULLED rather than pushed: the
	// policy asks this closure for the length of every window it opens, so
	// the registry stays the one owner of the number and no notifier has to
	// keep a copy of it in step (AD-8). That is why there is no
	// AddNotifier for this setting beside the routing one above — the
	// routing table is a built artefact that must be rebuilt when its
	// inputs move; a window length is just a number, read when it is needed.
	//
	// Which window a change governs is stated at the seam that decides it
	// (notify.WithWindowSource): a window's length is fixed from the moment
	// it opens until the moment it closes, so a change governs every window
	// opened after it and no window already open.
	//
	// An unreadable setting answers 0, which the policy reads as "fall back
	// to the window I was constructed with" rather than "no debounce" — a
	// zero window would deliver one notification per event, which is the
	// flood the policy exists to prevent. So the degrade is to the default,
	// and it says so in the log rather than only in behaviour.
	notifyWindow := notify.WindowSource(func() time.Duration {
		seconds, readErr := settingsRegistry.GetNumber(notifyDebounceWindowSetting)
		if readErr != nil {
			logger.Warn("debounce window unreadable; falling back to the default",
				"key", notifyDebounceWindowSetting.Key(),
				"default", notifyDebounceWindow, "error", readErr)
			return 0
		}
		return time.Duration(seconds * float64(time.Second))
	})
	notifyPolicy, policyErr := notify.NewPolicy(
		context.Background(), notifyRouter, notifyDebounceWindow, notifyFocus, notify.RealClock{},
		notify.WithWindowSource(notifyWindow),
		// The failure surface (nocx-r6pxp). notify.raise answers {} at
		// ACCEPTANCE: under the policy the delivery can happen a debounce
		// window later, with no caller left to fail to, so a failure past
		// that point reached this logger.Warn and nothing else — the soft
		// degrade visible only in a log that AGENTS.md condemns.
		//
		// It becomes a row in the feed, which is the thing whose whole
		// purpose is remembering what happened while you were not looking.
		// The row is admitted DIRECTLY through the feed and never raised
		// back through the router that just failed: a complaint carried by
		// the broken sink would fail the same way and produce another, and
		// one broken sink would become an unbounded feed of complaints
		// about being broken (RecordDeliveryFailure says the same at the
		// seam that enforces it). The log stays: a headless host with no
		// panel still has to be diagnosable.
		notify.WithResultHandler(func(out notify.Outcome) {
			// ErrUnavailable is the ONE failure that gets no row, and the
			// line is between a channel that lost the message and a channel
			// that does not exist on this host. UnavailableHost reports it
			// for every raise on a build with no desktop attention surface
			// (cmd/devharness, the dev-web harness, an e2e run), so a row
			// per notification would say "this build has no banner" once per
			// notification, forever, beside a notification that IS in the
			// feed — nothing was lost, and nothing the user does can change
			// it. That is noise, and it is the same argument the debounce
			// makes when it refuses to put "1 notification" behind every
			// notification.
			//
			// Every other host failure earns its row, and the ones that
			// matter most are exactly the ones this keeps: the wails host
			// returns ErrNotRequested and ErrDenied for a permission the
			// user CAN act on (notify/wailsadapter/host.go), and a denied
			// banner that says so in the centre is the whole point. The
			// router still records unavailability in its outcome and the
			// log still names it, so it stays visible where it is a fact
			// about the host rather than about a notification.
			record := func(channel string, err error) {
				if errors.Is(err, notify.ErrUnavailable) {
					return
				}
				notifyFeed.RecordDeliveryFailure(out.Event, channel, err)
			}
			if out.Err != nil {
				logger.Warn("notification refused", "error", out.Err)
				record(notify.ChannelPipeline, out.Err)
				return
			}
			for _, r := range out.Results {
				if r.Err != nil {
					logger.Warn("notification delivery failed", "target", r.Route.Destination.Target, "error", r.Err)
					record(r.Route.Destination.Target, r.Err)
				}
			}
		}),
	)
	if policyErr != nil {
		return nil, fmt.Errorf("notify policy: %w", policyErr)
	}
	// Ingress is the pipeline's one entry point: it stamps what nocx owns,
	// records the occurrence in the feed, and only THEN submits for
	// delivery. The policy is reached THROUGH it and never around it, which
	// is the whole inversion — before this, a suppressed notification was
	// destroyed, so the events most worth seeing were exactly the ones
	// nothing remembered.
	notifyIngress, ingressErr := notify.NewIngress(notifyFeed, notifyPolicy, notify.RealClock{})
	if ingressErr != nil {
		return nil, fmt.Errorf("notify ingress: %w", ingressErr)
	}
	tpOpts = append(tpOpts,
		transport.WithNotifyRaiser(notifyIngress),
		transport.WithNotifyFeed(notifyFeed),
	)

	// The assistant engine (nocx-edio): eino behind the guarded HTTP
	// client, wired at the composition root like every other client —
	// before this line the whole package was reachable from its own tests
	// and nowhere else (AGENTS.md check 5). The probe store is
	// process-lifetime: agent.status's "last probe result" fact, whose
	// meaning expires with the endpoint that produced it.
	assistantProbes := assistant.NewProbeStore()
	// The floor is fixed here from the same roots that own nocx's policy,
	// vault, ledger, and shell-integration documents; no policy layer can
	// widen it.
	floor := content.NewFloor(paths.ConfigDir(), paths.DataDir())
	assistantClient, agentToolRegistry, err := assistant.NewClientAndRegistry(logger, &ledgerWireRecorder{ledger: contentDB.Ledger()}, floor, []string{skillRoots[0].Dir, skillRoots[1].Dir})
	if err != nil {
		return nil, err
	}
	tpOpts = append(tpOpts,
		transport.WithAssistantClient(assistantClient),
		transport.WithAssistantProbeStore(assistantProbes),
		transport.WithSkillSource(skills),
		transport.WithAgentToolRegistry(agentToolRegistry),
	)
	// The same store the publisher enrols into, on its other end: the
	// transport is what feeds it, from the session's own read path.
	tpOpts = append(tpOpts, transport.WithPaneGrid(paneGrid), transport.WithPaneObserver(paneWatch))
	tp := transport.NewWSServer(logger, sess, tpOpts...)
	// The feed's change hint, bound now that the server exists: every
	// mutation tells the attached renderers the revision moved. It carries
	// the revision only, so it rides the refreshable outbound queue and a
	// dropped one costs one refetch rather than a row nobody learns about.
	notifyFeed.OnChange(tp.BroadcastFeedChanged)
	// The toast route's implementation, bound now that the server exists. The
	// route itself was decided above and is not reachable from here — this
	// binds an implementation, never a destination. The window before this
	// line is empty: nothing can raise before the transport is listening.
	notifyToast.Set(tp)
	// The transport is the publisher's emitter: facts route to the lane's
	// session's current subscriber. Bound post-construction because the
	// server is built above; the window before this line is empty (no
	// session can have spawned a shell yet).
	lifecyclePub.SetEmitter(tp)

	// Where a pane's classification goes, bound post-construction for the
	// same reason as the emitter above. Without this line the backend would
	// go on classifying every enrolled pane and telling nobody — which is
	// precisely the silent degrade AGENTS.md names, so the watcher refuses
	// to sweep at all until it has a destination.
	paneWatch.SetEmitter(tp.EmitPaneObservation)

	// The liveness projection's watcher (nocx-iarf9), bound here for the same
	// reason as the emitter above. The registry decides WHEN a session's
	// belief changed — the keepalive prober's findings arrive there — and the
	// transport decides who is told; without this line the ssh prober's
	// knowledge that a host stopped answering would stop at the record, and
	// the tab would go on looking alive until the connection finally died.
	sess.SetLivenessObserver(tp.PublishLiveness)

	// The vault's prompt carrier, bound post-construction for the same
	// reason as the emitter above: the server is built here. The vault
	// owns "I am sealed and one unlock is already pending" (nocx-o9jdu) and
	// the transport owns "deliver one prompt to whichever renderer is
	// there" — this line is the only place the two meet.
	// Every production credential resolver now reaches EnsureUnsealed through
	// the vault's structural capability (nocx-k41yv): asks, probes, command
	// secret expansion and SSH authentication all share this coalescing state.
	v.SetUnlockRequester(tp)

	// One resolver, one consumer family: connections.test probes and
	// ordinary connects resolve identically. Created after tp so the
	// connection-password ask (the second direction, nocx-v64o) can be
	// wired into every ConnectConfig the resolver builds.
	//
	// The SFTP carrier (nocx-mlm7 P8) is wired here and nowhere else: the
	// same shellintegration.Impl the in-band bootstrap uses satisfies
	// ssh.RemoteInstaller without an adapter — the signatures are
	// identical — and WithRemoteInstaller stamps it on every ConnectConfig
	// the resolver builds for a SAVED profile. A saved connection in
	// script mode therefore publishes the integration bundle over SFTP
	// through P1's publisher before the session starts (design §4), while
	// direct-host opens (no profile, no resolver) never publish. Before
	// this line the carrier was reachable from its own tests and nowhere
	// else (AGENTS.md check 5).

	// The lane-registration callback binds a minted lifecycle lane to the
	// session that owns it, so published facts route to the right
	// subscriber. One owner for the decision (AD-8): the local pty factory
	// and the remote provider share this closure, and it can only exist
	// once the server (which holds the lane→session registry) is built.
	registerLane := func(lane lifecycle.LaneID, sid string) {
		if sid != "" {
			tp.RegisterLifecycleLane(lane, session.ID(sid))
			childSessions.register(lane, sid)
		}
	}
	remoteLifecycle.registerLane = registerLane
	ptf.registerLane = registerLane
	// The bootstrap's terminal outcome (carrier design §5.5, §6.1), routed
	// by the same lane the lifecycle facts use. It is a THIRD seam onto the
	// integration axis and it has to be: the bootstrap concludes before any
	// domain exists, so neither a published fact nor a transport loss cause
	// can carry it, and until this line every one of its outcomes was a log
	// entry the user cannot read.
	// §6.2's loss events on the remote path, into the same sink the local
	// adapter's already use. The cause crosses as its string so the transport
	// does not depend on the adapter package.
	remoteLifecycle.reportLoss = func(lane lifecycle.LaneID, cause lifecycleremote.LossCause) {
		tp.NoteIntegrationLoss(lane, string(cause))
	}
	remoteLauncher.reportBootstrapOutcome = func(lane string, reason ssh.RefusalReason) {
		if lane == "" {
			return
		}
		tp.NoteBootstrapOutcome(lifecycle.LaneID(lane), reason)
	}
	// And the same axis for the TYPED path, through the same two seams: the
	// launch side says a second shell is starting in this session, and the
	// bootstrap says how the far side answered. Before this line the typed
	// path reached its terminal outcome and logged it, so the thirty-one
	// refusal names the epic opened the vocabulary to were a structured
	// reason on one path and nothing at all on the other.
	bindTypedIntegrationAxis(typedSSH, tp)
	// The session integration axis (nocx-dvql). Two seams, one owner: the
	// pty factory says what it started and how far it got, and the adapter
	// says which path ended the channel. The transport joins them with the
	// kernel's own "a domain went live" and publishes
	// session.integrationChanged. The cause crosses as its string so the
	// transport does not depend on the adapter package — the adapter's
	// constants remain the single spelling.
	ptf.reportIntegration = func(sid, shell, status string, reason ssh.RefusalReason) {
		tp.RegisterIntegration(session.ID(sid), shell, status, reason)
	}
	ptf.noteLifecycleLoss = func(lane lifecycle.LaneID, cause lifecyclechannel.LossCause) {
		tp.NoteIntegrationLoss(lane, string(cause))
	}
	// The third seam onto the same axis (nocx-cgzc): the observer says the
	// shell was replaced before it ever answered, and the transport decides
	// whether that still applies. The factory does not decide it, because
	// only the transport knows whether the session has integrated since.
	ptf.reportShellReplaced = func(sid, observed string) {
		tp.NoteShellReplaced(session.ID(sid), observed)
	}
	// And how far the shell got through nocx's rcfile (nocx-yww2), which is
	// what turns the dominant failure from "ten seconds of silence" into a
	// stage. Routed by session id rather than by lane: the progress
	// descriptor belongs to the session, carries no domain and confers no
	// authority, so it never touches the lane registry.
	ptf.noteBootstrapStage = func(sid, stage string) {
		tp.NoteBootstrapStage(session.ID(sid), stage)
	}
	// The connection/SSH material seam uses the same resolver as the egress
	// gate. The auth ladder resolves on the dial — PHASE TWO of the open,
	// which deliberately holds no domain gate — so waiting there cannot block
	// the unseal that answers it.
	resolver := connection.NewResolver(
		profileStore, profileStore, credResolver,
		connection.WithConfigResolver(sshCfgResolver),
		connection.WithPasswordAsker(tp.RequestConnectionPassword),
		connection.WithSecretCreator(v),
		connection.WithRemoteInstaller(shint),
	)
	tp.SetProfileResolver(resolver)
	// The same resolver the transport uses, handed to the API route table.
	// It is set here rather than at construction for the reason the
	// transport's own holder gives: the resolver needs the transport (the
	// connection-password ask) and the transport needs the sender, so one of
	// the three has to be wired after the other two exist. There is one
	// resolver, not two — the API route asks the same question a tab asks
	// and gets the same answer, credentials and jump route included.
	apiRoutes.setResolver(resolver)
	app := &App{
		Logger:           logger,
		Pty:              ptf,
		Session:          sess,
		Transport:        tp,
		UploadSources:    tp.UploadSources(),
		ShellIntegration: shint,
		Profiles:         profileStore,
		Credentials:      v,
		vaultCloser:      v,
		noteCloser:       noteCloser,
		discoverySched:   discoverySched,
		gitFactory:       gitFactory,
		logFilePath:      logFilePath,
		logFile:          logFile,
		procs:            procs,
		attentionHost:    attentionHost,
		notifyToast:      notifyToast,
		notifyFeed:       notifyFeed,
		notifyIngress:    notifyIngress,
		notifyWindow:     notifyWindow,
		UIState:          uiStateStore,
		slogger:          slogger,
	}

	// ── the client host (nocx-uo1k6, design D3) ────────────────────────
	//
	// The native-host capabilities — a file picker, a browser open, a
	// desktop banner, a window raise — used to be injected here from
	// main.go, because main.go WAS this process and held the Wails
	// runtime. It is not this process any more: the coordinator has no
	// window, only zero or more attached clients. So the same three seams
	// are wired with implementations that ASK a client (internal/app/
	// clienthost) and answer honestly when none is attached.
	//
	// Wired before Start, exactly as the setters have always required, so
	// no renderer request and no raise can observe the unset state. The
	// setters kept their names and their contract; only the caller moved,
	// from a shell that could inject to the composition root that can.
	app.SetDialogService(clienthost.NewDialogs(tp))
	app.SetUrlOpener(clienthost.NewURLs(tp))
	attention := clienthost.NewAttention(tp, app.Slog(), app.FocusSession)
	app.SetAttentionHost(attention)
	// The other half of a banner. The click lands in the client, which is
	// where the OS delivers it; what it MEANS — raise a window, focus the
	// pane holding that session — is decided here, on the far side of the
	// wire from the shell that reported it (AD-3).
	tp.SetAttentionActivation(attention)

	logger.Info("application initialized")
	return app, nil
}

// ── the file-tree provider factory (fm-w8) ────────────────────────────────

// fsLeaseProvider acquires the SFTP lease a remote provider is built on.
// *ssh.RealClient satisfies it; the interface exists so the factory is
// testable against a double without a live connection — the same reason
// internal/filesystem/sftp declares its own narrow fsConn seam.
type fsLeaseProvider interface {
	FSConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.FSConn, error)
}

// filesystemProviderFactory is the composition root's answer to
// transport.FilesystemProviderFactory: local sessions get a real local
// provider rooted at the caller's verified cwd when one is sent; remote
// sessions get the sftp provider over an ssh.FSConn lease acquired with the
// session's OWN connect options, so the lease resolves to the same
// destination the shell did (spec D3, AD-4). The context is deliberately
// background: the factory has no caller context, and the lease's own
// hard-timeout lane is the bound that keeps a silent server from hanging
// files.open.
// The three D14 bounds, named once, here, rather than as defaults buried in
// two provider packages that would then be free to disagree. Spec §9 calls
// them starting numbers to be tuned once the panel is in daily use, and a
// number nobody can find is a number nobody tunes — so the composition root
// is where they live and where a reviewer looks for them.
//
// The remote values are deliberately not the local ones. An enumeration is a
// chain of round trips over SFTP, so its time bound is larger; and the entry
// bound is smaller because the whole directory must be enumerated before any
// page can be returned, which makes the cost of a huge remote directory a
// network cost rather than a syscall one.
const (
	fsLocalEntryCap    = 10_000
	fsLocalSizeCap     = 8 << 20 // 8 MiB
	fsLocalListTimeout = 10 * time.Second

	fsRemoteEntryCap    = 5_000
	fsRemoteSizeCap     = 8 << 20 // 8 MiB
	fsRemoteListTimeout = 30 * time.Second
)

func filesystemProviderFactory(client fsLeaseProvider) transport.FilesystemProviderFactory {
	return func(sess session.Session, rootPath string) (filesystem.Provider, error) {
		if sess.Kind() != session.KindRemote {
			localOpts := []local.Option{
				local.WithEntryCap(fsLocalEntryCap),
				local.WithSizeCap(fsLocalSizeCap),
				local.WithListTimeout(fsLocalListTimeout),
			}
			if rootPath != "" {
				localOpts = append(localOpts, local.WithRoot(rootPath))
			}
			// Declared as writableProvider for the same reason the remote
			// half is: the day local.Provider loses Sink must be a compile
			// error here rather than a local tab quietly refusing every
			// upload while every other files.* call still works.
			var p writableProvider = local.New(localOpts...)
			return p, nil
		}
		fs, err := client.FSConn(context.Background(), sess.Host(), sess.SSHOptions()...)
		if err != nil {
			return nil, fmt.Errorf("sftp provider for %s: %w", sess.Host(), err)
		}
		opts := []sftp.Option{
			sftp.WithEntryCap(fsRemoteEntryCap),
			sftp.WithSizeCap(fsRemoteSizeCap),
			sftp.WithListTimeout(fsRemoteListTimeout),
		}
		if rootPath != "" {
			opts = append(opts, sftp.WithRoot(rootPath))
		}
		return &endpointAttestedProvider{
			writableProvider: sftp.New(fsTransferLease{FSConn: fs}, opts...),
			endpointID:       endpointIDFor(sess),
		}, nil
	}
}

// fsTransferLease presents an SFTP lease as the two surfaces
// internal/transfer declares — RemoteFS for the write direction and
// RemoteReadFS for the read one. It is the one place the two vocabularies meet,
// and it exists because neither package may know the other: internal/ssh
// must not import internal/transfer, and internal/transfer deliberately
// declares its own RemoteFS rather than importing internal/ssh
// (transfer.go's package doc). The composition root is where a translation
// between two module boundaries belongs.
//
// It translates two things and forwards everything else untouched.
//
// The SHAPE: Create returns ssh.FSFile where RemoteFS asks for
// transfer.RemoteFile — one shape under two names, and Go matches result
// types by identity, so the conversion has to be a method rather than an
// assertion.
//
// The VOCABULARY: "this server has no posix-rename@openssh.com" arrives as
// ssh.ErrPosixRenameUnsupported and the sink's fallback keys on
// transfer.ErrPosixRenameUnsupported. Untranslated, a server without the
// extension would read as an ordinary promote failure, the temp would be
// removed and the upload would fail — on every such server, with both
// packages' tests green, because each fakes its own sentinel. The
// translation ADDS the transfer vocabulary and keeps the lease's, so a log
// still says which lease said it.
//
// The contract that needs no translation is worth naming too, because it is
// the other one RemoteFS documents and the compiler cannot check: a missing
// path must satisfy errors.Is(err, fs.ErrNotExist), which the sink's
// "nothing to back up" branch keys on. pkg/sftp normalises
// SSH_FX_NO_SUCH_FILE to os.ErrNotExist (client.go:2237) and fsConn.classify
// passes an unclassified error through unchanged, so it already holds; this
// adapter's job there is not to break it. Both directions are asserted in
// fs_upload_lease_test.go.
type fsTransferLease struct {
	ssh.FSConn
}

func (l fsTransferLease) Create(path string) (transfer.RemoteFile, error) {
	f, err := l.FSConn.Create(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (l fsTransferLease) PosixRename(old, new string) error {
	err := l.FSConn.PosixRename(old, new)
	if err != nil && errors.Is(err, ssh.ErrPosixRenameUnsupported) {
		return fmt.Errorf("%w: %w", transfer.ErrPosixRenameUnsupported, err)
	}
	return err
}

// Open translates the READ direction, and it has the same two jobs the
// write direction has, for the same two reasons.
//
// The SHAPE: Open returns ssh.FSReadFile where RemoteReadFS asks for
// transfer.RemoteReader — one shape under two names, and Go matches result
// types by identity.
//
// The VOCABULARY: "that is a folder, not a file" arrives as
// ssh.ErrNotRegularFile and the transport's refusal keys on
// transfer.ErrNotRegular. Untranslated, a person who asked to download a
// directory would be told the server had gone wrong (-32603) instead of
// being told what they actually did, with both packages' tests green
// because each fakes its own sentinel. The translation ADDS the transfer
// vocabulary and keeps the lease's, so a log still says which lease said
// it.
//
// The contracts that need no translation are worth naming for the same
// reason the write half names its one: fs.ErrNotExist and fs.ErrPermission
// already hold, because pkg/sftp normalises SSH_FX_NO_SUCH_FILE and
// SSH_FX_PERMISSION_DENIED (client.go:2237) and fsConn.classify passes an
// unclassified error through unchanged. This adapter's job there is not to
// break it, and fs_upload_lease_test.go asserts both directions.
func (l fsTransferLease) Open(path string) (transfer.RemoteReader, int64, error) {
	f, size, err := l.FSConn.Open(path)
	if err != nil {
		if errors.Is(err, ssh.ErrNotRegularFile) {
			return nil, 0, fmt.Errorf("%w: %w", transfer.ErrNotRegular, err)
		}
		return nil, 0, err
	}
	return f, size, nil
}

// writableProvider is what the factory builds for EITHER kind of session: a
// filesystem this backend can read and write. The two halves are named
// together because they are not separable in the product — a tab whose files
// this backend can list is a tab a file can be uploaded to (upload design
// R1) — and because naming them together is what makes the day either
// provider loses Sink a compile error here rather than a tab quietly
// refusing every upload.
//
// One interface, not one per side. D7 first gave the local provider no write
// half at all, reasoning from the desktop build where a drop on a local tab
// yields an absolute path to insert. A browser drop yields bytes and no path,
// and the machine those bytes must land on is the backend's own — the machine
// that tab's shell is on, which is what R1 asks. So both sides are writable
// and one name says so; a second identical interface would be two owners of
// one idea.
type writableProvider interface {
	filesystem.Provider
	filesystem.Uploader
	filesystem.Downloader
}

// endpointAttestedProvider wraps a remote provider with the endpoint
// attestation (spec §5.1, D4/D6). The transport reads it through the
// optional filesystemEndpointAttester seam; a local provider never carries
// it, which is what makes files.reveal a local-only capability.
//
// It embeds writableProvider rather than filesystem.Provider, and the
// difference is load-bearing: embedding an interface promotes exactly that
// interface's methods, so a wrapper embedding filesystem.Provider has no
// Sink at all however writable the value inside it is. files.open asserts
// filesystem.Uploader on what the factory returned — this wrapper — so the
// narrower embedding would drop the write capability there, with every
// other files.* call still working and the only symptom being uploads
// refusing on a remote tab.
type endpointAttestedProvider struct {
	writableProvider
	endpointID string
}

func (p *endpointAttestedProvider) EndpointID() string { return p.endpointID }

// ── endpointId v1 (spec §5.1) ─────────────────────────────────────────────

// endpointIDFor assembles the v1 endpoint attestation for a session:
// "v1:" + base64url(SHA-256(canonical encoding of the attestation)), the
// attestation being the ordered hop record — bastions first, target last —
// each hop with its address, port and effective principal. The version
// prefix is load-bearing: a v2 (verified host identity) must fail to match
// rather than accidentally match.
//
// The record is built from the session's OWN frozen state — Host() and the
// connect options captured at open — never from the profile store or
// ~/.ssh/config at call time: a profile edited, or a config file changed,
// between the drop and the reconnect must not move the id, or Reload (D6)
// would refuse a viewer for the same endpoint. That frozen-ness is also the
// honest limit of v1, and it is written down rather than hidden: the values
// internal/ssh actually dialed — the effective user when the config names
// none, the effective port when it leaves the port unset, and the final
// per-hop resolution through ~/.ssh/config — are computed inside
// resolveConfig and discarded once the pool key is built; none are exposed.
// So the id carries the configured values: an empty user or port 0 mean
// "unset — the effective value was decided by resolution", and the address
// is the host string the session was opened with (already ssh -G-resolved
// by the transport for direct-host opens, ADR-0015). Host-key fingerprints
// are deliberately absent (§5.1 records exactly what that loses).
func endpointIDFor(sess session.Session) string {
	cfg := &ssh.ConnectConfig{}
	for _, o := range sess.SSHOptions() {
		o(cfg)
	}
	hops := make([]endpointHop, 0, 2)
	appendRouteHops(cfg, &hops)
	hops = append(hops, endpointHop{Address: sess.Host(), Port: cfg.Port, User: cfg.User})
	sum := sha256.Sum256([]byte(endpointAttestation{Hops: hops}.canonical()))
	return "v1:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// endpointHop is one hop of the route the connection follows: the configured
// address, port and user the dial would use for that hop. Port 0 and an
// empty user mean the value was left unset — the effective value is decided
// by resolution inside internal/ssh, which does not expose it.
type endpointHop struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	User    string `json:"user"`
}

// endpointAttestation is the pinned wire shape of the attestation record.
type endpointAttestation struct {
	Hops []endpointHop `json:"hops"`
}

// canonical serialises the attestation for hashing. encoding/json emits
// struct fields in declaration order, so these bytes ARE the pinned
// canonical serialisation (spec §5.1); a field added later changes every id
// derived from it, which is exactly what the version prefix is for. A fixed
// struct of plain fields cannot fail to marshal.
func (a endpointAttestation) canonical() string {
	b, _ := json.Marshal(a)
	return string(b)
}

// appendRouteHops walks the jump chain in connection order — the first
// bastion first, the target last — the same route the ssh package's dial
// follows (jumpRouteKey walks the same chain for the pool key). Each hop
// carries the configured user and port: the recursive JumpConfig when the
// resolver populated it, the flat Jump* fields otherwise (ssh.ConnectConfig
// documents both as populated for compatibility).
func appendRouteHops(cfg *ssh.ConnectConfig, hops *[]endpointHop) {
	if cfg == nil || cfg.JumpHost == "" {
		return
	}
	hopCfg := cfg.JumpConfig
	if hopCfg == nil {
		hopCfg = &ssh.ConnectConfig{User: cfg.JumpUser, Port: cfg.JumpPort}
	}
	*hops = append(*hops, endpointHop{Address: cfg.JumpHost, Port: hopCfg.Port, User: hopCfg.User})
	appendRouteHops(hopCfg, hops)
}

type localPTYFactory struct {
	log    log.Logger
	shint  shellintegration.ShellIntegration
	kernel lifecyclechannel.Kernel
	// shells answers which shell this user logs in with. Injected because the
	// platform half is a subprocess against the OS account database, and
	// because the answer decides the tier: it is the single call site of the
	// value (nocx-wwz0).
	shells loginshell.Resolver
	// transports records each local adapter's transport kind so the child
	// grant builder knows the child rides the inherited descriptor
	// (nocx-u7uh.11).
	transports *transportRegistry
	// registerLane binds a minted lifecycle lane to the session that owns
	// it, so published facts route to the right subscriber. Wired at the
	// composition root once the transport exists; nil (tests, or a server
	// without lifecycle wiring) leaves facts unrouted, which is the safe
	// direction — the renderer keys enhanced mode on the fact, and an
	// unregistered lane is a conventional terminal.
	registerLane func(lane lifecycle.LaneID, sid string)
	// reportIntegration enters a session into the integration axis the
	// product renders (nocx-dvql): what this factory started, and how far
	// it got before it handed the pty back. Only this factory knows which
	// binary was exec'd, so only it may answer — the transport registers
	// remote sessions from the ssh path instead. Nil (tests, or a server
	// without the wiring) leaves the session unregistered, which emits
	// nothing, which is the safe direction.
	reportIntegration func(sid, shell, status string, reason ssh.RefusalReason)
	// noteLifecycleLoss carries the adapter's loss cause to the same axis.
	// It is a separate seam from the published lifecycle facts because a
	// handshake that expires establishes no domain and therefore publishes
	// no fact at all — the silence this bead exists to end.
	noteLifecycleLoss func(lane lifecycle.LaneID, cause lifecyclechannel.LossCause)
	// procs watches the process this factory forked, so a shell replaced
	// out of the user's own startup files is noticed when it happens rather
	// than when the handshake bound expires ten seconds later (nocx-cgzc).
	// Injected because the observation is per-OS; nil (tests, or a server
	// without the wiring) leaves the bound as the only detector, which is
	// where the product was before.
	procs procwatch.Watcher
	// reportShellReplaced carries one observation to the session's
	// integration axis. Separate from reportIntegration because it answers
	// a different question — reportIntegration says what the launch did,
	// this says what happened to it afterwards — and because only the
	// transport may decide whether an observation still applies.
	reportShellReplaced func(sid, observed string)
	// noteBootstrapStage carries how far the shell got through nocx's rcfile
	// to the same axis (nocx-yww2). A third seam and not a variant of the
	// two above, because it is the only one that speaks BEFORE anything has
	// failed: the handshake bound can say a session did not integrate, and
	// only these facts can say where it stopped. Diagnostic only — nothing
	// reached through here may grant authority, and the transport's
	// NoteBootstrapStage emits nothing on its own.
	noteBootstrapStage func(sid, stage string)
}

// lifecyclePTY is an enhanced session's pty plus the lifecycle channel whose
// descriptor the shell inherited. It exists so the channel dies with the
// session that owns it: a conventional session is a bare pty and carries no
// channel at all, which is the observable difference ADR-0024 decision 4 is
// about.
type lifecyclePTY struct {
	pty.Pty
	ch *lifecyclechannel.Adapter
	// stopWatch releases this session's process observation. Nil when
	// nothing is watching — no observer wired, or a platform that cannot
	// look.
	stopWatch func()
	// bp is the bootstrap progress reader, when one was created. It dies
	// with the session for the same reason the channel does: both are
	// descriptors this session's shell inherited, and a reader outliving its
	// shell would report a stage for a session that no longer exists.
	bp *bootstrapprogress.Reader
}

// WaitErr forwards the shell's wait result from the pty this wraps
// (nocx-o3amz). It exists because lifecyclePTY embeds the pty.Pty INTERFACE,
// and a concrete type's method is not promoted through an embedded interface:
// *pty.LocalPty.WaitErr — the only thing that knows what cmd.Wait returned —
// was invisible to the optional-method assertion session.ExitOutcome makes, so
// every enhanced local session classified as a LOSS. The tab hung about marked
// "Connection lost" on a clean `exit`, and the shell's status was discarded.
//
// Anything else optional a pty grows needs the same forward, for the same
// reason. The `(nil, false)` answer is not a stand-in for a clean exit: it says
// this pty made no report, which ExitOutcome maps to a loss without inventing
// a status.
func (p *lifecyclePTY) WaitErr() (error, bool) {
	provider, ok := p.Pty.(interface{ WaitErr() (error, bool) })
	if !ok {
		return nil, false
	}
	return provider.WaitErr()
}

// SignalForeground forwards the signal to the pty this wraps, for exactly the
// reason WaitErr above does and with a costlier consequence (nocx-7l4ex.13).
//
// Without it the optional-method assertion in realSession.SignalForeground
// found nothing on an ENHANCED local session, answered pty.ErrNoForeground for
// every signal, and session.signal told the person "nothing is running in this
// pane" while their command plainly was — the incident nocx-92gfl.4 was filed
// as. The shell's protected process group had nothing to do with it: nothing
// ever asked the pty at all.
//
// ForegroundProcessGroup travels with it because it is the same seam asked a
// question instead of told to act, and a wrapper that can signal a group it
// cannot name is half-wired in the way that hides.
func (p *lifecyclePTY) SignalForeground(sig syscall.Signal) error {
	sg, ok := p.Pty.(interface {
		SignalForeground(sig syscall.Signal) error
	})
	if !ok {
		return pty.ErrNoForeground
	}
	return sg.SignalForeground(sig)
}

func (p *lifecyclePTY) ForegroundProcessGroup() (int, error) {
	fg, ok := p.Pty.(interface{ ForegroundProcessGroup() (int, error) })
	if !ok {
		return 0, pty.ErrNoForeground
	}
	return fg.ForegroundProcessGroup()
}

// ForegroundJob and SignalProcessGroup travel with the two above for the
// reason the comment on SignalForeground already gives, and they are here
// because that reason was proved a second time (nocx-i5a1k's sibling).
//
// nocx-uvac6.11 split a stop into "name the addressee once" and "signal that
// exact group", and a stop now ASKS ForegroundJob before it signals anything.
// This wrapper forwarded SignalForeground and ForegroundProcessGroup and not
// these two, so on an ENHANCED local session the optional-method assertion in
// realSession.ForegroundJob found nothing, answered pty.ErrNoForeground, and
// session.signal told the person "nothing is running in this pane" about a
// full-screen program that plainly was — nocx-92gfl.4 word for word, through
// a door that did not exist when it was closed.
//
// The rule this seam keeps: every method of the pty signal seam is forwarded
// here, or none is. A wrapper that answers some of them is not degraded, it
// is wrong, and it is wrong silently — nothing fails to compile and the
// answer it gives is a plausible one.
func (p *lifecyclePTY) ForegroundJob() (int, error) {
	fg, ok := p.Pty.(interface{ ForegroundJob() (int, error) })
	if !ok {
		return 0, pty.ErrNoForeground
	}
	return fg.ForegroundJob()
}

func (p *lifecyclePTY) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	sg, ok := p.Pty.(interface {
		SignalProcessGroup(pgid int, sig syscall.Signal) error
	})
	if !ok {
		return pty.ErrNoForeground
	}
	return sg.SignalProcessGroup(pgid, sig)
}

func (p *lifecyclePTY) Close() error {
	err := p.Pty.Close()
	_ = p.ch.Close()
	// The pid is the OS's to reuse the moment the shell is reaped, so a
	// watch that outlived its session would be a watch on somebody else's
	// process.
	if p.stopWatch != nil {
		p.stopWatch()
	}
	if p.bp != nil {
		_ = p.bp.Close()
	}
	return err
}

func (f *localPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	env := f.shint.ActivationEnv(cfg.Enhanced)
	if !cfg.Enhanced || f.kernel == nil {
		return pty.NewLocal(f.log, cfg, pty.WithExtraEnv(env))
	}
	// Which shell the user logs in with, and which local tier starts it. One
	// resolution, one log line, one decision — everything below reads them
	// (nocx-wwz0). Before this the answer was the constant "bash", so on macOS,
	// whose default login shell has been zsh since Catalina, every local tab
	// opened a shell the user had not chosen and none of their own environment.
	shell := f.shells.Resolve()
	kind := shellintegration.LocalShellKind(shell.Path)
	f.log.Info("local session shell resolved",
		"shell", shell.Path, "source", string(shell.Source), "tier", string(kind))

	if kind == shellintegration.ShellUnknown {
		// fish, csh, tcsh, dash, anything: started as itself, integrated not
		// at all, and SAID so. Substituting bash here is the defect this bead
		// is; degrading silently is the one AGENTS.md names. The activation
		// env is the conventional one — a shell that will not be integrated
		// must not be told it is being integrated.
		cfg.Command = shell.Path
		cfg.Args = []string{"-l"}
		p, err := pty.NewLocal(f.log, cfg, pty.WithExtraEnv(f.shint.ActivationEnv(false)))
		if err != nil {
			return nil, err
		}
		f.log.Warn("no local shell-integration tier for this login shell; the session is conventional",
			"shell", shell.Path, "reason", string(ssh.ReasonUnsupportedShell))
		// Reported here and only here. A local session's status has one owner
		// — this factory, the only thing that knows which binary it exec'd —
		// and registerRemoteIntegration returns early for local sessions, so
		// a reason carried on the session's optional-method seam instead
		// would be a write nothing reads: the fish user's tab would degrade
		// exactly as silently as before this bead.
		f.report(cfg.SessionID, shell.Path, transport.IntegrationConventional, ssh.ReasonUnsupportedShell)
		return p, nil
	}
	// Enhanced: the shell reports its lifecycle over a descriptor that is not
	// the tty (ADR-0024 decision 2). The child end goes in as fd 3; the parent
	// end stays here and is pumped by the adapter.
	// The handshake bound is set here rather than left to the adapter's
	// default: how long a shell may take to prove itself before the session
	// falls back to conventional is a product decision, and the composition
	// root is where product decisions belong.
	ch, child, err := lifecyclechannel.New(f.log, f.kernel,
		lifecyclechannel.WithHelloTimeout(lifecycle.HelloTimeout),
		lifecyclechannel.WithLossReporter(f.noteLifecycleLoss))
	if err != nil {
		return nil, err
	}
	// The bootstrap progress descriptor (nocx-yww2): a SECOND, one-way
	// descriptor, never the lifecycle channel and never its codec. It carries
	// two unauthenticated facts about how far the rcfile got, and the reason
	// it is a separate object rather than two more frames is the rule it must
	// not break — every envelope on the lifecycle channel is authenticated,
	// and these cannot be. A failure to create it costs the diagnosis, never
	// the session: the shell starts anyway and the handshake bound goes back
	// to being the only detector.
	bp, bpChild := f.newBootstrapProgress(cfg.SessionID)
	if f.transports != nil {
		f.transports.register(ch.TransportID(), transportKind{local: true})
	}
	// The local bootstrap (nocx-u7uh.21, extended to zsh by nocx-wwz0): the
	// user's own login shell starts with a transient artefact carrying THIS
	// session's capability and recovery fence in its TEXT — never in the
	// environment (ADR-0024 decision 2) — and the non-secret addressing as
	// NOCX_LIFECYCLE_* env, exactly the way the remote tier learns them.
	// shellintegration.LaunchOptions is the single description of "how a shell
	// learns its addressing and its capability", and LocalEnhancedLaunch owns
	// the per-tier difference between them: a transient rcfile for bash, a
	// transient ZDOTDIR for zsh, because zsh has no --rcfile.
	launch := ch.Launch()
	local, rerr := shellintegration.LocalEnhancedLaunch(shell.Path, kind, shellintegration.LaunchOptions{
		SessionID:   cfg.SessionID,
		Enhanced:    true,
		Capability:  launch.Capability,
		Recovery:    launch.Recovery,
		Lane:        string(launch.Lane),
		Domain:      string(launch.Domain),
		Epoch:       launch.Epoch,
		LifecycleFD: 3, // the child end of the socketpair, via ExtraFiles
		BootstrapFD: bootstrapFD(bpChild),
	})
	if rerr != nil {
		f.log.Warn("local lifecycle bootstrap failed; session stays conventional",
			"shell", shell.Path, "tier", string(kind), "error", rerr)
		_ = ch.Close()
		_ = child.Close()
		closeProgress(bp, bpChild)
		// No channel and no bootstrap: the user's OWN login shell, plain, with
		// a visible native prompt (the script's init bails without config).
		// The activation env is the conventional one — a shell that will not
		// be integrated must not be told it is being integrated.
		cfg.Command = shell.Path
		cfg.Args = []string{"-i"}
		p, perr := pty.NewLocal(f.log, cfg, pty.WithExtraEnv(f.shint.ActivationEnv(false)))
		if perr != nil {
			return nil, perr
		}
		// The session asked for integration and will not get it, so it says
		// so — with `unknown`, because the failure is a local bootstrap error
		// and none of the refusal vocabulary describes it. `unknown` is a
		// real visible answer, never a synonym for success, which is what the
		// renderer would read an absent reason as.
		f.report(cfg.SessionID, shell.Path, transport.IntegrationConventional, ssh.ReasonUnknown)
		return p, nil
	}
	cfg.Command = local.Command
	cfg.Args = local.Args
	// Descriptor order is the contract: fd 3 is the lifecycle channel, fd 4
	// the bootstrap progress pipe, and the rcfile reads both numbers from the
	// environment block LaunchOptions rendered above. Appending the progress
	// end second is what makes bootstrapFD's answer true.
	p, err := pty.NewLocal(f.log, cfg,
		pty.WithExtraEnv(env), pty.WithExtraEnv(local.Env), pty.WithExtraFiles(extraFiles(child, bpChild)...))
	// The child ends are the shell's once the fork has happened; this process
	// keeps no reference either way.
	_ = child.Close()
	if bpChild != nil {
		_ = bpChild.Close()
	}
	if err != nil {
		_ = ch.Close()
		if bp != nil {
			_ = bp.Close()
		}
		// The shell erases the artefact itself once it has read it; on a spawn
		// failure there is no shell, so the capability would sit in TMPDIR
		// until the machine cleaned it.
		local.Cleanup()
		return nil, err
	}
	// Bind the lane to the session that will receive its facts. Without
	// this, published lifecycle facts are dropped (the transport routes by
	// lane registration) and enhanced mode never engages — the whole
	// lifecycle stack reachable only from its own tests (AGENTS.md check 5).
	if cfg.SessionID != "" && f.registerLane != nil {
		f.registerLane(ch.Lane(), cfg.SessionID)
	}
	// The lane is bound first, deliberately: the loss reporter resolves a
	// lane to its session, so a handshake that expired between the two
	// would have nowhere to land. Registering the axis afterwards is the
	// safe order — the status is only emitted after the open ack anyway.
	f.report(cfg.SessionID, p.Shell(), transport.IntegrationStarting, ssh.ReasonNone)
	// Watched only here, on the one path that has a handshake to shorten.
	// A session already reported conventional has nothing an observation
	// could bring forward, and watching it could only produce noise.
	return &lifecyclePTY{Pty: p, ch: ch, bp: bp, stopWatch: f.watchForReplacement(cfg.SessionID, p)}, nil
}

// watchForReplacement asks the observer to say when the shell this factory
// just started stops being the process running under its pid — the takeover
// nocx-cgzc measured, where a wrapper execs out of the user's own startup
// file milliseconds after the fork and the product finds out ten seconds
// later.
//
// It is a SECOND detector, never a replacement for the first: the handshake
// bound still bounds the handshake, and a platform that cannot observe an
// exec (or a kernel that refuses the watch) degrades to exactly the product
// that shipped before this — which is why the failure is a Debug line and
// not an error the session carries.
func (f *localPTYFactory) watchForReplacement(sid string, p *pty.LocalPty) func() {
	if sid == "" || f.procs == nil || f.reportShellReplaced == nil {
		return nil
	}
	pid := p.Pid()
	if pid <= 0 {
		return nil
	}
	shell := p.Shell()
	stop, err := f.procs.Started(pid, shell, func(obs procwatch.Observation) {
		f.log.Info("the shell this session started was replaced before it answered",
			"session", sid, "pid", obs.PID, "started", shell, "observed", obs.Name)
		f.reportShellReplaced(sid, obs.Name)
	})
	if err != nil {
		f.log.Debug("this session's shell is not watched for replacement",
			"session", sid, "error", err)
		return nil
	}
	return stop
}

// report enters this session into the integration axis, when the wiring
// exists. A local session that never asked for integration never reaches
// here, and so emits nothing at all: absence is how "conventional by design"
// is expressed, and a session with nothing to say must not nag.
func (f *localPTYFactory) report(sid, shell, status string, reason ssh.RefusalReason) {
	if sid == "" || shell == "" || f.reportIntegration == nil {
		return
	}
	f.reportIntegration(sid, shell, status, reason)
}

// newBootstrapProgress creates this session's progress reader, or reports
// nothing at all. Both halves are legitimate outcomes: a session with no
// session id has nowhere to route a stage, a factory with no sink has nobody
// to tell, and a pipe that cannot be created costs a diagnosis rather than a
// terminal. Every caller downstream treats a nil reader as "no progress
// reporting", which is exactly where the product was before this existed.
func (f *localPTYFactory) newBootstrapProgress(sid string) (*bootstrapprogress.Reader, *os.File) {
	if sid == "" || f.noteBootstrapStage == nil {
		return nil, nil
	}
	bp, child, err := bootstrapprogress.New(f.log, func(stage bootstrapprogress.Stage) {
		f.noteBootstrapStage(sid, string(stage))
	})
	if err != nil {
		f.log.Warn("bootstrap progress channel unavailable; a startup that does not return will report only a timeout",
			"session", sid, "error", err)
		return nil, nil
	}
	return bp, child
}

// bootstrapFD is the descriptor number the rcfile writes its progress facts
// to: 4, because ExtraFiles hands the shell fd 3 first and the lifecycle
// channel is always that one. Zero when there is no progress pipe, which the
// rcfile's own guard reads as "report nothing".
func bootstrapFD(child *os.File) int {
	if child == nil {
		return 0
	}
	return 4
}

// extraFiles assembles the descriptors the shell inherits, in the order their
// numbers depend on.
func extraFiles(lifecycleChild, progressChild *os.File) []*os.File {
	if progressChild == nil {
		return []*os.File{lifecycleChild}
	}
	return []*os.File{lifecycleChild, progressChild}
}

// closeProgress releases both ends on a path that will not start a shell.
func closeProgress(bp *bootstrapprogress.Reader, child *os.File) {
	if bp != nil {
		_ = bp.Close()
	}
	if child != nil {
		_ = child.Close()
	}
}

func (a *App) Start(ctx context.Context) error {
	a.Logger.Info("starting application services")

	home, err := os.UserHomeDir()
	if err != nil {
		a.Logger.Warn("shellintegration: could not determine home dir", "error", err)
	} else if err := a.ShellIntegration.EnsureInstalled(home); err != nil {
		a.Logger.Warn("shellintegration: install failed", "error", err)
	}

	return a.Transport.Start(ctx)
}

func (a *App) Shutdown(ctx context.Context) {
	a.Logger.Info("shutting down application")
	if err := a.Transport.Stop(ctx); err != nil {
		a.Logger.Error("transport shutdown error", "error", err)
	}
	// After the transport, so nothing is still writing a note.
	if a.noteCloser != nil {
		if err := a.noteCloser.Close(); err != nil {
			a.Logger.Error("notes database shutdown error", "error", err)
		}
	}
	// After the transport, so nothing is still asking the vault for secrets.
	// This seals it as well as stopping its timer: leaving the root key in a
	// live heap on the way out would undo the reason the seal exists.
	if a.vaultCloser != nil {
		a.vaultCloser.Close()
	}
	// Discovery cadence last: every lease is released and every timer
	// stopped, so no probe can outlive the process.
	if a.discoverySched != nil {
		_ = a.discoverySched.Close()
	}
	// The git environment resolution (nocx-6pz0) runs in the background
	// from factory construction; cancel it so no resolution child can
	// outlive the process.
	if a.gitFactory != nil {
		a.gitFactory.Stop()
	}
	// The process observer last of the background owners: nothing can ask
	// for a new watch once the transport and the sessions are gone.
	if a.procs != nil {
		_ = a.procs.Close()
	}
	// The UI-state document last of the writers: its debounce means the very
	// last drag of the session may still be pending, and Close is what turns
	// a clean quit into a write rather than a lost layout. After the
	// transport, so no renderer can still be setting a layout underneath it.
	if a.UIState != nil {
		if err := a.UIState.Close(); err != nil {
			a.Logger.Error("ui state shutdown error", "error", err)
		}
	}
	a.Logger.Info("application stopped")
	// Close the log file last, after the final line: the stable copy of
	// the log must not lose the stop record to a shutdown ordering.
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
}

// sshFactoryAdapter adapts ssh.SSH to session.SSHFactory.
type sshFactoryAdapter struct {
	client ssh.SSH
}

func (a *sshFactoryAdapter) Connect(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.Channel, error) {
	return a.client.Connect(ctx, host, opts...)
}

// proberAdapter adapts ssh.RealClient to transport.Prober and
// transport.HostKeyTruster (the same client owns known_hosts for both).
type proberAdapter struct {
	client *ssh.RealClient
}

func (a *proberAdapter) Probe(ctx context.Context, host string, cfg *ssh.ConnectConfig) error {
	return a.client.ProbeConfig(ctx, host, cfg)
}

func (a *proberAdapter) ProbeWithResult(ctx context.Context, host string, cfg *ssh.ConnectConfig) (string, error) {
	return a.client.ProbeConfigWithResult(ctx, host, cfg)
}

func (a *proberAdapter) TrustHostKey(ctx context.Context, addr string, key []byte) (string, error) {
	return a.client.TrustHostKey(addr, key)
}

// remoteLauncherAdapter adapts shellintegration.RemoteLauncher to
// ssh.RemoteLauncher (nocx-ei04). internal/ssh and internal/shellintegration
// declare their own identically-named ShellKind, LaunchOptions and
// RefusalReason on purpose — internal/ssh must not depend on shellintegration
// — and Go interface satisfaction needs identical named types, so the
// composition root translates between the two declarations at wiring time.
// Reasons map explicitly; a value the ssh vocabulary does not know degrades
// to ssh.ReasonUnknown, a distinct "integration did not happen, and I cannot
// say why", never to ssh.ReasonNone — the product renders ReasonNone as
// "integration succeeded", which is how a soft degrade becomes invisible
// (AGENTS.md). The original tripwire for an unmapped reason was a panic; a
// crash in the composition root of a terminal backend is the most extreme
// violation of ADR-0004's fail-open invariant, so the tripwire shouts into
// the log and hands the caller a usable plain-shell fallback instead.
type remoteLauncherAdapter struct {
	inner  shellintegration.RemoteLauncher
	logger log.Logger
	// reportBootstrapOutcome carries the bootstrap's terminal outcome, as a
	// product reason, to the session integration axis. Keyed by the lifecycle
	// LANE, which is the addressing the transport already uses to route this
	// session's facts (RegisterLifecycleLane), so no second registry is
	// created for one more fact.
	//
	// Without it the outcome was a log line and nothing else: a refused
	// bootstrap left the axis in `starting` until some other detector — a
	// hello bound, a process observation — concluded it with a vaguer word,
	// or in the remote case with nothing at all. Nil leaves the outcome in
	// the log, which is the safe direction and the state before P5.
	reportBootstrapOutcome func(lane string, reason ssh.RefusalReason)
}

func (a *remoteLauncherAdapter) StartCommand(shell ssh.ShellKind, opts ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	cmd, reason, ok := a.inner.StartCommand(
		shellintegration.ShellKind(shell),
		shellintegration.LaunchOptions{
			SessionID: opts.SessionID,
			Enhanced:  opts.Enhanced,
			// The lifecycle channel (ADR-0024 decision 2 "Over SSH"): the
			// port becomes NOCX_LIFECYCLE_PORT and the capability the
			// rcfile's @CAP@. Empty when no channel was established — the
			// session is conventional and the launch carries nothing.
			Capability:    opts.Capability,
			Recovery:      opts.Recovery,
			Lane:          opts.Lane,
			Domain:        opts.Domain,
			Epoch:         opts.Epoch,
			LifecyclePort: opts.LifecyclePort,
			// The stage-1 digest the carrier commits to. Empty until the
			// frame sender exists (design §12, P2).
			StageDigest: opts.StageDigest,
		},
	)
	if !ok {
		return "", a.mapRefusalReason(reason), false
	}
	// Accepted: the pinned contract says ok=true means the shell was
	// integrated, so the reason must be the empty "no refusal" value. A
	// launcher that accepts while claiming a refusal contradicts itself; the
	// ssh layer would drop the reason on an accept, so decline instead — the
	// claimed reason stays visible on the product and the session falls back
	// to a plain shell (ADR-0004:60) — and shout the contradiction into the
	// log rather than killing the backend with a panic.
	if reason != shellintegration.ReasonNone {
		a.logger.Error("shellintegration launcher accepted while naming a refusal reason; treating as a decline",
			"reason", reason, "shell", shell, "session_id", opts.SessionID)
		return "", a.mapRefusalReason(reason), false
	}
	return cmd, ssh.ReasonNone, true
}

// mapRefusalReason translates a shellintegration refusal into the ssh
// vocabulary. The switch is exhaustive over the declared set on purpose: the
// production launcher only ever returns these three, so the default arm is a
// tripwire for the next reason added to one package and forgotten in the
// other. It degrades to the distinct ssh.ReasonUnknown — never a silent
// ssh.ReasonNone — and shouts, keeping the tripwire while failing open
// (ADR-0004:60) instead of crashing the terminal.
func (a *remoteLauncherAdapter) mapRefusalReason(r shellintegration.RefusalReason) ssh.RefusalReason {
	switch r {
	case shellintegration.ReasonNone:
		return ssh.ReasonNone
	case shellintegration.ReasonUnsupportedShell:
		return ssh.ReasonUnsupportedShell
	case shellintegration.ReasonNoSecureTemp:
		return ssh.ReasonNoSecureTemp
	default:
		a.logger.Error("shellintegration launcher returned unmapped refusal reason; add it to remoteLauncherAdapter",
			"reason", r)
		return ssh.ReasonUnknown
	}
}

// Prepare is remoteLauncherAdapter's other half: the frames the command's
// loader reads (design §12, P2). It lives on the same adapter as StartCommand
// because the two are one delivery — the command commits to the digest of the
// stage-1 frame, so a second component building the frames could disagree with
// the one building the command, and the far side would refuse a stage-1 that
// was perfectly valid.
//
// The gap it used to carry is closed. A bootstrap OUTCOME is more precise than
// ssh.RefusalReason could carry, so every one of the twenty-one outcomes
// reached the product as ssh.ReasonUnknown — "integration did not happen and
// the backend cannot say why" — with the precise answer only in a log. That is
// a soft degrade the UI contradicts. The vocabulary now has one member per
// outcome, spelled identically, and mapBootstrapOutcome below is the exhaustive
// switch that keeps the two tables from drifting apart silently.
//
// The secret is NOT built here. It is built inside the run, after frame 1 has
// been verified, the receiver has announced itself and the publish has reached
// a terminal outcome — design §6.1's steps 3, 4 and 5, the last two behind the
// MintGate this returns.
func (a *remoteLauncherAdapter) Prepare(shell ssh.ShellKind, opts ssh.LaunchOptions) (string, ssh.BootstrapRun, ssh.BootstrapGate, bool) {
	if opts.Capability != "" && (opts.LifecyclePort < 1 || opts.LifecyclePort > 65535) {
		a.logger.Error("shellintegration: capability supplied without a valid lifecycle port; the session runs a plain shell",
			"session_id", opts.SessionID, "port", opts.LifecyclePort)
		return "", nil, nil, false
	}
	sopts := shellintegration.LaunchOptions{
		SessionID:     opts.SessionID,
		Enhanced:      opts.Enhanced,
		Capability:    opts.Capability,
		Recovery:      opts.Recovery,
		Lane:          opts.Lane,
		Domain:        opts.Domain,
		Epoch:         opts.Epoch,
		LifecyclePort: opts.LifecyclePort,
	}
	stage, err := shellintegration.Stage1Frame(shellintegration.ShellKind(shell), sopts)
	if err != nil {
		// Fail closed and say so: no stage-1 means no carrier, and the
		// session opens a plain shell rather than one that blocks on a
		// frame nobody will send.
		a.logger.Error("shellintegration: stage-1 could not be rendered; the session runs a plain shell",
			"shell", shell, "session_id", opts.SessionID, "error", err)
		return "", nil, nil, false
	}
	gate := shellintegration.NewMintGate()
	// The publish's own error, kept so the §6.4 subsystem row can be
	// reported as its cause rather than as its symptom. It is written once,
	// by the ssh side, before the gate opens, and read once, after the
	// bootstrap has finished — the gate is the happens-before between them.
	publishFailure := &atomic.Pointer[error]{}
	plan := shellintegration.BootstrapPlan{Stage1: stage}
	// §6.1 steps 4 and 5, on EVERY path and not only the one with something
	// to mint: frame 2 is not delivered until the lifecycle receiver has
	// answered and the publish has reached a terminal outcome. Step 8 — the
	// far side re-proving the generation as it now stands — follows frame 2
	// whether it carried the pair or a refusal, so a session with no
	// lifecycle channel needs this barrier for exactly the reason a session
	// with one does. It was inside the `Capability != ""` arm below, and
	// that is what left a refused forward racing its own publish.
	//
	// The wait is bounded by T, and it has to be bounded here rather than
	// left to the session's own context: a publish that never settles would
	// otherwise hold the frame — and the session in `starting` — for the
	// life of the tab, which §7 forbids outright.
	plan.Ordered = func(ctx context.Context) error {
		wctx, cancel := context.WithTimeout(ctx, shellintegration.PublishDeadline)
		defer cancel()
		_, gerr := gate.Await(wctx)
		return gerr
	}
	if opts.Capability != "" {
		// The bearer itself. By the time this runs the barrier above has
		// returned without an error, so both facts are in; what is left is
		// the mint, and an error here still means the far side gets a
		// NON-SECRET refusal rather than a secret we then discard.
		plan.Secret = shellintegration.SecretFunc(func(context.Context) ([]byte, error) {
			return shellintegration.SecretFrame(sopts)
		})
	}
	run := func(ctx context.Context, stream ssh.BootstrapStream) ssh.RefusalReason {
		// The two BootstrapStream declarations have identical method
		// sets, so the ssh value satisfies the shellintegration
		// interface directly — the translation is in the types, not in
		// an object that copies bytes between them.
		outcome := shellintegration.DeliverBootstrap(ctx, a.logger, stream, plan)
		// The publish's own failure joins the outcome here, in the one
		// function that answers "which reason does the product hear" for
		// BOTH paths. The gate is the happens-before between the two: the
		// pointer is written by the ssh side before the gate opens, and read
		// here only after the bootstrap that waited on it has finished.
		var publishErr error
		if perr := publishFailure.Load(); perr != nil {
			publishErr = *perr
		}
		reason := bootstrapProductReason(a.logger, outcome, publishErr)
		if reason != ssh.ReasonNone {
			a.logger.Warn("shell bootstrap refused",
				"outcome", string(outcome), "reason", string(reason), "session_id", opts.SessionID)
		}
		// The fence's confidentiality interval, backend side, closes here
		// on the copy this attempt holds — refusal, timeout AND after a
		// SUCCESSFUL bootstrap alike (§5.3, assertion 11). The kernel's own
		// expected value is a DIFFERENT copy and deliberately outlives this
		// one: it is what validates a recovery acknowledgement, so closing
		// the authority interval is not what destroys it and the two must
		// not be conflated.
		sopts.Capability = ""
		sopts.Recovery = ""
		if a.reportBootstrapOutcome != nil {
			a.reportBootstrapOutcome(opts.Lane, reason)
		}
		return reason
	}
	return shellintegration.StageDigest(stage), run, gateAdapter{g: gate, publishErr: publishFailure}, true
}

// gateAdapter is the composition root doing what it is for: the ssh side
// produces §6.1's two facts and shellintegration.MintGate consumes them, and
// neither package may import the other. Four lines rather than a second
// vocabulary for one concept (AD-8).
//
// The publish OUTCOME does not cross: all four of §6.1's terminal outcomes
// open the gate, because a failed publish is not a refusal — the far side may
// still accept a generation installed earlier, and it is the owner of that
// question. What crosses is the error, and only so a diagnosis can name it.
type gateAdapter struct {
	g          *shellintegration.MintGate
	publishErr *atomic.Pointer[error]
}

func (a gateAdapter) ReceiverReady()                { a.g.ReceiverReady() }
func (a gateAdapter) ReceiverUnavailable(err error) { a.g.ReceiverUnavailable(err) }

func (a gateAdapter) PublishSettled(err error) {
	if a.publishErr != nil {
		a.publishErr.Store(&err)
	}
	if err != nil {
		a.g.PublishSettled(shellintegration.PublishFailed)
		return
	}
	a.g.PublishSettled(shellintegration.PublishCommitted)
}

// mapBootstrapOutcome turns the bootstrap's closed outcome set into the
// product vocabulary. The switch is exhaustive on purpose and the default arm
// is a tripwire: an outcome added to shellintegration and forgotten here is a
// loud log and ssh.ReasonUnknown, never a silent ReasonNone.
//
// The two vocabularies are spelled identically, which makes this look like a
// cast and is exactly why it is not one. A cast would accept a member that
// exists on one side and not the other and put an undeclared string on the
// wire, past a contract whose whole point is that the enum is closed
// (AGENTS.md rule 5). The switch cannot: a new outcome fails
// TestBootstrapOutcomes_EachHasAProductReason instead.
func mapBootstrapOutcome(lg log.Logger, o shellintegration.Outcome) ssh.RefusalReason {
	switch o {
	case shellintegration.OutcomeBootstrapAccepted:
		return ssh.ReasonNone
	case shellintegration.OutcomeLoaderTermiosUnavailable:
		return ssh.ReasonLoaderTermiosUnavailable
	case shellintegration.OutcomeBootstrapInterrupted:
		return ssh.ReasonBootstrapInterrupted
	case shellintegration.OutcomeBootstrapProtocol:
		return ssh.ReasonBootstrapProtocol
	case shellintegration.OutcomeStageTooLarge:
		return ssh.ReasonStageTooLarge
	case shellintegration.OutcomeNoSecureTemp:
		return ssh.ReasonNoSecureTemp
	case shellintegration.OutcomeStageDigestUnavailable:
		return ssh.ReasonStageDigestUnavailable
	case shellintegration.OutcomeStageDigestMismatch:
		return ssh.ReasonStageDigestMismatch
	case shellintegration.OutcomeStageFDUnavailable:
		return ssh.ReasonStageFDUnavailable
	case shellintegration.OutcomeStageSourceFailed:
		return ssh.ReasonStageSourceFailed
	case shellintegration.OutcomeSecretTooLarge:
		return ssh.ReasonSecretTooLarge
	case shellintegration.OutcomeSecretMalformed:
		return ssh.ReasonSecretMalformed
	case shellintegration.OutcomeSecretNotForThisSession:
		return ssh.ReasonSecretNotForThisSession
	case shellintegration.OutcomeCapabilityFDUnavailable:
		return ssh.ReasonCapabilityFDUnavailable
	case shellintegration.OutcomeCapabilityUnlinkFailed:
		return ssh.ReasonCapabilityUnlinkFailed
	case shellintegration.OutcomeCapabilityWriteFailed:
		return ssh.ReasonCapabilityWriteFailed
	case shellintegration.OutcomeGenerationUnavailable:
		return ssh.ReasonGenerationUnavailable
	case shellintegration.OutcomeReceiverUnready:
		return ssh.ReasonReceiverUnready
	case shellintegration.OutcomeBootstrapTimeout:
		return ssh.ReasonBootstrapTimeout
	case shellintegration.OutcomeBootstrapOutOfOrder:
		return ssh.ReasonBootstrapOutOfOrder
	case shellintegration.OutcomeChannelUnavailable:
		return ssh.ReasonChannelUnavailable
	default:
		lg.Error("shellintegration returned an unmapped bootstrap outcome; add it to mapBootstrapOutcome",
			"outcome", string(o))
		return ssh.ReasonUnknown
	}
}

// bootstrapProductReason is the WHOLE of "which reason does the product hear
// for this bootstrap", and it is one function because there are two paths and
// there must not be two answers (AD-8).
//
// The saved path reaches it from remoteLauncherAdapter.Prepare's run; the
// typed path from typedDelivery. Both produce the same two inputs — the
// bootstrap's terminal outcome, and whether nocx's own publish failed — and
// §6.4's subsystem row is the rule that joins them: the far side answers
// generation-unavailable, which is TRUE and is the symptom; when the publish
// also failed, the half a user can act on is that nocx could not write a
// generation, so the cause is reported in place of the symptom. Only when BOTH
// are true — a far side that found nothing AND a publish that failed — is the
// substitution honest; a far side that finds nothing after a SUCCESSFUL
// publish is a different fault and keeps its own name.
func bootstrapProductReason(lg log.Logger, o shellintegration.Outcome, publishErr error) ssh.RefusalReason {
	reason := mapBootstrapOutcome(lg, o)
	if reason == ssh.ReasonGenerationUnavailable && publishErr != nil {
		lg.Warn("shellintegration: the far host has no generation and the publish failed; "+
			"reporting the cause rather than the symptom", "error", publishErr)
		return ssh.ReasonPublishUnavailable
	}
	return reason
}

// remoteLifecycleProvider implements ssh.RemoteLifecycle with the lifecycle
// kernel and the ssh client (ADR-0024 decision 2 "Over SSH"; bead
type remoteLifecycleProvider struct {
	client *ssh.RealClient
	kernel lifecyclechannel.Kernel
	logger log.Logger
	// reportLoss carries the remote adapter's §6.2 loss cause to the session
	// integration axis, keyed by the adapter's own lane. Wired at the
	// composition root once the server exists; nil reports nowhere, which is
	// the state before P5 and the reason a refused remote session used to sit
	// in `starting` forever.
	reportLoss lifecycleremote.LossReporter
	// transports records each remote adapter's transport kind and its
	// forwarded port so the child grant builder can compose the child's
	// launch (nocx-u7uh.11).
	transports *transportRegistry
	// registerLane binds the minted lane to the session that owns it
	// (RegisterLifecycleLane at the transport), so the published facts of
	// this remote domain route to the right subscriber. Wired at the
	// composition root once the server exists; nil leaves facts unrouted,
	// the safe direction.
	registerLane func(lane lifecycle.LaneID, sid string)
}

// Establish implements ssh.RemoteLifecycle.
func (p *remoteLifecycleProvider) Establish(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.RemoteLifecycleLaunch, io.Closer, error) {
	tc, err := p.client.TunnelConn(ctx, host, opts...)
	if err != nil {
		return ssh.RemoteLifecycleLaunch{}, nil, fmt.Errorf("lifecycle tunnel lease: %w", err)
	}
	// The product decisions land here, not in the adapter: how long a
	// shell may take to prove itself (the protocol constant) and how many
	// candidate connections the adapter serves at once — the same reason
	// the local path passes its hello timeout at the composition root.
	adapter, cfg, err := lifecycleremote.New(p.logger, p.kernel, tc,
		lifecycleremote.WithHelloTimeout(lifecycle.HelloTimeout),
		lifecycleremote.WithMaxCandidates(lifecycleremote.DefaultMaxCandidates),
		// §6.2's loss events, routed to the session integration axis. The
		// local path has had this since nocx-dvql and the remote path had
		// nothing: a remote session whose shell never spoke established no
		// domain, so no fact was published and no cause was reported, and
		// the axis stayed at `starting` for the life of the tab — which §7
		// forbids outright.
		lifecycleremote.WithLossReporter(p.reportLoss),
	)
	if err != nil {
		_ = tc.Close()
		return ssh.RemoteLifecycleLaunch{}, nil, err
	}
	if p.transports != nil {
		p.transports.register(adapter.TransportID(), transportKind{local: false, port: cfg.Port})
	}
	// The session id rides the connect options (ssh.WithSessionID); apply
	// them to a scratch config to read it back — the lane must be bound to
	// the session that will receive its facts, or the whole remote
	// lifecycle publication is dropped at the transport.
	scratch := &ssh.ConnectConfig{}
	for _, opt := range opts {
		opt(scratch)
	}
	if scratch.SessionID != "" && p.registerLane != nil {
		p.registerLane(cfg.Lane, scratch.SessionID)
	}
	return ssh.RemoteLifecycleLaunch{
		Lane:       string(cfg.Lane),
		Domain:     string(cfg.Domain),
		Epoch:      cfg.Epoch,
		Port:       cfg.Port,
		Capability: cfg.Capability,
		Recovery:   cfg.Recovery,
	}, adapter, nil
}

// apiRouteLeaser turns the PROFILE an API-testing environment names into a
// lease on that profile's pooled SSH connection (design §6.5, §7.1). It is
// apisend.ConnectionLeaser, and it exists here rather than in that package
// because resolving a profile is the composition root's job: the sender
// knows about routes, not about credentials, jump routes or the profile
// store.
//
// Two things it deliberately does NOT do.
//
// It does not resolve the profile itself. connection.Resolver is the one
// owner of "what does this profile id mean", the same one a tab and a port
// forward go through — so a request routed through a connection is
// authorized by exactly the credential authorization a tab is, and reaches
// the same host through the same jump route.
//
// It does not take a connection of its own. ssh.RealClient.TunnelConn goes
// through acquirePooled, which SHARES when the resolved pool key matches and
// establishes one otherwise (AD-7, AD-4). So a send rides the connection a
// tab already has when the key matches, and authenticates anew when it does
// not — a route names a destination, not a window, and the design says so in
// as many words rather than promising a particular live session.
//
// The lease is not released here. apisend's route holds it for as long as
// the connection lives and takes a fresh one when it dies; releasing it
// after one send would drop a pool reference other tabs and forwards are
// counting on, and would cost every send a new authentication.
type apiRouteLeaser struct {
	client *ssh.RealClient

	// mu guards the resolver, which is set after construction. The
	// transport's own resolverHolder has the same shape for the same
	// reason: the value is read per call, never captured, so a lease taken
	// before SetProfileResolver refuses by name instead of dereferencing
	// nil.
	mu       sync.RWMutex
	resolver transport.ProfileResolver
}

func (l *apiRouteLeaser) setResolver(r transport.ProfileResolver) {
	l.mu.Lock()
	l.resolver = r
	l.mu.Unlock()
}

// LeaseForProfile implements apisend.ConnectionLeaser.
func (l *apiRouteLeaser) LeaseForProfile(ctx context.Context, profileID string) (ssh.TunnelConn, error) {
	l.mu.RLock()
	resolver := l.resolver
	l.mu.RUnlock()
	if resolver == nil {
		return nil, fmt.Errorf("no profile resolver is wired, so connection %s cannot be resolved", profileID)
	}
	host, cfg, err := resolver.Resolve(profileID)
	if err != nil {
		// Resolving reads the stored secret, so a sealed vault surfaces
		// here — and the sender wraps this into its own named refusal, so
		// the reason reaches a surface that can offer the unlock prompt.
		return nil, fmt.Errorf("resolve connection %s: %w", profileID, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("connection %s resolved to no configuration", profileID)
	}
	// The WHOLE resolved config rides one option, exactly as a forward's
	// does (ws_tunnel.go): credentials, jump route and authorized endpoints
	// together, so the lease is pool-keyed and authorized like a tab.
	return l.client.TunnelConn(ctx, host, func(dst *ssh.ConnectConfig) { *dst = *cfg })
}

// apiSecretMaterial is the composition root's join between the capability's
// reference grammar and the credential package's stanced read.
//
// IT EXISTS RATHER THAN THE CAPABILITY HOLDING THE RESOLVER because a
// capability may not (nocx-o3606): an operation-stance read blocks until a
// person answers the vault's unlock, and vault.unseal needs the lane an
// operation holds. The transport calls the reference pass after its operation
// has released that lane, and by then this adapter may block for as long as
// the dialog takes.
//
// THE STANCE IS AN OPERATION, deliberately. Report() would never raise the
// prompt, so a sealed vault would turn a perfectly good request into an
// unresolved reference and tell the person their collection was wrong. The
// reason is what the vault's own dialog shows.
type apiSecretMaterial struct{ resolver credential.Resolver }

func (m apiSecretMaterial) Material(ctx context.Context, id credential.SecretID) (credential.Secret, error) {
	if m.resolver == nil {
		return credential.Secret{}, vault.ErrVaultSealed
	}
	return m.resolver.Resolve(ctx, id, credential.Operation("send an API request"))
}
