// AnswerBody — the ONE owner of what an assistant answer's body looks like
// (nocx-4em1z).
//
// It was written inside the streaming path, because the streaming path was
// the only caller: an answer arrived in chunks, and the code that drew it was
// shaped around receiving them. A RESTORED answer has the same body and no
// stream — the whole text at once, out of the ledger — and drawing it needed
// the same headings, lists, inline code, fences and wrapping.
//
// A second renderer beside this one would be the shape AGENTS.md names: two
// implementations of one concept, agreeing everywhere anybody looks and
// disagreeing somewhere nobody does. So the drawing moved out here and the
// stream became one of its two callers.
//
// WHAT IT OWNS: the rows, the fence regions and which grammar each row is
// painted in. WHAT IT DOES NOT: the block, its header, its status chips, the
// waiting state, and the elements a turn places through it (the reasoning
// note is the one today) — those land in arrival order, but they are built
// by their own owners.
//
// CHUNKS, NOT LINES, ARE THE INPUT. `append` takes whatever arrived and
// keeps the trailing partial row open, because a stream splits mid-line and
// the next chunk continues it. A caller with the whole text calls it once;
// the machine cannot tell the difference, which is exactly why there is only
// one of it.
import { paintShellInto } from './shell-paint'
import {
  paintAnswerLine,
  paintTableRow,
  tableDelimiterAlignments,
  tableRowParts,
  type TableAlignment,
} from '../ui/answer-markdown'
import { mountCodeBlockCopyButton } from '../ui/code-block'
import type { CommandSnapshotStore } from '../command-snapshot'

// ── A fenced block's language ───────────────────────────────────────────────
//
// nocx has ONE lexer (shell-highlight.ts, the same tokenizer the command
// editor and the frozen header use), so a fence is either shell or it is
// plain. A second highlighter for a second language is exactly the "look for
// the existing answer before you write a second one" prohibition, and
// guessing at a grammar we do not have would colour Python as if it were sh.
//
// A fence with NO info string is treated as shell. This is a terminal: a bare
// fence in an assistant's answer is a command far more often than it is
// anything else, and the tokenizer is purely syntactic — a wrong guess costs
// colour and never meaning, where leaving every bare fence grey costs the
// feature the owner asked for.
const SHELL_FENCE = new Set([
  '',
  'sh',
  'bash',
  'zsh',
  'ksh',
  'fish',
  'shell',
  'shellscript',
  'shell-session',
  'console',
  'terminal',
])

/** The info string of a fence opener: everything after the backticks, lower
 *  cased, first word only (```` ```bash title="x" ```` is bash). */
