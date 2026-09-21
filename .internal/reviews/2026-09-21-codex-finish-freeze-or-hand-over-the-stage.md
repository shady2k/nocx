# Stage fate consultation

## Decision

Do **not** finish or partially merge this branch. Use a fourth option: retire it as an
integration candidate, keep it as a read-only donor/reference, perform the xterm removal first,
then build the interval record on that foundation. Transfer the stage's outcome and acceptance
evidence, not its `CaptureParams` representation. Land the log-ratchet defect separately because
it is the only finding here that is both representation-independent and useful immediately.

If constrained to the three offered choices, option 2 is the least wrong code decision. Option 3
has the right instinct about transferring the outcome, but its proposed code transfer is not
honest: most of the named packages contain the half-feature, not reusable cell-model foundations.

> 🧠 **From Hindsight memory (Key decisions and rationale)** — ADR-0066 is the durable
> backend-owned-terminal direction: the backend owns terminal state and the frontend paints cells
> and sends intent. I used that only as orientation and verified it against ADR-0066, the design,
> the branch, and the issue export.

## Measurements and factual corrections

The file and line totals are correct. These commands reproduce them:

```sh
git diff --shortstat origin/main...HEAD
# 80 files changed, 7327 insertions(+), 1140 deletions(-)

git diff --numstat origin/main...HEAD | awk '...group by first two path components...'
# contracts +172/-1; frontend/src +233/-978
# internal/app +2002/-7; internal/captureview +507/-0
# internal/content +708/-81; internal/emulator +994/-31
# internal/helper +869/-9; internal/outputcap +21/-0
# internal/sessionruntime +926/-1; internal/transport +859/-12
```

The named frontend deletions also reproduce exactly: `capture-client.ts` -173,
`scrollback/sgr.ts` -171, `scrollback/blocks.ts` +1/-31, and
`scrollback/serializer.ts` +2/-65, plus -367 lines in their four named tests.

Two repository-state claims do not match this checkout:

- `git rev-list --count origin/main..HEAD` returns **48**, not 44. Excluding merge commits,
  `git rev-list --count --no-merges origin/main..HEAD` returns **41**. The branch may have been at
  44 at an earlier snapshot, but 44 is not a current count.
- The tracked `.beads/issues.jsonl` says `.2.1` through `.2.7` are all **`in_progress`**, not
  `open`. The brief forbids `br`, so I did not query or mutate the live database; the claim may
  describe newer database state, but it is not verifiable from the tracked state in this branch.

Both roots were created on 2026-09-12, are priority 0, and carry `v0-5` in the tracked export.

The five record sizes are correct. I independently rebuilt the declared JSON shape with every
screen cell encoded as `{text,width,hasText,attrs,underline}`, two full screens, and the requested
departed rows. The exact byte lengths are:

| geometry | departed rows | JSON bytes |
| -------- | ------------: | ---------: |
| 80x24    |             0 |    240,672 |
| 80x24    |            48 |    480,957 |
| 120x40   |             0 |    599,266 |
| 120x40   |           100 |  1,347,863 |
| 200x50   |             0 |  1,244,986 |

The zero-row cases require Go's nil slice encoding (`"departed":null`); using `[]` makes each
two bytes smaller. `proto.MaxFrameBytes` is 1,048,576 at
`internal/helper/proto/frame.go:89-91`, and `Host.Ask` refuses an oversized reverse request at
`internal/helper/host/reverse.go:85-93`.

The three ceilings are indeed independent:

- helper frame: 1 MiB (`internal/helper/proto/frame.go:89-91`);
- content artifact: 1 MiB (`internal/content/ledger.go:1155-1165`);
- output retention: 256 KiB (`internal/outputcap/outputcap.go:18-21`).

The opening-screen observation is also correct. In production code, after the record crosses the
wire, `Opening` is read only to select `Cols` and `Rows` for an unfinished record
(`internal/app/helper_capture.go:268-275`). `internal/captureview/captureview.go:63-71`
deliberately constructs both views from `Departed + Closing` and excludes `Opening`. The branch
therefore pays for an entire opening rectangle that no stored or rendered view consumes.

The slowdown reproduces. Three targeted `-race` runs of
`TestAProgramThatFloodsAndDoesNotReadKeepsItsPaneAlive` measured:

| tree                          | runs (seconds)        |  mean |
| ----------------------------- | --------------------- | ----: |
| `origin/main` (`0cc4f0f2`)    | 1.037 / 1.041 / 1.034 | 1.037 |
| this branch, including `.2.7` | 1.304 / 1.336 / 1.298 | 1.313 |

That is a **26.6%** mean increase, consistent with the filed "about 27%". I ran only that
targeted test, not a repository gate.

## Finding-by-finding audit

The original three buckets are too coarse. Several findings have a durable requirement and a
current fix site with different fates.

