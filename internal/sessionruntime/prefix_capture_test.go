package sessionruntime

import (
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
)

type prefixRowCounter struct {
	emulator.Terminal
	reads int
}

func (e *prefixRowCounter) Row(y int) (emulator.Row, error) {
	e.reads++
	return e.Terminal.Row(y)
}

func TestUnpinnedPrefixRefreshDoesNotCaptureRows(t *testing.T) {
	var counter *prefixRowCounter
	s, _, _ := realRuntime(t, func(cfg *Config) {
		counter = &prefixRowCounter{Terminal: cfg.Emulator}
		cfg.Emulator = counter
	})
	if err := s.Ingest([]byte("visible output")); err != nil {
		t.Fatal(err)
	}
	counter.reads = 0
	s.mu.Lock()
	s.refreshOutputPrefixesLocked()
	s.mu.Unlock()
	if counter.reads != 0 {
		t.Fatalf("unpinned refresh captured %d rows", counter.reads)
	}
}
