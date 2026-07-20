package app

import (
	"context"
	"log/slog"
	"os"

	"github.com/shady2k/nocx/internal/config"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

type App struct {
	Logger           log.Logger
	Pty              *pty.Stub
	SSH              *ssh.Stub
	Session          *session.Stub
	Transport        *transport.Stub
	Config           *config.Stub
	ShellIntegration *shellintegration.Stub
}

func New() (*App, error) {
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := log.NewSlogAdapter(slogger)

	cfg := config.NewStub(logger)
	pt := pty.NewStub(logger)
	sshClient := ssh.NewStub(logger)
	sess := session.NewStub(logger, pt, sshClient)
	shint := shellintegration.NewStub(logger)
	tp := transport.NewStub(logger, sess)

	app := &App{
		Logger:           logger,
		Pty:              pt,
		SSH:              sshClient,
		Session:          sess,
		Transport:        tp,
		Config:           cfg,
		ShellIntegration: shint,
	}

	logger.Info("application initialized")
	return app, nil
}

func (a *App) Start(ctx context.Context) error {
	a.Logger.Info("starting application services")
	return a.Transport.Start(ctx)
}

func (a *App) Shutdown(ctx context.Context) {
	a.Logger.Info("shutting down application")
	if err := a.Transport.Stop(ctx); err != nil {
		a.Logger.Error("transport shutdown error", "error", err)
	}
	a.Logger.Info("application stopped")
}
