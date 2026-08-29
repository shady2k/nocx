# W3 — the settings matrix parser serves both notification namespaces (nocx-enetk)

Read `.internal/briefs/_common.md` first. Then design §5, in full.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
frontend/src/settings-domain.ts
frontend/src/settings-domain.test.ts
frontend/src/settings.test.ts        (fixture labels only)
frontend/src/settings.tsx            (only if the rename forces an import change)
```

**Files other workers own — escalate, do not edit:** all Go, `contracts/**`,
`frontend/src/ui/**`, `frontend/src/styles/**`, `frontend/src/notify/**`,
`frontend/src/main.tsx`, `frontend/src/sidebar.tsx`.

## What changes

A second settings namespace is arriving from the backend, `notifications.centre.<kindId>`
— one toggle per event kind, saying whether the notification centre shows that kind. It
is **not** a route: it delivers nothing, it is not a channel, and it exists for a kind
that has no offered routing pair at all. That is why it does not live under
`notifications.route.*`, and why the parser is what pays for the honesty.

`parseRouteSettingKey`, `RouteCell` and the surrounding comments in `settings-domain.ts`
(from about `:373`) become notification-**matrix** vocabulary, because after this they
serve both namespaces. Leaving routing names on a parser that no longer only parses
routes satisfies "look for the existing answer" in the letter and makes the API's meaning
false.

The parser accepts both:

```
notifications.route.<kindId>.<channelId>   ->  cell (<kindId>, <channelId>)
notifications.centre.<kindId>              ->  cell (<kindId>, "centre")
```

Everything else about `sectionBlocks` (`:454`) is unchanged: rows and columns stay
**first-seen order**, a cell key on a non-toggle still renders as an ordinary setting row
rather than being dropped, and `cell()` still answers `undefined` where the backend
offers no pair.

## The one thing to be careful about

The matrix takes its **axis labels from the declaration's label**, not from the key
(`splitCellLabel`, `:507`), and **the first declaration seen wins** (`:473`). The backend
registers the centre toggles with the label `<kind label> → Notification centre`, using
the same kind labels the routing toggles carry, so the two agree — but nothing enforces
it. A row label that silently differs between the two namespaces would be invisible.
Assert it.

## The label migration

Another worker is changing the backend's kind labels from sentence fragments to noun
phrases, so a badge and a settings row can say one word for one concept:

| old                                | new                                     |
| ---------------------------------- | --------------------------------------- |
| A command finished                 | Command finished                        |
| A session ended                    | Session ended                           |
| A file transfer finished           | File transfer finished                  |
| A program asked for a notification | Program notification request            |
| A terminal bell                    | Terminal bell                           |
| Work seems to have finished        | Work seems to have finished (unchanged) |

Your fixtures in `settings-domain.test.ts` (from `:392`) and `settings.test.ts` (from
`:1139`) are synthetic — they do not read the catalogue — but they are written in the old
wording. Move them, so the fixtures and the product do not drift apart.

## Assertions

- Both namespaces fold into **one** matrix block, with `centre` as a column of it.
- A malformed key in **either** namespace is still refused and renders as an ordinary
  setting row — same refusal, same reason.
- Column order is first-seen, so registration order decides `banner`, `toast`, `centre`.
- A kind with only a centre key still produces a row (its route cells are `undefined`);
  a kind with only route keys still produces a row with an undefined centre cell. The UI
  carries asymmetry without breaking — assert it rather than assuming it.
- Row labels agree across the two namespaces, and the first-seen rule is what makes a
  disagreement invisible.

## Verification, scoped

```
cd frontend
./node_modules/.bin/tsc --noEmit -p tsconfig.json
./node_modules/.bin/vitest run src/settings-domain.test.ts src/settings.test.ts
```

Nothing wider.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w3-settings

If you cannot finish, print instead:

    NCBLOCK-3f7a::w3-settings <one line why>
