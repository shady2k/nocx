package session

import (
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

func TestAdopt_PreservesSSHOptionsUntilClose(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	remote := &ssh.ConnectConfig{
		User:    "e2e",
		Port:    2222,
		KeyFile: "/tmp/e2e-key",
	}
	sess, err := reg.Adopt(Config{
		Kind:   KindRemote,
		Host:   "remote.example",
		Remote: remote,
		Cols:   80,
		Rows:   24,
	}, ID("0123456789abcdef0123456789abcdef"), &reasonChannel{})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	// Read the options through the public seam while the adopted session is
	// alive. This is the interval the helper-backed probe relies on: adoption
	// through the session's close, not merely the constructor's return.
	got := &ssh.ConnectConfig{}
	for _, opt := range sess.SSHOptions() {
		opt(got)
	}
	if got.User != remote.User || got.Port != remote.Port || got.KeyFile != remote.KeyFile {
		t.Fatalf("adopted SSH options = user %q, port %d, key %q; want %q, %d, %q",
			got.User, got.Port, got.KeyFile, remote.User, remote.Port, remote.KeyFile)
	}

	// The closing end of the interval. The name of this test promises a span,
	// and a span needs both ends measured: a probe can be issued while the
	// session is being torn down, and options cleared on close would be the
	// same defect arriving later.
	if err := reg.Close(sess.ID()); err != nil {
		t.Fatalf("Close adopted session: %v", err)
	}
	after := &ssh.ConnectConfig{}
	for _, opt := range sess.SSHOptions() {
		opt(after)
	}
	if after.User != remote.User || after.Port != remote.Port || after.KeyFile != remote.KeyFile {
		t.Fatalf("after close, adopted SSH options = user %q, port %d, key %q; want them unchanged (%q, %d, %q)",
			after.User, after.Port, after.KeyFile, remote.User, remote.Port, remote.KeyFile)
	}
}
