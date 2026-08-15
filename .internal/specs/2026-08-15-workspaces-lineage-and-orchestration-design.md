# Workspaces, lineage, and orchestration as a view over sessions

- **Date:** 2026-08-15
- **Beads:** `nocx-kewo` (this session), `nocx-ms7v.4` (the attention queue — **filed, not
  built**), `nocx-jv3q` (the tab-strip epic), `nocx-if6` (the phase-A argument it reuses),
  `nocx-457v` (the helper whose reservations it spends), `nocx-dw3` / `nocx-x8s2` (the agent),
  `nocx-hz94` / `nocx-jiwq` (notification delivery — filed, not built)
- **Amends:** `docs/vision.md` §11 (one factual claim), ADR-0020 (§5 gains a clause),
  `.internal/specs/2026-08-06-git-manager-design.md` line 251 (an exclusion loses its reason)
- **Proposes** (owner's decision, not this document's): one narrow exception to AGENTS.md's
  "Clean-only: no backward-compatibility shims" rule — see §9.4
- **Status:** design, second draft. Drafted 2026-08-15 from a working session with the owner,
  then revised after an adversarial review (codex, same day) that found four defects in the
  first draft, two of them authorial errors. **Not yet approved.** Open questions are marked
  as open rather than resolved by the author.
- **Review note.** The first draft asserted that the attention queue "already exists" and
  cited ADR-0029 as the notification core. Both were false — see §2's footnoted rows. They are
  recorded rather than quietly fixed because the first is precisely the failure AGENTS.md
  names: _a file on `main` is not a feature in the product_, and a filed bead is less than a
  file.

## 1. In one sentence

nocx gains **workspaces** — user-created, host-agnostic groups spanning several environments —
and **session lineage** — an immutable parent edge that records who created whom; on top of
them, authority is carried by an explicit human-approved **binding** and realised per run,
never by membership or ancestry. Together this makes running several agents across several
machines a _view over sessions_ rather than a second product, and it does so without nocx
becoming agent-first, because nothing in it requires an agent to be present.

**What a user can do that they cannot today:** create a workspace for one piece of work, open
sessions in it on the laptop and on two servers, start an agent in one of them, let that agent
open child sessions of its own, and — coming back from lunch — see which finished, which
failed, and which is waiting on a human. And an agent in one workspace cannot see, address or
disturb anything in another.

## 2. What this design crosses, and what those documents already decided

AGENTS.md requires a brief that crosses a boundary to name the `AD`s and ADRs it touches and
what they already decided, **before** it says what to build. Rows marked † are corrections to
the first draft of this document.

