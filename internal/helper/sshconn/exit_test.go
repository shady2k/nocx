//go:build nocx_local_ssh

package sshconn

// The one translation of a remote process's exit, asserted on the errors that
// are NOT an exit: absence of evidence is not a status, and that is the rule a
// second owner gets wrong first (a nil wait reads as a clean exit somewhere
// downstream).
//
// The positive case — a real exit-status request arriving from a real server,
// read as the status the peer sent — is proven where the server already exists:
// internal/helper/sshsvc's probe test sends 127 and asserts the answer, and its
// lane test reads a bridge's exit status through this same function.

import (
	"errors"
	"fmt"
	"io"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestExitAnswersAStatusOnlyWhenThePeerReportedOne(t *testing.T) {
	status, ok := Exit(nil)
	if !ok || status != 0 {
		t.Fatalf("a clean wait reads (%d, %v), want (0, true)", status, ok)
	}

	cases := []struct {
		name string
		err  error
	}{
		{"the channel closed with no exit-status request", &gossh.ExitMissingError{}},
		{"the connection ended", io.EOF},
		{"the library's absence wrapped by our own error", fmt.Errorf("lane: %w", &gossh.ExitMissingError{})},
		{"an unrelated error", errors.New("ssh: the channel was closed")},
	}
	for _, c := range cases {
		status, ok := Exit(c.err)
		if ok {
			t.Errorf("%s: reads (%d, true); a status nobody reported must not become an exit", c.name, status)
		}
	}
}
