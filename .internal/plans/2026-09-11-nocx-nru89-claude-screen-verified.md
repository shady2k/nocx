# nocx reads Claude's screen correctly on the version you run today — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development
> (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task
> maps to an existing bead under epic `nocx-nru89` (named in its heading). Steps use checkbox
> (`- [ ]`) syntax for human readability. The tracker is `br`, not `bd` (AGENTS.md).

**Goal:** A person watching a Claude pane in nocx sees the right state — working from the first
second of a turn, error while the API is waiting, working under a `/btw` overlay — and the rule is
checked against a manifest of owner-labelled moments recorded on the current Claude Code.

**Architecture:** The shipped rule `internal/agentdriver/claude.rule.json` gains three branches and one
anchor. Fixed-moment expectations move from Go tests into
`internal/agentdriver/testdata/captures/manifest.json`, read through `internal/agentcapture` — the one
reader of the capture format. The user's seam is proved in `internal/transport` over the real socket
with the real watcher and rule. `cmd/agent-capture` gains an isolated environment so recordings of a
real Claude never inherit the caller's session or read the person's Claude configuration.

**Tech Stack:** Go 1.x (`go test`), JSON rule document, `cmd/agent-capture` (real PTY via
`github.com/creack/pty`), Claude Code 2.1.266 against an Anthropic-compatible LM Studio endpoint for
recording only.

**Design:** `.internal/specs/2026-09-11-coordinator-surface-after-herdr-design.md` §6 (owner-approved
at 69b9fdbb).

## Global Constraints

- Every document, comment, commit message and bead text is English (AGENTS.md "Language").
- Commit subject format: `<type>(<scope>): <imperative, lower case, no full stop> (<bead-id>)`, prose
  body, `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- TDD: the failing test first, run it red, then the change, run it green.
- `agentcapture.Read`'s contract does not change (calibration depends on it: `agentcalib` writes
  headers without `Started` and treats a load error as no evidence against the rule).
- No CI test starts `claude` or reaches a model endpoint. Live recording happens only in Task 6.
- Branch 0 of the rule stays the first permission branch (`TestExplainNamesTheBranchThatAnswered`),
  the `free_text` branch stays last (`TestExplainSaysWhereEachBranchStopped`,
  `internal/transport/ws_agent_emitting_test.go`), and the first anchor stays `bottomRule`
  (`TestExplainReportsWhereAnchorsBound`).
- A worker runs the unit tests for the packages it touched (`go test ./internal/agentdriver/`,
  `./internal/transport/`, `./cmd/agent-capture/`); `make ci-full` is the coordinator's, on the merged
  tree (AGENTS.md "Git authority").
- Discovery captures of 2026-09-11 are preserved in `~/.local/share/nocx-dev/detect-discovery-2026-09-11/`
  (`btw.jsonl` sha256 prefix `5a4ecd34de1149e9`).

---

### Task 1: One replay path and the manifest (`nocx-nru89.3`)

**Files:**

- Modify: `internal/agentdriver/capture_test.go` (replace the test-only JSONL reader)
- Modify: `internal/agentdriver/claude_test.go:171-191` and `internal/agentdriver/observation_test.go:179`
  (the two callers that paint onto a replayed screen)
- Modify: `internal/agentdriver/claude_test.go` (delete the fixed-moment tests the manifest now holds)
- Create: `internal/agentdriver/manifest_test.go`
- Create: `internal/agentdriver/manifest_check_test.go`
- Create: `internal/agentdriver/testdata/captures/manifest.json`

**Interfaces:**

- Consumes: `agentcapture.Read(path) (Header, []Chunk, error)`, `agentcapture.NewReplayer(log.Logger, Header) (*Replayer, error)`,
  `(*Replayer).Feed([]Chunk) error`, `(*Replayer).Frame() (panegrid.Frame, error)`, `(*Replayer).Close()`,
  `agentcapture.ChunksThrough([]Chunk, int64, int) int`, `agentcapture.Frames(log.Logger, Header, []Chunk, []int64) ([]Moment, error)`,
  `(*agentdriver.Registry).Explain(agent string, f panegrid.Frame) Explanation` (fields `State`, `Matched`).
- Produces (package `agentdriver_test`): `replayer(t, name string, atMs int64) *agentcapture.Replayer`,
  `replay(t, name string, atMs int64) panegrid.Frame`, `capturePath(name string) string`,
  `type manifest`, `type manifestEntry`, `loadManifest(path string) (manifest, error)`,
  `checkManifest(m manifest, dir string, reg *agentdriver.Registry) []error`, `const manifestPath`.

**Acceptance Criteria:**

- A complete manifest over the committed captures passes (`TestTheManifestHolds`).
- A missing or unreadable capture, a malformed manifest, an unknown field, a wrong version, no agent,
  an unknown state name, the state `exited`, a moment not in the inventory, an inventory moment with no
  entry or with two, an unverified entry that also names a recording, a recorded entry with no mark,
  a wrong state and a wrong branch each fail with a named cause.
- The mutation, extraction and explanation tests and the calibration tests stay green on the shared
  replayer; `captureHeader`/`captureChunk` and the hand-written scanner are gone.

- [ ] **Step 1: Replace the test-only reader in `capture_test.go`**

Replace the whole file body from the import block down to (not including) `func screen` with:

```go
import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
)

// captureNames is the corpus, named once. Two tests walk all of it — the
// closed-set sweep in claude_test.go and the projection sweep in
// observation_test.go — and a corpus named twice is a corpus that grows in one
// place only.
var captureNames = []string{
	"claude-error",
	"claude-idle", "claude-idle-60", "claude-idle-80", "claude-working",
	"claude-permission", "claude-permission-60", "claude-modal", "claude-subagent",
	"claude-trust",
}

// capturePath names a committed capture.
func capturePath(name string) string {
	return filepath.Join("testdata", "captures", name+".jsonl")
}

// replayer feeds a capture up to atMs through internal/agentcapture — the one
// reader of the format, the same one calibration and cmd/agent-capture use —
// and hands the replayer back, so a test that wants to paint something MORE
// onto that screen can Feed it.
func replayer(t *testing.T, name string, atMs int64) *agentcapture.Replayer {
	t.Helper()
	header, chunks, err := agentcapture.Read(capturePath(name))
	if err != nil {
		t.Fatalf("read capture %s: %v", name, err)
	}
	r, err := agentcapture.NewReplayer(log.NewSlogAdapter(nil), header)
	if err != nil {
		t.Fatalf("replayer for %s: %v", name, err)
	}
	t.Cleanup(r.Close)
	if err := r.Feed(chunks[:agentcapture.ChunksThrough(chunks, atMs, 0)]); err != nil {
		t.Fatalf("feed %s to %dms: %v", name, atMs, err)
	}
	return r
}

// replay is the common case: the screen at a moment, and nothing else.
func replay(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	f, err := replayer(t, name, atMs).Frame()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	return f
}
```

Keep the file's leading package comment and `func screen` unchanged.

- [ ] **Step 2: Move the two painting callers onto the replayer**

In `claude_test.go` `TestTextTheAgentPrintedCannotForgeAnyVerdict`, replace
`store, pane := replayStore(t, "claude-idle", 11000)` with `r := replayer(t, "claude-idle", 11000)`,
replace `store.Feed(pane, []byte(forged))` with:

```go
	if err := r.Feed([]agentcapture.Chunk{{Data: forged}}); err != nil {
		t.Fatalf("feed forged text: %v", err)
	}
```

and replace `fr, err := store.Frame(pane)` with `fr, err := r.Frame()`. Add
`"github.com/shady2k/nocx/internal/agentcapture"` to its imports. Make the same three replacements in
`observation_test.go` at the `replayStore(t, "claude-idle", 11000)` call (line 179), adding the same
import.

- [ ] **Step 3: Run the package to confirm the replayer is a drop-in**

Run: `go test ./internal/agentdriver/ -count=1`
Expected: `ok` (same assertions, now through `agentcapture.Read`, which also validates offsets).

- [ ] **Step 4: Write the manifest loader and checker (`manifest_test.go`)**

```go
package agentdriver_test

// THE CORPUS'S EXPECTATIONS ARE DATA (nocx-nru89.3).
//
// A fixed moment's expected state used to be a Go test per moment. It is now one
// entry in testdata/captures/manifest.json, read through internal/agentcapture,
// so recording a new Claude version adds entries rather than tests, and a
// moment nobody could record says so as an unverified entry instead of
// disappearing. Tests that PAINT onto a replayed screen, check extraction, or
// check explanation behaviour stay tests: those are not "this moment is this
// state".

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
)