function fenceLanguage(openerLine: string): string {
  const rest = openerLine.trim().replace(/^`+/, '').trim()
  return rest.split(/\s+/)[0].toLowerCase()
}

const FENCE_MARKER = /^\s*```/

/** The body of one answer, mid-write. */
export interface AnswerBody {
  /** Append a chunk. Everything before the last '\n' becomes completed rows;
   *  the remainder stays open for the next call. */
  append(text: string): void
  /** Place a non-text element (the reasoning note) where it arrived. Closes
   *  the open row first, so text arriving afterwards cannot be written into a
   *  row that sits BEFORE the element. */
  insert(node: HTMLElement): void
  /** No more text: paint a final non-empty partial row, then drop trailing
   *  empty rows the '\n'-terminated stream leaves behind, in the body and
   *  inside a fence alike. */
  finish(): void
}

export interface AnswerBodyOpts {
  /** The tab's command-existence store — the same instance the frozen
   *  headers judge against, so a shell fence in an answer is tokenised
   *  exactly as the same words would be in a block header. */
  store: CommandSnapshotStore
  /** Called the first time anything is written into the body. The streaming
   *  caller uses it to retire the typing dots, which stand in for text that
   *  is not there yet; a restored answer passes nothing, because it never
   *  had them. */
  /** Clipboard seam for the code-only button on each fenced region. */
  copy?: (text: string) => Promise<void>
  onContent?: () => void
}

/** Start writing an answer body into `outputEl`. */
export function createAnswerBody(outputEl: HTMLElement, opts: AnswerBodyOpts): AnswerBody {
  const { store, onContent } = opts

  // The streamed chunks split MID-LINE, so the body keeps one persistent
  // partial row: a chunk's final segment stays on it and the next chunk
  // continues it. Every '\n' completes a row — including a chunk ending in
  // '\n', whose trailing empty segment starts a fresh (possibly empty)
  // partial, so "a\n" + "b" renders as two rows, never "ab".
  //
  // A fenced block the model returns (```…```) is the one place in an answer
  // where the command grammar is the right grammar: its rows land in a
  // `.cmd-output-code` container that stays monospace and unwrapped (the
  // nocx-juau rule, reachable through the kind, never by accident). The fence
  // toggles on the COMPLETED line, so a marker split across chunks still
  // works, and BOTH delimiters belong to the code region: the opener moves
  // into the container it opens, the closer stays in the container it closes.
  // A second fence after intervening prose gets a fresh container, so the
  // order fence → prose → fence survives.
  let partial: HTMLSpanElement | null = null
  // Leading blank rows are not answer content. Keep the decision at the
  // completed-line boundary so a partial row can span chunks without
  // accidentally dropping the first real line. Once content starts, blank
  // rows are ordinary paragraph spacing and must remain visible.
  let started = false
  let inFence = false
  /** Delimiters remain in the DOM for answer Copy output and selection, but
   * the fence button copies only code rows — never markers, info, or prose. */
  const codeText = (container: HTMLElement): string =>
    Array.from(container.querySelectorAll<HTMLElement>('.term-line'))
      .filter((row) => row.dataset.fenceDelim === undefined)
      .map((row) => row.textContent ?? '')
      .join('\n')

  /** The language the open fence declared — read once, at the opener, and
   * used for every row until the closer. */
  let fenceLang = ''
  let codeEl: HTMLElement | null = null
  let tableEl: HTMLElement | null = null
  let tableAlignments: TableAlignment[] = []
  let tableColumns = 0
  let tableCandidate: { row: HTMLSpanElement; text: string } | null = null

  const updateTableColumns = (count: number): void => {
    if (!tableEl || count <= tableColumns) return
    tableColumns = count
    tableEl.style.setProperty('--md-table-columns', String(tableColumns))
  }

  const endTable = (): void => {
    tableEl = null
    tableAlignments = []
    tableColumns = 0
  }

  const startTable = (
    candidate: { row: HTMLSpanElement; text: string },
    delimiter: HTMLSpanElement,
    delimiterText: string,
  ): void => {
    const container = document.createElement('div')
    container.className = 'ui-md-table'
    container.setAttribute('role', 'table')
    outputEl.insertBefore(container, candidate.row)
    container.appendChild(candidate.row)
    tableEl = container
    tableAlignments = tableDelimiterAlignments(delimiterText) ?? []
    updateTableColumns(
      Math.max(tableAlignments.length, tableRowParts(candidate.text)?.cells.length ?? 0),
    )
    paintTableRow(candidate.row, candidate.text, tableAlignments, true)
    container.appendChild(delimiter)
    paintTableRow(delimiter, delimiterText, tableAlignments, false, true)
  }
  const codeContainer = (): HTMLElement => {
    if (!codeEl) {
      const container = document.createElement('div')
      container.className = 'cmd-output-code ui-code-block ui-code-block-wrap'
      container.dataset.variant = 'answer'
      container.tabIndex = 0
      outputEl.appendChild(container)
      codeEl = container
      if (opts.copy) {
        const copyHost = document.createElement('div')
        copyHost.className = 'cmd-output-code-copy-host'
        container.appendChild(copyHost)
        mountCodeBlockCopyButton(copyHost, {
          getText: () => codeText(container),
          copy: opts.copy,
          keepHostInBlock: true,
        })
      }
    }
    return codeEl
  }

  const completeRow = (row: HTMLSpanElement, line: string): void => {
    if (!started && line.trim() === '') {
      row.remove()
      return
    }
    started = true
    if (FENCE_MARKER.test(line)) {
      tableCandidate = null
      endTable()
      const opening = !inFence
      row.dataset.fenceDelim = opening ? 'open' : 'close'
      inFence = opening
      if (opening) {
        // The opener belongs to the code region it opens: a fresh
        // container, with the marker as its first row.
        codeEl = null
        row.remove()
        const container = codeContainer()
        container.appendChild(row)
        fenceLang = fenceLanguage(line)
        const label = container.querySelector<HTMLElement>('.ui-code-block__label')
        if (label) label.textContent = fenceLang || 'Code'
      }
      // The closer was created inside the code region and stays there; the
      // rows after it go back to the prose body.
    } else if (inFence) {
      // A code row. The EXISTING lexer, on a shell fence only
      // (SHELL_FENCE above says why), through the one painter that also
      // catches up if the grammar has not loaded — an answer that
      // arrives in the first milliseconds of a launch must not stay
      // grey forever.
      if (SHELL_FENCE.has(fenceLang) && line !== '') paintShellInto(row, line, store)
    } else {
      const delimiter = tableCandidate ? tableDelimiterAlignments(line) : null
      if (delimiter && tableCandidate) {
        const candidate = tableCandidate
        tableCandidate = null
        startTable(candidate, row, line)
        return
      }
      if (tableEl) {
        const parts = tableRowParts(line)
        if (!parts) {
          row.remove()
          outputEl.appendChild(row)
          endTable()
        } else {
          tableEl.appendChild(row)
          updateTableColumns(parts.cells.length)
          paintTableRow(row, line, tableAlignments, false)
        }
        return
      }
      // Prose. The structure a model actually emits is painted per completed
      // line, with every byte escaped (nocx-swoje; the answer-markdown module
      // owns both the ordinary grammar and table cells).
      paintAnswerLine(row, line)
      tableCandidate = tableRowParts(line) ? { row, text: line } : null
    }
  }

  const makeRow = (): HTMLSpanElement => {
    const span = document.createElement('span')
    span.className = 'term-line'
    ;(inFence ? codeContainer() : (tableEl ?? outputEl)).appendChild(span)
    return span
  }

  const removeEmptyTextBlock = (): void => {
    if (outputEl.hasChildNodes()) return
    const block = outputEl.parentElement
    if (block?.dataset.blockKind !== 'text') return
    block.remove()
  }

  return {
    append(text: string): void {
      if (text === '') return
      onContent?.()
      const parts = text.split('\n')
      for (let i = 0; i < parts.length; i++) {
        const part = parts[i]
        if (i < parts.length - 1) {
          // A complete line: finish the current partial (or open one) and
          // close the row.
          if (!partial) partial = makeRow()
          partial.textContent += part
          const row = partial
          partial = null
          completeRow(row, row.textContent ?? '')
        } else {
          // The final segment stays partial — the next chunk continues it.
          if (!partial) partial = makeRow()
          partial.textContent += part
          if (!started && part.trim() !== '') started = true
        }
      }
    },

    insert(node: HTMLElement): void {
      onContent?.()
      const hadContent = partial !== null && partial.textContent.trim() !== ''
      if (hadContent) started = true
      if (!started && partial) partial.remove()
      partial = null
      tableCandidate = null
      endTable()
      outputEl.appendChild(node)
    },

    finish(): void {
      if (partial) {
        const row = partial
        partial = null
        const line = row.textContent ?? ''
        if (line.trim() === '') row.remove()
        else completeRow(row, line)
      }
      for (;;) {
        const last = outputEl.lastElementChild
        if (!last) {
          removeEmptyTextBlock()
          return
        }
        if (last.classList.contains('term-line') && last.textContent?.trim() === '') {
          last.remove()
          continue
        }
        if (last.classList.contains('cmd-output-code') || last.classList.contains('ui-md-table')) {
          const rows = last.querySelectorAll<HTMLElement>(':scope > .term-line')
          const row = rows[rows.length - 1]
          if (row?.textContent?.trim() === '') {
            row.remove()
            continue
          }
          if (last.classList.contains('cmd-output-code') && !last.hasChildNodes()) {
            last.remove()
            continue
          }
        }
        return
      }
    },
  }
}
