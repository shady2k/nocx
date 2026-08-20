package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/ssh/mux"
)

// The bound on a remote command is ONE number, declared in three packages
// because AD-8 forbids the imports that would make it one symbol (nocx-e4ir3).
//
// internal/ssh must not depend on internal/shellintegration — that is why the
// composition root adapts between two identically-named RemoteLauncher
// vocabularies (nocx-ei04, app.go) — and internal/ssh/mux is a leaf that
// depends on nothing of ours. So the number cannot be shared by import, and
// three copies of a number is exactly the shape that drifts: they agree
// everywhere anybody looks until the day one of them is raised alone.
//
// This is the composition root: the one place that sees all three. Raising any
// one of them by itself goes red here, which is what makes them one number
// rather than three that currently match.
//
// Two of the three are guards at the point of no return — ssh refuses before
// session.Start and before discovery's Run, mux refuses before it dials the
// control socket. The third is the producer's own contract. The guards are
// what makes a ~92 KiB command impossible rather than merely unintended.
func TestTheBoundOnARemoteCommandIsOneNumber(t *testing.T) {
	if ssh.MaxRemoteCommandLen != shellintegration.MaxCarrierLen {
		t.Errorf("ssh.MaxRemoteCommandLen = %d, shellintegration.MaxCarrierLen = %d — "+
			"the transport would refuse a command the producer considers legal, or admit one it does not",
			ssh.MaxRemoteCommandLen, shellintegration.MaxCarrierLen)
	}
	if mux.MaxCommandLen != shellintegration.MaxCarrierLen {
		t.Errorf("mux.MaxCommandLen = %d, shellintegration.MaxCarrierLen = %d — "+
			"the typed path and the managed path would disagree about the same command",
			mux.MaxCommandLen, shellintegration.MaxCarrierLen)
	}
}
