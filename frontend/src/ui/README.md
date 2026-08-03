# UI Kit — `frontend/src/ui/`

One nocx vocabulary for every application control. Surfaces import from `ui/`,
never from a component library. Implementation behind each primitive is chosen
per-primitive (see ADR-0014).

## Component inventory

### Components we write

Every one renders a stable **base class** naming itself, on the element that carries the
appearance — not merely on a wrapper. Variance is a typed `data-*` attribute. None of
them takes a `class` prop; the structural containers that still do are marked.

| Component            | Module                  | Identity                                                                | Variance                                                                                                                       |
| -------------------- | ----------------------- | ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| **Stack**            | `stack.tsx`             | `ui-stack`                                                              | `data-gap`: default \| loose; `data-divided`                                                                                   |
| **Button**           | `button.tsx`            | `ui-button`                                                             | `data-variant`: default \| primary \| danger \| ghost; `data-size`                                                             |
| **IconButton**       | `icon-button.tsx`       | `ui-icon-button`                                                        | `data-size`; `selected` → `aria-selected`; `ariaLabel` is **required**                                                         |
| **TextField**        | `text-field.tsx`        | `ui-text-field`, `ui-text-field__input`                                 | input types text \| number \| password; `data-multiline` → `<textarea>`; composes Field for label/desc/error                   |
| **SearchField**      | `search-field.tsx`      | `ui-search-field`, `__input`, `__icon`                                  | —                                                                                                                              |
| **Select**           | `select.tsx`            | `ui-select`                                                             | native `<select>`, `appearance: none` (ADR-0014)                                                                               |
| **Checkbox**         | `checkbox.tsx`          | `ui-checkbox`, `ui-checkbox__control`                                   | `data-variant`: checkbox \| switch                                                                                             |
| **Radio**            | `radio.tsx`             | `ui-radio`, `ui-radio__control`                                         | —                                                                                                                              |
| **SegmentedControl** | `segmented-control.tsx` | `ui-segmented-control`, `__option`                                      | one row; same `radiogroup` ARIA as Radio, for few and short options                                                            |
| **FileInput**        | `file-input.tsx`        | `ui-file-input`, `__native`, `__name`                                   | kit Button as trigger; native input hidden but focusable                                                                       |
| **Toast**            | `toast.tsx`             | `ui-toast-host`, `ui-toast`, `__message`                                | `data-level`: info \| success \| warning \| danger                                                                             |
| **MarkerList**       | `marker-list.tsx`       | `ui-marker-list` + `__item/__marker/__text`                             | `data-tone`: included \| excluded \| note                                                                                      |
| **Badge**            | `badge.tsx`             | `ui-badge`                                                              | `data-tone`: neutral \| info \| warning \| danger                                                                              |
| **EmptyState**       | `empty-state.tsx`       | `ui-empty-state` + `__icon/__title/__desc/__action`                     | optional `icon`                                                                                                                |
| **StatusCard**       | `status-card.tsx`       | `ui-status-card` + `__icon/__body/__title/__desc/__action`              | `data-tone`: neutral \| ok \| warning \| danger; a state and the one action for it                                             |
| **CollectionView**   | `collection-view.tsx`   | `ui-collection-view`, `ui-collection-row`                               | Searchable manager shell and shared list row                                                                                   |
| **Prompt**           | `prompt.tsx`            | `ui-prompt-overlay`, `ui-prompt`                                        | `data-placement`: floating \| top-sheet                                                                                        |
| **Field**            | `field.tsx`             | `ui-field`, `+ ui-field-horizontal`                                     | `orientation`; `data-label` follows orientation (horizontal→primary)                                                           |
| **Section**          | `section.tsx`           | `ui-section`                                                            | children spaced by Stack; `divided`; no `class` passthrough                                                                    |
| **Toolbar**          | `toolbar.tsx`           | `ui-toolbar`                                                            | keeps `class`, **layout only**                                                                                                 |
| **Tabs**             | `tabs.tsx`              | `ui-tabs`, `ui-tabs__list`, `ui-tabs__panel`; row marker is `StatusDot` | `data-orientation`: vertical \| horizontal; rows are ghost Buttons; optional `data-tone` on `__status`: ok \| warning \| error |

## A container's size is the container's decision, never its content's

**A component that holds swappable content declares a definite size for it.**
Not a `max-width`, not a `min-height` — a size the content cannot argue with.
There is one honest exception, and the trade it makes is documented below.

`Dialog`'s panel was `max-width: 480px` on a `<dialog>`, which is
`width: fit-content`: the panel therefore shrank to whatever the body needed,
so a body with sections redrew the dialog at a different width on every
section — 356px on one, the full 560 on the next. The visible symptom is the
same as Tabs' and it is not cosmetic: **the footer buttons move out from under
the pointer that is reaching for them**, and a control that moves while being
aimed at is a control that gets misclicked. `Dialog` names its width through
`size` — a definite width, not a cap.

