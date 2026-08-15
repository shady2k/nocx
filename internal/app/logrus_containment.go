package app

// The logrus containment (design §4.1, ADR-0028 consequence): logrus
// arrives compiled-in through compose → schema → gonja/exec — the template
// engine, not the logic — and it collides with the rule that structured
// logging goes through one log/slog-backed interface (internal/log). So it
// is contained, not tolerated: the composition root redirects the standard
// logrus logger into our slog-backed interface, nothing reaches stderr
// around it, and a test asserts it (acceptance: "asserted by receipt, not
// by silence" — a logrus write arrives at the injected sink with its level
// and fields intact, and nothing is written to stderr).
//
// The redirect is idempotent and global-on-purpose: it targets the process's
// ONE standard logrus logger, and the hook routes to whichever app logger
// was installed last. A second App in the same process (a test) simply
// re-targets it.

import (
	"io"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/shady2k/nocx/internal/log"
)

var (
	logrusMu     sync.Mutex
	logrusTarget log.Logger
	logrusHooked bool
)

// installLogrusContainment redirects logrus into l. Call once per backend
// at the composition root, after the app logger exists.
func installLogrusContainment(l log.Logger) {
	logrusMu.Lock()
	defer logrusMu.Unlock()
	logrusTarget = l
	// Nothing reaches stderr around logrus: the hook receives every record
	// and routes it into our logger; whatever escapes the hook (a level
	// below the app's threshold) is discarded, never printed.
	logrus.SetOutput(io.Discard)
	// One level authority: ours. logrus's own default (Info) would drop
	// Trace/Debug records before the hook sees them, and the app's slog
	// level is the only level decision this repo recognises.
	logrus.SetLevel(logrus.TraceLevel)
	logrus.SetFormatter(&logrus.JSONFormatter{}) // irrelevant once output is discarded; kept so a stray direct write is structured
	if !logrusHooked {
		logrus.StandardLogger().AddHook(logrusContainmentHook{})
		logrusHooked = true
	}
}

type logrusContainmentHook struct{}

func (logrusContainmentHook) Levels() []logrus.Level { return logrus.AllLevels }

// Fire routes one logrus record into the slog-backed interface, mapping the
// level vocabulary and carrying the fields through as slog args. Fatal and
// Panic map to Error — logrus's own os.Exit/panic behaviour after the hooks
// is not ours to change, but nothing in this dependency graph calls them.
func (logrusContainmentHook) Fire(entry *logrus.Entry) error {
	logrusMu.Lock()
	l := logrusTarget
	logrusMu.Unlock()
	if l == nil {
		return nil
	}
	args := make([]any, 0, len(entry.Data)*2)
	for k, v := range entry.Data {
		args = append(args, k, v)
	}
	switch entry.Level {
	case logrus.TraceLevel, logrus.DebugLevel:
		l.Debug(entry.Message, args...)
	case logrus.InfoLevel:
		l.Info(entry.Message, args...)
	case logrus.WarnLevel:
		l.Warn(entry.Message, args...)
	default: // Error, Fatal, Panic
		l.Error(entry.Message, args...)
	}
	return nil
}
