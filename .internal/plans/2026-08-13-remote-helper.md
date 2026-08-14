# Remote Helper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** On an SSH tab, the Git panel shows that host's repository and its mutation controls work — stage a change an agent made there and commit it, with the repository's own hooks running on the machine that owns the repository.

**Architecture:** One new build target, `cmd/nocx-helper`, launched on the remote host over a single pty-less SSH `exec` channel and framed over its stdin/stdout. It hosts a closed set of named operations grouped into services; `git` is the only service in this plan, mapped onto the existing `internal/git/local`. The backend gets a second `git.RepoFactory` implementation that speaks that protocol, selected at the composition root by session kind and helper availability.

**Tech Stack:** Go (same module, no new dependencies), `golang.org/x/crypto/ssh` (already present), SFTP via the existing `internal/shellintegration` publisher, SolidJS for the panel states.

**Spec:** `.internal/specs/2026-08-13-remote-helper-design.md` — approved and stress-tested. Decision ids below (`D0`–`D26`) refer to its §3.

## Global Constraints

- **No operation may accept argv** (D3). There is no escape hatch, no read-only variant, no debug build that enables one.
- **The helper's channel never requests `pty-req`** (D19). A pty translates `\n` → `\r\n` and echoes input; frames would be corrupted.
- **stdout is the wire; stderr is diagnostics** (D22). The helper writes nothing but frames to stdout.
- **Bounds are applied by the helper, never by the backend's reader** (D9).
- **Mutations are never cancelled** (D11), and transport loss during one is `indeterminate`, never a failure and never retried (D12).
- **Build targets:** `linux/amd64`, `linux/arm64`, `darwin/arm64`. `darwin/amd64` answers `unsupportedPlatform` (D20).
- **Install path:** `~/.nocx/helper/<version>-<goos>-<goarch>-<contenthash>/`, complete only with an `.install-complete` marker (D7).
- **Frame:** `[type:1][seq:4][ack:4][len:4][payload:len]`, payload JSON. `seq`/`ack` are written and ignored (D15).
- **Every commit names its bead** and follows the AGENTS.md message contract: prose body, wrapped at 80, explaining what was wrong and why this way.
- **The worker runs the unit tests for the files it changed and stops there.** `make ci-full` and the containerized jobs belong to whoever integrates (AGENTS.md, "Git authority").

**A note on this plan's code blocks.** Where a step shows Go, the code is the contract — signatures, types and test assertions are exact and later tasks depend on them. Where a step says **read first**, the implementer must read the named file before writing: those are places where the surrounding code's shape decides the implementation, and inventing it from this document would produce something that compiles against a codebase that does not exist.

**But the code blocks were written, not compiled, and they are not lint-clean by construction.** Task 2's test shipped two `err :=` inside `if` statements that shadow an outer `err`; `golangci-lint` runs govet with `enable-all`, so shadow is on, and the pre-commit hook rejected the commit. `go vet` did not catch it — shadow is not in vet's default set — so the worker followed this plan's own verification steps, saw green, and was stopped at the gate with a brief telling it the test was verbatim and not to be edited. **"Verbatim" governs the assertions and the signatures, never the lint hygiene.** A worker that finds a code block failing a repo gate should fix the hygiene, keep the assertions byte-identical, and say so in the commit body.

---

### Task 1: Amend the git-manager spec so two documents cannot disagree

**Files:**

- Modify: `.internal/specs/2026-08-06-git-manager-design.md` (the D3 and D16 rows of §3)

**Interfaces:**

- Consumes: nothing.
- Produces: nothing in code. This task exists because every later task contradicts a document that currently says the opposite, and a "see the other spec" note is a second truth rather than an amendment (stress-test branch 1).

**Acceptance Criteria:**

- D3's row states that the remote case is served by the remote helper, names
  `.internal/specs/2026-08-13-remote-helper-design.md`, and **keeps**
  `DiscoveryConn.Exec` as a rejected alternative with its original reasoning.
- D16's row states the seam is upheld and hardened to "no exception", citing the cost orca
  paid for keeping one.
- No other decision in that document is edited.
- `grep -n "waits for the relay" .internal/specs/2026-08-06-git-manager-design.md` returns
  nothing.

- [ ] **Step 1: Read the two rows**

Run: `grep -n "| D3 \|| D16 " .internal/specs/2026-08-06-git-manager-design.md`

- [ ] **Step 2: Rewrite D3's decision cell**

Replace the decision text with: **The local case runs git here; the remote case runs it on the remote helper** (`.internal/specs/2026-08-13-remote-helper-design.md`, 2026-08-13). Leave the rejected-alternative cell as it stands — `DiscoveryConn.Exec` is still rejected, for the reasons already written there.

- [ ] **Step 3: Add one sentence to D16's decision cell**

Append: "Hardened 2026-08-13: no operation accepts argv, and there is no exception — see the remote-helper design, D3 in full."

- [ ] **Step 4: Verify no stale claim survives**

