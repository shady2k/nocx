package app

// The composition root's half of app.diagnostics (nocx-3an1s.1.8): the two
// facts the transport cannot know — this machine's helper connection and the
// log file's size — and nothing about a destination.

import (
	"os"

	"github.com/shady2k/nocx/internal/diagnostics"
)

type appDiagnosticsProvider struct {
	opener  *localHelperOpener
	logPath string
}

func (p appDiagnosticsProvider) HelperStatus() (s diagnostics.HelperStatus) {
	if p.opener == nil {
		return diagnostics.HelperStatus{Known: true, Failure: "no helper opener"}
	}
	return p.opener.HelperStatus()
}

// LogSize is the FILE's size, never its path: Copy diagnostics is pasted into
// an issue, and a profile path names the person who wrote it.
func (p appDiagnosticsProvider) LogSize() (int64, bool) {
	if p.logPath == "" {
		return 0, false
	}
	info, err := os.Stat(p.logPath)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}