const manifestPath = "testdata/captures/manifest.json"

// manifest is the owner's labels for the corpus. Inventory is the list of
// moments the design says must be covered (spec §6.4); every inventory moment
// has exactly one entry, recorded or unverified. Entries with no Moment are
// further recorded marks the corpus keeps as regressions.
type manifest struct {
	Version   int             `json:"version"`
	Agent     string          `json:"agent"`
	Inventory []string        `json:"inventory"`
	Entries   []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	Moment     string            `json:"moment,omitempty"`
	Capture    string            `json:"capture,omitempty"`
	AtMs       *int64            `json:"atMs,omitempty"`
	State      agentdriver.State `json:"state,omitempty"`
	Branch     *int              `json:"branch,omitempty"`
	Unverified string            `json:"unverified,omitempty"`
	Note       string            `json:"note,omitempty"`
}

func (e manifestEntry) recorded() bool { return e.Unverified == "" }

func (e manifestEntry) String() string {
	if !e.recorded() {
		return fmt.Sprintf("moment %q (unverified)", e.Moment)
	}
	at := int64(-1)
	if e.AtMs != nil {
		at = *e.AtMs
	}
	return fmt.Sprintf("%s@%dms", e.Capture, at)
}

// loadManifest reads the manifest and refuses one that cannot be trusted,
// naming why. It checks the manifest's own shape only; whether the rule agrees
// is checkManifest's.
func loadManifest(path string) (manifest, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixture path the test names
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m manifest
	if err := dec.Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Version != 1 {
		return manifest{}, fmt.Errorf("manifest version %d, want 1", m.Version)
	}
	if m.Agent == "" {
		return manifest{}, errors.New("manifest names no agent")
	}
	inventory := make(map[string]int, len(m.Inventory))
	for _, id := range m.Inventory {
		if id == "" {
			return manifest{}, errors.New("manifest inventory has an empty moment id")
		}
		if _, dup := inventory[id]; dup {
			return manifest{}, fmt.Errorf("manifest inventory names %q twice", id)
		}
		inventory[id] = 0
	}
	for i, e := range m.Entries {
		if e.Moment != "" {
			if _, ok := inventory[e.Moment]; !ok {
				return manifest{}, fmt.Errorf("entry %d names moment %q, which is not in the inventory", i, e.Moment)
			}
			inventory[e.Moment]++
		}
		if !e.recorded() {
			if e.Moment == "" {
				return manifest{}, fmt.Errorf("entry %d is unverified but names no moment", i)
			}
			if e.Capture != "" || e.AtMs != nil || e.State != "" || e.Branch != nil {
				return manifest{}, fmt.Errorf("entry %d is unverified and also names a recording", i)
			}
			continue
		}
		switch {
		case e.Capture == "" || e.AtMs == nil:
			return manifest{}, fmt.Errorf("entry %d is recorded but names no capture and mark", i)
		case *e.AtMs < 0:
			return manifest{}, fmt.Errorf("entry %d (%s) has a negative mark", i, e)
		case !e.State.Valid():
			return manifest{}, fmt.Errorf("entry %d (%s) names state %q, which is not a driver state", i, e, e.State)
		case e.State == agentdriver.StateExited:
			return manifest{}, fmt.Errorf("entry %d (%s) names exited, which is a fact about the process and never a screen", i, e)
		}
	}
	for _, id := range m.Inventory {
		switch n := inventory[id]; n {
		case 0:
			return manifest{}, fmt.Errorf("inventory moment %q has no entry", id)
		case 1:
		default:
			return manifest{}, fmt.Errorf("inventory moment %q has %d entries, want exactly one", id, n)
		}
	}
	return m, nil
}

// checkManifest replays every recorded entry and asks the registry, returning
// one error per failure or disagreement. Entries are grouped by capture and
// replayed in mark order, because a capture only replays forward.
func checkManifest(m manifest, dir string, reg *agentdriver.Registry) []error {
	byCapture := map[string][]manifestEntry{}
	var names []string
	for _, e := range m.Entries {
		if !e.recorded() {
			continue
		}
		if _, seen := byCapture[e.Capture]; !seen {
			names = append(names, e.Capture)
		}
		byCapture[e.Capture] = append(byCapture[e.Capture], e)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		entries := byCapture[name]
		sort.SliceStable(entries, func(i, j int) bool { return *entries[i].AtMs < *entries[j].AtMs })
		header, chunks, err := agentcapture.Read(filepath.Join(dir, name+".jsonl"))
		if err != nil {
			errs = append(errs, fmt.Errorf("capture %s: %w", name, err))
			continue
		}
		marks := make([]int64, len(entries))
		for i, e := range entries {
			marks[i] = *e.AtMs
		}
		moments, err := agentcapture.Frames(log.NewSlogAdapter(nil), header, chunks, marks)
		if err != nil {
			errs = append(errs, fmt.Errorf("replay %s: %w", name, err))
			continue
		}
		for i, e := range entries {
			ex := reg.Explain(m.Agent, moments[i].Frame)
			if ex.State != e.State {
				errs = append(errs, fmt.Errorf("%s: state %q, want %q", e, ex.State, e.State))
			}
			if e.Branch != nil && ex.Matched != *e.Branch {
				errs = append(errs, fmt.Errorf("%s: matched branch %d, want %d", e, ex.Matched, *e.Branch))
			}
		}
	}
	return errs
}

// TestTheManifestHolds is the rule check of nocx-nru89: every recorded moment
// of the corpus classifies to the owner's state.
func TestTheManifestHolds(t *testing.T) {
	m, err := loadManifest(manifestPath)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	for _, err := range checkManifest(m, filepath.Dir(manifestPath), registry(t)) {
		t.Error(err)
	}
}
```

- [ ] **Step 5: Write the failure tests (`manifest_check_test.go`)**

```go
package agentdriver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

func TestAManifestThatCannotBeTrustedIsRefusedByName(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"not json", `{`, "decode manifest"},
		{"unknown field", `{"version":1,"agent":"claude","inventory":[],"entries":[],"extra":1}`, "unknown field"},
		{"wrong version", `{"version":2,"agent":"claude","inventory":[],"entries":[]}`, "version 2"},
		{"no agent", `{"version":1,"inventory":[],"entries":[]}`, "names no agent"},
		{"unknown state", `{"version":1,"agent":"claude","inventory":[],"entries":[{"capture":"claude-idle","atMs":11000,"state":"idle"}]}`, "not a driver state"},
		{"exited", `{"version":1,"agent":"claude","inventory":[],"entries":[{"capture":"claude-idle","atMs":11000,"state":"exited"}]}`, "fact about the process"},
		{"moment not in inventory", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[{"moment":"idle-120","capture":"claude-idle","atMs":11000,"state":"free_text"},{"moment":"nope","unverified":"x"}]}`, "not in the inventory"},
		{"inventory moment with no entry", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[]}`, `"idle-120" has no entry`},
		{"inventory moment twice", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[{"moment":"idle-120","capture":"claude-idle","atMs":11000,"state":"free_text"},{"moment":"idle-120","unverified":"x"}]}`, "want exactly one"},
		{"unverified with a recording", `{"version":1,"agent":"claude","inventory":["idle-120"],"entries":[{"moment":"idle-120","unverified":"x","capture":"claude-idle"}]}`, "also names a recording"},
		{"recorded without a mark", `{"version":1,"agent":"claude","inventory":[],"entries":[{"capture":"claude-idle","state":"free_text"}]}`, "names no capture and mark"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			_, err := loadManifest(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadManifest error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		_, err := loadManifest(filepath.Join(t.TempDir(), "absent.json"))
		if err == nil || !strings.Contains(err.Error(), "read manifest") {
			t.Fatalf("loadManifest error = %v, want a read failure", err)
		}
	})
}

func TestAManifestThatDisagreesWithTheRuleFailsByName(t *testing.T) {
	dir := filepath.Dir(manifestPath)
	at := int64(11000)
	zero := 0
	cases := []struct {
		name  string
		dir   string
		entry manifestEntry
		want  string
	}{
		{"wrong state", dir, manifestEntry{Capture: "claude-idle", AtMs: &at, State: agentdriver.StateWorking}, `state "free_text", want "working"`},
		{"wrong branch", dir, manifestEntry{Capture: "claude-idle", AtMs: &at, State: agentdriver.StateFreeText, Branch: &zero}, "matched branch"},
		{"missing capture", dir, manifestEntry{Capture: "no-such-capture", AtMs: &at, State: agentdriver.StateFreeText}, "capture no-such-capture"},
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "broken.jsonl"), []byte("{\n"), 0o600); err != nil {
		t.Fatalf("write broken capture: %v", err)
	}
	cases = append(cases, struct {
		name  string
		dir   string
		entry manifestEntry
		want  string
	}{"unreadable capture", broken, manifestEntry{Capture: "broken", AtMs: &at, State: agentdriver.StateFreeText}, "capture broken"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := manifest{Version: 1, Agent: "claude", Entries: []manifestEntry{tc.entry}}
			errs := checkManifest(m, tc.dir, registry(t))
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("checkManifest = %v, want one error containing %q", errs, tc.want)
			}
		})
	}
	t.Run("a correct entry passes", func(t *testing.T) {
		m := manifest{Version: 1, Agent: "claude", Entries: []manifestEntry{{Capture: "claude-idle", AtMs: &at, State: agentdriver.StateFreeText}}}
		if errs := checkManifest(m, dir, registry(t)); len(errs) != 0 {
			t.Fatalf("checkManifest = %v, want none", errs)
		}
	})
}
```

