---
name: nocx Sandbox
status: final
updated: 2026-10-05
sources:
  - ../../../docs/superpowers/specs/2026-10-05-sandbox-main-rewrite-design.md
---

# nocx Sandbox — Experience Spine

## Foundation

Desktop application shell with a native desktop runtime and web-rendered frontend. Use the existing nocx UI kit and singleton Settings surface; `{DESIGN.md}` owns visual styling and theme inheritance. The sandbox is a filesystem boundary for local shell processes, not a replacement terminal, agent-specific mode, container, network sandbox, or full process-isolation claim.

The only user-facing modes are **Off** and **Enforce**. Learn and a standalone Statistics surface are explicitly out of scope. A saved profile is policy input for future launches, never authority over a process already running.

## Information Architecture

| Surface                     | Reached from                                                                            | Purpose                                                                                                                        |
| --------------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Settings → Sandbox          | Settings rail; activity-bar shield; exact `/sandbox` command in a shell-routed composer | Edit defaults, inspect current launch, preview and explicitly replace the selected pane's process, view contextual diagnostics |
| Launch preview/confirmation | Apply, Relaunch, or Remove for a pinned local pane                                      | Review effective immutable launch policy, limitations, and the consequence before replacement                                  |
| Contextual diagnostics      | Selected Enforce launch within Settings → Sandbox                                       | Inspect bounded observations and make a future-profile suggestion or dismiss an event                                          |
| Pane restore state          | Restored pane when app starts/reconnects                                                | Reclaim the exact live session/grant, or represent dead/unknown without silently opening a shell                               |

Settings remains one singleton tab/surface (`SINGLETON_SETTINGS`). The shield and `/sandbox` open that same Settings surface and focus its Sandbox page; they do not create a second sandbox panel or authority path. The rail offers one Sandbox page: standard profile by default, named workspace profile selection where applicable, and current pane details. On unsupported/remote/nonlocal context, retain honest availability detail and disable Enforce actions that cannot be correctly executed; standard and workspace defaults remain editable for future supported local launches. Never imply remote enforcement.

## Voice and Tone

State what is known, what will change, and what is outside the guarantee. Keep copy compact and non-alarmist. Never use kernel jargon as the only explanation or describe prediction as a proven denial.

| Prefer                                                                                            | Avoid                                                  |
| ------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| “Enforce is active for this shell.”                                                               | “Sandbox enabled” when only a default changed          |
| “This is an observed file-access attempt; the operating system’s denial result is not confirmed.” | “Blocked” when the observer only saw an attempted open |
| “The shell process is still running. Reconnect to this same session.”                             | “Restore failed — starting a new shell.”               |
| “The process state is unknown. Retry the check; no shell was opened.”                             | “Off” when helper state cannot be queried              |

## Component Patterns

