# The skill viewer: a tab, a bundle you can read, and a check that is remembered

Status: DRAFT — revision 2, after a codex adversarial review and four owner
decisions. Not approved.
Brainstorm bead: nocx-4l3km.

Revision 2 supersedes revision 1 in four places, each on the owner's
instruction (2026-09-05):

1. **The audit produces a VERDICT.** Revision 1 inherited design §7's stance
   that it must not. It does check, and it says what it concluded.
2. **Nothing is written into the skill's own directory.** Revision 1's
   per-skill metadata file was withdrawn by the owner's own objection: a
   record that lives among the bytes it describes can be forged by whoever
   wrote those bytes.
3. **Reports live in `content.db`,** the encrypted store, in a table of their
   own. The registry (`skills.json`) stays a plain file.
4. **A builtin skill is not checked at all,** so it needs no storage and no
   button.

## Why

Three complaints from the owner about the Skills card as it shipped
(`frontend/src/skills-section.tsx`, `nocx-0bsa4.3` + `nocx-0bsa4.4`):

1. It is a one-column modal. A bundle of several files has nowhere to show
   them: the card renders a file list only when `manifestPaths().length > 1`
   (`skills-section.tsx:691`), then stacks the chosen file's bytes underneath
   it in the same column.
2. The check is re-run from scratch on every press. Nothing is remembered, so
   looking at a skill a second time costs a model call again.
3. The result is seven stacked components, in which the caveat carries the
   same visual weight as the report it qualifies, and the static scan's
   findings are cards detached from the files they were found in
   (`skills-section.tsx:793`).

## What the code already decides, and what it forces

Everything below was checked against the tree at `19ced0d8`, and most of it
was found by the review rather than assumed.

- **The surface seam.** `SurfaceRegistry.register(id, {surfaceType,
singletonKey, factory, descriptor})` + `PaneManager.openPane(content,
descriptor)`. **Three** surfaces are registered through the registry —
  `fileViewer`, `api`, `gitDiff` (`main.tsx:658,732,856`). Notes is NOT one of
  them: `registerNotesSurface(tm, store)` takes no registry and builds its
  descriptor directly (`notes/index.ts:28`). Revision 1 said four; it was
  wrong.
- **`openPane` deduplicates by `singletonKey` ALONE**, with no check of the
  surface type (`panes.ts:1630`). Every key must therefore be namespaced;
  notes does it as `note:${id}` (`notes/index.ts:41`).
- **`FileViewerContent` cannot be reused.** It reads through a live binding on
  some machine; a builtin's bytes are in the binary and reachable only through
  `skills.file`.
- **`Provenance.digested()` is true only for `managed` and `installed`**
  (`skill.go:45`). `authored` and `builtin` carry no digest and can never read
  `changed`.
- **Builtin lives in an `embed.FS`** (`skill.go:135-140`: a Root has either
  `FS` or `Dir`). It has no directory, cannot be written into, and does not
  enter a backup snapshot (`treesFor`, `backup.go:64-73`).
- **The two existing walks disagree about symlinks.** `hashSkillDirectory`
  hashes `"symlink:" + target` (`discover.go:315`); `directorySkillFilePaths`
  skips symlinks entirely (`files.go:130`). A digest computed over the second
  walk and compared against the first would give two contradictory sentences
  on one screen for one ordinary edit. This is why the check's digest is
  defined over the material and not over a walk (§5).
- **`skills.json` is rewritten whole under `docMu`** (`store_doc.go:548,596`),
  and `storage.Document.Write` replaces it atomically via temp+rename with a
  symlink guard (`document.go:175-199`). Atomic against tearing; NOT against a
  lost update across a long call.
- **`content.db` may legitimately be absent.** `app.go:811` starts with
  `content.NewStub(logger)` and replaces it only if the key loads and
  `content.Open` succeeds (`app.go:967`). Every method of the stub returns
  `ErrNotImplemented`.
- **Retention is table-scoped.** `retention.go` deletes from `entries` and
  `artifact_chunks` only (`:208,:230,:564`); there is no user-facing action
  that drops the database. A table of ours is not swept.
- **The store is single-connection.** `maxOpenConns` is 1 because the cipher
  enciphers whole 4096-byte blocks (`sqlite.go:65`, ADR-0043). Reads queue
  behind live ledger writes, which is why nothing here touches the database on
  the list path (§6).
