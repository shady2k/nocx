# W5 — the activity bar clamps a count without lying to a screen reader (nocx-syhg9)

Read `.internal/briefs/_common.md` first. Then design §8, last two parts.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
frontend/src/sidebar.tsx                      + its test
frontend/src/styles/components/sidebar.css
```

**Files other workers own — escalate, do not edit:** everything else. In particular
`frontend/src/ui/badge.tsx`, `frontend/src/styles/components/badge.css` and
`frontend/src/main.tsx` belong to other workers in this same wave.

**What already landed before you started:** `Badge` has typed variance for a **solid**
fill, and reads its ring colour from a named CSS custom property with a token default.
Read `frontend/src/ui/README.md` for the property's name — do not invent one.

## What changes

The rail's unread badge is `Badge tone="info"` (`sidebar.tsx:512`) — the accent at 80%
transparency, drawn over the bell glyph in a 48px rail. The owner photographed it and
could not read the number.

1. **Use the kit's solid variance**, and declare the rail's own background through the
   ring's context property. `sidebar.css` **gains no colour** — it already says of itself
   "the Badge paints itself, and this surface does not repaint it", and that stays true.
   Setting a context variable is placement; rewriting the rule that consumes it is not.

2. **Clamp to `99+` — only the Badge's children.** Never the `count()` accessor: it also
   feeds the `Show` guard and the button's accessible name (`sidebar.tsx:486`), so a
   screen reader must still hear 137 while the rail shows `99+`. `Badge` itself must not
   learn about numbers; it takes arbitrary content, and the clamp is the activity bar's
   decision. It therefore applies to **every** view's count, not only Notifications —
   Operations has one too.

## Found in passing, and it is yours to leave alone

`sidebar.css:100` defines `.activity-bar__badge` while the renderer uses
`.activity-bar-badge` (`sidebar.tsx:511`) — two answers to one placement question, one of
them dead. It has its own bead (`nocx-63j6r`) so the deletion is visible rather than
buried in an unrelated diff. **Do not delete it here.** Mention it in your report.

## Assertions

- A count of 137 renders `99+` in the rail and the button's accessible name still says 137. Both halves, in one test.
- A count of 99 renders `99`; 100 renders `99+`. State where you put the boundary.
- The clamp applies to any view's count, not only Notifications — assert it on a second
  view rather than on the bell alone.
- `sidebar.css` contains no `background`, `border`, `color`, `box-shadow` or `font-*` rule
  targeting a `ui-*` class. Grep your own diff for it before you report.

## Verification, scoped

```
cd frontend
./node_modules/.bin/tsc --noEmit -p tsconfig.json
./node_modules/.bin/vitest run src/sidebar.test.tsx
```

Nothing wider.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w5-rail

If you cannot finish, print instead:

    NCBLOCK-3f7a::w5-rail <one line why>
