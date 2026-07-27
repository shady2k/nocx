# Frontend State Ownership

**Deliverable of `nocx-82l9.7`.** Satisfies §8 (state ownership) of the
[2026-07-27 shell/kit/theming design](../.internal/specs/2026-07-27-ui-shell-kit-and-theming-design.md).
Binding ADs: AD-6 (single-owner state), AD-7 (session-id is server-authoritative),
ADR-0012 §2 (authority chain: Go → framework‑neutral .ts → Solid signals → components).

---

## 1. The ownership table

23 rows, one per piece of frontend state. Every row is verified against the code;
none are speculative.

| State                                    | Owner                                                                     | Authority                                                            | Persistence                                                                     | Lifetime      |
| ---------------------------------------- | ------------------------------------------------------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------- | ------------- |
| Tab list                                 | `TabManager` (`tabs.ts:265`, `tabs: Tab[]`)                               | Frontend (TabManager)                                                | Not persisted                                                                   | Process       |
| Active tab                               | `TabManager` (`tabs.ts:267`, `activeTab`)                                 | Frontend (TabManager) — derives from tab-list order + activation     | Not persisted                                                                   | Process       |
| Tab MRU order                            | `TabManager` (`tabs.ts:278`, `recentTabIds`)                              | Frontend (TabManager)                                                | Not persisted                                                                   | Process       |
| Tab placement (`tab.placement`)          | **Go** `settings.Registry` (`internal/settings/settings.go:470`)          | Go backend — `ProfileClient.getSnapshot()` + live `SettingsObserver` | Go settings doc                                                                 | Process       |
| Per-tab title                            | `Tab` (`tabs.ts:41-42`, `_title` / `_programTitle`)                       | Frontend — set via `TabHost.setTitle()` from terminal OSC            | Not persisted                                                                   | Tab           |
| Per-tab activity flag                    | `Tab` (`tabs.ts:43`, `_hasActivity`)                                      | Frontend — `Tab.requestAttention()` / `markActivity()`               | Not persisted                                                                   | Tab           |
| Per-tab agent status                     | `Tab` (`tabs.ts:44`, `_agentStatus`)                                      | Frontend — `detectAgentStatus()` from title text                     | Not persisted                                                                   | Tab           |
| Per-tab tooltip                          | `Tab` (`tabs.ts:45`, `_tooltip`)                                          | Frontend — `TerminalContent` via cwd/SSH info callback               | Not persisted                                                                   | Tab           |
| Tab pane geometry (viewport)             | `Tab` (`tabs.ts:49-50`, `_viewportObserver`, `_latestViewport`)           | Frontend — `ResizeObserver` on pane element                          | Not persisted                                                                   | Tab           |
| Active sidebar view                      | `SidebarSolid` → `sidebar` slice of store (`sidebar-model.ts:29`)         | Frontend — icon click                                                | **One versioned localStorage record** (§3)                                      | Process       |
| Sidebar collapsed                        | `SidebarSolid` → `sidebar` slice of store (`sidebar-model.ts:27`)         | Frontend — icon click, Ctrl/Cmd+B                                    | **One versioned localStorage record** (§3)                                      | Process       |
| Selected theme                           | **Go setting `ui.theme`** (decided — §3)                                  | Go backend                                                           | Go settings doc, plus a bootstrap **cache** in the local record (ADR-0013 §8.1) | Process       |
| Accepted settings values                 | `settings.tsx:84` (`createStore`) + `settings-domain.ts` `SettingsMirror` | Go backend — validated via `AcceptedSnapshot` revision gate          | Go settings doc                                                                 | Process       |
| Settings revision                        | `settings.tsx:88` (`createSignal`)                                        | Go backend — monotonic counter in `settings.Registry`                | Go settings doc                                                                 | Process       |
| Settings draft values                    | `settings.tsx:85` (`createStore`)                                         | Frontend — unsaved edits                                             | Not persisted                                                                   | Draft         |
| Settings validation errors               | `settings.tsx:87` (`createStore`)                                         | Frontend — local validation                                          | Not persisted                                                                   | Draft         |
| Settings search query                    | `settings.tsx:91` (`createSignal`)                                        | Frontend                                                             | Not persisted                                                                   | Surface mount |
| Settings modified-only filter            | `settings.tsx:92` (`createSignal`)                                        | Frontend                                                             | Not persisted                                                                   | Surface mount |
| Settings section filter                  | `settings.tsx:93` (`createSignal`)                                        | Frontend                                                             | Not persisted                                                                   | Surface mount |
| Profile/group/credential lists           | `connections.tsx:62-64` (3 `createSignal`)                                | Go backend — `ProfileClient` RPC calls                               | Go settings doc                                                                 | Surface mount |
| Connection selection / editing           | `connections.tsx:67-69` (3 `createSignal`)                                | Frontend — UI state                                                  | Not persisted                                                                   | Draft         |
| Connection form error                    | `connections.tsx:486` (`createSignal`)                                    | Frontend                                                             | Not persisted                                                                   | Draft         |
| Export-section state (25 `createSignal`) | `export-section.tsx` (25 `createSignal`)                                  | Frontend — per-section UI state                                      | Not persisted                                                                   | Surface mount |
| Clipboard banner shown                   | `ClipboardBannerImpl._shown` (`banner.tsx`)                               | Frontend — promise-based `show()` flow                               | Not persisted                                                                   | Process       |
| **Terminal render state**                | **xterm.js** (AD-6)                                                       | **Out — must never appear in the store**                             | N/A                                                                             | N/A           |

