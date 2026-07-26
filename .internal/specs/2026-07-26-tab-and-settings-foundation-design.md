# Tab presentation, tab content, and authoritative settings state — design

Date: 2026-07-26
Status: approved for implementation
Brainstorming session bead: nocx-2dxm

## Why this exists

The settings screen is mounted inside the collapsible sidebar panel
(`frontend/src/main.ts:154-163`, `action: 'panel'`). `#sidebar` is 240px wide with
`overflow: hidden` (`frontend/src/style.css:701-708`); `.st-view` adds `16px 24px` padding,
leaving 192px of content width; `.st-label-col` is declared `flex: 0 0 200px`
(`frontend/src/style.css:1507`) — a fixed column wider than the box that contains it. The
control column is therefore pushed past the right edge and clipped. This is arithmetic, not
taste: no CSS tuning makes a two-column settings screen usable in 192px.

Settings must be a dedicated tab, as in Warp. Pulling that thread exposed three separate
foundations that are currently tangled, and the project brief is explicit: lay the
foundation correctly, no quick wins, no rewriting later.

## Reference survey

Three products were examined, plus a second-opinion design review.

**Warp** — Settings is a plain tab in the tab strip (in horizontal mode) and an entry under a
separate "Settings" group in the vertical tab list. Inside: a header, a ~360px nav column
(search at top, categories with expandable chevrons, "Open settings file" pinned at the
bottom) and an independently scrolling content column.

**Tabby** (`~/repos/tabby`) — solves tab polymorphism by **inheritance**:
`BaseTabComponent` (264 lines) with five subclasses (`BaseTerminalTabComponent`,
`SettingsTabComponent`, `SplitTabComponent`, `WelcomeTabComponent`,
`ReleaseNotesComponent`). The consequence is visible in the base class: `getRecoveryToken()`
and `getCurrentProcess()` return `null` as no-op defaults for subclasses to override —
terminal/session concepts hoisted into the base so non-terminal tabs can opt out. Because
`SplitTabComponent` is itself a tab containing tabs, the base also carries `parent`,
`topmostParent` and `effectivelyPinned` walking a parent chain. Tabby's settings screen is
hand-written: 13 templates, one component per section. **Conclusion: inheritance rejected;
our generated registry is already structurally ahead of Tabby's settings.**

**Orca** (`~/repos/orca`) — solves it by **data modelling**. `Tab` (`src/shared/types.ts:815`)
is a pure record with no methods and no DOM: `id`, `entityId` (the ID of the backing
content — terminal tab ID, file path, browser workspace ID), `groupId`, `contentType`,
`label`, `customLabel`, `color`, `sortOrder`, `createdAt`, `isPinned`. `TabContentType` is a
serializable string union (`'terminal' | 'editor' | 'diff' | 'conflict-review' |
'check-details' | 'browser'`). Content-specific state lives in a separate `TerminalTab`
record (`ptyId`, titles). `TabGroup` carries `activeTabId`, `tabOrder` and `recentTabIds` —
a per-group MRU stack, so closing the active tab returns to the previously active tab rather
than a visual neighbour. **Conclusion: a shipping product uses serializable string
identities, not symbols, and separates the tab record from its content record.** Caution
from the same source: Orca's tabs slice is 1987 lines — an unbounded tab model grows without
limit, so ours stays small and does not speculatively grow splits or groups.

Orca needs `entityId` because its state lives in a serializable store. We are not adopting a
store, so holding a content object directly is fine — only the _restore descriptor_ must be
serializable.

## Binding constraints

- `AGENTS.md`: interface-first + DI wired at a single composition root; SRP; **no
  backward-compatibility shims** (greenfield — break and refactor freely); no dead code;
  YAGNI; TDD red→green→refactor; structured logging via the logging interface.
- `docs/architecture.md`: AD-1 (one WebSocket — binary data plane + JSON-RPC control plane),
  AD-6 (terminal render state lives in the frontend), AD-7 (server-authoritative session id).
- ADR-0011: DocumentStore holds "bounded, **human-recoverable** configuration"; a user
  opening and repairing those files in an editor is a feature. Secrets are opaque references
  in SecretStore with **no read path**.
- `nocx-9m5` / `nocx-1ei`: the settings screen is **generated** from typed declarations in
  `internal/settings`. There is no literal setting key anywhere in the frontend. Adding a
  setting is one `MustRegister*` call in Go and zero frontend changes. **This invariant is
  binding on everything below.**

## The three-bucket rule for settings capabilities

Every settings capability falls in exactly one bucket. Nothing is ever special-cased in
TypeScript by key.

