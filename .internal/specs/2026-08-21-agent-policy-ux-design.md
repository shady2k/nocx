# Agent policy: the decision is made where it is asked

**Bead:** `nocx-reas5` (brainstorming) · **Date:** 2026-08-21 · **Status:** awaiting owner approval

## What a person can do that they cannot today

**Answer "stop asking me this" at the moment they are asked**, and later see and
revoke every such answer in one place.

The end-to-end check that watches them do it (rule 2, `AGENTS.md`):

> A person asks the assistant about a failed build. The assistant asks to read
> the screen; they answer **Allow always**. They ask a second question in the
> same pane and are **not** asked again. Settings → Agent policy shows the
> standing decision; they revoke it; the next question asks again.

## The three complaints, and what actually causes them

The owner's review, 2026-08-21: the page has a Save button while every other
settings screen writes on change; it is not understandable in use; and its
description is ADR language pasted into the product.

The first and third are true as stated. The second has a cause that is not on
the page at all.

**The decision is asked in one place and answered in another, and the two are
not connected.** `agent-approval-prompt.tsx` offers exactly **Allow** and
**Deny**, and both are once-only: `ApprovalStore.IsApproved` matches a proposal
by run, attempt, tool, call id and canonical-argument hash, so the next run
re-asks the same question. A person answering "yes" for the fifth time has
nowhere to say "always" except to leave, find a seven-row matrix, work out that
`readScreen` is `observe`, and flip it — which also silently permits
`files.read` and `git.status` everywhere.

**And five of the seven rows govern nothing.** `agenttools/registry.go`
declares four tools: `files.read`, `readScreen` and `git.status` carry
`observe`; `run` carries `mutate-destructive`. `mutate-reversible`,
`privilege-change`, `disclose`, `cross-boundary` and `delegate` have no tool
behind them. The page shows seven identical rows and does not say which two are
live.

## What is NOT reopened

ADR-0020 §7 as amended (accepted 2026-08-16) is the binding shape and this
design does not touch it. The matrix stays: one row per effect class, one
decision per row, resource scopes per row, unstated fails toward asking.
ADR-0028 decision 4 stays: **no configuration path may express a rule over a
tool name.** Every mechanism below writes an effect row, never a tool.

The presets stay out of the wire and the store, as `effectpolicy.go` says. This
design does not add them to the UI either — see _Deliberately out_.

## The prompt grows six answers

The prompt is the fast path. The matrix is the fine path. The prompt therefore
asks nothing beyond the decision itself — the owner, on being shown a
scope-width follow-up step: _"разрешить всегда - это для всех"_.

| Answer                | What it does                                     | Lives until         |
| --------------------- | ------------------------------------------------ | ------------------- |
| Allow once            | Today's behaviour: this exact proposal           | the call runs       |
| Allow in this session | The effect is permitted in this terminal session | the session dies    |
| Allow always          | The effect's row becomes `permit`                | revoked             |
| Deny once             | Today's behaviour                                | the call is refused |
| Deny in this session  | The effect is refused in this terminal session   | the session dies    |
| Deny always           | The effect's row becomes `refuse`                | revoked             |

`Deny always` writing `refuse` is stronger than it looks and that is correct:
`EffectPolicy.PermittedEffects` drops a refused effect from the grant, so the
tool is never offered to the model rather than offered and rejected.

**`Allow always` on `run` grants the assistant every command in every pane,
permanently, from one click.** That is what the words mean and what other
assistants do; it is written down here so it is a decision rather than a
discovery.

### The egress prompt keeps two answers

`agent.approvalRequested` carries `reason: 'policy' | 'egress'`. The six
answers are for `policy`. An egress ask means a tool result contained
secret-shaped material and nothing has been sent to the model provider yet;
"always" there would mean _always send secrets to the provider_, which is not a
standing decision anyone should be able to make by clicking a button next to
five others. **Egress keeps Allow / Deny, once only.**

## Three wire changes, each load-bearing

