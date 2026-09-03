# The wave record: one coordinator, one worker, and the backend is what watches

- **Date:** 2026-09-03
- **Bead:** `nocx-dkawo.2`, a child of `nocx-dkawo` (replace herdr)
- **Implements:** `.internal/specs/2026-08-24-orchestration-mechanism-design.md` §5, §6,
  `D1`, `D3`, `D9`, `D15`; `.internal/specs/2026-09-03-the-waves-authority-model-design.md`
  (`A1`, `A2`, `A8`, `A9`, `A12`); `.internal/specs/2026-09-03-spawn-and-register-the-interval-design.md`
  (§3, §4, §5, §6)
- **Owner review:** the owner delegated it in session on 2026-09-03 — "бери, делай, читать не буду".
  Both draft specs are therefore **approved**, with the two corrections in §0 below.

## 0. Two corrections the specs owe, found on review

Neither overturns a decision; both are stale citations that would mislead the next reader.

1. **`nocx-aqz7o` is CLOSED** (commit `182191e0`), so the authority design's §5 table row "the
   per-epoch capability … is written in cleartext on the outbound half of the descriptor" no
   longer describes the tree. The fix was a **deletion**: the bearer field never authenticated a
   peer, because the shell's own hello hands that peer the capability a frame earlier. `A12`
   survives unchanged and is if anything stronger — there is now _less_ candidate pin material,
   not more. The successor bead for the remaining exposure is **`nocx-zo2ng`** (a nested grant
   still carries the child's capability on the parent's descriptor), and that is what §5 and the
   spawn design's §7 should cite where they currently cite `nocx-aqz7o`.
2. **`A11` is unblocked.** `nocx-intbc` is closed (`b29b1990`) — `internal/agenttools/resourcekind.go`
   now measures the kind set against the ledger instead of restating it, so a row naming
   `ResourceWorkspace` assembles.

Both corrections are applied to the spec files in the first commit of this plan.

## 1. The reading this plan commits to, where the bead was ambiguous

The bead says: _"the backend records both its declaration and its process exit, and **neither
alone terminalizes it**."_ Taken unqualified that is unimplementable — a process that exits
without ever declaring must reach a terminal state or the record leaks forever. The reading, and
it is the one `D9` and the transfer precedent (`nocx-9le.5.7`: state comes from the result and its
`done`, never from a progress sample) support:

| Declaration | Process exit | State                  | Why                                                                                      |
| ----------- | ------------ | ---------------------- | ---------------------------------------------------------------------------------------- |
| —           | —            | `live`                 | supervised, nothing to judge                                                             |
| present     | —            | `live`                 | the agent said it finished and is still running. **A declaration alone is not terminal** |
| —           | present      | `abandoned`            | terminal, and deliberately NOT `completed`: nothing said what it produced                |
| present     | present      | `completed` / `failed` | the declaration's own verdict decides which. **This is the only path to `completed`**    |

So: **neither fact alone produces `completed`**, which is the claim the bead is making, and exit
alone still closes the record — under a name that says what is missing. `abandoned` is the
fail-closed direction: a coordinator reading it learns "gone, and it never told me", never "done".

## 2. Where each thing lives, and why not somewhere else

- **`internal/wave`** owns the SEMANTICS: the state machine of §1, the register-and-spawn ordering
  of the spawn design §4, the conjunction rule, the participant/membership split (`A1`). It
  imports no transport and no assistant package.
- **`internal/content`** owns the DURABLE ROWS, because ADR-0043 says one connection to the
  encrypted store and AD-8 says one owner per behaviour. `wave` depends on a narrow store
  interface; `content` satisfies it. New tables ride the ladder (ADR-0055): `schemaVersion`
  16 → 17.
- **`internal/app`** is the composition root and already holds the enrolment seam
  (`paneenrol.go`, wired at `app.go:1349`), which is spawn-design step 4's arrival point.
