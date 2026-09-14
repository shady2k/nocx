//go:build nocx_local_ssh

package session_test

// An ssh pane, end to end: the helper opens a shell channel on a REAL ssh
// server, runs the session's runtime beside it, and drives the launcher on the
// far side (nocx-50w7p.4).
//
// # What each test waits on, and why nothing here waits on a clock
//
// Every wait in this file is on an EVENT the far side or the helper produced —
// the far side's own bytes, a request that arrived, an exit notification — and
// paneWait is only what turns a far side that never speaks into a failure with a
// sentence. A test that slept would pass on a fast machine and fail on a slow
// one, which is the same defect as a test asserting a duration, one step
// earlier.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/shellintegration"
)

// decReplyProgram is the far-side program the DSR test runs.
//
// Both details of how it reads are load-bearing:
//
//   - `stty -icanon -echo` FIRST, because a cursor-position report carries no
//     newline. A pty in canonical mode holds input until a line delimiter
//     arrives, so a bounded read would block forever on an answer that had
//     already been sent — and a program that hangs proves nothing about whether
//     its terminal answered. `-echo` keeps the reply out of the far side's own
//     output: what the terminal sent is an ANSWER, and a test that also saw it
//     echoed could not tell the two apart.
//   - the captured answer is rendered with `od -An -c` and printed, so the
//     reply the far program READ becomes a line of the far side's output —
//     which is the evidence this test reads, without a file and without
//     attaching anything to the session.
//
// The program prints NOTHING before the query, so the answer is the reply of a
// terminal whose caret is still at home: `ESC [ 1 ; 1 R` in a fresh 80x24
// session. That is the runtime's own committed geometry talking, not a constant
// a stub could produce.
const decReplyProgram = `stty -icanon -echo
printf '\033[6n'
r=$(dd bs=1 count=6 2>/dev/null)
printf 'DSR[%s]\n' "$r" | od -An -c
printf 'DSR-DONE\n'
exit 0
`

// TestTheHelperOpensAShellOnTheFarHostAndTheSessionRuntimeAnswersIt is the
// acceptance for the whole bead, and nobody is ATTACHED at any point: no
// WebSocket, no renderer, no subscriber, no window read. The helper spawns the
// session, the far side asks its terminal a question, and the helper's OWN
// runtime answers it — the owner's invariant that the backend answers the
// program's terminal queries for every session kind, proven for the kind this
// bead adds.
//
// The evidence is the far side's own output: the program read the DEC reply off
// its pty and printed it. Nothing else on that machine can write to a session's
// input, so a program that read a cursor report read it from the only terminal
// it has.
func TestTheHelperOpensAShellOnTheFarHostAndTheSessionRuntimeAnswersIt(t *testing.T) {
	f := newSSHFixture(t, "pw", decReplyProgram)
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	if !entry.IsRemote() {
		t.Fatalf("the session's launch record is not the ssh branch: %+v", entry)
	}
	// A `raw` session adds NOTHING to the far side: no launcher, no exec, no
	// frames — a plain login shell the far host chose.
	if execs := f.execsSeen(); len(execs) != 0 {
		t.Fatalf("a raw session exec'd %q: nothing may be added to the far side in raw mode", execs)
	}
	if shells := f.shellsSeen(); shells != 1 {
		t.Fatalf("the far host was asked for %d shells, want 1", shells)
	}

	// The event: the far program finished, having read its terminal's answer and
	// printed what it read.
	program := f.waitFarOutput(t, "DSR-DONE")
	answer := normaliseOD(program)
	if !strings.Contains(answer, "DSR[033[1;1R]") {
		t.Fatalf("the far program read %q from its terminal, want a cursor report for a caret at home (ESC[1;1R): the session's runtime did not answer it\nfull far-side output: %q",
			answer, program)
	}
	// The same fact from the other end: the bytes that reached the far
	// program's INPUT are exactly the runtime's reply, and nothing else was
	// ever written there. This is the assertion that would fail if the answer
	// had been produced by anything but the session's own terminal.
	if got := f.programInputSeen(); string(got) != "\x1b[1;1R" {
		t.Fatalf("the far program's input received % x, want the runtime's own reply 1b 5b 31 3b 31 52", got)
	}
	// And the pty was requested for the terminal this helper IS: the emulator
	// answers a program's questions, so the terminal type it asks a far host
	// for is the one whose answers it can give.
	if terms := f.termsSeen(); len(terms) == 0 || terms[0] != sshsvc.DefaultTerm {
		t.Fatalf("the far host was asked for terminal %v, want %q", terms, sshsvc.DefaultTerm)
	}
	// The host key was asked about on the connection the REQUEST arrived on,
	// which is the whole reason the helper holds no known_hosts of its own.
	if asked := stand.coord.asked(); !contains(asked, proto.OpVerifyHostKey) {
		t.Fatalf("the helper never asked the coordinator about the host key; it asked %v", asked)
	}
}

