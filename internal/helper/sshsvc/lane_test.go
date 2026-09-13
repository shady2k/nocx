//go:build nocx_local_ssh

package sshsvc_test

// The LANE op, against a REAL in-process ssh server: one pty-less exec lane on
// the pooled connection, running the installed helper of a named generation in
// its bridge subcommand (nocx-50w7p.10).
//
// Three things are asserted here and nowhere else, because this is the only
// place where the helper half meets a server that records what it was asked to
// run:
//
//   - the COMMAND. The caller names an install and a generation and never a
//     command (D3); the helper turns those into exactly one invocation, from
//     its own install layout, and the fixture is what proves the argv.
//   - the BYTES. What rides the lane is the frame protocol, unchanged: the
//     remote helper's ABI is not touched by the carrier moving.
//   - the EXIT STATUS. A lane's far end is a PROCESS, and the coordinator's
//     own classification reads its status (D5's exit 43 is "no helper is
//     serving that generation"). A status that did not cross would collapse
//     two facts a person acts on differently into one.
//
// Each refusal below is paired with the success it is the refusal OF, in the
// same test or the adjacent one: "the generation was refused" and "every lane
// was refused" produce the same red.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
)

// laneGeneration is the content hash a lane is asked for: 64 lowercase hex
// characters, which is what deploy.Ensure keys an install by.
const laneGeneration = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// laneDir is the install directory of a machine whose home is /home/u — the
// shape deploy writes, assembled from deploy's OWN derivation so the expected
// value cannot drift from the layout the installer uses.
func laneDir() string {
	return strings.TrimSuffix(deploy.InstalledPath("/home/u", deploy.Platform{GOOS: "linux", GOARCH: "amd64"}, laneGeneration), "/nocx-helper")
}

// laneParams is one lane request: a destination resolved exactly as every other
// op's, the machine's install directory, and the generation.
func laneParams(t *testing.T, f *fixture) proto.LaneParams {
	t.Helper()
	host, port := f.hostPort(t)
	return proto.LaneParams{
		Destination: proto.SSHDestination{
			Host: host, Port: port, User: "test",
			Identity: proto.SSHIdentity{
				Credential: &proto.SSHCredential{Ref: "cred-1"},
				Auth:       proto.SSHAuthPassword,
			},
		},
		Machine:    proto.Machine{Dir: laneDir()},
		Generation: proto.GenerationID(laneGeneration),
	}
}

// lane opens one lane over the wire and returns the channel the helper minted.
// It is the refusal path's shape: the CALL, answered or refused, with no stream
// to hold.
func (s *stand) lane(t *testing.T, p proto.LaneParams) (proto.OpenChannelResult, error) {
	t.Helper()
	var out proto.OpenChannelResult
	err := s.client.Call(context.Background(), proto.ServiceSSH, proto.OpLane, p, &out)
	return out, err
}

