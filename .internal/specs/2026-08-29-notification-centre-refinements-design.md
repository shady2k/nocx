# Notification centre — refinements

- **Date:** 2026-08-29
- **Owner:** shady2k
- **Bead:** `nocx-yzoku` (brainstorming session)
- **Amends:** §6 of [the notification centre design](2026-08-22-notification-centre-design.md),
  and the description of `unreadCount` in `contracts/notify.feed.read.schema.json`. §5 of
  that design — **unfiltered ingest** — is untouched and is what §5 below is built to
  protect
- **Reviewed by:** three adversarial passes against the code (codex), 2026-08-29. The first
  overturned the whole of the fifth defect's design and produced the `Amends` line; the
  second found twenty-four issues; the third found eight decisions the plan would otherwise
  have had to take on its own. All are settled inline, at the decision each one changed

## 0. What was designed first, and why it was wrong

The owner reported five defects with screenshots. The fifth — "Settings cannot say what
goes into the notification centre" — was first designed as a **third routing channel**:
`centre` joins `banner` and `toast` in the catalogue, and `Ingress` consults it before
`feed.Add`.

That design was wrong three times over, and the wrongness is the useful part.

- It would have reversed [centre design §5](2026-08-22-notification-centre-design.md),
  **Unfiltered ingest** — "Every raised event enters the feed. Policy decides which
  channels fire; it never decides membership" — as a side effect of adding a column. The
  five checks of AGENTS.md exist to catch exactly that, and they were not run against
  that spec before the design was written.
- `RoutableChannel.Delivers bool` is the flag parameter AD-8 forbids in as many words:
  "No mode strings, flag parameters or type tests selecting between behaviours."
- It would not have started. `RoutingSource.build()` reads `s.sinks[p.Channel.ID]` for
  every enabled pair (`routingsource.go:144`); for a channel with no sink that is `nil`,
  and `validateTable` rejects a route with no sink, so `NewRouter` never returns
  (`notify.go:349`). Loosening the sink-coverage check in `NewRoutingSource` — the
  obvious repair — would not have been enough.

The owner settled it in one sentence: **"В центр попадает всё и всегда. Но это можно не
отображать. Я говорю про UI, а не про изменение инварианта."**

So `Ingress`, `Policy`, `Router`, `RoutingSource` and `Feed` are not modified by this work
at all. The fifth defect is a **presentation** decision, and §5 is written so that it can
never quietly become a membership one.

## 1. What a user can do that they could not before

**Read their notification centre without decoding nocx's identifiers, reach the tab a
notification came from, see the unread count, and choose which kinds of event the centre
shows them.**

The end-to-end check that watches it (AGENTS.md rule 2), written now rather than at the
end:

> A terminal bell and a finished command are raised in a named tab while the window is
> elsewhere. The bell badge shows a legible count. Every row names its kind in words
> ("Terminal bell"), and the session filter offers the tab's title — no kind badge and no
> filter option anywhere in the panel contains a dotted slug or a session id. Clicking a
> row focuses its tab. Turning "Terminal bell → Notification centre" off in Settings
> removes those rows and lowers the count; turning it back on restores **every such row
> the feed still holds, including ones raised while it was off**, each with the read state
> it had.

"Still holds" is not a hedge, it is the retention interval of
[centre design §7](2026-08-22-notification-centre-design.md): an occurrence exists from
`Admit` until **eviction or process exit**, and a collapsed constituent may be dropped from
its row's tail before the row itself goes, leaving only `runDropped`. A visibility toggle
neither shortens nor extends that. The spec would be promising something the feed cannot do
if it said "every row raised in between", and an implementer would have had to discover
that.

## 2. Boundaries this crosses, and what they already decided