**1. `agent.approvalRequested` must carry the effect and the resource it
decided on.** It sends `tool` and `arguments` today. For the renderer to render
"the assistant wants to **read the screen**", and for `always` to name the
right row, it must be told the effect class — deriving it from the tool name in
the renderer would build exactly the tool-name vocabulary ADR-0028 forbids.
Add `effect` (the closed enum) and `resource` (`{kind, id}`, the one the policy
gate matched against).

**2. `agent.approve` grows `scope: 'once' | 'session' | 'always'`.** The decision
travels as ONE act and the backend applies it. The alternative — the renderer
reading the matrix, editing a row and writing it back — is a read-modify-write
race against the Settings page and puts a second owner on the policy document.
`approved: boolean` stays; `scope` says how far the answer reaches.

**3. `policy.get` reports which effect classes are live.** Add
`live: Effect[]` — the effects at least one declared tool carries, derived from
the registry, which is the only place that knows. Without it the page cannot
honestly distinguish a row that governs something from one that governs
nothing.

## The session layer

`content.ResolvePolicy` is, by the ADR, the ONE place the resolution order is
stated. It gains a third input rather than growing a second resolver:

```go
func ResolvePolicy(global EffectPolicy, workspace *EffectPolicy, session SessionOverrides) EffectPolicy
```

`SessionOverrides` is `map[Effect]Decision` — **not** a third matrix. It is
produced by clicks, never authored, carries no scopes, and overlays per row:
session over workspace over global. A matrix-shaped session layer would invite
the question "what does an empty row mean here", which for a click-produced
overlay has no answer.

It is held in memory, keyed by session id, beside the `ApprovalStore`, and
**dropped when the session ends**. Nothing persists it: a session-scoped
permission that survived a restart would be one in name only.

**The trap is where "ends" is.** `ApprovalStore` is process-lifetime, one per
server, keyed by the exact proposal — it is not per-session, so this is the
first per-session store in the assistant path and there is no existing hook
that will carry it. The session teardown that exists is
`WSServer.gitSessionClosed`, and it is called from **three** sites in `ws.go`
(1221, 2380, 2503 — the connection-loss path with a nil conn, and two ordinary
closes). A store dropped at one of them leaks the permission on the other two.
Either all three drop it, or the teardown becomes one function that both
concerns hang off; the latter is preferable and is the implementation's call.

**And it binds to the SESSION, not to the pane frame.** A person who restarts
the shell in that pane is in a new session and will be asked again. The pane
looks unchanged, so the copy says "this session" rather than "this pane" —
naming the pane would promise a lifetime the permission does not have.

`runGrantFor(sessionID)` passes the overrides for that session. The run grant's
base scope is already `ResourceSession{sessionID}`, so "this session" needs no
scope of its own — the run cannot reach outside its session anyway.

## The page

### It writes on change, like every other settings screen

The Save button goes. Each select writes immediately and the page **adopts the
policy the store returned**, the pattern `roles-section` uses — so the page can
never show a policy the store did not take. Scope text fields write on blur or
Enter, never per keystroke: `ParseEffectPolicy` rejects a non-absolute path, and
a half-typed path is a refused write on every character.

A refused write raises the kit's danger toast and the page re-reads. There is
no "unsaved changes" state to lose, because there is no unsaved state.

### It speaks product words

The seven rows stay — they are the model, and the model is accepted. Their
LABELS stop being the enum:

| Row                  | Label                              |
| -------------------- | ---------------------------------- |
| `observe`            | Read and inspect                   |
| `mutate-reversible`  | Make changes that can be undone    |
| `mutate-destructive` | Make changes that cannot be undone |
| `privilege-change`   | Gain more privilege                |
| `disclose`           | Send information out               |
| `cross-boundary`     | Reach another host                 |
| `delegate`           | Hand work to another agent         |

The page description replaces the ADR sentence. Proposed:

> What the assistant may do on its own, and what it must ask you about first.
> Anything not set here is asked.

"Effect class", "resource scope" and "refused" do not appear on the surface.
`Scope` stays as the fine-tuning control's own word, because a person who opens
that control is doing exactly the thing the word names.

