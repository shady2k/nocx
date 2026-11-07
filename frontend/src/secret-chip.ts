// The chip DECORATION for the command editor: every {{secret:NAME}} span in
// the document renders as an atomic chip (ADR-0021 — the reference is what
// gets stored, sent and resolved; only the RENDERING is a chip).
//
// Two halves, both provided by the same StateField:
//   - EditorView.decorations: the replacement widget (ui/secret-chip's
//     emitter) covers the span — the caret never sees the braces.
//   - EditorView.atomicRanges: the span is an ATOM for cursor motion and
//     deletion — the caret steps over it as one unit and Backspace removes
//     the whole reference rather than dismantling it into `{{secret:NAM`.
//     (Replacement decorations alone are NOT atomic; the facet is the
//     documented half, and secret-chip.test.ts pins both directions.)
//
// The document itself is never touched: typing, Backspace and paste all
// operate on the literal reference text, so a chip is always one keystroke
// from its text — deletion leaves a document that still parses.
import { StateField } from '@codemirror/state'
import { Decoration, EditorView, WidgetType, type DecorationSet } from '@codemirror/view'
import { findReferences } from './secret-reference'
import { createSecretChip } from './ui/secret-chip'
import { secretChipTitles } from './i18n/kit-labels'

class SecretChipWidget extends WidgetType {
  constructor(readonly name: string) {
    super()
  }
  eq(other: SecretChipWidget): boolean {
    return other.name === this.name
  }
  toDOM(): HTMLElement {
    return createSecretChip(this.name, secretChipTitles())
  }
  ignoreEvent(): boolean {
    // A chip is one unit: no mouse gesture may enter the replaced span.
    return true
  }
}

function chipDecorations(text: string): DecorationSet {
  const ranges: Array<ReturnType<Decoration['range']>> = []
  for (const ref of findReferences(text)) {
    ranges.push(
      Decoration.replace({ widget: new SecretChipWidget(ref.name) }).range(ref.from, ref.to),
    )
  }
  return Decoration.set(ranges, true)
}

/** Install in a CommandEditor to render references as chips. Document-level
 *  (not a language): the reference grammar belongs to the prompt, not to the
 *  shell, so this composes beside the shell's highlighting rather than
 *  inside it. */
export function secretChipExtension(): StateField<DecorationSet> {
  return StateField.define<DecorationSet>({
    create(state) {
      return chipDecorations(state.doc.toString())
    },
    update(decos, tr) {
      return tr.docChanged ? chipDecorations(tr.state.doc.toString()) : decos.map(tr.changes)
    },
    provide: (f) => [
      EditorView.decorations.from(f),
      EditorView.atomicRanges.of((view) => view.state.field(f)),
    ],
  })
}