**Notes on what the brief did not list:**

- **Tab pane geometry / viewport** (`_latestViewport`): Tab caches this and delivers
  it on mount start. Not listed in the brief, but it is state owned by the chrome
  with a defined lifetime (tab). Used by the content in the B.5 geometry contract.
  Does not belong in a shared store.
- **Per-tab tooltip** (`_tooltip`): displayed on hover, set by `TerminalContent`
  from cwd. Presentation state, tab-lifetime.
- **Settings search, modified-only filter, section filter**: three local signals in
  `settings.tsx:91-93`. These are UI chrome, not "setting values" — they do not go in
  a store.
- **Export-section state**: 25 `createSignal` calls across 3 sub-components
  (passphrase, confirm, showPasswords, includePrivate, status, busy, paths,
  configFile/status/busy, encFile, portablePass/status/busy, expanded, loaded,
  loading, manifest, error — each duplicating the same status/busy pattern per
  section). The surface is notably state-heavy and would benefit from a local
  `createStore` of its own.
- **Connection form error** (`formError` at `connections.tsx:486`): an edit-form
  transient, not listed in the brief.

---

## 2. The rule that keeps it honest

**Accepted shared state** goes in the store: values the backend has acknowledged,
values multiple surfaces need to agree on, values that survive navigation. **Transient
surface state** stays local: edit drafts, validation messages, search queries, section
filters, busy flags, expanded cards, selection state. "One store" must not become
"every control value in a global object" — that is a different failure with the same
shape.

### Worked examples (store)

- **`tab.placement`**: a Go-authoritative setting that the tab strip reads to decide
  horizontal vs vertical layout. Two consumers (TabManager, the initial read in
  `main.tsx`). Must be shared and accepted.
- **`sidebar.activeViewId`**: the view registry reads it to determine which panel to
  show; the activity bar reads it to apply the `active` CSS class. Two consumers,
  survives sidebar collapse/expand. Belongs in the store.

### Worked examples (local)

- **Settings draft values** (`settings.tsx:85` `draftValues` `createStore`): modified but
  unsaved. Only `SettingsComponent` reads and writes it. Putting it in a global store
  would force every setting mutation to go through the store while only one surface
  cares.
- **Export-section busy flags** (`export-section.tsx:83`, `:118`, `:213`, `:256`, `:260`,
  etc.): per-operation busy state scoped to one button. Putting it in a store would add
  subscription overhead for no shared-read benefit.

