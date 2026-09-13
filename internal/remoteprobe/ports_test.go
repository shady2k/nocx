package remoteprobe

// The port ladder, checked from INSIDE where the table is: the closed set is a
// value this package owns, so the proof that every rung it declares is a
// well-formed, bounded command belongs here rather than an external test that
// could only reach the exported lookup.
//
// Two things are asserted, and the second is the one a maintainer needs: every
// rung HAS a command, and the table covers every name this package exports. A
// rung added to the names without a command (or the reverse) is a probe that
// half exists — the helper would refuse it as bad params and the ladder would
// keep selecting it — and it fails here instead of on somebody's host.

import "testing"

// recordedRungSizes are the measured lengths of the ladder's commands when this
// test was written.
var recordedRungSizes = map[PortProbe]int{
	PortSS:             81,
	PortNetstat:        74,
	PortBusyboxNetstat: 73,
	PortLsof:           88,
	PortSockstat:       75,
}

func TestThePortLadderCoversEveryNameItExports(t *testing.T) {
	names := []PortProbe{PortSS, PortNetstat, PortBusyboxNetstat, PortLsof, PortSockstat}
	if len(portCommands) != len(names) {
		t.Fatalf("the ladder holds %d rungs and this package exports %d names", len(portCommands), len(names))
	}
	for _, name := range names {
		command, ok := PortCommand(name)
		if !ok {
			t.Errorf("the name %q has no rung in the ladder: the helper would refuse it as bad params "+
				"and the caller's ladder would keep selecting it", name)
			continue
		}
		if command == "" {
			t.Errorf("the rung %q has an empty command", name)
		}
		// The size is RECORDED rather than bounded by a margin somebody
		// invented (nocx-e4ir3's rule, applied to the probes): growth has to
		// walk past a number a person wrote, and the bound it is checked
		// against is this package's own — not the transport's session-carrier
		// number, which is a different question (MaxCommandLen's note, and
		// bound_test.go's carrier contrast, both say why).
		if want := recordedRungSizes[name]; len(command) != want {
			t.Errorf("the rung %q is %d bytes, recorded %d. Update the recorded number in the same "+
				"commit as the change, so the size is a decision somebody took rather than a drift "+
				"nobody saw", name, len(command), want)
		}
		if len(command) >= MaxCommandLen {
			t.Errorf("the rung %q is %d bytes, at or over the %d-byte probe bound", name, len(command), MaxCommandLen)
		}
	}
	if len(portCommands) != len(names) {
		t.Fatalf("ladder has %d rungs, want %d", len(portCommands), len(names))
	}
}

// TestThePortNamesAreTheOnlyMembers keeps the ladder's ORDER explicit: it is the
// capability-selection order (ss → netstat → busybox netstat → lsof → sockstat),
// and a reordering is a behaviour change rather than a tidy-up.
func TestThePortNamesAreTheOnlyMembers(t *testing.T) {
	want := []PortProbe{PortSS, PortNetstat, PortBusyboxNetstat, PortLsof, PortSockstat}
	if len(portCommands) != len(want) {
		t.Fatalf("the ladder holds %d rungs, want %d", len(portCommands), len(want))
	}
	for i, rung := range portCommands {
		if rung.Probe != want[i] {
			t.Errorf("rung %d is %q, want %q — the ladder's order is the capability-selection order", i, rung.Probe, want[i])
		}
	}
}
