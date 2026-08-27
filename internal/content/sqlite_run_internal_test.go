package content

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRunWaitsForAnAcceptedWriteAfterCancel pins the half of run's contract
// that the writer goroutine cannot defend on its own: once a request has been
// accepted onto writeCh, fn is executing, and the caller owns nothing it may
// return without.
//
// The scenario is the one that bit in production (nocx-s04ws): a caller's
// context is cancelled mid-transaction. fn runs to completion — including its
// commit — and the outcome that must reach the caller is fn's, not the
// context's.
//
// The ordering is made by channels alone, never by a duration: fn announces
// that it is running, the test cancels, and only then releases fn. So at the
// moment of the cancel the ONLY thing run can be waiting on is an answer it
// has already been promised.
func TestRunWaitsForAnAcceptedWriteAfterCancel(t *testing.T) {
	s := &sqliteContent{
		writeCh: make(chan writeReq),
		stop:    make(chan struct{}),
		// No file is ever created here: run's enforceFileModes stats a path
		// that does not exist and does nothing, which is what we want — this
		// test is about the handoff, not about SQLite.
		path: filepath.Join(t.TempDir(), "content.db"),
	}
	s.wg.Add(1)
	go s.writer()
	t.Cleanup(func() {
		close(s.stop)
		s.wg.Wait()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	errCh := make(chan error, 1)

	// observed is written on the writer goroutine and read on this one after
	// run returns. With run honouring its promise those two are ordered — the
	// answer travels fn → writer → run → here. Abandon the request and they
	// are concurrent instead, which is the data race this test exists to
	// catch under -race, quite apart from what it asserts below.
	var observed string

	go func() {
		err := s.run(ctx, func(context.Context) error {
			close(entered)
			<-release
			select {
			case <-returned:
				observed = "run returned while fn was still executing"
			default:
				observed = "committed"
			}
			return nil
		})
		close(returned)
		errCh <- err
	}()

	<-entered // accepted, and fn is running
	cancel()  // the caller goes away mid-write
	close(release)

	if err := <-errCh; err != nil {
		t.Fatalf("run reported %v for a write that committed; a committed outcome must never be replaced by a proxy error", err)
	}
	if observed != "committed" {
		t.Fatalf("observed %q: run must not return until the writer has answered", observed)
	}
}