| Boundary                                                         | Already decided                                                                        | This design                                                                                                                                                                                                                 |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **[Centre design §5](2026-08-22-notification-centre-design.md)** | Unfiltered ingest: every raised event enters the feed; policy never decides membership | **Unchanged, and guarded.** §5 filters the view, never the feed. §9's regression stands on the application seam so a future shortcut fails there first                                                                      |
| **[Centre design §6](2026-08-22-notification-centre-design.md)** | `feed.unreadCount` is the one source of truth for the bell and the dock badge          | **Amended.** §6 splits the wire field from the renderer's derivation and says which surface reads which                                                                                                                     |
| **[Centre design §7](2026-08-22-notification-centre-design.md)** | An occurrence exists from `Admit` until eviction or process exit                       | Unchanged, and §1's acceptance criterion is written inside it                                                                                                                                                               |
| **AD-8**                                                         | One owner per behaviour; variation via the interface, never a flag                     | The kind vocabulary gets one owner (`catalogue.go`) served over the wire. The session name is read off the pane the **activation lookup already found**, so no second lookup is created — and none is redefined either (§7) |
| **ADR-0033**                                                     | UI state is a document on the Go side; localStorage may not carry facts                | The visibility choices are settings in the backend registry, like every other setting                                                                                                                                       |
| **AGENTS.md — kit rules**                                        | A surface may place a kit component and may never repaint it                           | §8's three repairs all land in `ui/`; `sidebar.css` gains no colour                                                                                                                                                         |
| **AGENTS.md rule 5**                                             | Every JSON-RPC result shape is a schema in `contracts/`                                | §4 adds `notify.catalogue`; §6 corrects an existing schema description this work makes untrue                                                                                                                               |

## 3. The five defects, and the one that was not what it looked like

| #   | Symptom the owner saw               | Actual cause                                                                                                                                                                            |
| --- | ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | The Session filter lists 64-hex ids | `notifications-panel.tsx:133` labels the option `dotted(o.host, o.sessionId)`                                                                                                           |
| 2   | The badge reads `bell`, unexplained | `notifications-panel.tsx:271` passes the wire enum through as the label. Filed as `nocx-a9gf4`                                                                                          |
| 3   | Rows cannot be clicked              | **The rows ARE clickable.** `.ui-collection-row` sets `cursor: default` unconditionally (`collection-view.css:49`) and the kit has no activatable variant, so nothing on screen says so |
| 4   | The count badge is not visible      | `Badge tone="info"` is the accent at 80% transparency, pinned over the glyph in a 48px rail                                                                                             |
| 5   | Settings cannot govern the centre   | The catalogue declares two channels, both delivery. There is no visibility concept at all                                                                                               |

Defect 3 is the one worth writing down. `onActivate` is passed for every live row, the row
takes `tabIndex=0`, and Enter and Space both work — proven by
`notification-activation.test.tsx:139`, which drives a real `PaneManager`. A green suite,
a working feature, and the user still could not find the affordance. **A test that asserts
a user CAN do something does not assert they can SEE that they can.**

## 4. The kind vocabulary: one owner, served over the wire

`internal/notify/catalogue.go` already holds the human words for all six kinds. Writing
them a second time in TypeScript is what AGENTS.md's "look for the existing answer"
forbids, and the two copies would agree until the day somebody edited one.

**A new JSON-RPC read, `notify.catalogue`**, returns for each kind its wire `kind`, its
`label` and its `description`. Contract in `contracts/notify.catalogue.schema.json`,
renderer type generated from it, Go side validated against it, and the over-the-wire
conformance test AGENTS.md rule 5 requires.

**The catalogue does not currently hold what this method returns.** `Catalogue` stores
`channels` and `pairs` only (`catalogue.go:138`); the `kinds` slice `NewCatalogue` was
given is consumed and dropped. So this is not an accessor over existing state — the type
must retain the declarations. **Reconstructing the kinds from `Pairs()` is forbidden**, and
that is the whole reason the field is added: a kind the trust bound leaves with no offered
pair would vanish from the vocabulary while still being raisable, and its rows would render
with no name.

