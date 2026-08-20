package lifecyclechannel

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy on this machine") }

// The local channel's identifiers are refused the same way the remote's are
// (nocx-s16k8). Same function, same hole, and the two adapters bind against
// the same kernel — a shared transport id there is a session authenticating
// against another session's domains.
func TestNew_AFailedRandomReadCreatesNoAdapter(t *testing.T) {
	prev := randReader
	randReader = failingReader{}
	defer func() { randReader = prev }()

	a, child, err := New(log.NewSlogAdapter(nil), newTestKernel())
	if err == nil {
		_ = a.Close()
		_ = child.Close()
		t.Fatal("New returned an adapter whose identifiers could not be minted")
	}
	if !strings.Contains(err.Error(), "randomness") {
		t.Errorf("the error does not name the cause: %v", err)
	}
	if a != nil || child != nil {
		t.Error("a refused construction returned an adapter or a descriptor anyway")
	}
}

// The listener half of the same package takes the same refusal.
func TestNewListener_AFailedRandomReadCreatesNoListener(t *testing.T) {
	prev := randReader
	randReader = failingReader{}
	defer func() { randReader = prev }()

	l, err := NewListener(log.NewSlogAdapter(nil), newTestKernel())
	if err == nil {
		_ = l.Close()
		t.Fatal("NewListener returned a listener whose transport id could not be minted")
	}
	if l != nil {
		t.Error("a refused construction returned a listener anyway")
	}
}
