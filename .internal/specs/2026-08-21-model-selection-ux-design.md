# Model selection UX: a default model, a role that resolves, and a product that says what is missing

Date: 2026-08-21 · Bead: nocx-rikz5 · Owner: shady2k

## The report

From the owner's first real use of the Roles page (screenshot, 2026-08-21):

1. Assigning a model per role is inconvenient — two identical rows, each
   demanding the same two decisions.
2. There should be a **default model**, with the ability to override it for a
   specific task.
3. It is **not obvious** a person has to go into Roles at all. With many
   endpoints, each offering many models, the current shape does not scale.
4. The green line under each role — `Answers with openrouter ·
deepseek/deepseek-v4-flash-0731` — repeats what the two selects above it
   already say.
5. And, said plainly mid-discussion: **"нам всё равно есть соединения или нет,
   нам важно выбрана ли роль."**

Point 5 is the one that reorganises the rest, so it is where the design starts.

## What this crosses, and what those documents already decided

Named before anything is proposed, per AGENTS.md.

- **AD-8, one owner per behaviour.** `profile.ResolveRole` (internal/profile/role.go:140)
  is the single place a role becomes an (endpoint, model) pair. Everything
  below reads it; nothing below adds a second resolver. The default is a new
  _input_ to that function, never a second function.
