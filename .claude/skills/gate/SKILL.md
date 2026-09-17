---
name: gate
description: Check the backlog against its invariants, and apply the checks a script cannot. Use before filing an issue or an epic, before taking work, when grooming, and whenever the ready queue has stopped being an answer — hundreds of issues ready at once, statuses nobody trusts, epics that never finish.
---

# The backlog gate

A backlog stops being a queue quietly. Nothing breaks; issues simply accumulate
until "what do I work on next" has hundreds of answers, which is the same as
none. Measured on 2026-09-17 in the repository that bought these rules: 933
issues open, **773 of them ready to work** across 665 independent roots, 82 of
83 in progress untouched for over two days, 399 open items untouched for thirty.

`check.mjs` applies the ten invariants that state was missing. It knows no
tracker: input is the normalized backlog of [`model.md`](model.md), and turning
your tracker into that is an adapter's job.

## Run it

```bash
node .claude/skills/gate/adapters/beads.mjs | node .claude/skills/gate/check.mjs -
```

Read ages from before a bulk edit by pointing the adapter at an earlier
revision — the export is tracked, so any revision works:

```bash
node .claude/skills/gate/adapters/beads.mjs --at 4dacfb96 | node .claude/skills/gate/check.mjs -
```

`--json` for every violation rather than the first twelve, `--only <id,id>` for
one check, `--help` for the list.

## Act on what it says

Each violation prints **why** the rule exists and **fix**, the move that clears
it. Work from those, not from the count: the rules exist to be applied with
judgement, and a violation you can defend is a violation you close by amending
the config, not by editing the backlog to please a script.

Two of them are load-bearing and worth knowing before you see them:

- **`off-milestone-open`** is the horizon. A milestone that is not current stays
  deferred, decomposed no further than feature level. 802 issues were deferred
  in one pass for having been planned past that horizon, and most described a
  build that no longer existed.
- **`stale-hold`** is the status nobody trusts. Active means somebody is holding
  it now; set it back to open the minute you stop, or it is invisible to the
  queue and to every colleague looking for work.

## Ages after a bulk edit

The report always names timestamp **clusters** — minutes shared by many issues —
because a bulk status change rewrites every timestamp it touches. Deferring 802
issues made epics untouched for 45 days read as active today, and the analysis
that followed believed it. When a cluster is printed, re-run the adapter with
`--at <revision from before it>` before believing any age.

## Before you file, and the script cannot help you

Three checks belong to whoever is filing:

1. **Search the behaviour, not your name for it.** A split-panes epic was filed
   twice because the searches were "split", "pane" and "panes" while the
   existing issue was titled "Drag one tab onto another and watch both at
   once". When you cannot phrase it two ways, read the whole area listing.
2. **Write the criterion as something that stops being false exactly once**,
   and what would falsify it. `epic-without-criterion` finds a missing heading;
   only you can tell a criterion from a paragraph.
3. **State what is deliberately out.** An epic with no stated exclusions
   absorbs every new issue in its area until it can never finish.

## Adapting it

`adapters/` and `config.json` are the only files that know anything outside this
skill; everything else is portable and a self-test enforces that.

- **A new tracker** is one adapter producing [`model.md`](model.md)'s shape.
  Read its two warnings first: `blockedBy` carries gating edges only, and
  closed issues are emitted rather than filtered.
- **A new project** is `config.json`: its label vocabulary, its current
  milestone, its thresholds, and `projectWords` — the names that may never
  appear in the portable half.

```bash
node .claude/skills/gate/check.mjs --selftest
```

Every rule has a fixture that violates it and must fire exactly the checks that
fixture declares, plus clean backlogs that must fire nothing, plus the
portability guard. A fixture declaring more than one check is declaring a real
overlap: `idea-blocks-work` cannot be built without also tripping
`blocked-by-deferred`, because an idea belongs deferred. Add a rule, add its
fixture in the same commit — a gate nobody proved is a gate that certifies.
