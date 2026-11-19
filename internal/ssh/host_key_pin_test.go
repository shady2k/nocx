package ssh

import "testing"

func TestWithHostKeyPin(t *testing.T) {
	var config ConnectConfig
	WithHostKeyPin("SHA256:approved")(&config)
	if config.HostKeyFingerprint != "SHA256:approved" {
		t.Fatalf("host key pin = %q", config.HostKeyFingerprint)
	}
	WithHostKeyPin("")(&config)
	if config.HostKeyFingerprint != "" {
		t.Fatal("an unpinned acquisition retained the prior expectation")
	}
}