// contains reports whether a recorded op list names op.
func contains(ops []string, op string) bool {
	for _, seen := range ops {
		if seen == op {
			return true
		}
	}
	return false
}

// TestTheLauncherAndItsStageOneFramesRunOnTheFarHost is the other half of the
// bead: an INTEGRATED ssh pane. The helper builds the carrier and stage-1 from
// internal/shellintegration, hands the command to the far host as its exec
// request, and drives the frame protocol over the channel it holds.
//
// The evidence is the FAR SIDE'S OWN TOKENS, in order, and each one proves a
// different link:
//
//	LOADER_READY  the carrier reached the far side and the loader ran on a
//	              terminal it took over itself;
//	STAGE_READY   frame 1 arrived, hashed to the digest the COMMAND committed
//	              to, and stage-1 was sourced — so the helper wrote frames that
//	              match the command it built;
//	OUTCOME       the far side named a terminal outcome and left for a native
//	              login shell, which is the fail-open this design promises on a
//	              host with no generation installed.
func TestTheLauncherAndItsStageOneFramesRunOnTheFarHost(t *testing.T) {
	f := newSSHFixture(t, "pw", "")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeAuto))

	// The far host is EXEC'd, not given a plain shell: the carrier is the one
	// remote command a managed session emits.
	exec := f.waitExec(t)
	if !strings.Contains(exec, "nocx-loader") {
		t.Fatalf("the helper exec'd %q on the far host, which is not the launch carrier", exec)
	}
	if !strings.Contains(exec, shellintegration.FrameMagic) {
		t.Fatalf("the carrier %q does not carry the frame protocol's magic", exec)
	}
	f.waitFarOutput(t, shellintegration.LoaderReadyToken)
	f.waitFarOutput(t, shellintegration.StageReadyToken)
	f.waitFarOutput(t, shellintegration.OutcomePrefix)
}

// TestResizeReachesTheFarPTY proves the size travels the whole chain: the
// helper's runtime commits a geometry, the process sends the channel's
// window-change, and the FAR PTY's own ioctl reflects it — read back from the
// pty the fixture is running the command on, not from the request.
func TestResizeReachesTheFarPTY(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'READY\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	f.waitStarted(t)

	var res proto.ResizeResult
	if err := stand.client.Call(context.Background(), proto.ServiceSession, proto.OpResize,
		proto.ResizeParams{Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		}, Cols: 132, Rows: 43}, &res); err != nil {
		t.Fatalf("resize: %v", err)
	}

	cols, rows, ptyCols, ptyRows := f.waitWindowChange(t)
	if cols != 132 || rows != 43 {
		t.Fatalf("the far host was told %dx%d, want 132x43", cols, rows)
	}
	if ptyCols != 132 || ptyRows != 43 {
		t.Fatalf("the far PTY holds %dx%d, want 132x43: the request was sent but never applied", ptyCols, ptyRows)
	}
}

// TestTheRemoteExitStatusIsReported — the helper owns exit status (D3), and for
// an ssh pane that status is the far command's, which arrives on the channel.
func TestTheRemoteExitStatusIsReported(t *testing.T) {
	f := newSSHFixture(t, "pw", "echo LEAVING; exit 7")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))

	exit := stand.exits.waitExit(t, proto.HostSessionID{
		Generation: proto.GenerationID(entry.HostSessionID.Generation),
		Session:    entry.HostSessionID.Session,
	})
	if exit.Status.Code != 7 {
		t.Fatalf("the helper reported exit code %d, want the far command's 7", exit.Status.Code)
	}
	// The entry goes on carrying it: the coordinator that wants it may not exist
	// yet (D5's reconciliation reads exactly this).
	after := stand.entry(t, entry.HostSessionID)
	if after.Exit == nil || after.Exit.Code != 7 {
		t.Fatalf("the inventory entry after the exit is %+v, want the far command's status", after.Exit)
	}
}