**Retaining them makes an existing immutability claim load-bearing, and it is currently
false.** `RoutableKind` holds two nested slices, `Trusts` and `DefaultChannels`
(`catalogue.go:69`), and the existing `Pairs()` copy is shallow (`catalogue.go:256`) — a
caller can already reach through a returned `Pair` and rewrite the catalogue's trust
declaration. The immutability test does not see it because it mutates only scalars
(`catalogue_test.go:251`). So: **the copy is deep** — on construction, on `PresentedKinds`,
and on the kinds nested inside `Pairs` — and the test mutates a nested slice. A type that
calls itself immutable and is not is worse than one that never claimed it.

**It is not the `Kinds()` the catalogue refuses.** The comment at `catalogue.go:247` says
a kind list beside `Pairs()` would be a second answer to _"what can be routed"_. This
method answers _"what may a raised event be called"_ — every declared kind, including one
whose every routing cell is off, because enabled/disabled is registry state and the
vocabulary is not. It is therefore named for its own question (`PresentedKinds`), and that
comment is rewritten to say which question each accessor answers rather than to forbid a
name.

**One register for one concept.** The catalogue's labels are sentence fragments today —
`"A terminal bell"`, `"A command finished"` — because their only reader composes
`"<kind> → <channel>"` for a settings row. A badge reading "A terminal bell" is wrong, and
a badge reading "Terminal bell" beside a settings row reading "A terminal bell" is two
vocabularies for one concept, which is the defect this section exists to remove. So **the
catalogue's labels become noun phrases** — "Terminal bell", "Command finished", "Session
ended", "File transfer finished", "Program notification request", "Work seems to have
finished". No key changes, so no persisted value moves.

**The label change has consumers, and they are named here so they are a migration and not
a surprise:** `e2e/notification-channels.spec.ts:113` reads the real shipped label;
`settings.test.ts:1139` and `settings-domain.test.ts:392` use their own synthetic fixtures
in the same wording and should move with it so the two do not drift.

**Degradation, with both ends.** From the first render **until the first successful
`notify.catalogue` read** — through any number of failures and reconnects — a kind with no
entry renders as its wire value with dots **and camel-case boundaries** split and the first
letter capitalised: `pane.workFinished` → "Pane work finished". One function, in one
renderer utility, used by both the badge and the filter so they cannot disagree. Never the
raw slug, never blank. The result is held in a Solid signal, so a kind that arrived in a
push before the catalogue did re-renders when it lands.

## 5. What the centre SHOWS — a view, and the sentence that keeps it one

Six new settings, keyed `notifications.centre.<kindId>`, registered in `app.go` by a loop
over the same `DefaultCatalogue()` the routing toggles loop over. No kind is enumerated by
hand in that file today and none is added. All six default ON, so nothing about anybody's
centre changes on the day this lands.

**The key namespace is `centre` and not `route`.** `notifications.route.*` means an
offered delivery pair, subject to the trust bound and resolved to a sink. Visibility
delivers nothing, is not a channel, and exists for a kind with no offered routing pair at
all. Registering it under `route` would have made the third column appear with no renderer
change — the matrix derives its columns from the declared keys — and would have made the
persisted key a lie, and a trap for the next code that enumerates route keys.

The renderer pays a small change instead, and **it is a rename, not an addition**:
`parseRouteSettingKey`, `RouteCell` and the surrounding comments in `settings-domain.ts`
become notification-**matrix** vocabulary, since after this they serve both namespaces.
Leaving routing names on a parser that no longer only parses routes would satisfy "look
for the existing answer" in the letter and make the API's meaning false.

Four conditions make the third column land in the same matrix rather than beside it, and
each is an assertion in §9 because none is enforced by the code today:

- The centre settings carry the **same `Section`** as the routing settings
  (`notify.RouteSettingSection`), because `sectionBlocks` is called per section.
