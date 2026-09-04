// @vitest-environment jsdom
//
// FileReadout — one file, read: what it is, and its bytes or the reason they
// are not here.
//
// Every case here is read the way a person reads it — the sentence on screen,
// the bytes in the block — rather than through the props that produced it. The
// three refusals are the point of the component, so each is its own case: a
// viewer that went blank or drew a red box would pass a test that only asked
// "did it render", and would lie about what is on disk.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@solidjs/testing-library'
import { FileReadout } from './file-readout'
import type { FileReadoutOutcome } from './file-readout'

afterEach(cleanup)

const FACTS = [
  { name: 'Skill', value: 'skill-authoring' },
  { name: 'File', value: 'SKILL.md' },
  { name: 'Where it came from', value: 'builtin' },
]

const CSS = readFileSync(resolve(process.cwd(), 'src/styles/components/file-readout.css'), 'utf8')

function draw(outcome: FileReadoutOutcome): HTMLElement {
  const { container } = render(() => (
    <FileReadout facts={FACTS} ariaLabel="The whole of SKILL.md" outcome={outcome} />
  ))
  const el = container.querySelector<HTMLElement>('.ui-file-readout')
  if (!el) throw new Error('FileReadout rendered no ui-file-readout')
  return el
}

const bytesIn = (el: HTMLElement): string | null =>
  el.querySelector('.ui-code-block')?.textContent ?? null

const sentenceIn = (el: HTMLElement): string | null =>
  el.querySelector('.ui-status-card')?.textContent ?? null

describe('FileReadout', () => {
  it('shows the file whole, under the facts naming it', () => {
    const text = '# Skill authoring\n\nWrite the sentence a person could not say before.\n'
    const el = draw({ kind: 'text', text })

    expect(el.dataset.state).toBe('text')
    expect(bytesIn(el)).toBe(text)
    expect(el.querySelector('.ui-code-block')?.getAttribute('aria-label')).toBe(
      'The whole of SKILL.md',
    )
    // Nothing is refused, so nothing says anything was.
    expect(sentenceIn(el)).toBeNull()
    // The facts are the reader's answer to "which file am I looking at".
    expect(el.querySelector('.ui-fact-list')?.textContent).toContain('skill-authoring')
    expect(el.querySelector('.ui-fact-list')?.textContent).toContain('builtin')
  })

  // AN EMPTY FILE IS A FILE. Drawing nothing for it would be indistinguishable
  // from drawing nothing for a refusal, which is the confusion this component
  // exists to end.
  it('draws an empty file as an empty block, not as an absence', () => {
    const el = draw({ kind: 'text', text: '' })
    expect(el.dataset.state).toBe('text')
    expect(bytesIn(el)).toBe('')
  })

  it('says a file is not text, rather than showing an empty reader', () => {
    const el = draw({ kind: 'not-text' })

    expect(el.dataset.state).toBe('not-text')
    expect(bytesIn(el)).toBeNull()
    const said = sentenceIn(el) ?? ''
    expect(said).toContain('not text')
    // The file is there — this is a fact about it, not a failure of the read.
    expect(said).toContain('on disk')
    expect(el.querySelector('.ui-status-card')?.getAttribute('data-tone')).toBe('warning')
    // The facts stay: a person still has to know WHICH file this is about.
    expect(el.querySelector('.ui-fact-list')?.textContent).toContain('skill-authoring')
  })

  // THE BUDGET IS NAMED, in the person's units, because the sentence is about a
  // limit and a limit nobody can read is not a limit anybody can act on. The
  // number travels on the wire for exactly this reason, so the viewer must
  // spend it rather than keep a second copy.
  it('names the read budget when the file is larger than it', () => {
    const el = draw({ kind: 'too-large', maxBytes: 65536 })

    expect(el.dataset.state).toBe('too-large')
    expect(bytesIn(el)).toBeNull()
    const said = sentenceIn(el) ?? ''
    expect(said).toContain('65.5 kB')
    expect(said).toContain('on disk')
    expect(el.querySelector('.ui-status-card')?.getAttribute('data-tone')).toBe('warning')
  })

  // A REQUEST THAT WAS REFUSED IS A DIFFERENT SENTENCE, and a different tone.
  // The two above are true statements about a file that is there; this one says
  // there was nothing to describe, and the backend's own words are what say it.
  it('carries a refused read in the caller’s own sentence, at the danger tone', () => {
    const message = 'skill "deploy" path "SKILL.md": no such file or directory'
    const el = draw({ kind: 'unreadable', message })

    expect(el.dataset.state).toBe('unreadable')
    expect(bytesIn(el)).toBeNull()
    expect(sentenceIn(el)).toContain(message)
    expect(el.querySelector('.ui-status-card')?.getAttribute('data-tone')).toBe('danger')
  })

  // The identity is not decoration: the stylesheet keys off it, and a class
  // nothing styles is a hook that has stopped carrying the component's look.
  it('declares its identity in its own stylesheet', () => {
    expect(CSS).toContain('.ui-file-readout')
    expect(CSS).toMatch(/\.ui-file-readout\s*\{[^}]*flex-direction:\s*column;/s)
  })
})
