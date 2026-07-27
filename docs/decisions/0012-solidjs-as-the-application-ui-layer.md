# ADR-0012 — SolidJS as the application UI layer

- **Status:** Accepted
- **Date:** 2026-07-26
- **Related:** AD-6 (single-owner state ownership), AD-7 (session-id is
  server-authoritative), AD-8 (interface-first + DI at a single composition root),
  [ADR-0001](0001-xterm-js-as-vt-frontend.md) (xterm.js as the VT frontend),
  [ADR-0005](0005-linux-webkitgtk-forced-refresh-pump.md) (WebKitGTK refresh pump),
  beads `nocx-6zu5` (this decision), `nocx-njrx` (the migration epic), `nocx-ycet`
  (frontend state), `nocx-vxqj` (component kit), `nocx-708q` (multi-view sidebar).
- **Fills a gap rather than revising anything:** there was no prior decision to revise.

## Context

The frontend is vanilla TypeScript with no UI framework. **That was never decided.** It
arrived with the first commit — `2bb8ddf`, "M0 scaffold: Go module + Wails v2 app +
ghostty-web frontend skeleton" — described only as "TypeScript + Vite", closing
`nocx-29e.1` and `nocx-29e.3`, neither of which posed the question. `docs/architecture.md`
describes the frontend as "an xterm.js (WebGL) frontend" and names no UI layer at all.
ADR-0001 settled the **VT engine**, which is the terminal grid, not the application
around it. No ADR in this directory mentions a framework. The default was inherited and
never revisited.

That was fine while the frontend was a terminal. It has stopped being fine. Measured
2026-07-26, `frontend/src` is **9,307 non-test lines**, of which roughly **4,731 are
ordinary application UI** — settings, the SSH connections manager, profiles, the sidebar,
the tab strip, the export section, banners — and about 4,344 are terminal-adjacent or
protocol code. Nearly half this codebase is not a terminal, and `nocx-708q` would add
thousands more.

The defects are where the theory predicts. All of these shipped:

- `nocx-x6w9` — the settings screen renders its search box and modified-filter **twice**,
  because `settings.ts` and `settings-content.ts` independently built the same chrome.
- `nocx-98z6` / `nocx-nb8v` — switching tab placement left the tab strip empty and
  switching back did not restore it; container class state was reduced in two places that
  had to agree by hand.
- `nocx-ucxl` — the settings section rail highlights but changes nothing.
- `nocx-rp2j` — a cold start opens an empty panel, because view registration mixes
  panel-views and tab-actions in one list.

These are **state-ownership and component-reuse failures**, not rendering-model failures.

## Decision

**Adopt SolidJS 1.x, pinned, as the application UI layer. The terminal stays imperative
behind an explicit boundary. No second state library.**

### 1. Solid owns the application shell; xterm owns the terminal

Solid owns tabs, tab strip, sidebar, settings, profiles, SSH connections, banners,
dialogs, the component kit and the future file explorer.

The boundary against the terminal is a hard rule, not a convention, and it exists because
AD-6 puts render state in the VT frontend and xterm.js owns its own DOM subtree and WebGL
canvas:

1. Solid creates an **empty** host element.
2. The terminal adapter takes **exclusive ownership** of that element's descendants.
3. Solid never renders children beneath it.
4. Solid never **keys or remounts** the host during ordinary tab activation. Doing so
   risks session loss, WebGL context churn, and Linux repaint regressions.
5. Terminal render state — grid, scrollback, selection, per-cell data — is **never**
   expressed as reactive state.
6. Reactive state crosses the boundary only through explicit imperative methods
   (`setVisible`, `resize`, `focus`), never through markup describing terminal internals.
7. Disposal is owned exactly once.

ADR-0005's WebKitGTK forced-refresh pump stays **wholly inside the terminal controller**.
Solid effects must never drive terminal refreshes. A framework does not "solve" that pump:
it addresses compositor presentation, not DOM diffing.

### 2. No Zustand, and no second reactive system

Solid's signals and stores hold **accepted** application state. Adding Zustand — or any
separate store library — would introduce a second subscription system, a second model for
derived state and batching, adapters between the two, and a standing ambiguity about
where a slice belongs. That is more opportunity for parallel sources of truth, which is
the failure this adoption exists to end.

The Go backend being authoritative for some state does **not** argue for a
framework-independent container. It argues for explicit authority and transition rules:

```text
Go authority
    ↓ typed responses / events
framework-neutral clients and transition functions  (plain .ts)
    ↓ accepted state transitions only
Solid signals / stores
    ↓
components
```

Wire parsing, revision validation, session transition rules and command construction stay
in ordinary TypeScript and **must not import Solid**. Authority is encoded in the type, so
AD-7 cannot be violated by accident: no transition may produce a server session id except
by handling the backend's successful `open` ack, and an incoming settings snapshot must
pass the revision policy before it replaces the local mirror.

