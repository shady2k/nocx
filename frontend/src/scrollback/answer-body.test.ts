// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { CommandSnapshotStore } from '../command-snapshot'
import { createAnswerBody } from './answer-body'
import { blockOutputText } from './blocks'

function renderRows(chunks: string[]): string[] {
  const output = document.createElement('div')
  const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
  for (const chunk of chunks) body.append(chunk)
  body.finish()
  return Array.from(output.querySelectorAll<HTMLElement>(':scope > .term-line')).map(
    (row) => row.textContent ?? '',
  )
}
describe('streamed markdown tables', () => {
  it('turns split table lines into a grid while preserving row order and text', () => {
    const block = document.createElement('div')
    block.className = 'cmd-block'
    const output = document.createElement('div')
    output.className = 'cmd-output'
    block.appendChild(output)
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('| Name | Age |\n|---|---:')
    body.append('|\n| Ada | 37 |\n| only')
    body.append(' |\nAfter')
    body.finish()

    const table = output.querySelector<HTMLElement>(':scope > .ui-md-table')
    expect(table).not.toBeNull()
    expect(table?.getAttribute('role')).toBe('table')
    const rows = table?.querySelectorAll<HTMLElement>(':scope > .term-line')
    expect(rows).toHaveLength(4)
    expect(Array.from(rows ?? []).map((row) => row.textContent)).toEqual([
      '| Name | Age |',
      '|---|---:|',
      '| Ada | 37 |',
      '| only |',
    ])
    expect(table?.querySelectorAll('.ui-md-table-cell')).toHaveLength(5)
    expect(table?.querySelector('.ui-md-table-cell-center')).toBeNull()
    expect(table?.querySelector('.ui-md-table-cell-right')).not.toBeNull()
    expect(output.querySelector<HTMLElement>(':scope > .term-line')?.textContent).toBe('After')
    expect(blockOutputText(block)).toBe('| Name | Age |\n|---|---:|\n| Ada | 37 |\n| only |\nAfter')
  })

  it('keeps prose pipes and inline-code pipes outside a table', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    body.append('A | B\nUse `a | b` literally\nNo delimiter here')
    body.finish()

    expect(output.querySelector('.ui-md-table')).toBeNull()
    expect(Array.from(output.querySelectorAll('.term-line')).map((row) => row.textContent)).toEqual(
      ['A | B', 'Use a | b literally', 'No delimiter here'],
    )
  })
})