**Bucket 1 — derivable from today's declarations.** Control rendering (`control`); section
grouping and first-occurrence ordering (`section` + declaration order); search over label,
description, section, stable key and select-option labels; deep-link target (`key`);
default-value display (`default`); reset eligibility (non-secret + has `default`); numeric
bound display (`min`/`max`); privacy and export treatment (`dataClass`); secret
Configured/Not-configured treatment; section-level modified and error counts; "modified
only" filtering; generated accessibility relationships; search-result breadcrumbs.

**Bucket 2 — requires a declaration-schema extension, and is therefore OUT OF SCOPE until a
real declaration needs it.** Search aliases/synonyms; section ordering independent of
declaration order; nested categories; section icons and descriptions; units (px/ms/lines);
arbitrary validation guidance beyond declared bounds; restart-required/apply-scope metadata;
rich control affordances (placeholder, multiline, pickers). When these arrive they arrive as
declaration schema in Go, never as frontend exceptions.

**Bucket 3 — generic infrastructure, not per-declaration metadata.** Loading / load-failed /
empty / no-search-match screen states; provenance in the snapshot; runtime change
notification; save pending/error/race handling; responsive layout; search ranking and
keyboard semantics; the deep-link router; snapshot revision tracking; "open settings file";
export/import.

The declaration says what a setting _is_. The snapshot says its current _state_. The
transport says _when state changed_. The UI shell says _how users navigate it_.

## Part A — Authoritative settings state and propagation

### A.1 Snapshot contract

`settings.getAll` becomes `settings.getSnapshot` and returns:

```ts
interface SettingsSnapshot {
  values: Record<string, unknown> // effective, non-secret
  overridden: string[] // non-secret keys with a stored override
  revision: number // monotonic, per backend instance
}
```

Rationale for each field:

- `values` keeps the convenient effective-value lookup that the generated screen already uses.
- `overridden` supplies the fact the registry knows and the wire currently drops. Without it,
  export cannot distinguish "the user chose this" from "this happens to be the default", and
  an exported profile would silently pin every default against future default changes.
  Export/import is already filed (`nocx-6ek.3`), so the contract must be right before it is
  built.
- `revision` supports propagation ordering, gap detection and reconnect handling.

Rejected: per-key `{value, source}`. `source` invites profiles, policies, workspace
overrides, environment overrides and inheritance — none of which exist. A boolean membership
set states exactly what the registry knows today.

**Secret keys are absent from both `values` and `overridden`.** Including them would turn the
snapshot into an existence oracle; presence stays available only through the existing
`settings.secretExists`.

The revision is an in-memory instance epoch. It is **not persisted**. Clients always fetch a
full snapshot on connect, so a reset counter after a backend restart is harmless. If
cross-restart comparison is ever needed, use `{instanceId, revision}` rather than persisting.

### A.2 "Customized" is provenance, not value comparison

A row is **Customized** iff its key is in `overridden`; otherwise it is **Default**. An
explicit override that happens to equal the current default is still customization, because
it pins that value against future default changes — which is precisely the distinction export
depends on. Reset removes the customization; the reset RPC already exists
(`frontend/src/profiles.ts:295` → `settings.reset`; the Go side deletes the override) and the
UI has never exposed it.

Secret rows never claim "modified from default" — they have no default. Their state is
Configured / Not configured.

### A.3 Control-plane dispatcher

Today `WSClient` (`frontend/src/ipc.ts`) and `ProfileClient` (`frontend/src/profiles.ts`)
both listen to every message on the same socket, and `ProfileClient` avoids collisions by
starting its request IDs at 100000. Its own comment (`profiles.ts:184-188`) states the
failure mode: "an ID collision would make one client swallow the other's response, causing
the form to never open." A third listener for settings notifications would make this worse.

One control-plane dispatcher owns: request-ID allocation, pending-request correlation,
notification routing, disconnect/reconnect behaviour, and typed subscribe/unsubscribe.
`ProfileClient`, settings access and session control consume it. The disjoint ID ranges are
deleted, not preserved.

### A.4 Change propagation

Backend-originated, over the existing JSON-RPC control plane. The precedent exists: the
`exit` notification is already handled as a method without an `id` (`frontend/src/ipc.ts:443`).

```json
{
  "jsonrpc": "2.0",
  "method": "settings.changed",
  "params": { "revision": 42, "keys": ["terminal.fontSize"] }
}
```

Rules:

- Emitted **only after the store operation has succeeded** — it reports committed state, not
  write intent.