- Their label is exactly `<kind label> → Notification centre`, because the matrix takes
  its axis labels from the label, not from the key (`settings-domain.ts:472`).
- The registration does **not** call `RegisterSectionGroup` again — the routing helper
  already does (`app.go:568`), and a second call panics (`settings.go:195`).
- The centre toggles are registered **after** the routing toggles. Rows and columns are in
  first-seen order and the backend preserves package-init order, so this is what puts the
  column third; nothing in the renderer knows a preferred order.

**Two descriptions have to be written, and one of them is an existing sentence that this
work makes wrong.** `BoolSpec` carries a `Description` (`settings.go:212`) and the matrix
renders it as the cell's tooltip (`settings.tsx:1177`). It is not validated as non-empty,
which is why leaving it blank has to be refused here rather than by the registry.

- The centre cell says what it does and what it does not: the event is recorded either
  way; this governs whether the panel shows it and whether the bell counts it; turning it
  back on brings back what the feed still holds.
- The delivery cell's closing sentence is today "With every channel off, this kind reaches
  nothing" (`catalogue.go:131`). Beside a third column that may be on, that reads as a lie
  the user can see. It becomes "With every delivery channel off, this kind reaches no
  channel — it is still recorded in the notification centre."

**The invariant this design must not break, stated with both ends:** from `Admit` until
eviction or process exit, the occurrence is in the feed, whatever any setting says. A
visibility toggle changes which rows the panel draws and which the bell counts; it changes
nothing about what `Feed.Add` was given.

**Delivery-failure rows follow their kind, and need no rule of their own.**
`Feed.RecordDeliveryFailure` stamps the failed event's kind (`feed.go:327`), so hiding
"Terminal bell" hides "Not delivered: <a bell>" with it. Under a membership design that
would have needed a decision — a complaint about a broken banner is a fact about the host,
and losing it to a kind toggle would have broken AGENTS.md's "a soft degrade must be
visible in the product". Under a view design the question dissolves: the row is in the
feed, one toggle away, and no degrade is invisible. **This is the second place the
view/membership distinction pays for itself, and it is why the parser change is worth it.**

## 6. What the bell counts — the amendment

[Centre design §6](2026-08-22-notification-centre-design.md) made `feed.unreadCount` the
single source of truth for the bell and the dock badge, and the wire schema states it more
strongly still: "the ONE number the bell badge and the dock badge both read". After this
work there are two numbers, and pretending otherwise would leave the contract asserting
something untrue. **This section is that amendment, and the schema's description is
corrected in the same commit.** The wire _shape_ does not change; no field is added,
removed or retyped.

| Number                               | Means                                           | Read by                                                       |
| ------------------------------------ | ----------------------------------------------- | ------------------------------------------------------------- |
| `NotifyFeedRead.unreadCount` (wire)  | Every retained occurrence with `readAt == null` | The store's reconcile, and §9's equality assertion            |
| `FeedStore.unreadCount()` (renderer) | The same, restricted to kinds the centre shows  | The bell badge, and the dock badge when `nocx-3a40` builds it |

**One formula, not a correction term.** The renderer count is a memo over `occurrences()`
and the hidden set — count the visible rows whose `read` is false — and **not**
`wireUnread − hiddenUnread`. The store holds occurrences and the wire count in two
independent signals (`feed-store.ts:39`, `feed-store.ts:51`); a formula spanning both would
have an intermediate state where one had been set and the other had not, which is two
answers to one fact.

**It is exactly derivable.** The backend counts _rows_, not constituents: `Snapshot`
increments once per occurrence whose `ReadAt` is nil (`feed.go:385`). Every occurrence on
the wire carries its `kind` and its `read`, and an evicted row is absent from both counts,
so the memo reproduces the wire figure whenever nothing is hidden — which §9 asserts, and
which is what keeps the wire field meaningful rather than vestigial.

