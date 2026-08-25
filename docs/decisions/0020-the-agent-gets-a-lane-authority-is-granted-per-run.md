# ADR-0020 — The agent gets a lane, and authority is granted per run

- **Status:** Proposed
- **Date:** 2026-08-02 (§7 amended 2026-08-16, accepted)
- **Related:** [ADR-0019](0019-one-authoritative-ledger-disposable-projections.md) (the
  ledger it writes to), [ADR-0004](0004-input-ownership-and-editor-abstraction.md) (who owns
  the line), [ADR-0006](0006-marker-only-prompt-mode.md) (marker-only shell integration —
  why we can only partly see a nested environment), AD-1 (binary data plane + JSON-RPC
  control plane), AD-6 (the backend never sniffs the byte stream).
- **Design:** `.internal/specs/2026-07-31-command-blocks-history-syntax-design.md` §3.1,
  §3.3, §4, §10.2.
- **Consulted:** an adversarial review (codex, 2026-08-02) that supplied the supervised-lane
  primitive, the attempts table, and the workspace/scope/grant separation.

## Context

The product is about to gain an agent that acts _inside the block flow_ rather than in a
side panel — the owner's framing: "I am doing something, I ask the AI right there, and it
offers what to run, or a task starts." Three questions follow immediately, and none of them
are UI questions.

**What does the agent do while the shell is busy?** The owner's first instinct was that it
waits until the shell is free. That is a real option and it is wrong: a full-screen editor
owning the alternate screen is not a transient state, and an agent that is blocked by it is
useless exactly when a person most wants help.

**What stops an agent wedging itself forever** by launching something interactive? The
comparison worth knowing: Claude Code never shares the user's terminal. It runs each command
in its own shell with no interactive TTY and a hard timeout (120 s by default, 600 s
maximum), and refuses interactive flags outright — so a full-screen program either exits
complaining it is not writing to a terminal, or hangs until the timeout kills it. That is
safe and it buys the safety by making the agent's work invisible in the terminal the human
is looking at, and by making a TUI simply impossible. We want the opposite visibility, so we
cannot buy safety the same way.

**What is the boundary of what an agent may touch?** The owner's instinct: a tab group is a
limit, and there is no reach outside it.

## Decision

**1. The agent never takes the user's PTY, and never waits for it.** It gets a **lane**: a
persistent, PTY-backed session attached to the same `environment`, owned by the workspace,
and projected into the block flow so its work is visible where the work is. Two writers on
one PTY is not collaboration, it is a race over one state machine; and a lane means "the
shell is busy" is not a state the agent can be blocked by.

The lane is persistent and its _executions_ are disposable. One fresh session per command
would throw away shell continuity and pay connection setup every time; a lane is reset or
replaced when its state becomes suspect. Deliberate parallelism gets more lanes.

**2. Every execution runs under a lease.** A lease carries: a wall-clock deadline
(renewable, with a bounded ceiling), a **separate inactivity deadline** — silence is a
different failure from slowness — an output budget, a terminal-size contract, an explicit
interactivity policy, and cancellation that escalates `INT → TERM → KILL`. Each execution
runs in its own process group so cancellation reaches the children.

**3. Interactivity is a protocol transition, not a failure.** When a program activates the
alternate screen or blocks reading stdin, the lane enters **`awaiting-takeover`**: the agent
is **demoted, not evicted** — it loses write authority and keeps the right to read the
screen and advise — and the human takes over, answers with a bounded response, detaches the
process, or kills it. This is the answer to "the agent could get stuck forever", and it is
better than a timeout in the case that matters — the program is still there and still
usable, rather than killed for the crime of being interactive.

Demotion rather than eviction is deliberate and is where the next feature lives: the
frontend already holds the screen (AD-6 puts render state there), so the state in which a
TUI owns the lane is exactly the state in which an assistant can explain what is on screen
and what the choices are. Anything beyond advice — sending keystrokes — is governed by
rule 6 and by the staleness rule that a separate decision will own (`nocx-x8s2`).

**4. Attempts are first class.** An `executions` table sits between an entry and its
artifacts: `entry_id`, lane, attempt number, lease bounds, interactivity policy, process
group, start/end, **termination reason**, executor identity. Artifacts attach to an
execution, not to the intent.