// TestAChannelLostMidSessionEndsTheSessionWithAStatus — the far side going away
// is an END, not a hang: the session's process reports an error with no status
// (code -1, which is what SessionExitStatus means by it) and the session stays
// in the inventory carrying it.
//
// This is the case the owner's plan calls out as better than the coordinator's
// old path, where the session simply died with the coordinator.
func TestAChannelLostMidSessionEndsTheSessionWithAStatus(t *testing.T) {
	f := newSSHFixture(t, "pw", "echo VANISHING; exit 3")
	// No exit-status request at all: the channel is closed under the session.
	f.armsSilentEnd()
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))

	// The far command ended — the fixture's own event, not a deadline — and it
	// closed the channel silently, so nothing on this wire ever carried a
	// status.
	f.waitExit(t)
	exit := stand.exits.waitExit(t, proto.HostSessionID{
		Generation: proto.GenerationID(entry.HostSessionID.Generation),
		Session:    entry.HostSessionID.Session,
	})
	if exit.Status.Code != -1 {
		t.Fatalf("a channel lost without a status reported code %d, want -1", exit.Status.Code)
	}
	// Still in the inventory: a session whose process ended keeps its row until
	// somebody closes it.
	after := stand.entry(t, entry.HostSessionID)
	if after.Exit == nil {
		t.Fatal("the session left the inventory when its channel was lost")
	}
}

// TestAnSSHPaneReportsNoOSEvidenceAndNeverAsksTheInspector is the fabricated-pid
// guard, and it asserts the whole interval rather than one field:
//
//   - the entry's `observed` is NULL — "nobody could be asked";
//   - the OS-evidence seam was NEVER given a pid for an ssh session, which is
//     what stops a fabricated zero from being reported as the kernel scheduler;
//   - the launch record is the ssh branch, and the LOCAL record the coordinator
//     reads for a machine's own process carries no pid, no pgid and no shell.
//
// A mutation that dropped the `IsLocal()` gate in session.entry would fail the
// second assertion with the pid it handed over (0), and one that built the
// local launch branch for an ssh session would fail the third.
func TestAnSSHPaneReportsNoOSEvidenceAndNeverAsksTheInspector(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	f.waitStarted(t)

	if entry.Observed != nil {
		t.Fatalf("an ssh session reports OS evidence: %+v", entry.Observed)
	}
	if pids := stand.inspector.asked(); len(pids) != 0 {
		t.Fatalf("the OS-evidence seam was asked about pids %v for a session with no process on this machine", pids)
	}
	if entry.RemoteLaunch == nil {
		t.Fatal("the ssh launch branch is missing from the entry")
	}
	if entry.RemoteLaunch.Host == "" || entry.RemoteLaunch.Port == 0 || entry.RemoteLaunch.User == "" {
		t.Fatalf("the remote launch record is not the resolved destination: %+v", entry.RemoteLaunch)
	}
	if entry.RemoteLaunch.IdentityRef != sshTestRef {
		t.Fatalf("identity reference = %q, want the coordinator's own %q", entry.RemoteLaunch.IdentityRef, sshTestRef)
	}
	// The local branch is ABSENT — not a record of zeros (nocx-s8mfn) — and
	// that is the projection of "this session has no process here": a filled-in
	// record would carry pid 0, which is the kernel's scheduler rather than the
	// process the helper spawned, and the inventory contract that carries this
	// entry requires exactly one branch.
	if entry.Launch != nil {
		t.Fatalf("an ssh session carries a LOCAL launch record: %+v", *entry.Launch)
	}
	if entry.RemoteLaunch.Cwd != "" {
		t.Fatalf("a remote launch record reported a directory (%q) that nobody resolved", entry.RemoteLaunch.Cwd)
	}
}