| Finding                                             | Evidence                                                                                                                                                                                                                                                                                                                           | Correct fate                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.2.8` record exceeds one frame                     | The five sizes above cross the 1 MiB guard; the request is unchunked.                                                                                                                                                                                                                                                              | **Current defect dies; boundary requirement survives.** `CaptureParams`, its JSON cell spelling, and its reverse-request transport must not transfer. The replacement still needs a real-geometry size test across its own frame, storage, and retention ceilings. Calling the whole finding dead would repeat the omission that found it.                                                                                                                                                                       |
| `.2.9` restored card includes prior output          | `internal/captureview/captureview.go:63-71` appends every closing row after every departed row.                                                                                                                                                                                                                                    | **Requirement survives; fix site dies.** A card projection still needs an interval start/end rule. `internal/captureview` is the wrong place because that package exists solely to decode and render `CaptureParams`. The coordinator sorted this correctly.                                                                                                                                                                                                                                                     |
| `.2.10` refusal is unreachable                      | `helper_capture.go:276-287` stores the record as `application/json`; `recordCaptureRefusal` preserves that media type at `ledger_sqlite.go:1408-1423`; `restore-client.ts:367-401` selects only VT or text and otherwise reports ordinary absence.                                                                                 | **Requirement survives; both current endpoints die or change.** The replacement must expose suppressed/evicted/empty as different states in one model. The coordinator sorted this correctly.                                                                                                                                                                                                                                                                                                                    |
| `.2.11` stub says kept without storing              | `content/stub.go:354-358` returns `SessionOutputKept`; `ws_ledger_capture.go:177-198` turns that into `stored:true`. But that handler is explicitly the renderer-originated `ledger.capture`, which ADR-0066 retires. The new helper path avoids the stub by leaving its sink unwired (`helper_capture.go:159-167`, `247-253`).    | **Split it.** The false stub answer is a surviving content-abstraction defect if the new stance API transfers; the observable `ledger.capture` success dies with that method. “The ledger survives” is not enough to put the whole finding on the surviving side. Do not transfer the stance change now merely to preserve this bug; keep the invariant with the later record work.                                                                                                                              |
| `.2.12` three departure failures collapse to a bool | The producer distinguishes unmeasured depth (`terminal.go:441-454`), retention pruning (`:469-480`), row read failure (`:502-508`), and bounded-report overflow (`:510-552`), while `sessionruntime/capture.go:171-192` and `:219-235` keep only `err != nil`. `failDeparted` stores only the first error (`terminal.go:555-560`). | **Requirement survives; current consumer shape dies.** ADR-0066 design §6.3 explicitly says the capture record is different from the card wire/cell model and still requires rows that left the screen. Therefore the peer reviewer is right about the current `CaptureRecord.DepartedHole` sites: they are not cell-model foundation and should not transfer. The coordinator is right only that later interval capture must preserve diagnosable, counted loss. It is not true that “only a field name moves.” |
| `.2.13` missing context and blind ratchet           | `helper/session/capture.go:126-149` creates a context from `context.Background()` and logs through `hs.log`. The ratchet regex at `.githooks/check-log-context.mjs:60-62` accepts only an identifier receiver. Directly calling `violationsInText` produced no violation for `hs.log.Warn("a")` and one for `lg.Warn("b")`.        | **The proposed split is correct.** The four path-specific log calls and empty trace die with helper capture delivery. The selector-receiver blind spot is whole-tree tooling and survives; file it/fix it independently rather than making it wait for either feature.                                                                                                                                                                                                                                           |
| `nocx-190yr` 26.6% ingest regression                | `ghostty/terminal.go:393-399` now runs `noteDepartedLocked` after every VT write; `sessionruntime/runtime.go:1025-1089` adds a deferred flush, opening-screen reads, and unfinished-snapshot work to every ingest. `.2.7` bounded memory, not this work.                                                                           | **“Cause unknown” is honest; “unknown which side” is a dodge.** A profile is still needed to assign cost among candidates. But all candidates are capture-stage additions. Transfer the departure/capture machinery and the regression transfers; leave it for the post-cell-model record and xterm removal does not inherit it.                                                                                                                                                                                 |

So the coordinator put `.2.11` and `.2.12` too cleanly on the surviving side, put `.2.8` too
cleanly on the dying side, and treated `nocx-190yr` as less sortable than it is. `.2.9`, `.2.10`,
and the explicit split of `.2.13` are sound.

## Why not options 1–3 as written

### 1. Finish the stage

Reject it. The expensive fixes are concentrated exactly where the representation is wrong:
transport/chunking for spelled-out JSON cells, projection from `Opening/Departed/Closing`, the
refusal marker's media-type bridge, and the capture-specific helper trace. Finishing would make an
old representation acceptable immediately before replacing it.

The framing is loaded where it says this option "buys" already-built work and frontend deletions.
That is sunk-cost reasoning. Work already done is evidence about what the replacement must handle;
it is not a reason to merge the representation that produced the evidence.

### 2. Stop and freeze

Safer than finishing, but incomplete. A frozen branch alone lets acceptance criteria and findings
remain attached to a stage nobody will run. The branch should be retained as evidence, while the
requirements and finding ownership move immediately to the ordered future work.

Its divergence from `main` is not itself a product cost once it is no longer a merge candidate.
Calling that divergence a reason to merge overvalues a branch and undervalues the architecture.

### 3. Hand it to `nocx-zg3k3`

Transfer the outcome, but not in the form stated. The sentence “the record is the cell model from
the start” conflicts with the accepted design's §6.3, which explicitly distinguishes the card wire
format from the interval capture record. The passive live model answers “what cells exist at
revision R”; the record answers “what was observed during execution interval I, including rows no
longer in the live rectangle.” They can share cell vocabulary and revision identity without being
the same object.

Likewise, package names do not establish reuse. On this branch the changes in `internal/emulator`,
`internal/sessionruntime`, `internal/helper`, `internal/app`, `internal/content`, and
`internal/transport` are overwhelmingly capture plumbing. Merging those changes would activate a
26.6% hot-path regression and a record that cannot cross its wire at ordinary geometry, while the
user still lacks the accepted end-to-end behavior. That is a half-feature.

## Exact transfer

Transfer these requirements and evidence into the post-xterm record work:

- one runtime-owned observation record per authenticated execution interval;
- unwatched and unfinished output remains readable;
- rows that leave the live rectangle are retained or their loss is explicit, counted, and
  diagnosable;
- card and searchable text are projections of one retained source;
- suppressed, summarized, evicted, empty, and complete are distinct states;
- a card excludes pre-interval screen content;
- real-geometry transport/storage/retention limits are tested together;
- the targeted ingest test carries a performance budget, not merely a timeout;
- one happy-path test starts from an unwatched real session and reads the resulting card through
  the product seam.

Use these implementations as design/test donors later, not as commits to merge now:

- `internal/emulator/ghostty`'s departure tests and the evidence about pruning, reflow, alternate
  screen, and bounded accumulation;
- `internal/sessionruntime`'s two fence arrival orders and unfinished-interval scenarios;
- `internal/content`'s retention/refusal-state tests;
- frontend restored-card wording and styling tests, rewritten against the new model.

The following must **not** go to `main` as part of this branch:

- `contracts/helper/session.capture*.json`;
- `internal/helper/proto/capture.go` and its `CaptureParams`/duplicate cell vocabulary;
- `internal/sessionruntime/capture.go` and capture-only state/hooks in `runtime.go`;
- `internal/helper/session/capture.go` and its reverse ask;
- `internal/app/helper_capture.go` and capture bindings;
- all of `internal/captureview`;
- capture-derived-view storage/read plumbing in `internal/content` and `internal/transport`;
- `internal/outputcap`'s capture-driven coupling and the departure/peek additions in
  `internal/emulator` unless the later record design independently chooses them;
- capture-specific contract and generated-type changes;
- restore changes whose only producer is the JSON record.

The one immediately transferable item is not product code from this branch: fix
`.githooks/check-log-context.mjs` so selector receivers are scanned, with a fixture proving
`hs.log.Warn` is caught. That work should be a standalone tooling change.

## What `main` loses by delaying the frontend deletions

Nothing user-visible. On `main`, `capture-client.ts` and the SGR/text serializers are still the
only path that sends a frozen command body (`terminal-content.ts` calls `captureBlock` after the
history acknowledgement). Deleting them without the branch's replacement would remove output
history, not clean it up.

`nocx-zg3k3` will delete the same obsolete code during its coordinated cutover. It does not need to
rediscover that the code is obsolete; this diff and its tests are a precise deletion inventory.
But the deletion itself should be redone on the then-current tree because adjacent render,
selection, card, and restore code will already have changed. The only cost is small mechanical
conflict/deletion work; landing it now would buy negative product value.

## The fourth option and its risk

The missing option is **retire, re-home, then rebuild**:

1. declare stage 2 unaccepted and this branch non-mergeable;
2. preserve it as a donor/reference rather than a frozen future merge;
3. split and re-home every finding according to the table above;
4. land only the independent ratchet correction;
5. complete the xterm-removal/cell-model work;
6. start a fresh record stage on that model, importing tests, measurements, and algorithms only
   where the new design still needs them.

This also needs a documentation follow-up before implementation: the owner-selected order is now
xterm removal then record, while ADR-0066/design §9 still says live display, cards, and interaction
cut over together and that step 6 cannot precede step 7. The order is not being reopened here, but
the binding record must stop telling the next worker the opposite order.

The biggest risk is **requirement loss**: “abandon the representation” can be misread as “abandon
the unwatched-output promise,” especially for unfinished intervals, diagnosable holes, and the
end-to-end happy path. Re-homing those acceptance criteria and findings before anyone starts the
cell-model implementation is the control.

I would change this recommendation only if a proposed extraction proves all of the following at
once: it lands as an independently user-visible vertical slice; introduces no second cell
representation; is directly consumed by the accepted cell model; fits real geometry across all
three ceilings; passes the unwatched-output happy path; and does not retain the measured ingest
regression without an accepted cost. No package-sized subset of the current branch meets that bar.