| Pattern                     | Behavior                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Mode/status summary         | Shows current selected pane, local/unsupported status, live mode (`Off`, `Enforce`, `Unknown`, `Unavailable`), immutable launch/grant identity summary, and whether current defaults differ from the launch snapshot. A feature toggle only governs whether new Enforce launches may be prepared; it never removes protection from a live process.                                                                                                                                           |
| Standard/workspace selector | Selects one editing target. Standard is global baseline. A named workspace may inherit (`override: null`) or own a sparse override; first edit creates a snapshot. Default/unnamed workspace uses standard. Source and revision remain visible.                                                                                                                                                                                                                                              |
| RO/RW lists                 | Separate, explicitly labeled read-only and read/write roots. Add/edit/remove changes a local pending profile draft; Save commits with expected revision (CAS). Display canonical/effective roots separately from the editable user rules and label mandatory/system/runtime provenance. No precedence-by-order. Explain that a root outside the workspace may still be allowed if included in the effective preview.                                                                         |
| Policy preview              | Opens an existing `Dialog` after selecting a target mode and operation. Pinned to pane ID, expected source session/incarnation, profile revisions, and exact preparation identity. Lists effective roots, runtime HOME/TMP, platform and helper capability, limitations, and that the source shell will be ended only after successful commit. No command text or environment values are exposed.                                                                                            |
| Confirmation                | Final explicit action names the destination mode (`Apply Enforce`, `Relaunch Enforce`, or `Remove protection`). Enforce requires preview and confirmation. Remove separately warns it launches an unrestricted shell and is the only way to intentionally replace an Enforce process with Off. Confirm is bound to preview; changed context or policy invalidates it.                                                                                                                        |
| Diagnostic row              | `RecordRow`/`CollectionView` style row presents event identity, operation, executable, status qualifier, count and source/precision. Paths are fetched only while the explicit Settings detail is open. Actions are `Dismiss`, `Suggest read-only`, and `Suggest read/write`; a suggestion edits future defaults only and never retries a command or changes the running grant.                                                                                                              |
| Shield contextual action    | Typed top-zone contextual action appears after Files in the activity bar, independent of fake view registration. Outline shield means known Off, active shield means verified live Enforce, distinct warning/unknown treatment means state unavailable. Disabled for no pane/remote/unsupported pane with a reason. Activation opens singleton Settings → Sandbox pinned to the same pane/workspace; it never mutates policy on click. Existing toolbar roving keyboard controls include it. |
| `/sandbox` interception     | A typed internal-command hook returns `pass`, `consumed`, or `refused` before secret planning, history, ledger, submit planning, target submission, and PTY input. Only exact trimmed `/sandbox` with active input target `routesToShell` opens Sandbox. `pass` continues ordinary submit; `/sandbox arg`, embedded/multiline variants, and agent/chat target remain ordinary input. `consumed` clears only the still-identical draft; `refused` preserves draft.                            |
| Session replacement/rebind  | Replacement commits through backend operation identity and then rebinds session on the same pane. Pane/tab, existing scrollback/history divider, and editor/draft remain pane-owned. Session-scoped callbacks and runtime projections reset; old terminal live grid/output is not copied into the new process. While replacement is in flight input is unavailable, draft preserved. Pre-commit failure leaves source session untouched.                                                     |

## State Patterns

| State                           | Surface                | Treatment                                                                                                                                                                                                                    |
| ------------------------------- | ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Loading                         | Sandbox page           | Preserve the selected pane context and show an explicit loading state; no inferred Off, editable action, or green shield before authoritative status arrives.                                                                |
| Ready local Off                 | Summary                | Say Off for the current launch; offer Apply Enforce after capability/profile availability checks.                                                                                                                            |
| Ready Enforce                   | Summary                | Show active grant/launch and snapshot-vs-defaults difference. Apply updated rules only by an explicitly confirmed replacement. Remove is available only as a separately confirmed replacement.                               |
| Unsupported / remote            | Summary and action     | Explain platform/backend/context limitation. Disable Enforce/Remove actions that cannot be correctly executed; do not fallback to ordinary shell while claiming success.                                                     |
| Unknown / helper unavailable    | Summary and restore    | Use unknown/pending wording; retry status lookup. Do not display Off, offer an unguarded ordinary session open, or issue an unqualified success toast.                                                                       |
| Dead process                    | Restored pane          | Present ended process with preserved history and grant inspection. Allow explicit preview-and-confirmed new launch with a fresh grant. Never revive the ended grant or silently spawn a shell.                               |
| Live process                    | Restore                | Reclaim exact session ID, helper generation and immutable grant. Do not spawn or issue a new grant.                                                                                                                          |
| CAS stale revision              | Profile editor         | Keep user's edits, explain that another writer changed the profile, reload authoritative values/revision, and let the user reconcile then explicitly retry save. Never silently overwrite.                                   |
| Validation/policy conflict      | Profile editor/preview | Identify the relevant field/index and explain conflict (invalid/canonical alias, RO/RW conflict, reserved control/config path, bound exceeded). Keep draft and block preview/launch; do not auto-correct by widening rights. |
| Preview expired/context changed | Preview                | Invalidate confirmation and show why the pane/session/workspace/revision changed. Require a new preview and reconfirmation; never reprepare invisibly under the old confirmation.                                            |
| Pre-commit launch failure       | Operation              | Report that the original shell remains active; keep draft/history. Offer retry by creating a fresh preview, not replaying the same consumed launch ticket.                                                                   |
| Post-commit/lost response       | Operation              | Query operation identity/head. Show committed replacement or pending close truthfully; never retry spawn with a new operation ID automatically.                                                                              |
| Diagnostics unavailable         | Diagnostics            | Keep Enforce status independent. Label observer unavailable/failed; show no fabricated empty inbox and do not imply absence of attempts.                                                                                     |
| Diagnostics overflow            | Diagnostics            | Show dropped count and bounded-observation notice. No command stall or user-visible promise of complete event history.                                                                                                       |