// TestASignalledSSHPaneTakesTheChannelRequest — the session's own group is the
// channel's command, and a NAMED group is refused by name: a pgid is the other
// kernel's number, and sending the request anyway would signal whatever the
// channel happens to be running.
func TestASignalledSSHPaneTakesTheChannelRequest(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'WAITING\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	entry := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	f.waitStarted(t)

	// Paired success first: zero means the session's own process.
	if err := stand.client.Signal(context.Background(), entry.HostSessionID, 0, 15); err != nil {
		t.Fatalf("signalling the session's own process: %v", err)
	}
	f.waitSignal(t, f.signalled, "a signal request")
	if seen := f.signalsSeen(); len(seen) == 0 || seen[len(seen)-1] != "TERM" {
		t.Fatalf("the far host saw signals %v, want TERM", seen)
	}

	// The refusal: a group this helper cannot address.
	err := stand.client.Signal(context.Background(), entry.HostSessionID, 4242, 15)
	if got := refusalCode(err); got != proto.ErrCodeBadParams {
		t.Fatalf("signalling a far process group answered %v (code %q), want a bad_params refusal", err, got)
	}
	if err == nil || !strings.Contains(err.Error(), "remote process group") {
		t.Fatalf("the refusal does not name what it refused: %v", err)
	}
}

// TestSpawnSSHRefusesByNameWithAPairedSuccess walks the failure classes the
// brief names, and every one of them is paired with the request that DOES work.
//
// # Why each case gets its own far host
//
// AD-4's pool keys a connection by (host, port, user, identity), so a second
// spawn to the same destination rides the FIRST one's authenticated transport:
// no second handshake, no second credential, no second host-key question. A
// case driven through a pooled connection would therefore never reach the
// refusal it thinks it is driving — the sealed vault would never be read from,
// the changed key never verified — and would pass while proving nothing. A
// fresh fixture is a fresh port, which is a fresh pool key, which is a real
// dial.
// TestAPaneReachesAHostBehindAJumpHost is the ROUTE acceptance for a pane: the
// helper dials a bastion, dials the destination THROUGH it, and opens the
// shell on that connection — with each hop authenticated once and its host key
// verified by the coordinator.
//
// The fixture is a real ssh server that proxies `direct-tcpip` channels, which
// is exactly what a jump host is; the assertion that the route was really
// traversed is the bastion's own record of WHICH ADDRESS it was asked to reach,
// beside the connection count on each end. A helper that had dropped the hops
// would dial the destination directly — and would still reach it, because both
// fixtures are on loopback, which is why the bastion's own record is the
// evidence and not the pane's success.
func TestAPaneReachesAHostBehindAJumpHost(t *testing.T) {
	bastion := newSSHFixture(t, "pw", "")
	target := newSSHFixture(t, "pw", "printf 'ROUTED\n'; cat")

	// The scripted coordinator answers one password for every reference it is
	// asked about, which is what lets one principal stand for both hops: each
	// hop asks separately, over the connection its own request arrived on.
	coord := &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: target.fingerprint(),
	}
	stand := newSSHStand(t, target, coord)

	host, port := target.hostPort(t)
	bHost, bPort := bastion.hostPort(t)
	wantTarget := net.JoinHostPort(host, strconv.Itoa(port))
	params := proto.SSHSpawnParams{
		Destination: proto.SSHDestination{
			Host: host, Port: port, User: "test",
			Identity: proto.SSHIdentity{
				Credential: &proto.SSHCredential{Ref: sshTestRef},
				Auth:       proto.SSHAuthPassword,
			},
			// The bastion, with its OWN address, account and credential — a
			// hop is a destination that is not routed any further.
			Jumps: []proto.SSHHop{{
				Host: bHost, Port: bPort, User: "jumper",
				Identity: proto.SSHIdentity{
					Credential: &proto.SSHCredential{Ref: sshTestJumpRef},
					Auth:       proto.SSHAuthPassword,
				},
			}},
		},
		AcceptOnTrust:      true,
		HostKeyFingerprint: target.fingerprint(),
		Cols:               80,
		Rows:               24,
		DesiredMode:        proto.SSHModeRaw,
	}

	entry := stand.mustSpawn(t, params)
	if !entry.IsRemote() {
		t.Fatalf("the session's launch record is not the ssh branch: %+v", entry)
	}
	// The event: the far program ran, on the far side of the bastion.
	if out := target.waitFarOutput(t, "ROUTED"); !strings.Contains(out, "ROUTED") {
		t.Fatalf("the far program's output is %q, want the token it printed", out)
	}

	// ONE connection per hop, counted by the servers themselves: the bastion
	// authenticated once and the destination once. A helper that dialed the
	// destination directly would leave the bastion at zero; one that dialed a
	// fresh route per channel would show more than one on either end.
	if got := bastion.connections(); got != 1 {
		t.Fatalf("the bastion authenticated %d connection(s), want exactly 1", got)
	}
	if got := target.connections(); got != 1 {
		t.Fatalf("the destination authenticated %d connection(s), want exactly 1", got)
	}
	// And the bastion was asked to reach the destination's address, which is
	// what "through the jump host" means.
	if seen := bastion.directTargetsSeen(); len(seen) != 1 || seen[0] != wantTarget {
		t.Fatalf("the bastion was asked to reach %v, want exactly %s", seen, wantTarget)
	}
	if asked := coord.asked(); !contains(asked, proto.OpVerifyHostKey) {
		t.Fatalf("the helper never asked the coordinator about a host key; it asked %v", asked)
	}
	t.Logf("MEASURED bastion %d auth(s) reaching %v, destination %d auth(s)",
		bastion.connections(), bastion.directTargetsSeen(), target.connections())
}

