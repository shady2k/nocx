package session

import (
	"os"

	"github.com/shady2k/nocx/internal/sandbox"
)

// nativeObserverConfig is local process metadata, not a coordinator capability.
// A collector takes ownership of Listener and cannot broaden Policy.
type nativeObserverConfig struct {
	PID      int
	Done     <-chan struct{}
	Policy   sandbox.Policy
	Listener *os.File
	Nonce    string
	Sink     sandbox.DiagnosticSink
	Fatal    func()
}

// nativeObserver outlives renderer/coordinator connections and ends with the
// helper-owned process. Close joins its reader and never invokes Fatal.
type nativeObserver interface {
	Close()
}