### Test to catch a violation

A store contract test with **two real consumers asserting the same field** catches a
violation: register two test surfaces that each read a store field and assert it
is (a) updated after a named transition, and (b) unchanged after a local mutation that
should not touch it. If no second consumer can be named, the field is not shared state.

---

## 3. Persistence rules

### Go settings stay authoritative

Anything a user configures lives in the Go settings document
(`internal/settings/settings.go`), persisted by
`settings.Registry.commitLocked()`. The frontend **mirrors** values through
`ProfileClient.getSnapshot()` and `SettingsObserver` push notifications. It
must never write a second persisted copy.

**When the mirror and Go disagree:** the `monotonicRevisionPolicy`
(`settings-domain.ts:125`) refuses snapshots older than the current mirror
revision. On reconnect, `reconnectRevisionPolicy` (`settings-domain.ts:133`)
overrides revision checks entirely — the backend's counter is in-memory and may
have restarted (ADR-0011 §A.1). This is already tested in
`settings-domain.test.ts`.

**Current gap:** `tab.placement` is read once at startup (`main.tsx:156`) and
watched live through `SettingsObserver` (`main.tsx:198-214`). The read is
idempotent. **No frontend copy of tab.placement is persisted** — correct. The
frontend's local `placement` variable (`main.tsx:154`) is a startup mirror that
is superseeded by the observer callback on every change. No change needed.

### Local UI state: one versioned localStorage record

Two pieces of purely local UI state exist today: **sidebar collapsed** (stored
as `nocx.sidebar.collapsed` at `sidebar.tsx:19,98-100,221-233`) and **active
sidebar view**. Both go into a single versioned localStorage record.

**Record shape:**

```json
{
  "version": 1,
  "sidebar": {
    "collapsed": false,
    "activeViewId": "sessions"
  }
}
```

**Version field:** increments on breaking schema changes. The version check
is optimistic — unrecognised versions are treated as absent (defaults applied,
see below).

**Migration from `nocx.sidebar.collapsed`:**

1. Read the existing `nocx.sidebar.collapsed` key. If `'1'`, set
   `sidebar.collapsed = true` in the new record.
2. Delete the old key.
3. Write the new record.

The migration runs exactly once — after it, the old key is gone and the new
record is authoritative. The migration logic lives in the storage adapter, not
in component code.

**Defined defaults when the record is absent, unparseable, or from a future
version:**

- `sidebar.collapsed`: `true` when there are no panel views (`panelViews.length === 0`),
  else `false` (mirrors the fix for `nocx-rp2j` at `sidebar.tsx:230-234`).
- `sidebar.activeViewId`: the first panel view's id, or empty string.
  (These defaults are already implemented in `mountSidebar` — the persistence
  layer must not change them.)

### Selected theme: recommend a Go setting

Declaring a Go setting costs **one `MustRegisterSelect` call** and zero frontend
changes for the generated settings screen. The cost is a package-level `var`
declaration:

```go
var Theme = MustRegisterSelect(SelectSpec{
    Key:       "ui.theme",
    Section:   "Interface",
    Label:     "Theme",
    DataClass: PublicConfig,
    Default:   "tokyo-night",
    Options:   []SelectOption{...},
})
```

(Existing precedent: `TabPlacement` at `internal/settings/settings.go:470`.)

**Justification:** a theme setting should survive reinstall, sync with settings
export/import, appear in the generated settings screen, and be settable before
the frontend mounts (the bootstrap theme resolver in §5.4 of the design spec
needs a value). A local-only theme picker would miss all of these. The cost of
declaring it as a setting is negligible (< 20 lines in Go).

**One caveat:** if the theme list must be dynamic (themes added as files after
ship), a `SelectSpec` with hardcoded `Options` would need updating. At MVP
(one theme shipping, one in development) this is not a problem. When themes
become dynamic the setting can switch to a declarative key resolved at runtime.

