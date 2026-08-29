# W6 — the epic's gate: one check that watches a person do it (nocx-9wsb5)

Read `.internal/briefs/_common.md` first. Then §1 and §9 of
`.internal/specs/2026-08-29-notification-centre-refinements-design.md`, in full.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
e2e/notification-centre.spec.ts     (new — the only file you create)
```

You may READ anything. You may modify no other file. If the product needs a fix for
this spec to pass, **do not make it** — report it and stop; a coordinator decides
whether the spec or the product is wrong, and that decision is not a worker's.

## Why this file exists

AGENTS.md rule 2: an epic that is not a chore proves its happy path, and closes only
when one automated check has watched a user do the thing end to end. Every other task
in this epic proved its own unit. **None of them proved the sentence.** That is the
gap this file closes, and it is the last thing between the epic and DONE.

The sentence, from design §1:

> A terminal bell and a finished command are raised in a named tab while the window is
> elsewhere. The bell badge shows a legible count. Every row names its kind in words
> ("Terminal bell"), and the session filter offers the tab's title — no kind badge and
> no filter option anywhere in the panel contains a dotted slug or a session id.
> Clicking a row focuses its tab. Turning "Terminal bell → Notification centre" off in
> Settings removes those rows and lowers the count; turning it back on restores every
> such row the feed still holds, including ones raised while it was off, each with the
> read state it had.

## Read these first — they are the shape you are copying

- `e2e/notification-channels.spec.ts` — the closest relative: it drives the Settings
  routing matrix by clicking the cell where a row meets a column, and its header
  explains why it refuses to write the setting through the store. **Do the same.** A
  spec that flipped the toggle underneath the surface would pass on a build whose
  Settings page offers nothing at all.
- `e2e/activity-bell.spec.ts` — how a bell is produced from a real shell, and how it
  waits on a gate file rather than on a duration.
- `e2e/harness.ts` — `promptReady`, `settingsReady` and what a stand gives you.

## The rules that will bite you

**Never wait on a duration.** AGENTS.md: a test may not depend on timing. Wait on an
observable state change — a row appearing, a count changing, a class landing. A spec
that needs a slow machine to pass is broken on a fast one and has only not been caught
yet. `activity-bell.spec.ts` shows the gate-file technique for ordering an event after
a state, and its comments explain what `sleep 3` cost.

**The identifier assertions are narrow on purpose.** Assert that no **kind badge** and
no **session filter option** contains a dotted slug or a hex run. Do **not** assert it
over the whole panel's text: `title` and `body` are untrusted presentation data
(`contracts/notify.feed.read.schema.json`) and may legitimately carry a commit hash, so
a whole-panel regex would fail on an ordinary notification about a build.

**Read state is part of the claim.** "Restores every such row" is not enough — §1 says
"each with the read state it had". Mark nothing read in the middle unless you assert
what that does.

**"Every such row the feed still holds"** is the retention interval of the accepted
centre design §7, not a hedge you may drop: an occurrence lives from `Admit` until
eviction or process exit. Raise few enough that eviction cannot occur, so the claim is
exact rather than approximately true.

## What to verify, and what NOT to run

Run **only your own spec**, by name, on the container path:

```
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/notification-centre.spec.ts
```

That is the one exception to `_common.md`'s no-heavy-gates rule, and it is scoped to
one file: you cannot write an e2e spec you have never seen run. Do **not** run the
whole suite, `make ci`, `make ci-full`, or the other containerized scripts.

**The container's failure set is not CI's** (AGENTS.md): it runs Linux WebKit at a
container default viewport, and layout-sensitive specs fail there and pass in CI. If
your spec is red only on geometry, say so in your report rather than reshaping the
assertion to make the container happy — you would be fixing a test that is lying.

Also run, since you are creating a TypeScript file:

```
cd frontend && ./node_modules/.bin/tsc --noEmit -p tsconfig.json
```

## Report

Numbers, not adjectives. How many times you ran it and how many passed. Any assertion
you wanted to make and could not, with the reason. Anything you saw in the product that
looks wrong but was out of your scope — that is a finding, and silence is not the same
as nothing to report.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w6-e2e

If you cannot finish, print instead:

    NCBLOCK-3f7a::w6-e2e <one line why>