// TestALaneRunsTheInstalledBridgeAndCarriesItsBytes is the acceptance half: a
// lane reaches the in-process server, the server records the ONE command it was
// asked to run, the bridge's bytes cross in both directions, and the exit
// status the process ended with comes back.
func TestALaneRunsTheInstalledBridgeAndCarriesItsBytes(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	f.execPeer = func(in io.Reader, out io.Writer) int {
		// A bridge copies bytes between the channel and the endpoint socket.
		// What this fixture does instead is echo ONE frame and then end, so
		// that both halves of the assertion below are reachable from one run:
		// the bytes cross, and the process ends with its own status rather
		// than with the coordinator's close (which is not a status at all —
		// a caller that hangs up has not learned how anything exited).
		buf := make([]byte, 4096)
		n, err := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return 43
			}
		}
		_ = err
		return 43
	}
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	opened, err := stand.lane(t, laneParams(t, f))
	if err != nil {
		t.Fatalf("lane: %v", err)
	}
	if opened.Channel.IsZero() {
		t.Fatal("the helper answered no channel id, so the lane's bytes cannot be addressed")
	}

	// ── the command ────────────────────────────────────────────────────
	execs := f.ran()
	if len(execs) != 1 {
		t.Fatalf("the server was asked to run %d commands %q, want the one lane", len(execs), execs)
	}
	want := deploy.InstalledBinary(laneDir()) + " bridge " + laneGeneration
	if execs[0] != want {
		t.Fatalf("the lane ran %q, want %q", execs[0], want)
	}
	// And the two halves of that command are the values the caller NAMED — the
	// install directory it recorded and the generation it installed — rather
	// than a spelling this op accepted from anybody: the derivation is
	// deploy's, and the helper's `endpoint.BridgeCommand` is what makes the
	// middle word.
	if !strings.HasPrefix(execs[0], laneDir()+"/") {
		t.Fatalf("the lane's command %q does not start with the install directory the caller named", execs[0])
	}
	if !strings.HasSuffix(execs[0], " "+endpoint.BridgeCommand+" "+laneGeneration) {
		t.Fatalf("the lane's command %q does not end with the bridge subcommand and the generation", execs[0])
	}

	// ── the bytes, and the exit status ─────────────────────────────────
	// Through the coordinator's OWN carrier (client.OpenLane), because the
	// lane's whole purpose is that the helper client rides it: what the write
	// below puts on the channel is a frame-shaped binary blob — a zero byte
	// among it, which the carrier must not touch — and what comes back is the
	// same bytes, with the far process's exit status behind them.
	//
	// The first Call above answered the channel id; this second one is the
	// same op over the same wire through the client's own API, which is what a
	// caller has.
	lane, err := stand.client.OpenLane(context.Background(), laneParams(t, f))
	if err != nil {
		t.Fatalf("open the lane through the client: %v", err)
	}
	t.Cleanup(func() { _ = lane.Close() })

	frame := append([]byte{0x01, 0x00, 0x7f}, bytes.Repeat([]byte("nocx-frame"), 8)...)
	if _, werr := lane.Write(frame); werr != nil {
		t.Fatalf("write the frame protocol onto the lane: %v", werr)
	}
	got := make([]byte, len(frame))
	if _, rerr := io.ReadFull(lane, got); rerr != nil {
		t.Fatalf("read the lane: %v", rerr)
	}
	if !bytes.Equal(got, frame) {
		t.Fatalf("the lane carried %q, want %q — the frame protocol crosses unchanged", got, frame)
	}

	// The FAR PROCESS ended by itself, with 43, and that status is the fact
	// the coordinator's own classification reads (D5: exit 43 is "no helper is
	// serving that generation", which is not a lost connection and not any
	// other exit).
	code, err := lane.Wait()
	if err != nil {
		t.Fatalf("wait for the lane's process: %v", err)
	}
	if code != 43 {
		t.Fatalf("the lane reported exit %d, want the 43 its process ended with", code)
	}
	// And the lane's Done stayed open: a process that ended with a status is
	// not a transport that went, and a client that conflated them would retry
	// a fact about the host.
	select {
	case <-lane.Done():
		t.Fatal("Done closed for a lane whose process exited with a status")
	default:
	}
}

// TestALaneRefusesParametersThatCannotNameAnInstall is the refusal half of the
// command assertion above: a generation that is not a content hash, or a
// directory that is not an absolute path, is refused BEFORE anything is dialed
// — because both are spliced into the command line this helper builds, and a
// value that is more than the fact it claims to be is exactly the argv D3
// exists to refuse.
func TestALaneRefusesParametersThatCannotNameAnInstall(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	f.execPeer = func(_ io.Reader, _ io.Writer) int { return 0 }
	stand := newStand(t, &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()})

	bad := []struct {
		name   string
		mutate func(p *proto.LaneParams)
	}{
		{"no generation", func(p *proto.LaneParams) { p.Generation = "" }},
		{"a generation that is not a hash", func(p *proto.LaneParams) { p.Generation = "bridge; rm -rf /" }},
		{"a short generation", func(p *proto.LaneParams) { p.Generation = "deadbeef" }},
		{"no install directory", func(p *proto.LaneParams) { p.Machine.Dir = "" }},
		{"a relative install directory", func(p *proto.LaneParams) { p.Machine.Dir = ".nocx/helper/7-linux-amd64-" + laneGeneration }},
		{"no destination", func(p *proto.LaneParams) { p.Destination.Host = "" }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			p := laneParams(t, f)
			tc.mutate(&p)
			if _, err := stand.lane(t, p); refusalCode(err) != proto.ErrCodeBadParams {
				t.Fatalf("lane(%s) = %v (code %q), want %s", tc.name, err, refusalCode(err), proto.ErrCodeBadParams)
			}
		})
	}

	// Nothing was dialed and nothing ran: every refusal above is decided before
	// the connection, which is what makes them refusals rather than a far
	// side's answer about a command this helper should not have built.
	if got := len(f.ran()); got != 0 {
		t.Fatalf("a refused lane ran %d commands, want none", got)
	}
	if passwords, _ := f.authAttempts(); len(passwords) != 0 {
		t.Fatalf("a refused lane authenticated %d times, want none — a value the helper refuses must not reach a host", len(passwords))
	}

	// …and the SAME request with a valid generation and directory does dial and
	// does run, so the checks above have teeth rather than being a lane that
	// never works.
	if _, err := stand.lane(t, laneParams(t, f)); err != nil {
		t.Fatalf("a well-formed lane was refused too: %v", err)
	}
	if got := f.ran(); len(got) != 1 {
		t.Fatalf("the well-formed lane ran %d commands, want the bridge", len(got))
	}
}