Two reasons, both load-bearing. A rerun, a retry after approval, a takeover and an
infrastructure failure are _not new intents_, and modelling them as new entries destroys the
one thing recall is for. And a single `status` plus `exit_code` cannot distinguish **the
command failed**, **the executor timed out**, **the transport vanished**, **the user killed
it** and **the agent declined to proceed** — five outcomes with five different correct
responses.

**5. Authority is granted per run; a container never confers it.** Three things that the
first draft of this design collapsed into one:

- **workspace** — narrative and presentation scope: which sessions read as one story.
- **resource scope** — what exists to be touched: environments, sessions, paths,
  credentials, network destinations, tools.
- **authority grant** — a versioned, expiring capability issued to one agent turn or one
  execution, **immutable once execution starts**, and recorded on the run.

The workspace _mints_ the default grant from its policy; it is not the enforcement object.
The reason is mundane and decisive: workspaces get reorganised by dragging things around,
and dragging a tab must never silently confer or revoke the right to write to production.
The blast radius of a task stays a query — "this run held a grant for these environments and
touched these three sessions on two hosts" — because the grant is on the run.

**6. Autonomy is a policy evaluation, not a toggle.** The decision to act, ask, or refuse is
a function of:

```
effect × environment criticality × classification confidence × reversibility
      × scope expansion × provenance
```

`effect` needs more texture than read/write:
`observe | mutate-reversible | mutate-destructive | privilege-change | disclose |
cross-boundary | delegate`. Reading is not harmless when what is read is a secret and where
it goes is a model provider; writing is harmless when it creates a temporary file.

Two rules that a simpler model gets wrong:

- **Low confidence escalates on its own.** An opaque script, a shell expansion, a
  `curl … | sh`, an indirect tool invocation — escalate even when the apparent effect is
  low, because the apparent effect is the thing we cannot see.
- **Scope expansion invalidates prior approval.** Approval to modify one repository is not
  approval to follow an SSH hop discovered halfway through.

**7. What ships first.** The lane, the lease, the takeover transition, the `executions`
table, and a grant recorded on every run — with **three presets** on top of the policy
function: ask every time; ask on anything that mutates; autonomous within this workspace's
`routine` environments. The full effect lattice is the enum we grow into, not a policy
engine we build now. A policy engine nobody has needed yet is how this decision would turn
into a research project.

## Rationale

The lane is what makes "the AI works in my terminal" and "the AI cannot wedge my terminal"
both true. Claude Code achieves the second by giving up the first; Warp keeps agent commands
inside the conversation, which achieves neither visibility in the block flow nor a stated
answer to interactivity.

`awaiting-takeover` is the piece we would not have got to alone. The instinct is to forbid
interactive programs; the better move is to let the agent start one and hand it to the human
at the moment it becomes interactive, because that is also the moment the human learns
something.

Per-run grants matter for a reason that has nothing to do with security theatre: without
them, "what was this allowed to do" is reconstructed from what it _did_, which is exactly
backwards, and unanswerable after the fact.

## Consequences

- More shells, and more SSH connections, than a design where the agent borrows the user's.
- The agent's shell state is _not_ the user's — different exported variables, different
  history, a different `cd`. This is deliberate and must be visible in the UI, or the first
  surprise is an agent that "cannot see" a variable the human just exported.
- Process supervision (groups, signals, deadlines) must work on macOS, Linux and Windows.
- The UI must render concurrent blocks from two authors in one flow without implying that
  adjacency means causation (ADR-0019 §2).
- `phase = open | bound | closed` stays small and stays on the entry; approvals, retries,
  takeover, cancellation and timeouts live on the execution.

## Amendment (accepted 2026-08-16) — §7: the three presets become a matrix

**Accepted by the owner, 2026-08-16.** It amends §7; everything above it stands.
The owner declined the three-preset flip that day — _"нужна более гибкая политика.
Нужны scope. Нужна гибкая настройка."_ — and, on being shown the matrix, settled
where the boundary is enforced: _"по capability нужно."_

