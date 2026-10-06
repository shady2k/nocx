//go:build linux

package session

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/sandbox"
	"golang.org/x/sys/unix"
)

type linuxObserverTestSink struct{ status atomic.Value }

func (s *linuxObserverTestSink) Observe(sandbox.DiagnosticObservation)     {}
func (s *linuxObserverTestSink) Drop(uint64)                               {}
func (s *linuxObserverTestSink) SetObserver(status sandbox.ObserverStatus) { s.status.Store(status) }

func TestLinuxObserverListenerFailureIsFatalOnce(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var fatals atomic.Int32
	fatalDone := make(chan struct{})
	sink := &linuxObserverTestSink{}
	observer := startNativeObserver(nativeObserverConfig{PID: os.Getpid(), Listener: reader, Sink: sink, Fatal: func() { fatals.Add(1); close(fatalDone) }})
	select {
	case <-fatalDone:
	case <-time.After(2 * time.Second):
		t.Fatal("listener failure did not terminate the affected process")
	}
	observer.Close()
	if got := fatals.Load(); got != 1 {
		t.Fatalf("listener failure called Fatal %d times, want 1", got)
	}
	if got := sink.status.Load(); got != sandbox.ObserverFailed {
		t.Fatalf("listener failure status = %v", got)
	}
}

func TestLinuxObserverInvalidPointerCannotBecomeKnownPath(t *testing.T) {
	observer := &linuxNativeObserver{}
	notification := seccompNotif{PID: uint32(os.Getpid())} //nolint:gosec // Linux PIDs are positive signed C ints.
	notification.Data.Nr = int32(unix.SYS_OPENAT)
	notification.Data.Args[1] = 1
	notification.Data.Args[2] = unix.O_RDWR
	obs := observer.observation(notification)
	if obs.PathKnown || obs.Path != "" || obs.Executable != "" || obs.Access != sandbox.DiagnosticUnknown || obs.Precision != sandbox.PrecisionAttempted || obs.Source != sandbox.DiagnosticLinuxSeccomp {
		t.Fatalf("invalid tracee pointer fabricated data: %+v", obs)
	}
}