- **The report is ALREADY bounded, upstream.**
  `maxAuditReportBytes = 16 KiB` (`skillaudit.go:58`) is applied in
  `skillaudit.go:109` before the prose leaves `internal/assistant`. Its stated
  reason is the READER'S SCREEN — a hostile skill that talks the auditor into
  echoing forever must not spend it — and not the size of any record. This
  work adds no second bound (§6).
- **`Skill.Offered()` is the only filter on what the assistant is given**
  (`write.go:177` → `skill.go:162`): enabled, plus approval status. Nothing
  else may enter that predicate (§3).

## The design

### 1. A skill opens in its own tab

A fourth registry surface, wired from `main.tsx` beside the other three:

```
registerSkillSurface(registry, tm, deps)   // the one wiring point
openSkill(name)                            // what the Skills row calls
```

`singletonKey` is `skill:${name}` — namespaced, because `openPane` matches the
key alone. `restoreDescriptor` is `null`, for file-viewer's stated reason.

**Identity is the RESOLVED skill, never the requested name.** Names collide
across roots and discovery keeps the first root's copy (`discover.go:153`);
`FilesResult` already documents that requested and resolved identity can
differ (`files.go:40`). The tab holds the resolved name and provenance, and:

- deleting the skill a tab is open on **closes that tab** with a sentence
  saying so — the behaviour the current Dialog has (`skills-section.tsx:265`)
  and which must not be lost;
- if deletion reveals a same-name copy in a lower-precedence root, that is a
  DIFFERENT skill: the tab closes rather than silently re-pointing at bytes
  nobody asked for.

The `Dialog` in `skills-section.tsx` is deleted. It does not survive as a
second route to the same bytes.

Why a tab: the card's own argument for being a modal was that reading a skill
must not cost the page a person is on — which a tab satisfies better. A modal
is for "answer this now"; reading a skill is the opposite. And two skills side
by side is a question a modal cannot be asked.

**Multi-window staleness is a known, declared property, not a new defect.**
`main.tsx:238` states it: there is no change notification on the wire, a
writer re-reads. Each window builds its own `SkillsStore`. A long-lived tab
makes it more visible than a modal did, so the tab **re-reads on focus**. That
is the whole of the mitigation; a `skills.changed` notification is out of
scope and belongs to whoever revisits design §6.

### 2. The tab's layout

```
┌──────────────────────────────────────────────────────────────────┐
│ deploy  [installed]                          [⟳ Re-check]   (●—) │
│ /…/installed-skills/deploy/SKILL.md                               │
├─────────────────────┬────────────────────────────────────────────┤
│ THE CHECK           │  Suspect — gemma-4-26b-a4b · local · 4 Sep │
│  suspect · 4 Sep    │                                            │
│                     │  <the model's prose>                       │
│ FILES               │                                            │
│  SKILL.md           │  The static scan matched 2 lines, in       │
│  scripts/setup.sh ● │  scripts/setup.sh.                         │
│  references/hosts.md│                                            │
└─────────────────────┴────────────────────────────────────────────┘
```

**Header** — resolved name, provenance badge, path, the enable switch,
`Re-check`, and `Re-approve` when the bytes moved.

**Left column** — a `Stack` of two groups. `THE CHECK` is one row; `FILES` is
the whole bundle, ALWAYS, including a bundle of one file: the column's
existence says the bundle has one file, its absence says nothing. Split by the
kit's `ResizeHandle`.

The surface owns what `ResizeHandle` does not (`ui/README.md:51`): the initial
width, the minimum and maximum, and whether the width persists. Decision: a
fixed default, clamped to [180px, 40% of the pane], **not persisted** — one
less durable value, and no surface in this repo persists a split today.

Below 640px the pane stacks: the list above, the view below. Scroll ownership
is per column, never the pane.

**Right pane** — whatever is selected. A file renders as `FileReadout` with
scan matches marked on their lines (already built, `nocx-872jc`). The check
renders as §4.

Keyboard: the list is a single-select listbox; ↑/↓ move, Enter opens, focus
moves to the pane. After a deletion focus returns to the list.

**The mark on a file is a kit component, not a glyph.** Icons are
component-owned vocabulary (`ui/README.md:383`). `StatusDot tone="warning"`
already exists and already means this; the list uses it, with an accessible
name naming the file and the count.

### 3. The verdict

`skills.audit` gains a closed-vocabulary verdict, **copying
`internal/assistant/classifier.go`'s shape rather than inventing one**:
`clear | suspect`, with the model's prose as its justification, and an
unrecognised value refused rather than rendered (`classifier.go:246`).

Three properties are load-bearing, and they are code, not prose:

