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
	"github.com/shady2k/nocx/internal/transport"
)

type App struct {
	Logger           log.Logger
	Pty              session.PTYFactory
	Session          *session.Reg
	Transport        *transport.WSServer
	Config           *config.Stub
	ShellIntegration shellintegration.ShellIntegration
}

func New() (*App, error) {
	slogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := log.NewSlogAdapter(slogger)

	cfg := config.NewStub(logger)
	ptf := &localPTYFactory{log: logger}
	sess := session.New(logger, ptf)
	shint := shellintegration.NewStub(logger)
	tp := transport.NewWSServer(logger, sess)

	app := &App{
		Logger:           logger,
		Pty:              ptf,
		Session:          sess,
		Transport:        tp,
		Config:           cfg,
		ShellIntegration: shint,
	}

	logger.Info("application initialized")
	return app, nil
}

type localPTYFactory struct{ log log.Logger }

func (f *localPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	return pty.NewLocal(f.log, cfg)
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

func (a *App) WSPort() int {
	return a.Transport.Port()
}
