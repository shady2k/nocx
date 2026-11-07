// The unresolved-redaction spans of a recalled command (ADR-0021's
// consequence, made structural this round): a history row that was masked
// cannot run as written — the value is not in the text. When recall places
// such a row in the editor, its redaction segments are registered here as a
// StateField of spans; the editor renders each as the unresolved chip
// variant, the host refuses to submit while any remain, and Enter opens
// resolution on the first one. Picking a secret replaces the span with
// {{secret:NAME}}, which renders as the ordinary resolved chip; the span
// collapses to zero length and leaves the list.
//
// The spans are positions, mapped through every subsequent edit — a
// one-shot decoration set would point at the wrong text the moment the user
// types. The host replaces the whole set when recall places a different
// row, or with [] when the draft is restored.
import { StateField, StateEffect } from '@codemirror/state'
import { Decoration, EditorView, WidgetType, type DecorationSet } from '@codemirror/view'
import { KIND_LABELS, type SecretKind } from './secret-kind'
import { createSecretChipUnresolved } from './ui/secret-chip'
import { secretChipTitles } from './i18n/kit-labels'

/** One unresolved masked segment in the current document. */
export interface UnresolvedSpan {
  readonly from: number
  readonly to: number
  readonly kind: SecretKind
}

/** Replace the whole set of unresolved spans. */
export const setUnresolvedSpans = StateEffect.define<readonly UnresolvedSpan[]>()

class UnresolvedChipWidget extends WidgetType {
  constructor(readonly kind: SecretKind) {
    super()
  }
  eq(other: UnresolvedChipWidget): boolean {
    return other.kind === this.kind
  }
  toDOM(): HTMLElement {
    return createSecretChipUnresolved(KIND_LABELS[this.kind], secretChipTitles())
  }
  ignoreEvent(): boolean {
    // A chip is one unit: no mouse gesture may enter the replaced span.
    return true
  }
}

function decorationsFor(spans: readonly UnresolvedSpan[]): DecorationSet {
  const ranges: Array<ReturnType<Decoration['range']>> = []
  for (const span of spans) {
    if (span.to <= span.from) continue
    ranges.push(
      Decoration.replace({ widget: new UnresolvedChipWidget(span.kind) }).range(span.from, span.to),
    )
  }
  return Decoration.set(ranges, true)
}

/**
 * The StateField every editor that can hold recalled masked text installs.
 * One definition, many states: the host reads the live spans through the
 * editor's seam, and the extension is the same object so the read finds the
 * installed value. The field IS the extension — no wrapper, one name.
 */
export const unresolvedRedactionField = StateField.define<readonly UnresolvedSpan[]>({
  create() {
    return []
  },
  update(spans, tr) {
    for (const effect of tr.effects) {
      if (effect.is(setUnresolvedSpans)) {
        return effect.value
      }
    }
    if (!tr.docChanged) return spans
    // A span WHOLY replaced by the change — a resolution, a deletion of the
    // mask — collapses to zero length and leaves the list. mapPos alone
    // cannot tell "the span is exactly the replaced range" from "the span
    // sits at the range's edges", and a fully replaced span would otherwise
    // MAP ONTO the inserted reference and keep its chip over it. Every
    // other edit maps the surviving spans through: text typed before a mask
    // shifts it, an edit inside it shrinks it.
    const collapsed = new Set<UnresolvedSpan>()
    tr.changes.iterChanges((fromA, toA) => {
      if (toA <= fromA) return
      for (const span of spans) {
        if (span.from >= fromA && span.to <= toA) collapsed.add(span)
      }
    })
    return spans
      .filter((span) => !collapsed.has(span))
      .map((span) => ({
        from: tr.changes.mapPos(span.from, 1),
        to: tr.changes.mapPos(span.to, -1),
        kind: span.kind,
      }))
      .filter((span) => span.to > span.from)
  },
  provide: (f) => [
    EditorView.decorations.from(f, decorationsFor),
    EditorView.atomicRanges.of((view) => decorationsFor(view.state.field(f))),
  ],
})

/** Whether the document still carries any unresolved span. */
export function hasUnresolved(spans: readonly UnresolvedSpan[]): boolean {
  return spans.some((s) => s.to > s.from)
}

/** The first unresolved span, in document order — the one Enter opens
 *  resolution on. Null when none remain. */
export function firstUnresolved(spans: readonly UnresolvedSpan[]): UnresolvedSpan | null {
  return spans.find((s) => s.to > s.from) ?? null
}
