// PaneContext — the strip that names a pane before its transcript starts
// (decision 2026-09-15-terminal-screen-mockup-decision.md §1 item 3):
// `folder ~/repos/nocx on ⎇ main` in `01`, a local/remote pane identity in
// `03`, and the foreground-program/input-owner readout in `02`. Mounted
// once per terminal pane, immediately above `.scrollback-layout` — chrome,
// never a transcript row, so a failed block's rail and a selection tint
// never reach it and it never scrolls with the blocks.
//
// Vanilla-emitted, like PromptContext and Meta: the terminal screen that
// uses it is imperative DOM (ADR-0012), and a pane builds one once at
// mount. No Solid version exists until a Solid surface needs one.
//
// Identity `ui-pane-context`; variance on data-kind, data-split and
// data-active. A surface places it and never repaints it (ui/README).
//
// The where-line is PromptContext's `chrome` presentation
// (`ui/prompt-context.ts`, landed round 2 by the shared-kit task): this
// component supplies the leading identity icon (folder/server) and the
// program/trailing slots, PromptContext owns the ONE formatted
// path/host/branch line so a block, the composer and this chrome strip
// cannot start naming "where" three ways (AD-8). Round 1 rendered a muted
// path/branch locally here, before that presentation existed; integration
// replaced it with the real primitive below — see prompt-context.css's own
// `[data-presentation='chrome']` rule for why the colours differ from a
// block's accent prompt line without a second component.

import { KeyboardIcon, ServerIcon, FolderIcon, iconElement } from './icons'
import { createPromptContext, type PromptContextFacts } from './prompt-context'

// Not exported: nothing outside this module names the kind independently
// of `PaneContextFacts.kind` — an exported alias with no external caller is
// exactly what the dead-exports ratchet exists to catch.
type PaneContextKind = 'local' | 'remote' | 'program'

export interface PaneContextFacts {
  /** Which identity this strip names right now. `program` is the
   *  alternate-screen/foreground-program presentation (`02`); `local` and
   *  `remote` are the ordinary and SSH-child presentations (`01`, `03`). */
  kind: PaneContextKind
  /** The short path (`cwdLabel`'s own answer) — shown for every kind.
   *  Never fetched here: the caller (TerminalContent) already derives it
   *  for the composer and a block's own prompt line, and a second
   *  derivation is exactly what nocx-9bpeq.16 exists to prevent. */
  path?: string
  /** The branch known right now — `local` only; a remote pane's shell is
   *  not walked for one (spec §3, `_syncWhereSources`'s own `isLocal`
   *  gate). Absent renders no branch at all, never a guessed `main`. */
  branch?: string
  /** `user@host`, or the bare host — `remote`'s own identity label,
   *  TerminalContent's existing `hostLabel()`. */
  host?: string
  /** The foreground program's own name (`02`'s tab title, `nvim`) —
   *  `program` only. */
  program?: string
  /** Who owns the keyboard right now, for `02`'s "Keyboard → nvim"
   *  readout — `program` only. Absent while nothing has taken it. */
  keyboardTarget?: string
  /** 44px split identity presentation instead of the single-pane 40px
   *  (spec: "Height 40 px for a single pane, 44 px for split identity
   *  presentation"). */
  split?: boolean
  /** This pane is the active one in a split — a DIFFERENT fact from
   *  "this pane's TAB is active" (spec §3: "Keep active-pane indication
   *  distinct from active-tab indication"). Undefined outside a split,
   *  where there is only ever one pane to be active among. */
  active?: boolean
}

export interface PaneContextActions {
  /** `02`'s "Session actions" control — the existing native-input escape
   *  and whatever else a program-owned pane still lets a person reach.
   *  Absent for `local`/`remote`, which have no such menu. */
  onSessionActions?: () => void
}

function textPart(cls: string, text: string): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = cls
  el.textContent = text
  return el
}

/** The where-line, in PaneContext's own chrome register: PromptContext's
 *  `chrome` presentation reads path/branch muted instead of accent, and
 *  `host` reads muted already in every presentation — so `remote`'s
 *  `user@host:path` and `local`'s `path on branch` are both exactly the
 *  same primitive the composer and a block use, never a second format
 *  invented here. */
function whereLine(facts: PaneContextFacts): HTMLElement {
  const promptFacts: PromptContextFacts = { path: facts.path ?? '~' }
  if (facts.kind === 'remote' && facts.host) promptFacts.host = facts.host
  if (facts.kind !== 'remote' && facts.branch) promptFacts.branch = facts.branch
  return createPromptContext(promptFacts, { presentation: 'chrome' })
}

/** `local`/`remote`: identity icon, then the PromptContext where-line. */
function buildIdentity(facts: PaneContextFacts): HTMLElement[] {
  const icon = iconElement(facts.kind === 'remote' ? ServerIcon : FolderIcon)
  icon.classList.add('ui-pane-context__icon')
  return [icon as unknown as HTMLElement, whereLine(facts)]
}

/** `02`: the foreground program's own name on the leading edge, its path
 *  through the same where-line PromptContext draws elsewhere, and —
 *  trailing — who owns the keyboard right now plus the Session actions
 *  escape. Neither TUI internals nor a second status line (spec §5): this
 *  reads facts TerminalContent already has, and draws no program state of
 *  its own. */
function buildProgram(facts: PaneContextFacts, actions: PaneContextActions): HTMLElement[] {
  const children: HTMLElement[] = []
  children.push(textPart('ui-pane-context__program', facts.program ?? ''))
  if (facts.path) children.push(whereLine(facts))
  const trailing = document.createElement('span')
  trailing.className = 'ui-pane-context__trailing'
  if (facts.keyboardTarget) {
    const kbIcon = iconElement(KeyboardIcon)
    kbIcon.classList.add('ui-pane-context__icon')
    trailing.append(kbIcon)
    trailing.append(textPart('ui-pane-context__keyboard', `Keyboard → ${facts.keyboardTarget}`))
  }
  if (actions.onSessionActions) {
    const btn = document.createElement('button')
    btn.type = 'button'
    btn.className = 'ui-button'
    btn.dataset.variant = 'ghost'
    btn.dataset.size = 'sm'
    btn.textContent = 'Session actions'
    btn.addEventListener('click', () => actions.onSessionActions?.())
    trailing.append(btn)
  }
  children.push(trailing)
  return children
}

function fill(el: HTMLElement, facts: PaneContextFacts, actions: PaneContextActions): void {
  el.dataset.kind = facts.kind
  if (facts.split) el.dataset.split = 'true'
  else el.removeAttribute('data-split')
  if (facts.active === true) el.dataset.active = 'true'
  else el.removeAttribute('data-active')
  const children = facts.kind === 'program' ? buildProgram(facts, actions) : buildIdentity(facts)
  el.replaceChildren(...children)
}

export function createPaneContext(
  facts: PaneContextFacts,
  actions: PaneContextActions = {},
): HTMLElement {
  const el = document.createElement('div')
  el.className = 'ui-pane-context'
  fill(el, facts, actions)
  return el
}

/** Restate an existing PaneContext in place — TerminalContent's
 *  `_applyEnvironmentView`/`_onHomeKnown`/`_onBranchChanged` seam, the same
 *  facts already pushed to the composer's PromptContext (nocx-9bpeq.16). */
export function updatePaneContext(
  el: HTMLElement,
  facts: PaneContextFacts,
  actions: PaneContextActions = {},
): void {
  fill(el, facts, actions)
}