### Live rows first, inert rows behind a disclosure

The page renders the rows whose effect appears in `live` — today `observe` and
`mutate-destructive`. The rest sit behind one disclosure that says what they
are: capabilities the assistant does not have yet, listed so a decision made
now still holds when it gains them. Setting one is allowed and does exactly
what it says: nothing, until a tool carries that effect.

This is the honest form of a soft degrade being visible in the product
(`AGENTS.md`): five controls that govern nothing must not look like two that do.

### A row shows how it came to be set

A row set by `Allow always` from a prompt is indistinguishable, in the store,
from one set on this page — and it must be, because it is the same matrix. What
the page adds is a sentence per non-default row saying what it means in the
person's terms:

> **Read and inspect** — Allowed
> **Make changes that cannot be undone** — Ask every time

Changing a row's select IS the revoke; there is no second control. The e2e
check above revokes by setting the row back to Ask.

## Error handling

- **Unparseable stored policy** — already handled: `GlobalPolicyStore` degrades
  to the zero matrix, which asks. Unchanged.
- **`policy.set` refuses a write** — toast, re-read, page shows what the store
  holds. No local "dirty" state exists to diverge.
- **`agent.approve` with `scope: 'always'` and the policy write fails** — the
  answer must not half-apply. The call is approved (the person said yes and the
  run resumes) and the standing part is reported as not saved: a toast naming
  the failure. The alternative — refusing the call because a write failed —
  punishes the person for a store problem.
- **A stale binding** — unchanged: answered honestly, resumes nothing.
- **The session dies while a session-scoped ask is pending** — the overrides go
  with it; the pending ask is already terminalized by the existing path.

## Security

- No path added here can name a tool. `agent.approve`'s `scope` names how far,
  never what: the effect comes from the proposal the backend itself classified.
- The renderer never derives an effect from a tool name (change 1 exists for
  exactly this reason).
- Fail-toward-asking is preserved at every layer: an absent session override is
  not a permit, an unparseable policy asks, and `SessionOverrides` has no zero
  value that permits.
- Egress is excluded from standing answers, deliberately, above.
- A session-scoped permission cannot outlive its session, and cannot be
  persisted by any path.

## Testing

**The happy path, end to end** — the check named at the top, in the e2e suite
through the real backend.

**Both ends of each interval** (`AGENTS.md` rule 3):

- A session-scoped allow is in force from the answer until the session ends,
  and absent from the moment it ends — asserted on EACH of the three teardown
  paths, since one missed site is a permission that outlives its session.
- An `always` allow is in force from the answer until the row is set back.

**Failure paths** — `policy.set` refusing during a prompt answer; a session
override for a session that no longer exists; `policy.get` without `live`.

**Preserved by test** — the preset-as-matrix equivalences already pinned in
`effectpolicy_test.go` still hold; `ParseEffectPolicy` still rejects a tool
name as a row key and a tool-kind scope; `PermittedEffects` still drops a
refused effect.

**Contracts** — `agent.approvalRequested`, `agent.approve` and `policy.get`
each change shape, so each gets its schema updated and its
`_OverTheWireConformsToContract` test in the same commit (`contracts/README.md`).

## Deliberately out

- **Presets in the UI.** The owner declined them in `nocx-szsey` and asked for
  scopes and fine configuration instead. A preset button would also overwrite
  every standing answer a person had accumulated, which is the contradiction
  that killed it in discussion.
- **Workspace-level policy.** `nocx-mp2vd` owns that seam; `ResolvePolicy`
  already has the parameter.
- **Per-tool rules.** ADR-0028 decision 4.
- **Persisting session-scoped answers.** By definition.
- **Changing the matrix shape.** ADR-0020 §7 as amended.

## Loose end found while writing this

Several comments still read "amendment PROPOSED, awaiting owner approval"
(`effectpolicy.go:3`, `globalpolicy.go:2`, `agent-policy-section.tsx:3`) while
ADR-0020's header records it accepted on 2026-08-16. Stale comments on a
settled decision; correct them in the same epic.
