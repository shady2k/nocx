# A skill ages — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use `beads-superpowers:subagent-driven-development` (recommended) or `beads-superpowers:executing-plans` to implement this plan task-by-task. Each Task becomes a bead (`br create -t task --parent nocx-dzy7l`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.
>
> **The tracker is `br`, not `bd`.** See AGENTS.md — it wins over any skill text. `br create` / `br update --claim` / `br close --reason` / `br dep add`. There is no `br import` and no `br batch`; create children one at a time.

**Goal:** A person can see how long each skill has gone unused, nocx switches off the ones that have gone quiet — recording that it, and not the person, did so — and two pins say "never switch this off" and "never let the machine change this".

**Architecture:** Telemetry is a new `usage` map in `skills.json` beside `digests` and `sources`, bumped on a successful `skills.read` and flushed with the document's other writes. Auto-off is evaluated inside the existing discovery walk — there is no scheduler — and recorded as its own fact rather than as a name in `disabled`, so that list goes on meaning only "the person turned this off". Two independent per-skill flags cancel auto-off and refuse machine writes.

**Tech Stack:** Go (`internal/skill`, `internal/assistant`, `internal/agenttools`, `internal/transport`, `internal/settings`), JSON Schema contracts with generated TypeScript, SolidJS frontend, Playwright for the epic's happy path.

**Spec:** [`.internal/specs/2026-09-07-a-skill-ages-design.md`](../specs/2026-09-07-a-skill-ages-design.md)

## Global Constraints

- **Every commit names its bead** in the subject, `<type>(<scope>): <subject> (<bead-id>)`, body as prose explaining why this way rather than the obvious alternative. AGENTS.md.
- **TDD**: the failing test comes first, and every task ends with a commit.
- **A worker runs the unit tests for what it touched and nothing more.** No `make ci-full`, no containerized jobs, no e2e suite — those belong to whoever integrates. Task 9 is the exception: it writes an e2e and must run it.
- **The wire is a party to the contract.** Any JSON-RPC result shape that changes gets its `contracts/*.schema.json` updated in the same commit, the renderer's type regenerated (`cd frontend && npm run contracts`), and an over-the-wire test — never a DTO test alone.
- **A soft degrade must be visible in the product, not only in a log.**
- **Counters are best-effort**: a telemetry write that fails logs at debug and never fails the call that triggered it.
- **Never `git add -A`.** Stage named paths.
- Run `gofumpt -w` on touched Go files; `cd frontend && npx prettier --write` on touched TS/TSX/JSON.

## File map

