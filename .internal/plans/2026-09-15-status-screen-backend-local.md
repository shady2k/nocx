# Status screen, plan 1 of 4: the backend for local projects

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`br create -t task --parent nocx-ss9gh.1`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability. AGENTS.md wins over the skill: the tracker is `br`, not `bd`.

**Goal:** A headless nocx backend finds the local git repositories the owner worked in that carry a `br` backlog, builds each one's milestone → stage → epic → task model from `br`'s export, serves it over `status.*` JSON-RPC, and announces `status.changed` within one poll of a `br` write.

**Architecture:** A new `internal/tracker` port runs `br` through `proc.Supervisor` and reads `br`'s export, parsing it only when its SHA-256 equals `br sync --status`'s `jsonl_content_hash`. A new `internal/status` package derives projects from the command ledger (`content.LedgerRepository.DistinctCwds`), keeps locations in two new `content.db` tables, builds the model with pure functions, and polls. `internal/transport` exposes it; `internal/app` wires it.

**Tech Stack:** Go 1.26, `proc.Supervisor`, `internal/content` (encrypted SQLite, ADR-0055 ladder), `internal/git` (local factory), `github.com/santhosh-tekuri/jsonschema/v6`, `br` 0.5.10.

**Spec:** `.internal/specs/2026-09-15-status-screen-design.md`. Plans 2 (renderer: cards and tree), 3 (stage graph component) and 4 (remote hosts, after `nocx-522al`) follow.

## Global Constraints

- Base branch `feat/status-screen` (cut from `feat/agent-orchestration` at `cc63918b`). Work in `/home/dev/.herdr/worktrees/nocx/feat-status-screen`; run `pwd` first.
- Fresh worktree before the first commit: `npm ci` at the root and in `frontend/`, and `make vt-archives` (pre-commit lint needs libghostty-vt headers).
- Every `br` invocation carries `--no-auto-import --no-auto-flush --allow-stale`. Only `sync --status --json`, `info --json`, `coordination status --json` are run.
- The export is parsed only when `sha256(bytes) == jsonl_content_hash`.
- Vocabulary: milestone → epic → feature → task / bug / chore. A milestone is an `epic` with the label `milestone`.
- Not decomposed: an epic whose status is neither `closed` nor `deferred` and that has no non-deferred children. Never "0 of 0".
- Deferred issues (and their subtrees) are excluded from every count; `tombstone` rows are dropped at decode. A task is any non-epic.
- `content.db` goes 18 → 19 by one rung, with frozen `testdata/schema_v18.sql`, `schemaShapeDigests[18]`, `historicalSchemaObjectNames[18]`.
- Every JSON-RPC method: params and result schema in `contracts/`, an entry in `contracts/openrpc.json`, a `valid` entry in `params_contract_test.go`, `_DTOConformsToContract` and `_OverTheWireConformsToContract` tests. `additionalProperties: false` and explicit `required` everywhere.
- DTO slices are built with `make`, never nil.
- Remote hosts are out of this plan: non-local environments are skipped; `status.projects.add` with a non-empty host returns `remote projects are not available yet`.
- Workers run unit tests of the packages they touched, `go build ./...` and `go vet` on them. No `make ci-full`, no container suites, no e2e.
- Tests never depend on timing: wait on an observable condition with a deadline, never sleep-then-assert.
- Commit format (AGENTS.md): `<type>(<scope>): <subject> (<bead-id>)`, prose body, `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.

## File map

| file                                                                                                                                                       | responsibility                                               |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `internal/proc/supervisor.go` (modify)                                                                                                                     | keep a bounded stderr on `Output`                            |
| `internal/tracker/tracker.go`, `origin.go`                                                                                                                 | the port, decoded types, errors, `NormalizeOrigin`           |
| `internal/tracker/brspawn/{argv,decode}.go`, `testdata/`                                                                                                   | closed argv set, decoders, fixtures written by real `br`     |
| `internal/tracker/local/local.go`                                                                                                                          | runs `br` locally; verified export read                      |
| `internal/git/{git.go,local/remote.go,hostsvc/hostsvc.go,helper/factory.go,registry/registry.go}` (modify)                                                 | `OriginURL`                                                  |
| `internal/helper/proto/version.go` (modify)                                                                                                                | Version 15                                                   |
| `internal/content/{sqlite.go,schema_migrate.go,content.go,stub.go}` (modify), `status_location_sqlite.go`, `ledger_distinct.go`, `testdata/schema_v18.sql` | tables, repository, `DistinctCwds`                           |
| `internal/status/model.go`                                                                                                                                 | index, overview, children, level graph, diff                 |
| `internal/status/service.go`                                                                                                                               | discovery, locations, forgetting, polling, revisions, events |
| `internal/transport/ws_status.go`                                                                                                                          | `status.*` methods, `BroadcastStatusChanged`                 |
| `contracts/status.*.json`, `contracts/openrpc.json`                                                                                                        | wire contracts                                               |
| `internal/app/app.go` (modify), `internal/app/status_acceptance_test.go`                                                                                   | wiring; the DONE WHEN check                                  |

---

### Task 1: `proc.Supervisor` keeps a bounded stderr

`br` writes its refusal to stderr and the screen must show it (spec §8). `proc.Supervisor` discards stderr. Extend the existing runner rather than write a second one (AGENTS.md: look for the existing answer).

**Files:**

- Modify: `internal/proc/supervisor.go` (`Job`, `Output`, `Run`)
- Test: `internal/proc/supervisor_stderr_test.go`

**Interfaces:**

- Produces: `proc.Job.MaxStderrBytes int` (0 = discard, as today); `proc.Output.Stderr []byte`.

**Acceptance Criteria:**

- A job with `MaxStderrBytes: 16` whose child writes 64 bytes to stderr and exits 3 returns an error containing `exited 3` and `Output.Stderr` equal to the first 16 bytes.
- With `MaxStderrBytes: 0`, `Output.Stderr == nil`.
- `go test ./internal/proc/... ./internal/commandnames/...` passes.

- [ ] **Step 1: Write the failing test**

```go
package proc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunKeepsTheFirstBytesOfStderrWithinItsBound(t *testing.T) {
	out, err := Supervisor{}.Run(context.Background(), Job{
		Argv:           []string{"sh", "-c", `printf '%064d' 0 >&2; exit 3`},
		Deadline:       5 * time.Second,
		MaxBytes:       1024,
		MaxStderrBytes: 16,
	})
	if err == nil || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("err = %v, want an exit-3 error", err)
	}
	if got := string(out.Stderr); got != strings.Repeat("0", 16) {
		t.Fatalf("Stderr = %q, want the first 16 bytes", got)
	}
}

