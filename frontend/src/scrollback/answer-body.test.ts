// @vitest-environment jsdom

import { describe, expect, it } from 'vitest'
import { CommandSnapshotStore } from '../command-snapshot'
import { createAnswerBody } from './answer-body'

function renderRows(chunks: string[]): string[] {
  const output = document.createElement('div')
  const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
  for (const chunk of chunks) body.append(chunk)
  body.finish()
  return Array.from(output.querySelectorAll<HTMLElement>(':scope > .term-line')).map(
    (row) => row.textContent ?? '',
  )
}

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
