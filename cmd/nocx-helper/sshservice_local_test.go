//go:build nocx_local_ssh

package main

// The local build's answer, asserted rather than assumed: a helper built WITH
// nocx_local_ssh has an ssh service on every connection, so an ssh op reaches
// it rather than the dispatcher's `unknown_service`.
//
// The pair of assertions is the point. Alone, either one passes for the wrong
// reason — a build with no service passes the deployed half, and a build whose
// seam registered something useless would pass a weaker "not unknown_service"
// check — so this half asserts what the service DOES: it validates its params
// (a malformed probe is refused as such) and it dials (a probe to a port
// nothing listens on comes back classified).

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestTheLocalHelperServesTheSSHService(t *testing.T) {
	seam, err := holdSSHClient(discardLog())
	if err != nil {
		t.Fatalf("holdSSHClient: %v", err)
	}
	defer seam.release()

	c := standUpHelper(t, seam)
	ref := proto.SSHIdentity{
		Credential: proto.SSHCredential{Ref: "cred"},
		Auth:       proto.SSHAuthPassword,
	}

	// A probe the service itself refuses, which only a registered service can
	// produce: no host.
	var out proto.ProbeResult
	err = c.Call(context.Background(), proto.ServiceSSH, proto.OpProbe, proto.ProbeParams{
		Port: 22, User: "nobody", Identity: ref,
	}, &out)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("malformed probe error = %v (%+v), want a refusal from the service", err, out)
	}
	if refusal.Code != proto.ErrCodeBadParams {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, proto.ErrCodeBadParams)
	}

	// A probe that really dials, through the daemon's own ssh client: the
	// classified outcome can only come from the dial seam this build links.
	err = c.Call(context.Background(), proto.ServiceSSH, proto.OpProbe, proto.ProbeParams{
		Host: "127.0.0.1", Port: 1, User: "nobody", Identity: ref,
	}, &out)
	if err != nil {
		t.Fatalf("probe to a closed port: %v", err)
	}
	if out.Outcome != proto.ProbeUnreachable {
		t.Fatalf("outcome = %q (%s), want unreachable", out.Outcome, out.Detail)
	}
}