## Interaction Primitives

- Standard Settings navigation, search/rail keyboard model and component keyboard patterns remain unchanged.
- Activity-bar shield is in the same roving-focus toolbar; arrow keys move through entries, Home/End reach edges, Enter/Space activate. Accessible name includes state, e.g. `Sandbox status: Enforce — open Settings, Sandbox for {pane}`. Tooltip explains disabled reason.
- In the Settings page, Tab follows document order. List actions expose text labels or accessible names; no hover-only affordance. Root entry forms use explicit Save/Cancel, with Enter behavior only where the kit's form convention defines it; multiline path input does not bind bare Enter to save.
- Preview dialog: focus enters dialog at its heading/summary or first actionable control per existing Dialog behavior; Tab stays inside; Escape and Cancel close without mutation and return focus to the opener. No nested confirmation dialog: preview includes the final explicit action and warning.
- While profile CAS save is pending, disable duplicate Save but preserve editable data. On validation/CAS error, keep dialog/page and input intact, announce the error near the affected control and with the existing toast/status pattern where appropriate.
- Preview creation does not change any active process. Cancel, Escape, dismissing Settings, or changing selected context before confirmation discards/invalidates only the unconfirmed preview; it does not create an Off or Enforce launch.
- Confirmation becomes a server-owned operation after explicit confirmation. Closing Settings/window after confirmation does not imply rollback. Provide operation status by operation ID; no duplicate launch from repeated click or lost response.
- Replacement lock prevents simultaneous mutations and disables input to the replaced shell while preserving draft. Before commit, cancel/refusal leaves source active and restores input. After commit, rebind the same pane, restore draft and focus the composer when that pane is active; keep focus on Settings when initiated there.
- A suggestion is visibly labeled “future launches only”; Save uses CAS and confirms the resulting new revision. A separate Relaunch action is required to apply it to a process and opens a fresh preview.
- `/sandbox` internal hook is checked before `beforeSubmit` and any pre-submit side effects. Async hook and `beforeSubmit` share an in-flight guard so repeated Enter cannot open duplicate dialogs or submit twice. Capture exact document/target identity. While pending, swallow repeated Enter. On consumed, clear only if current draft still exactly matches the captured command; do not erase text typed after interception. On refused/cancel/error, retain draft. Restore focus to originating editor if still mounted/active; otherwise focus Sandbox Settings. A stale target/pane refuses rather than opening for a different pane.
- `/sandbox` in non-shell input target is not intercepted. The command is not inserted into shell PTY, submission history, command ledger, or attempt record. Plain terminal mode without CommandEditor does not sniff terminal bytes; shield remains available.

## Accessibility Floor

- Meet the existing UI kit's accessible-name, role, focus, and keyboard contracts. Preserve visible focus rings from the active theme.
- Mode is textually named everywhere; icon/color never carries authority meaning alone. State transitions such as Unknown → Enforce are announced through an appropriate live status without disclosing filesystem paths unsolicited.
- Every root has an accessible label including class and source; errors attach to the relevant field. Full paths remain available to assistive technology even when visually truncated.
- Dialog follows existing focus trap/return behavior; Escape and Cancel have identical non-mutating preview cancellation semantics.
- Toolbar action is keyboard-reachable and its accessible name updates with backend truth. Disabled states include an accessible reason, not just a disabled glyph.
- Avoid announcing paths through unsolicited notifications, global toast, logs, or generic errors. Path details are returned only in an explicit authenticated Settings request.

## Security Boundary Copy

Explain before confirmation and in Settings help:

- Enforce limits mediated filesystem operations of the local shell and its descendants to effective read-only/read-write roots. Read-only permits reading/executing but not writes; read/write permits workspace operations.
- This does not provide network isolation, defend against kernel exploits or arbitrary same-user host processes, hide all metadata, revoke already-open file descriptors, neutralize pre-existing hardlinks, or guarantee every metadata/ioctl operation is denied. SSH/remote panes and Windows are unsupported for Enforce.
- GUI Files, Git and completion operations are trusted user actions outside the shell boundary. Agent-originated nocx coordinator tools are unavailable while the source session is sandboxed. Nocx-owned local pathname control endpoints are blocked by the native boundary; this is not a claim to block all host IPC.
- Isolated HOME is temporary and credentials are not guaranteed scrubbed from every environment variable. Save durable files in the workspace or an explicit RW root.

## Key Flows

### Flow 1 — Apply Enforce from a live local shell (Nadia, changing trust after a toolchain install)

1. Nadia is in a local terminal pane and opens Settings → Sandbox from the activity-bar shield. Settings pins the exact pane and shows its current Off status and profile source.
2. She selects a named workspace profile, sees that it inherits standard, then edits its RO/RW roots. Save performs CAS against displayed revision. A stale conflict preserves her draft and lets her reload/reconcile.
3. Nadia selects **Apply Enforce**. The backend prepares a preview bound to pane ID, expected source session, workspace membership, and standard/workspace revisions. The preview separates her grants from mandatory workspace/runtime/system roots and states the platform limitations and that the source shell will be ended only after successful replacement.
4. She reviews the complete effective roots and confirms **Apply Enforce**. The backend commits one durable operation and launches the candidate under the prepared native policy. The pane stays bound to the source until commit; no command is replayed.
5. **Climax:** the same pane/tab now shows Enforce for the exact new immutable launch, with a new empty live terminal while the prior scrollback and draft remain pane-owned above the session divider. Nadia sees the grant snapshot and knows subsequent profile edits require another relaunch.

Failure: preparation or launch fails pre-commit → source shell, draft, history and focus remain intact, an actionable error is shown, and Nadia can preview again. Unknown helper state → no regular shell fallback.

### Flow 2 — `/sandbox` from the prompt without recording a shell command (Leo, exact command)

1. Leo types `/sandbox` into the command editor while the active input target routes to the shell and presses Enter.
2. The editor's internal-command hook runs before secret resolution/planning, history, ledger, and PTY submit. It captures the exact draft and pane identity; the in-flight guard swallows a second Enter.
3. The hook opens the singleton Settings surface on Sandbox pinned to Leo's pane. If text changes before resolution, it will not clear the changed draft.
4. **Climax:** the command is consumed as UI navigation: `/sandbox` is absent from PTY input, command history, ledger and execution attempts. Settings shows the pane's backend-derived state, not a guess based on selected profile.

Cancel/refusal/error leaves the exact draft in the editor and returns focus there if still available. `/sandbox arg`, embedded/multiline variants, and exact `/sandbox` while the agent target is active pass through normal input handling.

### Flow 3 — Restore a pane after coordinator/window loss (Ari, helper shell still alive)

1. Ari reopens the app. PaneManager loads durable pane/launch heads and asks the exact helper generation for live session inventory and sandbox launch facts.
2. If session ID, helper generation, grant digest and policy version match, the existing shell is reclaimed. No new policy is built and no shell is spawned.
3. If exact inventory proves the process ended, the pane shows dead state with history and immutable grant inspection; a new run requires an explicit preview/confirmation and a new launch grant.
4. If the helper/inventory is unavailable or generation unknown, the pane stays Unknown/Pending and offers recheck. It does not become Off or open a regular shell.
5. **Climax:** Ari returns either to the same still-running process under the same grant or to an honest dead/unknown state—never an unrestricted substitute that looks like a restored terminal.

## Implementation Contracts (handoff)

These contracts describe the required frontend behavior; they do not claim it is implemented.