A replay failure after a successful `Read` has no file-shaped trigger (`Read` already refuses bad
geometry, and `checkManifest` sorts marks); `agentcapture.Frames`' own tests cover its error path, and
`checkManifest` reports it as `replay <capture>`.

- [ ] **Step 6: Run the new tests red**

Run: `go test ./internal/agentdriver/ -run 'Manifest' -count=1`
Expected: FAIL — `TestTheManifestHolds: manifest: read manifest: open testdata/captures/manifest.json: no such file or directory`; the failure tests pass.

- [ ] **Step 7: Write `testdata/captures/manifest.json` from the corpus's existing assertions**

```json
{
  "version": 1,
  "agent": "claude",
  "inventory": [
    "theme-picker",
    "security-notes",
    "folder-trust",
    "idle-120",
    "idle-80",
    "idle-60",
    "turn-before-timer",
    "turn-with-timer",
    "turn-finished",
    "btw-overlay",
    "transcript-viewer",
    "model-menu",
    "bash-permission",
    "write-permission",
    "subagent-running",
    "subagent-finished",
    "api-refused",
    "api-waiting"
  ],
  "entries": [
    {
      "moment": "theme-picker",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-nru89.2)"
    },
    {
      "moment": "security-notes",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-nru89.2)"
    },
    {
      "moment": "folder-trust",
      "capture": "claude-trust",
      "atMs": 11000,
      "state": "permission_choice",
      "note": "numbers none of its options; identified by the cursor on the marker and the confirm legend below (nocx-f545a.2)"
    },
    {
      "moment": "idle-120",
      "capture": "claude-idle",
      "atMs": 11000,
      "state": "free_text",
      "note": "the idle input box is where nocx may type"
    },
    { "moment": "idle-80", "capture": "claude-idle-80", "atMs": 11000, "state": "free_text" },
    {
      "moment": "idle-60",
      "capture": "claude-idle-60",
      "atMs": 11000,
      "state": "free_text",
      "note": "the narrow geometry of a side-by-side worker pane"
    },
    {
      "moment": "turn-before-timer",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-ys9jd)"
    },
    {
      "moment": "turn-with-timer",
      "capture": "claude-working",
      "atMs": 17000,
      "state": "working",
      "note": "the spinner with its elapsed timer"
    },
    {
      "moment": "turn-finished",
      "capture": "claude-working",
      "atMs": 44000,
      "state": "free_text",
      "note": "the box never went away; this separates the box being on screen from the box waiting"
    },
    {
      "moment": "btw-overlay",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-nru89.4)"
    },
    {
      "moment": "transcript-viewer",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-nru89.2)"
    },
    {
      "moment": "model-menu",
      "capture": "claude-modal",
      "atMs": 20000,
      "state": "modal_choice",
      "note": "a menu the user opened, not a tool approval"
    },
    {
      "moment": "bash-permission",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-nru89.2)"
    },
    {
      "moment": "write-permission",
      "capture": "claude-permission",
      "atMs": 49000,
      "state": "permission_choice",
      "branch": 0,
      "note": "replaces the input box; its selected row opens with the input marker"
    },
    { "capture": "claude-permission-60", "atMs": 49000, "state": "permission_choice" },
    {
      "moment": "subagent-running",
      "capture": "claude-subagent",
      "atMs": 30000,
      "state": "working",
      "note": "task panel below the mode line, no spinner"
    },
    {
      "capture": "claude-subagent",
      "atMs": 40000,
      "state": "working",
      "note": "panel gone, mode line says /tasks to see subagents"
    },
    {
      "moment": "subagent-finished",
      "capture": "claude-subagent",
      "atMs": 70000,
      "state": "free_text",
      "note": "the interval's second end; a finished turn's summary in the status slot is not an error"
    },
    {
      "moment": "api-refused",
      "capture": "claude-error",
      "atMs": 30000,
      "state": "error",
      "note": "the TUI's own error, in the spinner's slot, told apart by grammar"
    },
    { "capture": "claude-error", "atMs": 20000, "state": "error" },
    { "capture": "claude-error", "atMs": 44000, "state": "error" },
    {
      "moment": "api-waiting",
      "unverified": "not yet recorded on Claude Code 2.1.266 (nocx-emors)"
    },
    {
      "capture": "claude-idle",
      "atMs": 0,
      "state": "unknown",
      "note": "the screen before the TUI has drawn"
    }
  ]
}
```

- [ ] **Step 8: Run the manifest green**

Run: `go test ./internal/agentdriver/ -run 'Manifest' -count=1`
Expected: PASS. If `claude-subagent@30000ms` or `@40000ms` reports a state other than `working` (the old
tests asserted only "not free_text"), stop and file a bead for the rule with the reported state; do not
weaken the entry — `working` is the owner's label.

- [ ] **Step 9: Delete the Go tests the manifest now holds**

From `claude_test.go` delete: `TestTheIdleInputBoxAcceptsFreeText`, `TestTheIdleInputBoxAt60ColumnsAcceptsFreeText`,
`TestTheIdleInputBoxAt80ColumnsAcceptsFreeText`, `TestThe60ColumnToolApprovalDialogIsAPermissionChoice`,
`TestATurnInFlightIsWorking`, `TestTheSameTurnFinishedIsFreeTextAgain`, `TestTheToolApprovalDialogIsAPermissionChoice`,
`TestAUserOpenedMenuIsAModalChoice`, `TestTheFolderTrustQuestionIsAPermissionChoice`,
`TestABackgroundAgentReportedByItsPanelIsNotFreeText`, `TestABackgroundAgentReportedByTheModeLineIsNotFreeText`,
`TestWhenTheBackgroundAgentFinishesThePaneAcceptsInputAgain`, `TestTheTUIsOwnErrorIsErrorAndNotFreeText`,
`TestAFinishedTurnsSummaryInTheSameSlotIsNotAnError`. In `TestAFrameWithNoChromeAtAllIsUnknown` delete the
`blank := replay(...)` half (now the `claude-idle@0` entry) and keep the zero-frame half. Keep every
`screen(...)`-based test, `TestTextTheAgentPrintedCannotForgeAnyVerdict`, the closed-set sweep and
`TestTheClaudeDriverNamesTheAgentItDrives`. Leave `verify_corpus_test.go` untouched.

- [ ] **Step 10: Run the package and the calibration package**

Run: `go test ./internal/agentdriver/ ./internal/agentcalib/ ./internal/agentcapture/ -count=1`
Expected: `ok` for all three.

- [ ] **Step 11: Commit**