- **The verdict gates nothing.** `Skill.Offered()` stays exactly
  `enabled && status` (`skill.go:162`), and a test asserts that the predicate
  has no third term. What the assistant is offered is decided by the person's
  switch and the digest comparison, and by nothing a model said. This is what
  makes a persuaded model survivable: a skill's own text can address whoever
  reads it, and if a hostile file talks a model into `clear`, nothing changes
  except a sentence on a page.
- **The verdict is attributed.** It is rendered as "_model_ concluded", never
  as nocx's finding, and the model and endpoint travel with it.
- **The static scan is drawn apart from the verdict.** The scan is ours,
  deterministic and checkable line by line (`internal/skill/scan.go`); the
  verdict is a model's opinion about attacker-controlled text. They are two
  claims of different kinds and the surface never merges them into one
  judgement.

The existing caveats stay, at a sentence's weight: absence of a scan match is
not safety, and a skill's own text can address its reader.

### 4. The report's presentation

| today                                       | becomes                                                                           |
| ------------------------------------------- | --------------------------------------------------------------------------------- |
| `StatusCard` "A description, not a verdict" | gone — there is a verdict now                                                     |
| `FactList` of role / endpoint / model       | one line: `Suspect — gemma-4-26b-a4b · local · 4 Sep`                             |
| `StatusCard prose` carrying the report      | prose at the pane's full width                                                    |
| `MarkerList` of files read                  | dropped — read files ARE the left column; OMITTED files stay, as a sentence       |
| `StatusCard` + `CodeBlock` per finding      | dropped — findings live on the files: the dot in the column, the mark on the line |
| `StatusCard` "the scan matched nothing"     | the same sentence, sized as a sentence                                            |

**The prose is rendered as text, never as markup.** Either plain Solid text
(what `status-card.tsx:62` does today) or the kit's `answer-markdown`, which
escapes model text and deliberately makes links inert
(`answer-markdown.ts:30,39`). Raw HTML and active links are forbidden, and a
test asserts it.

**Findings shown on a file come from the LIVE scan, never from the stored
check.** `skills.file` rescans current bytes at read time (`file.go:115`), and
a stored line number cannot safely mark an edited file. When the stored check
is stale (§5), its own finding count is still shown in the report — as a fact
about what was read then — and the dots in the file list continue to come from
the live scan. Two sources, two sentences, neither pretending to be the other.

### 5. What a stored check is about

**The digest is taken over the material the model was given**, accumulated as
`AuditMaterial.Document` is composed (`audit.go:146-172`), not by a separate
walk. Three reasons, and the first is fatal to the alternative:

- A separate walk observes different bytes than the one that was sent — under
  an ordinary concurrent edit, "current" would be untrue.
- The two existing walks disagree about symlinks (see above), so any digest
  built on `files.go`'s walk would contradict `status: changed` on a real
  edit.
- The material is already bounded by `MaxAuditBytes` = 128 KiB
  (`audit.go:34`), so recomputing it to test currency is bounded too. A hash
  of the tree has no ceiling at all.

It is therefore explicitly **"the digest of what the model read"**, a
different question from `Digests[name]`'s "did the bytes move since the person
approved them". The surface never presents them as comparable: one is on the
row as `Changed since installation`, the other is in the tab as `this check is
about an earlier version of these files`.

A multi-file check is a mixed-time snapshot — files are read one after another
— and the design does not pretend otherwise: the recorded digest is over
exactly the bytes sent, whenever each was read, and that is the only claim
made about them.

Three states in the tab:

- **no check** → `Check this skill`;
- **a check, digest matches** → it is on screen, zero model calls;
- **a check, digest differs** → it is on screen AND above it: "this check is
  about an earlier version of these files."

`Re-check` is the only thing that spends money.

### 6. Storage

Split on ONE axis — _is this a control or a record_ — and the split is forced
rather than chosen, because `content.db` can be a stub (`app.go:811`).

**The registry stays `skills.json`, a plain file.** `enabled`, the approved
digest, the install source. It is the control plane: the switch that takes a
skill away from the assistant must work when nothing else does, so it depends
on no key and no database. Builtin needs it too, and builtin can carry nothing
of its own.

**Checks go in `content.db`, in a table of their own.** One row per skill
name: verdict, report, role, endpoint, model, taken-at, material digest,
read paths, omissions, findings. Reached through a new
`SkillChecks() SkillCheckRepository` on `ContentDB` (`content.go:50`), with
its own stub arm.

