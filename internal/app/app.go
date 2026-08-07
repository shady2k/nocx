package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/connection"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/contentkey"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/discovery"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/filesystem/sftp"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/nativeports"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/update"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
	"github.com/shady2k/nocx/internal/vault/system"
	"github.com/shady2k/nocx/internal/vaultreset"
)

type App struct {
	Logger           log.Logger
	Pty              session.PTYFactory
	Session          *session.Reg
	Transport        *transport.WSServer
	ShellIntegration shellintegration.ShellIntegration
	Updater          update.Updater
	Profiles         profile.ProfileRepository
	Credentials      credential.SecretStore
	// Sandbox is the per-tab filesystem sandbox backend (ADR-0019). Injected
	// here — the single composition root — and nowhere else.
	Sandbox sandbox.Service
	// UnlockRequester lets backend code request a vault unlock from the
	// user (the second direction, nocx-25k9.22). Behind an interface so
	// app.New() never reaches into the transport directly (AD-8). Set
	// from the transport after construction.
	UnlockRequester transport.UnlockRequester

	// vaultCloser releases the vault's background worker and seals it at
	// shutdown. Held as a minimal interface rather than *vault.Vault so the
	// composition root keeps depending on behaviour instead of a type.
	vaultCloser interface{ Close() }

	// discoverySched owns the port-discovery cadence (nocx-wzc4.2); closed
	// at shutdown so no timer outlives the process.
	discoverySched *discovery.Scheduler

	// logFilePath is where the backend log file lives — the stable,
	// findable copy of the log the delivery-path decisions are written
	// to (the P0 that had to be diagnosed from a JSON file's mtime). ""
	// means file logging is unavailable and only stderr carries the log.
	logFilePath string
	// logFile is the open append handle, closed at shutdown after the
	// final line. nil when file logging is unavailable.
	logFile *os.File
}

// contentCompactionFloor is the hysteresis fraction of the disk ceiling at
// which an in-progress compaction stops (design §5.4 names hysteresis as
// part of the ceiling). A mechanism parameter, not a user knob.
const contentCompactionFloor = 0.8

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
	return p
}

// SetDialogService attaches the native dialog capability (dialog.* RPCs). It
// is wired from main.go's WailsApp.startup — the Wails context it needs only
// exists there, after the transport was built — and must be called before
// Start, so no renderer request can observe the unset state.
func (a *App) SetDialogService(ds transport.DialogService) {
	a.Transport.SetDialogService(ds)
}

// Log logs a message from the frontend.
func (a *App) Log(message string) {
	a.Logger.Info("frontend: " + message)
}

type Option func(*optionSet)

type optionSet struct {
	wsAddr string
	// keystoreProbe overrides the OS-keystore availability decision that
	// picks the ContentDB key's home (nocx-rtg0.14). Test-only: production
	// probes the real system provider once at startup and logs the outcome.
	keystoreProbe func(context.Context) bool
	// logFilePath overrides where the backend log file lives. Test-only:
	// without it New() resolves the profile's data directory, and a test
	// must not write into the developer's real profile (nocx-ti8w).
	logFilePath *string
	// noSystemKeystore builds the system vault provider over a keyring that
	// is absent by construction. Dev-only, and the portable way to say what
	// DBUS_SESSION_BUS_ADDRESS could only say on Linux — see
	// WithoutSystemKeystore.
	noSystemKeystore bool
}

// WithWSAddr pins the WebSocket listen address instead of the default
// 127.0.0.1:0. Dev-only; shipped code should never set this.
func WithWSAddr(addr string) Option {
	return func(o *optionSet) { o.wsAddr = addr }
}

// WithoutSystemKeystore builds the vault's system provider over a keyring that
// fails every operation, so the backend behaves exactly like one on a host with
// no OS secret store.
//
// Dev-only, wired from cmd/devharness. It exists because the e2e cases that are
// ABOUT the passphrase path had no portable way to state their premise: the
// suite pointed DBUS_SESSION_BUS_ADDRESS at nothing, which is a Linux
// mechanism, and on macOS go-keyring talks to the Security framework and
// ignores it. Those cases were therefore not arranging "no keystore" on macOS
// at all — and a backend given a disposable $HOME there put a "Keychain not
// found" dialog in front of whoever was running the suite, once per start
// (nocx-o4hg).
//
// It also skips the startup probe, because a probe is a real keystore call and
// there is nothing here to call.
func WithoutSystemKeystore() Option {
	return func(o *optionSet) { o.noSystemKeystore = true }
}