| Boundary                                           | What it already decides                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | What this design does about it                                                                                                                                                                                                                                                                                                      |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6**, byte-blindness                           | The backend never sniffs the byte stream; the renderer owns render state and parses OSC                                                                                                                                                                                                                                                                                                                                                                                                                 | **Unchanged.** Agent state derived from the terminal title or the screen is derived **frontend-side** and crosses the control plane as a typed fact, which AD-1's 2026-08-02 amendment already permits. The backend never receives the stream it was derived from                                                                   |
| **AD-7**, session model                            | One PTY/channel per tab; the backend `session` module is the authoritative registry; session-id is **server-authoritative**                                                                                                                                                                                                                                                                                                                                                                             | **Extended.** Parent identity, a session epoch and `workspaceId` join `sessionId` on the same registry, and session creation gains a durable prepared→active transaction (§4.2)                                                                                                                                                     |
| **AD-8**, interface-first + DI                     | Variation is expressed by the interface, never by a fork inside an implementation                                                                                                                                                                                                                                                                                                                                                                                                                       | Binds §6: the built-in agent and an enrolled external CLI reach **one** dispatcher, differing only in how their grant was sourced — not two control surfaces with a `switch` between them                                                                                                                                           |
| **AD-9**, replay ownership                         | Bounded per-session output ring keyed by byte offset; the frontend acks                                                                                                                                                                                                                                                                                                                                                                                                                                 | Two consequences: it is why tmux/dtach is rejected in D4, and its **offsets are what §5 binds evidence to**                                                                                                                                                                                                                         |
| **AD-1**, the wire                                 | Binary data plane, JSON-RPC control plane; ledger **facts** may cross, raw bytes may not                                                                                                                                                                                                                                                                                                                                                                                                                | Agent-state observations cross as typed JSON-RPC records with provenance, like `history.record`. No new plane                                                                                                                                                                                                                       |
| **ADR-0020**, the lane and the grant               | §5: workspace, resource scope and authority grant are three things; the workspace **mints the default grant from its policy** and is **not the enforcement object**; "Workspace as the security principal" is rejected for **three** reasons — membership is draggable, environments are shared, organisation must not confer authority. §3: takeover **demotes, does not evict**. §4: attempts are first class. §6: the effect lattice, incl. `delegate`; "scope expansion invalidates prior approval" | **Honoured throughout, and amended in one place.** The grant stays on the run. Lineage is added as an axis that answers _provenance only_; continuing authority is a separate, revocable object (D8). §9.2 records the clause ADR-0020 needs                                                                                        |
| **`internal/content/ledger.go`** †                 | Already implements ADR-0020: `GrantPolicy` "the autonomy preset **the workspace mints**" (:151), `StartExecution{Grant}` (:286), "`Grant` is the authority recorded **on a run**" (:319), `GrantScope` (:329), and `interrupted` as an assistant-run state after restart (:336)                                                                                                                                                                                                                         | **This is the model, not a thing to re-invent.** The first draft put an immutable grant on the _session_, contradicting both the ADR and this file. Corrected in D1. `interrupted` is also the precedent D7 follows rather than inventing a liveness vocabulary                                                                     |
| **ADR-0024**, the lifecycle leaves the byte stream | OSC 133 is an **anonymous broadcast channel**: "every process with that tty open can write it — a TUI, a `cat` of a hostile file, a remote host's MOTD". Hence an authenticated channel per session, with a lifecycle **domain**                                                                                                                                                                                                                                                                        | Binds §5 and §6 absolutely. A spawn or enrolment request may never ride the byte stream; the terminal **title** is the same anonymous class; and the lifecycle **domain** is the discriminator D8 uses for "did the authority context change"                                                                                       |
| **ADR-0028**, the agent loop is ours               | The grant is over **resources and effects, never tool names**; the dispatcher **narrows rather than checks** — the tool holds a scoped capability, so it cannot exceed the grant because it never holds more; Go package privacy is explicitly **not** the boundary                                                                                                                                                                                                                                     | Binds §6. The first draft claimed an environment bearer token was this narrowing; it is not (D13)                                                                                                                                                                                                                                   |
| **`frontend/src/agent-status.ts`** †               | **Already on `main`**: `AgentStatus = 'working' \| 'idle'`, derived from the title via OSC 0/2, with a header comment stating that "a coding agent runs as one long shell command, so OSC 133 … cannot see inside it", and deliberately keeping two facets apart — "evidence that _something_ is working is not proof of _who_ is working"                                                                                                                                                              | The honest current state, and the seed of §5. It is **two values with no provenance**, so §5 is new work built on a correct instinct, not reuse of an existing model                                                                                                                                                                |
| **`frontend/src/tabs.ts`** †                       | `_hasActivity` is **one boolean per tab** (:52), cleared by activating the tab (:170), and not set at all while the tab is active (:265)                                                                                                                                                                                                                                                                                                                                                                | The first draft called `nocx-ms7v.4` "the attention queue that already exists". It does not exist. Three workers — one finished, one asking, one failed — collapse to one dot, which activation erases. A durable, typed, individually acknowledged queue is **new work**, and this design depends on it rather than standing on it |
| **notification core** †                            | `nocx-ng6f` (event, trust, default-deny router), `nocx-9zmc` (`notify.raise`, backend-stamped provenance) and `nocx-hz94` ("a heuristic event never reaches a target") are **filed beads**, not shipped code                                                                                                                                                                                                                                                                                            | Same correction. Also: the first draft cited "ADR-0029" as the notification ADR. `docs/decisions/0029-*.md` is _A proposed keystroke is bound to what makes it meaningful_. `nocx-hz94` carries the same wrong number; see §9.3                                                                                                     |
| **`nocx-if6`** phase A                             | `(backendId, sessionId)`; retrofitting identity after tabs, restore, ledger and blocks key on a bare `sessionId` is "a wide, unpleasant change"                                                                                                                                                                                                                                                                                                                                                         | The same argument, reused unchanged, is why §8's cut is what it is                                                                                                                                                                                                                                                                  |
| **`nocx-457v`** / the remote helper                | _(its own numbering, cited as `helper-Dn`)_ `helper-D15` reserves `seq`/`ack`, an **instance id** in `hello-ok`, the `session` service name, and "a helper's lifetime is not tied to one channel". `helper-D4`: "the port is not the authenticator; the capability is". `helper-D7`: platform in the install path. `helper-D25`: pruning removes only older versions                                                                                                                                    | The remote `session` service is deferred (D5), but D4 is **revised now** because its first-draft form pre-decided a failure mode that strands live work                                                                                                                                                                             |
| **`nocx-jv3q.1` / `.2`**                           | Group identity is **session lineage plus the backend-attested endpoint**, not the display host; drag **between** groups was removed 2026-08-01 because a group is a fact about a session, not a position                                                                                                                                                                                                                                                                                                | **Both survive unamended.** Workspace filters; lineage groups. Moving a session between workspaces is a different act — and, per D1, one that changes presentation only                                                                                                                                                             |
| **git-manager design**, line 251                   | "Multi-repository, submodules, **worktrees as a list**. nocx has **no 'project' concept**"                                                                                                                                                                                                                                                                                                                                                                                                              | The workspace **is** that concept; the exclusion loses its reason (§9.1)                                                                                                                                                                                                                                                            |
| **git-manager design**, D13                        | OSC 133 command-end rejected: "**an agent is one long command**"                                                                                                                                                                                                                                                                                                                                                                                                                                        | Contradicts `vision.md` §11. `agent-status.ts` already acts on the correction; the vision does not (§9.1)                                                                                                                                                                                                                           |
| **`docs/vision.md`** §11                           | Orchestration-as-plugin is _undecided and not scheduled_; the counterweight is "the hard part is not the terminal"; the smallest first step is "make the session model orchestration-ready **without building orchestration**"                                                                                                                                                                                                                                                                          | This design is that step. §8's cut is what "without building orchestration" means concretely                                                                                                                                                                                                                                        |