// TestALaneNeverAcceptsAHostKeyOnTrust is the settings-style boundary applied
// to the one op that carries a program: an unknown key comes back as
// host-key-unknown WITH its evidence, so the coordinator can raise the sheet it
// always did, and nothing about this path can record a key by itself.
func TestALaneNeverAcceptsAHostKeyOnTrust(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	f.execPeer = func(_ io.Reader, _ io.Writer) int { return 0 }
	coord := &coordinator{
		password: "pw", verdict: proto.HostKeyUnknown, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	_, err := stand.lane(t, laneParams(t, f))
	if code := refusalCode(err); code != string(proto.ProbeHostKeyUnknown) {
		t.Fatalf("lane with an unrecorded host key = %v (code %q), want %s", err, code, proto.ProbeHostKeyUnknown)
	}
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("lane with an unrecorded host key = %v, want a refusal", err)
	}
	var ev proto.HostKeyEvidence
	if jerr := json.Unmarshal(refusal.Details, &ev); jerr != nil {
		t.Fatalf("the refusal's evidence did not decode (%v): %s — the accept sheet is built from exactly this", jerr, refusal.Details)
	}
	if ev.Fingerprint != f.hostKeyFingerprint() || len(ev.Key) == 0 {
		t.Fatalf("evidence = %+v, want the offered fingerprint and its key bytes", ev)
	}
	if got := len(f.ran()); got != 0 {
		t.Fatalf("a lane whose host key was refused ran %d commands, want none: an unrecorded key is refused before the credential is offered, and long before a program runs", got)
	}
	if trusted := coord.trusted(); len(trusted) != 0 {
		t.Fatalf("a lane trusted %d host keys on its own, want none", len(trusted))
	}
}