We do **not** abstract the reactive layer so the framework could be swapped later. A
framework replacement rewrites the component layer and its bindings regardless; keeping
protocol and domain logic plain is the valuable half, and the rest is speculative
architecture.

### 3. The lint gate is part of the decision

Pin `eslint-plugin-solid` and enable at minimum `no-destructure`, `reactivity`,
`no-react-deps`, `no-react-specific-props`, `prefer-for`, `prefer-show`,
`components-return-once`.

Then the part that must not be skipped: a **negative fixture** file containing every
prohibited pattern, with CI asserting that lint **fails** on it.

This is not belt-and-braces. The plugin was last published 2024-12-11, about nineteen
months before this ADR. Without a self-testing fixture, a future ESLint or TypeScript
upgrade can silently disable a rule and the mitigation evaporates with nobody noticing.
The fixture is what makes this decision safe.

Compatibility was verified before deciding, not assumed: `eslint-plugin-solid` 0.14.5
declares peer `eslint ^6 || ^7 || ^8 || ^9` and `typescript >=4.8.4`, and ships
flat-config ESM exports (`./configs/recommended`, `./configs/typescript`), which matches
this repo's ESLint 9 `eslint.config.mjs`.

### 4. Migration is strangler-style, and migrated surfaces are rewritten

Wrapping the existing DOM-building classes in Solid components without changing their
ownership preserves the exact defects listed in Context. `nocx-x6w9` is the proof: two
modules independently building the same chrome is an ownership failure, and a wrapper
keeps it.

Order, as `nocx-njrx` carries it: Solid root and lint gate → application state module →
framework-neutral transition functions → split `tabs.ts` along the terminal boundary →
component primitives → surface-by-surface migration → delete each imperative predecessor
as its replacement lands. A surface is not migrated while both versions exist.

Behaviour is preserved surface by surface. **A rewrite that also redesigns cannot be
reviewed**, so behaviour changes are separate beads.

## Rationale

### Why a framework at all, rather than signals alone

The considered middle path was reactive primitives (`@preact/signals-core` or similar)
with no component layer, keeping direct DOM control.

It was rejected because **signals fix state propagation but not component ownership**.
`nocx-x6w9` — the same chrome built twice by two modules — would not have been prevented
by any signals library. It is prevented by having components. Signals-only would leave us
maintaining an informal component framework: mount/dispose rules, subscription cleanup,
keyed collection reconciliation, focus preservation, event ownership.

Adopting signals now as a deliberate step toward a framework we expect to need later is
worse than either endpoint: it produces two migrations and two state APIs.

### Why Solid rather than Svelte 5

Both satisfy every constraint here: fine-grained reactivity, no virtual DOM, no
reconciliation over the xterm subtree, a small runtime, incremental migration. The
technical case is close to a tie, and this was argued in both directions before landing.

The argument **against** Solid was that its JSX resembles React closely enough that agents
— which author nearly all code in this repository — produce plausible-looking Solid with
React semantics: destructured reactive props (which break reactivity silently), components
treated as re-running, derived state wrapped in effects, `.map()` and JSX conditionals in
place of `<For>` and `<Show>`. Code that type-checks and reads as idiomatic is the worst
defect class for a reviewer.

That argument does not survive contact with `eslint-plugin-solid`, whose rules cover
essentially that entire list. This repository enforces lint in the pre-commit hook and in
CI, so those failures are **not silent here** — provided the fixture in §3 keeps proving
the rules are live.

Two facts were weighed against Solid and judged insufficient:

- **Maintenance asymmetry.** `eslint-plugin-solid` 0.14.5 was published 2024-12-11;
  `eslint-plugin-svelte` 3.22.0 was published 2026-07-20. The mitigation Solid depends on
  is much less actively maintained. This is converted into a managed risk by pinning the
  version and by the negative fixture, which turns a silent rule regression into a CI
  failure.
- **Solid 2.0 is in experimental** (`2.0.0-experimental.16`) while `solid-js` stable is
  1.9.14, published the same day as this ADR. A project experimenting with its next major
  while actively maintaining the current one is normal and is not evidence that 1.x is
  obsolete.

What remains, and what decides it: **Solid adds component ownership to the signal-based
architecture nocx already needs, without introducing either a virtual DOM or a second
conceptual state model.** The progression from "we need signals" to "we need signals plus
components" is continuous rather than a change of model, and the xterm boundary stays
exceptionally explicit.

Svelte 5 remains a legitimate near-peer and its tooling is better maintained. Where the
technical case is a tie, the owner's stated preference for Solid breaks it. That is
recorded as a tiebreak between near-peers, **not** as a technical argument.