**Decided, and it needs one exception to the mirror rule.** A Go setting arrives
asynchronously, but the bootstrap theme resolver must run _before_ the first frame, or
every launch flashes the default theme. So the local versioned record additionally carries
a **bootstrap cache** of the last accepted theme id: written when the accepted Go value
changes, read synchronously at startup, reconciled against Go when the snapshot lands.

This is the single permitted exception to "the frontend never writes a second persisted
copy", and it survives the rule's intent because the cache is never read as authority,
never written by user action, and always loses to Go. ADR-0013 §8.1 owns the rule; this
table defers to it. The exception applies to `ui.theme` **only** — `tab.placement` and
every other Go setting keep the plain mirror with no local copy.

---

## 4. Fate of unused slices

Four slices in `state/` have no production consumer. All live in
`createAppStore()` (`store.ts:72`) and are only exercised by tests.

### `tabModel` (`tab-model.ts`) — DELETE

- **Lines removed:** 281 (source) + 260 (test) = 541
- **Why:** `TabManager` (`tabs.ts:264`) maintains its own tabs array, active
  tab, and MRU stack as private fields. No production code reads the store's
  tabModel. The framework-neutral transition functions (`addTab`, `activateTab`,
  `closeTab`, etc.) are unused outside the test suite.
- **Impact on future migration:** A future migration that replaces `TabManager`
  with Solid-state tab management should define its own model on top of the
  framework-neutral functions if they're needed — but only when there are two
  consumers to justify sharing through a store. The destination is a store slice,
  not a pre-written model file that nothing reads.

### `profiles` (`profiles-model.ts`) — DELETE

- **Lines removed:** 66 (source) + 84 (test) = 150
- **Why:** `connections.tsx` manages its own profile lists through 3 local
  `createSignal` calls (`:62-64`). The store's `ProfileLists` slice is never
  read.
- **Note:** the `setProfiles` action in the store would need a source if
  adopted, but nothing pushes profile data into it. The real consumer reads
  directly from `ProfileClient`.

### `banner` (`banner-model.ts`) — DELETE

- **Lines removed:** 62 (source) + 50 (test) = 112
- **Why:** `ClipboardBannerImpl` (`banner.tsx`) manages its own `_shown` flag
  imperatively. No production code calls `showBanner`/`dismissBanner` on the
  store.

### `settings` (`settings-model.ts`) — DELETE (the slice only)

- **Lines removed from slice:** 46 (`settings-model.ts` — a re-export barrel)
  - 102 (test) = 148
- **The real code stays:** `settings-domain.ts` (236 lines) + `settings-domain.test.ts`
  (337 lines) = 573 lines. This is framework-neutral logic imported directly by
  `settings.tsx`. The `settings-model.ts` barrel duplicates the re-export job
  already done by `state/index.ts`. Delete the barrel file and the corresponding
  slice in `createAppState` / `AppState`.
- **The settings store in `settings.tsx` is local** — 3 `createStore` calls
  (`:84-87`) that mirror the same shape as the store's settings slice but are
  independently managed. They are the correct pattern for a surface that
  holds drafts and accepted values side by side; they do not need to be "migrated
  to the store." The store's settings slice was written as a future-home that
  never got a tenant.

### Total lines that deletion would remove

| File                                                                                                  | Lines      |
| ----------------------------------------------------------------------------------------------------- | ---------- |
| `tab-model.ts`                                                                                        | 281        |
| `tab-model.test.ts`                                                                                   | 260        |
| `profiles-model.ts`                                                                                   | 66         |
| `profiles-model.test.ts`                                                                              | 84         |
| `banner-model.ts`                                                                                     | 62         |
| `banner-model.test.ts`                                                                                | 50         |
| `settings-model.ts`                                                                                   | 46         |
| `settings-model.test.ts`                                                                              | 102        |
| **Subtotal (model files)**                                                                            | **951**    |
| Store slice fields in `store.ts` (~30 lines of `AppState`, `createInitialState`, and action wrappers) | ~30        |
| Store slice assertions in `store.test.ts`                                                             | ~80        |
| **Total deletable**                                                                                   | **~1,061** |