**The badge before the first read is a specified state, not an accident.** The store starts
at zero and reads asynchronously (`feed-store.ts:38`, `feed-store.ts:96`), and a failed read
keeps the last snapshot on purpose (`feed-store.ts:67`) — so today a never-successful read
shows a confident zero forever, which says "nothing happened" when it means "I could not
look". Now that §6 specifies what the badge means, that becomes a soft degrade hidden from
the product. So the store distinguishes **not yet known** from **zero**: the rail draws no
badge for either, as it does today, and the panel states it, in the place a person goes to
find out — "Could not read notifications" rather than "Nothing to catch up on". The empty
state and the unknown state are different sentences.

**The panel's own filter still may not reach the count**, and the distinction is the point:

| Narrowing                         | Lives in                       | Reaches the bell                                                |
| --------------------------------- | ------------------------------ | --------------------------------------------------------------- |
| The Host / Session / Kind selects | Renderer signals, in the panel | **No.** You are still waiting for what you narrowed away        |
| `notifications.centre.<kind>`     | The settings document          | **Yes.** You have declared you are not waiting for these at all |

Counting events a person has said they do not want is the same lie in the other direction:
the owner's bell read 61 while most of it was bells they had no interest in.

**`dropped` stays global, and says so.** The dropped record carries `count`, `oldest` and
`newest` and no kind, so the panel cannot restrict it. Making it per-kind is a wire and
backend change this design does not make. The line therefore keeps describing the whole
feed and its wording says so, rather than implying it describes what is on screen. The
panel's "N of M shown" counts visible kinds as its M, so the list never describes a
universe it cannot show.

**Mark all read is unrestricted, and the button is not.** `MarkAllRead` marks every
retained row (`feed.go:337`); the header action is enabled by the visible unread count. So:
if the only unread rows are hidden, the button is disabled and they stay unread; if at
least one visible row is unread, pressing it marks the **whole feed** read, hidden rows
included. That is the behaviour, it is deliberate — the backend has no notion of visible
and should not acquire one for this — and §1's criterion says "each with the read state it
had" so an implementer and a test cannot pick different models.

## 7. Naming a session — the lookup activation already made

`PaneManager.sessionDisplayName(sessionId)` exists (`panes.ts:2129`), composes the typed
tab name, the pane's title and the descriptor default exactly once, and refuses to fall
back to the id — "the internal handle this exists to keep off the screen", says its
comment. Two earlier drafts of this section were wrong about it and both are recorded,
because the second was wrong in a way that looked like a fix.

- **Draft one** added a "last known session name" registry beside `PaneManager`. A second
  owner of one question. Withdrawn.
- **Draft two** redefined `sessionDisplayName` to resolve through `findBySession`. That is
  a **public API change with consumers this work has no business touching**: one-argument
  callbacks for tool-call titles in local and SSH panes (`panes.ts:1109`, `panes.ts:1193`),
  a callback contract that takes only a session id (`blocks.ts:1617`), and `sessionWhere`
  (`panes.ts:2170`), which is the approval prompt's one derivation. Worse, `sessionWhere`
  reads its tab from `sessionDisplayName` and its machine and cwd from
  `terminalContentForSession` — so redefining one half would let it name a tab from one
  pane and a machine from another. Withdrawn.

**Nothing in `PaneManager` changes.** The composition root already calls
`tm.findBySession(backendId, sessionId)` for `canActivate` (`main.tsx:1107`); it passes the
name from the same call:

```
sessionNameOf = (backendId, sessionId) => tm.findBySession(backendId, sessionId)?.displayTitle ?? null
```

One lookup, two properties read off its result, at one call site, with no public method
redefined and no existing consumer disturbed. `findBySession` is also the right lookup on
its own terms: its comment says it reads `lineage()` rather than `activeOrigin()` precisely
so a `session.ended` row stays activatable after the shell exits (`panes.ts:2090`) — it was
written for this surface.

