# busy-terminal — A busy terminal does not slow nocx

Charter, approved by the owner on 2026-10-29 (`nocx-mm3w6`). Status is never kept here —
ask the tracker: `scripts/feature-status.sh nocx-3an1s`, `nocx-atpw5`, `nocx-vh2hr`.

## What this milestone is, under trunk-based development

The milestone is named for its outcome and not for a version, by the owner's decision.
ADR-0097's pipeline builds every merge to `main` as the next nightly, and a stable release
is a promotion of a nightly the owner has tried. So no release waits for this milestone:
each pull request it merges reaches people with the next promotion, whether that is
0.8.1 or 0.8.4. The charter names what the work of these days is for, and what the first
promotion after v0.8.0 has to prove.

## What ships

Heavy terminal output costs less, and a tab running a busy program no longer holds up
every other tab. Contributors' seven performance pull requests and one cross-tab stall fix
are taken in, each reviewed against the architecture and this project's rules, repaired by
us where it falls short, and merged. CI stops reddening correct pull requests, so that
work is judged on its own merits. And the first promotion after v0.8.0 shows a stable
install updating itself to the next stable.

## Outcomes

- **O1: A busy terminal does not slow nocx: heavy output costs less, and one busy tab
  does not hold up the others** (`nocx-3an1s`, stage `nocx-3an1s.1`). The work: block
  row eviction in one pass (#584, `nocx-n275f`), screen assembly without a clone (#583,
  `nocx-ynziq`), no unused alternate-screen snapshot (#582, `nocx-vekk3`), no live
  painting in hidden tabs (#577, `nocx-4y8mb`), departed history read a row at a time
  (#576, `nocx-zg3k3.12`), row positions decoded once (#575, `nocx-3zuaw`), no unused
  text extraction on publication (#574, `nocx-aknvn`), and busy tabs kept independent
  (#534, `nocx-5q0ej`). Two colleagues' reports of the same kind are taken in by the owner (2026-10-29):
  a pane that refuses an action as busy with nothing visible holding it (GitHub #539,
  `nocx-3an1s.1.1`), and Control plane busy after a failed ssh session with invisible input
  on the next connection (GitHub #538, `nocx-3an1s.1.2`). One finding is taken in under the
  same stage: the agent wrapper breaking `claude update` (GitHub #541, `nocx-3an1s.1.3`),
  finding 1 of 5.
  **Check:** the guards these pull requests add are on `main` and green — the ingestion
  allocation budget at the largest measured sample plus 25 percent, and the over-the-wire
  isolation regression, which fails on `092fa5f38` because tab B's intent and resize get
  no answer while tab A is held, and passes after the fix.
  **Success signal:** with sixteen busy sessions open, the visible tab answers like it
  does with one; #534's author measured a command in tab B at 7484 ms before the fix and
  93 ms after, while tab A was flooding.
  **Condition of acceptance (owner):** every pull request of this milestone is reviewed
  against `docs/architecture.md`, the ADRs and AGENTS.md before it merges; what falls
  short is repaired by us, on its branch, and then merged.
  `Serves: a Ghostty-grade engine that handles modern agent TUIs (vision §1, §4); daily-driving nocx without falling back (vision §9).`

- **O2: CI is green on the first run: a correct pull request merges without a rerun**
  (`nocx-atpw5`, stage `nocx-atpw5.1`). The CI-hygiene leaves detached from the closed
  v0-8 stage (`nocx-1ebrs.66.1.25`–`.30`, `.32`–`.34`, `.36`, `.37`) and `nocx-6o5xe`,
  several already in pull requests (#579, #580, #585, #587).
  **Check:** a pull request whose only red e2e test is held by
  `docs/testing/quarantine.json` under an open bead passes CI and reports it as
  quarantined (`.36`), and O1's pull requests reach green CI without a rerun.
  **Success signal:** no pull request of this milestone needed a rerun to merge.
  `Serves: daily-driving nocx without falling back (vision §9), through the quality gates every commit owes (AGENTS.md).`

- **O3: An installed stable nocx updates itself to the next stable, on Linux and on
  macOS** (`nocx-vh2hr`, stage `nocx-vh2hr.1`). v0.8.0 is the first stable on the
  trunk-based pipeline, so the first promotion after it is the first chance to see this.
  **Check:** on Linux, the release's proof step runs the installed v0.8.0 AppImage's own
  updater and sees it verify, swap and report healthy on the next promoted stable
  (`nocx-86e2f`); on macOS, the owner's pass on a clean Mac (`nocx-1ebrs.4`) sees a stable
  install update itself to the next stable from inside the app, and a nightly install to
  the next nightly (`nocx-1ebrs.35.5`, #550).
  **Success signal:** a person on stable receives the next stable without reinstalling.
  `Serves: local updates users can verify and control (vision §8).`

## Out

- Output lifetime and same-session recovery (#540), universal terminal scrolling (#507),
  on-demand MCP tools (#474) and the Russian interface (#553). Each can merge and ship in
  any nightly through the pipeline; no promotion waits for them. #540, #507 and #474 have
  been open longer than the two days a branch may live here, so when they are taken up
  they are cut into smaller pull requests rather than merged whole.
- The agent's pager wait (GitHub #536) and its asking for a command already visible in a
  block (GitHub #537): assistant behaviour, a later feature.
- The bulk terminal data carrier (`nocx-pdju7`) and the herdr replacement (`nocx-xn63t`).
- Native fish's leftover work (`nocx-c8esn`, `nocx-sd8uk.1.*`), the remote runner
  (`nocx-360js.2`), the model-request retry (`nocx-wujed`), the api-import dialog flake
  (`nocx-1ebrs.47`) and the notices overlay (`nocx-cirda`): deferred with their labels.
- The CI flakes already deferred under v0-8 that no O1 pull request trips over.

## What v0-8 left, and what became of it (owner, 2026-10-29)

v0-8 closed with v0.8.0 (`nocx-jonuc`). Of what it left live:

- The CI-hygiene leaves and `nocx-6o5xe` carry over as O2; the stable-to-stable proof and
  the macOS passes carry over as O3.
- Work merged to `main` and left `implemented` by earlier milestones — 72 issues, under
  the herdr replacement, the brew tap and others — is closed on its main-line revision,
  each verified an ancestor of `origin/main`. The owner: merged work is not held open.
- `nocx-1ebrs.30` (release notes for 0.6.1) is closed as obsolete: 0.8.0 has shipped.
- Everything else live is deferred, its milestone label intact.

v0-8 took 17 findings against its budget of 10.

## Finding budget

**10**, raised from 5 by the owner on 2026-10-30 when the fifth finding (the agent TUI
pane flipping between views) arrived; the milestone had absorbed unplanned pipeline repairs (the nightly's
sign and proof steps, the tracker move) beside its five findings. It was **5**, set in `.githooks/backlog-gate/config.json`. The work is small and already
written; v0-8 took 17 findings against 10, mostly flakes, so a larger budget would let
flakes widen the milestone unnoticed. Reaching five means deferring or trading, decided
by the owner.

## Target date

None set by the owner. No date is invented here.

## After busy-terminal

Feature titles only, none planned: the owner's working day without herdr; bulk terminal
data on its own carrier; on-demand MCP tools; the Russian interface.
