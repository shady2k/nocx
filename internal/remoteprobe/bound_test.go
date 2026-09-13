package remoteprobe_test

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/ssh"
)

// The probe commands fit the bound this package declares, with the size of each
// RECORDED rather than bounded by a margin somebody invented (nocx-e4ir3
// bought that rule; nocx-50w7p.9 moved the commands and kept it).
//
// A margin constant is a guess about how much growth is acceptable; a recorded
// measurement is a fact, and any growth at all has to walk past it. That
// matters here because the headroom is genuinely thin — the enumeration probe
// sits within a few hundred bytes of its neighbours and the completion script
// is the largest thing nocx sends anywhere — and thin headroom that nobody is
// watching is how the 92 KiB integration command that started all of this got
// to 75% of its own cap.
//
// WHEN THIS FAILS: you changed a probe (or a script). Decide, then update the
// number in the same commit. Growing toward the bound is a decision about every
// host nocx will ever probe.
func TestEveryProbeCommandFitsTheBound(t *testing.T) {
	const nonce = "0123456789abcdef0123456789abcdef"
	recorded := map[string]int{
		"uname":                 11,
		"home":                  10,
		"ports.ss":              81,
		"ports.netstat":         74,
		"ports.busybox-netstat": 73,
		"ports.lsof":            88,
		"ports.sockstat":        75,
		// The two enumeration commands carry the heredoc framing this package's
		// builder adds around the script, so they are bigger than the scripts
		// alone were; the recorded number is the WHOLE command, because that is
		// what the bound is applied to.
		"command-names.probe": 832,
		"command-names.scan":  477,
		"completion":          6309,
	}

	cases := []struct {
		name    string
		command string
	}{
		{"uname", remoteprobe.UnameCommand},
		{"home", remoteprobe.HomeCommand},
	}
	for _, phase := range []remoteprobe.CommandNamesPhase{remoteprobe.CommandNamesProbe, remoteprobe.CommandNamesScan} {
		cmd, ok := remoteprobe.CommandNamesCommand(phase, nonce)
		if !ok {
			t.Fatalf("phase %q has no command", phase)
		}
		cases = append(cases, struct {
			name    string
			command string
		}{"command-names." + string(phase), cmd})
	}
	cases = append(cases, struct {
		name    string
		command string
	}{"completion", remoteprobe.CompletionCommand("/home/somebody/a/long/path", "git commit -m 'a realistic line'", 20, 50, nonce)})

	for _, tc := range cases {
		if len(tc.command) >= remoteprobe.MaxCommandLen {
			t.Errorf("%s is %d bytes, at or over the %d-byte bound: the helper refuses it before "+
				"sending, and the probe stops working on every host at once",
				tc.name, len(tc.command), remoteprobe.MaxCommandLen)
			continue
		}
		if want := recorded[tc.name]; want != 0 && len(tc.command) != want {
			t.Errorf("%s is %d bytes, recorded %d (%d of the %d-byte bound left). Update the recorded "+
				"number in the same commit as the change, so the size is a decision somebody took "+
				"rather than a drift nobody saw",
				tc.name, len(tc.command), want, remoteprobe.MaxCommandLen-len(tc.command), remoteprobe.MaxCommandLen)
		}
	}
}

// TestTheCompletionProbeDoesNotFitTheCarrierBound records the fact behind this
// package's choice of its own bound, so nobody re-points the probes at
// internal/ssh's.
//
// ssh.MaxRemoteCommandLen is the LAUNCHER CARRIER's bound — one number in three
// packages, kept equal by internal/app's TestTheBoundOnARemoteCommandIsOneNumber
// — and it is deliberately small because a carrier is payload-free. The
// completion probe is a fixed 6 KiB script plus quoted arguments, so a probe
// held to the carrier's number cannot run AT ALL: that was the state of SSH
// completion until this task moved the commands, where the command was composed,
// handed to the transport, and refused at 1024 bytes with the cause a constant
// in another package.
//
// It is asserted rather than described because the alternative — "the two
// numbers are probably different enough" — is what let the defect live: a
// reviewer who believes probes are small will re-point them at the small bound.
func TestTheCompletionProbeDoesNotFitTheCarrierBound(t *testing.T) {
	cmd := remoteprobe.CompletionCommand("/tmp", "ls x", 4, 50, "n")
	if len(cmd) < ssh.MaxRemoteCommandLen {
		t.Fatalf("the completion probe is %d bytes, under the carrier bound %d — if the script shrank enough "+
			"for this to be true, decide again which bound the probes are held to rather than deleting this test",
			len(cmd), ssh.MaxRemoteCommandLen)
	}
	if !strings.Contains(cmd, "NOCXEOF_") {
		t.Fatal("the completion command no longer carries its heredoc delimiter")
	}
}