- The panel takes `sessionNameOf(backendId, sessionId)` as a prop, beside `canActivate`,
  so one surface asks one question of one owner.
- A renamed tab is named by its new name: `displayTitle` puts the user's typed name ahead
  of the terminal title (`panes.ts:191`).
- **Two panes on one session:** the answer is whichever `findBySession` returns _at the
  moment it is asked_. The label is computed when options are built and the target is
  resolved again on click, so a pane closing in between can move the target. This is
  accepted rather than guaranteed away: the alternative is pinning a pane id into a feed
  row, which is renderer state on a backend record. What is guaranteed is that name and
  target are never derived from **different lookups**, which is the defect that mattered.
- A session no pane holds cannot be named. The panel renders **one** option,
  "Unavailable sessions", covering all of them.

"Unavailable" and not "Closed": the name is missing when the pane is gone, but also when
the renderer reloaded, when the event preceded the pane, and — today — for every backend
that is not the local one, since `findBySession` resolves only `LOCAL_BACKEND_ID`. Calling
all of those "closed" states a cause the panel does not know.

**Collapsing them into one option is a trade.** Each unnamed session keeps its identity in
the feed, so the filter could offer one option each — and every one would read
"Unavailable session", a menu of identical entries nobody can choose between. One option
selecting all of them is honest about what the panel can distinguish.

**No id is rendered anywhere in this panel, in any state.**

**Reactivity needs a seam, and moving `AXES` inside the component is not one.** `AXES` is a
module-level constant (`notifications-panel.tsx:116`) and must move inside so its labels can
read props — but `sessionNameOf` reaches plain `PaneManager` fields, which Solid cannot
track. `main.tsx:546` already states this rule for `activeOrigin` and solves it with a
signal fed on change. The same shape here: a display-revision signal at the composition
root, bumped by the same notification that repaints the tab strip (`setTabDecoration`'s
`onDisplayChange`, `panes.ts:202`), read inside `sessionNameOf`. Without it a tab renamed
while the panel is open keeps its old label until an unrelated refetch.

**Found in passing, and not fixed here:** `findBySession` matches on `content.lineage()`
while `paneForSession` requires `instanceof TerminalContent` (`panes.ts:2111`). Two
derivations of "which pane holds this session", which can diverge for any future
`PaneContent` that implements the `lineage` capability — and the interface is deliberately
capability-based (`pane-content.ts:197`). Its own bead. A presentation fix is not where the
approval prompt's meaning gets changed.

## 8. Three kit repairs

**A row that can be activated says so.** `CollectionRow` sets `data-activatable="true"`
when it was given `onActivate`, and `collection-view.css` gives that row `cursor: pointer`.
Typed variance inside the component, appearance in the component's own stylesheet.

**The hover background does not move with it**, and this corrects an earlier draft that
said the fix reaches Connections, Endpoints, Git, Notes and Operations. It does not: those
rows carry action buttons and no `onActivate` (`connections.tsx:2119`,
`operation-row.tsx:184`), so the cursor variance is invisible to them — and moving hover
onto the same variance would have **removed** their hover instead. Hover answers "the
pointer is over this row", which is true and useful next to an action button; the cursor
answers "clicking the row does something". Two facts, two rules.

What clicking a dead row _should_ do is a separate question with its own bead, as is the
neighbouring accessibility gap: an activatable row is still `role="listitem"`, so a screen
reader is not told it can be activated either. CSS cannot invent behaviour that does not
exist, and neither is in scope here.

**A kind badge can carry its description.** `RecordRow.kind` is typed `{label, tone}` and
does not pass a title through to `Badge` (`record-row.tsx:88`), though `Badge` already
accepts one. §4's tooltip is therefore a kit change, not a panel change: the typed slot
gains a `description`, and the composite is still the only thing that renders a badge.