Run: `grep -n "waits for the relay\|Local only" .internal/specs/2026-08-06-git-manager-design.md`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add .internal/specs/2026-08-06-git-manager-design.md
git commit   # docs(spec): the remote git case is the helper, not the relay (<bead>)
```

---

### Task 2: Serve `exec` channels over pipes in the e2e sshd fixture

**Files:**

- Modify: `cmd/e2e-sshd/main.go` (the `"exec"` case around `:464`, and `startCommand` around `:530`)
- Test: `cmd/e2e-sshd/main_test.go`

**Read first:** `cmd/e2e-sshd/main.go:420-600`. The channel loop, `startCommand`, and the exit-status handshake all live there, and the pty path must keep working unchanged — `e2e/shell-mode.spec.ts` and `e2e/nocxify-journey.spec.ts` depend on it.

**Interfaces:**

- Consumes: nothing.
- Produces: an `exec` channel that, when no `pty-req` preceded it, runs the command with **pipes** — `cmd.Stdin`, `cmd.Stdout`, `cmd.Stderr` wired to the SSH channel, the channel's stderr going to `ch.Stderr()`. Task 6 and Task 12 depend on this.

**Acceptance Criteria:**

- A `pty-req` followed by `shell` behaves exactly as before (the existing specs stay green).
- An `exec` with no preceding `pty-req` runs the command with pipes, not a pty.
- A byte sequence containing `0x0A`, `0x0D` and `0x00` written to that command's stdin arrives
  byte-identical, and the same bytes written by the command arrive byte-identical at the
  client.
- The exit status is still delivered before channel close.

- [ ] **Step 1: Write the failing test**

```go
// TestExecChannelIsPtyLess proves the fixture is faithful to sshd for exec
// channels: no pty means no line discipline, so a binary frame survives.
// A pty would turn the 0x0A into 0x0D 0x0A and echo the input back.
func TestExecChannelIsPtyLess(t *testing.T) {
	addr, hostKey, signer := startFixture(t) // see the existing helpers in this file
	client := dial(t, addr, hostKey, signer)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	// `err =`, not `err :=`: golangci-lint runs govet with enable-all, so
	// shadow is on, and a fresh err inside the if would fail the pre-commit
	// gate. `go vet` alone does not catch it — shadow is not in its default
	// set — so a worker following this plan's own verification steps sees
	// green and is then stopped at the hook.
	if err = sess.Start("cat"); err != nil {
		t.Fatalf("start: %v", err)
	}

	want := []byte{0x00, 0x0A, 0x0D, 0x0A, 0xFF, 'x'}
	if _, err = stdin.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = stdin.Close()

	got, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes mangled: want %v, got %v", want, got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/e2e-sshd/ -run TestExecChannelIsPtyLess -v`
Expected: FAIL — `got` contains `0x0D 0x0A` where `want` has `0x0A`, and/or the echoed input is prepended.

- [ ] **Step 3: Split `startCommand` into a pty path and a pipe path**

Keep the existing pty body under its current name. Add a sibling that wires `cmd.Stdin = ch`, `cmd.Stdout = ch`, `cmd.Stderr = ch.Stderr()`, starts the command, waits, sends `exit-status`, and closes the channel — the same exit-status ordering the pty path already documents at `:560-585`. In the channel loop, `case "exec"` picks the pipe path when no `pty-req` has been seen on that channel, and the pty path when one has.

- [ ] **Step 4: Run both the new test and the existing suite**

Run: `go test ./cmd/e2e-sshd/ -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Commit**

```bash
git add cmd/e2e-sshd/
git commit   # test(e2e-sshd): serve exec channels over pipes, as sshd does (<bead>)
```

---

### Task 3: Split the binding registry out of the git domain package (D18)

**Files:**

- Create: `internal/git/registry/registry.go` (from `internal/git/binding.go`)
- Create: `internal/git/registry/errors.go` (the binding-scoped errors)
- Modify: `internal/git/git.go` (remove the `Caller` interface and the `internal/session` import)
- Modify: `internal/git/errors.go` (keep only the git-semantic errors)
- Delete: `internal/git/binding.go`
- Modify: every caller — `internal/transport/ws_git.go`, `internal/app/*`, and the tests that construct a registry
- Test: `internal/git/registry/registry_test.go` (moved from `internal/git/binding_test.go`)

**Read first:** `internal/git/binding.go` in full, and `internal/transport/ws_git.go` for how `Registry`, `Binding` and `Acquire` are consumed. The structural guarantee — a bound `Repo` is unreachable except through `Acquire` — must survive the move unchanged.

**Interfaces:**

- Consumes: nothing.
- Produces:
  - `internal/git` keeps the domain types, `Repo`, `RepoFactory`, and the git-semantic errors (`ErrNothingToCommit`, `ErrAmendUnborn`, `ErrConflicted`, `ErrNoRemote`) — the ones the helper must be able to return.
  - `internal/git/registry` gains `Caller`, `Binding`, `Handle`, `Registry`, `New()`,
    `Register(repo git.Repo, sessionID session.ID) (string, error)`, `Acquire(...)`, and
    `ErrUnknownBinding`, `ErrNotOwned`, `ErrHandleReleased`.

**Acceptance Criteria:**

- `go list -deps ./internal/git | grep -c 'nocx/internal/session'` is `0`.
- `go list -deps ./internal/git | grep -cE 'nocx/internal/(pty|ssh|storage)'` is `0`.
- Every pre-existing binding test passes unchanged apart from its package and import lines.
- `deadcode -filter 'nocx/internal/git' ./...` reports nothing new.

- [ ] **Step 1: Write the failing test — the dependency itself is the assertion**

```go
// TestDomainPackageIsLinkableStandalone is the whole point of the split: the
// helper binary links internal/git for its domain types, and internal/session
// drags pty, ssh and storage in behind it.
func TestDomainPackageIsLinkableStandalone(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/shady2k/nocx/internal/git").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, forbidden := range []string{
		"github.com/shady2k/nocx/internal/session",
		"github.com/shady2k/nocx/internal/pty",
		"github.com/shady2k/nocx/internal/ssh",
		"github.com/shady2k/nocx/internal/storage",
	} {
		if bytes.Contains(out, []byte(forbidden+"\n")) {
			t.Errorf("internal/git must not depend on %s", forbidden)
		}
	}
}
```

Put it in `internal/git/deps_test.go`.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/git/ -run TestDomainPackageIsLinkableStandalone -v`
Expected: FAIL, naming all four packages.

- [ ] **Step 3: Move the binding half**

`git mv internal/git/binding.go internal/git/registry/registry.go`, change the package clause, move `Caller` out of `git.go` and the three binding errors out of `errors.go` into `internal/git/registry/errors.go`. `Register` now takes `git.Repo`.

- [ ] **Step 4: Fix the callers**

Run: `go build ./... 2>&1 | head -40` and work the list. `internal/transport/ws_git.go` is the main one.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/git/... ./internal/transport/ ./internal/app/ -race`
Expected: PASS, and the new dependency test passes.

- [ ] **Step 6: Commit**

```bash
git add internal/git/ internal/transport/ internal/app/
git commit   # refactor(git): the domain package stops importing session (<bead>)
```

---

### Task 4: The frame codec (`internal/helper/proto`)

**Files:**

- Create: `internal/helper/proto/frame.go`, `internal/helper/proto/envelope.go`, `internal/helper/proto/version.go`
- Test: `internal/helper/proto/frame_test.go`, `internal/helper/proto/envelope_test.go`

**Read first:** `internal/lifecyclecodec/codec.go`. Its garbage-resync discipline and gap accounting are the shape to follow — a frame that cannot map is skipped to the next boundary and reported, never fatal. Do not import it: its envelopes are the lifecycle kernel's, and one codec serving two protocols would couple them permanently.

**Interfaces:**

- Consumes: nothing.
- Produces:

```go
package proto

const Version = "1"

// FrameType is the first header byte.
type FrameType uint8

const (
	TypeHello     FrameType = 1
	TypeHelloOK   FrameType = 2
	TypeRequest   FrameType = 3
	TypeResponse  FrameType = 4
	TypeNotify    FrameType = 5
	TypeCancel    FrameType = 6
	TypeChunk     FrameType = 7
	TypeKeepAlive FrameType = 9
)

// HeaderLen is [type:1][seq:4][ack:4][len:4].
const HeaderLen = 13

// MaxFrameBytes bounds one frame's payload (D14). A response above it is sent
// as a Response carrying ChunkedResult plus TypeChunk frames.
const MaxFrameBytes = 1 << 20

type Hello struct {
	Version string `json:"version"`
	Nonce   string `json:"nonce"`
	Corr    string `json:"corr"`
}

type HelloOK struct {
	Version     string `json:"version"`
	Nonce       string `json:"nonce"`
	ContentHash string `json:"contentHash"`
	InstanceID  string `json:"instanceId"`
}

type Request struct {
	ID      uint64          `json:"id"`
	Service string          `json:"service"`
	Op      string          `json:"op"`
	Params  json.RawMessage `json:"params,omitempty"`
	Corr    string          `json:"corr"`
}

type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ChunkedResult is the sentinel a Response carries when the real payload
// follows as TypeChunk frames, reassembled by concatenation (D14).
type ChunkedResult struct {
	ChunkedStreamID uint64 `json:"chunkedStreamId"`
	TotalBytes      int    `json:"totalBytes"`
	ChunkCount      int    `json:"chunkCount"`
}

func EncodeFrame(t FrameType, seq, ack uint32, payload []byte) []byte
func NewDecoder(onFrame func(t FrameType, seq, ack uint32, payload []byte), onGap func(bytes int)) *Decoder
func (d *Decoder) Feed(b []byte) error
```

Error codes are string constants in this package: `ErrCodeUnknownService`, `ErrCodeUnknownOp`, `ErrCodeCancelRefused`, `ErrCodeBadParams`, `ErrCodeInternal`.

**Acceptance Criteria:**

- A frame encoded and fed back to a decoder yields the same type, seq, ack and payload.
- A payload containing `0x00`, `0x0A` and `0x0D` round-trips byte-identically.
- A payload split across three `Feed` calls at arbitrary offsets decodes as one frame.
- Garbage before a valid frame is skipped, the valid frame is delivered, and `onGap` reports
  the skipped byte count.
- A length prefix above `MaxFrameBytes` is treated as garbage — the decoder resyncs rather
  than allocating.
- `EncodeFrame` refuses a payload above `MaxFrameBytes`.

- [ ] **Step 1: Write the failing round-trip and resync tests**

```go
func TestFrameRoundTripPreservesBinaryPayload(t *testing.T) {
	payload := []byte{0x00, 0x0A, 0x0D, 0x0A, 0xFF, '{', '}'}
	var gotType FrameType
	var gotSeq, gotAck uint32
	var gotPayload []byte
	d := NewDecoder(func(ty FrameType, seq, ack uint32, p []byte) {
		gotType, gotSeq, gotAck, gotPayload = ty, seq, ack, append([]byte(nil), p...)
	}, func(int) { t.Error("unexpected gap") })
	if err := d.Feed(EncodeFrame(TypeRequest, 7, 3, payload)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if gotType != TypeRequest || gotSeq != 7 || gotAck != 3 || !bytes.Equal(gotPayload, payload) {
		t.Fatalf("round trip lost data: %v %d %d %v", gotType, gotSeq, gotAck, gotPayload)
	}
}

func TestDecoderResyncsPastGarbageAndReportsIt(t *testing.T) {
	var frames int
	var gapped int
	d := NewDecoder(func(FrameType, uint32, uint32, []byte) { frames++ },
		func(n int) { gapped += n })
	garbage := []byte("bash: nocx-helper: command not found\n")
	if err := d.Feed(append(garbage, EncodeFrame(TypeHelloOK, 0, 0, []byte(`{}`))...)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if frames != 1 {
		t.Fatalf("want the valid frame delivered, got %d", frames)
	}
	if gapped == 0 {
		t.Fatal("want the skipped region reported")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/helper/proto/ -v`
Expected: FAIL — the package does not build yet.

- [ ] **Step 3: Implement `frame.go` and `envelope.go`**

Big-endian `binary.BigEndian.PutUint32` for the three 32-bit fields, in the order `seq`, `ack`, `len`. The decoder keeps a buffer, and on an implausible prefix advances **one byte** at a time so a valid frame starting inside the garbage is still found.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/helper/proto/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/helper/proto/
git commit   # feat(helper): the frame codec and envelopes (<bead>)
```

---

### Task 5: The helper host — hello, sentinel, dispatcher (`cmd/nocx-helper`, `internal/helper/host`)

**Files:**

- Create: `cmd/nocx-helper/main.go`
- Create: `internal/helper/host/host.go`, `internal/helper/host/service.go`
- Test: `internal/helper/host/host_test.go`

**Interfaces:**

- Consumes: `internal/helper/proto` (Task 4).
- Produces:

```go
package host

// Service is one named surface. Registering a second one is the whole
// extension point (D2); no service may expose an operation taking argv (D3).
type Service interface {
	Name() string
	Ops() []string
	Call(ctx context.Context, op string, params json.RawMessage) (any, error)
}

type Host struct{ /* ... */ }