func TestRunWithoutAStderrBoundKeepsNoStderr(t *testing.T) {
	out, err := Supervisor{}.Run(context.Background(), Job{
		Argv: []string{"sh", "-c", `echo hi >&2`}, Deadline: 5 * time.Second, MaxBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Stderr != nil {
		t.Fatalf("Stderr = %q, want nil when no bound is set", out.Stderr)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/proc/ -run 'TestRun(Keeps|Without)' -v`
Expected: FAIL to compile — `unknown field MaxStderrBytes`.

- [ ] **Step 3: Implement**

In `Job`, after `MaxBytes int`:

```go
	// MaxStderrBytes bounds the child's stderr kept on Output.Stderr. Zero
	// discards it, as every caller before the status screen wants; a caller
	// that shows a refusal to a person sets it (status spec §8).
	MaxStderrBytes int
```

In `Output`, after `Complete bool`:

```go
	// Stderr is the first MaxStderrBytes of the child's stderr, nil when the
	// job set no bound. It is returned on every path, errors included.
	Stderr []byte
```

In `Run`, replace `cmd.Stderr = io.Discard` with:

```go
	var stderr *boundedSink
	if job.MaxStderrBytes > 0 {
		stderr = &boundedSink{max: job.MaxStderrBytes}
		cmd.Stderr = stderr
	} else {
		cmd.Stderr = io.Discard
	}
```

and `out := Output{Stdout: sink.bytes()}` with:

```go
	out := Output{Stdout: sink.bytes()}
	if stderr != nil {
		out.Stderr = stderr.bytes()
	}
```

A full stderr sink only truncates: the watcher goroutine selects on `sink.full()` (stdout) only. Read `boundedSink.Write` and confirm it does not return an error that would break the child's pipe when full; if it does, give the stderr sink a variant that keeps accepting and discarding.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/proc/... ./internal/commandnames/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proc/supervisor.go internal/proc/supervisor_stderr_test.go
git commit   # feat(proc): the supervisor keeps a bounded stderr for callers that show a refusal (<bead>)
```

---

### Task 2: the tracker port and origin normalization

**Files:**

- Create: `internal/tracker/tracker.go`, `internal/tracker/origin.go`
- Test: `internal/tracker/origin_test.go`

**Interfaces:**

- Produces (exact; Step 3 carries the declarations): `Status{Hash; DirtyCount; DBNewer; JSONLNewer; Health}`, `Location{ExportPath}`, `Relation{DependsOn; Type}`, `Issue{ID, Title, Type, Status; Labels; Relations; CreatedAt, UpdatedAt}`, `Export{Hash; Issues}`, `Claim{ID; Classification; AgeMinutes}` with `Stale() bool`, `Tracker` (`Status`, `Locate`, `ReadExport(ctx, loc, expectHash)`, `Claims`), `Opener{Open(root) Tracker}`, `ErrNoBr`, `ErrNoWorkspace`, `*HashMismatchError{Want, Got}`, `*ContractError{What, Err}`, `*RefusalError{Command, Stderr, Err}`, `NormalizeOrigin(raw) (string, bool)`.

**Acceptance Criteria:**

- `NormalizeOrigin` maps `git@github.com:shady2k/nocx.git`, `ssh://git@github.com/shady2k/nocx`, `https://github.com/shady2k/nocx.git`, `https://GitHub.com/shady2k/nocx/`, `ssh://git@github.com:22/shady2k/nocx.git` to `github.com/shady2k/nocx`, true; `git@gitlab.com:group/sub/repo.git` to `gitlab.com/group/sub/repo`.
- It refuses `""`, `/srv/repos/local.git`, `file:///x`, `https://github.com/`, `nonsense`.
- `Claim.Stale()` is true for `stale_candidate` and `abandoned_likely` only.

- [ ] **Step 1: Write the failing test**

```go
package tracker

import "testing"

func TestNormalizeOriginGivesOneIdentityForEverySpellingOfARemote(t *testing.T) {
	for _, raw := range []string{
		"git@github.com:shady2k/nocx.git",
		"ssh://git@github.com/shady2k/nocx",
		"https://github.com/shady2k/nocx.git",
		"https://GitHub.com/shady2k/nocx/",
		"ssh://git@github.com:22/shady2k/nocx.git",
	} {
		if got, ok := NormalizeOrigin(raw); !ok || got != "github.com/shady2k/nocx" {
			t.Errorf("NormalizeOrigin(%q) = %q, %v", raw, got, ok)
		}
	}
	if got, ok := NormalizeOrigin("git@gitlab.com:group/sub/repo.git"); !ok || got != "gitlab.com/group/sub/repo" {
		t.Errorf("nested group = %q, %v", got, ok)
	}
}

func TestNormalizeOriginRefusesWhatIsNotAHostedRemote(t *testing.T) {
	for _, raw := range []string{"", "/srv/repos/local.git", "file:///x", "https://github.com/", "nonsense"} {
		if got, ok := NormalizeOrigin(raw); ok {
			t.Errorf("NormalizeOrigin(%q) = %q, true", raw, got)
		}
	}
}

func TestAClaimIsStaleOnlyWhenBrSaysSo(t *testing.T) {
	for class, want := range map[string]bool{"stale_candidate": true, "abandoned_likely": true, "fresh": false, "unassigned": false} {
		if got := (Claim{Classification: class}).Stale(); got != want {
			t.Errorf("Stale(%q) = %v, want %v", class, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tracker/ -v` — Expected: FAIL (no Go files / undefined).

- [ ] **Step 3: Implement `tracker.go`**

```go
// Package tracker is the port the status screen reads a repository's issue
// tracker through (spec §4). Its vocabulary is milestone → epic → feature →
// task; the one adapter today is br's. Structure comes from the tracker's
// export and is trusted only when the bytes read hash to the value the tracker
// itself reported (§4.1): a torn read is an error, never a model.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Status struct {
	Hash       string // jsonl_content_hash: SHA-256 hex of the export file
	DirtyCount int
	DBNewer    bool
	JSONLNewer bool
	Health     string
}

// Location is where the export lives as the tracker reports it — the main
// checkout's, never a worktree's copy.
type Location struct{ ExportPath string }

type Relation struct {
	DependsOn string
	Type      string // "blocks", "parent-child", "related", ...
}

type Issue struct {
	ID, Title, Type, Status string
	Labels                  []string
	Relations               []Relation
	CreatedAt, UpdatedAt    time.Time
}

type Export struct {
	Hash   string
	Issues []Issue
}

type Claim struct {
	ID             string
	Classification string
	AgeMinutes     int
}

// Stale is br's judgement, never nocx's.
func (c Claim) Stale() bool {
	return c.Classification == "stale_candidate" || c.Classification == "abandoned_likely"
}

// Tracker reads one repository's tracker. Every method only reads.
type Tracker interface {
	Status(ctx context.Context) (Status, error)
	Locate(ctx context.Context) (Location, error)
	ReadExport(ctx context.Context, loc Location, expectHash string) (Export, error)
	Claims(ctx context.Context) ([]Claim, error)
}

type Opener interface {
	Open(root string) Tracker
}

var (
	ErrNoBr        = errors.New("tracker: br is not installed on this host")
	ErrNoWorkspace = errors.New("tracker: no br workspace in this repository")
)

// HashMismatchError: the export changed between the status read and the file
// read. The caller discards the bytes and tries on the next poll.
type HashMismatchError struct{ Want, Got string }

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("tracker: export hash %s does not match the reported %s", e.Got, e.Want)
}

// ContractError: the tracker answered in a shape its own schema does not allow.
type ContractError struct {
	What string
	Err  error
}

func (e *ContractError) Error() string { return "tracker: " + e.What + ": " + e.Err.Error() }
func (e *ContractError) Unwrap() error { return e.Err }

// RefusalError carries a command's stderr so a person can read it.
type RefusalError struct {
	Command string
	Stderr  string
	Err     error
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("tracker: %s: %v: %s", e.Command, e.Err, e.Stderr)
}
func (e *RefusalError) Unwrap() error { return e.Err }
```

- [ ] **Step 4: Implement `origin.go`**

```go
package tracker

import (
	"net/url"
	"strings"
)

// NormalizeOrigin reduces a git remote URL to the identity of the repository
// it names — host/owner/repo — so two clones of one project are one project
// (spec §5). frontend/src/git/git-remote-url.ts answers a different question:
// it turns a remote into a web link to open, for three forges, in the
// renderer; this compares identities for any forge in the backend. A local
// path or a file URL has no shared identity and is refused.
func NormalizeOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	var host, path string
	switch {
	case raw == "":
		return "", false
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "file" || u.Hostname() == "" {
			return "", false
		}
		host, path = u.Hostname(), u.Path
	case strings.Contains(raw, ":") && !strings.HasPrefix(raw, "/"):
		colon := strings.Index(raw, ":")
		host = raw[:colon]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		path = raw[colon+1:]
	default:
		return "", false
	}
	path = strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if host == "" || !strings.Contains(path, "/") {
		return "", false
	}
	return strings.ToLower(host) + "/" + path, true
}
```

- [ ] **Step 5: Run the tests** — `go test ./internal/tracker/ -count=1 -v` — Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tracker/tracker.go internal/tracker/origin.go internal/tracker/origin_test.go
git commit   # feat(tracker): the port the status screen reads a tracker through, and one identity per remote (<bead>)
```

---

### Task 3: `brspawn` — the closed argv set and decoders, checked against `br`'s schemas

**Files:**

- Create: `internal/tracker/brspawn/argv.go`, `internal/tracker/brspawn/decode.go`
- Create: `internal/tracker/brspawn/testdata/generate.sh` and what it writes: `sync-status.json`, `sync-status-behind.json`, `info.json`, `coordination.json`, `export.jsonl`, `schema-issue.json`, `br-version.txt`
- Test: `internal/tracker/brspawn/decode_test.go`

**Interfaces:**

- Consumes: Task 2 types.
- Produces: `ReadOnlyFlags() []string`, `SyncStatusArgs()`, `InfoArgs()`, `ClaimsArgs()`; `DecodeStatus([]byte) (tracker.Status, error)`, `DecodeInfo([]byte) (tracker.Location, error)`, `DecodeClaims([]byte) ([]tracker.Claim, error)`, `DecodeExport([]byte) ([]tracker.Issue, error)` (drops `tombstone`).

**Acceptance Criteria:**

- `generate.sh` builds a scratch workspace with real `br` holding: a milestone epic (label `milestone`); stage epics "Stage one" and "Stage two" under it; under stage one "First task" (closed), "Second task" (in_progress), "Unbroken epic" (no children), "Later" (deferred); under stage two "Blocked task" blocked by "Second task" and "Gone" (deleted); "Orphan epic" with no parent. It writes every fixture from `br` output.
- Every `export.jsonl` row validates against `schema-issue.json` → `schemas.Issue` (draft 2020-12).
- `DecodeExport` keeps typed relations and timestamps; "Gone" is absent.
- `DecodeStatus(sync-status.json).Hash` is 64 hex; `DecodeStatus(sync-status-behind.json).JSONLNewer` is true.
- `DecodeInfo` returns a non-empty path; `DecodeClaims` returns the one in-progress claim.
- `{"jsonl_content_hash":"a","dirty_count":"x"}`, a status without a hash, an info without `jsonl_path`, and an export row without `id` each return `*tracker.ContractError`.
- Every argv contains every read-only flag.

- [ ] **Step 1: Write the generator**

```bash
#!/usr/bin/env bash
# Regenerates the brspawn fixtures from REAL br output, so the decoders are
# tested against what br writes, not what a test author believes it writes
# (AGENTS.md testing rule 1; status spec §11.4). Run it when br changes.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
ro=(--no-auto-import --no-auto-flush --allow-stale)
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q
br init --prefix fx >/dev/null

M=$(br create "Ship the thing" -t epic -l milestone --silent)
S1=$(br create "Stage one" -t epic --parent "$M" --silent)
S2=$(br create "Stage two" -t epic --parent "$M" --silent)
T1=$(br create "First task" -t task --parent "$S1" --silent)
T2=$(br create "Second task" -t task --parent "$S1" --silent)
br create "Unbroken epic" -t epic --parent "$S1" --silent >/dev/null
T3=$(br create "Blocked task" -t task --parent "$S2" --silent)
br create "Orphan epic" -t epic --silent >/dev/null
D=$(br create "Later" -t task --parent "$S1" --silent)
X=$(br create "Gone" -t task --parent "$S2" --silent)
br dep add "$T3" "$T2" >/dev/null
br close "$T1" --reason fixture >/dev/null
br update "$T2" --status in_progress >/dev/null
br defer "$D" >/dev/null
br delete "$X" --force >/dev/null 2>&1 || br delete "$X" >/dev/null

br sync --flush-only >/dev/null
br sync --status --json "${ro[@]}" >"$here/sync-status.json"
br info --json "${ro[@]}" >"$here/info.json"
br coordination status --json "${ro[@]}" >"$here/coordination.json"
cp .beads/issues.jsonl "$here/export.jsonl"
br schema issue --json >"$here/schema-issue.json"
br --version >"$here/br-version.txt"

# An export newer than the database: what a git pull leaves behind.
sed '1s/"title":"[^"]*"/"title":"Pulled title"/' .beads/issues.jsonl >.beads/pulled.jsonl
mv .beads/pulled.jsonl .beads/issues.jsonl
touch -d '+1 minute' .beads/issues.jsonl
br sync --status --json "${ro[@]}" >"$here/sync-status-behind.json"
```

Run: `chmod +x internal/tracker/brspawn/testdata/generate.sh && internal/tracker/brspawn/testdata/generate.sh && ls internal/tracker/brspawn/testdata/`
Expected: the seven files. If `sync-status-behind.json` does not show `"jsonl_newer": true`, read `br sync --help` for how staleness is detected (content hash vs mtime) and change the last block until it does; the test pins the behaviour, not the method.

- [ ] **Step 2: Write the failing tests**

```go
package brspawn

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/tracker"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v — run testdata/generate.sh", name, err)
	}
	return raw
}

func issueSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	var envelope struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	}
	if err := json.Unmarshal(fixture(t, "schema-issue.json"), &envelope); err != nil {
		t.Fatalf("schema envelope: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(envelope.Schemas["Issue"]))
	if err != nil {
		t.Fatalf("schema doc: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("br-issue.json", doc); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	s, err := c.Compile("br-issue.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func TestEveryExportRowIsTheIssueObjectBrDocuments(t *testing.T) {
	s := issueSchema(t)
	sc := bufio.NewScanner(bytes.NewReader(fixture(t, "export.jsonl")))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	rows := 0
	for sc.Scan() {
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(sc.Bytes()))
		if err != nil {
			t.Fatalf("row %d: %v", rows, err)
		}
		if err := s.Validate(v); err != nil {
			t.Fatalf("row %d does not match br schema issue: %v", rows, err)
		}
		rows++
	}
	if rows < 10 {
		t.Fatalf("rows = %d, want the generated backlog", rows)
	}
}

func TestDecodeExportKeepsTypedRelationsAndDropsTombstones(t *testing.T) {
	issues, err := DecodeExport(fixture(t, "export.jsonl"))
	if err != nil {
		t.Fatalf("DecodeExport: %v", err)
	}
	byTitle := map[string]tracker.Issue{}
	for _, i := range issues {
		byTitle[i.Title] = i
	}
	if _, ok := byTitle["Gone"]; ok {
		t.Fatal("a deleted issue reached the model")
	}
	m := byTitle["Ship the thing"]
	if m.Type != "epic" || !slices.Contains(m.Labels, "milestone") {
		t.Fatalf("milestone = %+v", m)
	}
	b := byTitle["Blocked task"]
	var kinds []string
	for _, r := range b.Relations {
		kinds = append(kinds, r.Type)
	}
	if !slices.Contains(kinds, "blocks") || !slices.Contains(kinds, "parent-child") {
		t.Fatalf("relations = %+v", b.Relations)
	}
	if b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not decoded: %+v", b)
	}
}

func TestDecodeStatusReadsTheHashAndTheLag(t *testing.T) {
	st, err := DecodeStatus(fixture(t, "sync-status.json"))
	if err != nil || len(st.Hash) != 64 {
		t.Fatalf("DecodeStatus = %+v, %v", st, err)
	}
	behind, err := DecodeStatus(fixture(t, "sync-status-behind.json"))
	if err != nil || !behind.JSONLNewer {
		t.Fatalf("behind = %+v, %v", behind, err)
	}
}

func TestDecodeInfoAndClaims(t *testing.T) {
	if loc, err := DecodeInfo(fixture(t, "info.json")); err != nil || loc.ExportPath == "" {
		t.Fatalf("DecodeInfo = %+v, %v", loc, err)
	}
	claims, err := DecodeClaims(fixture(t, "coordination.json"))
	if err != nil || len(claims) != 1 || claims[0].Classification == "" {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
}

func TestDecodersRefuseWhatBrsSchemaDoesNotAllow(t *testing.T) {
	var ce *tracker.ContractError
	for name, call := range map[string]func() error{
		"status with a string count": func() error { _, err := DecodeStatus([]byte(`{"jsonl_content_hash":"a","dirty_count":"x"}`)); return err },
		"status without a hash":      func() error { _, err := DecodeStatus([]byte(`{"dirty_count":0}`)); return err },
		"info without a path":        func() error { _, err := DecodeInfo([]byte(`{"beads_dir":"/x"}`)); return err },
		"export row without an id":   func() error { _, err := DecodeExport([]byte(`{"title":"t","status":"open"}` + "\n")); return err },
	} {
		if err := call(); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v, want *tracker.ContractError", name, err)
		}
	}
}

func TestEveryArgvIsReadOnly(t *testing.T) {
	for _, argv := range [][]string{SyncStatusArgs(), InfoArgs(), ClaimsArgs()} {
		for _, flag := range ReadOnlyFlags() {
			if !slices.Contains(argv, flag) {
				t.Errorf("%v lacks %s", argv, flag)
			}
		}
	}
}
```

- [ ] **Step 3: Run to verify failure** — `go test ./internal/tracker/brspawn/ -v` — Expected: FAIL to compile.

- [ ] **Step 4: Implement `argv.go`**

```go
// Package brspawn is everything about asking br a question that is shared by
// every implementation that runs br: the closed argv set and the decoding of
// what br writes. Like internal/git/spawn it is linked only by code that runs
// the binary; a relay client never builds argv.
package brspawn

// ReadOnlyFlags keep a read from writing: auto-import would import a pulled
// export into the database, auto-flush would rewrite the export (status spec
// §4.1); allow-stale lets a lagging database answer instead of refusing.
func ReadOnlyFlags() []string {
	return []string{"--no-auto-import", "--no-auto-flush", "--allow-stale"}
}

func SyncStatusArgs() []string {
	return append([]string{"sync", "--status", "--json"}, ReadOnlyFlags()...)
}

func InfoArgs() []string { return append([]string{"info", "--json"}, ReadOnlyFlags()...) }

func ClaimsArgs() []string {
	return append([]string{"coordination", "status", "--json"}, ReadOnlyFlags()...)
}
```

- [ ] **Step 5: Implement `decode.go`**

```go
package brspawn

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/tracker"
)

func contract(what string, err error) error { return &tracker.ContractError{What: what, Err: err} }

func DecodeStatus(raw []byte) (tracker.Status, error) {
	var v struct {
		Hash       *string `json:"jsonl_content_hash"`
		DirtyCount int     `json:"dirty_count"`
		DBNewer    bool    `json:"db_newer"`
		JSONLNewer bool    `json:"jsonl_newer"`
		Health     string  `json:"workspace_health"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return tracker.Status{}, contract("sync --status", err)
	}
	if v.Hash == nil || *v.Hash == "" {
		return tracker.Status{}, contract("sync --status", errors.New("no jsonl_content_hash"))
	}
	return tracker.Status{Hash: *v.Hash, DirtyCount: v.DirtyCount, DBNewer: v.DBNewer, JSONLNewer: v.JSONLNewer, Health: v.Health}, nil
}

func DecodeInfo(raw []byte) (tracker.Location, error) {
	var v struct {
		Path string `json:"jsonl_path"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return tracker.Location{}, contract("info", err)
	}
	if v.Path == "" {
		return tracker.Location{}, contract("info", errors.New("no jsonl_path"))
	}
	return tracker.Location{ExportPath: v.Path}, nil
}

func DecodeClaims(raw []byte) ([]tracker.Claim, error) {
	var v struct {
		Claims []struct {
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
			Assessment struct {
				Classification string `json:"classification"`
				Age            int    `json:"updated_age_minutes"`
			} `json:"assessment"`
		} `json:"claims"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, contract("coordination status", err)
	}
	out := make([]tracker.Claim, 0, len(v.Claims))
	for _, c := range v.Claims {
		if c.Issue.ID == "" {
			return nil, contract("coordination status", errors.New("a claim without an issue id"))
		}
		out = append(out, tracker.Claim{ID: c.Issue.ID, Classification: c.Assessment.Classification, AgeMinutes: c.Assessment.Age})
	}
	return out, nil
}

// exportRow is the subset of br's Issue object the model reads. The schema
// test, not this struct, pins the shape.
type exportRow struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Type         string    `json:"issue_type"`
	Labels       []string  `json:"labels"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Dependencies []struct {
		DependsOn string `json:"depends_on_id"`
		Type      string `json:"type"`
	} `json:"dependencies"`
}

func DecodeExport(raw []byte) ([]tracker.Issue, error) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	out := make([]tracker.Issue, 0, 1024)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r exportRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, contract(fmt.Sprintf("export line %d", line), err)
		}
		if r.ID == "" || r.Status == "" {
			return nil, contract(fmt.Sprintf("export line %d", line), errors.New("a row without id or status"))
		}
		if r.Status == "tombstone" {
			continue
		}
		rels := make([]tracker.Relation, 0, len(r.Dependencies))
		for _, d := range r.Dependencies {
			rels = append(rels, tracker.Relation{DependsOn: d.DependsOn, Type: d.Type})
		}
		if r.Labels == nil {
			r.Labels = []string{}
		}
		out = append(out, tracker.Issue{ID: r.ID, Title: r.Title, Type: r.Type, Status: r.Status,
			Labels: r.Labels, Relations: rels, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	if err := sc.Err(); err != nil {
		return nil, contract("export", err)
	}
	return out, nil
}
```

- [ ] **Step 6: Run the tests** — `go test ./internal/tracker/... -count=1 -v` — Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tracker/brspawn
git commit   # feat(tracker): br's read-only argv and decoders, pinned by fixtures br itself wrote (<bead>)
```

---

### Task 4: the local adapter, a verified export read, and the read-only proof

**Files:**

- Create: `internal/tracker/local/local.go`
- Test: `internal/tracker/local/local_test.go`

**Interfaces:**

- Consumes: `proc.Supervisor`, `proc.Job{…, MaxStderrBytes}` (Task 1); `brspawn` (Task 3); `tracker` (Task 2).
- Produces: `NewOpener() *Opener`; `(*Opener).Open(root string) tracker.Tracker`; constants `CommandDeadline = 10 * time.Second`, `MaxCommandBytes = 1 << 20`, `MaxExportBytes = 256 << 20`, `MaxStderrBytes = 8 << 10`.

**Acceptance Criteria:**

- Against a fresh `br` workspace: `Status` gives a 64-hex hash; `Locate` gives `.beads/issues.jsonl`; `ReadExport(loc, hash)` returns the issues with `Export.Hash == hash`; `Claims` returns without error.
- `ReadExport` with a wrong `expectHash` returns `*tracker.HashMismatchError` and no issues.
- An export over the bound returns an error and no issues.
- With no `br` on the child's `PATH`, `Status`, `Locate`, `Claims` return `tracker.ErrNoBr`.
- In a git repo without `.beads`, `Status` returns `tracker.ErrNoWorkspace`.
- **Read-only proof:** after `Status`, `Locate`, `ReadExport`, `Claims` against a workspace — current, and with the export rewritten newer than the database — `br sync --status` reports the same hash and dirty count, and `beads.db`'s modification time is unchanged.
- Tests needing `br` call `requireBr(t)`, which skips naming the CI bead filed in Step 6.

- [ ] **Step 1: Write the failing tests**

```go
package local

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/tracker"
	"github.com/shady2k/nocx/internal/tracker/brspawn"
)

func requireBr(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br is not installed; CI installs it under <CI bead from Step 6>")
	}
}

func brWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"git", "init", "-q"},
		{"br", "init", "--prefix", "lt"},
		{"br", "create", "Ship", "-t", "epic", "-l", "milestone", "--silent"},
		{"br", "sync", "--flush-only"},
	} {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", argv, err, out)
		}
	}
	return dir
}

func brStatus(t *testing.T, dir string) tracker.Status {
	t.Helper()
	cmd := exec.Command("br", brspawn.SyncStatusArgs()...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("br sync --status: %v", err)
	}
	st, err := brspawn.DecodeStatus(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return st
}

func TestTheLocalTrackerReadsAWorkspaceEndToEnd(t *testing.T) {
	requireBr(t)
	dir := brWorkspace(t)
	tr := NewOpener().Open(dir)
	ctx := context.Background()
	st, err := tr.Status(ctx)
	if err != nil || len(st.Hash) != 64 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	loc, err := tr.Locate(ctx)
	if err != nil || !strings.HasSuffix(loc.ExportPath, filepath.Join(".beads", "issues.jsonl")) {
		t.Fatalf("Locate = %+v, %v", loc, err)
	}
	exp, err := tr.ReadExport(ctx, loc, st.Hash)
	if err != nil || len(exp.Issues) != 1 || exp.Hash != st.Hash {
		t.Fatalf("ReadExport = %+v, %v", exp, err)
	}
	if _, err := tr.Claims(ctx); err != nil {
		t.Fatalf("Claims: %v", err)
	}
}

func TestAnExportThatDoesNotHashToTheReportedValueIsNeverParsed(t *testing.T) {
	requireBr(t)
	tr := NewOpener().Open(brWorkspace(t))
	loc, err := tr.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	exp, err := tr.ReadExport(context.Background(), loc, strings.Repeat("0", 64))
	var mismatch *tracker.HashMismatchError
	if !errors.As(err, &mismatch) || len(exp.Issues) != 0 {
		t.Fatalf("ReadExport = %+v, %v", exp, err)
	}
}

func TestAnExportOverItsBoundIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := &brTracker{o: &Opener{maxExport: 16}}
	if _, err := tr.ReadExport(context.Background(), tracker.Location{ExportPath: path}, "x"); err == nil {
		t.Fatal("an export over its bound was read")
	}
}

func TestWithoutBrOnPathEveryMethodSaysSo(t *testing.T) {
	tr := (&Opener{env: []string{"PATH=" + t.TempDir()}}).Open(t.TempDir())
	ctx := context.Background()
	if _, err := tr.Status(ctx); !errors.Is(err, tracker.ErrNoBr) {
		t.Errorf("Status: %v", err)
	}
	if _, err := tr.Locate(ctx); !errors.Is(err, tracker.ErrNoBr) {
		t.Errorf("Locate: %v", err)
	}
	if _, err := tr.Claims(ctx); !errors.Is(err, tracker.ErrNoBr) {
		t.Errorf("Claims: %v", err)
	}
}

func TestARepositoryWithoutAWorkspaceIsNotATracker(t *testing.T) {
	requireBr(t)
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, err := NewOpener().Open(dir).Status(context.Background()); !errors.Is(err, tracker.ErrNoWorkspace) {
		t.Fatalf("Status = %v, want ErrNoWorkspace", err)
	}
}

func TestReadingATrackerWritesNothing(t *testing.T) {
	requireBr(t)
	for name, prepare := range map[string]func(t *testing.T, dir string){
		"a current export": func(*testing.T, string) {},
		"an export newer than its database": func(t *testing.T, dir string) {
			p := filepath.Join(dir, ".beads", "issues.jsonl")
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(strings.Replace(string(raw), `"title":"Ship"`, `"title":"Pulled"`, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := brWorkspace(t)
			prepare(t, dir)
			db := filepath.Join(dir, ".beads", "beads.db")
			before, dbBefore := brStatus(t, dir), mustStat(t, db)
			tr := NewOpener().Open(dir)
			ctx := context.Background()
			st, _ := tr.Status(ctx)
			loc, _ := tr.Locate(ctx)
			_, _ = tr.ReadExport(ctx, loc, st.Hash)
			_, _ = tr.Claims(ctx)
			after := brStatus(t, dir)
			if after.Hash != before.Hash || after.DirtyCount != before.DirtyCount {
				t.Fatalf("status moved: before %+v after %+v", before, after)
			}
			if !mustStat(t, db).ModTime().Equal(dbBefore.ModTime()) {
				t.Fatal("the database was written")
			}
		})
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/tracker/local/ -v` — Expected: FAIL to compile.

- [ ] **Step 3: Confirm br's "no workspace" wording**

Run: `cd "$(mktemp -d)" && git init -q && br sync --status --json --no-auto-import --no-auto-flush --allow-stale; echo "exit=$?"`
Expected (br 0.5.10): non-zero exit, stderr `Error: Beads not initialized: run 'br init' first`. Match on the observed text in Step 4.

- [ ] **Step 4: Implement `local.go`**

```go
// Package local runs br on this machine for the status screen, through the
// shared proc.Supervisor (no shell, own process group, bounded output, a
// deadline). It never opens br's database: structure comes from the export,
// parsed only when its SHA-256 equals the hash br reported (status spec §4.1).
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/proc"
	"github.com/shady2k/nocx/internal/tracker"
	"github.com/shady2k/nocx/internal/tracker/brspawn"
)

const (
	CommandDeadline = 10 * time.Second
	MaxCommandBytes = 1 << 20
	MaxExportBytes  = 256 << 20
	MaxStderrBytes  = 8 << 10
)

type Opener struct {
	sup       proc.Supervisor
	env       []string
	maxExport int64
}

func NewOpener() *Opener {
	return &Opener{sup: proc.Supervisor{Clock: proc.RealClock{}}, env: os.Environ(), maxExport: MaxExportBytes}
}

func (o *Opener) Open(root string) tracker.Tracker { return &brTracker{o: o, root: root} }

type brTracker struct {
	o    *Opener
	root string
}

var _ tracker.Tracker = (*brTracker)(nil)

// resolveBr looks br up on the CHILD's PATH, as internal/git/local resolves
// git: the lookup must use the environment the child runs with.
func resolveBr(env []string) (string, error) {
	path := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		c := filepath.Join(dir, "br")
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return c, nil
		}
	}
	return "", exec.ErrNotFound
}

func (t *brTracker) run(ctx context.Context, what string, args []string) ([]byte, error) {
	bin, err := resolveBr(t.o.env)
	if err != nil {
		return nil, tracker.ErrNoBr
	}
	out, err := t.o.sup.Run(ctx, proc.Job{
		Argv: append([]string{bin}, args...), Env: t.o.env, Dir: t.root,
		Deadline: CommandDeadline, MaxBytes: MaxCommandBytes, MaxStderrBytes: MaxStderrBytes,
	})
	if err != nil {
		stderr := string(out.Stderr)
		if strings.Contains(stderr, "not initialized") {
			return nil, tracker.ErrNoWorkspace
		}
		return nil, &tracker.RefusalError{Command: what, Stderr: stderr, Err: err}
	}
	return out.Stdout, nil
}

func (t *brTracker) Status(ctx context.Context) (tracker.Status, error) {
	raw, err := t.run(ctx, "br sync --status", brspawn.SyncStatusArgs())
	if err != nil {
		return tracker.Status{}, err
	}
	return brspawn.DecodeStatus(raw)
}

func (t *brTracker) Locate(ctx context.Context) (tracker.Location, error) {
	raw, err := t.run(ctx, "br info", brspawn.InfoArgs())
	if err != nil {
		return tracker.Location{}, err
	}
	return brspawn.DecodeInfo(raw)
}

func (t *brTracker) Claims(ctx context.Context) ([]tracker.Claim, error) {
	raw, err := t.run(ctx, "br coordination status", brspawn.ClaimsArgs())
	if err != nil {
		return nil, err
	}
	return brspawn.DecodeClaims(raw)
}

func (t *brTracker) ReadExport(_ context.Context, loc tracker.Location, expectHash string) (tracker.Export, error) {
	f, err := os.Open(loc.ExportPath)
	if err != nil {
		return tracker.Export{}, fmt.Errorf("tracker: open export: %w", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, t.o.maxExport+1))
	if err != nil {
		return tracker.Export{}, fmt.Errorf("tracker: read export: %w", err)
	}
	if int64(len(raw)) > t.o.maxExport {
		return tracker.Export{}, fmt.Errorf("tracker: export exceeds %d bytes", t.o.maxExport)
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != expectHash {
		return tracker.Export{}, &tracker.HashMismatchError{Want: expectHash, Got: got}
	}
	issues, err := brspawn.DecodeExport(raw)
	if err != nil {
		return tracker.Export{}, err
	}
	return tracker.Export{Hash: got, Issues: issues}, nil
}
```

- [ ] **Step 5: Run the tests** — `go test ./internal/tracker/... -count=1 -v` — Expected: PASS.

- [ ] **Step 6: File the CI bead the skip names (coordinator)**

```bash
br create "CI carries a pinned br, so the tracker's real-binary tests run instead of skipping" -t chore -p 2 -l infra --parent nocx-ss9gh.1 --silent
```

Put the printed id into `requireBr`'s skip message, then commit.

- [ ] **Step 7: Commit**

```bash
git add internal/tracker/local
git commit   # feat(tracker): read br locally, parse the export only when it hashes to what br reported (<bead>)
```

---

### Task 5: the git plane reports `origin`

`Repo.RemoteURL` follows the current branch's upstream, not `origin` (spec §4.5). The local `remoteURL(ctx, remote)` already runs `git remote get-url`; extend that answer across the plane.

**Files:**

- Modify: `internal/git/git.go` (`Repo`), `internal/git/local/remote.go`, `internal/git/hostsvc/hostsvc.go` (`Ops`, `ParamsSchema`, `Call`, handler), `internal/git/helper/factory.go`, `internal/git/registry/registry.go` (`Handle`, `*handle`), `internal/helper/proto/version.go`
- Modify stubs: `internal/git/registry/registry_test.go` (`*stubRepo`), `internal/git/hostsvc/hostsvc_test.go` (`*stubRepo`), `internal/transport/ws_git_test.go` (`*stubGitRepo`)
- Test: `internal/git/local/origin_test.go`; a round-trip test in `internal/git/helper/repo_test.go`

**Interfaces:**

- Produces: `git.Repo.OriginURL(ctx) (string, error)` — `*git.ErrNoRemote` when there is no `origin`; the same on `registry.Handle`; helper op `"originURL"` with `hostsvc.BindingParams`; `proto.Version = "15"`.

**Acceptance Criteria:**

- With `origin = git@github.com:o/r.git` and the branch tracking `fork = git@github.com:me/r.git`: `RemoteURL` is the fork's URL, `OriginURL` is origin's.
- Without `origin`, `OriginURL` returns `*git.ErrNoRemote`.
- Through the helper round trip (`openHelper`), `OriginURL` equals the local answer.
- `proto.Version == "15"`, with a comment paragraph saying why.
- `go build ./... && go test ./internal/git/... ./internal/helper/... -count=1 && go test ./internal/transport/ -run Git -count=1` passes.

- [ ] **Step 1: Write the failing local test**

Read `internal/git/local/fixture_test.go` first and use its helpers with their real signatures (`realGitPath`, `gitEnv`, `newGitRepo`, `openRepo`, `gitCommit`, `commandIn`) and the repo's default branch name. The assertions:

```go
package local

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/git"
)

func TestOriginURLIsOriginEvenWhenTheBranchTracksAnotherRemote(t *testing.T) {
	realGitPath(t)
	env := gitEnv(t)
	dir := newGitRepo(t, env)
	gitCommit(t, env, dir, "a.txt", "one", "first")
	branch := currentBranch(t, env, dir) // write with `git symbolic-ref --short HEAD` via commandIn
	commandIn(t, env, dir, "git", "remote", "add", "origin", "git@github.com:o/r.git")
	commandIn(t, env, dir, "git", "remote", "add", "fork", "git@github.com:me/r.git")
	commandIn(t, env, dir, "git", "config", "branch."+branch+".remote", "fork")
	commandIn(t, env, dir, "git", "config", "branch."+branch+".merge", "refs/heads/"+branch)

	repo := openRepo(t, env, dir)
	ctx := context.Background()
	if got, err := repo.RemoteURL(ctx); err != nil || got != "git@github.com:me/r.git" {
		t.Fatalf("RemoteURL = %q, %v; want the tracked fork", got, err)
	}
	if got, err := repo.OriginURL(ctx); err != nil || got != "git@github.com:o/r.git" {
		t.Fatalf("OriginURL = %q, %v; want origin", got, err)
	}
}

func TestOriginURLWithoutOriginIsNoRemote(t *testing.T) {
	realGitPath(t)
	env := gitEnv(t)
	repo := openRepo(t, env, newGitRepo(t, env))
	var noRemote *git.ErrNoRemote
	if _, err := repo.OriginURL(context.Background()); !errors.As(err, &noRemote) {
		t.Fatalf("OriginURL = %v, want ErrNoRemote", err)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/git/local/ -run OriginURL -v` — Expected: FAIL to compile (`OriginURL undefined`).

- [ ] **Step 3: Implement across the plane**

`internal/git/git.go`, in `Repo` after `RemoteURL`:

```go
	// OriginURL is the URL of the remote named origin — the repository's
	// identity for the status screen. It is not RemoteURL, which follows the
	// current branch's upstream. No origin is *ErrNoRemote.
	OriginURL(ctx context.Context) (string, error)
```

`internal/git/local/remote.go`:

```go
func (r *Repo) OriginURL(ctx context.Context) (string, error) {
	url, err := r.remoteURL(ctx, "origin")
	if err != nil {
		return "", err
	}
	if url == "" {
		return "", &git.ErrNoRemote{}
	}
	return url, nil
}
```

`internal/git/hostsvc/hostsvc.go`: add `"originURL"` to `Ops()` after `"remoteURL"`, and to the `BindingParams` case of `ParamsSchema`; add `case "originURL": return s.originURL(ctx, params)` to `Call`; the handler is `remoteURL`'s body with `repo.OriginURL(ctx)`:

```go
func (s *Service) originURL(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	url, err := repo.OriginURL(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return url, nil
}
```

`internal/git/helper/factory.go`:

```go
func (r *repo) OriginURL(ctx context.Context) (string, error) {
	var url string
	if err := r.f.client.Call(ctx, "git", "originURL",
		hostsvc.BindingParams{BindingID: r.bindingID}, &url); err != nil {
		return "", classifyRefusal(err)
	}
	return url, nil
}
```

`internal/git/registry/registry.go`: add `OriginURL(ctx context.Context) (string, error)` to `Handle`; on `*handle`, copy its `RemoteURL` method and call `OriginURL`.

Each stub gains `OriginURL(context.Context) (string, error) { return "", &git.ErrNoRemote{} }` on its own receiver.

`internal/helper/proto/version.go`: `const Version = "15"`, and before it append:

```go
// This is 15 rather than 14 because the git service grew `originURL`
// (nocx-ss9gh.1): the status screen identifies a repository by its origin,
// which `remoteURL` does not return — that op follows the branch's upstream.
// An older helper answers `unknown_op`; the version makes that a refused
// handshake rather than a screen that silently has no origin.
```

- [ ] **Step 4: Add the helper round trip**

In `internal/git/helper/repo_test.go`, next to `TestHelperRepoStatusMatchesLocal`, add `TestHelperRepoOriginURLMatchesLocal`: copy that test's setup verbatim, add `git remote add origin git@github.com:o/r.git` to the fixture repo, and assert the helper repo's `OriginURL` equals the local repo's.

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go test ./internal/git/... ./internal/helper/... -count=1 && go test ./internal/transport/ -run Git -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/git internal/helper/proto/version.go internal/transport/ws_git_test.go
git commit   # feat(git): the plane reports origin, which is not the tracked remote (<bead>)
```

---

### Task 6: `content.db` 18 → 19 — status locations, hidden projects, distinct working directories

**Files:**

- Modify: `internal/content/sqlite.go` (`schemaVersion`, `schemaV1`), `internal/content/schema_migrate.go` (ladder, digests, object names), `internal/content/content.go`, `internal/content/stub.go`
- Create: `internal/content/status_location_sqlite.go`, `internal/content/ledger_distinct.go`, `internal/content/testdata/schema_v18.sql`
- Test: `internal/content/schema_migrate_18to19_test.go`, `internal/content/status_location_test.go`, `internal/content/ledger_distinct_test.go`

**Interfaces:**

- Produces:

```go
type StatusLocationState string

const (
	StatusLocationPresent     StatusLocationState = "present"
	StatusLocationGone        StatusLocationState = "gone"
	StatusLocationUnreachable StatusLocationState = "unreachable"
	StatusLocationNoHelper    StatusLocationState = "no_helper"
	StatusLocationNoBr        StatusLocationState = "no_br"
)

type StatusLocation struct {
	Origin    string
	Host      string // "" for this machine
	Path      string
	Manual    bool
	FirstSeen int64 // unix ms
	LastSeen  int64 // unix ms
	State     StatusLocationState
}

type StatusLocationRepository interface {
	Upsert(ctx context.Context, loc StatusLocation) error
	List(ctx context.Context) ([]StatusLocation, error)
	Delete(ctx context.Context, origin, host, path string) error
	SetHidden(ctx context.Context, origin string, hidden bool, at int64) error
	Hidden(ctx context.Context) (map[string]int64, error)
}

// ContentDB gains: StatusLocations() StatusLocationRepository

type CwdSeen struct {
	Environment *Environment
	Cwd         string
	LastSeen    int64 // newest submitted_at for the pair
}

// LedgerRepository gains: DistinctCwds(ctx context.Context, limit int) ([]CwdSeen, error)

const MaxDistinctCwds = 500
```

**Acceptance Criteria:**

- `schemaVersion == 19`; `schemaV1` creates `status_locations` (PK `origin, host, path`) and `status_hidden` (PK `origin`), both `STRICT`.
- Ladder rung `{from: 18, to: 19, apply: migrateAddStatusTables18to19, schemaDigest: sha256(schemaV1)}`; `schemaShapeDigests[18]` holds the 17→18 rung's former digest; `historicalSchemaObjectNames[18]` set; `testdata/schema_v18.sql` frozen from `cc63918b`.
- A released schema-18 database with an environment row opens, migrates to 19, keeps the row, and accepts `StatusLocations().Upsert`.
- `Upsert` twice for one key keeps the first `FirstSeen` and takes `LastSeen`/`State` from the second; `Delete` removes; `SetHidden(true)` shows in `Hidden()`; `SetHidden(false)` removes.
- `DistinctCwds` over entries `(local,/a,1)`, `(local,/b,2)`, `(local,/a,3)`, `(ssh,/a,4)`, `(local,"",5)` returns `(ssh,/a,4)`, `(local,/a,3)`, `(local,/b,2)` with joined environments; `limit` outside `[1, MaxDistinctCwds]` is an error.
- The stub's `StatusLocations()` methods and ledger `DistinctCwds` return `ErrNotImplemented`.
- `go test ./internal/content/ -count=1` passes, existing ladder tests included.

- [ ] **Step 1: Freeze the released 18 schema**

Look at the first and last lines of `internal/content/testdata/schema_v17.sql` and at `releasedSchema` in `schema_migrate_released_test.go` to see the exact expected shape, then extract the same span from the released tree:

```bash
git show cc63918b:internal/content/sqlite.go > /tmp/sqlite_v18.go
# copy the body of `const schemaV1 = \`...\`` from /tmp/sqlite_v18.go into
# internal/content/testdata/schema_v18.sql in the same shape as schema_v17.sql
grep -c "CREATE TABLE" internal/content/testdata/schema_v18.sql
```

Expected: the table count equals `schemaV1`'s at `cc63918b`.

- [ ] **Step 2: Write the failing tests**

Read `schema_migrate_17to18_test.go` for `rawExec`, `rawCount`, `rawUserVersion` signatures and the environments columns; read `ledger_test.go` for how a completed command is recorded (`EnsureEnvironment`, `RecordCompleted`, `CompletedCommand` fields). Then:

`schema_migrate_18to19_test.go`:

```go
package content

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func aReleasedSchema18Database(t *testing.T, path string) {
	t.Helper()
	rawExec(t, path,
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		releasedSchema(t, 18),
		`INSERT INTO environments (id, kind, endpoint, profile_id, first_seen, payload) VALUES ('env-local', 'local', NULL, NULL, 1, '{}')`,
		"PRAGMA user_version=18",
	)
}

func TestAReleasedSchema18DatabaseMigratesAndTakesStatusLocations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema18Database(t, path)
	db, err := Open(context.Background(), Config{Path: path, Key: schemaTestKey(), Budget: testBudgetInternal(), Logger: log.NewSlogAdapter(nil)})
	if err != nil {
		t.Fatalf("Open over a released schema 18 database: %v — a version behind must migrate, not refuse", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := rawUserVersion(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if n := rawCount(t, path, "SELECT count(*) FROM environments"); n != 1 {
		t.Fatalf("environments = %d, want the row that was there", n)
	}
	if err := db.StatusLocations().Upsert(context.Background(), StatusLocation{Origin: "github.com/o/r", Path: "/r", FirstSeen: 1, LastSeen: 1, State: StatusLocationPresent}); err != nil {
		t.Fatalf("Upsert after migration: %v", err)
	}
}
```

`status_location_test.go`:

```go
package content

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStatusLocationsKeepFirstSeenAndTakeTheRestFromTheLatestUpsert(t *testing.T) {
	db, err := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	repo, ctx := db.StatusLocations(), context.Background()
	first := StatusLocation{Origin: "github.com/o/r", Path: "/r", FirstSeen: 10, LastSeen: 10, State: StatusLocationPresent}
	second := StatusLocation{Origin: "github.com/o/r", Path: "/r", FirstSeen: 99, LastSeen: 20, State: StatusLocationUnreachable}
	for _, l := range []StatusLocation{first, second} {
		if err := repo.Upsert(ctx, l); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	got, err := repo.List(ctx)
	if err != nil || len(got) != 1 || got[0].FirstSeen != 10 || got[0].LastSeen != 20 || got[0].State != StatusLocationUnreachable {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if err := repo.Delete(ctx, "github.com/o/r", "", "/r"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := repo.List(ctx); len(got) != 0 {
		t.Fatalf("after Delete = %+v", got)
	}
	if err := repo.SetHidden(ctx, "github.com/o/r", true, 5); err != nil {
		t.Fatalf("SetHidden: %v", err)
	}
	if hidden, _ := repo.Hidden(ctx); hidden["github.com/o/r"] != 5 {
		t.Fatalf("Hidden = %v", hidden)
	}
	if err := repo.SetHidden(ctx, "github.com/o/r", false, 6); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	if hidden, _ := repo.Hidden(ctx); len(hidden) != 0 {
		t.Fatalf("Hidden after unhide = %v", hidden)
	}
}
```

`ledger_distinct_test.go`:

```go
package content

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDistinctCwdsIsOneRowPerEnvironmentAndDirectoryNewestFirst(t *testing.T) {
	db, err := openTestStore(t, filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	local := Environment{ID: EnvironmentIDFor(EnvLocal, ""), Kind: EnvLocal}
	host := "build.example.com"
	remote := Environment{ID: EnvironmentIDFor(EnvSSH, host), Kind: EnvSSH, Endpoint: &host}
	for _, c := range []struct {
		env Environment
		cwd string
		at  int64
	}{{local, "/a", 1}, {local, "/b", 2}, {local, "/a", 3}, {remote, "/a", 4}, {local, "", 5}} {
		recordCompletedAt(t, db, c.env, c.cwd, c.at)
	}
	got, err := db.Ledger().DistinctCwds(ctx, 10)
	if err != nil {
		t.Fatalf("DistinctCwds: %v", err)
	}
	want := []struct {
		kind EnvironmentKind
		cwd  string
		at   int64
	}{{EnvSSH, "/a", 4}, {EnvLocal, "/a", 3}, {EnvLocal, "/b", 2}}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v", got)
	}
	for i, w := range want {
		if got[i].Environment == nil || got[i].Environment.Kind != w.kind || got[i].Cwd != w.cwd || got[i].LastSeen != w.at {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
	if _, err := db.Ledger().DistinctCwds(ctx, 0); err == nil {
		t.Fatal("limit 0 accepted")
	}
}
```

Write `recordCompletedAt(t, db, env, cwd, at)` in the same file from the `RecordCompleted` call `ledger_test.go` makes, with `SubmittedAt: at` and the environment ensured first.

- [ ] **Step 3: Run to verify failure** — `go test ./internal/content/ -run 'StatusLocation|DistinctCwds|Schema18' -v` — Expected: FAIL to compile.

- [ ] **Step 4: Implement the schema and the rung**

`sqlite.go`: `const schemaVersion = 19`; extend the version comment "19 added status_locations and status_hidden (nocx-ss9gh.1)". Append to `schemaV1` after `skill_checks`:

```sql
CREATE TABLE IF NOT EXISTS status_locations (
  origin      TEXT NOT NULL,
  host        TEXT NOT NULL DEFAULT '',
  path        TEXT NOT NULL,
  manual      INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  state       TEXT NOT NULL CHECK (state IN ('present','gone','unreachable','no_helper','no_br')),
  PRIMARY KEY (origin, host, path)
) STRICT;

CREATE TABLE IF NOT EXISTS status_hidden (
  origin     TEXT PRIMARY KEY,
  hidden_at  INTEGER NOT NULL
) STRICT;
```

`schema_migrate.go`: move the 17→18 rung's `schemaDigest` value into `schemaShapeDigests[18]` (with a comment "18 is pinned in the commit that dethroned it: testdata/schema_v18.sql"), remove `schemaDigest` from that rung, and append:

```go
	{from: 18, to: 19, apply: migrateAddStatusTables18to19, schemaDigest: "<paste>"},
```

Get `<paste>` from `go test ./internal/content/ -run TestTheLadderIsAContiguousChainEndingAtTheCurrentSchema -v`, whose failure prints the expected digest. Add `18: schema18ObjectNames(),` to `historicalSchemaObjectNames` and:

```go
func schema18ObjectNames() map[string]struct{} {
	// 18 rebuilt executions with a wider CHECK; if validateLadderForSchema
	// reports a different object set for 18, use what it reports.
	return schema17ObjectNames()
}

func migrateAddStatusTables18to19(ctx context.Context, tx *sql.Tx) error {
	for i, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS status_locations (
  origin      TEXT NOT NULL,
  host        TEXT NOT NULL DEFAULT '',
  path        TEXT NOT NULL,
  manual      INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  state       TEXT NOT NULL CHECK (state IN ('present','gone','unreachable','no_helper','no_br')),
  PRIMARY KEY (origin, host, path)
) STRICT`,
		`CREATE TABLE IF NOT EXISTS status_hidden (
  origin     TEXT PRIMARY KEY,
  hidden_at  INTEGER NOT NULL
) STRICT`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("add status tables, statement %d: %w", i, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Implement the repository and the query**

`content.go`: the types and interface from **Interfaces**; `StatusLocations() StatusLocationRepository` in `ContentDB`; `DistinctCwds` in `LedgerRepository` with the comment "one row per (environment, cwd), newest first; the status screen's candidates (status spec §5)".

`status_location_sqlite.go`:

```go
package content

import (
	"context"
	"errors"
	"fmt"
)

type statusLocationSqlite struct{ s *sqliteContent }

var _ StatusLocationRepository = (*statusLocationSqlite)(nil)

func (s *sqliteContent) StatusLocations() StatusLocationRepository { return &statusLocationSqlite{s: s} }

func (r *statusLocationSqlite) Upsert(ctx context.Context, l StatusLocation) error {
	if l.Origin == "" || l.Path == "" {
		return errors.New("content: status location: origin and path are required")
	}
	manual := 0
	if l.Manual {
		manual = 1
	}
	return r.s.run(ctx, func(ctx context.Context) error {
		if _, err := r.s.db.ExecContext(ctx, `INSERT INTO status_locations
			(origin, host, path, manual, first_seen, last_seen, state) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(origin, host, path) DO UPDATE SET
				manual    = max(status_locations.manual, excluded.manual),
				last_seen = excluded.last_seen,
				state     = excluded.state`,
			l.Origin, l.Host, l.Path, manual, l.FirstSeen, l.LastSeen, string(l.State)); err != nil {
			return fmt.Errorf("content: upsert status location: %w", err)
		}
		return nil
	})
}

func (r *statusLocationSqlite) List(ctx context.Context) ([]StatusLocation, error) {
	rows, err := r.s.db.QueryContext(ctx, `SELECT origin, host, path, manual, first_seen, last_seen, state
		FROM status_locations ORDER BY origin, last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("content: list status locations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]StatusLocation, 0)
	for rows.Next() {
		var l StatusLocation
		var manual int
		var state string
		if err := rows.Scan(&l.Origin, &l.Host, &l.Path, &manual, &l.FirstSeen, &l.LastSeen, &state); err != nil {
			return nil, fmt.Errorf("content: scan status location: %w", err)
		}
		l.Manual, l.State = manual == 1, StatusLocationState(state)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *statusLocationSqlite) Delete(ctx context.Context, origin, host, path string) error {
	return r.s.run(ctx, func(ctx context.Context) error {
		_, err := r.s.db.ExecContext(ctx, `DELETE FROM status_locations WHERE origin = ? AND host = ? AND path = ?`, origin, host, path)
		return err
	})
}

func (r *statusLocationSqlite) SetHidden(ctx context.Context, origin string, hidden bool, at int64) error {
	return r.s.run(ctx, func(ctx context.Context) error {
		if hidden {
			_, err := r.s.db.ExecContext(ctx, `INSERT INTO status_hidden (origin, hidden_at) VALUES (?, ?)
				ON CONFLICT(origin) DO UPDATE SET hidden_at = excluded.hidden_at`, origin, at)
			return err
		}
		_, err := r.s.db.ExecContext(ctx, `DELETE FROM status_hidden WHERE origin = ?`, origin)
		return err
	})
}

func (r *statusLocationSqlite) Hidden(ctx context.Context) (map[string]int64, error) {
	rows, err := r.s.db.QueryContext(ctx, `SELECT origin, hidden_at FROM status_hidden`)
	if err != nil {
		return nil, fmt.Errorf("content: list hidden: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64)
	for rows.Next() {
		var origin string
		var at int64
		if err := rows.Scan(&origin, &at); err != nil {
			return nil, err
		}
		out[origin] = at
	}
	return out, rows.Err()
}
```

`ledger_distinct.go` (put the method on the receiver `Ledger()` returns — check `ledger_sqlite.go`):

```go
package content

import (
	"context"
	"fmt"
)

const MaxDistinctCwds = 500

func (s *sqliteContent) DistinctCwds(ctx context.Context, limit int) ([]CwdSeen, error) {
	if limit < 1 || limit > MaxDistinctCwds {
		return nil, fmt.Errorf("content: distinct cwds: limit %d outside [1, %d]", limit, MaxDistinctCwds)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+environmentColumns+`, e.cwd, max(e.submitted_at) AS last_seen
		FROM entries e `+environmentJoin+`
		WHERE e.cwd != ''
		GROUP BY e.environment_id, e.cwd
		ORDER BY last_seen DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("content: distinct cwds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]CwdSeen, 0, limit)
	for rows.Next() {
		var env environmentScan
		var c CwdSeen
		if err := rows.Scan(append(env.dest(), &c.Cwd, &c.LastSeen)...); err != nil {
			return nil, fmt.Errorf("content: scan distinct cwd: %w", err)
		}
		c.Environment = env.value()
		out = append(out, c)
	}
	return out, rows.Err()
}
```

`stub.go`: `StatusLocations()` returning a `statusLocationStub{log}` whose methods log and return `ErrNotImplemented`, and `DistinctCwds` on the ledger stub returning `nil, ErrNotImplemented`, mirroring `SkillChecks`.

- [ ] **Step 6: Run the tests** — `go build ./... && go test ./internal/content/ -count=1` — Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/content
git commit   # feat(content): schema 19 keeps status locations and hidden projects, and the ledger lists distinct working directories (<bead>)
```

---

### Task 7: the status model — milestones, stages, tree, one graph level, events

Pure functions over `[]tracker.Issue`, counting as `scripts/feature-status.sh` counts (Global Constraints).

**Files:**

- Create: `internal/status/model.go`
- Test: `internal/status/model_test.go` (reads `../tracker/brspawn/testdata/export.jsonl`)

**Interfaces:**

- Consumes: `tracker.Issue`, `tracker.Claim`; `brspawn.DecodeExport` (tests).
- Produces (declared in Step 3): `StageState` (`not_started`, `in_progress`, `done`); `Progress{Closed, Total}`; `Stage{ID, Title, State, Tasks, NotDecomposed, InProgress, BlockedOutside}`; `Milestone{ID, Title, Status, Tasks, StagesDone, Stages, NotDecomposed, InProgress, Stale}`; `Overview{Milestones, OutsideMilestones}`; `Node{ID, Title, Type, Status, Tasks, NotDecomposed, BlockedBy, HasChildren}`; `GraphNode{Node; External; Stage; Upstream}`; `Edge{From, To}` (From blocks To); `Graph{Parent, Nodes, Edges}`; `EventKind` (`created`, `taken`, `closed`, `reopened`, `deferred`, `deleted`, `blocked`, `unblocked`); `Change{IssueID, Title, Kind}`; `NewIndex`, `(*Index).Overview(claims)`, `.Children(parent)`, `.Level(parent)`, `.Has(id)`, `Diff(prev, next)`, `ErrUnknownIssue`.

**Acceptance Criteria (fixture backlog of Task 3):**

- One milestone, stages in creation order.
- Stage one: `Tasks {1,2}`, `NotDecomposed 1`, `InProgress 1`, `in_progress`. Stage two: `Tasks {0,1}`, `BlockedOutside 1`, `not_started`.
- Milestone: `Tasks {1,3}`, `StagesDone 0`, `NotDecomposed 1`, `InProgress 1`; a stale claim on Second task gives `Stale 1`.
- `OutsideMilestones 1`.
- `Children("")` = the milestone; `Children(stage one)` = First task, Second task, Unbroken epic (NotDecomposed, no children); unknown id → `ErrUnknownIssue`.
- `Level(stage two)`: Blocked task, external Second task with `Stage == stage one`, edge Second→Blocked, `Upstream(Blocked) == [Second]`.
- `Diff`: reopen First, close Second, create New, delete Blocked → exactly those four changes; adding a `blocks` edge on an open issue → `blocked`; closing its blocker → `unblocked` for the dependent. Changes are sorted by issue id, then kind.

- [ ] **Step 1: Write the failing tests**

```go
package status

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/tracker"
	"github.com/shady2k/nocx/internal/tracker/brspawn"
)

func fixtureIssues(t *testing.T) []tracker.Issue {
	t.Helper()
	raw, err := os.ReadFile("../tracker/brspawn/testdata/export.jsonl")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	issues, err := brspawn.DecodeExport(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return issues
}

func idOf(t *testing.T, issues []tracker.Issue, title string) string {
	t.Helper()
	for _, i := range issues {
		if i.Title == title {
			return i.ID
		}
	}
	t.Fatalf("no issue titled %q", title)
	return ""
}

func mutate(issues []tracker.Issue, fn func(i *tracker.Issue)) []tracker.Issue {
	out := make([]tracker.Issue, 0, len(issues))
	for _, i := range issues {
		c := i
		c.Relations = slices.Clone(i.Relations)
		fn(&c)
		out = append(out, c)
	}
	return out
}

func TestTheOverviewCountsWhatFeatureStatusCounts(t *testing.T) {
	issues := fixtureIssues(t)
	ov := NewIndex(issues).Overview(nil)
	if len(ov.Milestones) != 1 || ov.OutsideMilestones != 1 {
		t.Fatalf("overview = %+v", ov)
	}
	m := ov.Milestones[0]
	if m.Title != "Ship the thing" || len(m.Stages) != 2 || m.Stages[0].Title != "Stage one" {
		t.Fatalf("milestone = %+v", m)
	}
	one, two := m.Stages[0], m.Stages[1]
	if one.Tasks != (Progress{1, 2}) || one.NotDecomposed != 1 || one.InProgress != 1 || one.State != StageInProgress {
		t.Fatalf("stage one = %+v", one)
	}
	if two.Tasks != (Progress{0, 1}) || two.BlockedOutside != 1 || two.State != StageNotStarted {
		t.Fatalf("stage two = %+v", two)
	}
	if m.Tasks != (Progress{1, 3}) || m.StagesDone != 0 || m.NotDecomposed != 1 || m.InProgress != 1 {
		t.Fatalf("milestone totals = %+v", m)
	}
	stale := NewIndex(issues).Overview([]tracker.Claim{{ID: idOf(t, issues, "Second task"), Classification: "stale_candidate"}})
	if stale.Milestones[0].Stale != 1 {
		t.Fatalf("stale = %d", stale.Milestones[0].Stale)
	}
}

func TestChildrenAreOneLevelWithoutDeferredIssues(t *testing.T) {
	issues := fixtureIssues(t)
	ix := NewIndex(issues)
	roots, err := ix.Children("")
	if err != nil || len(roots) != 1 || roots[0].Title != "Ship the thing" {
		t.Fatalf("roots = %+v, %v", roots, err)
	}
	kids, err := ix.Children(idOf(t, issues, "Stage one"))
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	var titles []string
	for _, k := range kids {
		titles = append(titles, k.Title)
		if k.Title == "Unbroken epic" && (!k.NotDecomposed || k.HasChildren) {
			t.Fatalf("unbroken epic = %+v", k)
		}
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"First task", "Second task", "Unbroken epic"}) {
		t.Fatalf("children = %v", titles)
	}
	if _, err := ix.Children("nope"); err != ErrUnknownIssue {
		t.Fatalf("unknown = %v", err)
	}
}

func TestALevelGraphShowsTheBlockerOutsideTheStageAndItsChain(t *testing.T) {
	issues := fixtureIssues(t)
	second, blocked, stageOne := idOf(t, issues, "Second task"), idOf(t, issues, "Blocked task"), idOf(t, issues, "Stage one")
	g, err := NewIndex(issues).Level(idOf(t, issues, "Stage two"))
	if err != nil {
		t.Fatalf("Level: %v", err)
	}
	nodes := map[string]GraphNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	if ext := nodes[second]; !ext.External || ext.Stage != stageOne {
		t.Fatalf("external = %+v", ext)
	}
	if !slices.Equal(nodes[blocked].Upstream, []string{second}) {
		t.Fatalf("blocked = %+v", nodes[blocked])
	}
	if !slices.Contains(g.Edges, Edge{From: second, To: blocked}) {
		t.Fatalf("edges = %+v", g.Edges)
	}
}

func TestDiffNamesEachChangeOnce(t *testing.T) {
	prev := fixtureIssues(t)
	first, second, blocked := idOf(t, prev, "First task"), idOf(t, prev, "Second task"), idOf(t, prev, "Blocked task")
	next := mutate(prev, func(i *tracker.Issue) {
		switch i.ID {
		case first:
			i.Status = "open"
		case second:
			i.Status = "closed"
		}
	})
	next = slices.DeleteFunc(next, func(i tracker.Issue) bool { return i.ID == blocked })
	next = append(next, tracker.Issue{ID: "fx-new", Title: "New", Type: "task", Status: "open", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	got := Diff(NewIndex(prev), NewIndex(next))
	want := []Change{
		{IssueID: first, Title: "First task", Kind: EventReopened},
		{IssueID: second, Title: "Second task", Kind: EventClosed},
		{IssueID: "fx-new", Title: "New", Kind: EventCreated},
		{IssueID: blocked, Title: "Blocked task", Kind: EventDeleted},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v", got)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Fatalf("missing %+v in %+v", w, got)
		}
	}
}

func TestDiffNamesBlockingAndUnblocking(t *testing.T) {
	prev := fixtureIssues(t)
	first, second := idOf(t, prev, "First task"), idOf(t, prev, "Second task")
	blockedNow := mutate(prev, func(i *tracker.Issue) {
		if i.ID == first {
			i.Status = "open"
			i.Relations = append(i.Relations, tracker.Relation{DependsOn: second, Type: "blocks"})
		}
	})
	if got := Diff(NewIndex(prev), NewIndex(blockedNow)); !slices.Contains(got, Change{IssueID: first, Title: "First task", Kind: EventBlocked}) {
		t.Fatalf("no blocked event: %+v", got)
	}
	unblocked := mutate(blockedNow, func(i *tracker.Issue) {
		if i.ID == second {
			i.Status = "closed"
		}
	})
	if got := Diff(NewIndex(blockedNow), NewIndex(unblocked)); !slices.Contains(got, Change{IssueID: first, Title: "First task", Kind: EventUnblocked}) {
		t.Fatalf("no unblocked event: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/status/ -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement `model.go`**

```go
// Package status builds the status screen's model from a tracker's issues and
// keeps it current (status spec §4-§7). The counting rules are
// scripts/feature-status.sh's — deferred issues never count, a task is any
// non-epic, not decomposed is an open epic with no non-deferred children — so
// the script and the screen cannot disagree about where a milestone stands.
package status

import (
	"errors"
	"slices"
	"strings"

	"github.com/shady2k/nocx/internal/tracker"
)

var ErrUnknownIssue = errors.New("status: unknown issue")

type StageState string

const (
	StageNotStarted StageState = "not_started"
	StageInProgress StageState = "in_progress"
	StageDone       StageState = "done"
)

type Progress struct{ Closed, Total int }

type Stage struct {
	ID, Title      string
	State          StageState
	Tasks          Progress
	NotDecomposed  int
	InProgress     int
	BlockedOutside int
}

type Milestone struct {
	ID, Title     string
	Status        string
	Tasks         Progress
	StagesDone    int
	Stages        []Stage
	NotDecomposed int
	InProgress    int
	Stale         int
}

type Overview struct {
	Milestones        []Milestone
	OutsideMilestones int
}

type Node struct {
	ID, Title, Type, Status string
	Tasks                   Progress
	NotDecomposed           bool
	BlockedBy               int
	HasChildren             bool
}

type GraphNode struct {
	Node
	External bool
	Stage    string
	Upstream []string
}

type Edge struct{ From, To string }

type Graph struct {
	Parent string
	Nodes  []GraphNode
	Edges  []Edge
}

type EventKind string

const (
	EventCreated   EventKind = "created"
	EventTaken     EventKind = "taken"
	EventClosed    EventKind = "closed"
	EventReopened  EventKind = "reopened"
	EventDeferred  EventKind = "deferred"
	EventDeleted   EventKind = "deleted"
	EventBlocked   EventKind = "blocked"
	EventUnblocked EventKind = "unblocked"
)

type Change struct {
	IssueID, Title string
	Kind           EventKind
}

type Index struct {
	byID     map[string]tracker.Issue
	children map[string][]string
	parent   map[string]string
	blockers map[string][]string
}

func NewIndex(issues []tracker.Issue) *Index {
	ix := &Index{byID: make(map[string]tracker.Issue, len(issues)), children: map[string][]string{}, parent: map[string]string{}, blockers: map[string][]string{}}
	for _, i := range issues {
		ix.byID[i.ID] = i
	}
	for _, i := range issues {
		for _, r := range i.Relations {
			if _, ok := ix.byID[r.DependsOn]; !ok {
				continue
			}
			switch r.Type {
			case "parent-child":
				ix.parent[i.ID] = r.DependsOn
				ix.children[r.DependsOn] = append(ix.children[r.DependsOn], i.ID)
			case "blocks":
				ix.blockers[i.ID] = append(ix.blockers[i.ID], r.DependsOn)
			}
		}
	}
	for p := range ix.children {
		slices.SortFunc(ix.children[p], func(a, b string) int {
			if c := ix.byID[a].CreatedAt.Compare(ix.byID[b].CreatedAt); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
	}
	return ix
}

func (ix *Index) Has(id string) bool { _, ok := ix.byID[id]; return ok }

func isDeferred(i tracker.Issue) bool { return i.Status == "deferred" }
func isClosed(i tracker.Issue) bool   { return i.Status == "closed" }

func (ix *Index) liveChildren(id string) []string {
	out := make([]string, 0, len(ix.children[id]))
	for _, c := range ix.children[id] {
		if !isDeferred(ix.byID[c]) {
			out = append(out, c)
		}
	}
	return out
}

func (ix *Index) descendants(id string) []string {
	var out []string
	var walk func(string)
	walk = func(n string) {
		for _, c := range ix.liveChildren(n) {
			out = append(out, c)
			walk(c)
		}
	}
	walk(id)
	return out
}

func (ix *Index) notDecomposed(id string) bool {
	i := ix.byID[id]
	return i.Type == "epic" && !isClosed(i) && !isDeferred(i) && len(ix.liveChildren(id)) == 0
}

func (ix *Index) tasks(ids []string) Progress {
	var p Progress
	for _, id := range ids {
		if ix.byID[id].Type == "epic" {
			continue
		}
		p.Total++
		if isClosed(ix.byID[id]) {
			p.Closed++
		}
	}
	return p
}

func (ix *Index) isMilestone(i tracker.Issue) bool {
	return i.Type == "epic" && !isDeferred(i) && slices.Contains(i.Labels, "milestone")
}

func (ix *Index) milestoneOf(id string) string {
	for n, hops := id, 0; n != "" && hops <= len(ix.byID); n, hops = ix.parent[n], hops+1 {
		if ix.isMilestone(ix.byID[n]) {
			return n
		}
	}
	return ""
}

func (ix *Index) stageOf(id string) string {
	for n, hops := id, 0; n != "" && hops <= len(ix.byID); n, hops = ix.parent[n], hops+1 {
		if p := ix.parent[n]; p != "" && ix.isMilestone(ix.byID[p]) {
			return n
		}
	}
	return ""
}

func (ix *Index) sortedIDs() []string {
	ids := make([]string, 0, len(ix.byID))
	for id := range ix.byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (ix *Index) Overview(claims []tracker.Claim) Overview {
	stale := map[string]bool{}
	for _, c := range claims {
		if c.Stale() {
			stale[c.ID] = true
		}
	}
	ov := Overview{Milestones: make([]Milestone, 0)}
	for _, id := range ix.sortedIDs() {
		i := ix.byID[id]
		if ix.isMilestone(i) {
			ov.Milestones = append(ov.Milestones, ix.milestone(id, stale))
		} else if i.Type == "epic" && !isClosed(i) && !isDeferred(i) && ix.milestoneOf(id) == "" {
			ov.OutsideMilestones++
		}
	}
	return ov
}

func (ix *Index) milestone(id string, stale map[string]bool) Milestone {
	i := ix.byID[id]
	m := Milestone{ID: id, Title: i.Title, Status: i.Status, Stages: make([]Stage, 0)}
	desc := ix.descendants(id)
	m.Tasks = ix.tasks(desc)
	for _, d := range desc {
		if ix.notDecomposed(d) {
			m.NotDecomposed++
		}
		if ix.byID[d].Status == "in_progress" {
			m.InProgress++
		}
		if stale[d] {
			m.Stale++
		}
	}
	for _, s := range ix.liveChildren(id) {
		st := ix.stage(s)
		if st.State == StageDone {
			m.StagesDone++
		}
		m.Stages = append(m.Stages, st)
	}
	return m
}

func (ix *Index) stage(id string) Stage {
	i := ix.byID[id]
	desc := ix.descendants(id)
	in := map[string]bool{id: true}
	for _, d := range desc {
		in[d] = true
	}
	st := Stage{ID: id, Title: i.Title, Tasks: ix.tasks(desc)}
	started := false
	for _, n := range append([]string{id}, desc...) {
		di := ix.byID[n]
		if ix.notDecomposed(n) {
			st.NotDecomposed++
		}
		if di.Status == "in_progress" {
			st.InProgress++
			started = true
		}
		if isClosed(di) {
			if n != id {
				started = true
			}
			continue
		}
		for _, b := range ix.blockers[n] {
			if !in[b] && !isClosed(ix.byID[b]) {
				st.BlockedOutside++
			}
		}
	}
	switch {
	case isClosed(i):
		st.State = StageDone
	case started:
		st.State = StageInProgress
	default:
		st.State = StageNotStarted
	}
	return st
}

func (ix *Index) openBlockers(id string) int {
	n := 0
	for _, b := range ix.blockers[id] {
		if !isClosed(ix.byID[b]) {
			n++
		}
	}
	return n
}

func (ix *Index) node(id string) Node {
	i := ix.byID[id]
	return Node{ID: id, Title: i.Title, Type: i.Type, Status: i.Status, Tasks: ix.tasks(ix.descendants(id)),
		NotDecomposed: ix.notDecomposed(id), BlockedBy: ix.openBlockers(id), HasChildren: len(ix.liveChildren(id)) > 0}
}

func (ix *Index) Children(parent string) ([]Node, error) {
	var ids []string
	if parent == "" {
		for _, id := range ix.sortedIDs() {
			if ix.isMilestone(ix.byID[id]) {
				ids = append(ids, id)
			}
		}
	} else {
		if !ix.Has(parent) {
			return nil, ErrUnknownIssue
		}
		ids = ix.liveChildren(parent)
	}
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		out = append(out, ix.node(id))
	}
	return out, nil
}

func (ix *Index) Level(parent string) (Graph, error) {
	if !ix.Has(parent) {
		return Graph{}, ErrUnknownIssue
	}
	g := Graph{Parent: parent, Nodes: make([]GraphNode, 0), Edges: make([]Edge, 0)}
	level := ix.liveChildren(parent)
	inLevel := map[string]bool{}
	for _, id := range level {
		inLevel[id] = true
	}
	added := map[string]bool{}
	add := func(id string, external bool) {
		if !added[id] {
			added[id] = true
			g.Nodes = append(g.Nodes, GraphNode{Node: ix.node(id), External: external, Stage: ix.stageOf(id), Upstream: []string{}})
		}
	}
	for _, id := range level {
		add(id, false)
	}
	for _, id := range level {
		for _, b := range ix.blockers[id] {
			if !inLevel[b] {
				add(b, true)
			}
			g.Edges = append(g.Edges, Edge{From: b, To: id})
		}
	}
	for n := range g.Nodes {
		g.Nodes[n].Upstream = upstream(g.Edges, g.Nodes[n].ID)
	}
	return g, nil
}

func upstream(edges []Edge, id string) []string {
	out := []string{}
	seen := map[string]bool{id: true}
	for queue := []string{id}; len(queue) > 0; queue = queue[1:] {
		for _, e := range edges {
			if e.To == queue[0] && !seen[e.From] {
				seen[e.From] = true
				out = append(out, e.From)
				queue = append(queue, e.From)
			}
		}
	}
	return out
}

func Diff(prev, next *Index) []Change {
	var out []Change
	add := func(id, title string, k EventKind) { out = append(out, Change{IssueID: id, Title: title, Kind: k}) }
	for id, n := range next.byID {
		p, existed := prev.byID[id]
		if !existed {
			add(id, n.Title, EventCreated)
			continue
		}
		if p.Status != n.Status {
			switch {
			case n.Status == "closed":
				add(id, n.Title, EventClosed)
			case p.Status == "closed":
				add(id, n.Title, EventReopened)
			case n.Status == "deferred":
				add(id, n.Title, EventDeferred)
			case n.Status == "in_progress":
				add(id, n.Title, EventTaken)
			}
		}
		if isClosed(n) {
			continue
		}
		was, is := prev.openBlockers(id) > 0, next.openBlockers(id) > 0
		if !was && is {
			add(id, n.Title, EventBlocked)
		} else if was && !is {
			add(id, n.Title, EventUnblocked)
		}
	}
	for id, p := range prev.byID {
		if !next.Has(id) {
			add(id, p.Title, EventDeleted)
		}
	}
	slices.SortFunc(out, func(a, b Change) int {
		if c := strings.Compare(a.IssueID, b.IssueID); c != 0 {
			return c
		}
		return strings.Compare(string(a.Kind), string(b.Kind))
	})
	return out
}
```

- [ ] **Step 4: Run the tests** — `go test ./internal/status/ -count=1 -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/status/model.go internal/status/model_test.go
git commit   # feat(status): milestones, stages, one graph level and the feed's changes, counted as feature-status.sh counts (<bead>)
```

---

### Task 8: the status service — projects from the ledger, locations, polling, revisions

**Files:**

- Create: `internal/status/service.go`
- Test: `internal/status/service_test.go`

**Interfaces:**

- Consumes: `content.StatusLocationRepository`, `content.StatusLocation*`, `content.CwdSeen`, `content.EnvLocal`, `content.MaxDistinctCwds` (Task 6); `git.RepoFactory`, `git.OpenOK`, `git.OpenNotARepository`, `git.ErrNoRemote`, `Repo.OriginURL` (Task 5); `tracker.Opener`, `tracker.NormalizeOrigin`, `tracker.ErrNoBr`, `tracker.ErrNoWorkspace`, `*tracker.RefusalError` (Tasks 2, 4); `Index`, `Overview`, `Milestone`, `Node`, `Graph`, `Change`, `Diff` (Task 7).
- Produces:

```go
type Ledger interface {
	DistinctCwds(ctx context.Context, limit int) ([]content.CwdSeen, error)
}
type Clock interface{ Now() time.Time }
type Deps struct {
	Ledger    Ledger
	Locations content.StatusLocationRepository
	Repos     git.RepoFactory
	Trackers  tracker.Opener
	Clock     Clock
	Log       *slog.Logger
}
type Health string // fresh | export_behind | pulled_not_imported | error
type ProjectLocation struct {
	Host, Path string
	State      content.StatusLocationState
	LastSeen   int64
	Source     bool
}
type Project struct {
	Origin    string
	Locations []ProjectLocation
	Health    Health
	Error     string
	Diverged  bool
	Stale     bool
	Overview  Overview
}
type Event struct {
	Seq    uint64
	At     int64
	Origin string
	Change
}

func New(d Deps) *Service
func (s *Service) Discover(ctx context.Context) error
func (s *Service) Poll(ctx context.Context) error
func (s *Service) Run(ctx context.Context)
func (s *Service) OnChange(fn func(origin string, revision uint64))
func (s *Service) Revision() uint64
func (s *Service) Projects() []Project
func (s *Service) Tree(origin, parent string) ([]Node, error)
func (s *Service) Graph(origin, parent string) (Graph, error)
func (s *Service) Events(origin string, after uint64) []Event
func (s *Service) Hide(ctx context.Context, origin string) error
func (s *Service) Show(ctx context.Context, origin string) error
func (s *Service) Add(ctx context.Context, host, path string) (ProjectLocation, error)
func (s *Service) Touch()

var ErrUnknownProject = errors.New("status: unknown project")
var ErrRemoteNotAvailable = errors.New("remote projects are not available yet")
const MaxEvents = 500
const ActivePoll = 5 * time.Second
const IdlePoll = 60 * time.Second
```

**Acceptance Criteria (fakes only: an in-memory `Ledger`, a map-backed `StatusLocationRepository`, a fake `git.RepoFactory`/`Repo`, a scripted fake `tracker.Opener`, a settable clock; no sleeps, no real `br`):**

- `Discover` with ledger rows `(local, /r/sub)` and `(ssh, /x)`: opens `/r/sub` → toplevel `/r`, origin `git@github.com:o/r.git`, upserts `{github.com/o/r, "", /r, present}`; never opens the ssh row.
- A local cwd whose repo has no origin, or whose tracker answers `ErrNoWorkspace` or `ErrNoBr`, upserts nothing.
- First `Poll` of a location: status hash `h1`, export read with `expectHash == h1`, `OnChange("github.com/o/r", 1)` once; `Projects()` carries the overview.
- `Poll` with the same hash reads no export and calls no `OnChange`.
- `Poll` with `h2` and a task closed: revision 2; `Events("", 0)` has one `closed` event, `Seq 1`, `Origin` set; `Events("", 1)` is empty.
- `ReadExport` → `*tracker.HashMismatchError`: no revision, no `OnChange`, the overview stays.
- `Status` → `*tracker.RefusalError`: `Health == error`, `Error` = its stderr, `Stale == true`, overview stays, one revision so the renderer learns it; a repeat of the same refusal publishes nothing.
- `DirtyCount > 0` or `DBNewer` → `export_behind`; `JSONLNewer` → `pulled_not_imported`; the model is still published.
- Forgetting: `Repos.Open(path)` answering `OpenNotARepository` deletes that location and publishes; the project leaves `Projects()` only when no location is left. An `Open` returning an error keeps the location.
- Two locations for one origin with different hashes → `Diverged`; `Source` is the newest `LastSeen`.
- `Hide` removes from `Projects()`, `Show` restores; both persist via `SetHidden`; `Discover` reloads the hidden set.
- `Add(ctx, "", "/r")` upserts with `Manual: true`; `Add(ctx, "host", "/r")` returns `ErrRemoteNotAvailable` and writes nothing.
- `Tree`/`Graph` on an unknown origin → `ErrUnknownProject`; unknown issue → `ErrUnknownIssue`.
- The event ring keeps the newest `MaxEvents`.
- Cadence: `Touch()` at t → `due()` is `ActivePoll` at t+59 s and `IdlePoll` at t+61 s.
- `go test ./internal/status/ -race -count=1` passes.

- [ ] **Step 1: Write the failing tests**

Write one test per acceptance bullet in `service_test.go`, asserting exactly the bullet. Fakes:

```go
package status

import (
	"context"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/tracker"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type fakeLedger struct{ rows []content.CwdSeen }

func (l *fakeLedger) DistinctCwds(context.Context, int) ([]content.CwdSeen, error) { return l.rows, nil }

type fakeLocations struct {
	mu     sync.Mutex
	rows   map[string]content.StatusLocation
	hidden map[string]int64
}

func newFakeLocations() *fakeLocations {
	return &fakeLocations{rows: map[string]content.StatusLocation{}, hidden: map[string]int64{}}
}
func (f *fakeLocations) Upsert(_ context.Context, l content.StatusLocation) error {
	f.mu.Lock(); defer f.mu.Unlock()
	k := l.Origin + "|" + l.Host + "|" + l.Path
	if old, ok := f.rows[k]; ok {
		l.FirstSeen = old.FirstSeen
		l.Manual = l.Manual || old.Manual
	}
	f.rows[k] = l
	return nil
}
func (f *fakeLocations) List(context.Context) ([]content.StatusLocation, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	out := make([]content.StatusLocation, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeLocations) Delete(_ context.Context, o, h, p string) error {
	f.mu.Lock(); defer f.mu.Unlock(); delete(f.rows, o+"|"+h+"|"+p); return nil
}
func (f *fakeLocations) SetHidden(_ context.Context, o string, hidden bool, at int64) error {
	f.mu.Lock(); defer f.mu.Unlock()
	if hidden {
		f.hidden[o] = at
	} else {
		delete(f.hidden, o)
	}
	return nil
}
func (f *fakeLocations) Hidden(context.Context) (map[string]int64, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	out := map[string]int64{}
	for k, v := range f.hidden {
		out[k] = v
	}
	return out, nil
}

type fakeRepo struct {
	git.Repo // nil: only OriginURL and Close are called
	origin   string
}

func (r *fakeRepo) OriginURL(context.Context) (string, error) {
	if r.origin == "" {
		return "", &git.ErrNoRemote{}
	}
	return r.origin, nil
}
func (r *fakeRepo) Close() error { return nil }

type repoAt struct {
	toplevel, origin string
	state            git.OpenState
	err              error
}

type fakeRepos struct {
	mu sync.Mutex
	at map[string]repoAt
}

func (f *fakeRepos) Open(_ context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	r, ok := f.at[cwd]
	if !ok {
		return nil, git.OpenOutcome{State: git.OpenNotARepository}, nil
	}
	if r.err != nil {
		return nil, git.OpenOutcome{}, r.err
	}
	if r.state != "" && r.state != git.OpenOK {
		return nil, git.OpenOutcome{State: r.state}, nil
	}
	return &fakeRepo{origin: r.origin}, git.OpenOutcome{State: git.OpenOK, Toplevel: r.toplevel}, nil
}

type fakeTracker struct {
	mu      sync.Mutex
	status  tracker.Status
	statErr error
	export  []tracker.Issue
	expErr  error
	claims  []tracker.Claim
	reads   int
}

func (f *fakeTracker) Status(context.Context) (tracker.Status, error) {
	f.mu.Lock(); defer f.mu.Unlock(); return f.status, f.statErr
}
func (f *fakeTracker) Locate(context.Context) (tracker.Location, error) {
	return tracker.Location{ExportPath: "/r/.beads/issues.jsonl"}, nil
}
func (f *fakeTracker) ReadExport(_ context.Context, _ tracker.Location, want string) (tracker.Export, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	f.reads++
	if f.expErr != nil {
		return tracker.Export{}, f.expErr
	}
	if want != f.status.Hash {
		return tracker.Export{}, &tracker.HashMismatchError{Want: want, Got: f.status.Hash}
	}
	return tracker.Export{Hash: want, Issues: f.export}, nil
}
func (f *fakeTracker) Claims(context.Context) ([]tracker.Claim, error) { return f.claims, nil }

type fakeOpener map[string]*fakeTracker

func (o fakeOpener) Open(root string) tracker.Tracker {
	if t, ok := o[root]; ok {
		return t
	}
	return &fakeTracker{statErr: tracker.ErrNoWorkspace}
}
```

Use `fixtureIssues(t)` from `model_test.go` as the first export and `mutate` to close a task for the second.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/status/ -run 'Discover|Poll|Hide|Add|Events|Cadence|Forget|Diverg' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement `service.go`**

```go
package status

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/tracker"
)

type Ledger interface {
	DistinctCwds(ctx context.Context, limit int) ([]content.CwdSeen, error)
}

type Clock interface{ Now() time.Time }

type Deps struct {
	Ledger    Ledger
	Locations content.StatusLocationRepository
	Repos     git.RepoFactory
	Trackers  tracker.Opener
	Clock     Clock
	Log       *slog.Logger
}

type Health string

const (
	HealthFresh        Health = "fresh"
	HealthExportBehind Health = "export_behind"
	HealthPulledBehind Health = "pulled_not_imported"
	HealthError        Health = "error"
)

type ProjectLocation struct {
	Host, Path string
	State      content.StatusLocationState
	LastSeen   int64
	Source     bool
}

type Project struct {
	Origin    string
	Locations []ProjectLocation
	Health    Health
	Error     string
	Diverged  bool
	Stale     bool
	Overview  Overview
}

type Event struct {
	Seq    uint64
	At     int64
	Origin string
	Change
}

var (
	ErrUnknownProject     = errors.New("status: unknown project")
	ErrRemoteNotAvailable = errors.New("remote projects are not available yet")
)

const (
	MaxEvents  = 500
	ActivePoll = 5 * time.Second
	IdlePoll   = 60 * time.Second
)

type locationState struct {
	loc    content.StatusLocation
	hash   string
	index  *Index
	claims []tracker.Claim
	health Health
	err    string
	stale  bool
}

// Service keeps the status model current. Locks: pollMu serializes polls so
// one never overlaps the next (spec §4.3); mu guards the maps and is released
// around OnChange callbacks, which may call back into the service.
type Service struct {
	d        Deps
	pollMu   sync.Mutex
	mu       sync.Mutex
	states   map[string]*locationState
	hidden   map[string]int64
	revision uint64
	eventSeq uint64
	events   []Event
	onChange []func(string, uint64)
	lastRead time.Time
	lastPoll time.Time
}

func New(d Deps) *Service {
	return &Service{d: d, states: map[string]*locationState{}, hidden: map[string]int64{}}
}

func key(l content.StatusLocation) string { return l.Origin + "\x00" + l.Host + "\x00" + l.Path }

func (s *Service) OnChange(fn func(string, uint64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = append(s.onChange, fn)
}

func (s *Service) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

func (s *Service) Touch() {
	s.mu.Lock()
	s.lastRead = s.d.Clock.Now()
	s.mu.Unlock()
}

func (s *Service) due() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Clock.Now().Sub(s.lastRead) <= 60*time.Second {
		return ActivePoll
	}
	return IdlePoll
}

// publishLocked advances the revision and runs the callbacks with mu released.
// Call it with mu held; it returns with mu held.
func (s *Service) publishLocked(origin string) {
	s.revision++
	rev, fns := s.revision, slices.Clone(s.onChange)
	s.mu.Unlock()
	for _, fn := range fns {
		fn(origin, rev)
	}
	s.mu.Lock()
}

// resolve turns one local working directory into a location, or nothing.
// Remote environments are plan 4 (nocx-522al).
func (s *Service) resolve(ctx context.Context, cwd string) (content.StatusLocation, bool) {
	repo, outcome, err := s.d.Repos.Open(ctx, cwd)
	if err != nil || outcome.State != git.OpenOK || repo == nil {
		return content.StatusLocation{}, false
	}
	defer func() { _ = repo.Close() }()
	raw, err := repo.OriginURL(ctx)
	if err != nil {
		return content.StatusLocation{}, false
	}
	origin, ok := tracker.NormalizeOrigin(raw)
	if !ok {
		return content.StatusLocation{}, false
	}
	if _, err := s.d.Trackers.Open(outcome.Toplevel).Status(ctx); errors.Is(err, tracker.ErrNoBr) || errors.Is(err, tracker.ErrNoWorkspace) {
		return content.StatusLocation{}, false
	}
	now := s.d.Clock.Now().UnixMilli()
	return content.StatusLocation{Origin: origin, Path: outcome.Toplevel, FirstSeen: now, LastSeen: now, State: content.StatusLocationPresent}, true
}

func (s *Service) Discover(ctx context.Context) error {
	seen, err := s.d.Ledger.DistinctCwds(ctx, content.MaxDistinctCwds)
	if err != nil {
		return err
	}
	for _, c := range seen {
		if c.Environment == nil || c.Environment.Kind != content.EnvLocal {
			continue
		}
		loc, ok := s.resolve(ctx, c.Cwd)
		if !ok {
			continue
		}
		loc.LastSeen = c.LastSeen
		if err := s.d.Locations.Upsert(ctx, loc); err != nil {
			return err
		}
	}
	hidden, err := s.d.Locations.Hidden(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.hidden = hidden
	s.mu.Unlock()
	return nil
}

func healthOf(st tracker.Status) Health {
	switch {
	case st.JSONLNewer:
		return HealthPulledBehind
	case st.DirtyCount > 0 || st.DBNewer:
		return HealthExportBehind
	default:
		return HealthFresh
	}
}

func (s *Service) Poll(ctx context.Context) error {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	locs, err := s.d.Locations.List(ctx)
	if err != nil {
		return err
	}
	for _, loc := range locs {
		if loc.Host == "" {
			s.pollOne(ctx, loc)
		}
	}
	s.mu.Lock()
	s.lastPoll = s.d.Clock.Now()
	s.mu.Unlock()
	return nil
}

func (s *Service) pollOne(ctx context.Context, loc content.StatusLocation) {
	if _, outcome, err := s.d.Repos.Open(ctx, loc.Path); err == nil && outcome.State == git.OpenNotARepository {
		if s.d.Locations.Delete(ctx, loc.Origin, loc.Host, loc.Path) == nil {
			s.mu.Lock()
			delete(s.states, key(loc))
			s.publishLocked(loc.Origin)
			s.mu.Unlock()
		}
		return
	}
	s.mu.Lock()
	state := s.states[key(loc)]
	if state == nil {
		state = &locationState{loc: loc}
		s.states[key(loc)] = state
	}
	state.loc = loc
	s.mu.Unlock()

	tr := s.d.Trackers.Open(loc.Path)
	st, err := tr.Status(ctx)
	if err != nil {
		msg := err.Error()
		var refusal *tracker.RefusalError
		if errors.As(err, &refusal) {
			msg = refusal.Stderr
		}
		s.mu.Lock()
		if state.health != HealthError || state.err != msg {
			state.health, state.err, state.stale = HealthError, msg, true
			s.publishLocked(loc.Origin)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	unchanged := st.Hash == state.hash && state.health != HealthError
	s.mu.Unlock()
	if unchanged {
		return
	}
	where, err := tr.Locate(ctx)
	if err != nil {
		return
	}
	exp, err := tr.ReadExport(ctx, where, st.Hash)
	if err != nil {
		return // torn or unreadable: the next poll tries again (spec §4.1)
	}
	claims, _ := tr.Claims(ctx)
	next := NewIndex(exp.Issues)

	s.mu.Lock()
	defer s.mu.Unlock()
	if state.index != nil {
		now := s.d.Clock.Now().UnixMilli()
		for _, c := range Diff(state.index, next) {
			s.eventSeq++
			s.events = append(s.events, Event{Seq: s.eventSeq, At: now, Origin: loc.Origin, Change: c})
		}
		if over := len(s.events) - MaxEvents; over > 0 {
			s.events = slices.Delete(s.events, 0, over)
		}
	}
	state.hash, state.index, state.claims = st.Hash, next, claims
	state.health, state.err, state.stale = healthOf(st), "", false
	s.publishLocked(loc.Origin)
}

func (s *Service) byOriginLocked() map[string][]*locationState {
	out := map[string][]*locationState{}
	for _, st := range s.states {
		out[st.loc.Origin] = append(out[st.loc.Origin], st)
	}
	for _, sts := range out {
		slices.SortFunc(sts, func(a, b *locationState) int {
			if a.loc.LastSeen != b.loc.LastSeen {
				return int(b.loc.LastSeen - a.loc.LastSeen)
			}
			return strings.Compare(a.loc.Path, b.loc.Path)
		})
	}
	return out
}

func (s *Service) Projects() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := s.byOriginLocked()
	origins := make([]string, 0, len(groups))
	for o := range groups {
		if _, hidden := s.hidden[o]; !hidden {
			origins = append(origins, o)
		}
	}
	slices.Sort(origins)
	out := make([]Project, 0, len(origins))
	for _, o := range origins {
		sts := groups[o]
		src := sts[0]
		p := Project{Origin: o, Health: src.health, Error: src.err, Stale: src.stale,
			Locations: make([]ProjectLocation, 0, len(sts)), Overview: Overview{Milestones: make([]Milestone, 0)}}
		if src.index != nil {
			p.Overview = src.index.Overview(src.claims)
		}
		for i, st := range sts {
			p.Locations = append(p.Locations, ProjectLocation{Host: st.loc.Host, Path: st.loc.Path, State: st.loc.State, LastSeen: st.loc.LastSeen, Source: i == 0})
			if st.hash != "" && src.hash != "" && st.hash != src.hash {
				p.Diverged = true
			}
		}
		out = append(out, p)
	}
	return out
}

func (s *Service) source(origin string) (*Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sts, ok := s.byOriginLocked()[origin]
	if !ok || sts[0].index == nil {
		return nil, ErrUnknownProject
	}
	return sts[0].index, nil
}

func (s *Service) Tree(origin, parent string) ([]Node, error) {
	ix, err := s.source(origin)
	if err != nil {
		return nil, err
	}
	return ix.Children(parent)
}

func (s *Service) Graph(origin, parent string) (Graph, error) {
	ix, err := s.source(origin)
	if err != nil {
		return Graph{}, err
	}
	return ix.Level(parent)
}

func (s *Service) Events(origin string, after uint64) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, 0)
	for _, e := range s.events {
		if e.Seq > after && (origin == "" || e.Origin == origin) {
			out = append(out, e)
		}
	}
	return out
}

func (s *Service) Hide(ctx context.Context, origin string) error {
	now := s.d.Clock.Now().UnixMilli()
	if err := s.d.Locations.SetHidden(ctx, origin, true, now); err != nil {
		return err
	}
	s.mu.Lock()
	s.hidden[origin] = now
	s.publishLocked(origin)
	s.mu.Unlock()
	return nil
}

func (s *Service) Show(ctx context.Context, origin string) error {
	if err := s.d.Locations.SetHidden(ctx, origin, false, s.d.Clock.Now().UnixMilli()); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.hidden, origin)
	s.publishLocked(origin)
	s.mu.Unlock()
	return nil
}

func (s *Service) Add(ctx context.Context, host, path string) (ProjectLocation, error) {
	if host != "" {
		return ProjectLocation{}, ErrRemoteNotAvailable
	}
	loc, ok := s.resolve(ctx, path)
	if !ok {
		return ProjectLocation{}, errors.New("not a git repository with an origin and a br workspace")
	}
	loc.Manual = true
	if err := s.d.Locations.Upsert(ctx, loc); err != nil {
		return ProjectLocation{}, err
	}
	return ProjectLocation{Host: loc.Host, Path: loc.Path, State: loc.State, LastSeen: loc.LastSeen}, nil
}

// Run discovers and polls until ctx ends. The one-second tick only decides
// whether a poll is due; tests drive Discover and Poll directly.
func (s *Service) Run(ctx context.Context) {
	s.logErr("discover", s.Discover(ctx))
	s.logErr("poll", s.Poll(ctx))
	discover := time.NewTicker(IdlePoll)
	defer discover.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-discover.C:
			s.logErr("discover", s.Discover(ctx))
		case <-tick.C:
			s.mu.Lock()
			since := s.d.Clock.Now().Sub(s.lastPoll)
			s.mu.Unlock()
			if since >= s.due() {
				s.logErr("poll", s.Poll(ctx))
			}
		}
	}
}

func (s *Service) logErr(what string, err error) {
	if err != nil && s.d.Log != nil {
		s.d.Log.Warn("status: "+what, "err", err)
	}
}
```

- [ ] **Step 4: Run the tests** — `go test ./internal/status/ -race -count=1 -v` — Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
git add internal/status/service.go internal/status/service_test.go
git commit   # feat(status): projects found where the owner worked, polled by br's hash, published as revisions (<bead>)
```

---

### Task 9: `status.*` over the control plane

**Files:**

- Create: `internal/transport/ws_status.go`, `internal/transport/ws_status_test.go`
- Create contracts (params + result each): `status.overview.read`, `status.tree.read`, `status.graph.read`, `status.events.read`, `status.projects.hide`, `status.projects.show`, `status.projects.add`; notification `status.changed.schema.json`
- Modify: `contracts/openrpc.json`, `internal/transport/ws.go` (field, `buildControlPlane`), `internal/transport/params_contract_test.go`, `internal/transport/ws_contract_test.go`
- Generated (commit, never hand-edit): `frontend/src/generated/status.*.ts`

**Interfaces:**

- Consumes: `status.Project`, `status.Node`, `status.Graph`, `status.Event`, `status.ProjectLocation`, `status.ErrUnknownProject`, `status.ErrUnknownIssue`, `status.ErrRemoteNotAvailable` (Tasks 7–8).
- Produces:

```go
type StatusService interface {
	Revision() uint64
	Projects() []status.Project
	Tree(origin, parent string) ([]status.Node, error)
	Graph(origin, parent string) (status.Graph, error)
	Events(origin string, after uint64) []status.Event
	Hide(ctx context.Context, origin string) error
	Show(ctx context.Context, origin string) error
	Add(ctx context.Context, host, path string) (status.ProjectLocation, error)
	Touch()
}

func WithStatus(svc StatusService) WSServerOption
func (s *WSServer) BroadcastStatusChanged(origin string, revision uint64)
```

Wire shapes (camelCase; every object `additionalProperties: false`, every field `required`):

- `status.overview.read` `{}` → `{revision, projects: [{origin, health: enum(fresh, export_behind, pulled_not_imported, error), error, diverged, stale, locations: [{host, path, state: enum(present, gone, unreachable, no_helper, no_br), lastSeen, source}], milestones: [{id, title, status, tasks: {closed, total}, stagesDone, stages: [{id, title, state: enum(not_started, in_progress, done), tasks, notDecomposed, inProgress, blockedOutside}], notDecomposed, inProgress, stale}], outsideMilestones}]}`
- `status.tree.read` `{origin (minLength 1), parent}` → `{nodes: [{id, title, type, status, tasks, notDecomposed, blockedBy, hasChildren}]}`
- `status.graph.read` `{origin, parent}` → `{parent, nodes: [node + {external, stage, upstream: [string]}], edges: [{from, to}]}`
- `status.events.read` `{origin, after (integer ≥ 0)}` → `{events: [{seq, at, origin, issueId, title, kind: enum(created, taken, closed, reopened, deferred, deleted, blocked, unblocked)}]}`
- `status.projects.hide` / `.show` `{origin}` → `{ok: const true}`
- `status.projects.add` `{host, path}` → `{host, path, state, lastSeen}`
- `status.changed` → `{origin, revision}`

Errors: unknown project/issue and remote-not-available → `-32602` with the error text; anything else `-32603`.

**Acceptance Criteria:**

- The seven methods register on `s.lane`, validate params (non-empty `origin` for tree/graph/hide/show; `after` present and ≥ 0; absolute `path` for add) and answer `-32601 status not available` when no service is wired.
- `TestParamsContractsAgreeWithRegisteredValidators` and `TestOpenRPCManifestMatchesRegisteredMethods` pass.
- A `_DTOConformsToContract` test per result schema (empty and populated) and for `status.changed`.
- An over-the-wire test calls every method on a real `WSServer` with a fake service and validates each result; every read calls `Touch()`.
- `BroadcastStatusChanged` reaches two connections and its params validate.
- `cd frontend && npm run contracts && npm run contracts:check` passes.

- [ ] **Step 1: Write the contracts**

Follow `notify.feed.read.params.schema.json` and `notify.feed.markRead.schema.json` for header fields (`$schema` 2020-12, `$id` `https://nocx.local/contracts/<file>`, `title`, `description`). Put shared shapes in `status.overview.read.schema.json` `$defs` (`progress`, `stage`, `milestone`, `location`, `project`) and `$ref` `status.overview.read.schema.json#/$defs/progress` from the tree and graph schemas. Put `node` in `status.tree.read.schema.json` `$defs` and reference it from the graph schema. Two examples:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://nocx.local/contracts/status.tree.read.params.schema.json",
  "title": "StatusTreeReadParams",
  "description": "Params for status.tree.read: one level of a project's roadmap tree. An empty parent asks for the project's milestones.",
  "type": "object",
  "additionalProperties": false,
  "required": ["origin", "parent"],
  "properties": {
    "origin": {
      "description": "The project's normalized origin, e.g. github.com/shady2k/nocx.",
      "type": "string",
      "minLength": 1
    },
    "parent": {
      "description": "The issue whose non-deferred children are asked for; empty for the milestones.",
      "type": "string"
    }
  }
}
```

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://nocx.local/contracts/status.changed.schema.json",
  "title": "StatusChanged",
  "description": "Notification: a project's status moved. It carries no data; the renderer re-reads what it has open, as for notify.feed.changed.",
  "type": "object",
  "additionalProperties": false,
  "required": ["origin", "revision"],
  "properties": {
    "origin": { "type": "string", "minLength": 1 },
    "revision": { "type": "integer", "minimum": 0 }
  }
}
```

Add the seven entries to `contracts/openrpc.json` in the shape of the `notify.feed.read` entry (params `$ref`, the four errors, `"x-nocx-agent-disposition": "operation-owned"`, result `$ref`), and `{"$ref": "https://nocx.local/contracts/status.changed.schema.json"}` to `x-nocx-schemaRefs`.

- [ ] **Step 2: Write the failing tests**

`params_contract_test.go`, in `valid`:

```go
		"status.overview.read": {[]byte(`{}`)},
		"status.tree.read":     {[]byte(`{"origin":"github.com/o/r","parent":""}`)},
		"status.graph.read":    {[]byte(`{"origin":"github.com/o/r","parent":"fx-1"}`)},
		"status.events.read":   {[]byte(`{"origin":"","after":0}`)},
		"status.projects.hide": {[]byte(`{"origin":"github.com/o/r"}`)},
		"status.projects.show": {[]byte(`{"origin":"github.com/o/r"}`)},
		"status.projects.add":  {[]byte(`{"host":"","path":"/srv/r"}`)},
```

`ws_status_test.go` (copy imports and `wantWithin`/`waitForConns` usage from `ws_notify_feed_test.go`):

```go
type fakeStatus struct{ touched int }

func (f *fakeStatus) Revision() uint64 { return 3 }
func (f *fakeStatus) Projects() []status.Project {
	return []status.Project{{
		Origin: "github.com/o/r", Health: status.HealthFresh,
		Locations: []status.ProjectLocation{{Path: "/r", State: content.StatusLocationPresent, LastSeen: 1, Source: true}},
		Overview: status.Overview{Milestones: []status.Milestone{{
			ID: "fx-1", Title: "Ship", Status: "open", Tasks: status.Progress{Closed: 1, Total: 3},
			Stages: []status.Stage{{ID: "fx-2", Title: "One", State: status.StageInProgress, Tasks: status.Progress{Closed: 1, Total: 2}}},
		}}},
	}}
}
func (f *fakeStatus) Tree(string, string) ([]status.Node, error) {
	return []status.Node{{ID: "fx-2", Title: "One", Type: "epic", Status: "open", HasChildren: true}}, nil
}
func (f *fakeStatus) Graph(string, string) (status.Graph, error) {
	return status.Graph{Parent: "fx-2", Nodes: []status.GraphNode{{Node: status.Node{ID: "fx-3", Title: "T", Type: "task", Status: "open"}, Upstream: []string{}}}, Edges: []status.Edge{}}, nil
}
func (f *fakeStatus) Events(string, uint64) []status.Event {
	return []status.Event{{Seq: 1, At: 2, Origin: "github.com/o/r", Change: status.Change{IssueID: "fx-3", Title: "T", Kind: status.EventClosed}}}
}
func (f *fakeStatus) Hide(context.Context, string) error { return nil }
func (f *fakeStatus) Show(context.Context, string) error { return nil }
func (f *fakeStatus) Add(context.Context, string, string) (status.ProjectLocation, error) {
	return status.ProjectLocation{Path: "/srv/r", State: content.StatusLocationPresent, LastSeen: 1}, nil
}
func (f *fakeStatus) Touch() { f.touched++ }

func newStatusWS(t *testing.T, svc StatusService) (*WSServer, *websocket.Conn) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	opts := []WSServerOption{}
	if svc != nil {
		opts = append(opts, WithStatus(svc))
	}
	ws := NewWSServer(logger, newRegWithStub(logger), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return ws, conn
}

func TestStatusMethods_OverTheWireConformToContract(t *testing.T) {
	svc := &fakeStatus{}
	_, conn := newStatusWS(t, svc)
	for method, params := range map[string]map[string]any{
		"status.overview.read": {},
		"status.tree.read":     {"origin": "github.com/o/r", "parent": ""},
		"status.graph.read":    {"origin": "github.com/o/r", "parent": "fx-2"},
		"status.events.read":   {"origin": "", "after": 0},
		"status.projects.hide": {"origin": "github.com/o/r"},
		"status.projects.show": {"origin": "github.com/o/r"},
		"status.projects.add":  {"host": "", "path": "/srv/r"},
	} {
		t.Run(method, func(t *testing.T) {
			schema := loadSchema(t, method+".schema.json")
			resp := jsonrpcCall(t, conn, method, params)
			var envelope struct {
				Result json.RawMessage  `json:"result"`
				Error  *jsonrpcErrorObj `json:"error"`
			}
			if err := json.Unmarshal(resp, &envelope); err != nil {
				t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
			}
			if envelope.Error != nil {
				t.Fatalf("%s: %+v", method, envelope.Error)
			}
			validateJSON(t, schema, envelope.Result, method+" result")
		})
	}
	if svc.touched < 4 {
		t.Fatalf("Touch called %d times, want every read to count", svc.touched)
	}
}

func TestStatusChanged_ReachesEveryConnectionAndConformsToContract(t *testing.T) {
	schema := loadSchema(t, "status.changed.schema.json")
	ws, first := newStatusWS(t, &fakeStatus{})
	second := connectWS(t, ws)
	t.Cleanup(func() { _ = second.Close() })
	waitForConns(t, ws, 2)
	ws.BroadcastStatusChanged("github.com/o/r", 7)
	for _, conn := range []*websocket.Conn{first, second} {
		validateJSON(t, schema, readNotification(t, conn, "status.changed", wantWithin), "status.changed params")
	}
}

func TestStatus_NotWired_MethodsUnavailable(t *testing.T) {
	_, conn := newStatusWS(t, nil)
	resp := jsonrpcCall(t, conn, "status.overview.read", map[string]any{})
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	_ = json.Unmarshal(resp, &envelope)
	if envelope.Error == nil || envelope.Error.Code != -32601 {
		t.Fatalf("error = %+v, want -32601", envelope.Error)
	}
}
```

In `ws_contract_test.go`, one `TestStatus<Method>_DTOConformsToContract` per result schema, following `TestNotifyFeedMarkRead_DTOConformsToContract`, with an empty case and a populated case built from `(&fakeStatus{})` through the converters.

- [ ] **Step 3: Run to verify failure** — `go test ./internal/transport/ -run 'Status|ParamsContracts|OpenRPC' -v` — Expected: FAIL to compile.

- [ ] **Step 4: Implement `ws_status.go`**

Read `Responder` in `internal/transport` for its result and error method names first (`TryResult` is used by the feed handler); use the error counterpart it defines.

```go
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/shady2k/nocx/internal/status"
)

type StatusService interface {
	Revision() uint64
	Projects() []status.Project
	Tree(origin, parent string) ([]status.Node, error)
	Graph(origin, parent string) (status.Graph, error)
	Events(origin string, after uint64) []status.Event
	Hide(ctx context.Context, origin string) error
	Show(ctx context.Context, origin string) error
	Add(ctx context.Context, host, path string) (status.ProjectLocation, error)
	Touch()
}

func WithStatus(svc StatusService) WSServerOption { return func(s *WSServer) { s.status = svc } }

type statusOriginParams struct {
	Origin string `json:"origin"`
}
type statusLevelParams struct {
	Origin string `json:"origin"`
	Parent string `json:"parent"`
}
type statusEventsParams struct {
	Origin string `json:"origin"`
	After  *int64 `json:"after"`
}
type statusAddParams struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

func validateStatusOrigin(raw json.RawMessage) string {
	var p statusOriginParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.Origin == "" {
		return "origin is required"
	}
	return ""
}

func validateStatusLevel(raw json.RawMessage) string {
	var p statusLevelParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.Origin == "" {
		return "origin is required"
	}
	return ""
}

func validateStatusEvents(raw json.RawMessage) string {
	var p statusEventsParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.After == nil || *p.After < 0 {
		return "after is required and must be >= 0"
	}
	return ""
}

func validateStatusAdd(raw json.RawMessage) string {
	var p statusAddParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !filepath.IsAbs(p.Path) {
		return "path must be absolute"
	}
	return ""
}

func (s *WSServer) statusSpecs() []methodSpec {
	wired := func() bool { return s.status != nil }
	const unavailable = "status not available"
	on := func(method string, v paramsValidator, pick func(statusHandlers) handlerFunc) methodSpec {
		return whenAvailable(regResponder(s.lane, method, v, func(r Responder) handlerFunc {
			return pick(statusHandlers{svc: s.status, r: r})
		}), wired, unavailable)
	}
	return []methodSpec{
		on("status.overview.read", noParams(), func(h statusHandlers) handlerFunc { return h.overview }),
		on("status.tree.read", params(validateStatusLevel), func(h statusHandlers) handlerFunc { return h.tree }),
		on("status.graph.read", params(validateStatusLevel), func(h statusHandlers) handlerFunc { return h.graph }),
		on("status.events.read", params(validateStatusEvents), func(h statusHandlers) handlerFunc { return h.events }),
		on("status.projects.hide", params(validateStatusOrigin), func(h statusHandlers) handlerFunc { return h.hide }),
		on("status.projects.show", params(validateStatusOrigin), func(h statusHandlers) handlerFunc { return h.show }),
		on("status.projects.add", params(validateStatusAdd), func(h statusHandlers) handlerFunc { return h.add }),
	}
}

type statusHandlers struct {
	svc StatusService
	r   Responder
}

func statusErrorCode(err error) int {
	if errors.Is(err, status.ErrUnknownProject) || errors.Is(err, status.ErrUnknownIssue) || errors.Is(err, status.ErrRemoteNotAvailable) {
		return -32602
	}
	return -32603
}
```

Handlers (`overview`, `tree`, `graph`, `events`, `hide`, `show`, `add`), each `func (h statusHandlers) name(ctx context.Context, req jsonrpcRequest)`: read handlers call `h.svc.Touch()` first; each unmarshals its params struct from `req.Params`, calls the service, and on error answers with the `Responder` error method, `statusErrorCode(err)` and `err.Error()`; on success `h.r.TryResult(req.ID, mustMarshal(<dto>))`. DTOs, all slices made with `make`:

```go
type statusProgressDTO struct {
	Closed int `json:"closed"`
	Total  int `json:"total"`
}
type statusStageDTO struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	State          string            `json:"state"`
	Tasks          statusProgressDTO `json:"tasks"`
	NotDecomposed  int               `json:"notDecomposed"`
	InProgress     int               `json:"inProgress"`
	BlockedOutside int               `json:"blockedOutside"`
}
type statusMilestoneDTO struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Status        string            `json:"status"`
	Tasks         statusProgressDTO `json:"tasks"`
	StagesDone    int               `json:"stagesDone"`
	Stages        []statusStageDTO  `json:"stages"`
	NotDecomposed int               `json:"notDecomposed"`
	InProgress    int               `json:"inProgress"`
	Stale         int               `json:"stale"`
}
type statusLocationDTO struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	State    string `json:"state"`
	LastSeen int64  `json:"lastSeen"`
	Source   bool   `json:"source"`
}
type statusProjectDTO struct {
	Origin            string               `json:"origin"`
	Health            string               `json:"health"`
	Error             string               `json:"error"`
	Diverged          bool                 `json:"diverged"`
	Stale             bool                 `json:"stale"`
	Locations         []statusLocationDTO  `json:"locations"`
	Milestones        []statusMilestoneDTO `json:"milestones"`
	OutsideMilestones int                  `json:"outsideMilestones"`
}
type statusOverviewResult struct {
	Revision uint64             `json:"revision"`
	Projects []statusProjectDTO `json:"projects"`
}
type statusNodeDTO struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Type          string            `json:"type"`
	Status        string            `json:"status"`
	Tasks         statusProgressDTO `json:"tasks"`
	NotDecomposed bool              `json:"notDecomposed"`
	BlockedBy     int               `json:"blockedBy"`
	HasChildren   bool              `json:"hasChildren"`
}
type statusTreeResult struct {
	Nodes []statusNodeDTO `json:"nodes"`
}
type statusGraphNodeDTO struct {
	statusNodeDTO
	External bool     `json:"external"`
	Stage    string   `json:"stage"`
	Upstream []string `json:"upstream"`
}
type statusEdgeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type statusGraphResult struct {
	Parent string               `json:"parent"`
	Nodes  []statusGraphNodeDTO `json:"nodes"`
	Edges  []statusEdgeDTO      `json:"edges"`
}
type statusEventDTO struct {
	Seq     uint64 `json:"seq"`
	At      int64  `json:"at"`
	Origin  string `json:"origin"`
	IssueID string `json:"issueId"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
}
type statusEventsResult struct {
	Events []statusEventDTO `json:"events"`
}
type statusOkResult struct {
	Ok bool `json:"ok"`
}
type statusAddResult struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	State    string `json:"state"`
	LastSeen int64  `json:"lastSeen"`
}
type statusChangedParams struct {
	Origin   string `json:"origin"`
	Revision uint64 `json:"revision"`
}

func (s *WSServer) BroadcastStatusChanged(origin string, revision uint64) {
	s.connsMu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()
	params := mustMarshal(statusChangedParams{Origin: origin, Revision: revision})
	for _, wc := range conns {
		_ = wc.TryNotify("status.changed", params)
	}
}
```

Write the converters `toOverviewDTO(rev, []status.Project)`, `toNodeDTO(status.Node)`, `toGraphDTO(status.Graph)`, `toEventsDTO([]status.Event)`. In `ws.go` add `status StatusService` beside `notifyFeed`, and after `specs = append(specs, s.notifyFeedSpecs()...)` add `specs = append(specs, s.statusSpecs()...)`.

- [ ] **Step 5: Generate renderer types** — `cd frontend && npm run contracts && npm run contracts:check` — Expected: new `src/generated/status.*.ts`; check exits 0.

- [ ] **Step 6: Run the tests** — `go test ./internal/transport/ -count=1` — Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add contracts internal/transport frontend/src/generated
git commit   # feat(transport): status.* reads, hide, show, add and status.changed, each under its contract (<bead>)
```

---

### Task 10: wiring, and the DONE WHEN check on the real backend

**Files:**

- Modify: `internal/app/app.go`
- Create: `internal/app/status_acceptance_test.go`

**Interfaces:**

- Consumes: `status.New`, `status.Deps`, `(*Service).Run`, `.OnChange`; `transport.WithStatus`, `(*WSServer).BroadcastStatusChanged`; `trackerlocal.NewOpener`; `contentDB.Ledger()`, `contentDB.StatusLocations()`; `gitFactory`.

**Acceptance Criteria:**

- `deadcode -tags gtk3 -whylive` prints a chain from `main` for `github.com/shady2k/nocx/internal/status.(*Service).Poll`, `.../internal/tracker/local.(*brTracker).ReadExport` and `.../internal/content.(*statusLocationSqlite).Upsert`.
- `TestTheStatusScreenFollowsABrWriteOnALocalProject` (skips only without `br` or an integrated login shell):
  1. creates a temp git repo with `origin = git@github.com:acceptance/status.git` and a `br` workspace: milestone epic "Ship" (label `milestone`), stages "One" and "Two", task "Do it" under "One";
  2. starts `newLocalPaneApp`, opens a pane, waits for `lifecycle.changed` `prompt_ready` with a domain, calls `lifecycle.submitAttempt {domain, command: "true", cwd: <repo>, host: "", source: "user"}`;
  3. reads `status.overview.read` until `github.com/acceptance/status` has milestone "Ship" with two stages and stage "One" `tasks.total == 1` — a 20 s deadline on that condition, re-reading on each `status.changed`;
  4. runs `br close <task> --reason acceptance` in the repo;
  5. waits for `status.changed` for that origin, then asserts stage "One" `tasks.closed == 1` and `status.events.read {origin: "", after: 0}` holds a `closed` event for the task;
  6. `status.graph.read {origin, parent: <milestone>}` returns both stages as nodes.

- [ ] **Step 1: Write the failing acceptance test**

Build it from `TestLocalEnhancedSessionEstablishesThroughProductionWiring` (`app_test.go:395`) for the pane and the prompt wait, with one reader goroutine over the connection that routes responses by id and forwards `status.changed` notifications to a channel. The repository:

```go
func brAcceptanceRepo(t *testing.T) (dir, milestone, stageOne, task string) {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br is not installed")
	}
	dir = t.TempDir()
	run := func(argv ...string) string {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", argv, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("git", "init", "-q")
	run("git", "remote", "add", "origin", "git@github.com:acceptance/status.git")
	run("br", "init", "--prefix", "acc")
	milestone = run("br", "create", "Ship", "-t", "epic", "-l", "milestone", "--silent")
	stageOne = run("br", "create", "One", "-t", "epic", "--parent", milestone, "--silent")
	run("br", "create", "Two", "-t", "epic", "--parent", milestone, "--silent")
	task = run("br", "create", "Do it", "-t", "task", "--parent", stageOne, "--silent")
	return dir, milestone, stageOne, task
}
```

The assertions are exactly steps 3–6 of the acceptance criteria.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run TestTheStatusScreen -count=1 -v`
Expected: FAIL — `status.overview.read` answers `-32601 status not available`.

- [ ] **Step 3: Wire it in `app.go`**

After `gitFactory := gitlocal.NewFactory()`:

```go
	statusSvc := status.New(status.Deps{
		Ledger:    contentDB.Ledger(),
		Locations: contentDB.StatusLocations(),
		Repos:     gitFactory,
		Trackers:  trackerlocal.NewOpener(),
		Clock:     wallClock{},
		Log:       slogger,
	})
```

Search `internal/app` for an existing `Now() time.Time` clock type and use it; only if none exists add `type wallClock struct{}` with `func (wallClock) Now() time.Time { return time.Now() }`. Add `transport.WithStatus(statusSvc),` to `tpOpts` beside `transport.WithSkillChecks(...)`. After `notifyFeed.OnChange(tp.BroadcastFeedChanged)` add `statusSvc.OnChange(tp.BroadcastStatusChanged)`. Start `go statusSvc.Run(ctx)` where the app starts its other long-running goroutines, under the same context `Shutdown` cancels. Imports: `github.com/shady2k/nocx/internal/status`, `trackerlocal "github.com/shady2k/nocx/internal/tracker/local"`.

- [ ] **Step 4: Run the acceptance test and the reachability checks**

```bash
go build ./... && go test ./internal/app/ -run TestTheStatusScreen -count=1 -v
for sym in 'internal/status.(*Service).Poll' 'internal/tracker/local.(*brTracker).ReadExport' 'internal/content.(*statusLocationSqlite).Upsert'; do
  deadcode -tags gtk3 -whylive "github.com/shady2k/nocx/$sym" ./... | head -3
done
```

Expected: PASS; each `-whylive` prints a chain starting at `main`.

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/app/status_acceptance_test.go
git commit   # feat(app): the status service runs in the backend, and a br close reaches the screen's model (<bead>)
```

---

## After the plan

- The integrator runs `make ci-full` on the merged branch (AGENTS.md: the gate belongs to whoever integrates).
- Plan 2 (renderer: Status tab, project cards, roadmap tree, live updates) starts from the `frontend/src/generated/status.*.ts` Task 9 produces.