Height is the exception, and the exception is real. Width can be named because
it is one number for every form the dialog holds; height is a different number
for each. Naming it was tried twice and both numbers were wrong — 420px sat
below the panel's natural height and did nothing at all, and 45rem left a
third of the dialog empty below the footer. A guessed height is either too
small, and then a short section scrolls in a window with room to spare, or too
large, and then every section but one sits in a half-empty box.

So `Tabs` **sizes to the section it is showing**: the inactive panels are
`hidden` (`display: none`), which keeps them out of the tab order and the
accessibility tree and also stops them contributing height, so the box is the
active section's size rather than the tallest's. Switching sections therefore
changes the dialog's height — and `Dialog` **animates that change**, which is
what buys the stability back. The footer moves, but it moves visibly and
predictably instead of teleporting, so it never escapes a pointer mid-reach.

The animation is measure-and-transition, owned by `Dialog` because it owns the
panel and its `max-height`: the settled height is pinned, the new natural
height is measured (with the `max-height` applied, so a short viewport still
scrolls rather than overflow), the panel transitions between them, and it
releases back to `auto` on `transitionend`. Transitioning to and from
`height: auto` itself would need `interpolate-size` / `calc-size()`, which is
above both declared browser floors (ADR-0013 §3), so the pin-and-release is
the technique. Under `prefers-reduced-motion: reduce` there is no transition.

The general form: if switching what is inside a component can change that
component's outer size and you cannot name a size (one number that is right
for every content), then you animate the change — the size stays the content's
decision, and the movement stays the container's.

## Dialog body text is body text

Prose inside a dialog is set at the body size. Not `--font-size-sm`, not smaller
"because it is only supporting detail" — a dialog is the one surface where the reader
has been interrupted and is being asked to decide something, and the delete
confirmation had the smallest type on screen carrying the most consequential question.
`--font-size-sm` is for a caption beside a control that already says what it is: a
provenance label, a row's second line. Not for a sentence.

`Dialog` takes an `onSubmit` for the dialog's obvious yes, so Enter in a single-line
field confirms rather than doing nothing. It is opt-in: Enter must not fire a
destructive confirmation, and a dialog whose body is a message has nothing to submit.

## Vertical rhythm: Stack

**Stack** (`stack.tsx`) is the only source of vertical spacing between kit components.
Surfaces must not add their own margins between stacked controls — that constraint
is enforced by the `surface-spacing-kit` lint rule. Prefer Stack over a plain `<div>`
with a hand-rolled gap.

`divided` (`data-divided="true"`) turns the Stack into a divided list: each child
gets row padding and a hairline separator between visible children. The selector uses
`:not(.st-vis-hidden)` so search-filtered rows do not leave a gap or orphaned divider.
Settings pages and the Vault panel use divided Stacks for their row rhythm.

## Button variant rules

**Button** (`button.tsx`) has four variants. The rules for using them are in the
component's doc comment and summarised here — they are the contract that keeps
two sections of the same kind from showing different button looks for the same
kind of action:

- **primary** — the one action a section exists for. At most one per section.
  A control that reveals, expands or navigates is not primary (disclosure does
  not change data). A button rendered once per row of a list is never primary
  (emphasis is spent by repetition).