func New(in io.Reader, out io.Writer, contentHash, instanceID string, log *slog.Logger) *Host
func (h *Host) Register(s Service)
func (h *Host) Serve(ctx context.Context) error
```

`Serve` reads one `TypeHello`; on a version mismatch it returns an error whose exit code is `ExitVersionMismatch = 42` and writes nothing. On a match it writes the sentinel line `nocx-helper <version> ready\n` to stdout, then a `TypeHelloOK` frame echoing the nonce with the content hash and instance id, then serves frames until stdin EOF.

**Acceptance Criteria:**

- A hello with the wrong version produces exit code 42 and **no bytes on stdout**.
- A hello with the right version produces the sentinel, then a `HelloOK` echoing the nonce
  verbatim and carrying the content hash and instance id it was constructed with.
- An unknown service answers `ErrCodeUnknownService`; an unknown op answers
  `ErrCodeUnknownOp`; neither closes the connection.
- Two requests whose handlers both block are served concurrently — the second's response
  arrives while the first is still blocked (D13).
- `Serve` returns when stdin reaches EOF.
- Nothing but frames is ever written to stdout (D22); the logger is constructed over stderr.
- **`session` is a reserved name** — registering it panics at construction, and a request for
  it answers `ErrCodeUnknownService` (D15).
- **No registered op accepts free-form arguments** (D3), asserted by the test below. This is
  the check that makes D3 a rule rather than an intention.
- Every request's `Corr` appears in the log line the host emits for it (D26).

Add to Step 1's test file:

```go
// TestNoOperationAcceptsArgv is D3 with teeth. An operation whose params carry
// a list of strings destined for a command line turns this helper into a
// remote shell, and the closed set of named operations into a fiction. orca
// kept exactly one such operation and paid for it with a 300-line allowlist
// validator; we keep none, and this test is why that stays true.
func TestNoOperationAcceptsArgv(t *testing.T) {
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	for _, svc := range h.Services() {
		for _, op := range svc.Ops() {
			schema := svc.ParamsSchema(op) // every op declares its params type
			for _, field := range schema.Fields() {
				if field.IsFreeFormStringList() {
					t.Errorf("%s.%s takes %q: no operation may accept argv (D3)",
						svc.Name(), op, field.Name)
				}
			}
		}
	}
}
```

`ParamsSchema` is reflection over the op's declared params struct; `IsFreeFormStringList`
is true for a `[]string` field not carrying a `nocx:"pathspec"` tag — pathspecs are the one
string list that exists, and they never reach argv (D8, Task 8).

- [ ] **Step 1: Write the failing tests**

```go
func TestHelloVersionMismatchWritesNothing(t *testing.T) {
	var out bytes.Buffer
	in := bytes.NewReader(proto.EncodeFrame(proto.TypeHello, 0, 0,
		mustJSON(proto.Hello{Version: "999", Nonce: "n"})))
	h := host.New(in, &out, "hash", "inst", slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := h.Serve(context.Background())
	if !errors.Is(err, host.ErrVersionMismatch) {
		t.Fatalf("want ErrVersionMismatch, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a mismatched hello must write nothing, wrote %q", out.Bytes())
	}
}

func TestHelloOKEchoesTheNonce(t *testing.T) {
	// ... feed a well-formed hello with nonce "abc123";
	// assert stdout begins with the sentinel line, then decode the next frame
	// and assert HelloOK{Nonce: "abc123", ContentHash: "hash", InstanceID: "inst"}.
}

func TestASlowOperationDoesNotStallAnother(t *testing.T) {
	// Register a service whose "slow" op blocks on a channel and whose "fast"
	// op returns immediately. Send slow then fast. Assert the fast response
	// arrives before the slow one is released. This is D13.
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/helper/host/ -v`
Expected: FAIL — package does not build.

- [ ] **Step 3: Implement the host**

One goroutine per request; a mutex around the writer so concurrent responses cannot interleave mid-frame; a `context` per request stored by id so `TypeCancel` can reach it.

- [ ] **Step 4: Implement `cmd/nocx-helper/main.go`**

It computes its own content hash by reading `os.Executable()` and hashing it, mints an instance id from `crypto/rand`, builds a `slog` logger over **stderr**, constructs the host over `os.Stdin`/`os.Stdout`, registers nothing yet, and maps `ErrVersionMismatch` to `os.Exit(42)`.

- [ ] **Step 5: Run the tests and build all three targets**

```bash
go test ./internal/helper/host/ -race -v
GOOS=linux  GOARCH=amd64 go build -o /dev/null ./cmd/nocx-helper
GOOS=linux  GOARCH=arm64 go build -o /dev/null ./cmd/nocx-helper
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/nocx-helper
```

- [ ] **Step 6: Commit**

```bash
git add cmd/nocx-helper/ internal/helper/host/
git commit   # feat(helper): the host — hello, sentinel, dispatcher (<bead>)
```

---

### Task 6: The backend client (`internal/helper/client`)

**Files:**

- Create: `internal/helper/client/client.go`, `internal/helper/client/launch.go`, `internal/helper/client/errors.go`
- Create: `internal/ssh/ssh_helperconn.go` — the pty-less exec lane
- Test: `internal/helper/client/client_test.go`, `internal/ssh/ssh_helperconn_test.go`

**Read first:** `internal/ssh/ssh_discovery.go` for the lease shape (`Done`, `LostErr`, `Close`, its own pooled reference) and `internal/ssh/ssh_real.go:819-880` for how a session is opened and piped. The new lane is `DiscoveryConn`'s sibling: it takes its own pooled reference and never touches the tab's, but it opens **one long-lived** session with pipes and **no** `pty-req`, instead of a fresh capped one per call.

**Interfaces:**

- Consumes: `internal/helper/proto` (Task 4), the helper's wire behaviour (Task 5).
- Produces:

```go
package client

// Client is one helper instance on one host, over one exec channel.
type Client struct{ /* ... */ }

type Config struct {
	Exec        HelperConn // the pty-less exec lane
	Command     string     // absolute path to the installed helper
	ExpectHash  string     // the content hash the installer wrote (D21)
	SentinelTTL time.Duration
	Log         *slog.Logger
}

func Dial(ctx context.Context, cfg Config) (*Client, error)
func (c *Client) Call(ctx context.Context, service, op string, params, out any) error
func (c *Client) Cancel(id uint64)
func (c *Client) InstanceID() string
func (c *Client) Done() <-chan struct{}
func (c *Client) Close() error
```

Dial errors, each a distinct sentinel because §6 renders them as distinct states:
`ErrExecForbidden`, `ErrSentinelTimeout`, `ErrNotOurHelper` (something else answered — carries the bytes seen), `ErrVersionMismatch`, `ErrHashMismatch`.

**Acceptance Criteria:**

- Against an in-process fake speaking the protocol, `Dial` returns a client and `Call` gets a
  result.
- A peer that prints a line and exits without the sentinel yields `ErrNotOurHelper`, and the
  error carries what was printed.
- A peer that never writes yields `ErrSentinelTimeout` after `SentinelTTL`.
- A `HelloOK` whose nonce differs from the one sent yields `ErrNotOurHelper`.
- A `HelloOK` whose content hash differs from `ExpectHash` yields `ErrHashMismatch`.
- A peer exiting 42 yields `ErrVersionMismatch`.
- The channel is opened **without** `pty-req` — asserted by a fake `HelperConn` that fails the
  test if a pty is requested.
- Transport loss with a request in flight fails that request with an error the caller can
  distinguish from a refusal, and closes `Done`.

- [ ] **Step 1: Write the failing tests**

Drive them through a `HelperConn` fake backed by `io.Pipe`, so no SSH is needed. The one test that needs real SSH is Task 12's.

```go
func TestDialRefusesAPeerThatIsNotOurHelper(t *testing.T) {
	conn := fakeConn(func(_ io.Reader, out io.Writer) {
		_, _ = io.WriteString(out, "bash: nocx-helper: No such file or directory\n")
	})
	_, err := client.Dial(context.Background(), client.Config{Exec: conn, SentinelTTL: time.Second})
	if !errors.Is(err, client.ErrNotOurHelper) {
		t.Fatalf("want ErrNotOurHelper, got %v", err)
	}
	if !strings.Contains(err.Error(), "No such file") {
		t.Fatalf("the error must carry what was seen: %v", err)
	}
}

func TestDialRejectsAMismatchedNonce(t *testing.T) { /* ... */ }
func TestDialRejectsAMismatchedContentHash(t *testing.T) { /* ... */ }
func TestChannelIsOpenedWithoutAPty(t *testing.T) { /* fake fails on pty-req */ }
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/helper/client/ -v`
Expected: FAIL — package does not build.

- [ ] **Step 3: Implement the lane, then the client**

`ssh_helperconn.go` first: `RealClient.HelperConn(ctx, host, opts...) (HelperConn, error)`, holding its own pooled reference, opening one session, `StdinPipe`/`StdoutPipe`/`StderrPipe`, `Start(command)` — and never `RequestPty`. Then `Dial`: write hello, scan stdout for the sentinel with a deadline while buffering what arrives, verify `HelloOK`, hand the remainder of the buffer to the decoder (the leftover-bytes trap orca documents in its handshake), start the read pump.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/helper/client/ ./internal/ssh/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/helper/client/ internal/ssh/ssh_helperconn.go internal/ssh/ssh_helperconn_test.go
git commit   # feat(helper): the backend client and its pty-less exec lane (<bead>)
```

---

### Task 7: The git service, read operations

**Files:**

- Create: `internal/git/hostsvc/hostsvc.go`, `internal/git/hostsvc/params.go`
- Create: `internal/git/helper/repo.go`, `internal/git/helper/factory.go`
- Modify: `cmd/nocx-helper/main.go` (register the service)
- Test: `internal/git/hostsvc/hostsvc_test.go`, `internal/git/helper/repo_test.go`

**Read first:** `internal/git/local/factory.go` and `internal/git/local/local.go`. The service is a thin mapping onto them — it must not re-derive anything they already answer, and the bounds it applies are theirs (D9).

**Interfaces:**

- Consumes: Tasks 4–6, `internal/git` (Task 3), `internal/git/local`.
- Produces:
  - Service name `"git"`, ops `open`, `status`, `envState`, `diff`, `log`.
  - `helper.NewFactory(c *client.Client) git.RepoFactory` and a `git.Repo` whose read methods
    are one `Call` each.
  - Params and results are the domain types of `internal/git`, JSON-encoded. `Status`, `Diff`,
    `Log` and `OpenOutcome` cross verbatim.

**Acceptance Criteria:**

- Against a real temporary repository, `status` through the service returns the same
  `git.Status` the local factory returns for the same repository — asserted field by field.
- `Completeness` is computed by the service and crosses the wire; the client never counts.
- A `diff` above the byte bound returns `DiffTooLarge` with `Truncated` true, and the bytes
  beyond the bound never enter a frame.
- `open` on a directory that is not a repository returns `OpenNotARepository`; with no git on
  `PATH`, `OpenGitUnavailable`.
- Every empty slice marshals as `[]`, never `null` — the defect the first contract schema in
  this repository caught.
- **The remote environment is resolved once, at helper start** (D24): bounded by a deadline
  and an output cap through `internal/loginshell`, cached for the process lifetime, and
  reported as `EnvResolved`/`EnvDegraded` on `OpenOutcome` and by `envState`. A second `open`
  does not re-resolve. With a deliberately broken login shell the outcome is `degraded` with a
  reason, and git still runs.
- **A cancelled read actually stops git on the remote host** (D10): a `cancel` naming an
  in-flight `diff` on a repository with a slow textconv filter causes the child **and its
  process group** to die — asserted by the filter's own marker file not appearing. This is
  `internal/git/local`'s existing escalation doing its job inside the helper, and the test
  exists because a channel close could never have promised it.
- **A result above the frame bound is chunked** (D14): a `status` in a repository with enough
  entries to exceed `proto.MaxFrameBytes` returns a `ChunkedResult` sentinel followed by
  `TypeChunk` frames, and the client reassembles by concatenation into the identical
  `git.Status`. Test it by lowering the bound in the test, not by building a huge repository.

- [ ] **Step 1: Write the failing equivalence test**

```go
// TestServiceStatusMatchesLocal is the contract in one assertion: the panel
// must say the same thing on both machines, so the service is only correct if
// it agrees with the local implementation on the same repository.
func TestServiceStatusMatchesLocal(t *testing.T) {
	dir := fixtureRepo(t) // reuse internal/git/local/fixture_test.go's builder
	local := localgit.NewFactory()
	repoLocal, _, err := local.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("local open: %v", err)
	}
	want, err := repoLocal.Status(context.Background())
	if err != nil {
		t.Fatalf("local status: %v", err)
	}

	svc := hostsvc.New(localgit.NewFactory())
	got := callStatusThroughService(t, svc, dir)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("service disagrees with local (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/git/hostsvc/ -v`
Expected: FAIL — package does not build.

- [ ] **Step 3: Implement the service's read ops, then the client-side repo**

The service holds a `map[bindingKey]git.Repo` opened through `local.NewFactory()`, keyed by the id it returns from `open`. The client-side `Repo` holds that id and turns each method into one `Call`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/git/... -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/git/hostsvc/ internal/git/helper/ cmd/nocx-helper/
git commit   # feat(git): the helper's git service and client, read operations (<bead>)
```

---

### Task 8: The git service, mutations and `indeterminate`

**Files:**

- Modify: `internal/git/hostsvc/hostsvc.go` (ops `stage`, `unstage`, `stageAll`, `unstageAll`, `commit`, `headMessage`, `remoteURL`)
- Modify: `internal/git/helper/repo.go`
- Modify: `internal/git/git.go` — add `CommitIndeterminate` to `CommitState` (D12)
- Modify: `frontend/src/git/*` — render the indeterminate outcome
- Test: `internal/git/hostsvc/mutate_test.go`, `internal/git/helper/indeterminate_test.go`

**Interfaces:**

- Consumes: Task 7.
- Produces: `git.CommitIndeterminate CommitState = "indeterminate"`, and the same third value on
  every mutation result. `Cancel` naming a mutation returns `ErrCodeCancelRefused` (D11).

**Acceptance Criteria:**

- Paths cross as a NUL-joined blob in `params` and reach git through
  `--pathspec-from-file=- --pathspec-file-nul` — never in argv (D8). A path containing a
  space, a quote, a leading `-` and a newline stages correctly.
- A commit message containing newlines, quotes and non-ASCII reaches git through `commit -F -`
  and becomes `HEAD`'s message byte-for-byte.
- A `cancel` naming an in-flight mutation is **refused** with `ErrCodeCancelRefused`, and the
  mutation completes.
- Transport loss between a mutation request and its response yields `indeterminate` — not
  `failed` — and the store issues no retry.
- The panel renders indeterminate as "this may have happened" with a refresh, never as an
  error.

- [ ] **Step 1: Write the failing hostile-path test**

```go
func TestStageAcceptsAHostilePath(t *testing.T) {
	dir := fixtureRepo(t)
	hostile := "a file -with 'quotes' and\na newline.txt"
	writeFile(t, filepath.Join(dir, hostile), "x")

	svc := hostsvc.New(localgit.NewFactory())
	id := openThroughService(t, svc, dir)
	st := stageThroughService(t, svc, id, []string{hostile})

	if len(st.Staged) != 1 || st.Staged[0].Path != hostile {
		t.Fatalf("want exactly %q staged, got %+v", hostile, st.Staged)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/git/hostsvc/ -run TestStageAcceptsAHostilePath -v`
Expected: FAIL.

- [ ] **Step 3: Implement the mutation ops**

- [ ] **Step 4: Write and pass the cancel-refusal and indeterminate tests**

- [ ] **Step 5: Run everything**

Run: `go test ./internal/git/... -race && cd frontend && npx vitest run src/git`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/git/ frontend/src/git/
git commit   # feat(git): remote mutations, and the indeterminate outcome (<bead>)
```

---

### Task 9: Build, bundle and install the artifact (`internal/helper/deploy`)

**Files:**

- Create: `internal/helper/deploy/build.go` (the embedded artifacts), `internal/helper/deploy/install.go`, `internal/helper/deploy/platform.go`, `internal/helper/deploy/prune.go`
- Create: `build/helpers/.gitignore` (build output, not committed)
- Modify: `Makefile` — a `helpers` target and a release dependency on it
- Test: `internal/helper/deploy/install_test.go`, `internal/helper/deploy/platform_test.go`

**Read first:** `internal/shellintegration/install_remote.go` and `internal/shellintegration/publisher.go`. The SFTP publisher already exists and already knows how to write a bundle to a remote home without touching rc files; this task extends its use, it does not write a second uploader.

**Interfaces:**

- Consumes: Task 6's `client.Config.Command` and `ExpectHash`.
- Produces:

```go
package deploy

type Platform struct{ GOOS, GOARCH string }

// Probe asks the remote host what it is, with one bounded command.
func Probe(ctx context.Context, exec ExecOnce) (Platform, error)

// Artifact returns the embedded, still-compressed helper for p, or
// ErrUnsupportedPlatform.
func Artifact(p Platform) (data []byte, contentHash string, err error)

// Ensure installs the artifact if it is not already complete, and returns the
// absolute path of the installed binary and its content hash.
func Ensure(ctx context.Context, fs RemoteFS, home string, p Platform) (path, hash string, err error)

func Prune(ctx context.Context, fs RemoteFS, home string, keep string) error
func Uninstall(ctx context.Context, fs RemoteFS, home string) error
```

**Acceptance Criteria:**

- `make helpers` produces the three binaries; `darwin/amd64` is not built and `Artifact`
  returns `ErrUnsupportedPlatform` for it.
- The install directory is `~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/` — two platforms
  never collide on one name (D7).
- `Ensure` writes to a temporary name and renames; a directory without `.install-complete` is
  removed and reinstalled, never used.
- `Ensure` on an already-complete directory uploads nothing.
- `Prune` removes older versions and never the one passed as `keep`.
- `Uninstall` removes the whole `~/.nocx/helper` tree, and **closes the client's channel
  first** so no helper is running out of a directory being deleted (D25).
- An upload interrupted midway leaves nothing that a later `Ensure` mistakes for complete —
  tested by a `RemoteFS` fake that fails at 50% of the bytes.
- **A hash mismatch triggers exactly one reinstall** (D6): given a complete directory whose
  binary does not hash to its name, `Ensure` removes it, reinstalls once, and returns the
  fresh path. A second mismatch on the same call path returns `ErrHashMismatch` and does not
  loop. The caller in Task 11 maps that to `helperVersionMismatch`.

- [ ] **Step 1: Write the failing interrupted-upload test**

```go
func TestAnInterruptedInstallIsNeverMistakenForComplete(t *testing.T) {
	fs := newFakeFS()
	fs.failAfterBytes(1024)
	_, _, err := deploy.Ensure(context.Background(), fs, "/home/u", deploy.Platform{"linux", "amd64"})
	if err == nil {
		t.Fatal("want the interrupted upload to fail")
	}
	fs.failAfterBytes(0) // heal
	path, _, err := deploy.Ensure(context.Background(), fs, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("second attempt must succeed: %v", err)
	}
	if !fs.hasMarker(filepath.Dir(path)) {
		t.Fatal("the completed install must carry .install-complete")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/helper/deploy/ -v`
Expected: FAIL — package does not build.

- [ ] **Step 3: Add the `Makefile` target and the embed**

```make
HELPER_TARGETS := linux/amd64 linux/arm64 darwin/arm64

.PHONY: helpers
helpers:
	@mkdir -p build/helpers
	@for t in $(HELPER_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" \
	    -o build/helpers/nocx-helper-$$os-$$arch ./cmd/nocx-helper || exit 1; \
	  gzip -9 -f build/helpers/nocx-helper-$$os-$$arch || exit 1; \
	done
```

`build.go` embeds `build/helpers/*.gz` with `//go:embed` and computes each one's hash of the **decompressed** bytes at init.

- [ ] **Step 4: Implement install, prune, uninstall; run the tests**

Run: `make helpers && go test ./internal/helper/deploy/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Makefile internal/helper/deploy/ build/helpers/.gitignore
git commit   # feat(helper): build, bundle and install the artifact (<bead>)
```

---

### Task 10: Consent — ask at the feature, not at the connection (D8)

**Files:**

- Modify: the `auto` mode resolver (find it: `grep -rn "DesiredAuto" --include=*.go internal/`)
- Modify: `internal/transport/ws_shell_footprint.go` and the footprint screen — a row for the helper
- Modify: `frontend/src/git/git-panel.tsx` — the `consentRequired` state and its offer
- Test: the resolver's existing test file, plus a frontend test for the panel state

**Read first:** `.internal/specs/2026-08-10-remote-footprint-consent-design.md` §3.1 and §4. The ladder and the ask are that document's; this task adds one condition and one caller, and must not re-answer anything else it decided.

**Interfaces:**

- Consumes: Task 9's `Ensure`.
- Produces: `auto` resolves to `relay` only when a surface on that connection has requested
  the helper. `git.open` answers `consentRequired` when the machine has no `relay`-tier
  answer.

**Acceptance Criteria:**

- With no helper requested, `auto` resolves exactly as it does today — no new prompt appears
  on connect, on any host. This is the regression that matters most.
- A machine at an explicit `script` answers `consentRequired`, and is not silently upgraded.
- Accepting from the panel raises that machine to `relay` and the next `git.open` proceeds.
- The consent copy states that the helper serves other remote features, not only git.
- The footprint screen lists the installed helper and its uninstall removes it.

- [ ] **Step 1: Write the failing regression test**

```go
// TestShippingAHelperDoesNotOptEveryMachineIn is the stress test's finding as
// an assertion: auto's relay arm was written as "a suitable binary exists for
// that platform", which becomes true everywhere the day we ship one.
func TestShippingAHelperDoesNotOptEveryMachineIn(t *testing.T) {
	r := newResolver(withHelperArtifactAvailable(true), withHelperRequested(false))
	if got := r.Resolve(machineWithNoStoredAnswer); got == DesiredRelay {
		t.Fatal("auto must not reach relay for a connection nothing asked the helper for")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Expected: FAIL — `auto` resolves to `relay`.

- [ ] **Step 3: Add the second condition, the panel state and the footprint row**

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/... -race && cd frontend && npx vitest run`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit   # feat(helper): consent is asked at the feature, not at the connection (<bead>)
```

---

### Task 11: Wire it up — composition root, transport, contracts

**Files:**

- Modify: `internal/transport/ws_git.go:340-380` — factory selection instead of the refusal
- Modify: `internal/app/app.go` — construct the helper-backed factory
- Modify: `internal/git/git.go` — delete `OpenRemoteUnsupported`, add the states of §6
- Create: `contracts/git.open.schema.json` (or extend it if present)
- Modify: `frontend/src/git/git-panel.tsx`, `frontend/src/git/git-store.ts`
- Test: `internal/transport/ws_git_test.go`, `frontend/src/git/git-store.test.ts`

**Acceptance Criteria:**

- `grep -rn "OpenRemoteUnsupported" --include=*.go .` returns nothing.
- `grep -rn "wait on the relay" frontend/src` returns nothing.
- On an SSH tab with a consented, installed helper, `git.open` returns `ok` and the panel
  renders with its mutation controls **present** (git D14 — what it cannot do it does not
  draw, and now it can).
- Each refusal state renders its own message naming what to do about it.
- `npm run contracts:check` passes, and the `…_OverTheWireConformsToContract` test validates
  the real result off the real socket.
- **Polling is coalesced by repository identity, not by tab** (D23): two tabs bound to the
  same remote repository produce **one** status read in flight, and the interval backs off
  when a read's duration approaches it. Asserted in `git-store.test.ts` by counting client
  calls across two bound tabs — the existing store tests already drive it this way.
- **The correlation id reaches the helper** (D26): the backend's log line for a git operation
  and the helper's log line for the same operation carry the same value.

- [ ] **Step 1: Write the failing transport test**

An SSH-kind session with a helper factory wired returns `ok`, not `remoteUnsupported`.

- [ ] **Step 2: Run and watch it fail**

- [ ] **Step 3: Replace the refusal with factory selection**

- [ ] **Step 4: Regenerate the contract types and run every gate the changed files touch**

Run: `npm run contracts:check && go test ./internal/transport/ -race && cd frontend && npx vitest run src/git`

- [ ] **Step 5: Commit**

```bash
git commit   # feat(git): the panel opens a repository on an SSH host (<bead>)
```

---

### Task 12: The acceptance test

**Files:**

- Create: `e2e/git-remote.spec.ts`
- Modify: `cmd/e2e-sshd/main.go` only if the fixture needs a repository seeded

**Acceptance Criteria — this is the epic's DONE WHEN:**

> On an SSH tab in a remote repository containing a file whose name has a space, a quote, a leading `-` and a newline: stage exactly that row through the panel, then commit a multi-line message with quotes and non-ASCII text, against a remote `pre-commit` hook that writes a marker file and emits more than one packet of output. Assert the marker exists **on the remote host**, exactly that one path was staged, the exact message is `HEAD`'s, and the returned status is fresh and complete.

- [ ] **Step 1: Seed the fixture repository with the hostile filename and the hook**

- [ ] **Step 2: Write the spec, driving the real panel**

Wait on observable state — a row appearing, a status becoming clean — never on a duration (AGENTS.md: a test may not depend on timing).

- [ ] **Step 3: Run it in the container**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/git-remote.spec.ts`
Expected: PASS. Remember the container's failure set is not CI's — confirm in CI.

- [ ] **Step 4: Commit and close the epic**

```bash
git commit   # test(e2e): a commit from the panel, on a remote host, through a hook (<bead>)
```

---

## Task dependency order

```
1 (spec)        ─┐
2 (e2e-sshd)    ─┼─→ independent, any order
3 (D18 split)   ─┘
                    ↓
4 (proto) → 5 (host) → 6 (client) → 7 (git reads) → 8 (git mutations)
                                         ↓
                    9 (deploy) → 10 (consent) → 11 (wiring) → 12 (acceptance)
```

Tasks 1, 2 and 3 have no dependencies on each other and can run in parallel. Task 9 needs Task 5 (there must be a binary to install) but not Task 7. Task 11 needs 8, 9 and 10.

### Landing groups — which tasks share a commit, and why

A task's dependency order is not the same question as whether it can be
committed on its own, and this plan got the second one wrong. The pre-commit
deadcode ratchet (`.githooks/check-deadcode.mjs`) fails any commit that adds a
function no `main()` reaches, and `update-deadcode-baseline.mjs` refuses to
write a baseline that grows — so a package landing before its first consumer has
no committable path at all. Task 4 hit exactly that: `internal/helper/proto` has
no importer until `cmd/nocx-helper` exists, and the worker implementing it
correctly stopped without writing a line (`nocx-7t3e`, the same defect
`nocx-z7s6` found in the snippets plan).

The gate is right and must not be routed around — a package written, covered and
called by nobody is the defect AGENTS.md records shipping twice. So:

- **Tasks 4 + 5 are one commit.** `cmd/nocx-helper` is what makes the codec
  reachable. Neither half is committable alone.
- **Tasks 7 + 8 are expected to be committable together**, since a git service
  registers into the host and is reached through `cmd/nocx-helper`. Expected, not
  established — measure it.
- **Tasks 6, 9 and 10 are unmeasured and probably not committable alone**:
  `internal/helper/client`, the deploy package and the consent path are reached
  only once Task 11 wires them into `app.go`. Establish the grouping by running
  `node .githooks/check-deadcode.mjs` against the real tree before dispatching
  them, not by reasoning about it — reasoning about it is what produced this
  section.

**Every worker on this plan must pass the pre-commit hook.** The instruction not
to run `make ci-full` or the containerized jobs does not exempt them from it, and
per `nocx-z7s6` one worker has already read it that way and committed through
`git -c core.hooksPath=/dev/null`. Say so in the brief.
