# Native download hardening

Date: 2026-10-09. Change: PR #259, `nocx-9le.8.4`.
Status: implemented owner-requested review fixes; the checks below are implementation evidence, not stage acceptance.

## Requirements — Mary / John roles

Retain the existing download requirement: a remote regular file selected in Files lands byte-for-byte on this machine, or produces one honest failure/cancellation without replacing the destination before commit. The owner asked to repair the six runtime-reproduced review defects, not to add download features.

The observed failures are parent-directory substitution after Prepare, private destination permissions widened by replacement, shutdown returning before sink cleanup, Discard-before-Save reported as a source failure, a saved ACK accepted after the timeout path selected failure, and cancellation before GET waiting for a confirmation that cannot arrive.

## UX — Sally role

Keep the existing Files action, native save dialog and operations indicator. Dialog cancellation starts no server transfer. Fast cancellation reports cancelled, not source/destination failure. A cancellation after successful local promotion does not retract saved. Browser attachments and desktop-local eligibility do not change. No new widgets or retry flow.

## Architecture — Winston role

Reviewed AD-1 through AD-10, the module map, current main's ADRs and ADR-0011. This changes neither transport planes nor terminal/session ownership, SSH identity, credentials, replay or backpressure. File bytes stay on the existing loopback HTTP route; paths and native capabilities remain desktop-local. Wails remains a thin adapter. Inject the prepared local destination at the composition root rather than making the receiver import a concrete filesystem.

A selected destination is an open directory capability plus one filename, not a pathname to resolve later. On supported desktop platforms, Go's `os.OpenRoot` pins the directory across renames; all creation, promotion and removal use that root. Reuse the existing transfer sink and its bounded copy/sync/promotion/cleanup algorithm, with a rooted filesystem implementation, not a second transfer loop. An invalid or inaccessible parent is refused during Prepare, before the backend starts.

Native saves are private: create the temporary and final replacement with mode 0600. In particular, replacing a 0600 file must not make its bytes readable to group or others. This deliberately does not preserve public group/other access on an overwritten native destination. Ordinary local/remote upload permissions remain unchanged. Do not use a pathname-based chmod or a check-then-rename security boundary.

The receiver consumes an injected `Destination` with `Put(context.Context, int64, io.Reader) (transfer.Outcome, error)` and `Close() error`. `Config.PrepareDestination` constructs it from the picker result. The local provider exposes `PrepareDownload(path)` returning its rooted destination. Remove the obsolete path-only DownloadTarget and receiver-global Sink seam; migrate every caller and test. Destination lifetime ends on expiry, prepared cancellation, validation failure, Save completion or shutdown, and never before an active sink unwinds.

Each running save publishes a completion signal under the receiver mutex before execution. Shutdown prevents new work, cancels/interrupts streams, then joins all running saves outside the mutex, including saves already discarded. It must not join a non-cooperative OS dialog on the UI shutdown thread. A dialog returning after shutdown cannot publish a handle and must release any destination it opened.

Prepared cancellation keeps a bounded TTL tombstone in the existing handle map, releases its directory capability immediately, and makes a subsequent Save return cancelled. Unknown, expired and reused handles keep their existing failure semantics. Running cancellation interrupts the body once; the sink still owns cleanup and the final commit decision. Tombstones do not grow beyond the existing handle bound.

Native ACK acceptance and timeout settlement share the running-transfer mutex. Settlement uses the first accepted completion outcome even if the timeout select arm fired first; once timeout settlement wins, a new ACK is refused. Equal duplicates of an accepted ACK remain idempotent. There is one final transfer record.

If no writer/GET has been acquired, cancellation settles immediately: no desktop could have committed. After bytes could reach the receiver, retain the existing uncertainty policy for a missing completion ACK; do not turn late cancellation into a false claim that no file was saved.

The PR's unlanded native ADR is renumbered to 0083 because main already owns 0076–0082. Preserve all accepted main ADRs. This design uses BMAD's bounded requirements/UX/architecture/implementation/documentation roles; the local `_bmad` installation contains configuration only, so no unavailable executable BMAD agent is claimed as having run.

## Implementation and checks — Amelia role

Independent slices: rooted filesystem/privacy; backend native settlement; receiver lifecycle/frontend integration. The coordinator owns composition, docs and assembled verification. Tests and builds are run by the coordinator after assembly, not concurrently by workers.

Regression observations through existing interfaces:

- Prepare in a directory, rename it and replace its old name with a symlink: Save writes only into the pinned directory, leaving the symlink target untouched; cancellation cleans the pinned directory.
- With umask 022 and an existing 0600 destination, temporary and replacement remain private; normal upload behavior is unchanged.
- Hold sink cleanup after cancellation: Close cannot return while the temporary remains; after release it returns with no temporary. A blocked native picker does not block shutdown.
- Prepare, Discard, Save: cancelled, no GET and no destination mutation; unknown/expired/reused handles remain failures.
- Order timeout against an ACK with controlled clock/barriers: accepted saved implies terminal sent; a settled timeout refuses a new saved ACK. Same-outcome duplicates and conflicting duplicates retain their rules.
- Cancel before GET: immediate retained cancelled notification with no native-completion timer; late cancellation around a real commit retains the authoritative saved outcome.

