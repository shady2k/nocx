# A skill ages, and the person can see it age

- **Bead:** `nocx-dzy7l` (epic), `nocx-nizsc` (this brainstorm)
- **Date:** 2026-09-07
- **Status:** approved by the owner, section by section
- **Related:** [`2026-09-04-skills-under-the-same-policy-design.md`](2026-09-04-skills-under-the-same-policy-design.md)
  (the prompt wording that made staleness a real state),
  [`2026-09-05-the-skill-viewer-design.md`](2026-09-05-the-skill-viewer-design.md)
  (the skill's own tab, where the pins live)

## Why

Skills accumulate. A person installs ten, reaches for three, and the other seven
go on costing something: every enabled skill's description enters the system
prompt on every ask. Nothing anywhere says that a skill has not been opened
since March. The prompt was reworded from "follow it, what it returns is
instruction" to guidance that can go STALE, and staleness then has to be a real
state with real transitions rather than a figure of speech.

**What a person can do that they could not before:** see how long a skill has
gone unused, and have nocx switch off the ones that have gone quiet — while
keeping the ones they want kept, and knowing which switch was theirs.

## What this is NOT

The curator — consolidation of overlapping skills, a model's judgement about
what to keep, a mass prune — is planned and is not this. This epic is the
visible fact of age, the counters a curator will later read, and one automatic
action on them.

Also out: moving folders, a `.archive/` directory, restoring from an archive,
and showing any of this telemetry to the assistant.

## The reference, and where we leave it

hermes's `tools/skill_usage.py` is the epic's named reference and two of its
decisions are taken whole:

- **Telemetry goes in a sidecar, not in frontmatter.** Counters inside a
  SKILL.md would put operational data in authored content. Here the reason is
  stronger than hermes's: our frontmatter is inside the digest `Status` uses to
  tell a changed skill from an approved one, so a counter in it would mark a
  skill CHANGED every time it was used.
- **Counters are best-effort.** A sidecar that cannot be written logs at debug
  and never fails the call that was bumping it. A skill that cannot be used
  because its usage counter failed is worse than a counter that is wrong.

Where we leave hermes is the action. Its three states are driven by a curator
that MOVES a skill's folder into `.archive/`, and `pinned` exists to opt out of
that mover. We have no curator yet and we already have a switch a person
operates. **So the action is to switch a skill OFF, and nothing moves on disk.**
Nothing here breaks a recorded digest or source address, and nothing has to be
restored.

## 1. What counts as use

**A successful `skills.read` of that skill, and nothing else.**

This is exact rather than approximate. A skill's body is never copied into the
prompt — the index carries one name and one description per skill and
`systemprompt.go:37` says bodies are fetched by `skills.read` — so a skill
cannot be followed without that call.

**Inspection is not use.** Opening a skill in its tab (`skills.file`,
`skills.files`) and checking it (`skills.audit`) do not count. Otherwise
looking at a skill once would keep it young forever, which defeats the whole
measure.

## 2. Where the telemetry lives

In `skills.json` — the document beside the roots (`internal/skill/store_doc.go`)
— alongside `disabled`, `enabled`, `digests` and `sources`. A new map keyed by
skill name, like `Digests` and `Sources` are:

```json
"usage": {
  "deploy": { "count": 12, "lastUsedAt": "2026-03-03T…", "firstSeenAt": "2026-01-11T…" }
}
```

**`firstSeenAt` is when discovery first saw the skill, not when it was
installed.** A skill can be placed by hand, restored from a backup, or arrive
with a profile, and for those there is no install date at all. "When nocx first
saw it" exists for every skill however it got there.

The document's `schemaVersion` goes from 4 to 5. `usage` is optional in the
document: a version-4 document has none, and every skill in it is simply first
seen at the next discovery.

**Writes are batched, not per call.** `skills.json` is written whole under a
mutex and is today written only by a person's action. A `skills.read` on the
assistant's hot path would be the first write from that path. Counters
accumulate in memory and flush when the state changes anyway — a discovery
pass, a switch, the end of a run. A crash can therefore lose the last few
bumps, which for a threshold measured in days is not a defect worth mechanism.

## 3. Auto-off

### When it fires

Silence has exceeded the threshold, measured from `lastUsedAt` — or from
`firstSeenAt` when there has never been a use. One rule, one number, and a
newly installed skill gets the same window as everything else rather than being
switched off the day after it arrives.

### Where it is evaluated

**At discovery, with no background sweep.** The discovery walk already reads
`skills.json` and already decides whether a skill is enabled; the evaluation
joins it. A scheduler would be a second thing able to change state behind the
person's back and would raise the question of whether it ever ran.

**The honest cost, stated because the surface must not promise otherwise:** a
skill is switched off at the next discovery after its threshold passes, not on
the day it passes. For a threshold in weeks this is immaterial, and no text in
the product will claim "switched off on the thirtieth day".

A write happens only at the moment a threshold is actually crossed, which is
rare — not on every discovery.

### What is recorded

Not a name in `disabled`. Its own record, beside the telemetry: that nocx
switched it off, when, and the date the silence was measured from.

This is the load-bearing part. **`disabled` goes on meaning exactly one thing —
the person turned this off.** The document has two lists precisely so that "the
person has never touched this" is not written as "they turned it off", and
folding an automatic switch into that list would reintroduce the same loss one
level up: a person could not tell their own March decision from the product's.

### What the person sees, and how they undo it

The row says it in words: switched off automatically, unused since a date,
against a threshold. The switch is where it always was, and turning it back on
**clears the automatic mark** rather than leaving it standing. Turning it on is
a statement that this is wanted, and the silence is measured afresh from then.

### Builtins are never switched off automatically

The epic requires the refusal hermes gives `PROTECTED_BUILTIN_SKILLS`, and the
reason is ours too: a builtin backs an affordance the interface promises, and
silently switching one off turns a promise into nothing. Their counters are
still kept — knowing a builtin goes unused is worth seeing — but the switch
does not reach them.

### The assistant is not told

A switched-off skill is simply absent from the index, as today. Nothing tells
the model that a skill aged out; that would be an invitation to ask for it back.

## 4. The two pins

Two independent flags per skill, in `skills.json`, keyed by name. Flags, not
states: a skill can carry both, either or neither, and neither is a position on
a lifecycle.

- **`keepEnabled` — do not switch off.** Cancels §3 and nothing else.
- **`keepUnchanged` — do not modify.** `skills.update` and `skills.delete`
  refuse for this skill when the assistant calls them, and the future curator
  will read the same flag.

**Both protect against the machine, never against the owner.** The person's own
switch and the page's Delete button work exactly as they do now. A pin a person
has to remove before their own deliberate action is a dialog that protects
nothing.

They are genuinely separate. A skill that is rarely needed but irreplaceable
wants the first; a skill somebody tuned by hand wants the second while
remaining an ordinary candidate for switching off.

**The refusal is at execution, not at the offer.** The tools stay declared and
visible to the model; the CALL refuses, naming the skill and how to lift the
pin. Withholding `skills.update` entirely would be wrong twice: it is still
good for every other skill, and a model that is not offered a tool invents a
way around it — which is what `nocx-e28cw` was.

## 5. The setting

One field: switch off unused skills after N days, with a default and with a
"never" position that disables the mechanism entirely. It lives on the Skills
page, beside what it governs, rather than in the assistant's general settings.

The number is visible and editable because the product switches something off
by it, and a rule a person cannot see or change is one they cannot argue with.

## 6. The surface

Nothing new is built. The Skills page's `RecordRow` already carries the parts:

- **age and use** — the `detail` line: "used 12 times, last on 3 March".
- **the automatic mark** — the `status` cell, the way `Changed since
installation` sits there today.
- **the pins** — NOT two more switches in the row. The row already carries one,
  and three in a line cannot be read. They live on the skill's own tab, which
  the eye button opens (`2026-09-05-the-skill-viewer-design.md`) — where a
  person reads the whole skill before deciding anything about it.

## 7. The wire

`skills.list` gains, per skill: the use count, the last-used date, the
first-seen date, the automatic-off record when there is one, and the two pins.
Setting a pin is a new method, shaped like `skills.setEnabled`.

Every shape is declared in `contracts/` and the renderer's types are
regenerated from it, with an over-the-wire test — not a DTO test alone.

## 8. The epic's happy path

One automated check on `cmd/nocx-server`, driven through the shipped wiring:

1. A skill whose `firstSeenAt` is in the past and which has never been read,
   with the threshold set so it has passed.
2. The list is requested. The skill is switched off; the row says so in words;
   `disabled` does NOT contain its name.
3. The same skill with `keepEnabled` is not switched off.
4. A builtin with the same dates is not switched off.
5. Turning it back on clears the automatic mark, and the row stops saying it.

And a second, smaller one for the other pin: `skills.update` against a
`keepUnchanged` skill refuses and names it, while the same call against another
skill succeeds.

## Testing

- **Both ends of every interval.** Not "the mark is written when the threshold
  passes" but "the mark exists from the discovery that crossed the threshold
  until the person switches the skill back on".
- **The paired success.** For every "refuses when…" there is a "and on an
  ordinary machine it succeeds": a pinned skill refuses `skills.update` AND an
  unpinned one is updated in the same test.
- **The failure paths.** A `skills.json` that cannot be written during a
  counter flush must leave the call that triggered it successful; a document
  that cannot be read must not switch anything off.
- **Falsifiability.** Each happy-path assertion is confirmed to go red under a
  deliberate probe before it is believed.

## Open questions

None.