**A count badge is legible over a glyph.** `Badge` gains typed variance for a solid fill —
opaque background, contrasting text, and the ring that separates it from what it sits on —
inside `badge.tsx` and `badge.css`. `sidebar.css` gains no colour: it already says of
itself "the Badge paints itself, and this surface does not repaint it".

The ring needs the colour of whatever the badge sits on, which `Badge` cannot know. That
is a kit-level contract, not a surface override: the component reads a named custom
property with a token default, and a surface may set that property to declare its own
background. Setting a context variable is placement; rewriting the rule that consumes it
would not be.

**`99+` belongs to the activity bar, not to `Badge`.** `Badge` takes arbitrary content and
must not decide some of it is a number to abbreviate. The rail clamps **only the Badge's
children** (`sidebar.tsx:512`), never the `count()` accessor — that accessor also feeds the
`Show` guard and the button's accessible name (`sidebar.tsx:486`), so a screen reader still
hears 137 while the rail shows `99+`. The clamp is the activity bar's, so it applies to
every view's count, not only Notifications.

**Found in passing:** `sidebar.css:100` defines `.activity-bar__badge` while the renderer
uses `.activity-bar-badge` (`sidebar.tsx:511`). Two answers to one placement question, one
of them dead. Its own bead, not a silent deletion inside this work.

## 9. Testing

Rule 4 applies: the assertions are written here so the implementer is not the only author
of the tests.

**End-to-end (rule 2), the check from §1** — one Playwright spec on the real backend: raise
a bell and a finished command in a named tab; assert the rail badge's text and its
accessible name separately; assert **the kind badges and the session filter options**
contain no dotted slug and no hex run — _not_ the whole panel's text, since `title` and
`body` are untrusted free text that may legitimately hold a commit hash; assert the session
filter offers the tab's title; click a row and assert the tab is focused; toggle "Terminal
bell → Notification centre" off, assert the rows and the count drop; toggle it on and
assert every such row the feed still holds returns with the read state it had, including
one raised while it was off.

**Go, and the catalogue tests are two tests because they cannot be one.** The shipped
catalogue's channels are both local, so the trust bound offers every shipped kind at least
one pair (`catalogue.go:372`) and "a kind with no offered pair" cannot occur in it. So: a
**unit** test builds a catalogue with a heuristic kind and a network-only channel and
asserts `PresentedKinds` returns that kind while `Pairs` does not; and a **real-socket**
test asserts `notify.catalogue` conforms to its contract
(`…_OverTheWireConformsToContract`, not a payload the test built) and returns the shipped
catalogue's presented set exactly. Injecting a catalogue into the transport purely to make
one test do both jobs is what this avoids.

Also Go: the deep-copy assertions of §4 — mutate a nested `Trusts` slice through a returned
`Pair` and through `PresentedKinds`, and assert the catalogue is unchanged; the set of
registered `notifications.centre.*` keys equals the presented kind ids exactly; and their
section and label shape match §5's four conditions.

**The regression that guards §0 stands on the application seam, not on `Ingress`.**
`Ingress` does not know settings exist and calls `feed.Add` unconditionally
(`ingress.go:58`), so a unit test over it with different centre values would only prove the
test did not wire settings in. The assertion: set a centre toggle off, raise an event
through the real transport, read `notify.feed.read`, find the row. It needs **two
scenarios**, because the feed has two production writers and a test knowing one would be
falsely complete — the admitted occurrence, and a `RecordDeliveryFailure` row, which
requires a sink failing with something other than `ErrUnavailable` (that one is dropped on
purpose, `app.go:1580`) and a wait on the observable snapshot. The seam already exists:
`app_notify_failure_test.go:67`.

**Renderer** — the parser accepts both namespaces and still refuses a malformed key; the
memo excludes hidden kinds, equals the wire count when nothing is hidden, and is unmoved by
the panel's filter; a batch of settings changes applies as one replacement, so no render
observes three kinds hidden and three not; the panel distinguishes "nothing to catch up on"
from "could not read"; an unknown kind renders "Pane work finished" and never the slug;
`sessionNameOf` returning null puts the row under the single "Unavailable sessions" option;
a tab renamed while the panel is open relabels its filter option.

