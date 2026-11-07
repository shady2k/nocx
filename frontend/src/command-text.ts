// One way to put a command on screen, for every surface that shows one.
//
// A command carrying `{{secret:NAME}}` reads as a chip in the editor, and it
// took three separate rounds of "why is it raw here too?" to notice that the
// question was never about any one surface: the editor, the scrollback block
// and the recall panel each rendered command text their own way, so each one
// had to learn about references separately, and each one learned late. This
// module is the answer to "can it be done globally" — a surface calls it
// instead of building text nodes, and a future kind of decoration is added
// here once rather than in every window that displays a command.
//
// The editor is the deliberate exception and stays a CodeMirror decoration:
// its text is editable and its ranges must map through every keystroke,
// which is a different mechanism for the same fact (see secret-chip.ts).
import { findReferences } from './secret-reference'
import { createSecretChip } from './ui/secret-chip'
import { secretChipTitles } from './i18n/kit-labels'

/** A highlighted range in the command text — the recall panel's search
 *  match. Offsets are UTF-16 code units, half-open. */
export interface CommandHighlight {
  from: number
  to: number
}

/**
 * The command as a fragment: every `{{secret:NAME}}` a chip, every
 * highlighted range a `<mark>`, everything else a text node.
 *
 * Highlights inside a reference are dropped rather than split: a chip is one
 * glyph as far as the reader is concerned, and half a chip painted as a
 * search match is noise. Ranges are clamped and processed in order, so an
 * out-of-range or overlapping range can never reorder the text.
 */
export function commandFragment(
  command: string,
  highlights: ReadonlyArray<CommandHighlight> = [],
  markClass = '',
): DocumentFragment {
  const frag = document.createDocumentFragment()
  const refs = findReferences(command)
  const marks = [...highlights].sort((a, b) => a.from - b.from)

  let pos = 0
  for (const ref of refs) {
    if (ref.from > pos) appendMarked(frag, command, pos, ref.from, marks, markClass)
    frag.appendChild(createSecretChip(ref.name, secretChipTitles()))
    pos = ref.to
  }
  if (pos < command.length) appendMarked(frag, command, pos, command.length, marks, markClass)
  return frag
}

/** `command[from, to)` with the highlights that fall inside it marked. */
function appendMarked(
  frag: DocumentFragment,
  command: string,
  from: number,
  to: number,
  marks: ReadonlyArray<CommandHighlight>,
  markClass: string,
): void {
  let pos = from
  for (const m of marks) {
    const start = Math.max(pos, Math.min(m.from, to))
    const end = Math.max(start, Math.min(m.to, to))
    if (end <= start) continue
    if (start > pos) frag.appendChild(document.createTextNode(command.slice(pos, start)))
    const mark = document.createElement('mark')
    if (markClass !== '') mark.className = markClass
    mark.textContent = command.slice(start, end)
    frag.appendChild(mark)
    pos = end
  }
  if (pos < to) frag.appendChild(document.createTextNode(command.slice(pos, to)))
}