func TestSpawnSSHRefusesByNameWithAPairedSuccess(t *testing.T) {
	// mutate bends the request or the coordinator and answers how to put it
	// back. The refusal runs FIRST, while this destination has no pooled
	// connection; the paired success then runs with the bend undone.
	run := func(t *testing.T, want string, mutate func(p *proto.SSHSpawnParams, c *sshCoordinator) func()) {
		t.Helper()
		f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
		coord := &sshCoordinator{
			password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
		}
		stand := newSSHStand(t, f, coord)
		good := stand.spawnParams(t, proto.SSHModeRaw)

		p := good
		undo := mutate(&p, coord)
		_, err := stand.spawn(t, p)
		if got := refusalCode(err); got != want {
			t.Fatalf("answered %v (code %q), want the %q refusal", err, got, want)
		}
		if undo != nil {
			undo()
		}
		if _, err := stand.spawn(t, good); err != nil {
			t.Fatalf("the paired success failed, so the refusal was about this request rather than about the failure: %v", err)
		}
	}

	t.Run("an unreachable host", func(t *testing.T) {
		run(t, string(proto.ProbeUnreachable), func(p *proto.SSHSpawnParams, _ *sshCoordinator) func() {
			// A port nothing answers on: a listener opened and closed, so the
			// refusal is about the DESTINATION and not about a wrong request.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			addr := ln.Addr().String()
			_ = ln.Close()
			host, portStr, _ := net.SplitHostPort(addr)
			var port int
			if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
				t.Fatalf("port %q: %v", portStr, err)
			}
			host0, port0 := p.Destination.Host, p.Destination.Port
			p.Destination.Host, p.Destination.Port = host, port
			return func() { p.Destination.Host, p.Destination.Port = host0, port0 }
		})
	})

	t.Run("the coordinator cannot read the material", func(t *testing.T) {
		run(t, proto.ErrCodeVaultSealed, func(_ *proto.SSHSpawnParams, c *sshCoordinator) func() {
			// A sealed vault: the coordinator's OWN refusal, with its own code
			// and not a rejected credential — the distinction the helper's
			// classifier exists to keep, because a person sent to look at a
			// host never sees the sheet they needed.
			c.mu.Lock()
			c.sealed = true
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				c.sealed = false
				c.mu.Unlock()
			}
		})
	})

	t.Run("a host key that changed", func(t *testing.T) {
		run(t, string(proto.ProbeHostKeyChanged), func(_ *proto.SSHSpawnParams, c *sshCoordinator) func() {
			c.mu.Lock()
			c.verdict, c.expected = proto.HostKeyChanged, "SHA256:recorded"
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				c.verdict, c.expected = proto.HostKeyTrusted, ""
				c.mu.Unlock()
			}
		})
	})

	t.Run("a pinned fingerprint that does not match", func(t *testing.T) {
		run(t, string(proto.ProbeHostKeyChanged), func(p *proto.SSHSpawnParams, _ *sshCoordinator) func() {
			// The caller's OWN expectation, enforced before anything is
			// authenticated: the coordinator's verdict says the key is the
			// recorded one and the request pinned another value, so the two
			// facts disagree and no shell is opened.
			want := p.HostKeyFingerprint
			p.HostKeyFingerprint = "SHA256:not-the-key-this-host-presents"
			return func() { p.HostKeyFingerprint = want }
		})
	})

	t.Run("a directory this wire cannot name", func(t *testing.T) {
		run(t, proto.ErrCodeBadParams, func(p *proto.SSHSpawnParams, _ *sshCoordinator) func() {
			// Refused rather than ignored: the far login shell's directory is
			// the far side's answer, and a field that accepted a value nothing
			// acts on is what a later generation starts reading.
			p.Cwd = "/tmp"
			return func() { p.Cwd = "" }
		})
	})

	t.Run("an incomplete request", func(t *testing.T) {
		run(t, proto.ErrCodeBadParams, func(p *proto.SSHSpawnParams, _ *sshCoordinator) func() {
			p.Destination.User = ""
			return func() { p.Destination.User = "test" }
		})
	})
}

