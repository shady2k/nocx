# W1 — the kind vocabulary and the centre's settings (nocx-ixk5q, nocx-av5og)

Read `.internal/briefs/_common.md` first. Then design §4 and §5, in full.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
internal/notify/catalogue.go
internal/notify/catalogue_test.go
internal/app/app.go
internal/transport/**            (the new read method and its test)
contracts/notify.catalogue.schema.json          (new)
contracts/notify.feed.read.schema.json          (one description only)
frontend/src/generated/notify.catalogue.ts      (generated, never hand-edited)
e2e/notification-channels.spec.ts               (label migration only)
```

**Files other workers own — escalate, do not edit:** `frontend/src/ui/**`,
`frontend/src/styles/**`, `frontend/src/settings-domain.ts`, `frontend/src/settings.tsx`,
`frontend/src/notify/**`, `frontend/src/main.tsx`, `frontend/src/sidebar.tsx`.

## Part A — `notify.catalogue` (nocx-ixk5q)

`Catalogue` today keeps `channels` and `pairs` and **drops** the `kinds` slice it was
constructed with (`catalogue.go:138`). Retain it, and expose it as `PresentedKinds`.

**Reconstructing the kinds from `Pairs()` is forbidden** and that is the point of the
whole task: a kind whose trust bound leaves it with no offered pair would vanish from the
vocabulary while still being raisable, and its rows would render with no name.

`catalogue.go:247` says there is deliberately no `Kinds()`, because a kind list beside
`Pairs()` would be a second answer to _"what can be routed"_. `PresentedKinds` answers a
different question — _"what may a raised event be called"_ — so it is named for that
question, and you rewrite that comment to say which accessor answers which, rather than
to forbid a name.

**The copy becomes deep.** `RoutableKind` holds two nested slices, `Trusts` and
`DefaultChannels` (`catalogue.go:69`); the existing `Pairs()` copy is shallow
(`catalogue.go:256`), so a caller can already reach through a returned `Pair` and rewrite
the catalogue's trust declaration. `TestCatalogueAccessorsHandOutCopies`
(`catalogue_test.go:251`) does not see it because it mutates only scalars. Deep-copy on
construction, on `PresentedKinds`, and on the kinds nested inside `Pairs`.

**The wire.** New JSON-RPC read `notify.catalogue`, returning per kind: its wire `kind`,
its `label`, its `description`. Schema in `contracts/`, with
`additionalProperties: false` and an explicit `required` — a schema without both is
theatre. Generate the renderer type (`cd frontend && npm run contracts`), never hand-edit
it. Validate the Go side against the schema.

**Labels become noun phrases** — they are sentence fragments today because their only
reader composed `"<kind> → <channel>"`:

| id                 | new label                    |
| ------------------ | ---------------------------- |
| `blockFinished`    | Command finished             |
| `sessionEnded`     | Session ended                |
| `transferFinished` | File transfer finished       |
| `programNotify`    | Program notification request |
| `bell`             | Terminal bell                |
| `paneWorkFinished` | Work seems to have finished  |

Descriptions are unchanged and stay sentences.

## Part B — the centre's settings (nocx-av5og)

Six settings keyed `notifications.centre.<kindId>`, registered in `app.go` by a loop over
the same `DefaultCatalogue()` the routing toggles use — over `PresentedKinds`, so a kind
with no routing pair still gets its visibility toggle. **No kind is enumerated by hand in
that file today and none is added.** All six default **ON**.

Four conditions, each of which must be a test because none is enforced by the code:

1. **Same `Section`** as the routing settings (`notify.RouteSettingSection`) —
   `sectionBlocks` in the renderer is called per section.
2. Label is exactly `<kind label> → Notification centre`. The renderer's matrix takes its
   axis labels from the label, not from the key.
3. **No second `RegisterSectionGroup`.** `app.go:568` already registers it and a second
   call panics (`settings.go:195`).
4. Registered **after** the routing toggles. Rows and columns are first-seen order and
   the backend preserves package-init order — this is what puts the column third.

**Two descriptions.** `BoolSpec` carries a `Description` (`settings.go:212`) and the
matrix renders it as the cell's tooltip. It is not validated as non-empty, so leaving it
blank is refused here rather than by the registry.

- The **centre** cell: the event is recorded either way; this governs whether the panel
  shows it and whether the bell counts it; turning it back on brings back what the feed
  still holds.
- The **delivery** cell's closing sentence is today "With every channel off, this kind
  reaches nothing" (`catalogue.go:131`). Beside a third column that may be on, that is a
  lie the user can see. It becomes: `With every delivery channel off, this kind reaches
no channel — it is still recorded in the notification centre.`

## What you must NOT do

`Ingress`, `Policy`, `Router`, `RoutingSource` and `Feed` are **not modified**. If your
change needs one of them, stop and escalate — design §0 records three ways that road was
already tried and failed, and you would be walking the fourth.

## Assertions

- `PresentedKinds` returns a kind with **no offered pair** — a **unit** test on a
  purpose-built catalogue (a heuristic kind plus a network-only channel). It cannot be
  tested on the shipped catalogue: both its channels are local (`catalogue.go:372`) so
  every shipped kind is offered at least one pair. Do not inject a catalogue into the
  transport to make one test do both jobs.
- A **real-socket** test — `…_OverTheWireConformsToContract`, validating the actual
  result off the actual socket, not a payload the test built — asserts conformance and
  the shipped presented set exactly.
- Mutating a nested `Trusts` slice through a returned `Pair`, **and** through
  `PresentedKinds`, leaves the catalogue unchanged.
- The registered `notifications.centre.*` key set equals the presented kind ids exactly.
- Section, label shape, single `RegisterSectionGroup`, registration order: four tests.
- `e2e/notification-channels.spec.ts:113` reads the real shipped label — migrate it.
  Do not run the e2e suite; a compile/type check of the spec is what is asked.

## Verification, scoped

```
go build ./...
go vet ./internal/notify/... ./internal/app/... ./internal/transport/...
go test ./internal/notify/... ./internal/app/... ./internal/transport/...
cd frontend && npm run contracts:check && ./node_modules/.bin/tsc --noEmit -p tsconfig.json
```

Nothing wider. No `make ci`, no e2e run.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w1-go

If you cannot finish, print instead:

    NCBLOCK-3f7a::w1-go <one line why>