1. **Settings seam:** add one feature-owned Sandbox page/component in the existing `SettingsComponent`/`SettingsContent` and singleton registry. Use a feature client speaking authenticated sandbox RPC, separate from scalar settings keys. Standard profile and workspace sparse override are typed documents with revisions; every write carries expected revision and handles CAS conflicts explicitly.
2. **Pane-pinned context:** Settings entry points carry only a selected pane ID/context hint. Backend resolves current pane, local origin, workspace membership and source session. Preview request identifies expected source session and revisions; backend returns opaque preview identity and bounded display snapshot. Do not accept UI-supplied workspace as authority.
3. **Preview/replace:** UI calls `sandbox.preview`, displays immutable prepared effective policy, then `sandbox.replace` with preview identity plus stable operation ID. Mode/roots/session/revisions are not re-sent as mutable replace arguments. `sandbox.operation.get` resolves lost responses; never create a new operation automatically.
4. **Grant inspection:** `sandbox.grant.get` is read-only and only for a launch reachable from selected pane. Display immutable mode, grant ID/digest/version, policy snapshot/provenance, launch state and defaults divergence. Do not turn a grant into a reusable launch token.
5. **Diagnostics:** only contextual selected-launch list; list exposes bounded pages/cursor/revision and aggregate dropped/source/precision fields. Fetch paths only on explicit open/detail. `allowRO`/`allowRW` creates a profile suggestion through revision-checked update; dismiss only resolves event. No retry or live widening.
6. **Shield wiring:** extend sidebar's typed contextual-action seam after Files without creating a view or fake navigation entry; connect in `main.tsx` to backend-derived active-pane status and singleton Settings opener. Use current context, disabled reason for remote/unsupported/no registered pane. Status survives disabled new-Enforce feature if an existing grant is live.
7. **Typed internal command:** add `EditorActions` hook with discriminated `pass | consumed | refused` result, invoked before `beforeSubmit`/`planSubmit`, secret planning, history/ledger and `submit`. Only exact `doc.trim() === '/sandbox'` and active `routesToShell` target is eligible. Use a shared in-flight guard spanning hook and prior async submit hook; validate captured draft and pane before clearing/opening. No fake `SubmitPlan`.
8. **Restore/rebind:** replace null/error-as-open-fresh logic with discriminated `live | dead | unknown | unsupported/error` sandbox recovery. A live matching launch reuses session ID/grant. Dead, unknown, mismatch, or unrecognized generation never triggers ordinary shell open. After committed replace call a dedicated bind path built on existing `_bindSession` behavior; keep pane-owned scrollback/editor/draft, reset session-scoped listeners/projections and do not copy old live grid.
9. **Focus/cancellation:** dialogs use the existing kit Dialog and return focus to opener after Cancel/Escape. Internal-command cancellation/refusal preserves draft; successful consume clears only an unchanged captured draft. In-flight guards prevent double confirmation, double open and duplicate spawn. Async callbacks verify current pane/session identity.
10. **Privacy and errors:** use structured error codes/field-index and safe copy, not filesystem paths in global logs/toasts/events. Path display is an authenticated explicit detail response. Show exact known operation state; never hide unknown behind Off or optimistic success.

## Frontend Seam Map (observed source, read-only)

- `frontend/src/settings.tsx` renders grouped Settings pages and UI kit primitives; `settings-content.ts` wraps it as `SettingsContent`; `main.tsx` registers the `SINGLETON_SETTINGS` surface and owns `openSettingsPane()`.
- `frontend/src/sidebar.tsx` currently distinguishes top-zone views from bottom global actions (`SidebarAction`); the approved shield requires a typed top contextual-action extension after Files, not insertion as a fake view or bottom global action.
- `frontend/src/editor.ts` has `EditorActions.beforeSubmit`, `_submitInFlight`, `submit()`, and keydown arbitration; `terminal-content.ts` wires pre-submit planning and then writes target history and starts shell orchestration. The internal hook must precede this chain.
- `frontend/src/terminal-content.ts` has `openRequestedSession()`, `adoptLiveSession()`, and `_bindSession()`/`reconnect()`; its current adoption failure returns null and ordinary open follows. Sandbox recovery must be a distinct fail-closed outcome, while replacement can reuse the session-binding path.
- `frontend/src/panes.ts` primes `sessions.live`, consumes `adoptionFor()` once, and currently treats missing inventory as fresh-session eligible. Sandbox heads require an explicit live/dead/unknown authority result before this fallback.
- `frontend/src/ui/README.md` is the source for available kit primitives; reuse the existing Dialog, RecordRow, CollectionView, StatusCard, EditableRowList, IconButton, Select, and Toast patterns named in the approved product plan.

The backend implementation must update these contracts to generated RPC bindings as needed. This document does not claim any frontend code or integration has been changed or verified.