```bash
git add internal/agentdriver/capture_test.go internal/agentdriver/claude_test.go internal/agentdriver/observation_test.go internal/agentdriver/manifest_test.go internal/agentdriver/manifest_check_test.go internal/agentdriver/testdata/captures/manifest.json
git commit -m "test(agentdriver): the corpus's fixed-moment expectations move into a manifest read through agentcapture (nocx-nru89.3)" -m "The rule tests kept their own JSONL scanner beside agentcapture.Read, the reader calibration and cmd/agent-capture use, so one format had two readers that agreed only on the files anybody tried. The scanner is gone and every replay goes through agentcapture. Fixed-moment expectations, which were one Go test per moment, are now manifest entries checked by one test, so a new Claude version adds entries rather than tests and a moment nobody could record stays visible as an unverified entry. Tests that paint onto a replayed screen, check extraction or check explanation stay tests, because they do not say that a moment is a state." -m "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: A turn reads working before its elapsed timer (`nocx-ys9jd`)

**Files:**

- Create: `internal/agentdriver/testdata/captures/claude-lmstudio-turn.jsonl` (copy of the discovery capture)
- Create: `internal/agentdriver/testdata/captures/scripts/lmstudio-turn.script`
- Modify: `internal/agentdriver/testdata/captures/README.md` (one table row and one provenance paragraph)
- Modify: `internal/agentdriver/capture_test.go` (`captureNames` gains `"claude-lmstudio-turn"`)
- Create: `internal/transport/ws_paneobserve_states_test.go`
- Modify: `internal/agentdriver/claude.rule.json` (one branch)
- Modify: `internal/agentdriver/claude_test.go` (one near-miss test)
- Modify: `internal/agentdriver/testdata/captures/manifest.json`

**Interfaces:**

- Consumes (package `transport` tests): `newObservedWS(t) (*WSServer, *panegrid.Store, *paneobserve.Watcher, *feedablePTY)`,
  `connectWS`, `openSessionOnConn(t, ws, conn, id)`, `(*feedablePTY).emit(t, string)`,
  `readNotification(t, conn, method, wantWithin) json.RawMessage`, `claudeIdleChrome(cols int) string`,
  `observationChangedParams` (field `State string`). Task 1's manifest types.
- Produces: `claudeTurnStartingChrome(cols int) string` and `TestAnObservedPaneReportsEachStateItsScreenShows`
  (extended by Task 3).

**Acceptance Criteria:**

- Over the real socket, a watched pane whose screen shows a spinner row with no elapsed timer is
  reported `working`, and then the idle box `free_text` — red on the current rule.
- The manifest's `turn-before-timer` entry (`claude-lmstudio-turn@49000`) and the extra mark `@47500`
  classify `working`; every earlier entry still holds.
- An ellipsis the agent printed in its indented transcript, with the box live, stays `free_text`.

- [ ] **Step 1: Commit the discovery capture into the corpus**

```bash
cp ~/.local/share/nocx-dev/detect-discovery-2026-09-11/btw.jsonl internal/agentdriver/testdata/captures/claude-lmstudio-turn.jsonl
cp ~/.local/share/nocx-dev/detect-discovery-2026-09-11/btw.script internal/agentdriver/testdata/captures/scripts/lmstudio-turn.script
sha256sum internal/agentdriver/testdata/captures/claude-lmstudio-turn.jsonl | cut -c1-16
```

Expected: `5a4ecd34de1149e9`. Add `"claude-lmstudio-turn"` to `captureNames` in `capture_test.go`. Add to
`README.md` a table row
`| claude-lmstudio-turn | lmstudio-turn.script | onboarding, trust accepted, idle, ctrl+o transcript viewer (40000), a turn before its timer (47500, 49000), /btw overlay (55000, 58000), API waiting (72000) |`
and a paragraph: captured 2026-09-11 against Claude Code 2.1.266 with `qwen/qwen3.6-35b-a3b` on LM Studio
through `ANTHROPIC_BASE_URL`, under `env -i` with a fresh `HOME` and `CLAUDE_CONFIG_DIR` in `/var/tmp`, 120×40.

- [ ] **Step 2: Write the failing seam test (`internal/transport/ws_paneobserve_states_test.go`)**

```go
package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
)

// claudeTurnStartingChrome is the idle chrome with the status row Claude
// Code 2.1.266 draws in the first seconds of a turn: the spinner and its verb,
// no elapsed timer yet, and the interrupt hint in the mode line (nocx-ys9jd).
func claudeTurnStartingChrome(cols int) string {
	return claudeIdleChrome(cols) +
		"\x1b[6;1H✻ Burrowing…" +
		"\x1b[12;1H  ⏸ manual mode on · esc to interrupt" +
		"\x1b[9;3H"
}

// THE SEAM A PERSON REACHES (nocx-nru89). Nobody calls Explain: a person sees a
// pane's state because the watcher classifies the grid and the transport sends
// session.observationChanged. So the readings the rule fixes are proved here,
// through the real socket, the real watcher and the shipped rule, in the order
// a turn produces them.
func TestAnObservedPaneReportsEachStateItsScreenShows(t *testing.T) {
	ws, store, watch, term := newObservedWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	if err := store.Enrol(sid, 80, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	watch.Watch(sid, "claude")

	steps := []struct {
		name   string
		chrome string
		want   agentdriver.State
	}{
		{"a turn before its elapsed timer", claudeTurnStartingChrome(80), agentdriver.StateWorking},
		{"the turn finished", claudeIdleChrome(80), agentdriver.StateFreeText},
	}
	for _, step := range steps {
		term.emit(t, step.chrome)
		raw := readNotification(t, conn, "session.observationChanged", wantWithin)
		var got observationChangedParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", step.name, err)
		}
		if got.State != string(step.want) {
			t.Fatalf("%s: observed %q, want %q", step.name, got.State, step.want)
		}
	}
}
```

- [ ] **Step 3: Point the manifest at the recorded moment**

In `manifest.json` replace the `turn-before-timer` unverified entry with:

```json
    { "moment": "turn-before-timer", "capture": "claude-lmstudio-turn", "atMs": 49000, "state": "working", "note": "spinner row before the elapsed timer draws (nocx-ys9jd)" },
    { "capture": "claude-lmstudio-turn", "atMs": 47500, "state": "working" },
```

- [ ] **Step 4: Run both red**

Run: `go test ./internal/transport/ -run TestAnObservedPaneReportsEachStateItsScreenShows -count=1`
Expected: FAIL — `a turn before its elapsed timer: observed "free_text", want "working"`.
Run: `go test ./internal/agentdriver/ -run TestTheManifestHolds -count=1`
Expected: FAIL — `claude-lmstudio-turn@47500ms: state "free_text", want "working"` and the same for `@49000ms`.

- [ ] **Step 5: Add the branch to `claude.rule.json`**

Insert immediately after the existing branch whose `text` is `"… ("` (the timer branch), before the
`"below"` branch:

```json
    {
      "state": "working",
      "when": [
        {
          "kind": "regionAny",
          "anchor": "meter",
          "up": true,
          "maxRows": 4,
          "col0Only": true,
          "stopAtBlank": true,
          "suffix": "…"
        }
      ]
    },
```

It reads the same status stack the timer branch reads — unindented rows directly above the token
meter, stopping at a blank — so text the agent printed into its indented transcript cannot reach it.

- [ ] **Step 6: Add the near-miss test to `claude_test.go`**

```go
// The ellipsis branch reads the STATUS STACK, not the transcript (nocx-ys9jd).
// An agent that printed "Loading…" into its own output, indented and above a
// blank row, with the box live beneath it, is idle.
func TestAnEllipsisTheAgentPrintedIsNotATurnStarting(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Loading…",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an ellipsis in the transcript was read as a turn: %q, want %q", got, agentdriver.StateFreeText)
	}
}
```

- [ ] **Step 7: Run green**

Run: `go test ./internal/agentdriver/ ./internal/agentcalib/ ./internal/transport/ -count=1`
Expected: `ok` for all three (the explain and emitting tests keep branch 0 and the last branch).

- [ ] **Step 8: Commit**

```bash
git add internal/agentdriver/claude.rule.json internal/agentdriver/claude_test.go internal/agentdriver/capture_test.go internal/agentdriver/testdata/captures/claude-lmstudio-turn.jsonl internal/agentdriver/testdata/captures/scripts/lmstudio-turn.script internal/agentdriver/testdata/captures/README.md internal/agentdriver/testdata/captures/manifest.json internal/transport/ws_paneobserve_states_test.go
git commit -m "fix(agentdriver): a turn reads working before Claude draws its elapsed timer (nocx-ys9jd)" -m "On Claude Code 2.1.266 a turn draws its spinner row as a verb and an ellipsis for its first seconds and adds the elapsed timer later. The working branch required the timer, so those seconds fell through to the prompt box and read as free_text: the state the typing gate types into and a finished turn would be read from. A second working branch matches a status-stack row ending in an ellipsis, bounded by the same region as the timer branch, so text the agent printed into its indented transcript cannot reach it. The discovery capture joins the corpus, and the reading is proved over the real socket through the watcher, the seam a person actually sees." -m "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: An API wait reads error (`nocx-emors`)

**Files:**

- Modify: `internal/transport/ws_paneobserve_states_test.go` (one chrome builder, one step)
- Modify: `internal/agentdriver/claude.rule.json` (one branch)
- Modify: `internal/agentdriver/claude_test.go` (one near-miss test)
- Modify: `internal/agentdriver/testdata/captures/manifest.json`

