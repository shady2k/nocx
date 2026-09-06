# ADR-0059 — Retire the wave vocabulary from the worker tool surface

- **Status:** Accepted
- **Date:** 2026-09-06
- **Owner request:** Retire the `wave` vocabulary before the tool surface is exposed to an external model.
- **Related:** AD-8 (one owner per behaviour), ADR-0028 (the dispatcher narrows; it does not check), ADR-0056 (supervision outlives the coordinator, not the backend), ADR-0058 (authority is bound to a unit of work).
- **Design:** `.internal/specs/2026-09-05-the-tool-surface-at-launch-design.md` and `.internal/specs/2026-09-03-the-waves-authority-model-design.md`, both updated with the live vocabulary.

## Context

The six orchestration tools were named `wave.*`, although the model contains no wave
object. There is no `wave.create`, no wave id, and no wave-owned authority: `holdings`
is keyed by the caller's session, while the records and capabilities describe workers.
The prefix came from the team's phrase "a wave of workers". That phrase is useful for
describing a batch of work; it is not an API noun.

The distinction matters before the names become an external contract such as
`mcp__nocx__wave_spawn`. A model should be offered names that describe the objects it can
actually address.

## Decision

The public tool namespace is `workers.*`. The implementation packages and exported
assistant seams use tool and worker vocabulary. `holdings` keeps its name because it is
the question the caller asks, not a `wave` noun that needs renaming.

The old names map to the following live names:

| Old | New |
| --- | --- |
| `wave.spawn`, `wave.wait`, `wave.holdings`, `wave.say`, `wave.close`, `wave.inbox` | `workers.spawn`, `workers.wait`, `workers.holdings`, `workers.say`, `workers.close`, `workers.inbox` |
| `internal/wave` | `internal/workers` |
| `internal/waveendpoint` | `internal/toolendpoint` |
| `internal/wavepin` | `internal/peerpin` |
| `assistant.WaveDispatcher` | `assistant.ToolDispatcher` |
| `assistant.WaveInvocation` | `assistant.ToolInvocation` |
| worker-record fields and helpers that named a wave | names describing the worker group or tool surface |

The six schemas under `contracts/tools/`, the Go declaration table, the endpoint, and
live generated contract types use the new names. The worker notification kind is also
`workers.undispatched`, so a live wire identifier does not preserve the retired prefix.

## Why history is left alone

`.internal/briefs/`, `.internal/plans/`, `.internal/reports/`, and every pre-existing
ADR are records of what was decided and implemented at the time. Rewriting their old
names would falsify those records and break their citations. The two live specs named
above are different: they drive open work, so their names were updated and each carries a
retirement note dated 2026-09-06.

## Consequences

- External callers and model prompts inherit `workers.*`; no compatibility aliases for
  `wave.*` are provided. A second namespace would recreate the two-vocabulary defect.
- Code search for `wave` in Go, TypeScript, TSX, JSON contracts, and generated types is
  now a useful proof that the retired API noun did not survive in live code.
- Historical prose may still say `wave` when it records the old design. A reader must use
  this ADR and the two amended live specs to translate those citations.

## What the next person inherits

The next tool added to this surface belongs under `workers.*` only if it addresses a
worker capability; it must not revive `wave.*` as a synonym or introduce a second batch
object. Keep the declaration, schema, generated types, endpoint, and acceptance proof on
one vocabulary. If the model later gains a real batch object, that is a new decision with
an explicit object identity and lifecycle, not a reason to restore the retired prefix.
