// The ask entry gesture (nocx-4wtlh): the caret indicator that renders
// InputTargetRegistry.active(), and the blocks or output rows a person marks
// for a question. Selection is a quote; the mark carries the same granularity
// through session.read.

import { type Extension } from '@codemirror/state'
import { ViewPlugin } from '@codemirror/view'
import type { BadgeTone } from './ui/badge'
import { createModeIndicator, type ModeIndicatorMenuItem } from './ui/mode-indicator'
import { blockKindRules, type BlockKind } from './scrollback/blocks'

export interface GrantBlock {
  readonly itemId: string
  readonly blockEl: HTMLElement
  readonly command: string
  readonly state: 'running' | 'exited'
  /** The selected output window; omitted means the whole block. */
  readonly start?: number
  readonly count?: number
  /** Present only on the renderer's frozen-frame attachment; it is not a
   *  person mark and must never be counted or painted as one. */
  readonly automatic?: true
}

/** The rows a mark's window addresses. A block's output rows are
 *  `.term-line`; the frozen screen's are `.nocx-freeze-frame__row`. One
 *  question — "which rows does this mark cover" — answered in one place, so
 *  the window derivation, the granted paint and the reveal cannot disagree
 *  about it (nocx-hp8p2.7). */
export function grantRows(markable: HTMLElement): HTMLElement[] {
  const selector = markable.classList.contains(FROZEN_SCREEN_CLASS)
    ? '.nocx-freeze-frame__row'
    : '.term-line'
  return Array.from(markable.querySelectorAll<HTMLElement>(selector))
}

/** The frozen screen's own class. A selection lands in one of two markable
 *  surfaces — a scrollback block, or the pinned screen over a running
 *  program — and this names the second. */
const FROZEN_SCREEN_CLASS = 'nocx-freeze-frame'

function selectedWindow(
  blockEl: HTMLElement,
  range: Range,
): { start: number; count: number } | null {
  const rows = grantRows(blockEl)
  const selected = rows
    .map((row, index) => (range.intersectsNode(row) ? index : -1))
    .filter((index) => index >= 0)
  if (selected.length === 0) return null
  // A window is a window only when it is a genuine SUBSET (nocx-5u3oz.16). A
  // drag across every output row is one movement and means the block, so it
  // collapses to a whole-block mark — otherwise "select everything" and
  // "select the block" would be different things behind the same gesture, and
  // the block would lose its mark at exactly the moment a person meant it most.
  if (selected.length === rows.length) return null
  const start = selected[0]
  return { start, count: selected[selected.length - 1] - start + 1 }
}

/** The markable surface a node is in: a scrollback block, or the frozen
 *  screen pinned over a running program. Both carry rows a person can select
 *  and mark; before nocx-hp8p2.7 only the first was reachable, so the
 *  gesture inside the frame led nowhere. */
function markableOf(node: Node | null): HTMLElement | null {
  const element = node instanceof Element ? node : (node?.parentElement ?? null)
  return element?.closest<HTMLElement>(`.cmd-block, .${FROZEN_SCREEN_CLASS}`) ?? null
}

/** Derive one whole-block mark from the block's durable `data-entry-id`.
 *  Commands receive the lifecycle attempt id when they bind; restored and
 *  answer blocks carry their ledger entry id. Renderer selection counters
 *  are deliberately not mark identities. */
export function grantBlockFromElement(blockEl: HTMLElement): GrantBlock | null {
  const itemId = blockEl.dataset.entryId
  if (!itemId) return null
  const kind = (blockEl.dataset.blockKind ?? 'command') as BlockKind
  return {
    itemId,
    blockEl,
    command: blockKindRules(kind).label(blockEl),
    state: blockEl.classList.contains('cmd-block-running') ? 'running' : 'exited',
  }
}

/** A selection marks its selected rows; without output rows it marks the
 *  whole block.
 *
 *  `automatic` is the frozen screen's attachment, when one is pinned. A
 *  selection inside that screen NARROWS THAT ITEM rather than making a
 *  second one: the mark carries the attachment's own id plus a row span, so
 *  the model reads exactly the band and nothing is copied onto the wire
 *  (nocx-hp8p2.7). It comes back stripped of `automatic`, because it is a
 *  person mark now — counted on the chip and painted like any other. A
 *  selection covering the whole screen offers nothing: that is the
 *  attachment the question already carries. */