// TestALaneReportsARefusedCredentialAsItsOwnOutcome pairs the success above
// with the far side saying no: the code is the probe vocabulary's `rejected`,
// which is the class the coordinator's callers already switch on.
func TestALaneReportsARefusedCredentialAsItsOwnOutcome(t *testing.T) {
	f := newFixture(t, "the right password", newSigner(t))
	f.execPeer = func(_ io.Reader, _ io.Writer) int { return 0 }
	stand := newStand(t, &coordinator{
		password: "the wrong password", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	_, err := stand.lane(t, laneParams(t, f))
	if code := refusalCode(err); code != string(proto.ProbeRejected) {
		t.Fatalf("lane with a refused credential = %v (code %q), want %s", err, code, proto.ProbeRejected)
	}
	if got := len(f.ran()); got != 0 {
		t.Fatalf("a lane that could not authenticate ran %d commands, want none", got)
	}
}

// TestALaneWithoutACoordinatorIsRefusedByName: the helper asks the coordinator
// for the material a dial needs, so a request that arrives with no connection
// to ask on cannot be answered at all — and must say so rather than hang.
func TestALaneWithoutACoordinatorIsRefusedByName(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	svc := sshsvc.New(nil, discardLogger())
	// No connection on the context: no coordinator, so no material and no
	// verdict — the state the plan calls by this name. The code is read off the
	// service's own refusal coder, which is what the host puts on the wire.
	_, err := svc.Call(context.Background(), proto.OpLane, mustJSON(t, laneParams(t, f)))
	if err == nil {
		t.Fatal("a lane with no coordinator connection succeeded")
	}
	if code, _ := svc.Refusal(err); code != proto.ErrCodeNoAuthChannel {
		t.Fatalf("lane with no coordinator = %v (code %q), want %s", err, code, proto.ErrCodeNoAuthChannel)
	}
}

// ── the whole chain, with the real helper binary ───────────────────────
//
// The cases above prove the command and the carrier against a scripted far
// side. This one proves the ACCEPTANCE: a coordinator reaches a real helper
// daemon on this machine — the shipped binary, its endpoint socket, its
// sessions service — through a lane the helper opened on a real ssh session,
// where the far side runs the bridge this repository installs.
//
// Every part is the product's: the binary is built here from cmd/nocx-helper
// (the same content-addressed binary a socket is named for, D7), the ssh server
// is the in-process fixture, the ssh SERVICE is the real one over a real ssh
// client, and the coordinator side is the shipped client.Dial. Nothing stands
// in for anything except the ssh transport itself.
//
// The harness (build once, run one daemon on a private HOME, wait for its
// endpoint to accept) is internal/helper/endpoint's one_shape_test's, restated
// here for the reason this package restates the schema loader: those symbols
// live in another package's `_test.go`, and Go does not export those.

var (
	helperBuildOnce sync.Once
	helperBuiltBin  string
	helperBuiltGen  proto.GenerationID
	helperBuildErr  error
)

// builtHelperBinary builds cmd/nocx-helper once for this package's tests and
// answers its path and its generation — the sha256 of the file, because a
// helper install is content-addressed and the generation IS the build.
func builtHelperBinary(t *testing.T) (string, proto.GenerationID) {
	t.Helper()
	helperBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nocxlanebin")
		if err != nil {
			helperBuildErr = err
			return
		}
		bin := filepath.Join(dir, "nocx-helper")
		out, err := exec.Command("go", "build", "-o", bin, "../../../cmd/nocx-helper").CombinedOutput() //nolint:gosec // the arguments are this test's own constants
		if err != nil {
			helperBuildErr = errors.New(string(out))
			return
		}
		data, err := os.ReadFile(bin) // #nosec G304 — the path is this test's own build output
		if err != nil {
			helperBuildErr = err
			return
		}
		sum := sha256.Sum256(data)
		helperBuiltBin, helperBuiltGen = bin, proto.GenerationID(hex.EncodeToString(sum[:]))
	})
	if helperBuildErr != nil {
		t.Fatalf("building cmd/nocx-helper: %v", helperBuildErr)
	}
	return helperBuiltBin, helperBuiltGen
}

// helperAccount is the account the daemon runs as, as far as the endpoint is
// concerned: the run directory is derived from the home and from nothing else,
// so a home of its own is what keeps this test off the developer's endpoint and
// off every other test's.
func helperAccount(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "nocxlanehome")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

// runHelperDaemon starts `nocx-helper serve` and waits for its endpoint to
// ACCEPT — an observable state, never a duration.
func runHelperDaemon(t *testing.T, bin, home string, generation proto.GenerationID) {
	t.Helper()
	cmd := exec.Command(bin, endpoint.ServeCommand) //nolint:gosec // bin is this test's own build output
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper daemon: %v", err)
	}
	t.Cleanup(func() {
		// SIGTERM rather than SIGKILL: the helper's own shutdown closes what it
		// holds, so nothing is orphaned onto this machine.
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	})
	dir := endpoint.Dir(home)
	for {
		conn, err := endpoint.Dial(t.Context(), dir, generation)
		if err == nil {
			_ = conn.Close()
			return
		}
		if t.Context().Err() != nil {
			t.Fatalf("the helper never answered on its endpoint: %v", err)
		}
	}
}