**Interfaces:**

- Consumes: Task 2's `claudeTurnStartingChrome`, `TestAnObservedPaneReportsEachStateItsScreenShows`,
  `claude-lmstudio-turn.jsonl`.
- Produces: `claudeAPIWaitingChrome(cols int) string`.

**Acceptance Criteria:**

- Over the real socket the pane reports `working`, then `error` for `Waiting for API response · will retry in …`,
  then `free_text` — red at the second step on the current rule.
- The manifest's `api-waiting` entry (`claude-lmstudio-turn@72000`) classifies `error`; the `claude-error`
  entries still classify `error`.
- The same words printed into the indented transcript with the box live stay `free_text`.

- [ ] **Step 1: Extend the seam test**

Add to `ws_paneobserve_states_test.go`:

```go
// claudeAPIWaitingChrome is the status row Claude Code 2.1.266 draws while a
// request to its API has gone unanswered (nocx-emors).
func claudeAPIWaitingChrome(cols int) string {
	return claudeIdleChrome(cols) +
		"\x1b[6;1H✻ Waiting for API response · will retry in 4m 36s · check your network" +
		"\x1b[12;1H  ⏸ manual mode on · esc to interrupt" +
		"\x1b[9;3H"
}
```

and in `steps` insert between the two existing steps:

```go
		{"the API waiting for a response", claudeAPIWaitingChrome(80), agentdriver.StateError},
```

- [ ] **Step 2: Point the manifest at the recorded moment**

Replace the `api-waiting` unverified entry with:

```json
    { "moment": "api-waiting", "capture": "claude-lmstudio-turn", "atMs": 72000, "state": "error", "note": "Waiting for API response · will retry in; a second chrome of an unreachable API (nocx-emors)" },
```

- [ ] **Step 3: Run both red**

Run: `go test ./internal/transport/ -run TestAnObservedPaneReportsEachStateItsScreenShows -count=1`
Expected: FAIL — `the API waiting for a response: observed "free_text", want "error"`.
Run: `go test ./internal/agentdriver/ -run TestTheManifestHolds -count=1`
Expected: FAIL — `claude-lmstudio-turn@72000ms: state "free_text", want "error"`.

- [ ] **Step 4: Add the branch to `claude.rule.json`**

Insert immediately after the existing branch whose `text` is `"· Retrying in"`, before the working branches:

```json
    {
      "state": "error",
      "when": [
        {
          "kind": "regionAny",
          "anchor": "meter",
          "up": true,
          "maxRows": 4,
          "col0Only": true,
          "stopAtBlank": true,
          "text": "Waiting for API response"
        }
      ]
    },
```

- [ ] **Step 5: Add the near-miss test to `claude_test.go`**

```go
// The same words the agent printed into its transcript are content, not the
// TUI's own error: indented, above a blank row, with the box live (nocx-emors).
func TestAnAPIWaitPrintedIntoTheTranscriptIsIdleNotError(t *testing.T) {
	rule := strings.Repeat("─", 60)
	lines := []string{
		"",
		"  Waiting for API response · will retry in 4s",
		"",
		"                                                   0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
		"",
	}
	f := screen(t, 60, 10, lines, 2, 5)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("an API wait in the transcript was read as chrome: %q, want %q", got, agentdriver.StateFreeText)
	}
}
```

- [ ] **Step 6: Run green**

Run: `go test ./internal/agentdriver/ ./internal/agentcalib/ ./internal/transport/ -count=1`
Expected: `ok` for all three.

- [ ] **Step 7: Commit**

```bash
git add internal/agentdriver/claude.rule.json internal/agentdriver/claude_test.go internal/agentdriver/testdata/captures/manifest.json internal/transport/ws_paneobserve_states_test.go
git commit -m "fix(agentdriver): Claude waiting on its API reads error, not ready for input (nocx-emors)" -m "Claude Code 2.1.266 draws \"Waiting for API response · will retry in <duration>\" in the status row when a request goes unanswered. Only the \"Retrying in\" chrome of the same condition was recognised, so this one fell through to the prompt box and reported an agent that cannot reach its API as ready for input, the defect claude-error was captured to close, in a second shape. An error branch reads the phrase from the same bounded status stack, ahead of the working branches, and the reading is proved over the real socket between the working and finished steps of a turn." -m "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: The `/btw` overlay over a running turn reads working (`nocx-nru89.4`)

**Files:**

- Modify: `internal/agentdriver/claude.rule.json` (one anchor, appended last; one branch before the `anchorUnbound` branch)
- Modify: `internal/agentdriver/claude_test.go` (two near-miss tests)
- Modify: `internal/agentdriver/testdata/captures/manifest.json`

**Interfaces:**

- Consumes: Task 1's manifest, Task 2's `claude-lmstudio-turn.jsonl`, `screen(t, cols, rows, lines, cursorX, cursorY)`.
- Produces: anchor `overlayTop` in the rule document.

**Acceptance Criteria:**

- `claude-lmstudio-turn@55000` (`btw-overlay`) and `@58000` classify `working` — red on the current rule.
- An idle box with a forged full-width `▔` row, `Esc to close` and a spinner line printed into the
  transcript stays `free_text`.
- A `/btw` overlay with no running turn above it is not `working`.
- Branch 0 is still the first permission branch and the first anchor is still `bottomRule`.

- [ ] **Step 1: Point the manifest at the recorded moment**

Replace the `btw-overlay` unverified entry with:

```json
    { "moment": "btw-overlay", "capture": "claude-lmstudio-turn", "atMs": 55000, "state": "working", "note": "the overlay replaces the box; the main turn's spinner is drawn above its full-width ▔ row (nocx-nru89.4)" },
    { "capture": "claude-lmstudio-turn", "atMs": 58000, "state": "working" },
```

- [ ] **Step 2: Write the near-miss tests in `claude_test.go`**

```go
// The overlay branch requires the input box to be GONE (nocx-nru89.4). An idle
// box whose agent printed a full-width ▔ row, "Esc to close" and a spinner
// line into its transcript is still an idle box.
func TestAForgedOverlayAboveALiveBoxIsNotWorking(t *testing.T) {
	rule := strings.Repeat("─", 40)
	lines := []string{
		"✻ Burrowing…",
		strings.Repeat("▔", 40),
		"    Esc to close",
		"",
		"                           0 tokens",
		rule,
		"❯ ",
		rule,
		"",
		"  ⏵⏵ auto mode on",
	}
	f := screen(t, 40, 10, lines, 2, 6)
	if got := agentdriver.Claude().Classify(f); got != agentdriver.StateFreeText {
		t.Fatalf("a forged overlay above a live box = %q, want %q", got, agentdriver.StateFreeText)
	}
}

// And an overlay with no running turn above it is not working: the overlay
// alone says a side question is open, not that the agent is busy.
func TestAnOverlayWithNoTurnAboveItIsNotWorking(t *testing.T) {
	lines := []string{
		"",
		"",
		strings.Repeat("▔", 40),
		"",
		"    /btw what is 2+2?",
		"",
		"    Esc to close",
		"",
		"",
		"",
	}
	f := screen(t, 40, 10, lines, 0, 9)
	if got := agentdriver.Claude().Classify(f); got == agentdriver.StateWorking {
		t.Fatalf("an overlay with no running turn above it = %q", got)
	}
}
```

- [ ] **Step 3: Run red**

Run: `go test ./internal/agentdriver/ -run 'TestTheManifestHolds|Overlay' -count=1`
Expected: FAIL — `claude-lmstudio-turn@55000ms: state "unknown", want "working"` and `@58000ms`; the two
near-miss tests pass already (they guard the fix).

- [ ] **Step 4: Add the anchor (last in `anchors`)**

```json
{
  "name": "overlayTop",
  "kind": "searchUp",
  "ruleGlyph": "▔",
  "floor": 0,
  "minRow": 1
}
```

- [ ] **Step 5: Add the branch before the `anchorUnbound` branch (after the three choice branches)**

```json
    {
      "state": "working",
      "when": [
        {
          "kind": "anchorUnbound",
          "anchor": "meter"
        },
        {
          "kind": "regionAny",
          "anchor": "overlayTop",
          "maxRows": 8,
          "text": "Esc to close"
        },
        {
          "kind": "regionAny",
          "anchor": "overlayTop",
          "up": true,
          "maxRows": 2,
          "col0Only": true,
          "stopAtBlank": true,
          "text": "…"
        }
      ]
    },