- **`internal/lifecycle`** carries the participant's declaration, because `KindAgentEnrol` /
  `KindAgentWithdraw` already ride that channel and ADR-0024 decision 2 makes it the authenticated
  one. A second carrier would be a second authenticator for one trust decision.

## 3. Slices, each landing wired

The deadcode ratchet is the hook, not the brief (`nocx-z7s6`): every slice below lands its
package together with the wiring that makes it reachable from `main()`.

### S1 — the record, its store, and the register interval

Red first: `TestFaultAtEveryBoundaryConverges` over the register procedure, table **discovered**
from a recording run (the shape at `internal/shellintegration/publisher_fault_test.go:244`), every
assertion read through a freshly reopened store.

- `wave.Participant`, `wave.State`, `wave.Liveness` (backend instance + session id + epoch +
  domain + attempt + output offset — §6's full identity, because a bare attempt attaches old
  evidence to a new incarnation).
- `wave.Registrar.Register` implementing the spawn design §4 steps 1–6 **in that order**, where
  the order IS the rollback: reserve → commit `prepared` → create session/spawn → enrolment →
  delegation → `live` **then** attach supervision.
- `content` gains `waves`, `wave_participants`, `wave_delegations`; ladder step 17; the
  `closeUnanchoredEntries` sweep extended to terminalize non-terminal participants as
  `interrupted` on `Open` (spawn design C6).
- Wiring: constructed in `app.go` beside `paneGrid`.

### S2 — supervision: two facts, and neither alone

- Process exit from `session.Session.Done()` + `ExitOutcome()` — the fact nocx owns because it
  owns the PTY. Never from the screen.
- The declaration: a new lifecycle event pair `agent_report` / `agent_reported`, carrying the
  participant's own terminal verdict and what it produced, routed through `paneEnroller`'s path.
- The §1 table as the state machine, with the ordering guarantee of step 6: supervision attaches
  to a record that already exists, so a process exiting between the two is still observed.
- Assertion: a declaration with no exit stays `live`; an exit with no declaration reads
  `abandoned`; both read `completed`.

### S3 — `D3`: a fresh coordinator asks what its session holds

- `*WaveCoordinator` and `*WaveParticipant` as two capability types (`A8`), never one with a role
  flag.
- The holdings call takes **no participant parameter** (`A9`) — asserted against
  `contracts/tools/`, the way `session.run` already is.
- `A12` asserted: minting a participant capability from a fenced grant cannot exceed the fence.

### S4 — spawn is the `delegate` effect, not an eighth one (`A6`)

- A `Declaration` row with `Effect: []content.Effect{content.EffectDelegate}` and
  `ResourceKinds: []content.ResourceKind{content.ResourceEnvironment}`.
- A `Narrow` returning a spawner holding only the granted environments.
- **The environment scope minted into the run fence** (`ws_readscreen.go:294-302`), without which
  `Registry.ForGrant`'s kind check silently omits the tool and it is never offered.
- Out of scope by decision: escalate-versus-refuse for a spawn outside the fence. It refuses, and
  the refusal names the environment.

### S5 — the epic's happy path, watched end to end

`cmd/nocx-server` headless — the SHIPPED coordinator, not a harness beside it. One coordinator
pane starts one worker, gives it a task, goes idle; the wave stays supervised with no alarm; the
worker declares and exits; killing the coordinator between turns loses nothing and a fresh one is
told by name what its session holds.

## 4. Deliberately out of this bead

- **Fan-out, the routing table and the queue** — `nocx-dkawo.4`, and `D15` puts it there on
  purpose.
- **Waking the coordinator** — `nocx-dkawo.3`, which this blocks.
- **The mailbox and its four acknowledgements** (`D7`, `D8`) — they need more than one worker to
  be worth anything.
- **`delegate-further` and transitive revocation** (`A3`).
- **Adoption of a found process** — refused while no pin exists (`A12`, spawn design §5).