Why the same database and not a second one: the expensive, dangerous part is
the key lifecycle, not the file. `internal/contentkey` is a keystore branch, a
derivation branch, per-OS identity and the invariant that a lost key must
never be re-minted. A second database means a second salt, a second
"was it ever created" marker, a second lost-key story, and a second place to
obey the plaintext-canary rule (`sqlite.go:18`). Against that, the separation
buys nothing: retention is table-scoped and there is no action that drops the
file.

Consequences, all of them wanted:

- **Nothing is written into the skill's directory.** A hostile bundle cannot
  ship a pre-written `clear`, and cannot vouch for itself. This is the rule
  `skill.go` already states for provenance — the root, never a field a file
  could carry, "so it cannot be forged by whatever wrote one" — applied to a
  fact of the same class.
- **Checks do not travel in a backup.** `internal/backup` carries settings
  documents and skill trees; it does not carry `content.db`. Satisfied by
  construction, with no redaction code to write and none to forget.
- **The report is NOT masked, and that is decided rather than skipped.** The
  ledger masks the classifier's justification because it is model output about
  COMMAND ARGUMENTS, which carry secrets, and because the command is ephemeral
  while the ledger is durable — the ledger is what creates the durability. The
  premise does not hold here in either half. This report is about a skill's
  FILES, which are already on disk in plaintext in an unencrypted directory,
  and it would be written into `content.db`, which is encrypted. Masking would
  move a secret from a less protected place to a more protected one and redact
  it there, while the original sits beside it in the clear. Against
  `contentkey.go:14`'s stated threat — the detached copy — the copy carries the
  skill file itself whether or not the report was masked. It is ceremony, and
  ceremony is what teaches people to click past the prompt that matters.
- **No new bound is added, because one already exists.** The report arrives
  capped at `maxAuditReportBytes` = 16 KiB (`skillaudit.go:58`), applied
  before it leaves `internal/assistant`. The column is `TEXT` and carries no
  further ceiling: a second cut measured differently would be a second answer
  to one question, which is what that constant's own comment already says
  about `truncateRunes`.

  The storage side does not argue for a smaller one either, which is worth
  writing down because it is the reason not to invent a defensive number.
  SQLite `TEXT` is bounded by `SQLITE_MAX_LENGTH`, on the order of a gigabyte.
  `Budget.RetentionBytes` — "what eviction acts on" (`budget.go:11`) — acts on
  `entries` and `artifact_chunks`, so this table is never swept.
  `Budget.DiskCeilingBytes` is physical and DOES count these rows, but
  exceeding it triggers compaction rather than deletion, and 16 KiB across a
  library of skills is under a megabyte against a multi-gigabyte ceiling.

  So the 16 KiB is a SCREEN budget that happens to bound a row, and if real
  bundles start producing truncated reports it is one constant to raise, with
  nothing in storage objecting.

- **A stub database is a visible state, not a degrade.** With no store, a
  check runs and shows its result and the tab says it was not saved. It is not
  a `slog.Warn` under a UI that claims otherwise.

**Builtin is not checked.** No button on its row, none in its tab. Its bytes
came with the binary and the person decided about them when they installed
nocx; a check would be theatre with a model's bill attached. This is the same
line that already makes builtin the provenance with no `Dir`, no digest and no
backup entry.

### 7. Concurrency

- `docMu` is **never held across the model call**.
- The model answers → lock → reload the latest document → write only this
  skill's row → release. Two checks of different skills both survive. Two of
  the same skill: last writer wins, and the loser's result still shows in the
  tab that asked for it.
- **Before persisting, the resolved identity and the material digest are
  re-verified.** A check that finishes after the skill was deleted or
  reinstalled is discarded rather than attached to different bytes, and the
  tab says so.
- Restore writes `skills.json` outside the mutation lock (`backup.go:158`).
  Since checks no longer live in that document, a restore cannot lose one; it
  can invalidate one, and the digest comparison already reports that.

### 8. The wire

- `skills.list` — each skill gains an optional
  `check?: { at, verdict, model }`. **No currency flag and no database read on
  this path**: the list is refreshed often and the store is
  single-connection. The row states a date and a verdict, which are true
  whatever the bytes now are.
- `skills.check(name)` — NEW, reads what is stored, spends nothing. It is
  what computes `current`, because it is called when a tab opens. Nothing
  recorded is a RESULT, not an error.