```

The spinner predicate uses `text` rather than `suffix` so that the main turn's row matches with or
without its elapsed timer (`✻ Burrowing…` and `✻ Burrowing… (16s)`).

- [ ] **Step 6: Run green**

Run: `go test ./internal/agentdriver/ ./internal/agentcalib/ ./internal/transport/ -count=1`
Expected: `ok` for all three.

- [ ] **Step 7: Commit**

```bash
git add internal/agentdriver/claude.rule.json internal/agentdriver/claude_test.go internal/agentdriver/testdata/captures/manifest.json
git commit -m "fix(agentdriver): the /btw overlay over a running turn reads working (nocx-nru89.4)" -m "While a /btw side question is open, Claude Code replaces its input box with an overlay under a full-width row of ▔, so none of the box anchors bound and the rule answered unknown although the main turn was still running above the overlay. An overlayTop anchor and a working branch read that shape, and the branch requires the box to be gone, the overlay's Esc to close beneath the ▔ row and the main turn's spinner directly above it, so an idle box whose agent printed the same characters stays free_text and an overlay with no turn above it is not working." -m "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: `agent-capture` runs a program with exactly the environment it is given (`nocx-nru89.1`)

**Files:**

- Create: `cmd/agent-capture/isolation.go`
- Modify: `cmd/agent-capture/main.go` (flags `-env-file`, `-dir`, `-meta`; `captureOptions`; `pinnedEnvironment` takes its base)
- Modify: `cmd/agent-capture/main_test.go:109` (the one existing `captureProgram` call)
- Create: `cmd/agent-capture/isolation_test.go`

**Interfaces:**

- Produces: `type captureOptions struct { OutPath string; Argv []string; Cols, Rows int; Timeout time.Duration; Steps []scriptStep; ScriptProvided bool; Env []string; Dir string; MetaPath string }`,
  `captureProgram(opts captureOptions, stderr io.Writer) error`, `readEnvFile(path string) ([]string, error)`,
  `refuseOutsideClaudeConfig(dir string) error`, `resolveProgram(name string, env []string) (string, error)`,
  `writeRunMeta(path, program, resolved string, env []string) error`, package var `managedClaudeDirs []string`.

**Acceptance Criteria:**

- Under `-env-file` a program prints the file's variables and the five pinned terminal variables and none of
  the caller's `CLAUDE*`; the metadata file records the resolved program, its sha256 and the variable names.
- Each refusal source — `managed-settings.json`, `managed-settings.d/`, managed `CLAUDE.md`, managed
  `.claude/rules/`, and `CLAUDE.md`, `CLAUDE.local.md` or `.claude/` in the run directory or an ancestor —
  starts no process and writes no capture.
- An unreadable `-env-file`, a malformed line, an uninspectable ancestor and an unwritable metadata file each
  stop the run with a named cause and no process started.
- Without `-env-file` capture behaves as before (the existing tests pass unchanged apart from the call shape).

- [ ] **Step 1: Write the failing tests (`cmd/agent-capture/isolation_test.go`)**

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentcapture"
)

// setManagedDirs points the managed-configuration check at a directory the test
// owns, and puts the real list back afterwards.
func setManagedDirs(t *testing.T, dirs ...string) {
	t.Helper()
	saved := managedClaudeDirs
	managedClaudeDirs = dirs
	t.Cleanup(func() { managedClaudeDirs = saved })
}

func shellPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	return sh
}