### Rejected outright

- **Staying fully vanilla.** The application has crossed the size and interaction
  threshold where manual ownership is producing shipped bugs.
- **React.** An unnecessary virtual DOM and awkward imperative lifecycle for the central
  xterm island; Strict Mode makes imperative resource ownership easy to get wrong. Its
  ecosystem pull was `@tanstack/react-virtual` for `nocx-708q`, and the useful core
  (`@tanstack/virtual-core`) is framework-agnostic, so that pull does not exist.
- **Vue.** Capable; wrong weight-to-benefit ratio here.
- **Lit as the application framework.** Web-component encapsulation does not address
  coordinated application state, and shadow DOM complicates styling.
- **RxJS or MobX as the primary store.** Too abstract, or too implicit, for ordinary UI
  state.
- **A large prebuilt visual component suite.** It will fight the terminal aesthetic,
  inflate the bundle, and carry styling assumptions unsuited to embedded webviews.
- **A home-grown signals implementation.** Dependency tracking, batching, cleanup,
  equality and re-entrancy are not product differentiators.

## As shipped (2026-07-27, epic `nocx-njrx`)

The migration is complete for the surfaces it targeted. What follows states what the code
actually does, not what was planned; where the two differ, the difference is named.

**Solid renders parts of the application shell and owns the component kit.** It does not
own every surface. Measured on the shipped build:

- `App.tsx` renders the skeleton — `#tabbar`, `#activitybar`, `#sidebar`, `#panes` — as
  **empty hosts** with no reactive state. `index.html` carries only `<div id="app">`;
  `main.tsx` is the composition root.
- The activity bar (`SidebarSolid`) renders via Solid (`<For>`, `createEffect`,
  `createMemo`).
- The tab strip renders its static wrapper (`<div class="tabs-container">`, add button,
  spacer) in Solid, but every tab button is built imperatively: `createElement` per tab,
  `paintButton` for display updates, `innerHTML = ''` for reordering. There is no `<For>`.
  (`nocx-82l9.5` — this is pending work, not the design.)
- The clipboard banner (`ClipboardBannerImpl`) is a Solid component.
- The export section, connections manager and settings screen each render from a Solid root
  mounted into a pane by the `TabContent` seam.
- The `UpdateNotice` (application surface in the tab bar) is a vanilla DOM class using
  `createElement` and `innerHTML` resets. (`nocx-82l9.4` — pending.)
- The sidebar panel (title element, per-view content containers, `style.display` toggling)
  is built imperatively inside a `createEffect`. (`nocx-82l9.6` — pending.)

**There is not one Solid root.** `App.tsx` owns the shell layout. Per-tab surfaces
(settings, connections, export) each open their own Solid root inside their pane. The
banner, sidebar and tab strip also open independent roots. The six intermediate roots the
migration passed through on its way here are gone — `main.tsx` creates one shell root, and
every surface-specific root is opened inside a host element the shell provides.

The imperative predecessors deleted per-surface, preserved here for the historical record:
`banner.ts`, `sidebar.ts`, `export-section.ts`, `connections.ts`, `settings.ts` and
`tab-strip.ts` are gone, together with roughly 3,700 lines of structural tests that
asserted the shape of the DOM those files happened to build.

**What is deliberately still imperative**, and is not "not yet migrated":

- `tabs.ts` — the boundary file. Its tab-list half now delegates to the Solid tab strip;
  its terminal-lifecycle half stays imperative by §1. `Tab` creates exactly one element,
  the pane, which is the empty terminal host AD-6 requires.
- `tab-content.ts`, `terminal-content.ts`, `renderers/`, `scrollback/`, `editor.ts`,
  `input-state.ts`, `input-target.ts`, `dispatcher.ts`, `gutter.ts`, `command-ledger.ts`,
  `clipboard.ts`, `frame.ts`, `submit.ts`, `ipc.ts` — terminal-owned or protocol code,
  permanently framework-neutral.
- `settings-content.ts` (91 lines) and `connections-content.ts` (50) — `TabContent`
  adapters that open a Solid root inside a pane. They are the seam, not UI.
- `settings-domain.ts`, `export-utils.ts`, `profiles.ts`, `agent-status.ts`,
  `surface-registry.ts` — framework-neutral logic and clients, which is where §2 wants
  authority to live.

**The three defects fixed inside the migration were real.** As acceptance criteria rather
than separate patches: `nocx-x6w9` (the settings surface now has exactly one search box
and one modified filter, pinned by tests), `nocx-ucxl` (selecting a rail section always
changes the content pane), `nocx-rp2j` (panel-views and tab-actions are separate concepts,
so no empty panel opens at cold start).