Keep deterministic consumer-visible regressions, run affected Go tests with race detection, existing frontend/contract checks and the real SFTP native receiver path. Exercise native GTK save/cancel against disposable data, plus browser download. Then run the product's full gates, security checks and independent review on the assembled revision. Plant 2–3 bounded mutations in changed decisions and record the tests that detect them. Evidence names exact revision, commands, results and any unobserved surface; tests alone are not GUI proof.

## Documentation — Paige role

Append ADR-0084 without rewriting accepted decisions; fix the native ADR's numbering collision and its index/architecture references. Describe pinned destinations, private native files, joined shutdown, cancellation and ACK/timeout precedence. Keep wire schemas unchanged unless a real contract change is found. Reconcile incoming main through the documented lossless tracker sync, preserving database-only work, comments and audit history; publish the WHOLE canonical export as required by `docs/agents/backlog.md`, never splice per-issue rows from different comment-ID namespaces. Leave unrelated tasks and user files unchanged. Update PR #259 without merge. Tracker acceptance/closure waits for the owner's merge.

## Out

No resume, retries, download queue, directory downloads, credential/storage migration, alternate transfer engine, new public network endpoint, telemetry or renderer changes beyond this cancellation path. No weakening of security or quality gates to make the PR appear ready.

## Owner-approved security expansion — 2026-10-10

The owner chose to remove the critical frontend dependency findings in this MR. The authenticated npm audit reports four critical dependency records: seroval/SolidJS and tinypool/Vitest. These were already tracked in `nocx-l3pk`; do not duplicate the finding or claim that its broader root/high-severity criteria are fulfilled by this narrower repair.

Raise SolidJS's supported 1.x minimum to 1.9.17 and resolve a patched seroval. Move Vitest only to the minimum fixed 4.1.11 line, not the unrelated latest major 5. Node 24 and existing Vite 7 meet the documented requirements. Preserve environments, mock semantics where required by consumer-visible tests, suite selection and timeouts. Do not apply a blanket audit fix, add overrides/shims, or change unrelated direct dependencies. Verify the full frontend/type/lint/build suites, authenticated audit with zero critical findings, and the actual runtime surface after the update.

Vitest 4 requires mocked constructors to use ordinary functions or classes, not arrow implementations. Keep the existing command-output and reclaim/write-barrier behavior checks while migrating those constructor callbacks. Typed mocks use their owning port/session contracts and named result types; no concrete function's inferred `ReturnType` becomes a published contract.

## Owner-held integration prerequisite

The owner chose to keep the existing output-bounds repair `nocx-xn63t.6.21` (submitted in PR #267) separate, rather than importing its helper row-accounting changes into this download MR. A missing end marker at 200×50 still blocks the full CI gate; a narrower native/frontend pass is not stage acceptance. Merge readiness requires that prerequisite on main and green assembled checks, including the unresolved full-package app/content timeout evidence.

The gate also exposed two bounded test-fixture defects repaired here: serialize the output-bounds recorder's confirmation send and shutdown so teardown cannot race channel closure, and isolate the interactive-shell close test's HOME through `storagetest.IsolateWithHome`, registering cleanup before waiting for the prompt. Keep the original geometry, shell-exit assertions, deadlines and test selection. No helper-output, SQLite pool/VFS or timeout policy change is part of these repairs.

## Receipt prerequisite and fresh-main integration

The owner approved a separate upstream receipt-locator repair (`nocx-q8yjf.23`), now proposed in `shady2k/skills#28`. The canonical source CLI measures the real local OMP transcript from this project's dedicated non-Git workspace, established through its actual checkout. It refuses arbitrary children, foreign Git roots and historical missing-clone hints; unreadable identity is not absence. Neither `--unknown`, `--basis`, transcript edits, cache forks nor gate exemptions were used. Node 24's complete helper suite passed (147 tests plus 5 pi tests and selftests); an actual EACCES smoke and independent security follow-up cover the fail-closed boundary. Upstream acceptance/release remains pending.

The download branch integrates main `ee8137841`, preserving its critical outbound FIFO and lifecycle/recovery vocabulary. After integration, frontend typecheck, 7,214 tests plus 76 lint-fixture tests, schema drift checks and production build passed. The receiver race suite passed (1.406s), rooted local filesystem race suite passed (1.822s), and selected native/download, critical-capacity, saturated-outbound and lifecycle replay transport regressions passed under race (50.159s). An actual receiver/HTTP/rooted-filesystem smoke saved 2,258,480 matching bytes at 0600 after parent substitution, left the symlink victim unchanged, cancelled a discarded handle without another GET, and joined cleanup with no temporary files. The full transport package still failed output-bounds and reached its 10-minute deadline; selected passes do not replace that red result. Earlier GTK/browser smoke and three killed mutations apply to the implemented download repair, not a claimed all-platform or full-CI acceptance of fresh main. The automated document acceptance gate is not installed. Full CI, the separate helper prerequisite, unresolved full-package timeout evidence and native macOS proof remain explicit acceptance limits.
