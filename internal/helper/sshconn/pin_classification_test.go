//go:build nocx_local_ssh

package sshconn

import (
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

func TestDialFailureWrappedHostKeyPinMismatch(t *testing.T) {
	err := fmt.Errorf("dial: %w", &ssh.ErrAuthFailed{Err: fmt.Errorf("handshake: %w", &ssh.ErrHostKeyPinMismatch{Fingerprint: "offered", Expected: "approved"})})
	want := hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseHostKeyChanged}
	if got := dialFailure(err); got != want {
		t.Fatalf("wrapped pin mismatch=%v, want %v", got, want)
	}
}