func writeEnvFile(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "run.env")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestAnEnvFileIsTheWholeEnvironment(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "leaked-from-the-caller")
	root := t.TempDir()
	env, err := readEnvFile(writeEnvFile(t, root, "# isolated", "ONLY_THIS=1", "PATH="+filepath.Dir(shellPath(t))+":/usr/bin:/bin"))
	if err != nil {
		t.Fatalf("readEnvFile: %v", err)
	}
	out := filepath.Join(root, "capture.jsonl")
	meta := filepath.Join(root, "meta.json")
	var stderr bytes.Buffer
	err = captureProgram(captureOptions{
		OutPath: out, Argv: []string{"sh", "-c", "env"}, Cols: 80, Rows: 24,
		Timeout: 5 * time.Second, Env: env, Dir: root, MetaPath: meta,
	}, &stderr)
	if err != nil {
		t.Fatalf("captureProgram: %v (stderr %q)", err, stderr.String())
	}
	_, chunks, err := agentcapture.Read(out)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var printed strings.Builder
	for _, c := range chunks {
		printed.WriteString(c.Data)
	}
	for _, want := range []string{"ONLY_THIS=1", "TERM=xterm-256color", "COLUMNS=80"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("environment lacks %q:\n%s", want, printed.String())
		}
	}
	if strings.Contains(printed.String(), "CLAUDE_CODE_SESSION_ID") {
		t.Errorf("the caller's session reached the program:\n%s", printed.String())
	}
	raw, err := os.ReadFile(meta) //nolint:gosec // a path this test made
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m struct {
		Program  string   `json:"program"`
		Resolved string   `json:"resolved"`
		SHA256   string   `json:"sha256"`
		EnvNames []string `json:"envNames"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if m.Program != "sh" || m.Resolved == "" || len(m.SHA256) != 64 {
		t.Errorf("meta = %+v, want sh resolved with a sha256", m)
	}
	if strings.Contains(string(raw), "=1") {
		t.Errorf("meta carries a variable's value: %s", raw)
	}
}

func TestIsolationRefusesClaudeConfigurationOutsideTheRun(t *testing.T) {
	cases := []struct {
		name  string
		place func(managed, parent, run string) string
	}{
		{"managed settings file", func(m, _, _ string) string { return filepath.Join(m, "managed-settings.json") }},
		{"managed settings directory", func(m, _, _ string) string { return filepath.Join(m, "managed-settings.d") }},
		{"managed instructions", func(m, _, _ string) string { return filepath.Join(m, "CLAUDE.md") }},
		{"managed rules", func(m, _, _ string) string { return filepath.Join(m, ".claude", "rules") }},
		{"CLAUDE.md in the run directory", func(_, _, r string) string { return filepath.Join(r, "CLAUDE.md") }},
		{"CLAUDE.local.md in an ancestor", func(_, p, _ string) string { return filepath.Join(p, "CLAUDE.local.md") }},
		{".claude in an ancestor", func(_, p, _ string) string { return filepath.Join(p, ".claude") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			managed := t.TempDir()
			setManagedDirs(t, managed)
			parent := t.TempDir()
			run := filepath.Join(parent, "run")
			if err := os.MkdirAll(run, 0o700); err != nil {
				t.Fatal(err)
			}
			source := tc.place(managed, parent, run)
			if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(parent, "started")
			out := filepath.Join(parent, "capture.jsonl")
			err := captureProgram(captureOptions{
				OutPath: out, Argv: []string{shellPath(t), "-c", "touch " + marker}, Cols: 80, Rows: 24,
				Timeout: 5 * time.Second, Env: []string{"PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin"}, Dir: run,
				MetaPath: filepath.Join(parent, "meta.json"),
			}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "refusing to start") || !strings.Contains(err.Error(), source) {
				t.Fatalf("captureProgram error = %v, want a refusal naming %s", err, source)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("the program ran despite the refusal")
			}
			if _, statErr := os.Stat(out); statErr == nil {
				t.Fatal("a capture was written despite the refusal")
			}
		})
	}
}

func TestIsolationFailingCallsStartNothing(t *testing.T) {
	setManagedDirs(t, t.TempDir())
	t.Run("an unreadable env file", func(t *testing.T) {
		err := run([]string{"capture", "-out", filepath.Join(t.TempDir(), "c.jsonl"),
			"-env-file", filepath.Join(t.TempDir(), "absent.env"), "--", "sh"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "cannot read env file") {
			t.Fatalf("run error = %v, want an unreadable env file", err)
		}
	})
	t.Run("a malformed line", func(t *testing.T) {
		_, err := readEnvFile(writeEnvFile(t, t.TempDir(), "GOOD=1", "NOEQUALS"))
		if err == nil || !strings.Contains(err.Error(), "env file line 2") {
			t.Fatalf("readEnvFile error = %v, want line 2 named", err)
		}
	})
	t.Run("an ancestor that cannot be inspected", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root inspects every directory")
		}
		parent := t.TempDir()
		locked := filepath.Join(parent, "locked")
		run := filepath.Join(locked, "run")
		if err := os.MkdirAll(run, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		err := refuseOutsideClaudeConfig(run)
		if err == nil || !strings.Contains(err.Error(), "cannot inspect") {
			t.Fatalf("refuseOutsideClaudeConfig error = %v, want an uninspectable source", err)
		}
	})
	t.Run("an unwritable metadata file", func(t *testing.T) {
		root := t.TempDir()
		marker := filepath.Join(root, "started")
		err := captureProgram(captureOptions{
			OutPath: filepath.Join(root, "c.jsonl"), Argv: []string{shellPath(t), "-c", "touch " + marker},
			Cols: 80, Rows: 24, Timeout: 5 * time.Second, Env: []string{"PATH=" + filepath.Dir(shellPath(t)) + ":/usr/bin:/bin"}, Dir: root,
			MetaPath: filepath.Join(root, "no-such-dir", "meta.json"),
		}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "write run metadata") {
			t.Fatalf("captureProgram error = %v, want a metadata failure", err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("the program ran although its metadata could not be written")
		}
	})
}
```

- [ ] **Step 2: Run red**

Run: `go test ./cmd/agent-capture/ -count=1`
Expected: build FAIL — `undefined: managedClaudeDirs`, `undefined: readEnvFile`, `undefined: captureOptions`.

- [ ] **Step 3: Write `cmd/agent-capture/isolation.go`**

```go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// managedClaudeDirs are where Claude Code reads managed settings and managed
// instructions, outside any directory a run owns. A variable so a test can
// point the check at directories it made.
var managedClaudeDirs = []string{"/etc/claude-code", "/Library/Application Support/ClaudeCode"}

var (
	managedClaudeEntries = []string{"managed-settings.json", "managed-settings.d", "CLAUDE.md", filepath.Join(".claude", "rules")}
	localClaudeEntries   = []string{"CLAUDE.md", "CLAUDE.local.md", ".claude"}
)

// readEnvFile parses KEY=VALUE lines; blank lines and # comments are skipped.
func readEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator explicitly supplies the env file
	if err != nil {
		return nil, fmt.Errorf("cannot read env file: %w", err)
	}
	var env []string
	for n, line := range strings.Split(string(data), "\n") {
		n++
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || !validEnvKey(key) {
			return nil, fmt.Errorf("env file line %d: want KEY=VALUE with a key of letters, digits and underscores", n)
		}
		env = append(env, line)
	}
	return env, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// refuseOutsideClaudeConfig refuses a run in which Claude Code would read
// settings or instructions the run does not own: managed ones, or any
// CLAUDE.md, CLAUDE.local.md or .claude in the run directory or above it. A
// source that cannot be inspected refuses too — not knowing is not absence.
func refuseOutsideClaudeConfig(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("refusing to start: cannot resolve %q: %w", dir, err)
	}
	for _, managed := range managedClaudeDirs {
		for _, entry := range managedClaudeEntries {
			if err := refuseIfPresent(filepath.Join(managed, entry)); err != nil {
				return err
			}
		}
	}
	for d := abs; ; {
		for _, entry := range localClaudeEntries {
			if err := refuseIfPresent(filepath.Join(d, entry)); err != nil {
				return err
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil
		}
		d = parent
	}
}

func refuseIfPresent(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("refusing to start: Claude Code would read %s, which is outside this run", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("refusing to start: cannot inspect %s: %w", path, err)
	}
}

// resolveProgram finds name on the PATH the program will be given, not on the
// caller's: under -env-file the caller's PATH is not the program's.
func resolveProgram(name string, env []string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		return filepath.Abs(name)
	}
	var path string
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%q is not on the env file's PATH", name)
}

// writeRunMeta records what ran — the program, where it resolved, its digest —
// and the NAMES of the variables it was given, never their values. A launcher
// that adds variables of its own (the Nix claude wrapper does) is inspectable
// from the resolved path afterwards.
func writeRunMeta(path, program, resolved string, env []string) error {
	f, err := os.Open(resolved) //nolint:gosec // the program this run resolved
	if err != nil {
		return fmt.Errorf("write run metadata: open %s: %w", resolved, err)
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	_ = f.Close()
	if copyErr != nil {
		return fmt.Errorf("write run metadata: digest %s: %w", resolved, copyErr)
	}
	names := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		names = append(names, key)
	}
	sort.Strings(names)
	raw, err := json.MarshalIndent(map[string]any{
		"program": program, "resolved": resolved, "sha256": hex.EncodeToString(h.Sum(nil)), "envNames": names,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write run metadata: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Change `main.go` to carry the options**

Add the flags in `runCaptureCommand` after `timeout`:

```go
	envFile := fs.String("env-file", "", "replace the inherited environment with this file's KEY=VALUE lines, and refuse to start if Claude Code would read configuration outside the run")
	dir := fs.String("dir", "", "working directory for the program (default: the current directory)")
	metaPath := fs.String("meta", "", "with -env-file, write what ran and the variable names here")
```

After the script is parsed, replace the `return captureProgram(...)` line with:

```go
	opts := captureOptions{
		OutPath: *outPath, Argv: argv, Cols: *cols, Rows: *rows, Timeout: *timeout,
		Steps: steps, ScriptProvided: *scriptPath != "", Dir: *dir, MetaPath: *metaPath,
	}
	if *envFile != "" {
		env, err := readEnvFile(*envFile)
		if err != nil {
			return fmt.Errorf("capture: %w", err)
		}
		opts.Env = env
	}
	return captureProgram(opts, stderr)
```

Define beside `scriptStep`:

```go
// captureOptions is one capture. Env nil inherits the caller's environment, as
// capture always has; a non-nil Env replaces it and turns on the Claude
// configuration refusals.
type captureOptions struct {
	OutPath        string
	Argv           []string
	Cols, Rows     int
	Timeout        time.Duration
	Steps          []scriptStep
	ScriptProvided bool
	Env            []string
	Dir            string
	MetaPath       string
}
```

Change `captureProgram`'s signature to `func captureProgram(opts captureOptions, stderr io.Writer) error`, and
replace its first lines (`cmd := exec.Command(argv[0], argv[1:]...)` and `cmd.Env = pinnedEnvironment(cols, rows)`) with:

```go
	outPath, argv, cols, rows, timeout, steps, scriptProvided := opts.OutPath, opts.Argv, opts.Cols, opts.Rows, opts.Timeout, opts.Steps, opts.ScriptProvided
	base := os.Environ()
	program := argv[0]
	if opts.Env != nil {
		base = opts.Env
		root := opts.Dir
		if root == "" {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("refusing to start: cannot resolve the working directory: %w", err)
			}
			root = wd
		}
		if err := refuseOutsideClaudeConfig(root); err != nil {
			return err
		}
		resolved, err := resolveProgram(argv[0], opts.Env)
		if err != nil {
			return fmt.Errorf("refusing to start: %w", err)
		}
		if opts.MetaPath != "" {
			if err := writeRunMeta(opts.MetaPath, argv[0], resolved, opts.Env); err != nil {
				return err
			}
		}
		program = resolved
	}
	//nolint:gosec // the operator explicitly supplies the program and arguments
	cmd := exec.Command(program, argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = pinnedEnvironment(base, cols, rows)
```

Change `pinnedEnvironment` to take its base:

```go
func pinnedEnvironment(base []string, cols, rows int) []string {
	keys := map[string]struct{}{
		"TERM": {}, "LANG": {}, "LC_ALL": {}, "COLUMNS": {}, "LINES": {},
	}
	env := make([]string, 0, len(base)+5)
	for _, entry := range base {
```

(the rest of the function unchanged). In `main_test.go` change the call to
`captureProgram(captureOptions{OutPath: outPath, Argv: []string{"bash", "-i"}, Cols: 80, Rows: 24, Timeout: 500 * time.Millisecond, Steps: steps, ScriptProvided: true}, &stderr)`.
Add `-env-file`, `-dir` and `-meta` to the usage line in `fs.Usage`.

- [ ] **Step 5: Run green**

Run: `go test ./cmd/agent-capture/ -count=1`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent-capture/isolation.go cmd/agent-capture/isolation_test.go cmd/agent-capture/main.go cmd/agent-capture/main_test.go
git commit -m "feat(agent-capture): a capture runs with exactly the environment it is given, and refuses Claude configuration outside the run (nocx-nru89.1)" -m "A capture passed its caller's whole environment except five terminal variables, so a capture started inside a Claude Code session handed the captured Claude that session's CLAUDECODE, session id and messaging token. -env-file replaces inheritance with the file, resolves the program on that file's PATH, and turns on refusals for every place Claude Code reads configuration outside the run: managed settings and instructions, and CLAUDE.md, CLAUDE.local.md or .claude in the run directory or above it. A source that cannot be inspected refuses too, because not knowing is not absence. The run metadata records the program, its digest and the variable names, never their values." -m "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: Record the moments on the current Claude, and the skill that redoes it (`nocx-nru89.2`)

This task is a live recording. It is not TDD code; its evidence is the manifest test going green on
captures recorded today, with every inventory moment recorded or explicitly unverified.

**Files:**

- Create: `.claude/skills/nocx-detection-verify/SKILL.md`
- Create: `.claude/skills/nocx-detection-verify/record.sh`
- Create: `internal/agentdriver/testdata/captures/scripts/lmstudio-onboarding.script`
- Create: `internal/agentdriver/testdata/captures/scripts/lmstudio-permission.script`
- Create: `internal/agentdriver/testdata/captures/scripts/lmstudio-subagent.script`
- Create: `internal/agentdriver/testdata/captures/scripts/lmstudio-api.script`
- Create: `internal/agentdriver/testdata/captures/claude-lmstudio-*.jsonl` (the recordings)
- Modify: `internal/agentdriver/testdata/captures/manifest.json`, `README.md`, `internal/agentdriver/capture_test.go` (`captureNames`)

**Interfaces:**

- Consumes: Task 5's `-env-file`, `-dir`, `-meta`; Task 1's manifest and `TestTheManifestHolds`.

**Acceptance Criteria:**

- Every inventory moment of the manifest has exactly one entry; recorded entries come from captures made
  with Claude Code current on the day (recorded in `README.md`); each unverified entry states why and the
  owner's acceptance is recorded in the bead's close reason.
- `go test ./internal/agentdriver/ -run TestTheManifestHolds` passes.
- Following `SKILL.md` from an empty run directory reproduces the onboarding recording.

- [ ] **Step 1: Write `record.sh`**

```bash
#!/usr/bin/env bash
# Record one Claude Code moment for internal/agentdriver's corpus, isolated.
# usage: record.sh <endpoint> <model> <script> <out.jsonl> [cols] [settings.json]
set -euo pipefail
endpoint=$1 model=$2 script=$3 out=$4 cols=${5:-120} settings=${6:-}
repo=$(git rev-parse --show-toplevel)
run=$(mktemp -d /var/tmp/nocx-detect-XXXXXX)
mkdir -p "$run/home" "$run/claude-config" "$run/work"
claude_dir=$(dirname "$(command -v claude)")
cat > "$run/run.env" <<EOF
HOME=$run/home
CLAUDE_CONFIG_DIR=$run/claude-config
PATH=$claude_dir:/run/current-system/sw/bin:/usr/bin:/bin
ANTHROPIC_BASE_URL=$endpoint
ANTHROPIC_AUTH_TOKEN=local-model-no-credential
ANTHROPIC_MODEL=$model
ANTHROPIC_SMALL_FAST_MODEL=$model
DISABLE_TELEMETRY=1
EOF
args=()
if [[ -n $settings ]]; then cp "$settings" "$run/settings.json"; args=(--settings "$run/settings.json"); fi
go build -o "$run/agent-capture" "$repo/cmd/agent-capture"
"$run/agent-capture" capture -env-file "$run/run.env" -dir "$run/work" -meta "$run/meta.json" \
  -out "$out" -script "$script" -cols "$cols" -rows 40 -timeout 240s -- claude "${args[@]}"
claude --version > "$run/claude-version.txt"
echo "run directory: $run (meta.json, claude-version.txt)"
```

- [ ] **Step 2: Write the scripts**

`lmstudio-onboarding.script` (theme ~13500, security ~19500, trust ~26500, idle ~38000):

```
14000 \r
6000 \r
7000 \x1b[B
1500 \r
12000
```

`lmstudio-permission.script` (run with `settings` = `{"permissions":{"ask":["Bash(touch marker.txt)"]}}`;
the dialog is left with Esc once recorded; the second prompt asks for a Write):

```
14000 \r
6000 \r
7000 \x1b[B
1500 \r
9000 Run exactly this shell command and nothing else: touch marker.txt
800 \r
60000 \x1b
6000 Create a file named note.txt containing exactly the word hi
800 \r
60000 \x1b
5000
```

`lmstudio-subagent.script`:

```
14000 \r
6000 \r
7000 \x1b[B
1500 \r
9000 Use the Agent tool to launch the Explore subagent with run_in_background true, asking it to list the files in this folder. Do not wait for it.
800 \r
120000
```

`lmstudio-api.script` (run once with the endpoint `http://127.0.0.1:9` for refused-retrying, once against a
listener that accepts and never answers for waiting — see SKILL.md):

```
14000 \r
6000 \r
7000 \x1b[B
1500 \r
9000 hi
800 \r
40000
```

- [ ] **Step 3: Write `SKILL.md`**

```markdown
---
name: nocx-detection-verify
description: Re-record Claude Code screen moments for internal/agentdriver's manifest after a Claude update, isolated from the person's Claude account and configuration, and check the shipped rule against them.
---

# Re-recording Claude's screen moments

Use after `claude --version` changes, or when a pane's reported state looks wrong.

1. **Endpoint.** An Anthropic-compatible local model (the owner's LM Studio; see bead `nocx-34r0i`
   notes 13). Check it: `curl -s <endpoint>/v1/models` lists the model, and a `/v1/messages` call with one
   tool definition answers `stop_reason: tool_use`. Never use an Anthropic account for recording.
2. **Record** with `.claude/skills/nocx-detection-verify/record.sh <endpoint> <model> <script> <out> [cols] [settings]`.
   It builds `agent-capture`, runs `claude` under `-env-file` in a fresh `/var/tmp/nocx-detect-*` directory
   and refuses if Claude would read any configuration outside it. Scripts live in
   `internal/agentdriver/testdata/captures/scripts/lmstudio-*.script`.
   - API refused: endpoint `http://127.0.0.1:9`.
   - API waiting: start `python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",18999));s.listen();c=[s.accept() for _ in range(64)]'`
     in another pane and use endpoint `http://127.0.0.1:18999`.
3. **Place marks by reading the replay:** `go run ./cmd/agent-capture replay -at <ms,...> <capture>` around each
   script step, until the moment's identifying text (spec §6.4) is on screen. Never place a mark by timing
   alone.
4. **Update `internal/agentdriver/testdata/captures/manifest.json`:** one entry per inventory moment —
   recorded (capture, `atMs`, the owner's state) or `unverified` with the reason. Add the capture name to
   `captureNames` in `capture_test.go` and a row plus the Claude version to the captures `README.md`.
5. **Check:** `go test ./internal/agentdriver/ -run TestTheManifestHolds -count=1`. A disagreement goes to the
   owner, who decides whether the rule, the mark or the label is wrong; a rule change comes with a test that
   is red first.
6. **Never** approve a tool call, work outside the run directory, or commit a capture from a directory that
   was not isolated.
```

- [ ] **Step 4: Record every moment**

For each moment in the manifest inventory, run `record.sh` with the matching script (onboarding at 120, 80 and
60 columns; permission; subagent; API refused; API waiting; `/model` and `ctrl+o` are recorded by appending
`5000 /model` / `5000 \x0f` steps to a copy of the onboarding script). Place marks per SKILL.md step 3.

- [ ] **Step 5: Update the manifest, README and `captureNames`**

Replace each `unverified` entry that now has a recording, and replace the older-version recorded entries for
the same inventory moments with the new recordings (keep the older captures' marks as entries without a
`moment`, as regressions). Every remaining `unverified` entry carries the reason the moment could not be reached.

- [ ] **Step 6: Run the manifest**

Run: `go test ./internal/agentdriver/ ./internal/agentcalib/ ./internal/transport/ -count=1`
Expected: `ok`. A failing entry is a disagreement: stop and bring it to the owner (SKILL.md step 5).

- [ ] **Step 7: Commit**

```bash
chmod +x .claude/skills/nocx-detection-verify/record.sh
git add .claude/skills/nocx-detection-verify internal/agentdriver/testdata/captures internal/agentdriver/capture_test.go
git commit -m "test(agentdriver): record Claude's screen moments on the current version, isolated, with the skill that redoes it (nocx-nru89.2)" -m "The corpus was recorded on Claude Code 2.1.238 to 2.1.263 and the rule is now verified against the version installed today, recorded against a local Anthropic-compatible model through an isolated agent-capture run so no recording touches the owner's Claude account, credentials, hooks or permissions. Every moment of the manifest inventory is recorded or named as unverified with the reason. The skill records the procedure so the next Claude update is re-verified the same way." -m "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```