- Emitted by the settings application service after mutation, not by each WebSocket handler,
  so `set`, `reset`, secret set/delete, import and migration cannot diverge.
- Plural `keys` so batch operations do not produce a notification storm.
- Broadcast to all connected clients. The server currently tracks per-session subscribers
  only, so this requires a connection registry with safe unregister — real work, not a
  one-line addition.
- Clients treat it as invalidation: expected successor revision → refresh; gap → fetch a full
  snapshot; reconnect → fetch a full snapshot; duplicate or older revision → ignore. At the
  current scale, refetching the whole (small) snapshot after every notification is correct
  and far less error-prone than carrying values in notifications.
- Secret changes may name the key (presence is already exposed to authorized frontend code
  via `secretExists`) but never carry a value.

A frontend-only event bus is rejected: it would leave every client except the writer stale,
and it creates a second owner of a truth the backend already owns — the failure this codebase
has already been bitten by (`nocx-aok`).

## Part B — Tab presentation and tab content

### B.1 What is wrong today

`class Tab` (`frontend/src/tabs.ts:61`) owns three unrelated things:

1. **Chrome** — `pane`, `button`, `closeBtn`, `indexLabel`, `titleSpan`, `statusIcon`,
   `label`, `indicator`, title/activity/agent-status.
2. **Terminal machinery** — `renderer`, `session`, `editor`, `shellTarget`, `scrollback`,
   `ledger`, `inputState`, `_markers`, `cols`/`rows`, `resizeTimer`, `_cwd`, `_host`,
   `_lastExitCode`, `_bufferType`.
3. **An escape hatch** — `managerView: ConnectionManagerViewImpl | null` (line 120). When set,
   `Tab.start()` early-returns (lines 284-290) and `TabManager.activate()` logs
   `isManager: !!tab.managerView` (line 1096): the class peeks at which kind it is.

The tab-button wiring (click, close, middle-click, dragstart/dragend/dragover/drop) exists in
two near-identical copies: `newManagerTab` (937-969) and `createTab` (1006-1040).

### B.2 There are no `Tab` subclasses

`Tab` stays a single class. Polymorphism lives in the content, behind an interface, by
composition. `managerView` is deleted outright — no shim.

### B.3 The `TabStrip` presentation port

`TabManager` currently constructs the tab-list container and add button itself
(`tabs.ts:808`) and directly reorders button DOM (`tabs.ts:1127`). That is exactly the
coupling `nocx-d3q.1` exists to remove, so the port is pulled forward into this foundation
rather than left for the placement epic to refactor a second time.

- `Tab` is state and lifecycle. It does not own tab-button DOM.
- A `TabStrip` implementation creates, places and styles chrome, and emits intents:
  activate, close, new-tab, reorder.
- `TabManager` owns the ordered tab model and activation rules, and consumes intents.
- The horizontal implementation ships first and reproduces today's behaviour exactly.
- Keyboard and ARIA semantics belong to the port and are done here, not retrofitted: roving
  `tabindex`, Left/Right in horizontal placement, Up/Down in vertical, Home/End, focus-visible,
  a stable tab↔tabpanel relationship, and a decision recorded on whether drag-reorder has a
  keyboard equivalent. The tab button is currently a `div` (`tabs.ts:64`).

### B.4 The `TabContent` seam

```ts
interface ContentViewport {
  width: number
  height: number
  devicePixelRatio: number
}

interface TabHost {
  setTitle(title: string): void
  requestAttention(): void
  requestClose(): void
}

interface TabContent {
  mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void>
  viewportChanged(viewport: ContentViewport): void
  focus(): void
  dispose(): void
}
```

Content pushes up; `Tab` renders chrome. Content never touches the tab button; `Tab` never
touches the session.

Host vocabulary decisions:

- `requestAttention()` rather than `setActivity(boolean)`: activity is cleared by tab
  activation today (`tabs.ts:171`), so content should report an attention-worthy event, not
  own the chrome's derived boolean.
- `setBufferType` is **not** on the host. It exists to toggle global app layout when the
  active terminal enters alt-screen (`tabs.ts:1050`), which is not tab chrome. It is modelled
  as a terminal presentation concern.
- Agent status stays terminal-specific unless and until a second content type needs a chrome
  badge; if it becomes generic it becomes a typed decoration, not an agent-shaped method.
- The host object is scoped to one mounted tab and becomes **inert after disposal**, so late
  asynchronous callbacks cannot mutate recycled UI.

### B.5 Geometry ownership

Three owners, three different quantities, no one reaching into another:

```
DOM/layout change
  → TabStrip / pane presentation measures the allocated viewport (CSS pixels)
  → TabContent.viewportChanged({ width, height, devicePixelRatio })
  → TerminalContent asks TerminalRenderer to fit that viewport
  → TerminalRenderer computes cols/rows from real cell metrics
  → TerminalContent debounces and sends the PTY resize
```

Placement owns the rectangle. The renderer owns the conversion to a grid. `TerminalContent`
owns PTY resize policy. A `ResizeObserver` is a legitimate measurement mechanism, but it
lives in the presentation layer and its output flows down as an explicit structured
measurement — content never independently interprets container geometry, because that would
strip the placement layer of the authority this refactor exists to give it.

The renderer may still observe things placement cannot express (font loading, glyph metrics,
device-pixel-ratio changes). Those recompute the grid **within the last authoritative
viewport**; they never redefine the viewport.

Delivery rules: no viewport callback before `mount` starts; equal consecutive rectangles may
be suppressed; the latest viewport is replayed after an asynchronous mount completes;
callbacks stop after disposal; hidden or inactive tabs are never sent a misleading zero
rectangle; activation delivers the current real rectangle before focus. Raw observer
callbacks are coalesced per animation frame by the presentation layer; `TerminalContent`
keeps its own PTY debounce, because DOM batching and remote SIGWINCH throttling solve
different problems.

### B.6 Lifecycle guarantees

`mount()` is asynchronous and today's `start()` already has a long async path through
renderer mounting and session opening (`tabs.ts:295`). Without explicit guarantees, closing
or switching during mount lets a completed mount open a PTY into a detached pane.

- `mount` is called at most once per content instance.
- `dispose` is idempotent.
- `dispose` cancels an in-progress mount via the `AbortSignal` passed to `mount`.
- Host callbacks are inert after disposal.
- `focus` may only occur after a successful mount, or must safely queue.
- Mount failure has a defined policy: the chrome remains usable and the tab can be retried or
  closed; the content decides which partial resources to tear down.
- Inactive content **stays mounted** until the tab is closed, so Settings keeps its search
  query, selected section and scroll position.

### B.7 Identity

Two branded string types, centrally registered at the composition root — registered, not
dynamically generated. These are protocol-like constants and must stay stable across releases.

```ts
type SurfaceType = string & { readonly __brand: 'SurfaceType' }
type SingletonKey = string & { readonly __brand: 'SingletonKey' }
```

- `SurfaceType` — serializable content type, used in restore descriptors and registration.
- `SingletonKey` — optional; present only for content that deduplicates open instances.
- A deep link resolves a `SurfaceType` through the surface registry, then a surface-specific
  target.

```
settings:   surface nocx.settings   singleton nocx.settings   target terminal.fontSize
terminal:   surface nocx.terminal   singleton absent          restore local/SSH descriptor
```

Symbols are rejected: they cannot be serialized, and both tab restore (`nocx-8yg.3`) and deep
links require an identity that survives a restart. `TabManager` compares optional singleton
keys and never asks whether content is Settings.

### B.8 View-tab policies are model state, not comments

Four existing sites assume every tab is a terminal. Each needs an explicit policy on the tab
model — restore descriptor or `null`, attention capability, badge/decoration state, singleton
key, fixed initial title — rather than a `kind` test:

| Site                | Current assumption                                                              |
| ------------------- | ------------------------------------------------------------------------------- |
| `tabs.ts:861-863`   | `getTabs()` substitutes `'Terminal'` for an empty title                         |
| `tabs.ts:1077-1079` | closing the last tab opens a fresh **terminal**                                 |
| `tabs.ts:1172-1177` | Cmd/Ctrl+1..9 addresses all visual tabs                                         |
| `tabs.ts:847-851`   | `_initialTabReady` from `newTab().ready` gates updater health via `main.ts:168` |

The last one is the trap: after content extraction, "the first tab mounted" must **not** be
redefined as application health. The criterion is specifically "the initial terminal renderer
mounted and the PTY opened", and it stays owned by `TerminalContent` or a startup coordinator.

Decisions recorded: view tabs are **not restored on restart** (only terminal tabs are); view
tabs have no cwd, no exit code, no activity bell and no SSH profile badge.

Adopted from Orca: closing the active tab activates the **previously active** tab (MRU),
not the visual neighbour as `tabs.ts:1086-1088` does today.

## Part C — Settings as a tab

Entry: the activity-bar gear changes from `action: 'panel'` to opening the singleton Settings
tab; `Cmd+,` opens or focuses the same tab. Connections migrates onto the same content seam,
so both system surfaces work the same way.

Layout — width-responsive, never content-count-responsive:

```
Wide viewport                              Narrow viewport
┌───────────────┬──────────────────────┐   ┌────────────────────────┐
│ Search        │ Settings             │   │ Settings               │
│               │                      │   │ Search                 │
│ Clipboard     │ Clipboard            │   │ Category picker        │
│               │ generated rows…      │   │ generated rows…        │
│ Modified (0)  │                      │   │                        │
└───────────────┴──────────────────────┘   └────────────────────────┘
```

The rail is structurally permanent. With one declared section it renders one item and looks
sparse — that is accepted. It is not dead code: it carries search, section navigation, the
modified-only filter and count, and the error count, and it exercises the same layout,
selection, focus and scroll-synchronisation code that stays in production. Making the rail
appear based on **section count** is rejected: four more declarations are already filed and
unblocked (`nocx-8yg.5`, `.6`, `.7`, copy-on-select), so a count-driven mode would be
exercised for weeks and then rot, and the screen would change shape as the product grows.

Rail width is `clamp(240px, 28vw, 360px)`; the breakpoint is chosen by whether both columns
remain usable, not by Warp's screenshot dimensions. The content column scrolls independently
of the rail.

Generated behaviour, all from bucket 1: Customized/Default state and Reset per row;
modified-only filter with a count; `dataClass`-driven privacy/export treatment (currently
declared in both `settings.ts:15` and Go and **ignored by the UI**); declared bound display;
search over label, description, section, key and option labels with section breadcrumbs and
keyboard operation; a stable per-row DOM id derived from the key, and a deep link that opens
or focuses the tab, clears a hiding filter, scrolls the row into view, focuses the control and
briefly highlights it.

"Open settings file" is **deferred, not rejected** — ADR-0011 explicitly makes
human-recoverable config a feature. It is deferred because safe open/reveal needs host
integration, and when it lands it opens only the non-secret document.

Do not interpolate a raw key into a CSS selector as `rerenderRow` does today
(`settings.ts:356`): `assertValidKey` (`internal/settings/settings.go:440-449`) only checks
non-empty and uniqueness. Keys are authored by us in Go, so this is fragility rather than a
vulnerability — fix it with a key→element map.

## Part D — Placement (the existing `nocx-d3q` epic)

The vertical `TabStrip` implementation, the persisted placement declaration, live application
through the settings observer, and Warp-style grouping of Settings in the vertical list. No
port or content seam from Parts A–C is reopened.

## Defects fixed along the way

- `nocx-q07f` (P1) — `saveSetting` (`settings.ts:319-330`) never writes the saved value into
  `this.values`, then `rerenderRow` rebuilds the control from the stale value, so a successful
  save visually reverts. On failure it also discards the user's rejected input.
- `nocx-jwkw` (P2) — `refresh()` swallows every fetch failure and `render()` then prints
  "No settings declared", making a dead RPC indistinguishable from an empty registry. Secret
  existence is also fetched sequentially in a `for await` loop.

## Out of scope, filed separately

A general Modal primitive replacing the native `prompt()` for secrets (`settings.ts:300`);
"open settings file" host integration; bucket-2 declaration metadata; Warp-style Settings
grouping in the vertical list (Part D).

## Verification

The reliable gate is `gofumpt -l .`, `golangci-lint run`, `go test -race ./...`,
`npx prettier --check .`, `npx eslint .`, `npx tsc --noEmit`, `npx vitest run`.

The Playwright e2e suite is **red on `main`**: 13 tests fail (`nocx-bw2`) and Playwright is
not in the per-commit gate. New e2e specs may be written, but no claim of correctness may
rest on an e2e run; their status is reported explicitly rather than presented as passing.

Test layering after the extraction:

1. Generic tab lifecycle against a fake `TabContent` — chrome without a PTY, which is
   impossible today.
2. `TerminalContent` against renderer/session fakes.
3. `TabStrip` presentation with no terminal machinery.

`frontend/src/tabs.test.ts` is currently coupled to renderer and session mocks from its first
test (`tabs.test.ts:42`) and is the safety net for the extraction.

The lifecycle and integration tests matter more than rendering tests: close during
asynchronous mount leaks no session; mount rejection leaves usable chrome; rapid activation
does not let an earlier async activation steal focus; racing singleton opens produce one tab;
closing and reopening Settings creates a fresh view; `Cmd+,` focuses existing Settings from
terminal, editor and alternate-screen states; a save while a filter is active does not make
the row vanish; a deep link into a filtered section reveals and focuses it; view tabs never
enter restore records; terminal readiness — not generic view mount — gates updater health.
