package session

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/ssh"
)

// TestRemoteSession_CarriesPasswordAskAcrossConfigBoundary pins the field
// that dies between the resolver and the dial: the session path decomposes
// a resolver-built ConnectConfig into options and rebuilds it, and a field
// not forwarded here is silently dropped — the e2e caught exactly that
// (the open failed with ErrNoAuthMethod while the probe, which uses the
// config directly, had the ask). ConnectionName rides the same path so the
// prompt can name the connection (nocx-s8jn).
func TestRemoteSession_CarriesPasswordAskAcrossConfigBoundary(t *testing.T) {
	asker := &fakePasswordRequester{}
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{reason: ssh.ReasonNone}, Config{
		Host: "web.example.com",
		Remote: &ssh.ConnectConfig{
			User:              "deploy",
			AuthMode:          "password",
			ConnectionName:    "prod-web",
			PasswordRequester: asker,
		},
	})

	// Rebuild the config from the session's options and check the ask
	// survived the round trip.
	rebuilt := sshOptionsOf(sess)
	if rebuilt.ConnectionName != "prod-web" {
		t.Errorf("connection name lost across the config boundary: %q", rebuilt.ConnectionName)
	}
	if rebuilt.PasswordRequester == nil {
		t.Fatal("password requester lost across the config boundary — the ask would never fire")
	}
	if _, ok := rebuilt.PasswordRequester.(*fakePasswordRequester); !ok {
		t.Errorf("password requester = %T, want the wired asker", rebuilt.PasswordRequester)
	}
}

// fakePasswordRequester satisfies ssh.ConnectionPasswordRequester for the
// boundary test; the ask itself is tested in internal/ssh.
type fakePasswordRequester struct{}

func (f *fakePasswordRequester) RequestConnectionPassword(ctx context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	return ssh.PasswordAnswer{}, nil
}
