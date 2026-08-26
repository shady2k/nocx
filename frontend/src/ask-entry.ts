// The ask entry gesture (nocx-4wtlh): the caret indicator that renders
// InputTargetRegistry.active(), and the blocks or output rows a person marks
// for a question. Selection is a quote; the mark carries the same granularity
// through session.read.

import { StateEffect, type Extension } from '@codemirror/state'
import { EditorView, GutterMarker, ViewPlugin, gutter } from '@codemirror/view'
import type { BadgeTone } from './ui/badge'
import { createModeIndicator } from './ui/mode-indicator'
import { blockKindRules, type BlockKind } from './scrollback/blocks'

export interface GrantBlock {
  readonly itemId: string
  readonly blockEl: HTMLElement
  readonly command: string
  readonly state: 'running' | 'exited'
  /** The selected output window; omitted means the whole block. */
  readonly start?: number
  readonly count?: number
}

function selectedWindow(
  blockEl: HTMLElement,
  range: Range,
): { start: number; count: number } | null {
  const rows = Array.from(blockEl.querySelectorAll<HTMLElement>('.term-line'))
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

function blockOf(node: Node | null): HTMLElement | null {
  const element = node instanceof Element ? node : (node?.parentElement ?? null)
  return element?.closest<HTMLElement>('.cmd-block') ?? null
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

/** A selection marks its selected rows; without output rows it marks the whole block. */
export function grantBlockFromSelection(sel: Selection | null): GrantBlock | null {
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null
  const range = sel.getRangeAt(0)
  const start = blockOf(range.startContainer)
  const end = blockOf(range.endContainer)
  if (!start || start !== end) return null
  const grant = grantBlockFromElement(start)
  if (!grant) return null
  const window = selectedWindow(start, range)
  return window ? { ...grant, ...window } : grant
}

// ── The line-start indicator (ADR-0004 §3's UI chip) ───────────────────────

/** Repaint signal: the registry's active target changed (or the host wants
 *  the chip re-read). */
const refreshIndicator = StateEffect.define<null>()

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

class TargetMarker extends GutterMarker {
  constructor(
    private readonly word: string,
    private readonly tone: BadgeTone,
    private readonly targetId: string,
    private readonly onToggle: () => void,
  ) {
    super()
  }
  eq(other: TargetMarker): boolean {
    return other.word === this.word && other.tone === this.tone && other.targetId === this.targetId
  }
  toDOM(): HTMLElement {
    // The kit's ModeIndicator: the badge shape and tone are ui-badge's, the
    // operable variance is the indicator's own (ui/mode-indicator.ts). The
    // gutter PLACES it; the component owns its appearance.
    return createModeIndicator({
      word: this.word,
      tone: this.tone,
      targetId: this.targetId,
      onClick: this.onToggle,
    })
  }
}

/**
 * The line-start indicator: the token in the prompt rendering what the
 * ACTIVE target does — `Run` for the shell, `Ask` for the assistant — and
 * toggling the target on click. The host wires the registry's active target
 * and pushes every change through set(); the word and tone are this
 * module's own mapping (targetPresentation), never a rename of the target.
 * The editor stays passive; this is a decoration, never a second input owner.
 *
 * IT IS A GUTTER, NOT A WIDGET IN THE DOCUMENT, and that is the whole point
 * (nocx-4wtlh, after the widget shipped and was lived with). A widget at
 * position 0 sits INSIDE `.cm-content` — the element carrying
 * `role="textbox"` — so the control's label became part of the textbox's
 * text: a screen reader read "Run printf ..." as the line's content, and
 * every e2e assertion that reads the prompt got `Runprintf ...` back. The
 * same placement also cost three separate repairs, each a symptom of a
 * control pretending to be text: CM6's zero-width widget buffer painting a
 * broken-image square when it was given the trailing gap, a chip sitting
 * below a baseline it shares with larger text, and a second line that began
 * underneath the token because only the first line had one.
 *
 * A gutter answers all of it structurally. It lives beside the content, not
 * in it, so the textbox contains exactly what the person typed; it reserves
 * its width on EVERY line, so continuation lines and wrapped rows align
 * where the caret starts with no measurement and no hanging indent; and it
 * is where an editor is expected to put a prompt sigil.
 */
export class TargetIndicator {
  /** The word currently rendered, for the gutter's marker. */
  word = targetPresentation('shell', 'Shell').word
  /** The badge tone currently rendered — the active state's register. */
  tone: BadgeTone = targetPresentation('shell', 'Shell').tone
  /** The target id currently rendered (the data-target hook). */
  targetId = 'shell'
  /** The explicit switch (ADR-0004 §3): wired once by the host; the marker
   *  and the ⌘/Ctrl+Enter seam both end here. Reads the registry live at
   *  call time, so it never goes stale. */
  readonly toggle: () => void
  private view: EditorView | null = null

  constructor(toggle: () => void) {
    this.toggle = toggle
  }

  /** The CM6 extension the host feeds to the CommandEditor. */
  extension(): Extension {
    return indicatorGutter(this)
  }

  /** The gutter registers the live view so set() can repaint it. */
  attachView(view: EditorView): void {
    this.view = view
  }

  /** Repaint with the registry's active target — called by the host
   *  whenever the registry reports a change, never on any other signal.
   *  The WORD and TONE are derived here (targetPresentation); the
   *  indicator never shows the target's internal label. */
  set(targetId: string, label: string): void {
    const p = targetPresentation(targetId, label)
    if (this.word === p.word && this.tone === p.tone && this.targetId === targetId && this.view)
      return
    this.word = p.word
    this.tone = p.tone
    this.targetId = targetId
    this.view?.dispatch({ effects: refreshIndicator.of(null) })
  }
}

/** The gutter half of the indicator: one marker, on the FIRST line only,
 *  repainted when set() dispatches its effect. Later lines get the gutter's
 *  reserved width and no marker — the token is the prompt's sigil, not a
 *  per-line ornament — and `initialSpacer` keeps that width stable from the
 *  first frame instead of letting the text jump left before the marker
 *  renders. */
function indicatorGutter(indicator: TargetIndicator): Extension {
  const marker = (): TargetMarker =>
    new TargetMarker(indicator.word, indicator.tone, indicator.targetId, indicator.toggle)
  return [
    ViewPlugin.define((view) => {
      indicator.attachView(view)
      // CM6 marks its gutter container aria-hidden, which is right for the
      // thing gutters usually hold — line numbers are decoration, and a
      // screen reader gains nothing from them. Ours holds an operable
      // control: the one explicit switch ADR-0004 §3 requires. A focusable
      // button inside an aria-hidden subtree is exposed to nobody, so the
      // attribute is removed for THIS editor's gutters and no other's. The
      // measurement spacer CM6 keeps beside the real marker is
      // visibility:hidden, which assistive technology already ignores, so
      // removing the attribute exposes one control and not two.
      view.requestMeasure({
        read: () => null,
        write: (_, v) => v.dom.querySelector('.cm-gutters')?.removeAttribute('aria-hidden'),
      })
      return {}
    }),
    gutter({
      class: 'nocx-editor-target-gutter',
      lineMarker: (view, line) => (line.from === 0 ? marker() : null),
      initialSpacer: () => marker(),
      updateSpacer: () => marker(),
      lineMarkerChange: (update) =>
        update.transactions.some((t) => t.effects.some((e) => e.is(refreshIndicator))),
    }),
  ]
}