// TestACoordinatorReachesARealHelperThroughALaneOverSSH is the criterion: git's
// carrier — an exec lane to a remote helper's bridge — carries the FRAME
// PROTOCOL to a real helper, over a real ssh session, and the remote helper's
// own ABI is unchanged by the move (which is the whole claim of the task: only
// the carrier moved).
func TestACoordinatorReachesARealHelperThroughALaneOverSSH(t *testing.T) {
	bin, generation := builtHelperBinary(t)
	home := helperAccount(t)
	runHelperDaemon(t, bin, home, generation)

	f := newFixture(t, "pw", newSigner(t))
	// The far side runs the command the lane op builds, for real, as the
	// account that owns the endpoint.
	f.execRun = true
	f.execEnv = []string{"HOME=" + home}
	stand := newStand(t, &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	// The lane names the install DIRECTORY — the directory the binary was
	// built into, which is what "the machine's install" means — and the
	// generation, which is the binary's own content hash. The helper derives
	// `<dir>/nocx-helper bridge <generation>` from exactly those two facts.
	params := laneParams(t, f)
	params.Machine.Dir = filepath.Dir(bin)
	params.Generation = generation

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The coordinator's own carrier, over the lane the helper opened.
	lane, err := stand.client.OpenLane(ctx, params)
	if err != nil {
		t.Fatalf("open the lane: %v", err)
	}
	t.Cleanup(func() { _ = lane.Close() })

	helper, err := client.Dial(ctx, client.Config{
		Exec: lane, ExpectHash: string(generation), SentinelTTL: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("the coordinator could not reach the helper through the lane: %v", err)
	}
	t.Cleanup(func() { _ = helper.Close() })

	// A REAL request over it: the daemon's own inventory, answered by the
	// shipped session service. An empty list is the right answer — nothing has
	// been spawned — and it is an ANSWER rather than a refusal, which is what
	// proves the frame protocol crossed and the daemon is on the other end.
	sessions, err := helper.Sessions(ctx)
	if err != nil {
		t.Fatalf("the helper did not answer its own inventory over the lane: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("the daemon reported %d sessions, want none: this HOME is private to the test", len(sessions))
	}

	// And the far side ran the ONE command that is a lane: the installed
	// binary, this package's bridge subcommand, and the generation.
	want := deploy.InstalledBinary(filepath.Dir(bin)) + " " + endpoint.BridgeCommand + " " + string(generation)
	execs := f.ran()
	if len(execs) != 1 || execs[0] != want {
		t.Fatalf("the ssh session ran %q, want exactly %q", execs, want)
	}
}

// TestALaneToAGenerationNothingServesIsClassifiedByTheBridgeExit is the refusal
// half of the case above, with the same real binary: a generation nothing is
// serving makes the bridge exit with its own code (43), which the coordinator's
// classification reads as "no helper is running there" — a sentence about the
// HOST, whose recovery is different from every other pre-sentinel ending.
func TestALaneToAGenerationNothingServesIsClassifiedByTheBridgeExit(t *testing.T) {
	bin, generation := builtHelperBinary(t)
	home := helperAccount(t)
	runHelperDaemon(t, bin, home, generation)

	f := newFixture(t, "pw", newSigner(t))
	f.execRun = true
	f.execEnv = []string{"HOME=" + home}
	stand := newStand(t, &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	// A generation this binary is NOT: the bridge refuses rather than starting
	// a helper under somebody else's name (endpoint.Ensure says why), and the
	// exit status is what carries that fact back.
	other := proto.GenerationID(strings.Repeat("f", 64))
	if other == generation {
		t.Fatal("this test needs a generation that is not the binary's own")
	}
	params := laneParams(t, f)
	params.Machine.Dir = filepath.Dir(bin)
	params.Generation = other

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lane, err := stand.client.OpenLane(ctx, params)
	if err != nil {
		t.Fatalf("open the lane: %v", err)
	}
	t.Cleanup(func() { _ = lane.Close() })

	_, err = client.Dial(ctx, client.Config{
		Exec: lane, ExpectHash: string(other), SentinelTTL: 10 * time.Second,
	})
	if !errors.Is(err, client.ErrHelperNotServing) {
		t.Fatalf("a lane to a generation nothing serves = %v, want %v — the bridge's exit status is the only "+
			"thing that can tell this apart from a host that refused the exec", err, client.ErrHelperNotServing)
	}
}