- **default** — everything else that is a real action. This is the default.
- **danger** — destructive and irreversible.
- **ghost** — a control that reads as a row rather than a button
  (e.g. the settings rail's nav items).

**A wrapper identity and a control identity are different identities with different
duties, and neither may be inferred from the other.** `ui-checkbox` owns the row and its
label; `ui-checkbox__control` owns the box, its checked mark and its disabled state. The one that matters most is the identity on the element that carries the appearance — an input, a select, or a button. Every defect the kit migration unwound (`nocx-pp3y`) came from that element having no name at all, so its rules could only be reached through an ancestor class and each surface wrote its own instead.

**Toggle is not a component.** Checkbox already has the contract — checked, onChange,
label, disabled — and a switch is a shape, not a different behaviour. It is
`variant="switch"`.

## Form validation

`Field` and `TextField` have always rendered an `error` and set `aria-invalid`. What the
kit lacked was a way to decide there _was_ an error, so surfaces either skipped
validation (the connections form saved a profile with no host and reported the resulting
backend failure as a connection error) or grew a private one.

`validation.ts` is that vocabulary, in two separable halves:

- **Validators** — `(value: string) => message | undefined`. Plain functions:
  `required(label)`, `hostname()`, `port()`, `nonNegativeInteger(label)`, `combine(...)`.
- **`createFormValidation(rules)`** — decides _when_ a message is shown, which is a
  different question from whether the value is wrong. Rules are accessors, so it makes
  no assumption about how the surface stores its draft.

```tsx
const v = createFormValidation({
  host: () => combine(required('Host'), hostname())(draft()?.host ?? ''),
})

<TextField error={v.error('host')} onBlur={() => v.touch('host')} required … />
```

An error appears when the field is left (`touch`, from `TextField`'s `onBlur`) or when
submit is attempted (`revealAll`) — never while the user is still typing the first
character of an empty field. `valid()` and `firstError()` ignore both and answer about
the values, which is what a submit handler needs. `reset()` when the form switches to a
different record, or the previous record's touches greet the user with errors they have
not caused.

**Messages carry no trailing full stop.** They are fragments, and the same string is
shown inline under a field and inside a Toast.

## Telling the user something happened: Toast

**Toast** (`toast.tsx`) is the only notification affordance. An operation's outcome is
raised with `showToast({ level, message, duration })` and rendered by the single
`ToastHost` in `App.tsx` — surfaces do not render a status line of their own.

`info` and `success` dismiss themselves after 4 s; `warning` after 8 s; `danger` is
sticky, because an error the user was not looking at is an error they never saw. An
explicit `duration` overrides the level's default and `0` means sticky, which is what a
half-succeeded import uses.

**A Toast raised from inside a modal Dialog is visible.** `Dialog` uses `showModal()`, so
it paints in the browser's **top layer**, above every `z-index` in the normal layer —
`ToastHost`'s 300 included. Being above a top-layer element is not a number, it is a
parent: `ToastHost` portals itself into the topmost open overlay (`topOverlayElement`) and
falls back to the body when none is open. Raise outcomes with `showToast` from inside a
dialog freely; do not re-derive the answer from z-index and conclude otherwise.

What belongs on the field instead is **field validation** — "Enter your passphrase" is
about what is in the box, is answered by editing the box, and clears as you type. The
outcome of the call the box triggered is a Toast.

The rule this replaces a pattern for: **a message about an action does not live in the
document flow.** The export page kept a `.st-export-status` div under every action,
holding an empty line on four sections forever so that a message could appear without
shifting the layout — and shifting it anyway when the message ran to two lines.

## Prompt: a modal that is not a `<dialog>`

**Prompt** (`prompt.tsx`) is the overlay treatment for asking one thing of the
user without burying what is behind it. `data-placement="top-sheet"` slides a
panel down from the top edge and leaves the surface it interrupted visible —
that is why the vault's password prompts are top-sheets while the New
Connection form they interrupt stays a `Dialog`.

Everything a `<dialog>` gives for free is either supplied by the overlay stack
or by Prompt itself, and the boundary matters:

- **Escape** comes from the overlay stack's document-level handler — Prompt
  registers with `pushOverlay` like every other overlay, so Escape closes the
  topmost one. Tested, not assumed.
- **Enter** is `onSubmit`, opt-in with the same contract as `Dialog`'s: a
  single-line input fires it, a textarea and a button own their own Enter, and
  an IME's Enter accepts a candidate. The vault prompts all pass one.
- **Focus** is Prompt's job: on open it focuses the `autofocus` field, else
  the first field, else the first button (the same order a native modal
  chooses); on close it restores focus to whatever had it before — the overlay
  stack records that on push.
- **Toasts are visible from inside a Prompt.** `ToastHost` portals into the
  topmost overlay's element, and a Prompt's element is on the stack, so a
  toast raised while the prompt is open renders inside it. A one-time
  recovery code that cannot be copied is reported by a sticky toast — it must
  survive, and it does.

One trap the kit records for the next reader: **a component body executes
once.** Switching a surface between a Prompt and a Dialog on state is a
`<Show>` with the two as branches — a top-level ternary freezes on the first
branch and the second can never appear.

**`Tab` is not a kit primitive either.** It carries `role=tab`, drag and reorder,
middle-click close, activity indicators, `aria-controls` and two orientations: a
behavioural unit, not a styled button. Feature components like it are declared in
`feature-components.json` rather than inferred from a directory.

### Platform primitives (no wrapper needed per ADR-0014)

| Primitive             | Implementation                 | Why                                                                                                                   |
| --------------------- | ------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| Dialog                | `dialog.tsx` + `overlay/` core | Native `<dialog>` + `showModal()` — top-layer rendering, Escape/cancel, native focus. ADR-0014. Built in nocx-vxqj.5. |
| Tooltip               | Native `title` attribute       | ~8 call sites, none need rich content.                                                                                |
| Popover/Menu/Combobox | **Not built**                  | Zero consumers. Revisit when a real consumer exists.                                                                  |

### Page primitives (separate ownership — not merged with kit Section)

| Component       | Module              | Notes                                                                                                                         |
| --------------- | ------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| Page            | `page.tsx`          | Page layout container                                                                                                         |
| PageHeader      | `page-header.tsx`   | Page header                                                                                                                   |
| PageBody        | `page-body.tsx`     | Page body                                                                                                                     |
| PageRail        | `page-rail.tsx`     | Side navigation rail                                                                                                          |
| PageScroller    | `page-scroller.tsx` | Scroll owner                                                                                                                  |
| **PageSection** | `page-section.tsx`  | Semantic `<section>` within a Page, with `id` for deep linking; accepts `divided` and one `description` for the whole section |
| SidebarView     | `sidebar-view.tsx`  | Sidebar view wrapper                                                                                                          |

### `Section` vs `PageSection` overlap

`Section` (kit) and `PageSection` (page layout) both render an `h2` + children.
They differ in:

- **`Section`** — `<section>` element, `id` for anchor targeting, `class` passthrough. Part of the kit. Accepts `divided` to forward to its inner Stack.
- **`PageSection`** — `<section>` element, `id` for deep-linking, gets page-specific spacing from `surface.css`. Accepts `divided` to forward to its inner Stack.

**Recommendation: do not merge them.** `Section` is a generic kit component for form
groups and control sections within any surface. `PageSection` is a layout primitive
that participates in the Page's scroll‑anchor and spacing system. A setting row
inside a settings page uses `Section`; the settings page's top-level groups use
`PageSection`. If they converged, the kit would need to import page-layout CSS,
which is the wrong dependency direction.

## CSS

Component styles live in `frontend/src/styles/components/`, **one file per component**
(ADR-0013 §1), each imported from `style.css`.

There used to be one `kit.css` holding all of it — 515 lines — and it was split during
the kit migration (`nocx-v0ai`) for a reason worth keeping in mind: while every
component's rules shared one file, every transaction that touched a component touched
that file, so nothing could be worked in parallel and no file answered "who owns this
selector".

A file owns one **identity family**: a root identity plus the `__part` identities only
that root renders. `Page`, `PageHeader`, `PageBody`, `PageRail`, `PageScroller` and
`PageSection` all emit `ui-page*` identities that exist only inside a Page, so they are
one family and one file. `SidebarView` is its own root and gets its own file even though
it is small.

Two rules about what may live where, both gated:

- A component file may contain only selectors rooted at an identity of its own family.
  No bare-tag selectors — `button`, `input[type=…]`, `label:has(input)` — because a rule
  that matches by element rather than by identity is a rule any surface can collide with.
- A surface may name a kit identity **only to place it**, never to change how it looks.
  The discriminator is the property: `flex`, `margin`, `width`, `order`, `align-self`,
  `position` are placement; `background`, `border`, `border-radius`, `color`, `font-*`,
  `padding`, `box-shadow` and any drawing pseudo-element are appearance. That boundary
  was decided in `nocx-zeti` after the first draft of the rule fired on correct code.

`base.css` is deliberately exempt from the first rule and is where the focus ring lives:
a visible focus indicator is an application-wide invariant (WCAG 2.4.7), not something a
surface should have to remember to ask for.

## Keyboard and accessibility

Every component:

- Is natively focusable (no `tabindex` needed) or uses explicit `role`.
- Works with keyboard: Enter/Space for buttons and checkboxes, arrow keys for
  Select (native), Tab for navigation.
- Uses `aria-label` when visible text is absent.
- `disabled` prevents interaction and reduces opacity.
- Error states use `aria-invalid` and `role="alert"` on the error message.

## Identity is what a component renders, not what it is spelled

A class is a **kit identity** when a component in this directory renders it, and that
set is computed from the AST — static `class` / `className` / `classList` values on JSX
elements in `ui/**`, and nothing else (`lint-fixtures/scan-kit-identities.mjs`). Comments,
doc strings, `querySelector` arguments and variant lookup tables are invisible to it.

**The `ui-` prefix is not the test, and treating it as one was a bug.** The derivation
used to be a regex over raw source, which swept in prose and lookup tables — eight false
positives from the variant tables alone. It also could not see identities that do not
start with `ui-`, such as the dialog's `nocx-dialog__panel`. Meanwhile the prefix is
genuinely in use by surface classes no component renders — `ui-settings-row`,
`ui-settings-filter`, `ui-export-desc` — and a prefix-based rule would forbid the
settings surface from styling its own markup.

So: `nocx/no-inline-markup` flags a **rendered** class appearing in a file other than the
component that renders it, because that means markup is duplicating a component. Renaming
is never the fix. If a component renders the class, the fix is to use the component.

Two identities per component is normal and deliberate. `ui-checkbox` is the row and
`ui-checkbox__control` is the box; they have different duties and neither may be inferred
from the other. The one that matters most is the identity on the **element that carries
the appearance** — an input, a select, a button. Every defect the kit migration
(`nocx-pp3y`) unwound came from that element having no name, so its rules could only be
reached through an ancestor and each surface wrote its own instead.