// WithKeystoreProbe overrides the OS-keystore availability decision for the
// ContentDB key. Test-only: it makes the composition root deterministic on
// hosts that do (or do not) run a Secret Service, so the no-keystore branch
// of the key lifecycle can be asserted without depending on the machine.
func WithKeystoreProbe(probe func(context.Context) bool) Option {
	return func(o *optionSet) { o.keystoreProbe = probe }
}

// WithLogFilePath pins the backend log file path instead of the app-dir
// default. Test-only: an empty path disables file logging (stderr only);
// any other path must be under a disposable directory the test owns.
func WithLogFilePath(path string) Option {
	return func(o *optionSet) { o.logFilePath = &path }
}

func New(opts ...Option) (*App, error) {
	var o optionSet
	for _, opt := range opts {
		opt(&o)
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

	logFilePath := filepath.Join(paths.DataDir(), "nocx.log")
	if o.logFilePath != nil {
		logFilePath = *o.logFilePath // test override; empty disables file logging
	}
	var logFile *os.File
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
			slogger = slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f),
				&slog.HandlerOptions{Level: slog.LevelInfo}))
		}
	}
	logger := log.NewSlogAdapter(slogger)
	// The log names itself, first: a running session can say where the
	// file is by reading its own first line.
	if logFilePath != "" {
		logger.Info("backend log file", "path", logFilePath)
	}

	shint := shellintegration.New(logger)
	// Sandbox backend (ADR-0019): the per-tab filesystem sandbox needs the
	// cache dir for its per-session runtime trees (design spec §5.2).
	sandboxSvc := sandbox.New(logger, paths.CacheDir())
	ptf := &localPTYFactory{log: logger, shint: shint, sandbox: sandboxSvc}
	sess := session.New(logger, ptf)

	// SSH config resolver
	// resolver so the authorization comparison matches canonical hostnames.
	// AD-4: nocx asks OpenSSH via ssh -G; the injected resolver is the sole
	// path through which ~/.ssh/config is read.
	home, _ := os.UserHomeDir()
	sshConfigPath := filepath.Join(home, ".ssh", "config")
	sshCfgResolver := ssh.NewSSHConfigResolver(logger, sshConfigPath, "")

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

	// The installed fact (nocx-mlm7 P7, design §5.4): backend-owned,
	// persisted across restarts, keyed by the resolved destination
	// identity, written only from a passport the renderer accepted and
	// invalidated when a connection that expected installed-script
	// produces no passport. The delivery planner reads it to choose the
	// compact installed line; without it every host bootstraps.
	installedFacts := ssh.NewInstalledFactStore(logger, docStore, "installed-facts.json")
	// ContentDB (ADR-0018, amended 2026-08-01): the one SQLite database for
	// unbounded private content, encrypted at rest by the adiantum VFS
	// (ncruces/go-sqlite3 — no cgo). The real store is constructed below,
	// after the vault exists, via the content key lifecycle (nocx-rtg0.9);
	// the stub is the null implementation per AD-8 and the fallback when
	// the key cannot be read (the terminal starts without durable history
	// and history.query answers source=session, which the overlay labels
	// honestly).
	var contentDB content.ContentDB = content.NewStub(logger)

	sysProv := system.New()
	if o.noSystemKeystore {
		sysProv = system.New(system.WithKeyring(system.AbsentKeyring{}))
	}
	fileProv := file.New(docStore, "vault-file.json")
	reg, err := vault.NewRegistry(sysProv, fileProv)
	if err != nil {
		return nil, fmt.Errorf("vault registry: %w", err)
	}

	ctx := context.Background()
	// Probe the system provider once at startup and log the outcome. A
	// machine with no Secret Service says so in the log rather than
	// failing mysteriously later. The WithKeystoreProbe override (tests)
	// replaces the probe entirely — a probe is a real keychain write and
	// must not run on a host the test claims has no keystore.
	probeStatus := vault.Status{}
	systemReady := false
	switch {
	case o.noSystemKeystore:
		// Nothing to probe: the provider is absent by construction, and a
		// probe is a real keystore call.
		probeStatus = vault.Status{Reason: "no system keystore (dev override)"}
	case o.keystoreProbe != nil:
		systemReady = o.keystoreProbe(ctx)
	default:
		probeStatus = sysProv.Probe(ctx)
		systemReady = probeStatus.Ready
	}
	slogger.Info("vault system-provider availability probe",
		"ready", systemReady, "reason", probeStatus.Reason)

	v, err := vault.New(docStore, reg, slogger)
	if err != nil {
		return nil, fmt.Errorf("vault init: %w", err)
	}

	settingsRegistry := settings.New(docStore, v)

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
	historyPolicy := policyFromSettings(settingsRegistry)
	budget, budgetErr := budgetFromSettings(settingsRegistry)
	if budgetErr != nil {
		slogger.Warn("durable command history unavailable; starting without it", "reason", budgetErr)
	} else if key, keyErr := contentkey.LoadOrCreate(ctx, contentkey.Config{
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
		slogger.Warn("durable command history unavailable; starting without it", "reason", keyErr)
	} else if db, openErr := content.Open(ctx, content.Config{
		Path:   filepath.Join(paths.DataDir(), "content.db"),
		Key:    key,
		Budget: budget,
		Policy: historyPolicy,
		Logger: logger,
	}); openErr != nil {
		slogger.Warn("durable command history unavailable; starting without it", "reason", openErr)
	} else {
		contentDB = db
	}

	// Live History policy: a Settings toggle applies without a restart. The
	// transport's own notifier broadcasts settings.changed to the renderer;
	// this second listener keeps the store's policy in sync.
	settingsRegistry.AddNotifier(func(_ int, keys []string) {
		for _, k := range keys {
			switch k {
			case settings.HistoryEnabled.Key(), settings.HistoryRetentionDays.Key(),
				settings.HistoryOutputEnabled.Key():
				if v, err := settingsRegistry.GetBool(settings.HistoryEnabled); err == nil {
					historyPolicy.SetEnabled(v)
				}
				if v, err := settingsRegistry.GetNumber(settings.HistoryRetentionDays); err == nil {
					historyPolicy.SetRetentionDays(int(v))
				}
				if v, err := settingsRegistry.GetBool(settings.HistoryOutputEnabled); err == nil {
					historyPolicy.SetOutputEnabled(v)
				}
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
	// Probe result store: operational evidence for connections.test.
	// Process-lifetime only (not persisted across restarts).
	probeResultStore := transport.NewProbeResultStore()
	// Profile service: single validated write path for profiles and groups.
	// Used by the import handlers and version transitions.
	profileSvc := profile.NewProfileService(profileStore)

	tpOpts := []transport.WSServerOption{
		transport.WithProfileRepository(profileStore),
		transport.WithGroupRepository(profileStore),
		transport.WithCredentialStore(v),
		transport.WithSettingsRegistry(settingsRegistry),
		transport.WithSandboxService(sandboxSvc),
		transport.WithVaultLifecycle(v),
		transport.WithVaultReset(vaultreset.New(v, profileStore, slogger)),
		transport.WithProfileUsageStore(usageStore),
		transport.WithExportPaths(paths),
		// One ContentDB at the composition root (AD-8): the same store backs
		// export and history.query. A stub is correct until the SQLCipher
		// backing lands (ADR-0018 gate); history.query then answers
		// source=session, which the overlay labels "this session only".
		transport.WithExportContentDB(contentDB),
		transport.WithContentDB(contentDB),
		transport.WithProber(&proberAdapter{client: sshClient}),
		transport.WithProfileService(profileSvc),
		transport.WithHostKeyTruster(&proberAdapter{client: sshClient}),
		// The remote shell launcher (nocx-xs1d), adapted across the two
		// identically-named declarations and wired into every ConnectConfig
		// the transport builds. Before this line the launcher was reachable
		// from its own tests and nowhere else (AGENTS.md check 5).
		transport.WithRemoteLauncher(&remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: logger}),
		// Launcher staging for the hand-typed-ssh rewrite (nocx-pu4.6).
		// The launcher is ~35 KB and a typed line has only the tty, whose
		// canonical buffer is 4096 bytes, so the payload goes to a private
		// file and the renderer types the path. The home directory is
		// known here and nowhere below: the transport must not pick a
		// filesystem location of its own.
		transport.WithLauncherStager(shellintegration.NewLauncherStager(logger, home)),
		// The installed fact (nocx-mlm7 P7, design §5.4): the persisted
		// memory of which resolved destinations carry a committed
		// integration. The delivery planner reads it to choose the compact
		// installed line; the observation RPC writes and invalidates it.
		transport.WithInstalledFactStore(installedFacts),
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
		// The completion adapter (nocx-w7h.15): two completers wired at the
		// composition root — the handler routes by session kind. The local
		// completer answers from the backend's filesystem; the SSH completer
		// runs a second shell on the remote host through DiscoveryConn, the
		// same owned pooled lease the discovery ladder uses.
		transport.WithCompleters(
			completion.NewLocal(),
			completion.NewSSH(sshExecConnProvider(sshClient)),
		),

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
	}
	// WithWSAddr set the field and nothing read it, so NOCX_WS_ADDR was accepted
	// and ignored and the listener always took an ephemeral port. The dev stand
	// pins 9880 precisely so the SSH forward survives a restart; instead every
	// restart moved the backend and left the open tab talking to a port that no
	// longer existed — which reads as "the backend stopped responding".
	if o.wsAddr != "" {
		tpOpts = append(tpOpts, transport.WithListenAddr(o.wsAddr))
	}
	tp := transport.NewWSServer(logger, sess, tpOpts...)

	// One resolver, one consumer family: connections.test probes and
	// ordinary connects resolve identically. Created after tp so the
	// UnlockRequester (the second direction, nocx-25k9.22) can be wired
	// into every ConnectConfig the resolver builds.
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
	resolver := connection.NewResolver(
		profileStore, profileStore, v,
		connection.WithConfigResolver(sshCfgResolver),
		connection.WithUnlockRequester(tp.RequestUnlock),
		connection.WithPasswordAsker(tp.RequestConnectionPassword),
		connection.WithSecretCreator(v),
		connection.WithRemoteInstaller(shint),
	)
	tp.SetProfileResolver(resolver)

	app := &App{
		Logger:           logger,
		Pty:              ptf,
		Session:          sess,
		Transport:        tp,
		ShellIntegration: shint,
		Profiles:         profileStore,
		Credentials:      v,
		Sandbox:          sandboxSvc,
		discoverySched:   discoverySched,
		UnlockRequester:  tp,
		logFilePath:      logFilePath,
		logFile:          logFile,
	}

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
			return local.New(localOpts...), nil
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
			Provider:   sftp.New(fs, opts...),
			endpointID: endpointIDFor(sess),
		}, nil
	}
}

// endpointAttestedProvider wraps a remote provider with the endpoint
// attestation (spec §5.1, D4/D6). The transport reads it through the
// optional filesystemEndpointAttester seam; a local provider never carries
// it, which is what makes files.reveal a local-only capability.
type endpointAttestedProvider struct {
	filesystem.Provider
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
	log     log.Logger
	shint   shellintegration.ShellIntegration
	sandbox sandbox.Service
}

func (f *localPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	env := f.shint.ActivationEnv(cfg.Enhanced)
	return pty.NewLocal(f.log, cfg, pty.WithExtraEnv(env), pty.WithSandboxService(f.sandbox))
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
	a.Logger.Info("application stopped")
	// Close the log file last, after the final line: the stable copy of
	// the log must not lose the stop record to a shutdown ordering.
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
}

// LogFilePath returns where this backend's log file lives, or "" when file
// logging is unavailable (stderr only). A running session can say where the
// log is instead of the P0's mtime archaeology — the desktop binding
// (WailsApp) and the dev stand both reach it through this accessor.
func (a *App) LogFilePath() string {
	return a.logFilePath
}

func (a *App) WSPort() int {
	return a.Transport.Port()
}

func (a *App) WSToken() string {
	return a.Transport.Token()
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
}

func (a *remoteLauncherAdapter) StartCommand(shell ssh.ShellKind, opts ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	cmd, reason, ok := a.inner.StartCommand(
		shellintegration.ShellKind(shell),
		shellintegration.LaunchOptions{
			SessionID:     opts.SessionID,
			Enhanced:      opts.Enhanced,
			EnvironmentID: opts.EnvironmentID,
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
