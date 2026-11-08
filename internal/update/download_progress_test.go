package update

// WHAT THE WINDOW CAN SEE WHILE THE BYTES ARRIVE (nocx-vh2hr.1.25).
//
// The owner's update took 73 seconds to download and the notice said nothing:
// no percent, no bar. The notice's poll was there (nocx-vh2hr.1.15) — this is
// the half behind it. [Updater.State] gathers the download's numbers and then
// reads the journal, and it used to return `UpdateState{}` on every journal
// test that fails; during a download the record's newBundleID is still zero
// because the exchange has not been staged, so those tests always failed and
// the numbers were thrown away. The state read is what the toast polls, so the
// toast was told "nothing is downloading" once per tick.
//
// The test drives a REAL Apply over a THROTTLED body: it reads the state only
// through the interface the window calls, and it asserts on the state reaching
// a byte count rather than on a clock (a fixed sleep would pass on whatever
// machine it was written on).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/version"
)

// slowAppImage serves an AppImage that arrives in chunks with a pause between
// them, so a state read can happen while the body is still on its way. The
// fixture is a valid minimal AppImage because the same Apply extracts and
// verifies it once the download lands.
func slowAppImage(t *testing.T, chunk, chunks int, pause time.Duration) (url, sum string, size int64, srv *httptest.Server) {
	t.Helper()
	header := []byte{
		0x7f, 0x45, 0x4c, 0x46, // ELF magic
		0x02,             // 64-bit
		0x01,             // little-endian
		0x01,             // ELF version
		0x00,             // SYSV
		0x41, 0x49, 0x02, // AppImage type-2
	}
	payload := make([]byte, chunk*chunks)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	fixture := append(header, payload...)

	h := sha256.Sum256(fixture)
	sum = hex.EncodeToString(h[:])
	size = int64(len(fixture))

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(fixture)))
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for offset := 0; offset < len(fixture); offset += chunk {
			end := offset + chunk
			if end > len(fixture) {
				end = len(fixture)
			}
			if _, err := w.Write(fixture[offset:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(pause):
			}
		}
	}))
	return srv.URL, sum, size, srv
}

func TestStateReportsTheDownloadWhileTheBodyArrives(t *testing.T) {
	dir := t.TempDir()
	url, sum, size, srv := slowAppImage(t, 64<<10, 24, 15*time.Millisecond)
	defer srv.Close()

	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.8.1",
		InstallPath:    installPath,
		StateDir:       filepath.Join(dir, "state"),
	})
	info := &UpdateInfo{Version: "0.8.1", URL: url, SHA256: sum, Size: size}

	done := make(chan error, 1)
	go func() { done <- u.Apply(context.Background(), info) }()

	// WAIT ON THE STATE MOVING, not on a clock. The tick that follows the
	// first flushed chunk must already report the body.
	var seen UpdateState
	deadline := time.After(30 * time.Second)
	for seen.Downloading != true || seen.Downloaded == 0 {
		seen = u.State(context.Background())
		if seen.Downloading && seen.Downloaded > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the download finished before a state read ever reported progress (Apply: %v); the window was told nothing while it ran", err)
		case <-deadline:
			t.Fatal("no state read reported the download that was still arriving")
		case <-time.After(2 * time.Millisecond):
		}
	}
	if seen.Total != size {
		t.Fatalf("state reported %d bytes of %d", seen.Downloaded, seen.Total)
	}
	if seen.Downloaded > seen.Total {
		t.Fatalf("state reported %d downloaded of %d declared", seen.Downloaded, seen.Total)
	}
	if seen.Pending {
		t.Fatal("a download that has not exchanged anything was reported as a pending restart")
	}

	if err := <-done; err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if after := u.State(context.Background()); after.Downloading {
		t.Fatalf("the download ended and the state still says it is downloading: %+v", after)
	}
}

// The journal's own pair of builds is recorded when the transaction opens, so
// the rollback line — which runs in a LATER launch — can name the build it
// reverted FROM as well as the one it reverted to (nocx-vh2hr.1.25).
func TestTheJournalRecordsTheBuildThatStartedTheTransaction(t *testing.T) {
	useStampedTestBuild(t)
	dir := t.TempDir()
	url, sum, size, srv := serveAppImageFixture(t)
	defer srv.Close()

	installPath := filepath.Join(dir, "nocx.AppImage")
	makeMinimalAppImage(t, installPath)
	stateDir := filepath.Join(dir, "state")
	u := NewUpdater(UpdaterConfig{
		Platform:       newFakePlatform(),
		Fetcher:        &mockFetcher{},
		CurrentVersion: "0.1.0",
		InstallPath:    installPath,
		StateDir:       stateDir,
	})
	info := &UpdateInfo{Version: "0.2.0", URL: url, SHA256: sum, Size: size}
	if err := u.Apply(context.Background(), info); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rec, err := readJournal(journalPath(stateDir))
	if err != nil || rec == nil {
		t.Fatalf("read journal: %v, %v", rec, err)
	}
	if rec.FromBuild != version.Identity() {
		t.Fatalf("journal FromBuild = %+v, want the build that applied %+v", rec.FromBuild, version.Identity())
	}
	if got := buildNumberOf(rec.FromBuild); got != "202610281000" {
		t.Fatalf("the applying build's number = %q, want the stamped minute", got)
	}
}
