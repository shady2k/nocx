package app

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/bootstrapstream"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// What a stream that stopped answering looks like from the driver: the loader
// announced itself, and then every read raises the error the PRODUCTION
// readers raise — internal/ssh's over a channel, internal/session's over a
// local pty, both from internal/bootstrapstream.
//
// That is the whole point of driving it from here. The two tests below are in
// the composition root because it is the one package that holds the real
// driver and the real vocabulary at once; the same assertion inside
// shellintegration could only be made against a fake returning
// shellintegration's own sentinel, which is the arrangement that let a
// permanently-false errors.Is stay green in three packages at the same time.
type muteFar struct {
	mu     sync.Mutex
	step   int
	writes [][]byte
	err    error
}

func (f *muteFar) ReadLine(_ context.Context, _ time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.step++
	if f.step == 1 {
		return shellintegration.LoaderReadyToken, nil
	}
	return "", f.err
}

func (f *muteFar) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (f *muteFar) sawAbort() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.writes {
		if bytes.Equal(w, shellintegration.AbortFrame()) {
			return true
		}
	}
	return false
}

func driveTo(t *testing.T, err error) (ssh.RefusalReason, *muteFar) {
	t.Helper()
	_, run, _, _ := prepareMint(t)
	far := &muteFar{err: err}
	done := make(chan ssh.RefusalReason, 1)
	go func() { done <- run(context.Background(), far) }()
	select {
	case reason := <-done:
		return reason, far
	case <-time.After(15 * time.Second):
		t.Fatal("the bootstrap never named a terminal outcome")
		return ssh.ReasonUnknown, far
	}
}

// A far side that goes quiet has TIMED OUT, and the product says so.
//
// It used to say the connection ended, on every deadline, on every transport:
// the driver tested errors.Is against a sentinel of its own while both readers
// returned one of theirs, so the deadline branch was unreachable and the
// stream-ended branch caught everything. bootstrap-timeout existed in the
// vocabulary, in the contract and in the card's wording, and no session could
// reach it.
func TestBootstrap_ASilentFarSideIsATimeout(t *testing.T) {
	reason, far := driveTo(t, bootstrapstream.ErrDeadline)
	if reason != ssh.ReasonBootstrapTimeout {
		t.Errorf("reason = %q, want %q — the stream is open and the far side stopped answering",
			reason, ssh.ReasonBootstrapTimeout)
	}
	// The other half of the same branch, and the half a user feels: the far
	// side is blocked on a header read, and the abort is what turns that hang
	// into a prompt they can type at.
	if !far.sawAbort() {
		t.Error("no abort frame reached the far side; it is left blocked on a header read " +
			"until something else unwinds it")
	}
}

// A far side that writes more than the reader's bound with no line ending has
// not ended the connection either — it has written output that is not this
// protocol, which is what bootstrap-protocol already means.
func TestBootstrap_AnOverBoundLineIsNotAConnectionThatEnded(t *testing.T) {
	reason, _ := driveTo(t, bootstrapstream.ErrLineTooLong)
	if reason != ssh.ReasonBootstrapProtocol {
		t.Errorf("reason = %q, want %q — nothing ended; %d KB arrived with no line ending",
			reason, ssh.ReasonBootstrapProtocol, 4)
	}
}
