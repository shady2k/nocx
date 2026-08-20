package commandnames

import (
	"testing"

	"github.com/shady2k/nocx/internal/ssh"
)

// The discovery scripts fit the bound the transport enforces, with room to
// spare (nocx-e4ir3).
//
// internal/ssh refuses a remote command at or above ssh.MaxRemoteCommandLen
// before it is sent. These two scripts are remote commands, and they are the
// longest nocx emits after the carrier: measured 785 and 430 bytes against
// 1024. Nobody would learn that from here — a script that grew past the bound
// would fail at RUNTIME, as a discovery that stops working on every host at
// once, with the cause a constant in another package.
//
// The measured size is RECORDED rather than bounded by a margin somebody
// invented, which is the answer this repo already uses for exactly this
// question (publisher_measure_test.go: "the measured maximum is still"). A
// margin constant is a guess about how much growth is acceptable; a recorded
// measurement is a fact, and any growth at all has to walk past it. That
// matters here because the headroom is genuinely thin — 785 of 1024 for the
// probe — and thin headroom that nobody is watching is how the 92 KiB command
// that started all of this got to 75% of its own cap.
//
// The ssh import is test-only on purpose. internal/commandnames does not
// depend on the transport and must not start to; the number is read from its
// owner rather than copied, which is what stops this assertion from being a
// second declaration of the bound.
func TestTheDiscoveryScriptsFitTheRemoteCommandBound(t *testing.T) {
	// WHEN THIS FAILS: you changed a discovery script. Decide, then update the
	// number. Growing toward the bound is a decision about every host nocx
	// will ever probe — past it, discovery stops working everywhere at once,
	// and the cause is a constant in another package.
	recorded := map[string]int{"probe": 785, "scan": 430}

	nonce, err := newNonce()
	if err != nil {
		t.Fatalf("newNonce: %v", err)
	}
	for _, tc := range []struct {
		name   string
		script string
	}{
		{"probe", probeScript},
		{"scan", scanScript},
	} {
		cmd := remoteCommand(tc.script, nonce)
		if len(cmd) >= ssh.MaxRemoteCommandLen {
			t.Errorf("%s is %d bytes, at or over the %d-byte bound: the transport refuses it "+
				"before sending, and discovery stops working on every host",
				tc.name, len(cmd), ssh.MaxRemoteCommandLen)
			continue
		}
		if len(cmd) != recorded[tc.name] {
			t.Errorf("%s is %d bytes, recorded %d (%d of the %d-byte bound left). "+
				"Update the recorded number in the same commit as the script, so the size is "+
				"a decision somebody took rather than a drift nobody saw",
				tc.name, len(cmd), recorded[tc.name], ssh.MaxRemoteCommandLen-len(cmd), ssh.MaxRemoteCommandLen)
		}
	}
}