After deletion the store holds only the `sidebar` slice, plus whatever new
slices `nocx-ycet` adds (tab list, active tab, selected theme — if the
implementing bead adds them). The sidebar slice stays: it has one production
consumer today, and the design spec (§8) expects active sidebar view and
collapsed flag in the store.

**Files that stay:**

- `sidebar-model.ts` (83 lines) + `sidebar-model.test.ts` (86 lines) — one
  production consumer (mountSidebar via sidebar slice).
- `settings-domain.ts` (236 lines) + `settings-domain.test.ts` (337 lines) —
  framework-neutral code, imported directly by `settings.tsx`.
- `state/index.ts` (~54 lines) — slimmed to re-export only the sidebar model
  and settings-domain types. The tab-model and profile-model exports drop.

---

## 5. Migration order

The sequence below is what the implementing bead (`nocx-ycet`) must execute.
Steps 1–6 prepare the store and infrastructure; step 7 moves the first surface.
Cross-boundary moves are flagged.

### Step 1: Clean the store

Delete the four unused slices (tabModel, profiles, banner, settings-model).
Slim `state/index.ts` to re-export only sidebar-model and settings-domain.
Update `store.test.ts` to test only the sidebar slice.

**Verification:** `tsc --noEmit` passes. Only sidebar-related store tests survive.

### Step 2: Single-homed localStorage record

Create a versioned localStorage adapter (`local-ui-state.ts` or similar) with:

- A typed record carrying `sidebar.collapsed` and `sidebar.activeViewId`.
- The one-shot migration from `nocx.sidebar.collapsed` (§3).
- Version check with fallback to defaults on absent/unparseable/future-version records.
- A `subscribe` / `watch` mechanism (or accept that the store effect writes
  back on every change — the current `createEffect` at `sidebar.tsx:98-100`
  already does this; the writeback just targets the new record shape).

**Must not change component behaviour:** the defaults for collapsed/activeViewId
are identical to today's.

### Step 3: Add `setActive` to the TabContent interface

Prerequisite for the `nocx-fttm` fix (see §6). `TabContent` gains:

```ts
setActive(active: boolean): void
```

- `Tab.setActive` (`tabs.ts:96`) calls `this.content.setVisible(active)`.
  After this step it also calls `this.content.setActive(active)`.
- `BaseTabContent` provides a no-op default.
- `TerminalContent` stores the flag and uses it in the keyboard handler
  (replacing `target.classList.contains('active')` at `terminal-content.ts:279`).
- **This is a cross-boundary move** — it touches `tabs.ts` (chrome) and
  `terminal-content.ts` (terminal, AD-6). The interface addition is in
  `tab-content.ts` (the seam). Acceptable because the interface _is_ the
  boundary.

### Step 4: Inject the store into the composition root

Move `createAppStore()` from `mountSidebar` (`sidebar.tsx:218`) into `main.tsx`.
Pass `state` and `actions` as parameters to `mountSidebar` instead of letting it
create its own store.

This is the architectural linchpin: once the store is in the root, other consumers
can share it.

**Before:**

```ts
// sidebar.tsx
export function mountSidebar(...) {
  const [state, actions] = createAppStore()  // private store
  ...
}
```

**After:**

```ts
// main.tsx
const [state, actions] = createAppStore()
mountSidebar(bar, panel, panelViews, tabActions, storage, state, actions)
```

### Step 5: Make the sidebar use the injected localStorage adapter

Replace the raw `localStorage.setItem` calls in `sidebar.tsx:98-100` with the
versioned adapter from Step 2. The adapter's default-return logic replaces the
inline `getItem` checks in `mountSidebar`.

**No behavioural change** — the same defaults produce the same initial state.

### Step 6: Decide tab placement mirror

`tab.placement` is currently read at startup (`main.tsx:156`) and watched through
`SettingsObserver`. If the store should carry it as a shared state, add a store
field and a `mirrorTabPlacement` transition that reads the Go snapshot. If not
— the current pattern of a local variable plus observer callback is sufficient
for a single consumer (TabManager.replaceStrip) — leave it as is.

