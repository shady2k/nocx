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
