// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { grantBlockFromElement, grantBlockFromSelection } from './ask-entry'

const blockOf = (id: string, command: string, running = false) => {
  const block = document.createElement('div')
  block.className = running ? 'cmd-block cmd-block-running' : 'cmd-block'
  block.dataset.entryId = id
  const header = document.createElement('span')
  header.className = 'cmd-header-text'
  header.textContent = command
  const output = document.createElement('div')
  output.className = 'cmd-output'
  output.textContent = 'output'
  document.body.appendChild(block)
  block.append(header, output)
  return { block, output }
}

describe('whole-block grants', () => {
  it('derives the session.read item id, command, and running state from one block', () => {
    const { block } = blockOf('item-7', 'git status', true)
    const grant = grantBlockFromElement(block)
    expect(grant).toEqual({
      itemId: 'item-7',
      blockEl: block,
      command: 'git status',
      state: 'running',
    })
  })
  it('does not treat a renderer-local block counter as a grant identity', () => {
    const { block } = blockOf('entry-ignored', 'git status')
    delete block.dataset.entryId
    block.dataset.blockId = '3'
    expect(grantBlockFromElement(block)).toBeNull()
  })

  it('marks the whole containing block for a non-collapsed selection', () => {
    const { block, output } = blockOf('item-8', 'npm test')
    const text = output.firstChild!
    const range = document.createRange()
    range.setStart(text, 0)
    range.setEnd(text, text.textContent!.length)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)

    const grant = grantBlockFromSelection(selection)
    expect(grant?.itemId).toBe('item-8')
    expect(grant?.blockEl).toBe(block)
    expect(Object.keys(grant ?? {}).sort()).toEqual(['blockEl', 'command', 'itemId', 'state'])
  })

  it('marks one block when selection crosses answer prose into a nested fenced row', () => {
    const { block, output } = blockOf('item-9', 'explain the failure')
    output.replaceChildren()
    const prose = document.createElement('div')
    prose.className = 'cmd-answer-body'
    prose.textContent = 'The command failed because the file was absent.'
    const fence = document.createElement('div')
    fence.className = 'cmd-output-code'
    const code = document.createElement('span')
    code.className = 'term-line'
    code.textContent = 'cat missing.txt'
    fence.appendChild(code)
    output.append(prose, fence)

    const range = document.createRange()
    range.setStart(prose.firstChild!, 4)
    range.setEnd(code.firstChild!, code.textContent.length)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)

    expect(grantBlockFromSelection(selection)?.itemId).toBe('item-9')
    expect(grantBlockFromSelection(selection)?.blockEl).toBe(block)
  })

  it('refuses a selection crossing blocks', () => {
    const first = blockOf('item-1', 'git status')
    const second = blockOf('item-2', 'npm test')
    const root = document.createElement('div')
    root.append(first.block, second.block)
    const range = document.createRange()
    range.setStart(first.output.firstChild!, 0)
    range.setEnd(second.output.firstChild!, 3)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    expect(grantBlockFromSelection(selection)).toBeNull()
  })
})