- **`nocx-e6kn2` (closed, "Model roles: a feature asks for a role, never for a
  model id").** Its acceptance criterion is binding here: _"A role with no
  model assigned is a VISIBLE failure where the feature is used, never a
  silent fallback to some other model — a silent fallback means a person
  cannot tell which model answered."_ The default in this design does **not**
  violate it, and §2 states exactly why.
- **ADR-0028 / `nocx-6bo1` (the agent loop is ours; the model client is
  replaceable).** Model selection stays above the client seam. No part of this
  design lets a client pick a model.
- **AGENTS.md, "a soft degrade must be visible in the product, not only in a
  log."** §1 is that rule applied to readiness.
- **`frontend/src/ui/README.md` (read the kit first).** §4 uses the editor
  chrome's existing `.nocx-chip` family rather than introducing the kit's
  `ui-badge` into a row that has none. The tension is recorded in §4.

## 1. Readiness is the resolvability of a role, not the existence of an endpoint

**Today's defect, stated exactly.** `contracts/agent.status.schema.json`
declares three fields — `endpointConfigured`, `credential`, `lastProbe` — and
knows nothing about roles. `agentStatusLine`
(frontend/src/agent-status-line.ts:44) therefore branches on
`endpointConfigured` first and ends at `Ready`. So an endpoint that exists and
holds a valid key reports **"Ready"** while the `answering` role is unassigned,
and the person discovers otherwise only when a question is refused with
`ErrRoleUnassigned`. That is a readiness line contradicted by the product one
keystroke later.

**The change.** The status answers a different question: _can the role the
feature will ask for resolve, and if not, why?_ The vocabulary is not invented
— `internal/profile/role.go` already returns it:

| Error                 | Meaning                                  |
| --------------------- | ---------------------------------------- |
| `ErrRoleUnassigned`   | no model chosen for this role            |
| `ErrRoleEndpointGone` | the assigned endpoint no longer exists   |
| `ErrRoleModelGone`    | the endpoint no longer offers that model |
| `ErrRoleUnknown`      | not a role the product defines           |

**Consequences.**

- `agent.status` grows the resolution of `answering`: whether it resolves, and
  when it does not, which of the reasons above. The contract changes, so it
  carries `additionalProperties: false` plus an explicit `required`, and is
  asserted both as a DTO and **over the real socket**.
- Endpoint and credential facts stay, but stop being the headline. They become
  _reasons a role cannot resolve_, not a separate top-level state.
- `agentStatusLine`'s branch order inverts: role first, endpoint and credential
  as detail underneath.

## 2. The default model, and why it is not the forbidden fallback

**Shape.** Beside the existing `RoleAssignment{Role, EndpointID, Model}`
(internal/profile/role.go:72) the store keeps **one (endpointId, model) pair
with no role** — the default. `ResolveRole` gains one step, and remains the
only resolver:

```
role has its own assignment  → use it
otherwise, a default exists  → use the default
otherwise                    → refuse, naming the reason
```

**Why this does not violate `nocx-e6kn2`.** That decision forbids the product
choosing a model on the person's behalf, because then nobody can tell which
model answered. Here the model is named **by the person, explicitly, once**.
Using a choice someone made is not a fallback; inventing one is. The
distinction is load-bearing and is the reason the default is a stored,
user-authored value rather than "the first model of the first endpoint".

**Where the default is set.** At the top of the Roles page, as its own control
— one endpoint + model pair, above the roles. The page is not renamed: "Roles"
is what the closed decision, the wire methods (`roles.list` / `roles.assign`)
and the rail all call it, and renaming the surface while adding a control to it
would make two names for one place mid-change. The person is not expected to
find this page unaided — §3 and §4 are what bring them here.

**Overrides stay per role**, as they are today: the owner confirmed the role
_is_ the task. Each role's select gains the value **"As default"**, and that is
its initial state — so the Roles page opens already working and demands
nothing.

**The consequence for the unready state.** With a default, `ErrRoleUnassigned`
stops being the common case. Exactly one state remains unready: neither a
default nor a per-role assignment exists.

## 3. The ladder: one rung, one sentence, one place to fix it

Each state names a single next action and points at a single surface. This is
the answer to "it is not obvious you must go into Roles" — the person never has
to deduce it.

| State                                         | What the product says                      | Where the fix is     |
| --------------------------------------------- | ------------------------------------------ | -------------------- |
| No endpoints at all                           | Add an endpoint first                      | Settings → Endpoints |
| Endpoints exist, no default and no assignment | Choose a model                             | Settings → Roles     |
| The role's endpoint is gone                   | Assign a model again                       | Settings → Roles     |
| The role's model is gone                      | Assign a model again                       | Settings → Roles     |
| The endpoint has no key                       | (the existing `credentialLine` vocabulary) | Settings → Endpoints |

"No endpoints at all" and "nothing assigned" are **two states, not one**,
because they are fixed in different places. Collapsing them would send a person
to Roles to choose from an empty list.

## 4. The model chip in the editor

**Placement.** The editor's chrome row (`editor.ts`, `chromeLeft`), beside the
cwd chip — where there is room, unlike the gutter. It joins an existing family:
`recoveryChip` is already a `<button class="nocx-chip …">` with a click
handler, so a clickable chip here is precedent, not invention. A chip that
appears and disappears is also already this row's normal behaviour —
`locationChip` and `recoveryChip` both live behind `display: none` until a fact
arrives — so the chip does not disturb the composer's single height
(`nocx-6c546`).

**Visibility.** The chip is shown **only in Ask mode**, and it shows the model
that will answer _this_ question — the `answering` role's resolution. In Run
mode there is no chip: no model answers a shell command, and a chip claiming
otherwise would be decoration.

`classifier` gets no chip. It is internal machinery with no question of its
own; its home is the Roles page, and an unresolvable classifier still refuses
visibly at the moment the feature calls it.

**Content and targets.** The chip is the ladder made permanent — its text is
the current rung, its click is that rung's fix:

- Resolves → two chips: the provider (→ Settings → Endpoints) and the model
  (→ Settings → Roles). This is the "respectively" the owner asked for.
- No default, nothing assigned → one chip, "Choose a model" (→ Roles).
- No endpoints → one chip, "Add an endpoint" (→ Endpoints).
- Endpoint or model gone → one chip, "Assign a model again" (→ Roles).

**Long ids.** `deepseek/deepseek-v4-flash-0731` is long. The chip truncates on
one line and carries the full value in `title` and the accessible name. It
never wraps — a wrapped chip is the layout shift this row exists to avoid.

**One recorded tension.** The chrome row's chips are `.nocx-chip`
(`style.css:204`); the kit offers `ui-badge`. The new chip joins its
neighbours, because two visual grammars inside one row is worse than one
pre-kit grammar. Migrating the whole row to the kit is a separate job and is
not smuggled in here.

## 5. The green line says only what the controls cannot

`roles-section.tsx:63-78` has four states. Three carry a fact the selects
cannot show; the fourth restates them. The rule that fixes it:

- Resolved to exactly what the selects show → **no line at all.**
- Resolved through the default → the line names endpoint and model, because the
  select reads "As default" and the person cannot otherwise tell what that is.
- Unresolved → the line says which rung of §3 this is.

The redundancy the owner pointed at disappears because the line acquires work,
not because its wording was tuned.

## 6. Acceptance

**What a person can do that they could not before:** install an endpoint,
choose a model once, and ask a question — without ever opening the Roles page
to discover it was required, and without ever being told "Ready" by an
assistant that is not.

**The end-to-end check** (`cmd/devharness`, real backend, real socket): clean
profile → the readiness line says _add an endpoint_ → add an endpoint with a
key → the line becomes _choose a model_, **not** _Ready_ → choose the default
from that line's own control → ask → the answer arrives.

**Failure paths — one test per rung of §3**, because each points somewhere
different and a mis-aimed button sends a person to fix the wrong thing. Paired
with the positive case each time: on an ordinary machine with a default
assigned, the role resolves.

**Interval, both ends.** The default exists from the moment it is written until
it is either overwritten or its endpoint is deleted. Deleting the endpoint a
default names must return the ladder to _choose a model_ — it must not leave a
default pointing at nothing. Endpoint deletion is an ordinary action, not an
edge, so it is tested.

**Contract.** `agent.status` changes shape: `additionalProperties: false`, an
explicit `required`, `…_DTOConformsToContract`, and
`…_OverTheWireConformsToContract` against the real result off the real socket.

**Test authorship.** Acceptance criteria are written as assertions in the
beads rather than prose, so the implementer is not the only author of the
tests.

## Shape of the work

This is an epic, not a task: it changes a contract, the resolution seam, the
readiness derivation, the Roles page and the editor chrome. The natural
children, sequenced so the front stays narrow:

1. `ResolveRole` learns the default; the store keeps it. (No UI.)
2. `agent.status` grows the role's resolution — contract, DTO and
   over-the-socket assertions.
3. `agentStatusLine` inverts to role-first; the ladder's sentences and targets.
4. The Roles page: the default control, "As default" on each role, and the
   green line's new rule.
5. The editor chip, Ask mode only.
6. The end-to-end check of §6.

Child 1 lands with the wiring that makes it reachable, per AGENTS.md: a
resolution path with no caller cannot pass the deadcode ratchet, and the gate
is the hook, not the brief.

## Explicitly out of scope

- A first-run wizard. The ladder in §3 solves the same problem without a new
  surface.
- Choosing the model at endpoint creation — rejected by the owner: the endpoint
  is not the unit that matters, the role is.
- Migrating the editor chrome row from `.nocx-chip` to the kit's `ui-badge`.
- A per-question model override from the composer. The owner settled that the
  override is per role; a per-question picker would be a second surface owning
  the same choice.
- Any change to `classifier`'s own behaviour beyond appearing on the Roles page
  with an "As default" value.