**Three defects shipped**, all from surfaces that are partially imperative or entirely
vanilla. These are structural — the next epic (`nocx-82l9`) owns fixing them:

- **Settings does not scroll** (`nocx-82l9.2`). `.pane` is `position:absolute; inset:0;
overflow:hidden` and `SettingsContent.mount` appends an unclassed `<div>`, so the
  `flex:1; overflow-y:auto` scroll chain is broken. Reproducible in WebKit only.
- **Vertical tab placement is broken** (`nocx-82l9.3`). There is no `#vertical-tabstrip`
  container alongside `#body`. `App.tsx` renders `#tabbar` then `#body`, and both
  orientations mount into the same `#tabbar` element.
- **UpdateNotice keeps a stale class** (`nocx-82l9.1`). `showAvailable()` does not reset
  `className`, so a previous error class persists across states.

**Bundle, measured on the shipped build:** 623,145 bytes raw / 162,827 gzip, against the
pre-adoption baseline of 600 KB raw / 152 KB gzip recorded below — about **+7 KB gzip net**,
against a budget of 25–35 KB. Net rather than gross: Solid's runtime and the component kit
are partly paid for by the imperative code they replaced.

**The §3 lint gate is real and was verified by breaking it, three times during the epic.**
`eslint-plugin-solid` runs at `error` on `.ts` as well as `.tsx` — the extension was
necessary, because the state module in §2 is plain `.ts` and `flat/recommended` leaves
`reactivity` and `no-react-deps` at _warn_ there while `prefer-show` is off entirely — and
`npm run lint` carries `--max-warnings 0`. `lint-fixtures/gate.sh` asserts that each of the
seven required rules fires **by name**, and separately that `solid/reactivity` fires **from
a `.ts` file**, which a `.tsx`-only fixture could never catch.

**What the boundary cost, honestly.** The one real regression of the epic was an AD-6
ordering bug, and it was caught by WebKit and by nothing else: moving the pane's `active`
class from the shell to the `TabContent` seam left the pane invisible during `mount()` and
the first geometry delivery, a hidden pane measures ~0 width in WebKit but not in Chromium,
and the settings surface silently rendered its narrow layout with no content column. 544
unit tests and all 20 Chromium specs were green. That is the evidence for keeping the
`webkit` project in `playwright.config.ts`, alongside `nocx-q18`.

**The next iteration is designed.** The design spec at
`.internal/specs/2026-07-27-ui-shell-kit-and-theming-design.md` (epic `nocx-82l9`) describes
the shell layout, component kit, token layer, and the fix for each shipped defect. The
surfaces named `nocx-82l9.4` through `nocx-82l9.6` above are the pending migration targets
from that design's §7.

## Consequences

- Bundle grows by Solid's runtime. Measured baseline before adoption: **600 KB raw /
  152 KB gzip**, dominated by xterm.js. Set and enforce a budget of roughly **25–35 KB
  gzip** for framework, store and initial component primitives combined.
- `nocx-njrx` blocks five epics — `nocx-ycet`, `nocx-vxqj`, `nocx-708q`, `nocx-9le`,
  `nocx-8yg` — on real file collisions. `nocx-2gf` and `nocx-4ff` are deliberately not
  blocked: they live entirely in files that stay imperative.
- `nocx-ycet` is largely absorbed. Solid brings the reactive container, so what survives
  is the slice-ownership map and the authority boundary, not a library choice.
- `nocx-vxqj` becomes a Solid component kit rather than a framework-neutral one.
- `nocx-708q`'s file tree is built natively in Solid over `@tanstack/virtual-core`.
- Verification runs against WKWebView / WebView2 / **WebKitGTK**, not Chromium. "Works in
  Chrome" is not evidence. Terminal mount and dispose counters are asserted by test —
  they are what prove the §1 boundary held.
- New agent-facing rules: runes-equivalent discipline is enforced by lint, not review, and
  agents copy one canonical component, one state module and one xterm adapter as patterns.

## Revisit when

- The negative fixture fails to fail — i.e. `eslint-plugin-solid` stops working against
  the pinned ESLint/TypeScript toolchain. That is the falsification condition for §3, and
  it makes the Svelte 5 comparison live again.
- A bake-off or accumulated review evidence shows materially more silent behavioural
  defects in agent-authored Solid than the equivalent Svelte would carry — roughly 25%
  is the threshold worth acting on.
- Solid 2.0 reaches stable, its ecosystem follows, and it offers nocx something concrete.
  Until then: pin 1.x, write against Solid 1 semantics, build no 2.x compatibility
  abstractions.
- The §1 boundary proves unsustainable — if keeping xterm outside the reactive tree starts
  requiring escape hatches rather than one adapter, the boundary is wrong and the
  architecture, not the rule, needs revisiting.
