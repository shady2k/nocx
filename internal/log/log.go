package log

import (
	"context"
	"log/slog"
)

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
	WithContext(ctx context.Context) Logger
}

type SlogAdapter struct {
	log *slog.Logger
}

func NewSlogAdapter(log *slog.Logger) *SlogAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &SlogAdapter{log: log}
}

func (a *SlogAdapter) Debug(msg string, args ...any) { a.log.Debug(msg, args...) }
func (a *SlogAdapter) Info(msg string, args ...any)  { a.log.Info(msg, args...) }
func (a *SlogAdapter) Warn(msg string, args ...any)  { a.log.Warn(msg, args...) }
func (a *SlogAdapter) Error(msg string, args ...any) { a.log.Error(msg, args...) }

func (a *SlogAdapter) With(args ...any) Logger {
	return &SlogAdapter{log: a.log.With(args...)}
}

func (a *SlogAdapter) WithContext(ctx context.Context) Logger {
	return &SlogAdapter{log: a.log.With(slog.String("traceID", traceIDFromContext(ctx)))}
}

func traceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey).(string); ok {
		return v
	}
	return ""
}

type ctxKeyType struct{}

var ctxKey ctxKeyType
