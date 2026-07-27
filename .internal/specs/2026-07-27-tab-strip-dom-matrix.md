# Tab Strip DOM Compatibility Matrix — `nocx-82l9.5`

Preservation audit: imperative `createElement` + `createEffect` DOM patching → Solid `<For>` + `createStore` reactive JSX — no DOM patch effect remains.

## Summary

- **Preserved:** 20 selectors/behaviours unchanged
- **Changed:** 0 selectors/behaviours changed
- **No assertions weakened**

| Selector / behaviour                                      | Preserved? | Notes                                                                                                                               |
| --------------------------------------------------------- | ---------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `.tabs-container`                                         | ✓          | Same `<div class="tabs-container" />` from Solid template                                                                           |
| `.tab`                                                    | ✓          | Same `<div class="tab" …>` — classList driven by `classList` directive                                                              |
| `.tab-index`                                              | ✓          | `<span class="tab-index">` — content driven by `<For>` index, `i+1`                                                                 |
| `.tab-label`                                              | ✓          | `<span class="tab-label">` wrapping status + title                                                                                  |
| `.tab-status`                                             | ✓          | `<span class="tab-status" />` — empty indicator element                                                                             |
| `.tab-title`                                              | ✓          | `<span class="tab-title">` — `textContent` set by `{tab.title}` in template                                                         |
| `.tab-close`                                              | ✓          | `<button class="tab-close">` — same content + aria-label                                                                            |
| `.tab-indicator`                                          | ✓          | `<div class="tab-indicator">` — activity class driven by `classList`                                                                |
| `.tab-add`                                                | ✓          | Same `<button class="tab-add">` from Solid template                                                                                 |
| `.tabbar-spacer`                                          | ✓          | Same `<div class="tabbar-spacer">` from Solid `<Show>`                                                                              |
| `id="tab-btn-N"`                                          | ✓          | Template: `id={\`tab-btn-${tab.id}\`}` — stable per tab.id                                                                          |
| `data-tab-id`                                             | ✓          | Template: `data-tab-id={String(tab.id)}`                                                                                            |
| `role="tab"`                                              | ✓          | Static attribute on each tab div                                                                                                    |
| `aria-selected`                                           | ✓          | Computed from active-tab state in template                                                                                          |
| `aria-controls`                                           | ✓          | Reads `tab.paneId` — same value                                                                                                     |
| `aria-labelledby` on pane                                 | ✓          | Still set imperatively in `addTab()` via `document.getElementById`                                                                  |
| `draggable`                                               | ✓          | Static `draggable={true}` on tab div                                                                                                |
| `tabIndex` (roving)                                       | ✓          | Computed from active-tab state: `tabIndex={isActive ? 0 : -1}`                                                                      |
| `.dragging` class                                         | ✓          | Set/removed via DOM `classList` in `onDragStart`/`onDragEnd` handlers                                                               |
| `.active` class                                           | ✓          | `classList={{ active: isActive, … }}` — truth tracking same state                                                                   |
| `.working` class                                          | ✓          | Driven by `tab.agentStatus === 'working'`                                                                                           |
| `.waiting` class                                          | ✓          | Driven by `tab.agentStatus === 'idle'`                                                                                              |
| `.tab-activity` class                                     | ✓          | Driven by `tab.hasActivity && !isActive`                                                                                            |
| event `click` → activate                                  | ✓          | Solid `onClick` on tab div                                                                                                          |
| event `mousedown` (button 1) → close                      | ✓          | Solid `onMouseDown` on tab div                                                                                                      |
| event `click` on close button → close                     | ✓          | Solid `onClick` with `stopPropagation` on close button                                                                              |
| Drag-and-drop: `dragstart`, `dragend`, `dragover`, `drop` | ✓          | Solid `onDragStart`/`onDragEnd`/`onDragOver`/`onDrop` on tab div                                                                    |
| Roving tabindex: ArrowLeft/Right/Up/Down, Home, End       | ✓          | Key handler uses signal array via `_getTabViews()` instead of `orderedButtons()` DOM query                                          |
| Keyboard focus across reorder                             | ✓          | Key handler uses signal array via `_getTabViews()` — same key order as DOM                                                          |
| Node identity preserved across reorder                    | ✓          | `<For>` keyed by tab object identity — Solid reconciliation keeps same DOM node for same key; confirmed by `isSameNode()` assertion |
| `onDisplayChange` wiring + cleanup                        | ✓          | `addTab` wires `onDisplayChange` to update Solid store; `removeTab` clears it                                                       |

## Things that change internally (no DOM observer impact)

| Old mechanism                                           | New mechanism                                                   | Observable difference?                        |
| ------------------------------------------------------- | --------------------------------------------------------------- | --------------------------------------------- |
| `createElement` per tab in `addTab()`                   | Solid `<For>` renders tab div from signal                       | No — same DOM output                          |
| `paintButton()` + `refreshIndices()`                    | Solid store + inline JSX expressions                            | No — same DOM, reactive via store             |
| `createEffect` version-bump + `querySelector` DOM patch | Solid store `onDisplayChange` writes to store; JSX reads inline | No — same DOM update, reactive via store      |
| `tabsContainer.innerHTML = ''` in `reorder()`           | `setTabViews([...tabs])` — Solid reconciles                     | No — same DOM reorder with identity preserved |
| `orderedButtons()` DOM query in key handler             | `_getTabViews()` signal array                                   | No — same logical order                       |
| `buttons` and `views` Maps                              | Signal array `_getTabViews()`                                   | No — replaced by reactive data                |