## 3. Decisions

| #       | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **D1**  | **Membership is never an input to authority.** A **workspace** is user-created, host-agnostic and optional, and holds a policy revision pointer that is _offered_ when a human approves an **AuthorityBinding** for a session. A **RunGrant** is instantiated per execution from that binding. Moving a session between workspaces changes **presentation only**; acquiring the destination's authority requires an explicit, impact-previewed re-grant                                                                                                                                                                                                                                                                                                                      | Two rejected in turn. (a) **The workspace as enforcement object** — ADR-0020 §5's original rejection. (b) **An immutable grant on the session**, which the first draft proposed: it contradicts `ledger.go:319` ("authority recorded on a run") and fails concretely — a session created in `staging` and dragged into `production` shows inside the production story while still authorised only for staging, so the tab lies in one direction and denies in the other. (c) **Minting per run from the _current_ workspace**, the reviewer's first fix: it restores ADR-0020's original hole with a delay, because dragging then confers production authority at the next run start, announced by a dialog the user clicks through                                                                                                                        |
| **D2**  | **A workspace holds zero or more heterogeneous rows: `(environment, path?, git binding?)`.** None is primary                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | A single root. It breaks the owner's case — a devops task touching five servers has no primary host — and it breaks GitOps, where the repository is local and the targets are remote, so the repository is **not** a property of an environment                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| **D3**  | **Workspace is optional.** With none, `workspaceId` is null, the run grant's source is the global default policy, and roots lie in a flat list — today's behaviour. **With no workspace, an agent may spawn children only into its parent session's own environment**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | A hidden default workspace. It lies in the UI, and it turns "the row list bounds spawning" into a hole anyone walks through by not creating a workspace                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| **D4**  | **The remote durability substrate is a thin `nocx-helper`** holding PTYs and the AD-9 ring and nothing else, over a unix socket in a runtime directory keyed by **host identity**. **Durable invariants, decided now:** a helper generation owns the PTYs and replay state it created; a client/helper version mismatch **may refuse to create new sessions** and **must never kill an existing session, nor be the terminal state for one**; a live session must retain an actionable attachment or rescue path through its owning generation. **The version-lifecycle mechanism is decided in epic E, before the first durable helper ships**                                                                                                                              | (a) **tmux/dtach** — a second VT engine, against the product's table-stakes claim (`vision.md` §4); a second replay owner against AD-9; a prerequisite we cannot install. (b) **The first draft's "a version mismatch refuses" as a blanket rule, and its blanket rejection of generation machinery.** Both pre-decided a deferred feature's failure mode, and the reviewer showed the consequence: helper v1 holds a live PTY, client v2 refuses, self-restart is forbidden, so the user chooses between losing access and killing durable work. A deferred epic may carry known requirements and prohibitions; it may not carry a recovery policy already known to strand work. (c) **Self-restart on mismatch**, which `herdr --remote` does                                                                                                            |
| **D5**  | **Durability is deferred.** At stage 1 workers die with the backend — herdr's behaviour, accepted deliberately by the owner                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Building D4 now. `vision.md` §11's own counterweight: MVP is not closed, and this would start a second product inside unfinished work                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| **D6**  | **A parent's death never closes its children** — not on process exit, not on a backend restart, not on a dropped link. Only an explicit human act may, and closing a tab with live descendants **asks** rather than decides                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Closing the subtree with its root. Three of the four ways to lose a parent are _failures_, and a failure carries no information about whether the work is still wanted                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| **D7**  | **Liveness is a projection with an epoch, and `unknown` is first class.** At least `alive`, `dead`, `unknown`, `interrupted`, stored as `{liveness, livenessEpoch, observedAt}`. A node is `dead` only on an authoritative terminal event                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | "A dead node reads as dead", the first draft's rule, with no third state. A child on a host we cannot currently reach is neither: rendering `dead` lies and rendering `alive` lies. `ledger.go:336` already uses `interrupted` after a restart rather than asserting liveness — the same principle, reused. The epoch is what stops a late observation from a previous incarnation reviving a current record                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| **D8**  | **Lineage records provenance; a separate revocable Delegation carries continuing authority.** `createdBy(parent, child)` is immutable and proves only "A created B". `Delegation{controllerSession, childSession, epoch, effects, state}` proves "A may currently observe or control B", with effects `observe \| receive-events \| send-input \| delegate-further \| close`. **Human takeover suspends `send-input` and preserves `observe`** (ADR-0020 §3: demoted, not evicted); a change of **authenticated authority context** — new credential, different lifecycle domain, an adopted unrelated process — moves the delegation to `scope-suspended`, from which control does not resume without human approval. Upward communication is by event, never by addressing | (a) **Lineage as the authority boundary**, the first draft's position. It answers only one of ADR-0020's three objections. Concretely: A creates B; B is taken over, re-credentialed, and `ssh`'d into production; B is still a descendant forever, so A retains read and control over a session whose operator and context have changed. (b) **Revoking the delegation on any takeover**, the reviewer's first fix: helping your own worker past a `y/n` prompt would permanently sever the coordinator from it. The discriminator is _did the authority context change_, not _did a human touch the PTY_. (c) **Owning the delegation from the parent run** — a coordinator run ends while its workers live, so it belongs to a controller **session**'s binding                                                                                         |
| **D9**  | **Spawning a child is the `delegate` effect** over the resource `environment`, permitted only into an environment the workspace lists (or, per D3, the parent's own). Reaching further is scope expansion and escalates                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | A free `spawn` capability. An agent that may spawn anywhere grows its own sandbox sideways and the tree becomes decorative                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| **D10** | **The git panel follows the active tab.** The session remains the sole owner of "which repository"; the workspace influences only where a new session opens                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | The panel following the workspace — a second owner of one input, and the loser goes on advertising what it can no longer deliver                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| **D11** | **State is evidence, not a value.** Each observation is appended with `{source, sourceSequence, targetAttempt, outputOffset, observedAt, provenance, freshness}` and **bound to the attempt and output offset it describes** (§5.2). Display state, phone-notification eligibility and tree state are **reducers** over that evidence                                                                                                                                                                                                                                                                                                                                                                                                                                        | (a) **One `status` field with source priority** — it produces a stuck `done`: the hook fires at end of turn, the agent resumes without firing, and a higher-ranked stale value outranks a fresh lower-ranked one forever. (b) **The first draft's "precedence is time order, not source rank"** — there is no shared clock (hooks in the backend, title and screen in the renderer, process facts in the session, later a remote host), and it contradicts the same draft's "tier 5 is override only": if a tier-5 event can win by arriving last, it is primary                                                                                                                                                                                                                                                                                           |
| **D12** | **Detection rules are local, user-editable settings with shipped defaults, switchable off per agent, and accompanied by a live "what is this agent emitting" view**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | herdr's remote manifest catalogue (`ManifestCatalog`, `AgentRemoteStatus{cached_version, attempted_version, …}`). Right for a public product serving twenty agents to strangers; for us a network dependency for correct behaviour, against `vision.md`'s "no cloud, ever". The emitting-view is **not** optional: a rule the user must write blind is a dead rule                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| **D13** | **External control authority belongs to an explicitly enrolled process tree.** Enrolment happens either at nocx-managed launch **or while a hand-started foreground process is running**: it asks over the ADR-0024 authenticated channel, nocx takes the caller's OS identity from peer credentials, verifies it belongs to that session's current foreground group and lifecycle domain, pins the tree root by a **non-reusable** handle (pidfd / start-time), and a human approves once, seeing the executable and the scope. No bearer material in the environment                                                                                                                                                                                                       | (a) **A capability token in the environment**, the first draft's proposal, presented as ADR-0028 narrowing. It is not: every descendant inherits it, so `npm test` hands it to a dependency's postinstall script, and it leaks through `/proc/<pid>/environ`, `env`, `sudo -E`, crash reports and agent transcripts. (b) herdr's **`HERDR_ENV=1`** — a boolean that tells the agent it is inside and bounds nothing. (c) An **inherited control FD**: the agent cannot pass it to a shim without also passing it to every other child, and `CLOEXEC` prevents the shim from receiving it at all — so it is either ambient or impossible. It remains useful _inside_ nocx, where we own both ends. (d) **"Only nocx-launched agents may control"**, the reviewer's first fix: it breaks the owner's stated case, where `claude` is typed into a tab by hand |
| **D14** | **The enrolled principal is the process tree, and the product says so.** The approval reads "allow this agent **and commands it launches**"                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Attempted subprocess attribution. Once the agent chooses to run a postinstall script, nocx cannot decide that script is semantically less the agent than `git` or `go test`. Stating it is honest; the mitigation for "too broad" is a narrower delegation or a sandboxed tree, not a guess about intent                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| **D15** | **Worktree discovery returns everything with a reason attached; the surface decides what to show and always states how many it hid**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Silent filtering. _Which_ worktrees to hide is deferred to trying (§11.2) — that is fine and cheap, provided the deferral cannot ship silent hiding meanwhile. orca has erred in both directions here (#9388: "broader matches can hide legitimate user worktrees")                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |

## 4. The model

### 4.1 Objects

```
Workspace                      user-created, optional, host-agnostic
  rows: [(environment, path?, git binding?), …]     zero or more, none primary
  currentPolicyRevisionId ─────► offered as the default for NEW approvals only

AuthorityBinding               what a human actually approved
  sessionId, workspaceId, policyRevisionId
  approved resource ceiling, approved effects, approvedAt, revokedAt?

RunGrant                       ledger.go's existing Grant, on StartExecution
  grantSource: GlobalDefault{policyVersion} | AuthorityBinding{bindingId}
  resolved resources, expiry, immutable snapshot

Session                        server-authoritative (AD-7)
  sessionId + sessionEpoch     epoch prevents id reuse and stale-incarnation revival
  parent: (backendInstance, sessionId, sessionEpoch)    immutable, provenance only
  workspaceId                  nullable, carries no authority
  liveness, livenessEpoch, observedAt

Delegation                     who may currently observe or control whom
  controllerSession, childSession, epoch, createdByRunId (provenance)
  effects: observe | receive-events | send-input | delegate-further | close
  state:   active | input-suspended | scope-suspended | revoked | expired
```

Each object may change without forcing another to lie. Membership says which sessions form the
user's story. The policy revision says what authority is _offered_ for new approvals. The
binding says what a human _approved_. The run grant says what one execution _held_. Lineage
says who created whom. Delegation says who may act **now**.

**Three shapes fall out of D2 without a branch in the model:** the coder (one row, `local`,
git-bound — the `nocx / silver-river-0fc7` row in the owner's screenshot); the folder (one row,
no git binding — where orca needs a `folder-workspace-worktree` projection, we get it because
rows are heterogeneous); and devops/GitOps (one git-bound row `local:~/repos/ansible` plus
three unbound rows `deploy@srv-0{1,2,3}` — the repository is where work is _authored_, the
environments are where it _lands_; a pull-based host that also has a checkout is just another
git-bound row).

**Where the boundary is not.** Environments are shared. Two workspaces may both list `srv-01`,
and on that machine there is no wall between them. This bounds **reachability** — in the UI, in
the binding, in the delegation — never **blast radius**. That is ADR-0020's second objection to
workspace-as-principal, and this design does not repeal it.

### 4.2 The session creation interval — **required by epic E, not by epic A**

AGENTS.md's testing rule 3 requires an invariant to name **both** ends, and the invariant below
is the one a durable session record needs. It is stated here so epic E inherits it rather than
re-deriving it, and it is **deliberately not** in epic A's cut.

The reason is a fact about the tree: `internal/session/` has **no restore path at all**, and
tabs survive a restart through `internal/settings/` — the frontend re-opens sessions from
persisted tab descriptors and the backend restores nothing. So this interval would introduce
session persistence for the first time. And under D5 there is nothing yet for it to preserve:
if workers die with the backend, every node after a restart is a fresh session, and a durable
parent edge has no surviving child to point at. `nocx-if6` phase A's argument is about **shape**
— what the record and the wire carry — not about durability, and only the shape is expensive to
retrofit.

`internal/session/session.go` is memory-only today and makes a session addressable by inserting
it into a map.

> A **prepared** session record — carrying `sessionId`, `sessionEpoch`, the immutable parent
> edge, `workspaceId`, and the grant source — exists **before any process is spawned**, remains
> prepared while spawn and registration proceed, and becomes **addressable only in the same
> durable transition that marks it active**. On any failure before activation, recovery either
> adopts the identified process under that record or terminalizes the record. **The interval
> closes at durable activation or durable terminalization.**

The crash cuts this must survive, each needing a recovery test: record persisted → no spawn;
spawn → no record; in-memory registration → no persistence; `open` returned → no durable
commit; parent edge persisted → child spawn failed; child spawned → parent edge not committed.

## 5. Agent state

### 5.1 What is true today, and what the correction is

`vision.md` §11 founds orchestration on OSC 133 answering "did this command finish?", "which is
the same question as 'is this agent done?'". **That is false for agent TUIs**, and this
repository already knows it twice over: the git-manager design's D13 rejected OSC 133
command-end because "an agent is one long command", and `frontend/src/agent-status.ts` says the
same in its header comment and reads the **title** instead.

So the existing honest state is two values, `working | idle`, from OSC 0/2, with no provenance
and no history. Everything below is new work — built on a correct instinct already in the tree,
not reuse of a model that exists.

Both prior-art projects converged from outside the agent: herdr ships twenty TOML manifests
matching literal UI copy against screen regions and updates them from a network catalogue;
nelix calls per-tool drivers "irreducibly per-tool" and budgets the cost explicitly. nocx is
not outside the agent for the cases that matter most.

### 5.2 Evidence, bound to an attempt

The sources are not comparable and must not be forced through one last-writer-wins field (D11).
They are appended as observations and reduced.

| Provenance           | Source                                                               | May wake your phone?                |
| -------------------- | -------------------------------------------------------------------- | ----------------------------------- |
| `known`              | our own agent; the loop is ours (ADR-0028)                           | yes                                 |
| `declared`           | a hook on the ADR-0024 authenticated channel                         | yes                                 |
| `declared-anonymous` | the terminal title, OSC 0/2                                          | **no by default**, per-agent opt-in |
| `observed`           | pty facts: exit, alternate screen, blocked on stdin, silence for _N_ | no                                  |
| `inferred`           | pattern match against the bottom of the screen                       | no                                  |

`declared-anonymous` is the class ADR-0024 exists to name. The title is far more stable than
screen copy because it is a deliberate status field — but it is written on the same anonymous
channel, so a file containing `ESC]0;Action Required BEL` would push a notification from a
`cat`. Hence the default.

**What makes ordering solvable is not a clock — it is that nocx knows what no orchestrator
knows.** Every observation is bound to `session epoch + lifecycle domain (ADR-0024) + attempt
(ADR-0020 §4) + output offset (AD-9)`. Then "waiting for input" is not a floating status; it is
_attempt 17 in domain D is waiting as of output offset 92831_. A late `inferred` observation
from attempt 16 cannot overwrite it, because it does not describe the same thing. This is also
what gives the attention queue an exact acknowledgement boundary.

**Facets are separate, not merged** — `agent-status.ts` already draws this line: process
liveness, turn state, work state, human-attention state. termic's "Still working (screen → not
done yet)" exists "for agents that background work and end their turn anyway", which is exactly
the turn ending while the work does not.

**Launch configuration is data, per agent** (termic's shape): command, default args with
placeholders, YOLO args, `--session-id {UUID}` / `--resume {UUID}`, `--name {WORKSPACE_SLUG}`,
environment lines. One subtlety is load-bearing for D2: in a worktree row each tree has its own
directory, so the agent's most-recent-CWD session _is_ that row's session and `--continue` is
correct; in a shared main checkout it would lasso unrelated sessions, so an explicitly minted
UUID is required.

## 6. The control surface

**One dispatcher, two callers** (AD-8). The built-in agent reaches it in-process with a grant
minted per run; an **enrolled** external CLI reaches it over a private local socket. They differ
only in how the grant was sourced.

**Enrolment** (D13) binds three facts: the **session and lifecycle domain** the request names;
the **process identity** the socket supplies, resolved to a foreground tree root and pinned by a
non-reusable handle; and **human intent**, approved once against that exact executable and
scope. The request proves "I am a process executing inside this authenticated session"; it does
not prove "I am genuinely Claude" — human approval supplies that attribution, which is
acceptable because the approved principal is the displayed process tree.

Root exit, domain termination or explicit revocation ends enrolment; pid reuse cannot revive it.
A child asking after its tree is enrolled is simply part of it, and cannot obtain a second or
wider enrolment without another approval.

Requests are then narrowed by the delegation (D8) and the run grant. Per ADR-0028 this is
narrowing, not checking: the caller cannot exceed its scope because nothing it holds names
anything outside it.

**A spawn or enrolment request may never ride the byte stream.** ADR-0024 settled this at cost:
OSC 133 is an anonymous broadcast channel, so a `cat` of a hostile file would open tabs.

## 7. What is deliberately out

- The remote `session` service, a daemon, reattach, cross-host replay, orphan reaping and
  adoption (D5). Reserved by `helper-D15`; not built.
- A remote manifest catalogue or any network fetch of detection rules (D12).
- Any change to the horizontal tab strip or to `nocx-jv3q.1`'s grouping key.
- Deriving workspaces from current facts instead of letting the user create them — raised in
  review and **rejected**: user-created, host-agnostic workspaces are settled owner input.
- The orchestration _plugin_ of `vision.md` §11. This is the smallest first step that section
  names, and deliberately not the step after it.

## 8. What must not be deferred

Everything is deferrable except the following, and the argument is `nocx-if6` phase A's reused
unchanged: retrofitting session identity after tabs, restore, the ledger and blocks all key on
a bare `sessionId` is a wide, unpleasant change, while adding it now is mechanical.

**In epic A:**

1. `sessionId` **plus a backend-instance / session-epoch identity** sufficient to prevent id
   reuse and to distinguish a restored record from a current incarnation.
2. The **immutable parent edge, using that full identity** — bare `parentId` is not enough;
   orca validates repo, host, project _and_ both endpoint instance ids and removes cycles.
3. **Nullable `workspaceId`**, carrying no authority, because it otherwise touches every
   session wire and restore shape later.
4. **Liveness** as `{liveness, livenessEpoch, observedAt}` including `unknown` and
   `interrupted`.
5. The rule that **no addressability follows from lineage alone** — enforced now, so that no
   later epic can implement "lineage implies control" as a temporary shortcut.
6. **D6**, the rule that a parent's death never closes its children.

All six are shape: a record, a wire contract, and two prohibitions. None needs a store.

**Two items were in this list in the second draft and were removed after checking the tree:**

- **The prepared → active transaction** (§4.2) introduces session persistence, which does not
  exist, and under D5 has nothing to preserve. It belongs to epic E.
- **The grant-source seam.** `StartExecution` — the whole ADR-0020 grant path in
  `internal/content/` — has **no caller outside its own package**. Adding a `GrantSource`
  parameter to it in A would widen an unreachable write path and call it a foundation, which is
  `nocx-rtg0`'s defect exactly: "a reachable read path hid an unreachable write path in the same
  package". The seam lands in epic C, in the same change that first mints a grant from a live
  caller. What A carries instead is the **prohibition** in item 5, which costs nothing and needs
  no reachable path.

**Not in A, at no retrofit cost:** `PolicyRevision`; the `AuthorityBinding` row (the grant-source
seam in C is what makes its later insertion mechanical — the run records its source, and the
dispatcher consumes a resolved grant, never workspace membership); `Delegation` (nothing to
delegate until one session can address another); the evidence model and provenance table
(epic C, with `agent-status.ts` remaining the honest projection meanwhile); external enrolment.

A fictitious `AuthorityBinding` row in A was proposed and **rejected in review**: a row claiming
human approval where no approval act exists is not an honest stub, and it would prematurely fix
binding ownership and lifetime before controller sessions exist.

**`workspaceId` is the weakest item in A** and is kept on the `nocx-if6` argument alone — it is
a field in a record and on a wire, carrying no behaviour, whose later addition would touch every
session open and restore shape. If that argument is rejected, it is the one item here that can
leave without taking anything else with it.

## 9. Amendments this design owes other documents

1. **`docs/vision.md` §11** — the claim that OSC 133 answers "is this agent done?" is false for
   agent TUIs. Two places in the tree already act on the correction (git-manager D13,
   `agent-status.ts`); §11 is the stated foundation for orchestration, so the error is
   load-bearing and should not survive in the document people quote.
2. **ADR-0020 §5** — record that lineage answers **provenance only** and that continuing
   authority is a separate revocable object, so the ADR's three objections all stand; and record
   that the workspace-with-rights the owner asked for is the ADR's own _minting_ role, refined:
   the workspace **offers**, a human **approves** a binding, a run **realizes** it, and moving a
   session changes presentation only.
3. **ADR number collision** — `docs/decisions/0029-*.md` is about keystrokes, while `nocx-hz94`
   cites "ADR-0029 §2.2" as governing notifications. The repository already carries two
   ADR-0006 files. Needs its own bead; not fixed here.
4. **AGENTS.md, "Clean-only: no backward-compatibility shims"** — _a proposal for the owner,
   not adopted by this document._ Proposed wording, from the review: _no backward-compatibility
   shims for re-derivable greenfield state; a deployed generation holding irrecoverable live
   external state remains supported by its version-matched adapter until that state
   terminalizes, and new code does not emulate its protocol._ The rationale is that the helper is
   the first nocx component for which "break freely" can destroy someone's running session.
   The exception is narrow: it begins only when an artifact owns non-reconstructible live state,
   requires version-addressed implementations rather than conditionals in current code, and ends
   deterministically when the last owned session ends.
5. **`.internal/specs/2026-08-06-git-manager-design.md` line 251** — the exclusion of "worktrees
   as a list" is justified by "nocx has no 'project' concept". The workspace is that concept.

## 10. Epic decomposition

`vision.md` §11 warns specifically against bundling a second product into unfinished work.
**A is valuable alone** and is the only piece whose cost rises with delay.

|       | Epic                                                                                                                                                      | Depends on                                                    | Note                                                                                                                                                                |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A** | The tab knows who opened it, and stops lying about whether it is alive — §8's six items                                                                   | —                                                             | Shape only: a record, a wire contract, two prohibitions. No store, no UI beyond the strip, no authority model                                                       |
| **B** | Workspaces as a surface — the panel, heterogeneous rows, new-session behaviour                                                                            | A, `nocx-jv3q`                                                | Filters over the grouping key `jv3q.1` defines, so that key must exist first                                                                                        |
| **C** | An agent addresses a delegated session — `Delegation`, `AuthorityBinding`, the grant-source seam and its first live minter, enrolment, the evidence model | A, `nocx-dw3`, **and a real attention queue — `nocx-ms7v.4`** | Where §5 and §6 land. The attention-queue dependency is load-bearing and is filed-not-built: without it "the worker finished / is asking for you" has nowhere to go |
| **D** | Worktrees as a list                                                                                                                                       | B                                                             | Unblocks the git-manager exclusion. **Parallel with C** — they share no files                                                                                       |
| **E** | The remote `session` service, durable session records, and the version lifecycle                                                                          | A, `nocx-457v`                                                | Deferred (D5). Owns §4.2's interval and must answer D4's open mechanism before the first durable helper ships                                                       |

**The critical path to the thing this design is for is A → C**, and C is gated not by workspaces
but by the attention queue and the agent. B may land any time after A: it buys ergonomics and
authority, and under D3 an agent with no workspace still spawns into its parent's environment,
so workers do not wait on it.

## 11. Open questions

Marked open rather than answered by the author. The first draft listed five; review replaced
most of them.

1. **What revokes addressability without erasing lineage** — D8 names the states; the exact
   trigger set, and who may re-approve a `scope-suspended` delegation, is not designed.
2. **What takeover does to the parent's rights in the hard case** — the `y/n` case is answered
   (`input-suspended`); "the human kept the session and changed what it is" is answered in
   principle (`scope-suspended`) but not in the mechanics of detecting it.
3. **Is reading output separately granted from control?** D8's effect list separates them; the
   default bundle is not decided.
4. **What happens when a workspace policy changes, or a workspace is deleted, while sessions and
   runs live.** D1 pins bindings to a revision; migration UI, and the deletion case, are open.
5. **What constitutes an environment identity** for local, ad-hoc SSH — `ProfileID` is
   documented "Empty for ad-hoc/local sessions" (`internal/session/session.go:56`), so a
   hand-typed `ssh deploy@srv-01` has no profile to name — nested authenticated domains,
   containers and jump chains. The connection profile is a _launch recipe_; it may not be the
   environment.
6. **Lineage bounds** — cycles, self-parent, depth, fan-out, fork-bomb resistance, and what a
   restored child referring to an absent parent means.
7. **The stitch with `nocx-jv3q`** — the strip's concrete behaviour under an active workspace,
   and what `Cmd+1..9` selects then (`jv3q.1` has an explicit assertion about this).
8. **Worktree list hygiene** — deferred to trying (D15 keeps the deferral honest). orca's prefix
   list and its #9388 are the prior art in both directions.
9. **The removal fence** — a session open in a worktree being deleted. orca fences PTY and
   watcher installs and has the renderer _recognise_ the fence so a doomed pane does not surface
   it as a terminal error. We have no such situation today and will after epic D.
10. **Retention and privacy** for durable attention events and agent transcripts.

### What would falsify this design's thesis

"Orchestration is a view over sessions, not a second product" holds only while every
orchestration action is expressible as ordinary session operations plus durable facts. It is
falsified by any of: scheduling work when no session yet exists; dependency-aware retries;
resource quotas across workers; durable jobs outliving every tab and client; ownership transfer
between coordinators; transactional fan-out/fan-in; reconciliation across several independently
available backends. Stage 1 may remain session-first, but the thesis should not be claimed as
permanent architecture — and the first of those needs is the signal to revisit it.
