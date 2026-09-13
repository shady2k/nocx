package app

// THE LANE: which process on this machine may tell the tool endpoint which pane
// a connection is for (nocx-50w7p.16).
//
// A forwarded connection arrives from a process with no pane of its own — the
// helper — and everything the endpoint learns about the pane comes from what
// that process says. So "who may say it" is the whole of the trust, and the
// answer is the kernel's: the peer of this coordinator's OWN connection to this
// machine's helper is the one process allowed to name a pane.
//
// # Why the pid is read and not remembered
//
// It is read from the connection the coordinator is holding right now, so the
// fact is only ever as old as the socket it came from. A helper that died and
// was replaced is a different pid — or no live connection at all, which is the
// same refusal. A pid kept as state would need a start time beside it to mean
// anything, because a pid alone is a number the kernel reuses; a pid read from
// a live socket does not, because the socket cannot outlive the process at its
// other end.
//
// # What this is not
//
// It is not a defence against a same-uid process that can ptrace the helper or
// read its memory. ADR-0058's ceiling stands: an actor at this uid is not
// something a pid comparison separates. What it separates is a process that
// merely knows the path of the tool socket, which is every other process on the
// machine, from the one the coordinator itself dialed.

import (
	"github.com/shady2k/nocx/internal/toolendpoint"
)

// ToolLane answers whether an accepted peer is this machine's helper daemon:
// the process at the other end of the coordinator's own connection to it.
//
// It is the predicate the tool endpoint asks about every accepted connection,
// and a false answer is not a refusal — a connection from anywhere else is
// admitted, or refused, by the kernel rule it always was. What a false answer
// costs is the PANE RECORD: a connection the helper did not bring may not name
// a pane, so it cannot be admitted as one.
func (a *App) ToolLane(peer toolendpoint.Peer) bool {
	if a == nil || a.localHelper == nil {
		return false
	}
	pid, live := a.localHelper.helperDaemonPID()
	if !live {
		return false
	}
	return laneMatches(peer, pid)
}

// laneMatches is the comparison itself, kept separate from where the pid comes
// from so that it can be asserted without a helper process: the peer's pid is
// the kernel's stamp on the connection being admitted, and lanePID is the
// kernel's stamp on the coordinator's own connection to its helper.
func laneMatches(peer toolendpoint.Peer, lanePID int) bool {
	return lanePID > 0 && peer.PID == lanePID
}

// helperDaemonPID answers the pid at the other end of this coordinator's live
// connection to this machine's helper, or false when there is none.
//
// No connection means no lane, and no lane means nobody may name a pane: a
// coordinator that has never opened a pane has no helper to have forwarded one,
// and inventing an answer here would be inventing the very fact this exists to
// check.
func (o *localHelperOpener) helperDaemonPID() (int, bool) {
	o.mu.Lock()
	client := o.client
	o.mu.Unlock()
	if client == nil {
		return 0, false
	}
	return client.PeerPID()
}