**Kit** — an activatable `CollectionRow` carries `data-activatable` and a non-activatable
one does not, **and both still take the hover background**; `RecordRow` passes its kind
description to the badge's title; the solid badge's ring reads its context property.
Contrast is asserted where it can be computed — a browser-level check, since jsdom does not
resolve CSS custom properties to colours — against a named threshold rather than by eye.

**Failure paths, per rule 3, with both ends named:**

- `notify.catalogue` fails → the fallback naming, from first render until the first
  successful read, across any number of failures and reconnects.
- The **first** settings read fails → every kind is visible. A visibility setting that
  cannot be read must not hide a notification.
- A **later** settings read fails → the last confirmed set stays in force. Reverting to
  "show everything" on a transient error would contradict a choice the user made and can
  see in Settings — the same reason `FeedStore` keeps its last snapshot (`feed-store.ts:67`).
- The **first** feed read fails → "could not read", not "nothing to catch up on" (§6).

## 10. Where the hidden set lives

One reactive projection of the settings document, owned by the composition root, read by
the store — and **no second observer**. `SettingsObserver` accepts many handlers
(`settings-observer.ts:25`), so adding one here would give a single settings push two
independent refetches and two reconciliations of one fact. The existing reconciliation
(`main.tsx:935`) gains these keys; it already reads the snapshot once and applies known
keys from it.

**The ordering works, and one existing line has to change for it to.** The first settings
snapshot is already awaited at `main.tsx:385`, `FeedStore` is created at `main.tsx:1047`
and the sidebar mounts at `main.tsx:1131` — so the set can be seeded before anything
renders and there is no flash of hidden kinds. But that snapshot's revision is dropped with
its block-local variable, and the observer is started with a hard-coded baseline of zero
(`main.tsx:937`). §10 promises replacement "after the revision check", so the initial
revision must be carried to `observer.setRevision` instead.

The interval, both ends: the set is seeded from the first settings snapshot before the
sidebar first renders, and every later change replaces it **whole** after the revision
check. Between those, the previous set is entirely in force — there is no render in which
three kinds are hidden and three are not.

**On reconnect the two reads race, and the closing event is named.** The feed read and the
settings read are started by different subscribers and may finish in either order, so a
fresh feed snapshot can briefly be counted under the previous connection's visibility set.
That is accepted: the set is the user's standing choice, it rarely differs across a
reconnect, and coordinating two reads to remove a transient miscount would couple two
stores. The interval closes at **the first successful settings snapshot of the current
connection**, at which point the count is recomputed from the occurrences already held.

**A filter pick whose kind is hidden is cleared, not remembered.** `activeOn` today treats
an option the feed no longer offers as no filter, without clearing the stored pick
(`notifications-panel.tsx:190`) — so hiding a kind would silently drop the filter and
un-hiding it would silently restore one the user had long forgotten. Hiding a kind clears
a pick that named it.

## 11. Deliberately out

- **Clicking a row does not mark it read.** Different facts, settled by centre design §6.
- **What a dead row's click should do**, and the `role="listitem"` accessibility gap. Own beads.
- **`findBySession` vs `paneForSession`** — two derivations of one question (§7). Own bead.
- **`findBySession` resolving non-local backends.** A real gap belonging to `nocx-if6`
  phase A, not to a presentation fix.
- **Per-kind `dropped`.** §6 states why, and it would be a wire change.
- **The dead `.activity-bar__badge` rule.** Own bead (§8).
- **The 163 renderer-side `showToast` call sites** (`nocx-zlxmm`).
- **Persisting the feed.** ADR-0033 and centre design §7 stand: it dies with the process.
