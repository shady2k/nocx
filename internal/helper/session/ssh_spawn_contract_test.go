//go:build nocx_local_ssh

package session_test

// The third contract check, applied to the ssh pane: the payloads OFF THE WIRE,
// not the structs a test built (contracts/helper/README.md).
//
// A test that marshals its own value proves the struct is well-formed. This
// takes the bytes each side actually sent — through the real framing, the real
// pool, the real channel and the real session service — and validates THOSE,
// which is the only form of the check that can see a field the server adds on
// the way out or loses on the way in.
//
// The schema loader mirrors internal/helper/sshsvc's, which mirrors
// internal/helper/client's (the package that owns the other half of this ABI's
// validation). It is duplicated rather than shared for the reason those two are:
// the symbols live in another package's `_test.go` file, and Go does not export
// those.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/shellintegration"
)

const helperContractDir = "../../../contracts/helper"

func loadHelperSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	entries, err := os.ReadDir(helperContractDir)
	if err != nil {
		t.Fatalf("read contracts/helper: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		f, openErr := os.Open(filepath.Join(helperContractDir, e.Name())) //nolint:gosec // test-only path under contracts/
		if openErr != nil {
			t.Fatalf("open %s: %v", e.Name(), openErr)
		}
		doc, parseErr := jsonschema.UnmarshalJSON(f)
		_ = f.Close()
		if parseErr != nil {
			t.Fatalf("parse %s: %v", e.Name(), parseErr)
		}
		if addErr := c.AddResource("https://nocx.local/contracts/helper/"+e.Name(), doc); addErr != nil {
			t.Fatalf("add %s: %v", e.Name(), addErr)
		}
	}
	s, err := c.Compile("https://nocx.local/contracts/helper/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

func validateHelperJSON(s *jsonschema.Schema, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

// framesOf collects the payloads of one frame type from a recorded direction.
// The recording starts mid-handshake — its first bytes are the sentinel LINE,
// which is not a frame — and the decoder's own resync is what gets past it,
// exactly as the shipping client's pump does.
func framesOf(t *testing.T, raw []byte, want proto.FrameType) [][]byte {
	t.Helper()
	var out [][]byte
	dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
		if ty == want {
			out = append(out, append([]byte(nil), payload...))
		}
	}, func(int) {})
	if err := dec.Feed(raw); err != nil {
		t.Fatalf("decode recorded frames: %v", err)
	}
	return out
}

// requestOf decodes one request frame.
type recordedRequest struct {
	ID      uint64          `json:"id"`
	Service string          `json:"service"`
	Op      string          `json:"op"`
	Params  json.RawMessage `json:"params"`
}

// responseOf decodes one response frame.
type recordedResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

// TestTheSpawnSSHOpAndTheLaunchUnionConformToTheirContractsOverTheWire drives a
// real ssh pane and a real LOCAL session and validates every payload that
// crossed, in both directions:
//
//   - the params the coordinator SENT for `spawn-ssh`;
//   - the results the helper ANSWERED with, for both ops;
//   - and, through the entry's own `launch`, the UNION — which is the half of
//     this change that is easiest to get subtly wrong, because a decoder that
//     read one branch would still pass a schema check written against the other.
func TestTheSpawnSSHOpAndTheLaunchUnionConformToTheirContractsOverTheWire(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	// A remote pane, and a LOCAL one beside it: the union has two branches and a
	// test that only drove one of them would leave the other's shape unchecked.
	remote := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
	local, err := stand.client.Spawn(context.Background(), proto.SpawnParams{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("local spawn: %v", err)
	}

	// ── what the coordinator SENT ──────────────────────────────────────
	spawnSSHParams := loadHelperSchema(t, "session.spawn-ssh.params.schema.json")
	seen := 0
	for _, payload := range framesOf(t, stand.toCoord.bytes(), proto.TypeRequest) {
		var req recordedRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Fatalf("decode recorded request: %v", err)
		}
		if req.Service != proto.ServiceSession || req.Op != proto.OpSpawnSSH {
			continue
		}
		if err := validateHelperJSON(spawnSSHParams, req.Params); err != nil {
			t.Errorf("spawn-ssh params off the wire do not satisfy their contract:\n%v\n\npayload was:\n%s", err, req.Params)
		}
		seen++
	}
	if seen != 1 {
		t.Fatalf("recorded %d spawn-ssh requests, want 1", seen)
	}

	// ── what the helper ANSWERED, both ops ─────────────────────────────
	spawnSSHResult := loadHelperSchema(t, "session.spawn-ssh.schema.json")
	spawnResult := loadHelperSchema(t, "session.spawn.schema.json")
	results := 0
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeResponse) {
		var resp recordedResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			t.Fatalf("decode recorded response: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("a spawn was refused: %s: %s", resp.Error.Code, resp.Error.Message)
		}
		var envelope struct {
			Entry struct {
				Launch json.RawMessage `json:"launch"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(resp.Result, &envelope); err != nil {
			t.Fatalf("decode a spawn result: %v", err)
		}
		var schema *jsonschema.Schema
		var branch string
		if bytes.Contains(envelope.Entry.Launch, []byte(`"kind":"local"`)) {
			schema, branch = spawnResult, "local"
		} else {
			schema, branch = spawnSSHResult, "ssh"
		}
		if err := validateHelperJSON(schema, resp.Result); err != nil {
			t.Errorf("the %s spawn result off the wire does not satisfy its contract:\n%v\n\npayload was:\n%s",
				branch, err, resp.Result)
		}
		results++
	}
	if results != 2 {
		t.Fatalf("recorded %d spawn results, want 2 (one ssh, one local)", results)
	}

	// ── the union, field by field, in the raw bytes ────────────────────
	//
	// The schema validation above is necessary and NOT sufficient: it checks
	// each payload against its own op's contract, while what this bead changed
	// is which CONTRACT a payload is judged by. So the wire's launch document is
	// inspected directly, and the assertions are the acceptance's own:
	//
	//   - the ssh branch carries its destination and says which branch it is;
	//   - it carries NO pid and NO pgid AT ALL, anywhere in the document —
	//     absence, never a fabricated zero, because 0 is the kernel scheduler
	//     and a reader that trusted the key would ask the OS about it;
	//   - the LOCAL branch keeps its own facts, which is what makes the union an
	//     extension rather than a rewrite for every reader that already exists.
	launches := make(map[string]json.RawMessage)
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeResponse) {
		var resp recordedResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			t.Fatalf("decode recorded response: %v", err)
		}
		if resp.Error != nil {
			continue
		}
		var envelope struct {
			Entry struct {
				Launch json.RawMessage `json:"launch"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(resp.Result, &envelope); err != nil {
			t.Fatalf("decode a spawn result: %v", err)
		}
		if bytes.Contains(envelope.Entry.Launch, []byte(`"kind":"local"`)) {
			launches["local"] = envelope.Entry.Launch
		} else {
			launches["ssh"] = envelope.Entry.Launch
		}
	}
	sshLaunch, ok := launches["ssh"]
	if !ok {
		t.Fatalf("no ssh launch record crossed the wire; recorded: %v", keysOf(launches))
	}
	localLaunch, ok := launches["local"]
	if !ok {
		t.Fatalf("no local launch record crossed the wire; recorded: %v", keysOf(launches))
	}
	for _, banned := range []string{`"pid"`, `"pgid"`} {
		if bytes.Contains(sshLaunch, []byte(banned)) {
			t.Fatalf("the ssh launch record carries %s: %s", banned, sshLaunch)
		}
	}
	for _, want := range []string{`"kind":"ssh"`, `"host"`, `"port"`, `"user"`, `"identityRef"`, `"shell"`} {
		if !bytes.Contains(sshLaunch, []byte(want)) {
			t.Fatalf("the ssh launch record is missing %s: %s", want, sshLaunch)
		}
	}
	for _, want := range []string{`"kind":"local"`, `"pid"`, `"pgid"`, `"shell"`} {
		if !bytes.Contains(localLaunch, []byte(want)) {
			t.Fatalf("the local launch record lost %s: %s", want, localLaunch)
		}
	}

	// And the coordinator's projection of the same two sessions: the ssh pane
	// answers with its remote branch and NO local record, the local pane with
	// its own record and no remote branch.
	//
	// ABSENCE, NOT A ZERO RECORD (nocx-s8mfn). The coordinator's DTO used to
	// fill the branch it had not got with zeros, which is the same fabricated
	// pid the wire above refuses: a reader that found `launch` beside a remote
	// one would ask this machine's OS about a process on another one. Exactly
	// one branch is present, which is also what the inventory contract that
	// carries this entry now states.
	if remote.RemoteLaunch == nil || remote.IsRemote() == false {
		t.Fatalf("the ssh session's projection has no remote branch: %+v", remote)
	}
	if remote.Launch != nil {
		t.Fatalf("the ssh session's projection carries a LOCAL launch record: %+v", *remote.Launch)
	}
	if local.RemoteLaunch != nil {
		t.Fatalf("a local session reported a remote launch: %+v", local.RemoteLaunch)
	}
	if local.Launch == nil {
		t.Fatal("the local session's projection lost its launch record entirely")
	}
	if local.Launch.Pid == 0 || local.Launch.Shell == "" {
		t.Fatalf("the local launch record lost its own facts: %+v", *local.Launch)
	}
}

// keysOf names the branches a test actually recorded, so a failure says what it
// saw rather than only what it wanted.
func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTheSSHShellKindSpellingsMatchTheLauncher is the drift guard between the
// wire's closed set and the package that renders the launcher.
//
// The spellings are declared twice on purpose: proto is the wire's leaf, and it
// is linked by every helper — including the artifact deployed to a host we do
// not own, which carries no launcher at all — so it may not import
// internal/shellintegration. The two tables are held together by a test rather
// than by luck: a tier added to the launcher without a wire spelling, or spelled
// differently, fails here.
func TestTheSSHShellKindSpellingsMatchTheLauncher(t *testing.T) {
	fromLauncher := []shellintegration.ShellKind{
		shellintegration.ShellAuto,
		shellintegration.ShellBash,
		shellintegration.ShellZsh,
		shellintegration.ShellUnknown,
	}
	onWire := map[proto.SSHShellKind]bool{
		proto.SSHShellAuto:    true,
		proto.SSHShellBash:    true,
		proto.SSHShellZsh:     true,
		proto.SSHShellUnknown: true,
	}
	if len(onWire) != len(fromLauncher) {
		t.Fatalf("the wire knows %d shell kinds and the launcher %d", len(onWire), len(fromLauncher))
	}
	for _, kind := range fromLauncher {
		if !onWire[proto.SSHShellKind(kind)] {
			t.Errorf("the launcher's %q has no spelling on this wire", kind)
		}
		delete(onWire, proto.SSHShellKind(kind))
	}
	for kind := range onWire {
		t.Errorf("the wire spells %q and the launcher does not know it", kind)
	}
}

// TestTheSSHModeSpellingsMatchTheProfileAxis is the same guard for the mode a
// remote session is asked to run in. The GATE is not duplicated — the spawner
// asks profile.DesiredMode.DeliversScripts, which owns it — but the four
// spellings are, and a mode added to the axis without a wire spelling would
// otherwise reach the helper as an unrecognised string and fail closed in
// silence.
func TestTheSSHModeSpellingsMatchTheProfileAxis(t *testing.T) {
	fromProfile := []profile.DesiredMode{
		profile.DesiredAuto,
		profile.DesiredRaw,
		profile.DesiredScript,
		profile.DesiredHelper,
	}
	onWire := map[proto.SSHMode]bool{
		proto.SSHModeAuto:   true,
		proto.SSHModeRaw:    true,
		proto.SSHModeScript: true,
		proto.SSHModeHelper: true,
	}
	if len(onWire) != len(fromProfile) {
		t.Fatalf("the wire knows %d modes and the profile axis %d", len(onWire), len(fromProfile))
	}
	for _, mode := range fromProfile {
		if !onWire[proto.SSHMode(mode)] {
			t.Errorf("the profile axis's %q has no spelling on this wire", mode)
		}
		delete(onWire, proto.SSHMode(mode))
	}
	for mode := range onWire {
		t.Errorf("the wire spells %q and the profile axis does not know it", mode)
	}
}
