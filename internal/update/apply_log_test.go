package update

// THE UPDATE'S LINES NAME THE BUILD ON BOTH SIDES (nocx-vh2hr.1.25).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// Two nightlies share one marketing version (ADR-79b6cy61), so a line reading
// `from=0.8.1 to=0.8.1` cannot say which build was replaced by which. Every
// update line that names the from/to pair carries the build number beside each
// version; the number is the one the manifest signs and the About page shows.
func TestTheApplyLinesNameBothBuilds(t *testing.T) {
	useStampedTestBuild(t) // Date = 2026-10-28T10:00:00Z: the applying build's number is 202610281000
	url, sum, size, srv := serveAppImageFixture(t)
	defer srv.Close()

	dir := t.TempDir()
	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	sl := logtest.Slog(t)
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.8.1",
		InstallPath:    installPath,
		StateDir:       filepath.Join(dir, "state"),
		Logger:         log.NewSlogAdapter(sl),
	})
	info := &UpdateInfo{
		Version: "0.8.1",
		Commit:  "cccccccccccccccccccccccccccccccccccccccc",
		BuiltAt: "2026-11-06T21:30:00Z",
		URL:     url, SHA256: sum, Size: size,
	}
	if err := u.Apply(context.Background(), info); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	text := logtest.TextSlog(sl)
	pair := "from=0.8.1 fromBuild=202610281000 to=0.8.1 toBuild=202611062130"
	for _, message := range []string{
		"update apply: journal written, starting download",
		"update apply: exchange complete — restart to apply",
	} {
		line := lineContaining(text, message)
		if line == "" {
			t.Fatalf("no %q line was written; log was:\n%s", message, text)
		}
		if !strings.Contains(line, pair) {
			t.Errorf("%q does not name both builds; want %q in:\n%s", message, pair, line)
		}
	}
}

// lineContaining answers the one log line whose message is message.
func lineContaining(text, message string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, `"`+message+`"`) {
			return line
		}
	}
	return ""
}