That last sentence is the half a policy document usually leaves out, so it is
written into the decision rather than left to the implementation. **The matrix
decides; the narrowed capability enforces.** A row's scopes are not a predicate
consulted before the call — they are the bound of the object the tool is handed,
so a tool cannot reach outside its row because it never holds more than the row
allows. A check would leave the tool holding a full manager and would rot at the
first refactor; the capability cannot.

### What changed

§7 shipped "three presets on top of the policy function" — `ask-every-time`,
`ask-on-mutate`, `autonomous` — and deferred the full effect lattice as "the enum we
grow into, not a policy engine we build now". That deferral is revoked. The policy a
workspace (and, until a workspace exists, the product as a whole) configures is a
**matrix**:

- One row per effect class of decision 6 (`observe | mutate-reversible |
mutate-destructive | privilege-change | disclose | cross-boundary | delegate`).
- Each row carries exactly one of `permit | ask | refuse` — never fewer, never a
  second decision for the same effect, so the matrix evaluates without a conflict
  resolver and a person sees what is permitted without simulating rules.
- Each row carries the **resource scopes** (paths, sessions — the ledger's closed
  `ResourceKind` set) the decision applies within. A row with no stated scope
  applies within the grant's own bound (the run's session at mint); a
  resource named outside the row's scopes is refused, never silently re-scoped.

The three presets remain expressible in this form: `ask-every-time` is every row
`ask`, `ask-on-mutate` is `observe → permit` and the rest `ask`, `autonomous` is
every row `permit`. A run under each behaves exactly as it did — the matrix is the
preset generalized, and a preset is its rows: there is no preset vocabulary on the
wire or in the store, only the matrix. The equivalence is pinned by the tests, so
"the presets still exist" stays true without a second representation to drift.

Decision **7 is amended accordingly**: what ships first now includes
this matrix as the shape of the policy, with the global default policy minting the
run grant and a workspace-level override arriving with the workspace grant-source
bead `nocx-mp2vd` (the resolution order is stated once, in the resolver: workspace
overrides global; global is the default).

### Why the deferral no longer holds

The deferral's own condition is met: "a policy engine nobody has needed yet" —
somebody needs it, and said so in terms the three presets cannot express (per-effect
and per-scope configuration). And the shape chosen is deliberately NOT the research
project the deferral feared: a fixed seven-row matrix over a closed effect enum is a
table, not a rule language; it adds no conflict resolution, no precedence, and no
new vocabulary — the scopes and effects it composes are the grant's existing
vocabulary (ADR-0028: the grant is over resources and effects).

### What is still deliberately out

- **Rules over tool names.** The matrix is over effects and resources only; a
  configuration that permits `readScreen` rather than `observe within these
sessions` re-introduces the `--no-tools` mistake at the settings layer
  (ADR-0028 decision 4) and is rejected by construction.
- A dynamic rule language, precedence, or any conflict-resolution machinery.
- A migration for the stored grant shape: greenfield, the store rebuilds on a
  shape change.
- The workspace as a second grant source until nocx-mp2vd lands; the global
  policy is the one source now, and mp2vd overrides it rather than supplementing it.
- The approval UI, retention and sensitivity of AI dialogues — already listed under
  "Not decided here"; this change does not move them.

## Alternatives considered

**The agent waits for the shell.** Rejected: a full-screen program is not a transient state,
and the wait is unbounded exactly when help is wanted.

**The agent shares the user's PTY.** Rejected: two writers, one state machine; and every
interactive program becomes a fight over who owns the keyboard.

**A sandboxed non-interactive executor (Claude Code's shape).** Rejected as the _only_ mode
— it makes a TUI impossible and the work invisible — but kept as a lane _policy_
(`interactivity: none`) for the cases where that is exactly what is wanted.

**Workspace as the security principal.** Rejected: membership changes by drag and drop,
environments are shared, and organisation must not confer authority.

## Not decided here

AI dialogues as a data class — retention, sensitivity as a propagating taint rather than a
flag, and whether they need their own key so that "delete my AI history" can be a real
erasure rather than a `DELETE`. Multi-agent lanes and their arbitration. Remote or cloud
execution. The concrete approval UI.

## Revisit when

An agent needs to act across two workspaces at once — at which point the grant is already
the right object to widen, and the workspace is already not the thing being widened.
