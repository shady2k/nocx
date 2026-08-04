package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/connection"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/contentkey"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
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

	// vaultCloser releases the vault's background worker and seals it at
	// shutdown. Held as a minimal interface rather than *vault.Vault so the
	// composition root keeps depending on behaviour instead of a type.
	vaultCloser interface{ Close() }
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
}

// WithWSAddr pins the WebSocket listen address instead of the default
// 127.0.0.1:0. Dev-only; shipped code should never set this.
func WithWSAddr(addr string) Option {
	return func(o *optionSet) { o.wsAddr = addr }
}

// WithKeystoreProbe overrides the OS-keystore availability decision for the
// ContentDB key. Test-only: it makes the composition root deterministic on
// hosts that do (or do not) run a Secret Service, so the no-keystore branch
// of the key lifecycle can be asserted without depending on the machine.
func WithKeystoreProbe(probe func(context.Context) bool) Option {
	return func(o *optionSet) { o.keystoreProbe = probe }
}

func New(opts ...Option) (*App, error) {
	var o optionSet
	for _, opt := range opts {
		opt(&o)
	}

	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := log.NewSlogAdapter(slogger)

	// Storage paths resolve first: the sandbox backend needs the cache dir
	// for its per-session runtime trees (design spec §5.2).
	paths, err := storage.NewAppPaths()
	if err != nil {
		return nil, fmt.Errorf("storage paths: %w", err)
	}

	shint := shellintegration.New(logger)
	sandboxSvc := sandbox.New(logger, paths.CacheDir())
	ptf := &localPTYFactory{log: logger, shint: shint, sandbox: sandboxSvc}
	sess := session.New(logger, ptf)

	// SSH config resolver: shared by both the SSH client and the profile
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
	if o.keystoreProbe != nil {
		systemReady = o.keystoreProbe(ctx)
	} else {
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
	// Probe result store: operational evidence for connections.test.
	// Process-lifetime only (not persisted across restarts).
	probeResultStore := transport.NewProbeResultStore()
	// Profile service: single validated write path for profiles and groups.
	// Used by the import handlers and version transitions.
	profileSvc := profile.NewProfileService(profileStore)
	// One resolver, one consumer family: connections.test probes and
	// ordinary connects resolve identically.
	resolver := connection.NewResolver(
		profileStore, profileStore, v,
		connection.WithConfigResolver(sshCfgResolver),
	)

	tpOpts := []transport.WSServerOption{
		transport.WithProfileRepository(profileStore),
		transport.WithGroupRepository(profileStore),
		transport.WithCredentialStore(v),
		transport.WithVaultLifecycle(v),
		transport.WithVaultReset(vaultreset.New(v, profileStore, slogger)),
		transport.WithProfileResolver(resolver),
		transport.WithSettingsRegistry(settingsRegistry),
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
		transport.WithSandboxService(sandboxSvc),

		transport.WithProbeResultStore(probeResultStore),
		transport.WithSSHConfigResolver(sshCfgResolver, sshConfigPath),
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

	app := &App{
		Logger:           logger,
		Pty:              ptf,
		Session:          sess,
		Transport:        tp,
		ShellIntegration: shint,
		Profiles:         profileStore,
		Credentials:      v,
		Sandbox:          sandboxSvc,
		vaultCloser:      v,
	}

	logger.Info("application initialized")
	return app, nil
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
	a.Logger.Info("application stopped")
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