// TestTheHostKeyRefusalCarriesTheEvidenceItRefusedOn — the coordinator rebuilds
// its own typed error from the refusal's details (the accept sheet and the
// mismatch warning switch on the fingerprint), so a refusal that dropped them
// would leave a person with a warning they cannot answer.
func TestTheHostKeyRefusalCarriesTheEvidenceItRefusedOn(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	coord := &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	}
	stand := newSSHStand(t, f, coord)
	p := stand.spawnParams(t, proto.SSHModeRaw)
	p.HostKeyFingerprint = "SHA256:not-the-key-this-host-presents"

	_, err := stand.spawn(t, p)
	if got := refusalCode(err); got != string(proto.ProbeHostKeyChanged) {
		t.Fatalf("answered %v (code %q), want host-key-changed", err, got)
	}
	details := refusalDetails(err)
	if len(details) == 0 {
		t.Fatal("the host-key refusal carries no evidence in its details")
	}
	var evidence proto.HostKeyEvidence
	if uerr := json.Unmarshal(details, &evidence); uerr != nil {
		t.Fatalf("the refusal's details are not host-key evidence: %v", uerr)
	}
	if evidence.Fingerprint != f.fingerprint() {
		t.Fatalf("the evidence names %q as the offered key, want the fixture's %q", evidence.Fingerprint, f.fingerprint())
	}
	if evidence.Expected != "SHA256:not-the-key-this-host-presents" {
		t.Fatalf("the evidence names %q as the expected key, want the pinned value", evidence.Expected)
	}
}

// TestABuildWithoutAnSSHClientRefusesTheOp — the untagged helper's answer, and
// it is a REFUSAL BY NAME (no_ssh_client) rather than a session that could never
// carry a byte: the artifact written to a host nobody here controls links no ssh
// client, so `spawn-ssh` there is a fact about the BINARY and not about the
// request. It goes over the real socket, because the code is what a caller
// switches on — and the far host is never dialed, which is the other half of the
// claim: a helper that cannot open a pane must not try.
func TestABuildWithoutAnSSHClientRefusesTheOp(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStandWithoutSpawner(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	_, err := stand.spawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	if got := refusalCode(err); got != proto.ErrCodeNoSSHClient {
		t.Fatalf("a helper with no ssh client answered %v (code %q), want %q", err, got, proto.ErrCodeNoSSHClient)
	}
	if attempts := f.authAttempts(); len(attempts) != 0 {
		t.Fatalf("the refusal dialed anyway: the far host was offered %v", attempts)
	}
	if shells := f.shellsSeen(); shells != 0 {
		t.Fatalf("the refusal opened %d shells on the far host", shells)
	}
}

// normaliseOD collapses the whitespace `od -An -c` puts between the characters
// it renders, so an assertion can name the bytes rather than the column layout:
// "   D   S   R   [" and "DSR[" are the same answer.
func normaliseOD(s string) string {
	return strings.Join(strings.Fields(s), "")
}
