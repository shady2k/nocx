//go:build !nocx_local_ssh

package main

// The deployed build's answer, asserted rather than assumed: a helper built
// without nocx_local_ssh has no ssh service, so an ssh op reaches the host's
// dispatcher and is refused with `unknown_service` — a sentence about the
// BUILD, which a coordinator can act on (reinstall, or do not ask this machine
// to dial), rather than a failure it has to guess at.
//
// The alternative state is the one this test exists to catch: a seam that
// registered SOMETHING without an ssh client behind it would answer an op it
// cannot serve, which is a helper claiming a capability its bytes do not have.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestTheDeployedHelperHasNoSSHService(t *testing.T) {
	seam, err := holdSSHClient(discardLog())
	if err != nil {
		t.Fatalf("holdSSHClient: %v", err)
	}
	defer seam.release()

	c := standUpHelper(t, seam)

	var out proto.ProbeResult
	err = c.Call(context.Background(), proto.ServiceSSH, proto.OpProbe, proto.ProbeParams{
		Host: "example.invalid", Port: 22, User: "nobody",
		Identity: proto.SSHIdentity{
			Credential: proto.SSHCredential{Ref: "cred"},
			Auth:       proto.SSHAuthPassword,
		},
	}, &out)
	if err == nil {
		t.Fatalf("an ssh probe was answered by a build with no ssh service: %+v", out)
	}
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("probe error = %v, want a helper refusal", err)
	}
	if refusal.Code != proto.ErrCodeUnknownService {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, proto.ErrCodeUnknownService)
	}
}
