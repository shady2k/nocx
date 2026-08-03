package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/connection"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
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

	// vaultCloser releases the vault's background worker and seals it at
	// shutdown. Held as a minimal interface rather than *vault.Vault so the
	// composition root keeps depending on behaviour instead of a type.
	vaultCloser interface{ Close() }
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
}

// WithWSAddr pins the WebSocket listen address instead of the default
// 127.0.0.1:0. Dev-only; shipped code should never set this.
func WithWSAddr(addr string) Option {
	return func(o *optionSet) { o.wsAddr = addr }
}

func New(opts ...Option) (*App, error) {
	var o optionSet
	for _, opt := range opts {
		opt(&o)
	}

	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := log.NewSlogAdapter(slogger)

	shint := shellintegration.New(logger)
	ptf := &localPTYFactory{log: logger, shint: shint}
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
	paths, err := storage.NewAppPaths()
	if err != nil {
		return nil, fmt.Errorf("storage paths: %w", err)
	}
	docStore := storage.NewDocumentStore(paths.ConfigDir())
	profileStore := profile.NewJSONStoreWithDocStore(docStore, "profiles.json")

	sysProv := system.New()
	fileProv := file.New(docStore, "vault-file.json")
	reg, err := vault.NewRegistry(sysProv, fileProv)
	if err != nil {
		return nil, fmt.Errorf("vault registry: %w", err)
	}

	// Probe the system provider once at startup and log the outcome.
	// A machine with no Secret Service says so in the log rather than
	// failing mysteriously later.
	ctx := context.Background()
	probeStatus := sysProv.Probe(ctx)
	slogger.Info("vault system-provider availability probe",
		"ready", probeStatus.Ready, "reason", probeStatus.Reason)

	v, err := vault.New(docStore, reg, slogger)
	if err != nil {
		return nil, fmt.Errorf("vault init: %w", err)
	}
	settingsRegistry := settings.New(docStore, v)
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

	// Backup service (ADR-0018): uses profile store, settings registry, and
	// the shared DocumentStore for its crash-recovery journal.
	backupSvc := backup.NewService(profileStore, settingsRegistry, docStore)
	if err := backupSvc.Recover(); err != nil {
		return nil, fmt.Errorf("backup recovery: %w", err)
	}

	tpOpts := []transport.WSServerOption{
		transport.WithProfileRepository(profileStore),
		transport.WithGroupRepository(profileStore),
		transport.WithCredentialStore(v),
		transport.WithVaultLifecycle(v),
		transport.WithVaultReset(vaultreset.New(v, profileStore, slogger)),
		transport.WithProfileResolver(resolver),
		transport.WithSettingsRegistry(settingsRegistry),
		transport.WithBackupService(backupSvc),
		transport.WithProfileUsageStore(usageStore),
		transport.WithProber(&proberAdapter{client: sshClient}),
		transport.WithProfileService(profileSvc),
		transport.WithHostKeyTruster(&proberAdapter{client: sshClient}),

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
		vaultCloser:      v,
	}

	logger.Info("application initialized")
	return app, nil
}

type localPTYFactory struct {
	log   log.Logger
	shint shellintegration.ShellIntegration
}

func (f *localPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	env := f.shint.ActivationEnv(cfg.Enhanced)
	return pty.NewLocal(f.log, cfg, pty.WithExtraEnv(env))
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
