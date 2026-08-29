# W2 — the kit says which rows are activatable, and paints a legible badge (nocx-i4k4r)

Read `.internal/briefs/_common.md` first. Then design §8, in full.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
frontend/src/ui/collection-view.tsx        + its test
frontend/src/ui/record-row.tsx             + its test
frontend/src/ui/badge.tsx                  + its test
frontend/src/styles/components/collection-view.css
frontend/src/styles/components/badge.css
frontend/src/styles/components/record-row.css
frontend/src/ui/README.md                  (the inventory row, if a variant is new)
```

**Files other workers own — escalate, do not edit:** `frontend/src/notify/**`,
`frontend/src/main.tsx`, `frontend/src/sidebar.tsx`,
`frontend/src/styles/components/sidebar.css`, `frontend/src/settings*`, all Go.

Three repairs. They are independent of each other; do them in this order because the
first is the one the owner reported.

## 1. An activatable row says so

`CollectionRow` sets `data-activatable="true"` when it was given `onActivate`, and
`collection-view.css` gives **that** row `cursor: pointer`. Typed variance inside the
component; appearance in the component's own stylesheet.

The rows were always clickable — `onActivate` is passed, `tabIndex` is 0, Enter and Space
both work, and `frontend/src/notify/notification-activation.test.tsx:139` proves it by
driving a real `PaneManager`. Nothing on screen said so, because
`collection-view.css:49` sets `cursor: default` unconditionally with no activatable
variant. **A test that asserts a user CAN do something does not assert they can SEE that
they can** — that is what this repair is.

**The hover background does NOT move onto the new variance, and this is the part to get
right.** It applies to every row today (`collection-view.css:61`). Connections
(`connections.tsx:2119`) and Operations (`operation-row.tsx:184`) have action-bearing
rows with **no** `onActivate` — gating hover on activation would take hover away from
them. Hover answers "the pointer is over this row", which is true and useful next to an
action button. The cursor answers "clicking the row does something". Two facts, two
rules. Assert both directions.

## 2. A kind badge can carry its description

`RecordRow.kind` is typed `{label, tone}` (`record-row.tsx:88`) and does not pass a title
through to `Badge`, though `Badge` already accepts one. Add a typed `description` to that
slot and pass it as the badge's title. The composite stays the only thing that renders a
badge — do not open a JSX slot.

## 3. A count badge is legible over a glyph

`Badge` gains typed variance for a **solid** fill: opaque background, contrasting text,
and a ring that separates it from whatever it sits on. Today the activity bar uses
`tone="info"`, which is the accent at 80% transparency over the rail's own glyph — the
defect the owner photographed.

`Badge` cannot know what it sits on, so the ring's colour is a **kit-level contract**:
the component reads a named CSS custom property with a token default, and a surface may
set that property to declare its own background. Setting a context variable is placement;
rewriting the rule that consumes it would not be. Name the property in
`frontend/src/ui/README.md` so the next surface finds it.

**Do not** add number formatting, clamping or `99+` to `Badge`. It takes arbitrary
content and must not decide some of it is a number. That clamp belongs to the activity
bar and another worker owns it.

## Assertions

- An activatable `CollectionRow` carries `data-activatable`; a non-activatable one does
  not; **both still take the hover background**.
- `RecordRow` passes its kind description through to the badge's title.
- The solid badge reads its ring colour from the context property, and falls back to the
  token default when no surface set one.
- Contrast of the solid variance in **both** themes, asserted against a named threshold.
  jsdom does not resolve CSS custom properties to colours, so this is a browser-level
  check — put it where the kit's other computed-style checks live, or say plainly in your
  report that you could not compute it and why.

## Verification, scoped

```
cd frontend
./node_modules/.bin/tsc --noEmit -p tsconfig.json
./node_modules/.bin/vitest run src/ui/collection-view.test.tsx src/ui/record-row.test.tsx src/ui/badge.test.tsx
```

Then the suites of the surfaces that consume these components, to prove you did not move
their behaviour — `src/connections.test.tsx` and any `operation-row` test. Nothing wider:
no `npm test`, no `make ci`, no e2e.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w2-kit

If you cannot finish, print instead:

    NCBLOCK-3f7a::w2-kit <one line why>