export function grantBlockFromSelection(
  sel: Selection | null,
  automatic: GrantBlock | null = null,
): GrantBlock | null {
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null
  const range = sel.getRangeAt(0)
  const start = markableOf(range.startContainer)
  const end = markableOf(range.endContainer)
  if (!start || start !== end) return null
  if (start.classList.contains(FROZEN_SCREEN_CLASS)) {
    if (!automatic) return null
    const rows = selectedWindow(start, range)
    if (!rows) return null
    return {
      itemId: automatic.itemId,
      blockEl: start,
      command: automatic.command,
      state: automatic.state,
      ...rows,
    }
  }
  const grant = grantBlockFromElement(start)
  if (!grant) return null
  const window = selectedWindow(start, range)
  return window ? { ...grant, ...window } : grant
}

// ── The line-start indicator (ADR-0004 §3's UI chip) ───────────────────────

/** The indicator's OWN presentation of a target — the word the person
 *  reads and the badge tone the state wears, never the target's internal
 *  name. InputTarget.label stays the registry's word ('Shell'/'Agent' —
 *  other consumers may legitimately read it); this map is the indicator's
 *  vocabulary, keyed by target id, and the tone is the badge register the
 *  running block already uses for the same author (agent = info). Unknown
 *  ids fall back to the label and the neutral tone (a future target still
 *  gets an honest chip). */
const TARGET_PRESENTATION: Record<string, { word: string; tone: BadgeTone }> = {
  shell: { word: 'Run', tone: 'neutral' },
  agent: { word: 'Ask', tone: 'info' },
}

function targetPresentation(targetId: string, label: string): { word: string; tone: BadgeTone } {
  return TARGET_PRESENTATION[targetId] ?? { word: label, tone: 'neutral' }
}

/** The menu's rows (spec 2026-09-15 §6): one per entry this module already
 *  knows about, in TARGET_PRESENTATION's own order. This is "the registry
 *  the gutter already knows" — the indicator has no live handle on
 *  InputTargetRegistry (it is handed a toggle, not the registry itself),
 *  and this vocabulary is the one it does own. Exported so the derivation
 *  is a plain, table-driven unit under test rather than something only
 *  visible through a mounted CM6 gutter. */
export const TARGET_MENU_ITEMS: ModeIndicatorMenuItem[] = Object.entries(TARGET_PRESENTATION).map(
  ([targetId, presentation]) => ({ targetId, word: presentation.word }),
)

/**
 * The line-start indicator: the token rendering what the ACTIVE target does
 * — `Run` for the shell, `Ask` for the assistant — and toggling the target
 * on click. The host wires the registry's active target and pushes every
 * change through set(); the word and tone are this module's own mapping
 * (targetPresentation), never a rename of the target. The editor stays
 * passive; this is a decoration, never a second input owner.
 *
 * IT MOUNTS BESIDE CM6, NOT INSIDE THE DOCUMENT OR EVEN INSIDE `.cm-editor`
 * (nocx-4wtlh's original lesson, carried one step further by spec
 * 2026-09-15 §4). The control used to live in a CM6 gutter — beside the
 * content, not in it, which already fixed the widget-in-`.cm-content`
 * defect a still earlier version had (a screen reader reading "Run printf
 * ..." as the line's content; every e2e prompt assertion getting
 * `Runprintf ...` back). The gutter is gone now too: ComposerFrame gives
 * the switch its own grid column beside CM6's whole box (composer-
 * frame.ts), so ownership is cleaner still — CM6 never draws it, never
 * reserves space for it inside its own DOM, and ISN'T EVEN THE ELEMENT the
 * ownership question is about any more. `extension()` still returns a CM6
 * `Extension`, unchanged as a call site (terminal-content.ts:
 * `this.indicator.extension()`, composed into the SAME stable extensions
 * array as before) — it just no longer builds a gutter. It is a ViewPlugin
 * that, once CM6 has appended its own root into ComposerFrame's `editor`
 * slot (`view.dom.closest('.ui-composer-frame__field')` — safe exactly
 * because `@codemirror/view` appends `view.dom` to `config.parent` before
 * constructing any ViewPlugin, so the field's other slots already exist as
 * siblings by the time this runs), mounts the SAME kit control as a plain
 * DOM child of that field, ahead of CM6's own slot — which is what puts it
 * in the grid's leading column, by construction, with no coordinate this
 * module or CommandEditor has to agree on separately.
 */
