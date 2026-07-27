package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/shady2k/nocx/internal/connection"
	"github.com/shady2k/nocx/internal/content"
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
)

type App struct {
	Logger           log.Logger
	Pty              session.PTYFactory
	Session          *session.Reg
	Transport        *transport.WSServer
	ShellIntegration shellintegration.ShellIntegration
	Updater          update.Updater
	Profiles         profile.ProfileRepository
	Credentials      credential.CredentialStore
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

	// SSH client (AD-4): real client on x/crypto/ssh, honors ~/.ssh/config.
	sshClient, err := ssh.NewReal(logger)
	if err != nil {
		return nil, fmt.Errorf("ssh client: %w", err)
	}
	sess = sess.WithSSHFactory(&sshFactoryAdapter{client: sshClient})

	// Profile + credential stores (AD-4/ADR-0011): storage paths resolved
	// by the shared Paths capability. The DocumentStore is the atomic JSON
	// capability; the profile store uses it for profiles.json.
	paths, err := storage.NewOSPaths("nocx")
	if err != nil {
		return nil, fmt.Errorf("storage paths: %w", err)
	}
	docStore := storage.NewDocumentStore(paths.ConfigDir())
	profileStore := profile.NewJSONStoreWithDocStore(docStore, "profiles.json")
	credStore := credential.NewKeychain()
	settingsRegistry := settings.New(docStore, credStore)

	// Create SSH endpoint resolver for ADR-0013 canonical endpoint resolution
	sshEndpointResolver := ssh.NewEndpointResolver(sshClient)
	
	// Install resolver in profileStore BEFORE any Load (migration needs it)
	// ADR-0013 §Migration: resolver creates grants on match with canonical endpoint
	profileStore.SetEndpointResolver(func(p profile.SSHProfile) (string, uint16, error) {
		ep, err := sshEndpointResolver.ResolveEndpoint(p)
		if err != nil {
			return "", 0, err
		}
		return ep.Host, ep.Port, nil
	})
	

	
	tpOpts := []transport.WSServerOption{
		transport.WithProfileRepository(profileStore),
		transport.WithProfileAtomicMutator(profileStore),  // ADR-0013: atomic profile+grant operations
		transport.WithEndpointResolver(&transport.SSHConfigEndpointResolver{Resolver: sshEndpointResolver}),
		transport.WithGroupRepository(profileStore),
		transport.WithCredentialMetadataRepository(profileStore),
		transport.WithCredentialMetadataMutator(profileStore),
		transport.WithCredentialStore(credStore),
		transport.WithProfileResolver(connection.NewResolver(profileStore, profileStore, credStore, profileStore.EndpointResolver())),
		transport.WithSettingsRegistry(settingsRegistry),
		transport.WithExportPaths(paths),
		transport.WithExportContentDB(content.NewStub(logger)),
	}
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
		Credentials:      credStore,
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
