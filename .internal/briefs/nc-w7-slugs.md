# W7 — three e2e specs still assert the wire slug (nocx-9wsb5, nocx-a9gf4)

Read `.internal/briefs/_common.md` first, then §4 of
`.internal/specs/2026-08-29-notification-centre-refinements-design.md`.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
e2e/notification-block-finished.spec.ts
e2e/notification-centre-grouping.spec.ts
e2e/notification-centre.spec.ts
```

Everything else is read-only, including the product. **If a spec fails for any reason
other than the wording below, stop and report it** — do not change product code, and do
not reshape an assertion to make it pass.

## What happened

The notification centre no longer shows a person `block.finished` or `program.notify`.
Those are wire identifiers, and putting them in front of a user was the defect
`nocx-a9gf4` was filed for. The kind badge now reads the catalogue's noun phrase, with
the catalogue's sentence as its `title`.

These three specs assert the old wording, so they fail in the containerized suite — six
failures, three specs across two browsers. **They are not regressions: they are tests
asserting the defect**, which `nocx-a9gf4`'s acceptance criterion says must move with it.

Observed, from the container run:

```
> 207 |   await expect(badgeOf(okRow)).toHaveText('block.finished')
        locator resolved to <span class="ui-badge" data-tone="success"
        title="nocx's own block ledger recorded that a command finished.">Command finished</span>

waiting for ... filter({ has: getByText('program.notify', { exact: true }) })
        locator resolved to 0 elements
```

## The mapping — read it from the source, do not invent it

The labels and the descriptions are declared in `internal/notify/catalogue.go`. Read them
there. As of this change:

| wire kind           | badge text                   |
| ------------------- | ---------------------------- |
| `block.finished`    | Command finished             |
| `session.ended`     | Session ended                |
| `transfer.finished` | File transfer finished       |
| `program.notify`    | Program notification request |
| `bell`              | Terminal bell                |
| `pane.workFinished` | Work seems to have finished  |

A badge's `title` is that kind's description sentence from the same file.

## What to change, and what not to

Change **only the expected strings** — the assertions themselves are right and their
reasoning in the surrounding comments stays. Where a comment explains why the spec looks
at a badge, leave it; where a comment quotes the old slug as the thing being asserted,
update the quote so the prose and the code agree.

`e2e/notification-channels.spec.ts` was already migrated and is **not** yours — do not
touch it.

Do not add an assertion on the badge `title` unless a spec already had one. Growing these
gates is not this task; `e2e/notification-centre-choice.spec.ts` is the epic's own gate
and it already covers the vocabulary.

## Verification

The three specs, both browsers, in the container — nothing wider:

```
e2e/run-in-container.sh e2e/notification-block-finished.spec.ts e2e/notification-centre-grouping.spec.ts e2e/notification-centre.spec.ts
```

That is the one exception to `_common.md`'s no-heavy-gates rule and it is scoped to three
files. Do not run the whole suite, `make ci`, `make ci-full` or the other containerized
scripts.

Report the counts: how many failed before, how many pass after, per browser.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w7-slugs

If you cannot finish, print instead:

    NCBLOCK-3f7a::w7-slugs <one line why>