- `skills.audit(name)` — as today, but stores. Its result gains a
  `stored: 'yes' | 'no'` with a reason, so a persistence failure still
  delivers the report: the RPC returns the reading either way and errors only
  when the check itself could not be produced. Without this the requirement
  "the report stays on screen" is not implementable — the handler today
  returns a result or an error and nothing else (`ws_skill_audit.go:145,166`).

Each gets a schema in `contracts/` with `additionalProperties: false` and an
explicit `required`, a generated renderer type, Go validated against it, and a
`…OverTheWireConformsToContract` test off the real socket.

Note: `ws_skill_audit_test.go:176` compares `skills.list` byte-for-byte and
will necessarily need updating; it must be updated to assert the new field,
never loosened.

### 9. The Settings row

Three icons: **eye** (open the tab), **⟳ re-approve** (only when the bytes
moved), **trash** (delete without entering the skill). Plus the switch.

**The magnifier goes.** It was added in `19ced0d8` as "open and start the
audit"; with a check remembered there is nothing to spend when one exists, and
the button lives in the tab when one does not.

The row gains a third evidence line: `Checked 4 Sep — suspect`. A date and the
model's word, never a tick of our own.

The switch is on the row and in the tab. Both call `store.setEnabled`, both
read the store's one answer; the STATE is single and that is what may not be
duplicated. (This is not the "two surfaces may never own the same input" rule,
which is about two claimants on one input EVENT. The real exposure here is
cross-window staleness, addressed in §1.)

### 10. Deliberately out

- Checking automatically on install, or on a schedule.
- Any behaviour that depends on the verdict.
- Ageing, staleness, archiving — `nocx-dzy7l` owns those, and will want the
  same table.
- A `skills.changed` notification, and any general fix to cross-window
  staleness.
- Encrypting `skills.json`. It is plaintext today and records what was
  approved and from what URL — a real detached-copy exposure by
  `contentkey.go:14`'s own threat model. **File it as its own bug**; it is not
  this epic, and it must not be fixed by moving the control plane behind a
  key.

### 11. The epic's happy path

One e2e on the shipped backend (`cmd/nocx-server`):

1. A skill with several files opens in its own tab; each file in the left
   column opens and shows ITS OWN bytes.
2. `Check this skill` is pressed once; the verdict and the report appear.
3. The tab is closed and reopened — **the verdict is still there, and
   `skills.audit` did not go over the wire a second time.**
4. A file on disk is changed — the verdict is still there, and beside it
   stands the sentence that it is about an earlier version.
5. A builtin row offers no check at all.

Steps 3, 4 and 5 are what no unit can report.

## Testing

**Re-homed, not deleted.** The existing product contracts assert things about
the PRODUCT, not about a modal, and every one of them moves onto the tab:
opening spends nothing, the fallback role is disclosed, omissions are named, a
refusal is shown as a refusal, the switch stays synchronised, provenance and
source are stated, deletion works (`skills-management.spec.ts:287,326,347,383`;
`skills-section.test.tsx:1141,1174,1225,1240,1256`). A narrower new e2e that
let these regress would be the defect AGENTS.md's rule 2 is about.

**Go.**

- The check table's schema rung, under `schema_migrate.go`'s parity tests.
- A check written and read back; one whose digest no longer matches.
- The material digest is stable for the same bytes and differs for changed
  ones, over both an `FS` root and a `Dir` root.
- `Skill.Offered()` has no third term — asserted structurally, so a verdict
  can never enter it.
- The stored report is byte-for-byte what `skills.audit` returned — the
  storage path adds no cut of its own.
- Builtin: `skills.audit` on a builtin is refused.
- **Failure paths, one per external call:** the model refuses; the store is a
  stub; the store write fails; the document is unwritable; a file in the
  manifest cannot be read; the material cannot be composed.
- **Adversarial interleavings:** a toggle during a check (the toggle
  survives); two checks of different skills (both survive); the skill deleted
  mid-check (the result is discarded); reinstalled mid-check (discarded).
- **And the paired success:** for every "returns an error when…", the
  "and on an ordinary machine it succeeds".

**Frontend.** The two-group column; the check selected by default when one
exists and the first file when none does; the row's third evidence line in all
three states; the model's prose rendered inert (no HTML, no live links); the
scan dots sourced from the live scan; the tab closing when its skill is
deleted.

**Contracts.** Three schemas, over the wire.

## Open questions

None. Owner decisions of 2026-09-05: the check yields a verdict; checks do not
go in a backup; nothing is written into the skill's folder; builtin is not
checked; the metadata store is a table in `content.db` rather than a database
of its own.