describe('createAnswerBody leading rows', () => {
  it('drops blank rows before the first non-empty answer row', () => {
    expect(renderRows(['\n\nПривет! Чем помочь?'])).toEqual(['Привет! Чем помочь?'])
  })

  it('preserves a blank row between paragraphs', () => {
    expect(renderRows(['Привет!\n\nЧем помочь?'])).toEqual(['Привет!', '', 'Чем помочь?'])
  })

  it('drops leading blank rows split across chunks', () => {
    expect(renderRows(['\n', '\nПривет!'])).toEqual(['Привет!'])
  })
  it('removes a whitespace-only prose body when a call boundary finishes it', () => {
    const parent = document.createElement('div')
    parent.className = 'cmd-block'
    parent.dataset.blockKind = 'text'
    const output = document.createElement('div')
    parent.appendChild(output)
    document.body.appendChild(parent)
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append(' \t')
    body.finish()

    expect(parent.isConnected).toBe(false)
  })
  it('trims a whitespace-only trailing row but preserves interior spacing', () => {
    expect(renderRows(['before\n\nafter\n \t'])).toEqual(['before', '', 'after'])
  })
  it('preserves spacing after an element interrupts the first open row', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    body.append('First paragraph')
    const reasoning = document.createElement('div')
    reasoning.className = 'reasoning'
    body.insert(reasoning)
    body.append('\nSecond paragraph')
    body.finish()

    expect(
      Array.from(output.children).map((child) =>
        child.classList.contains('term-line') ? child.textContent : child.className,
      ),
    ).toEqual(['First paragraph', 'reasoning', '', 'Second paragraph'])
  })
  it('drops an unfinished empty row before an inserted reasoning node', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    const reasoning = document.createElement('div')
    reasoning.className = 'reasoning'

    body.append('short\n')
    body.insert(reasoning)
    body.finish()

    expect(
      Array.from(output.children).map((child) =>
        child.classList.contains('term-line') ? child.textContent : child.className,
      ),
    ).toEqual(['short', 'reasoning'])
  })
  it('trims the table’s unfinished row at the insert boundary', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    const reasoning = document.createElement('div')
    reasoning.className = 'reasoning'

    body.append('| A | B |\n|---|---|\n| x | y |\n')
    body.insert(reasoning)
    body.finish()

    const table = output.querySelector<HTMLElement>(':scope > .ui-md-table')
    expect(table?.querySelectorAll<HTMLElement>(':scope > .term-line')).toHaveLength(3)
    expect(table?.querySelector<HTMLElement>(':scope > .term-line:last-child')?.textContent).toBe(
      '| x | y |',
    )
  })

  it('keeps a split partial row while the answer is still streaming', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('a long answer ')
    expect(Array.from(output.querySelectorAll('.term-line')).map((row) => row.textContent)).toEqual(
      ['a long answer '],
    )
    body.append('continues')

    expect(Array.from(output.querySelectorAll('.term-line')).map((row) => row.textContent)).toEqual(
      ['a long answer continues'],
    )
  })

  it('keeps every written row in a long finished answer', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    const lines = Array.from({ length: 120 }, (_, i) => `line ${i + 1}`)

    body.append(lines.join('\n'))
    body.finish()

    const rows = Array.from(output.querySelectorAll<HTMLElement>(':scope > .term-line'))
    expect(rows).toHaveLength(lines.length)
    expect(rows.map((row) => row.textContent)).toEqual(lines)
    // jsdom cannot lay out boxes, so pin the shipped row-pitch contract and
    // assert that the body has exactly one flow row for each written line.
    const css = readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8')
    const rowRule = css.match(/\.term-line\s*\{([^}]*)\}/)
    expect(rowRule).not.toBeNull()
    expect(rowRule![1]).toContain('min-height: var(--term-cell-height, 1.2em)')
    expect(rowRule![1]).toContain('line-height: var(--term-cell-height, 1.2em)')
    expect(output.childElementCount).toBe(lines.length)
    expect(rows.some((row) => row.textContent?.trim() === '')).toBe(false)
  })
  it('drops leading blanks when an element arrives before answer text', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    const reasoning = document.createElement('div')
    reasoning.className = 'reasoning'
    body.insert(reasoning)
    body.append('\nAnswer')
    body.finish()

    expect(
      Array.from(output.children).map((child) =>
        child.classList.contains('term-line') ? child.textContent : child.className,
      ),
    ).toEqual(['reasoning', 'Answer'])
  })
})

describe('createAnswerBody fenced markdown and final rows', () => {
  it('keeps the owner’s markdown example as source when it is inside a markdown fence', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('```markdown\n# Heading\n| A | B |\n```')
    body.finish()

    const code = output.querySelector<HTMLElement>('.cmd-output-code')
    expect(code?.dataset.variant).toBe('answer')
    expect(code?.tabIndex).toBe(0)
    expect(code?.querySelector('[data-md]')).toBeNull()
    expect(code?.textContent).toContain('# Heading')
    expect(code?.textContent).toContain('| A | B |')
  })

  it('paints a non-newline-terminated final prose row before finishing', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('# Final heading')
    body.finish()

    const row = output.querySelector<HTMLElement>(':scope > .term-line')
    expect(row?.dataset.md).toBe('h1')
    expect(row?.textContent).toBe('Final heading')
  })

  it('processes a non-newline-terminated final code row inside its fence', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('```text\nfinal code')
    body.finish()

    const code = output.querySelector<HTMLElement>('.cmd-output-code')
    expect(code).not.toBeNull()
    expect(code?.querySelector<HTMLElement>(':scope > .term-line:last-child')?.textContent).toBe(
      'final code',
    )
  })

  it('closes a fence when its final delimiter has no trailing newline', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('```text\ncode\n```')
    body.finish()

    const code = output.querySelector<HTMLElement>('.cmd-output-code')
    const close = code?.querySelector<HTMLElement>('[data-fence-delim="close"]')
    expect(close).not.toBeNull()
    expect(close?.textContent).toBe('```')
  })

  it('never sends prose through the shell lexer', () => {
    const output = document.createElement('div')
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })

    body.append('Таблица: Аббревиатура **styled**\n')
    body.finish()

    const row = output.querySelector<HTMLElement>(':scope > .term-line')
    expect(row?.textContent).toBe('Таблица: Аббревиатура styled')
    expect(row?.querySelector('.ui-md-strong')).not.toBeNull()
    expect(row?.querySelector('[class^="tok-"]')).toBeNull()
  })
})
