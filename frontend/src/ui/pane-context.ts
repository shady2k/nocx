// PaneContext — the strip that names a pane before its transcript starts
// (decision 2026-09-15-terminal-screen-mockup-decision.md §1 item 3):
// `folder ~/repos/nocx │ branch main` in `01`, a local/remote pane identity
// in `03`, and the foreground-program/input-owner readout in `02`. Mounted
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
// NOT PromptContext underneath. The decision record's §1 item 3 assigns
// PromptContext's `chrome` presentation to the shared-kit task (A); this
// component is the layout task's (D) own file, and A's variant does not
// exist yet in this tree. Rendering the muted path/branch parts locally
// here — instead of leaving the strip broken, or reaching into A's
// exclusive prompt-context.ts — is the "temporary local fallback" the
// wave's common rules allow. `/* until A lands PromptContext's chrome
// presentation */` marks the one spot that should be replaced with
// `createPromptContext(facts, { presentation: 'chrome' })` once it exists.

import { FolderIcon, GitBranchIcon, ServerIcon, KeyboardIcon, iconElement } from './icons'

// Not exported: nothing outside this module names the kind independently
// of `PaneContextFacts.kind` — an exported alias with no external caller is
// exactly what the dead-exports ratchet exists to catch.
type PaneContextKind = 'local' | 'remote' | 'program'

export interface PaneContextFacts {
  /** Which identity this strip names right now. `program` is the
   *  alternate-screen/foreground-program presentation (`02`); `local` and
   *  `remote` are the ordinary and SSH-child presentations (`01`, `03`). */
  kind: PaneContextKind
  /** The short path (`cwdLabel`'s own answer) — shown for `local` and
   *  `remote`. Never fetched here: the caller (TerminalContent) already
   *  derives it for the composer and a block's own prompt line, and a
   *  second derivation is exactly what nocx-9bpeq.16 exists to prevent. */
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

function verticalDivider(): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-pane-context__divider'
  el.setAttribute('aria-hidden', 'true')
  return el
}

/** `local`/`remote`: identity icon, optional host, muted path, and — local
 *  only — a divider plus the branch. Built locally rather than through
 *  PromptContext (see the file header): both parts read muted here, which
 *  is the opposite of PromptContext's own accent path/branch, because a
 *  block's prompt line and this chrome answer two different questions
 *  (spec: "prompt lines inside blocks remain accent"). */
function buildIdentity(facts: PaneContextFacts): HTMLElement[] {
  const children: HTMLElement[] = []
  const icon = iconElement(facts.kind === 'remote' ? ServerIcon : FolderIcon)
  icon.classList.add('ui-pane-context__icon')
  children.push(icon as unknown as HTMLElement)
  if (facts.kind === 'remote' && facts.host) {
    children.push(textPart('ui-pane-context__host', facts.host))
  }
  children.push(textPart('ui-pane-context__path', facts.path ?? '~'))
  if (facts.kind === 'local' && facts.branch) {
    children.push(verticalDivider())
    const branchIcon = iconElement(GitBranchIcon)
    branchIcon.classList.add('ui-pane-context__icon')
    children.push(branchIcon as unknown as HTMLElement)
    children.push(textPart('ui-pane-context__branch', facts.branch))
  }
  return children
}

/** `02`: the foreground program's own name on the leading edge, and —
 *  trailing — who owns the keyboard right now plus the Session actions
 *  escape. Neither TUI internals nor a second status line (spec §5): this
 *  reads facts TerminalContent already has, and draws no program state of
 *  its own. */
function buildProgram(facts: PaneContextFacts, actions: PaneContextActions): HTMLElement[] {
  const children: HTMLElement[] = []
  children.push(textPart('ui-pane-context__program', facts.program ?? ''))
  if (facts.path) children.push(textPart('ui-pane-context__path', facts.path))
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