export class TargetIndicator {
  /** The word currently rendered. */
  word = targetPresentation('shell', 'Shell').word
  /** The badge tone currently rendered — the active state's register. */
  tone: BadgeTone = targetPresentation('shell', 'Shell').tone
  /** The target id currently rendered (the data-target hook). */
  targetId = 'shell'
  /** The explicit switch (ADR-0004 §3): wired once by the host; the button
   *  and the ⌘/Ctrl+Enter seam both end here. Reads the registry live at
   *  call time, so it never goes stale. */
  readonly toggle: () => void
  /** The field this control is mounted in — null before CM6 attaches (or
   *  after it detaches). Repainting with no host is a no-op: there is
   *  nowhere yet (or any more) to put the button. */
  private host: HTMLElement | null = null
  private button: HTMLButtonElement | null = null

  constructor(toggle: () => void) {
    this.toggle = toggle
  }

  /** The CM6 extension the host feeds to the CommandEditor — unchanged as
   *  a call site; see the class doc for what it does instead of a gutter
   *  now. */
  extension(): Extension {
    return indicatorHost(this)
  }

  /** Mount into ComposerFrame's field, ahead of CM6's own slot. Called once
   *  per CM6 view lifetime by the ViewPlugin above. */
  mount(field: HTMLElement): void {
    this.host = field
    this.repaint()
  }

  /** Detach on CM6 view destruction — the button must not outlive the view
   *  whose lifetime it was mounted for (a torn-down editor leaves no
   *  orphaned control behind in the field it no longer owns). */
  unmount(): void {
    this.button?.remove()
    this.button = null
    this.host = null
  }

  /** Repaint with the registry's active target — called by the host
   *  whenever the registry reports a change, never on any other signal.
   *  The WORD and TONE are derived here (targetPresentation); the
   *  indicator never shows the target's internal label. */
  set(targetId: string, label: string): void {
    const p = targetPresentation(targetId, label)
    if (this.word === p.word && this.tone === p.tone && this.targetId === targetId && this.button)
      return
    this.word = p.word
    this.tone = p.tone
    this.targetId = targetId
    this.repaint()
  }

  /** Build a fresh button (the kit's ModeIndicator, `field` variant — spec
   *  §4) and swap it in at the field's leading position. A fresh element
   *  per repaint mirrors the CM6-gutter version's own marker() precedent:
   *  target switches are a rare, explicit user action, not a hot path. */
  private repaint(): void {
    if (!this.host) return
    const button = createModeIndicator({
      word: this.word,
      tone: this.tone,
      targetId: this.targetId,
      items: TARGET_MENU_ITEMS,
      variant: 'field',
      // The registry's switch is binary today (register() only ever sees
      // shell and agent), so "pick a target" and "toggle away from the
      // active one" are the same operation — picking the row that is
      // already active is the one case that must NOT toggle, or choosing
      // "Run" while already on Run would flip to Ask underneath the
      // person. A future third target would need the registry's own
      // setActive here instead; nothing in this module can reach it yet.
      onSelect: (targetId) => {
        if (targetId !== this.targetId) this.toggle()
      },
    })
    const previous = this.button
    // Inserted as the FIRST child, ahead of CM6's `.ui-composer-frame__editor`
    // slot and the submit slot after it — which is what places it in the
    // field grid's leading column (composer-frame.css), by DOM order alone.
    this.host.insertBefore(button, this.host.firstElementChild)
    previous?.remove()
    this.button = button
  }
}

/** The extension half of the indicator (see the class doc): a ViewPlugin
 *  that locates ComposerFrame's field — the ancestor CM6's own root was
 *  just appended into — and mounts the indicator there once, tearing it
 *  down when the view is destroyed. */
function indicatorHost(indicator: TargetIndicator): Extension {
  return ViewPlugin.define((view) => {
    const field = view.dom.closest<HTMLElement>('.ui-composer-frame__field')
    if (field) indicator.mount(field)
    return {
      destroy(): void {
        indicator.unmount()
      },
    }
  })
}
