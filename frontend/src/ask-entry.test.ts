// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { EditorState } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import {
  grantBlockFromElement,
  grantBlockFromSelection,
  TARGET_MENU_ITEMS,
  TargetIndicator,
} from './ask-entry'
import { createAnswerBody } from './scrollback/answer-body'
import { CommandSnapshotStore } from './command-snapshot'

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
  it('uses the ask question when an empty recorded command is present', () => {
    const { block } = blockOf('answer-1', 'what does this do?')
    block.dataset.blockKind = 'ask'
    block.dataset.recordedCommand = ''

    expect(grantBlockFromElement(block)?.command).toBe('what does this do?')
  })

  it('gives an empty command an explicit label', () => {
    const { block } = blockOf('empty-1', '')
    block.dataset.recordedCommand = ''

    expect(grantBlockFromElement(block)?.command).toBe('(empty command)')
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
  it('carries the selected output row window instead of a whole-block mark', () => {
    const { output } = blockOf('item-10', 'npm test')
    output.replaceChildren()
    for (const text of ['first', 'second', 'third']) {
      const row = document.createElement('span')
      row.className = 'term-line'
      row.textContent = text
      output.appendChild(row)
    }
    const rows = output.querySelectorAll<HTMLElement>('.term-line')
    const range = document.createRange()
    range.setStart(rows[0].firstChild!, 0)
    range.setEnd(rows[1].firstChild!, rows[1].textContent.length)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)

    expect(grantBlockFromSelection(selection)).toMatchObject({
      itemId: 'item-10',
      start: 0,
      count: 2,
    })
    expect(grantBlockFromSelection(selection)).not.toHaveProperty('rowStart')
    expect(grantBlockFromSelection(selection)).not.toHaveProperty('rowEnd')
  })

  it('selects streamed table rows through DOM ranges, not row geometry', () => {
    const { output } = blockOf('answer-table', 'compare values')
    output.replaceChildren()
    const body = createAnswerBody(output, { store: new CommandSnapshotStore() })
    body.append('| Name | Age |\n|---|---:\n| Ada | 37 |')
    body.finish()

    const rows = output.querySelectorAll<HTMLElement>('.term-line')
    const range = document.createRange()
    range.setStart(rows[0], 0)
    range.setEnd(rows[1], rows[1].childNodes.length)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)

    expect(grantBlockFromSelection(selection)).toMatchObject({
      itemId: 'answer-table',
      start: 0,
      count: 2,
    })
  })

  it('collapses a selection covering every output row into a whole-block mark', () => {
    // One movement across the whole output IS the block (nocx-5u3oz.16). A
    // window is a window only when it is a genuine subset; otherwise "select
    // everything" and "select the block" would be different things behind the
    // same gesture.
    const { block, output } = blockOf('item-12', 'npm test')
    output.replaceChildren()
    for (const text of ['first', 'second', 'third']) {
      const row = document.createElement('span')
      row.className = 'term-line'
      row.textContent = text
      output.appendChild(row)
    }
    const rows = output.querySelectorAll<HTMLElement>('.term-line')
    const range = document.createRange()
    range.setStart(rows[0].firstChild!, 0)
    range.setEnd(rows[2].firstChild!, rows[2].textContent.length)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)

    const grant = grantBlockFromSelection(selection)
    expect(grant?.blockEl).toBe(block)
    expect(Object.keys(grant ?? {}).sort()).toEqual(['blockEl', 'command', 'itemId', 'state'])
  })

  it('keeps the whole-block mark without a line window', () => {
    const { block } = blockOf('item-11', 'pwd')
    const grant = grantBlockFromElement(block)

    expect(grant).not.toHaveProperty('start')
    expect(grant).not.toHaveProperty('count')
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

describe('marks inside the frozen screen (nocx-hp8p2.7)', () => {
  function frameOf(rows: string[]): HTMLElement {
    const frame = document.createElement('div')
    frame.className = 'nocx-freeze-frame'
    for (const text of rows) {
      const row = document.createElement('div')
      row.className = 'nocx-freeze-frame__row'
      row.textContent = text
      frame.appendChild(row)
    }
    document.body.appendChild(frame)
    return frame
  }

  function selectRows(frame: HTMLElement, from: number, to: number): Selection {
    const rows = frame.querySelectorAll<HTMLElement>('.nocx-freeze-frame__row')
    const range = document.createRange()
    range.setStart(rows[from].firstChild!, 0)
    range.setEnd(rows[to].firstChild!, rows[to].textContent.length)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    return selection
  }

  const automatic = (blockEl: HTMLElement) =>
    ({
      itemId: 'att-screen',
      blockEl,
      command: 'top',
      state: 'running',
      automatic: true,
    }) as const

  it('marks the selected rows on the automatic attachment, not a second item', () => {
    const { block } = blockOf('item-20', 'top', true)
    const frame = frameOf(['load 1.00', 'PID USER', 'nocx  0.1', 'idle'])
    const grant = grantBlockFromSelection(selectRows(frame, 1, 2), automatic(block))
    expect(grant).toEqual({
      // The automatic attachment's OWN id, so the model reads a band of the
      // pinned frame rather than a second whole screen.
      itemId: 'att-screen',
      blockEl: frame,
      command: 'top',
      state: 'running',
      start: 1,
      count: 2,
    })
    // Not an automatic attachment any more: it is a person mark and counts.
    expect(grant).not.toHaveProperty('automatic')
  })

  it('collapses a selection covering the whole screen — that is the attachment already', () => {
    const { block } = blockOf('item-21', 'top', true)
    const frame = frameOf(['a', 'b', 'c'])
    expect(grantBlockFromSelection(selectRows(frame, 0, 2), automatic(block))).toBeNull()
  })

  it('offers nothing when no frozen screen is attached', () => {
    const frame = frameOf(['a', 'b', 'c'])
    expect(grantBlockFromSelection(selectRows(frame, 0, 1), null)).toBeNull()
  })
})

describe('the target menu (spec 2026-09-15 §6)', () => {
  it('lists one row per entry this module already knows, by its presentation word', () => {
    // TARGET_PRESENTATION is the indicator's own vocabulary — the module
    // has no live handle on InputTargetRegistry, only the toggle it is
    // handed — so this is "the registry the gutter already knows" the spec
    // names, table-driven and exported so it is a plain unit under test.
    expect(TARGET_MENU_ITEMS).toEqual([
      { targetId: 'shell', word: 'Run' },
      { targetId: 'agent', word: 'Ask' },
    ])
  })
})

describe('TargetIndicator mounts beside CM6, not in a CM6 gutter (spec 2026-09-15 §4)', () => {
  /** A minimal stand-in for the slice of ComposerFrame this indicator cares
   *  about: a field with CM6's mount parent already inside it, exactly the
   *  shape editor.ts builds before constructing the EditorView (composer-
   *  frame.ts). */
  function fieldStand(): { field: HTMLElement; editorSlot: HTMLElement; submitSlot: HTMLElement } {
    const field = document.createElement('div')
    field.className = 'ui-composer-frame__field'
    const editorSlot = document.createElement('div')
    editorSlot.className = 'ui-composer-frame__editor'
    const submitSlot = document.createElement('div')
    submitSlot.className = 'ui-composer-frame__submit'
    field.append(editorSlot, submitSlot)
    document.body.appendChild(field)
    return { field, editorSlot, submitSlot }
  }

  it('mounts as the field’s leading child, ahead of CM6’s own editor slot', () => {
    const { field, editorSlot, submitSlot } = fieldStand()
    const indicator = new TargetIndicator(() => {})
    const view = new EditorView({
      state: EditorState.create({ extensions: [indicator.extension()] }),
      parent: editorSlot,
    })

    const button = field.firstElementChild as HTMLButtonElement
    expect(button.classList.contains('ui-mode-indicator')).toBe(true)
    expect(button.dataset.variant).toBe('field')
    expect(button.textContent).toBe('Run')
    expect([...field.children]).toEqual([button, editorSlot, submitSlot])

    view.destroy()
    field.remove()
  })

  it('destroying the CM6 view unmounts the control — no orphan left in a field it no longer owns', () => {
    const { field, editorSlot } = fieldStand()
    const indicator = new TargetIndicator(() => {})
    const view = new EditorView({
      state: EditorState.create({ extensions: [indicator.extension()] }),
      parent: editorSlot,
    })
    expect(field.querySelector('.ui-mode-indicator')).not.toBeNull()
    view.destroy()
    expect(field.querySelector('.ui-mode-indicator')).toBeNull()
    field.remove()
  })

  it('set() repaints the SAME leading position — no duplicate, no reorder', () => {
    const { field, editorSlot, submitSlot } = fieldStand()
    const indicator = new TargetIndicator(() => {})
    const view = new EditorView({
      state: EditorState.create({ extensions: [indicator.extension()] }),
      parent: editorSlot,
    })

    indicator.set('agent', 'Agent')
    expect(field.children.length).toBe(3)
    const button = field.firstElementChild as HTMLButtonElement
    expect(button.textContent).toBe('Ask')
    expect(button.dataset.target).toBe('agent')
    expect([...field.children]).toEqual([button, editorSlot, submitSlot])

    view.destroy()
    field.remove()
  })

  it('picking a different row calls toggle(); picking the already-active row does not', () => {
    const { editorSlot } = fieldStand()
    const toggle = vi.fn()
    const indicator = new TargetIndicator(toggle)
    const view = new EditorView({
      state: EditorState.create({ extensions: [indicator.extension()] }),
      parent: editorSlot,
    })
    const button = editorSlot.parentElement!.firstElementChild as HTMLButtonElement
    button.click()
    const ask = [...document.querySelectorAll('[role="menuitem"]')].find(
      (i) => i.textContent === 'Ask',
    ) as HTMLButtonElement
    ask.click()
    expect(toggle).toHaveBeenCalledTimes(1)

    view.destroy()
    editorSlot.parentElement?.remove()
  })

  it('with no ComposerFrame field as an ancestor, the extension mounts nothing (defensive, never throws)', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const indicator = new TargetIndicator(() => {})
    expect(() => {
      const view = new EditorView({
        state: EditorState.create({ extensions: [indicator.extension()] }),
        parent: container,
      })
      view.destroy()
    }).not.toThrow()
    expect(container.querySelector('.ui-mode-indicator')).toBeNull()
    container.remove()
  })
})