**Recommendation:** add it to the store. The design spec (§8) names tab placement
as shared state, and the observer callback in `main.tsx:200-213` is already the
right place to push an update to the store. The cost is one store field, one
action, and the observer sets it. The benefit is one uniform answer when
`tab-strip.tsx` later renders from the store.

### Step 7: Move the tab model into the store (the big change)

This is the main payload of `nocx-ycet`. It replaces `TabManager`'s private
fields (`tabs`, `activeTab`, `recentTabIds`) with store-backed state.

**What moves:**

- Tab list → `state.tabModel.tabs` (new store slice)
- Active tab id → `state.tabModel.activeTabId`
- MRU order → `state.tabModel.recentTabIds`
- Per-tab display state → `state.tabModel.tabs[i].{title, hasActivity, agentStatus}`

**What stays in Tab/TabManager:**

- Tab chrome lifecycle (pane creation, ResizeObserver, setActive/setVisible on
  content — these are imperative operations on DOM elements, not state)
- `_tooltip` (presentation-only, displayed on hover, no shared consumer)
- `_disposed`, `_mountStarted` (internal lifecycle)

**The hard part:** `TabManager.addTab` currently creates a `Tab` object and
pushes it into `this.tabs[]`. After the migration, `addTab` also dispatches a
store action. The `Tab` object must stay as the chrome wrapper (it owns the pane
element, the ResizeObserver, the content instance). The store holds only the
model data (`TabData`). The mapping from store model → `Tab` instance is the
challenge — `Tab` instances are imperative objects, not reactive. One approach:
key the tabs array by `tab.id` and look up the instance in a `Map<number, Tab>`.

**Cross-boundary check:** per-tab display state (`title`, `hasActivity`,
`agentStatus`) today lives on the `Tab` class and is read by `TabStrip` through
the `TabView` interface. Moving these to the store means `TabStrip` reads from
the store instead of the `Tab` object. This is a clean change — the store is
shared, the strip is Solid, and reading reactive state instead of imperative
getters is the point. No terminal boundary crossed.

### Explicitly out (AD-6 — must not move)

- **Terminal render state** (xterm.js grid, scrollback, selection, per-cell data).
  AD-6 is binding: the VT frontend owns this. It must never appear in the store.
- **Terminal pane geometry** (`_latestViewport` on `Tab`). Crosses the AD-6
  seam through `viewportChanged()` — a typed imperative method, not reactive
  state.
- **Shell/cwd/OSC events.** Consumed frontend-side only, never expressed as
  store state (ADR-0012 Data Flow diagram).

---

## 6. nocx-fttm: keyboard handling gated on CSS class

**Bug:** `terminal-content.ts:279` reads `target.classList.contains('active')`
to decide whether to handle keyboard input. The authoritative value is
`Tab._active` (`tabs.ts:38`), which was used to set the class one line before
(`tabs.ts:100` `this.content.setVisible(active)`, which toggles `'active'` on
the pane).

**The authoritative slice:** `TabContent.setActive(active: boolean)` — the
interface method added in Step 3 above. The store is not involved; this is a
seam interface, not a reactive channel. The `Tab._active` field is the single
source of truth at the chrome level, and it must be propagated to content
through the seam.

**What currently reads DOM for state:** the `_globalKeydown` handler at
`terminal-content.ts:279`:

```ts
if (!target.isConnected || !target.classList.contains('active')) return
```

**What it should read instead:**

```ts
// terminal-content.ts — store its own active flag
private _active = false

setActive(active: boolean): void {
  this._active = active
}

// in _globalKeydown:
if (!target.isConnected || !this._active) return
```

The `active` CSS class remains on the pane for presentation (styling the active
pane). The keyboard handler reads the explicit flag, not the CSS class. A unit
test asserts the handler is inert for a non-active `TerminalContent` instance.