| File                                                 | Responsibility                                                                                                                            |
| ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/skill/usage.go` _(new)_                    | The `Usage` record, the in-memory accumulator, `RecordUse`, `flushUsage`, and the idle predicate. One file because these change together. |
| `internal/skill/usage_test.go` _(new)_               | Its unit tests.                                                                                                                           |
| `internal/skill/store_doc.go`                        | `document` gains `Usage` and `Pins`; `skillsSchemaVersion` 4 → 5; a migration rung; `ListedSkill` gains the wire fields.                  |
| `internal/skill/discover.go`                         | The idle evaluation joins the walk; `firstSeenAt` is stamped there.                                                                       |
| `internal/skill/write.go`                            | `Store` gains the accumulator, the threshold seam and the clock seam; `Update`/`Delete` consult `keepUnchanged`.                          |
| `internal/assistant/execute.go`                      | `executeSkillsRead` records the use through the library seam.                                                                             |
| `internal/assistant/assistant.go`                    | `SkillLibrary` gains `RecordUse`.                                                                                                         |
| `internal/settings/settings.go`                      | `SkillsIdleDays`, modelled on `HistoryRetentionDays`.                                                                                     |
| `contracts/skills.list.schema.json`                  | `usage`, `autoOff` and the two pins per skill.                                                                                            |
| `contracts/skills.setPin.schema.json` _(new)_        | The pin-setting result.                                                                                                                   |
| `contracts/skills.setPin.params.schema.json` _(new)_ | Its params.                                                                                                                               |
| `internal/transport/ws_skill_handlers.go`            | The `skills.setPin` handler.                                                                                                              |
| `frontend/src/skills-section.tsx`                    | The age line and the auto-off status.                                                                                                     |
| `frontend/src/skill-view/`                           | The two pin switches.                                                                                                                     |
| `e2e/skill-ageing.spec.ts` _(new)_                   | The epic's happy path.                                                                                                                    |

---

### Task 1: The document carries usage, and an old one still opens

**Files:**

- Modify: `internal/skill/store_doc.go` (the `document` struct near line 93, `skillsSchemaVersion` at line 26, `Module.Migrations` near line 36)
- Create: `internal/skill/usage.go`
- Test: `internal/skill/usage_test.go`, `internal/skill/store_doc_test.go`

**Interfaces:**

- Produces: `skill.Usage{Count int, LastUsedAt string, FirstSeenAt string}` — RFC3339 strings, empty meaning "never". `document.Usage map[string]Usage` with JSON key `usage`, `omitempty`.

**Acceptance Criteria:**

- A version-4 `skills.json` on disk still loads, and reads as having no usage for any skill.
- A document written by this build carries `schemaVersion: 5`.
- A usage record survives a write/read round trip with its three fields intact.
- A version-6 document is refused rather than silently rewritten (this already holds via `storage.Module.Migrate`; the test pins it).

- [ ] **Step 1: Write the failing round-trip test**

In `internal/skill/store_doc_test.go`:

```go
func TestDocumentCarriesUsageAcrossAWrite(t *testing.T) {
	configDir := t.TempDir()
	store := NewStore(OSFileSystem{},
		[]Root{{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored}},
		storage.NewDocumentStore(configDir))

	if err := store.recordUsageForTest("deploy", Usage{
		Count: 3, LastUsedAt: "2026-03-03T10:00:00Z", FirstSeenAt: "2026-01-01T10:00:00Z",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	reopened := NewStore(OSFileSystem{},
		[]Root{{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored}},
		storage.NewDocumentStore(configDir))
	got, err := reopened.usageFor("deploy")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Count != 3 || got.LastUsedAt != "2026-03-03T10:00:00Z" || got.FirstSeenAt != "2026-01-01T10:00:00Z" {
		t.Fatalf("usage = %+v, want the three fields intact", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/skill/ -run TestDocumentCarriesUsageAcrossAWrite`
Expected: FAIL — `undefined: Usage`, `recordUsageForTest`, `usageFor`.

- [ ] **Step 3: Add the type and the document field**

In `internal/skill/usage.go`:

```go
package skill

// Usage is what nocx knows about how a skill has been reached for. It lives
// in skills.json beside digests and sources rather than in the skill's own
// frontmatter — the epic's sidecar rule, and here it is stronger than the
// reason the epic gives: our frontmatter is inside the digest Status uses to
// tell a changed skill from an approved one, so a counter written into it
// would mark a skill CHANGED every time it was used.
type Usage struct {
	// Count is successful skills.read calls. Inspection is not use: the
	// viewer and the audit read the same bytes and do not move this.
	Count int `json:"count"`
	// LastUsedAt is RFC3339, empty when the skill has never been read.
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	// FirstSeenAt is when DISCOVERY first saw the skill, not when it was
	// installed. A skill can be placed by hand, restored from a backup or
	// arrive with a profile, and for those there is no install date at all;
	// "when nocx first saw it" exists for every skill however it got there.
	FirstSeenAt string `json:"firstSeenAt,omitempty"`
}
```

In `internal/skill/store_doc.go`, inside `document`, after `Sources`:

```go
	// Usage is per-skill telemetry, keyed by name like Digests and Sources.
	// Optional: a document written before version 5 has none, and a skill
	// with no row is one nothing has been recorded about yet.
	Usage map[string]Usage `json:"usage,omitempty"`
```

- [ ] **Step 4: Bump the version and add the rung**

In `internal/skill/store_doc.go`:

```go
const skillsSchemaVersion storage.SchemaVersion = 5
```

and inside `Module.Migrations`, after the `3 → 4` rung:

```go
		{From: 4, To: 5, Up: restampTo(5)},
```

Extend `restampTo`'s doc comment with one sentence, in its existing voice:

```go
// version 5 is version 4 plus an optional `usage` map and an optional `pins`
// map, so a document written before either simply has neither
```

- [ ] **Step 5: Add the two accessors the test needs**

In `internal/skill/usage.go`:

```go
// usageFor answers what the document records about one skill. A skill with no
// row reads as a zero Usage rather than an error: nothing recorded and a count
// of zero are the same fact to every caller.
func (s *Store) usageFor(name string) (Usage, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return Usage{}, err
	}
	return d.Usage[name], nil
}

// recordUsageForTest writes one row straight through, without the accumulator.
// It exists so the document's shape can be tested apart from when the
// accumulator decides to flush.
func (s *Store) recordUsageForTest(name string, u Usage) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	if d.Usage == nil {
		d.Usage = make(map[string]Usage, 1)
	}
	d.Usage[name] = u
	return s.writeDocumentLocked(d)
}
```

> Read `loadDocumentLocked` and `writeDocumentLocked` before writing this — match their exact signatures, which the surrounding file already uses (`recordedSources`, `recordApprovalDigest`).

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/skill/ -run TestDocumentCarriesUsageAcrossAWrite -v`
Expected: PASS.

- [ ] **Step 7: Pin the old document**

Add to `internal/skill/store_doc_test.go`:

```go
func TestAVersionFourDocumentStillOpensAndHasNoUsage(t *testing.T) {
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, "skills"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Written by the build before this one: no `usage` key at all.
	old := `{"schemaVersion":4,"disabled":["deploy"],"digests":{"deploy":"abc"}}`
	if err := os.WriteFile(filepath.Join(configDir, DocumentName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(OSFileSystem{},
		[]Root{{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored}},
		storage.NewDocumentStore(configDir))

	got, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("a version 4 document did not open: %v", err)
	}
	if got.Count != 0 || got.LastUsedAt != "" || got.FirstSeenAt != "" {
		t.Fatalf("usage = %+v, want the zero record: nothing was ever recorded", got)
	}
}
```

> Check `DocumentName`'s directory against `Store.DocumentPath()` before assuming the file sits directly in `configDir` — fix the path in the test to whatever that method returns.

- [ ] **Step 8: Run the whole package**

Run: `go test ./internal/skill/ -count=1`
Expected: ok.

- [ ] **Step 9: Commit**

```bash
gofumpt -w internal/skill/usage.go internal/skill/store_doc.go internal/skill/usage_test.go internal/skill/store_doc_test.go
git add internal/skill/usage.go internal/skill/store_doc.go internal/skill/store_doc_test.go
git commit -m "feat(skill): the document carries per-skill usage (nocx-dzy7l)"
```

---

### Task 2: A skills.read is recorded, and a failed recording never fails the read

**Files:**

- Modify: `internal/skill/usage.go`, `internal/skill/write.go` (the `Store` struct near line 124)
- Modify: `internal/assistant/assistant.go` (`SkillLibrary`, line 210), `internal/assistant/execute.go` (`executeSkillsRead`, line 534)
- Test: `internal/skill/usage_test.go`, `internal/assistant/skills_read_usage_test.go` _(new)_

**Interfaces:**

- Consumes: `skill.Usage` from Task 1.
- Produces: `func (s *Store) RecordUse(name string)` — no error return, deliberately: the caller must not be able to fail on it. `SkillLibrary` gains `RecordUse(name string)`.

**Acceptance Criteria:**

- A successful `skills.read` increments the count and sets `lastUsedAt`.
- A failed `skills.read` records nothing.
- `skills.file`, `skills.files` and `skills.audit` record nothing.
- A document that cannot be written during a flush leaves the `skills.read` successful and its result intact.
- Counts accumulate in memory and reach disk without one write per call.

- [ ] **Step 1: Write the failing accumulator tests**

In `internal/skill/usage_test.go`:

```go
package skill

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

func usageStand(t *testing.T) *Store {
	t.Helper()
	configDir := t.TempDir()
	return NewStore(OSFileSystem{},
		[]Root{{Dir: filepath.Join(configDir, "skills"), Provenance: ProvenanceAuthored}},
		storage.NewDocumentStore(configDir),
		WithClock(func() time.Time { return time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC) }))
}

func TestRecordUseCountsAndDates(t *testing.T) {
	store := usageStand(t)
	store.RecordUse("deploy")
	store.RecordUse("deploy")
	if err := store.FlushUsage(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d, want 2", got.Count)
	}
	if got.LastUsedAt != "2026-03-03T10:00:00Z" {
		t.Fatalf("lastUsedAt = %q", got.LastUsedAt)
	}
}

func TestRecordUseDoesNotWritePerCall(t *testing.T) {
	store := usageStand(t)
	store.RecordUse("deploy")
	// Nothing is on disk until a flush: the accumulator is what keeps
	// skills.json off the assistant's hot path.
	got, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Count != 0 {
		t.Fatalf("count = %d before a flush, want 0", got.Count)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/skill/ -run 'TestRecordUse'`
Expected: FAIL — `undefined: WithClock`, `RecordUse`, `FlushUsage`.

- [ ] **Step 3: Add the clock seam and the accumulator to Store**

In `internal/skill/write.go`, inside the `Store` struct beside `previewMu`:

```go
	// now is the clock. It is a seam because every date this package writes
	// is compared against a threshold later, and a test that cannot move the
	// clock can only assert the comparison by sleeping.
	now func() time.Time

	// pendingUsage holds counts that have not reached the document yet.
	// skills.json is written whole under a mutex and has until now been
	// written only by a person's action; a write per skills.read would put
	// that on the assistant's hot path. A crash loses the last few bumps,
	// which for a threshold measured in days is not worth mechanism.
	usageMu      sync.Mutex
	pendingUsage map[string]pendingUse
```

and next to `WithFetcher`:

```go
// WithClock replaces the clock. Production passes nothing and gets time.Now.
func WithClock(now func() time.Time) StoreOption {
	return func(s *Store) { s.now = now }
}
```

In `newStore`, after the existing defaulting:

```go
	if s.now == nil {
		s.now = time.Now
	}
```

- [ ] **Step 4: Write RecordUse and FlushUsage**

In `internal/skill/usage.go`:

```go
type pendingUse struct {
	count int
	last  time.Time
}

// RecordUse notes that a skill was read. It RETURNS NOTHING, and that is the
// contract rather than an omission: the caller is executeSkillsRead, and a
// skill that cannot be used because its usage counter failed is worse than a
// counter that is wrong (the epic's rule, taken from hermes whole).
func (s *Store) RecordUse(name string) {
	if s == nil || name == "" {
		return
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.pendingUsage == nil {
		s.pendingUsage = make(map[string]pendingUse, 1)
	}
	held := s.pendingUsage[name]
	held.count++
	held.last = s.now().UTC()
	s.pendingUsage[name] = held
}

// FlushUsage folds what has accumulated into the document. It is called where
// the document is being written anyway — a discovery pass, a switch, the end
// of a run — so a flush costs no write of its own.
//
// A failure LEAVES THE PENDING COUNTS IN PLACE rather than dropping them: the
// next flush tries again, and the failure a person can act on is the one the
// document surfaces already.
func (s *Store) FlushUsage() error {
	if s == nil {
		return nil
	}
	s.usageMu.Lock()
	pending := s.pendingUsage
	s.pendingUsage = nil
	s.usageMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		s.restorePending(pending)
		return err
	}
	if d.Usage == nil {
		d.Usage = make(map[string]Usage, len(pending))
	}
	for name, held := range pending {
		row := d.Usage[name]
		row.Count += held.count
		row.LastUsedAt = held.last.Format(time.RFC3339)
		d.Usage[name] = row
	}
	if writeErr := s.writeDocumentLocked(d); writeErr != nil {
		s.restorePending(pending)
		return writeErr
	}
	return nil
}

func (s *Store) restorePending(pending map[string]pendingUse) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.pendingUsage == nil {
		s.pendingUsage = pending
		return
	}
	for name, held := range pending {
		merged := s.pendingUsage[name]
		merged.count += held.count
		if held.last.After(merged.last) {
			merged.last = held.last
		}
		s.pendingUsage[name] = merged
	}
}
```

- [ ] **Step 5: Run the two tests**

Run: `go test ./internal/skill/ -run 'TestRecordUse' -v`
Expected: PASS.

- [ ] **Step 6: Write the failing wiring test**

In `internal/assistant/skills_read_usage_test.go`:

```go
package assistant

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/skill"
)

// A recording fake stands in for the library so this test asserts the WIRING
// — that executeSkillsRead reaches RecordUse on success and not on failure —
// rather than re-testing internal/skill's accumulator.
type recordingSkillLibrary struct {
	SkillLibrary
	recorded []string
	readErr  error
}

func (r *recordingSkillLibrary) RecordUse(name string) { r.recorded = append(r.recorded, name) }

func (r *recordingSkillLibrary) Read(name, path string) (skill.Content, error) {
	if r.readErr != nil {
		return skill.Content{}, r.readErr
	}
	return skill.Content{Path: "SKILL.md", Bytes: []byte("---\nname: deploy\ndescription: d\n---\nbody\n")}, nil
}

func skillsReadKernel(t *testing.T, library SkillLibrary) *effectKernel {
	t.Helper()
	grant := autonomousMatrix().AsGrant([]content.GrantScope{{
		Kind: content.ResourceContent, ID: "skill/deploy",
	}})
	reg, err := agenttools.Assemble(os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	k, err := newEffectKernel(nil, grant, reg, &fakeLedger{}, NewApprovalStore(),
		&fakeKnownMaterial{}, "run-usage", "session-usage", 1, "", nil, Attachments{}, nil, nil,
		toolSeams{skills: library})
	if err != nil {
		t.Fatalf("newEffectKernel: %v", err)
	}
	return k
}

func TestSkillsReadRecordsAUseOnlyWhenItSucceeded(t *testing.T) {
	t.Run("a successful read is a use", func(t *testing.T) {
		library := &recordingSkillLibrary{}
		k := skillsReadKernel(t, library)
		if _, err := k.Invoke(context.Background(), "skills.read", "call-1", `{"name":"deploy"}`); err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if len(library.recorded) != 1 || library.recorded[0] != "deploy" {
			t.Fatalf("recorded = %v, want exactly one use of deploy", library.recorded)
		}
	})

	t.Run("a refused read is not a use", func(t *testing.T) {
		// The paired failure, and it is the half that matters: recording a
		// refused read would let a model keep a skill young by asking for
		// something it is not allowed to have.
		library := &recordingSkillLibrary{readErr: errors.New("no such skill")}
		k := skillsReadKernel(t, library)
		if _, err := k.Invoke(context.Background(), "skills.read", "call-2", `{"name":"deploy"}`); err == nil {
			t.Fatal("the read succeeded, so this subtest proves nothing")
		}
		if len(library.recorded) != 0 {
			t.Fatalf("recorded = %v after a failed read, want none", library.recorded)
		}
	})
}
```

> Two things to confirm against the tree rather than trusting this listing: `skill.Content`'s field names (read `internal/skill/write.go`'s `Read`), and whether `autonomousMatrix()`'s grant needs a `ResourceContent` scope of `skill` rather than `skill/deploy` for `skills.read` to be in scope. Both are two-line fixes; the two assertions are the test.

- [ ] **Step 7: Run it and watch it fail**

Run: `go test ./internal/assistant/ -run TestSkillsReadRecordsAUse`
Expected: FAIL.

- [ ] **Step 8: Add RecordUse to the seam and call it**

In `internal/assistant/assistant.go`, inside `SkillLibrary`:

```go
	// RecordUse notes that a skill was read, for the ageing the Skills page
	// shows and the auto-off it drives. It returns nothing on purpose: a
	// skill that cannot be used because its counter failed is worse than a
	// counter that is wrong.
	RecordUse(name string)
```

In `internal/assistant/execute.go`, in `executeSkillsRead`, immediately after the successful `seams.skills.Read`:

```go
	// AFTER the read succeeded, and only then. A refused read is not a use,
	// and recording one would let a model keep a skill young by asking for
	// something it is not allowed to have.
	seams.skills.RecordUse(p.Name)
```

- [ ] **Step 9: Run both packages**

Run: `go test ./internal/skill/ ./internal/assistant/ -count=1`
Expected: ok, ok. Fix any other implementer of `SkillLibrary` in tests that now fails to compile — add the one-line method.

- [ ] **Step 10: Commit**

```bash
gofumpt -w internal/skill internal/assistant
git add internal/skill/usage.go internal/skill/usage_test.go internal/skill/write.go internal/assistant/assistant.go internal/assistant/execute.go internal/assistant/skills_read_usage_test.go
git commit -m "feat(skill,assistant): a successful skills.read is counted, and a failed count never fails the read (nocx-dzy7l)"
```

---

### Task 3: Discovery stamps when it first saw a skill

**Files:**

- Modify: `internal/skill/discover.go` (`discoverAll`), `internal/skill/usage.go`
- Test: `internal/skill/usage_test.go`

**Interfaces:**

- Consumes: `Usage.FirstSeenAt`, `Store.now` from Tasks 1–2.
- Produces: `func (s *Store) stampFirstSeen(names []string) error`.

**Acceptance Criteria:**

- A skill discovered for the first time gets `firstSeenAt` set to the clock's now.
- A second discovery does NOT move it.
- A skill that has never been discovered has none.
- `Discover` (the assistant's index) does not stamp — only the Store's own list path does, because `Discover` is a free function with no document to write to.

- [ ] **Step 1: Write the failing test**

```go
func TestFirstSeenIsStampedOnceAndNeverMoves(t *testing.T) {
	store := usageStand(t)
	writeSkillAt(t, store, "deploy", "Deploy the service")

	if _, err := store.List(); err != nil {
		t.Fatalf("first list: %v", err)
	}
	first, err := store.usageFor("deploy")
	if err != nil || first.FirstSeenAt == "" {
		t.Fatalf("firstSeenAt = %q, %v; want it stamped by the first discovery", first.FirstSeenAt, err)
	}

	store.now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	if _, err := store.List(); err != nil {
		t.Fatalf("second list: %v", err)
	}
	second, err := store.usageFor("deploy")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if second.FirstSeenAt != first.FirstSeenAt {
		t.Fatalf("firstSeenAt moved from %q to %q: it is when nocx FIRST saw the skill",
			first.FirstSeenAt, second.FirstSeenAt)
	}
}
```

> `writeSkillAt` does not exist yet — write it as a three-line helper in `usage_test.go` that creates `<root>/<name>/SKILL.md` with valid frontmatter, the way `internal/skill/refused_test.go`'s `writeRefusedSkill` does.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/skill/ -run TestFirstSeenIsStamped`
Expected: FAIL — `firstSeenAt = ""`.

- [ ] **Step 3: Implement the stamp**

In `internal/skill/usage.go`:

```go
// stampFirstSeen records the moment discovery first saw each name, and never
// moves one already recorded. It is the OTHER end of the ageing measure: a
// skill installed yesterday and not yet needed must not be switched off for
// silence it has had no chance to break.
func (s *Store) stampFirstSeen(names []string) error {
	if s == nil || len(names) == 0 {
		return nil
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return err
	}
	changed := false
	for _, name := range names {
		row := d.Usage[name]
		if row.FirstSeenAt != "" {
			continue
		}
		row.FirstSeenAt = s.now().UTC().Format(time.RFC3339)
		if d.Usage == nil {
			d.Usage = make(map[string]Usage, len(names))
		}
		d.Usage[name] = row
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeDocumentLocked(d)
}
```

- [ ] **Step 4: Call it from List**

In `internal/skill/store_doc.go`'s `List()`, after `discoverAll` and before the rows are built:

```go
	// Flush what has accumulated and stamp anything new, in that order: a
	// flush that ran second would write a document the stamp had already
	// rebuilt. Both are best-effort — a list that failed because telemetry
	// could not be written would take the person's switches down with it.
	if flushErr := s.FlushUsage(); flushErr != nil {
		slog.Debug("skill: usage counters were not flushed", "error", flushErr)
	}
	names := make([]string, 0, len(detailed))
	for _, found := range detailed {
		names = append(names, found.Name)
	}
	if stampErr := s.stampFirstSeen(names); stampErr != nil {
		slog.Debug("skill: first-seen dates were not stamped", "error", stampErr)
	}
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/skill/ -run TestFirstSeenIsStamped -v`
Expected: PASS.

- [ ] **Step 6: Run the package and commit**

```bash
go test ./internal/skill/ -count=1
gofumpt -w internal/skill
git add internal/skill/usage.go internal/skill/usage_test.go internal/skill/store_doc.go
git commit -m "feat(skill): discovery stamps when it first saw a skill (nocx-dzy7l)"
```

---

### Task 4: The threshold is a setting a person can see and change

**Files:**

- Modify: `internal/settings/settings.go`, `internal/skill/write.go`
- Test: `internal/settings/settings_test.go`, `internal/skill/usage_test.go`

**Interfaces:**

- Produces: `settings.SkillsIdleDays` (a `NumberSpec`, default 90, `ZeroLabel` "Never switched off automatically"), and `skill.WithIdleDays(func() int) StoreOption`.

**Acceptance Criteria:**

- The setting is registered, defaults to 90, and 0 means never.
- `WithIdleDays` is read at evaluation time, not captured at construction, so changing the setting takes effect without a restart.
- A Store built without the option never switches anything off.

- [ ] **Step 1: Write the failing settings test**

```go
func TestSkillsIdleDaysIsRegisteredWithAZeroThatMeansNever(t *testing.T) {
	spec, ok := Lookup("skills.idleDays")
	if !ok {
		t.Fatal("skills.idleDays is not registered")
	}
	if spec.ZeroLabel == "" {
		t.Error("zero has no label: a person setting 0 must be told what it does, " +
			"the way history.retentionDays does")
	}
}
```

> Match `Lookup` to whatever the registry actually exposes — read the top of `internal/settings/settings.go` and copy the shape another spec's test uses.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/settings/ -run TestSkillsIdleDays`
Expected: FAIL.

- [ ] **Step 3: Register the setting**

In `internal/settings/settings.go`, directly after `SkillsEnabled`:

```go
// SkillsIdleDays is how long a skill may go unused before nocx switches it
// off. It is modelled on HistoryRetentionDays down to the ZeroLabel, and for
// the same reason: the product acts on this number, so a person has to be
// able to see it, change it, and turn it off entirely.
var SkillsIdleDays = MustRegisterNumber(NumberSpec{
	Key:         "skills.idleDays",
	Section:     "Skills",
	Label:       "Switch off unused skills after",
	Description: "A skill nobody has used for this long is switched off, and the row says nocx did it. Turning it back on undoes that and starts the clock again. Built-in skills are never switched off this way.",
	DataClass:   PublicConfig,
	Default:     90,
	Min:         fp(0),
	Max:         fp(3650),
	Unit:        "days",
	ZeroLabel:   "Never switched off automatically",
})
```

- [ ] **Step 4: Add the Store option**

In `internal/skill/write.go`, beside `WithClock`:

```go
// WithIdleDays supplies the threshold, as a FUNCTION rather than a value: the
// person can change the setting while the backend runs, and a number captured
// at construction would go on governing after they changed it. Zero, and a
// Store built without this option, mean nothing is ever switched off.
func WithIdleDays(days func() int) StoreOption {
	return func(s *Store) { s.idleDays = days }
}
```

with the field `idleDays func() int` on `Store`.

- [ ] **Step 5: Run both packages, then commit**

```bash
go test ./internal/settings/ ./internal/skill/ -count=1
gofumpt -w internal/settings internal/skill
git add internal/settings/settings.go internal/settings/settings_test.go internal/skill/write.go
git commit -m "feat(settings,skill): the idle threshold is a setting with a zero that means never (nocx-dzy7l)"
```

---

### Task 5: A quiet skill is switched off, and the record says nocx did it

**Files:**

- Modify: `internal/skill/usage.go`, `internal/skill/store_doc.go` (`document`, `ListedSkill`), `internal/skill/discover.go`
- Test: `internal/skill/usage_test.go`

**Interfaces:**

- Consumes: everything from Tasks 1–4.
- Produces: `skill.AutoOff{At string, SilentSince string, Days int}`; `document.AutoOff map[string]AutoOff` (JSON `autoOff`); `ListedSkill.AutoOff *AutoOff`.

**Acceptance Criteria:**

- A skill whose silence exceeds the threshold is not enabled in the list, and carries an `AutoOff` record naming when and from what date.
- Its name does NOT appear in `disabled`.
- A skill inside the threshold is untouched.
- A skill that has never been read ages from `firstSeenAt`.
- A builtin is never switched off, whatever its dates.
- Threshold 0, or no `WithIdleDays`, switches nothing off.
- Turning the skill back on clears the record.

- [ ] **Step 1: Write the failing tests — all seven criteria as subtests**

```go
func TestAQuietSkillIsSwitchedOffAndTheRecordSaysNocxDidIt(t *testing.T) {
	store := usageStandWithIdleDays(t, 30)
	writeSkillAt(t, store, "deploy", "Deploy the service")

	// Seen a hundred days ago and never read.
	if _, err := store.List(); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	store.now = func() time.Time { return time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC) }

	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	row := listedByName(t, listed, "deploy")
	if row.Enabled {
		t.Fatal("a skill unused past the threshold is still enabled")
	}
	if row.AutoOff == nil {
		t.Fatal("no record of WHO switched it off, so the row cannot say")
	}
	if row.AutoOff.SilentSince == "" || row.AutoOff.Days != 30 {
		t.Fatalf("autoOff = %+v, want the date measured from and the threshold applied", row.AutoOff)
	}
	// THE LOAD-BEARING ASSERTION. `disabled` means the person turned it off,
	// and it must go on meaning only that.
	if disabledNames(t, store) != nil {
		t.Fatalf("disabled = %v, want empty: nocx switched this off, the person did not",
			disabledNames(t, store))
	}
}
```

Then, in the same file, one test each for: inside the threshold (untouched); read recently (ages from `lastUsedAt`, not `firstSeenAt`); a builtin root (untouched); `idleDays` 0 (untouched); no `WithIdleDays` (untouched); and re-enabling clearing the record.

> `usageStandWithIdleDays`, `listedByName` and `disabledNames` are three short helpers to write in `usage_test.go`. `disabledNames` reads the raw `skills.json` off disk rather than asking the Store — the point of the assertion is what is in the FILE.

- [ ] **Step 2: Run them and watch every one fail**

Run: `go test ./internal/skill/ -run 'TestAQuietSkill|TestAutoOff' -v`
Expected: FAIL on each.

- [ ] **Step 3: Add the record type and the document field**

In `internal/skill/usage.go`:

```go
// AutoOff is nocx's own record that IT switched a skill off. It is not a name
// in `disabled`, and that is the decision rather than an implementation
// detail: the document has two switch lists precisely so "the person has
// never touched this" is not written as "they turned it off", and folding an
// automatic switch into that list would reintroduce the same loss one level
// up — a person could not tell their own decision from the product's.
type AutoOff struct {
	// At is when nocx switched it off, RFC3339.
	At string `json:"at"`
	// SilentSince is the date the silence was measured from: the last read,
	// or the first sighting when there has never been a read.
	SilentSince string `json:"silentSince"`
	// Days is the threshold that was in force. It travels with the record so
	// a row can say what rule applied, rather than the current setting, which
	// may have changed since.
	Days int `json:"days"`
}
```

and in `document`, beside `Usage`:

```go
	AutoOff map[string]AutoOff `json:"autoOff,omitempty"`
```

and in `ListedSkill`, beside `Source`:

```go
	// AutoOff is present only when NOCX switched this skill off. A pointer,
	// like Source and for the same reason: "nobody switched this off" and "it
	// was switched off with no detail" must be tellable apart.
	AutoOff *AutoOff `json:"autoOff,omitempty"`
```

- [ ] **Step 4: Write the predicate**

In `internal/skill/usage.go`:

```go
// silentSince is the date a skill's silence is measured from: its last read,
// or when discovery first saw it when it has never been read. An empty string
// means neither is known, and nothing ages from nothing.
func silentSince(u Usage) string {
	if u.LastUsedAt != "" {
		return u.LastUsedAt
	}
	return u.FirstSeenAt
}

// idleBeyond reports whether a skill has been quiet longer than days, and the
// date that was measured from. days <= 0 is "never", which is the setting's
// zero and also a Store with no threshold wired at all.
func idleBeyond(u Usage, days int, now time.Time) (string, bool) {
	if days <= 0 {
		return "", false
	}
	since := silentSince(u)
	if since == "" {
		return "", false
	}
	at, err := time.Parse(time.RFC3339, since)
	if err != nil {
		// An unparseable date is not evidence of silence. Refusing to act on
		// it is the strict direction: nocx switches nothing off on a fact it
		// cannot read.
		return "", false
	}
	if now.Sub(at) <= time.Duration(days)*24*time.Hour {
		return "", false
	}
	return since, true
}
```

- [ ] **Step 5: Join it to the walk**

In `internal/skill/store_doc.go`'s `List()`, after the stamp and before rows are built, evaluate and persist. Read the freshly stamped document once, decide per skill, and write back only if something crossed:

```go
	// Evaluated HERE, inside the pass that already reads the document and
	// already decides enablement — not in a background sweep. A scheduler
	// would be a second thing able to change state behind the person's back,
	// and would raise the question of whether it ever ran. The cost is that a
	// skill is switched off at the next discovery after its threshold passes
	// rather than on the day it passes, and nothing in the product claims
	// otherwise.
	autoOff, autoErr := s.applyAutoOff(detailed)
	if autoErr != nil {
		slog.Debug("skill: idle skills were not evaluated", "error", autoErr)
	}
```

In `internal/skill/usage.go`:

```go
// applyAutoOff decides which discovered skills have gone quiet past the
// threshold, records that nocx switched them off, and answers with the rows so
// the list can mark them. It writes only when something has actually crossed —
// which is rare — so an ordinary discovery costs no write.
func (s *Store) applyAutoOff(found []discovered) (map[string]AutoOff, error) {
	days := 0
	if s.idleDays != nil {
		days = s.idleDays()
	}
	s.docMu.Lock()
	defer s.docMu.Unlock()
	d, err := s.loadDocumentLocked()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	changed := false
	for _, candidate := range found {
		if _, already := d.AutoOff[candidate.Name]; already {
			continue
		}
		// A BUILTIN IS NEVER SWITCHED OFF THIS WAY. It backs an affordance the
		// interface promises, and silently switching one off turns a promise
		// into nothing — the refusal hermes gives PROTECTED_BUILTIN_SKILLS,
		// for our own reason. Its counters are still kept: knowing a builtin
		// goes unused is worth seeing.
		if candidate.Provenance == ProvenanceBuiltin {
			continue
		}
		// The pin cancels this and nothing else (Task 6). Until that task
		// lands, d.Pins is absent and this reads as false for everything.
		if d.Pins[candidate.Name].KeepEnabled {
			continue
		}
		since, idle := idleBeyond(d.Usage[candidate.Name], days, now)
		if !idle {
			continue
		}
		if d.AutoOff == nil {
			d.AutoOff = make(map[string]AutoOff, 1)
		}
		d.AutoOff[candidate.Name] = AutoOff{
			At:          now.Format(time.RFC3339),
			SilentSince: since,
			Days:        days,
		}
		changed = true
	}
	if changed {
		if writeErr := s.writeDocumentLocked(d); writeErr != nil {
			return nil, writeErr
		}
	}
	return d.AutoOff, nil
}
```

The listed row's `Enabled` is then `found.Enabled && autoOff[name] == nil`, and `listed.AutoOff` is a pointer to the row when there is one.

> Read `discoverDetailed`'s enablement block before writing this. Do NOT move the auto-off decision into `discoverAll`: `Discover` (the assistant's index) is a free function with no Store and therefore no document to write, and a skill switched off must be absent from the index by the ENABLEMENT it already computes, not by a second rule living in two places. Make `applyAutoOff` write `document.AutoOff`, and have `discoverAll`'s enablement read `AutoOff` from the document it already loads — one owner, two readers.

- [ ] **Step 6: Clear the record on re-enable**

In the `skills.setEnabled` path (`internal/skill`'s setter — find it with `grep -n "func (s \*Store) SetEnabled" internal/skill`), when `enabled` is true, delete the skill's `AutoOff` row in the same document write:

```go
	// Turning it back on is a statement that this is wanted, so the automatic
	// mark goes rather than standing beside a switch that contradicts it. The
	// silence is measured afresh from here — the next flush of a read, or the
	// first-seen date, whichever the skill has.
	delete(d.AutoOff, name)
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/skill/ -count=1 -v -run 'TestAQuietSkill|TestAutoOff'`
Expected: PASS on all subtests.

- [ ] **Step 8: Falsify the load-bearing assertion**

Temporarily make `applyAutoOff` add the name to `d.Disabled` as well. Run the test. Expected: FAIL on `disabled = [deploy], want empty`. Revert the probe.

- [ ] **Step 9: Run the package and commit**

```bash
go test ./internal/skill/ ./internal/assistant/ -count=1
gofumpt -w internal/skill
git add internal/skill/usage.go internal/skill/usage_test.go internal/skill/store_doc.go internal/skill/discover.go
git commit -m "feat(skill): a skill nobody uses is switched off, and the record says nocx did it (nocx-dzy7l)"
```

---

### Task 6: Two pins, and the machine obeys them

**Files:**

- Modify: `internal/skill/store_doc.go` (`document`, `ListedSkill`), `internal/skill/usage.go`, `internal/skill/write.go` (`Update`, `Delete`)
- Test: `internal/skill/pins_test.go` _(new)_

**Interfaces:**

- Produces: `skill.Pins{KeepEnabled bool, KeepUnchanged bool}`; `document.Pins map[string]Pins` (JSON `pins`); `ListedSkill.Pins Pins`; `func (s *Store) SetPin(name string, pin PinKind, on bool) error` with `PinKind` a closed string type (`PinKeepEnabled`, `PinKeepUnchanged`).

**Acceptance Criteria:**

- `keepEnabled` cancels auto-off and nothing else — the person's own switch still works on that skill.
- `keepUnchanged` makes `Update` and `Delete` refuse, naming the skill and how to lift the pin.
- The refusal is at the CALL, not at the offer: `skills.update` stays in the registry and stays offered.
- An unpinned skill updates and deletes normally **in the same test** as the refusal.
- Setting an unknown `PinKind` is refused rather than silently ignored.

- [ ] **Step 1: Write the failing tests, refusal and success paired**

```go
func TestKeepUnchangedRefusesTheMachineAndOnlyForThatSkill(t *testing.T) {
	store := usageStand(t)
	writeSkillAt(t, store, "pinned", "Do not touch")
	writeSkillAt(t, store, "ordinary", "An ordinary skill")
	if err := store.SetPin("pinned", PinKeepUnchanged, true); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	err := store.Update("pinned", "Do not touch", "new body")
	if err == nil {
		t.Fatal("a pinned skill was updated")
	}
	if !strings.Contains(err.Error(), "pinned") {
		t.Errorf("refusal = %q, want it to name the skill", err)
	}

	// THE PAIRED SUCCESS. Without it this test passes against a build where
	// Update refuses everything.
	if err := store.Update("ordinary", "An ordinary skill", "new body"); err != nil {
		t.Fatalf("an unpinned skill refused an update: %v", err)
	}
}

func TestKeepEnabledCancelsAutoOffAndNotThePersonsSwitch(t *testing.T) {
	// A skill past the threshold with keepEnabled stays enabled; the same
	// skill switched off BY THE PERSON is off, pin or no pin.
}
```

- [ ] **Step 2: Run and watch fail**

Run: `go test ./internal/skill/ -run 'TestKeepUnchanged|TestKeepEnabled'`
Expected: FAIL — `undefined: SetPin`, `PinKeepUnchanged`.

- [ ] **Step 3: Add the type, the document field and the setter**

In `internal/skill/usage.go` (or a new `pins.go` if that file is getting long):

```go
// PinKind is the closed vocabulary of pins. Closed for the reason every other
// vocabulary in this package is: an unrecognised value must be a refusal
// rather than a flag nobody set.
type PinKind string

const (
	// PinKeepEnabled cancels automatic switching-off and nothing else. The
	// person's own switch is unaffected: a pin protects against the machine,
	// never against the owner.
	PinKeepEnabled PinKind = "keepEnabled"
	// PinKeepUnchanged refuses machine writes — skills.update and
	// skills.delete called by the assistant, and the curator when it exists.
	// The page's own Delete button is unaffected, for PinKeepEnabled's
	// reason: a pin a person must remove before their own deliberate action
	// is a dialog that protects nothing.
	PinKeepUnchanged PinKind = "keepUnchanged"
)

// Pins are independent FLAGS, not a state. A skill can carry both, either or
// neither, and neither is a position on a lifecycle: a rarely-needed but
// irreplaceable skill wants the first, and a hand-tuned one wants the second
// while remaining an ordinary candidate for switching off.
type Pins struct {
	KeepEnabled   bool `json:"keepEnabled,omitempty"`
	KeepUnchanged bool `json:"keepUnchanged,omitempty"`
}
```

- [ ] **Step 4: Make Update and Delete consult it**

At the top of both `Store.Update` and `Store.Delete`, before anything is written:

```go
	if pinned, err := s.pinned(name); err != nil {
		return err
	} else if pinned.KeepUnchanged {
		return fmt.Errorf("skill %q is pinned unchanged, so it is not yours to edit: "+
			"turn that off on the skill's own tab first", name)
	}
```

- [ ] **Step 5: Make applyAutoOff skip keepEnabled**

Add to the skip beside the builtin check in Task 5's `applyAutoOff`.

- [ ] **Step 6: Run, then commit**

```bash
go test ./internal/skill/ -count=1
gofumpt -w internal/skill
git add internal/skill/
git commit -m "feat(skill): two pins — keep enabled, keep unchanged — and the machine obeys both (nocx-dzy7l)"
```

---

### Task 7: The wire carries age, the automatic mark and the pins

**Files:**

- Modify: `contracts/skills.list.schema.json`
- Create: `contracts/skills.setPin.schema.json`, `contracts/skills.setPin.params.schema.json`
- Modify: `internal/transport/ws_skill_handlers.go`, `internal/transport/ws_config_handlers.go` (registration near line 2435)
- Regenerate: `frontend/src/generated/skills.list.ts`, `frontend/src/generated/skills.setPin.ts`
- Test: `internal/transport/ws_skill_contract_test.go`

**Interfaces:**

- Consumes: `ListedSkill.AutoOff`, `.Pins`, and the usage fields.
- Produces: `skills.setPin` taking `{name, pin, on}` and answering `{name, pins}`.

**Acceptance Criteria:**

- `skills.list` carries per skill: `usage` (count, lastUsedAt, firstSeenAt), `autoOff` when there is one, and `pins`.
- Every added shape is in `contracts/` with `additionalProperties: false` and an explicit `required`.
- An OVER-THE-WIRE test drives a real backend, ages a real skill, and asserts the payload — not a DTO test alone.
- `npm run contracts:check` passes with the committed generated file.

- [ ] **Step 1: Extend the contract**

Add to `skills.list.schema.json`'s `$defs.skill.properties`, and to its `required` where the field is always sent:

```json
"usage": {
  "description": "How this skill has been reached for. `count` is successful skills.read calls and nothing else — opening the skill in its tab and checking it are inspection, not use, and a skill that could be kept young by being looked at would never age. `firstSeenAt` is when DISCOVERY first saw it rather than when it was installed, because a skill can be placed by hand, restored from a backup or arrive with a profile and have no install date at all. Always present; a skill nothing is recorded about carries a count of zero and empty dates.",
  "type": "object",
  "additionalProperties": false,
  "required": ["count"],
  "properties": {
    "count": { "type": "integer" },
    "lastUsedAt": { "type": "string" },
    "firstSeenAt": { "type": "string" }
  }
},
"autoOff": {
  "description": "Present only when NOCX switched this skill off for going unused — never when the person did. That distinction is the point of the field rather than a nicety: the document's `disabled` list means \"the person turned this off\" and goes on meaning only that, so an automatic switch needs a record of its own or a person could not tell their own decision from the product's. `days` is the threshold that was in force at the time, not the current setting, which may have changed since. Turning the skill back on clears it.",
  "type": "object",
  "additionalProperties": false,
  "required": ["at", "silentSince", "days"],
  "properties": {
    "at": { "type": "string" },
    "silentSince": { "type": "string" },
    "days": { "type": "integer" }
  }
},
"pins": {
  "description": "Two independent flags, not a state: a skill may carry both, either or neither. `keepEnabled` cancels the automatic switch-off and nothing else; `keepUnchanged` refuses machine writes — skills.update and skills.delete from the assistant, and the curator when it exists. Both protect against the machine and never against the owner: the person's switch and the page's Delete button are unaffected, because a pin somebody has to remove before their own deliberate action is a dialog that protects nothing.",
  "type": "object",
  "additionalProperties": false,
  "required": [],
  "properties": {
    "keepEnabled": { "type": "boolean" },
    "keepUnchanged": { "type": "boolean" }
  }
}
```

- [ ] **Step 2: Regenerate and check**

```bash
cd frontend && npm run contracts && npm run contracts:check
```

Expected: the generated `skills.list.ts` gains the three shapes; `contracts:check` passes.

- [ ] **Step 3: Extend the DTO test, watch it fail, then fix the handler**

`TestSkillsList_DTOConformsToContract` builds `skillsListResult` literals. Add a row carrying usage, an `autoOff` and both pins. Run it: it fails until `skillsListEntry` actually marshals them. Then confirm `ListedSkill`'s new fields reach the wire (they do by embedding — assert it rather than assume).

- [ ] **Step 4: Write the over-the-wire test**

In `internal/transport/ws_skill_contract_test.go`, modelled on `TestSkillsList_ARefusedDirectoryReachesThePersonOverTheWire`: write a skill to disk, drive a real backend with the threshold set and the clock moved, call `skills.list`, validate against the schema, and assert `autoOff` is present with the right `silentSince` while `usage.count` is 0.

> The clock is a Store seam, so the backend under test has to be built with it. Read `skillsURLConnection` and add an option there rather than exporting a setter — a production path that can have its clock moved is a production path that can be lied to.

- [ ] **Step 5: Add skills.setPin**

Two contract files, the handler beside `skills.setEnabled` in `ws_skill_handlers.go:209`, and the registration beside `ws_config_handlers.go:2435`. The handler refuses an unknown `pin` value by name.

- [ ] **Step 6: Run and commit**

```bash
go test ./internal/transport/ -count=1
cd frontend && npm run contracts:check && npx tsc --noEmit && cd ..
git add contracts/ internal/transport/ frontend/src/generated/
git commit -m "feat(transport): the wire carries a skill's age, the automatic mark and its pins (nocx-dzy7l)"
```

---

### Task 8: The page shows the age, and the tab carries the pins

**Files:**

- Modify: `frontend/src/skills-store.ts`, `frontend/src/skills-presentation.ts`, `frontend/src/skills-section.tsx`
- Modify: `frontend/src/skill-view/` (the tab's body — read `skill-view-body.tsx` first)
- Test: `frontend/src/skills-section.test.tsx`, `frontend/src/skill-view/*.test.tsx`

**Acceptance Criteria:**

- A skill's row shows its use count and last-used date in the `detail` slot.
- A skill nocx switched off shows it in the `status` cell, in words, with the date.
- A skill the PERSON switched off shows no such mark.
- The pins are two switches on the skill's own tab, not in the row.
- Toggling a pin calls `skills.setPin` and refreshes.

- [ ] **Step 1: Write the failing row tests**

In `skills-section.test.tsx`, following the `describe('a directory nocx would not index')` block's shape: a fixture with `autoOff` renders a row saying so; a fixture with the name in the person's own off state does not.

- [ ] **Step 2: Run, fail, implement**

The row already has both slots. Add to `skills-presentation.ts`:

```ts
/** "used 12 times, last on 3 March" — or the honest absence. */
export function usageLine(usage: Usage): string {
  if (usage.count === 0) return 'never used'
  return `used ${usage.count} ${usage.count === 1 ? 'time' : 'times'}, last on ${shortDate(usage.lastUsedAt)}`
}
```

- [ ] **Step 3: Kit check before the pins**

Read `frontend/src/ui/README.md` and list `frontend/src/ui/` BEFORE building the pin controls. A pin is `Checkbox variant="switch"`; do not hand-roll one, and do not repaint it.

- [ ] **Step 4: Run the frontend gates and commit**

```bash
cd frontend && npx vitest run src/skills-section.test.tsx src/skill-view/ && npm run lint && npx tsc --noEmit -p tsconfig.json && npx tsc --noEmit -p tsconfig.test.json
cd .. && git add frontend/src/
git commit -m "feat(frontend): a skill's row shows its age, and its tab carries the two pins (nocx-dzy7l)"
```

---

### Task 9: The epic's happy path

**Files:**

- Create: `e2e/skill-ageing.spec.ts`
- Test: itself

**Acceptance Criteria — the five steps of the spec's §8, in one spec:**

1. A skill first seen in the past and never read, with the threshold passed, is switched off after a list.
2. Its row says nocx did it, in words, with the date.
3. `skills.json` on disk does NOT contain its name in `disabled`.
4. The same skill with `keepEnabled` is not switched off; a builtin with the same dates is not switched off.
5. Turning it back on clears the mark, and the row stops saying it.

Plus the second, smaller check: `skills.update` against a `keepUnchanged` skill refuses and names it, while the same call against another skill succeeds.

- [ ] **Step 1: Write the spec**

Model it on `e2e/skills-management.spec.ts` — the disposable `$HOME`, the fixture written before the page opens, `rowFor` by visible name. **No `waitForTimeout` and no `sleep` anywhere**: wait on a DOM state.

The dates are seeded by writing `skills.json` directly into the disposable home before the backend starts, with a `firstSeenAt` a hundred days back and `schemaVersion: 5`. That is the whole seam — the backend's clock is not moved, real time does the ageing, and the fixture is what is old.

- [ ] **Step 2: Run it in the container**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/skill-ageing.spec.ts
```

Expected: passed.

- [ ] **Step 3: Falsify it**

Set the threshold to 0 in the fixture. Expected: the spec fails on step 1. Revert.

- [ ] **Step 4: Commit**

```bash
cd frontend && npx prettier --write ../e2e/skill-ageing.spec.ts && cd ..
npx tsc --noEmit -p e2e/tsconfig.json
git add e2e/skill-ageing.spec.ts
git commit -m "test(e2e): the epic's happy path — a skill ages, nocx switches it off, and the person takes it back (nocx-dzy7l)"
```

---

## Task ordering

```
1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9
```

Strictly sequential: every task consumes the one before it, and all nine touch `internal/skill/store_doc.go` or the same document shape. Declare it with `br dep add <task-N+1> <task-N>` so only one is ready at a time.

## Spec coverage

| Spec section                                                      | Task                             |
| ----------------------------------------------------------------- | -------------------------------- |
| §1 what counts as use                                             | 2                                |
| §2 where the telemetry lives, batching, schemaVersion 5           | 1, 2                             |
| §2 `firstSeenAt` at discovery                                     | 3                                |
| §3 when it fires, where evaluated, the honest cost                | 5                                |
| §3 what is recorded, `disabled` untouched                         | 5                                |
| §3 what the person sees, undo clears the mark                     | 5, 8                             |
| §3 builtins never switched off                                    | 5                                |
| §3 the assistant is not told                                      | 5 (by enablement; asserted in 9) |
| §4 the two pins, execution-time refusal                           | 6                                |
| §5 the setting                                                    | 4                                |
| §6 the surface                                                    | 8                                |
| §7 the wire                                                       | 7                                |
| §8 the epic's happy path                                          | 9                                |
| Testing: both ends, paired success, failure paths, falsifiability | 2, 5, 6, 9                       |
