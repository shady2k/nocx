// @vitest-environment jsdom
//
// A turn reads in the order it happened (ADR-0040), asserted on the LIVE
// path — the scrollback the ask surface actually drives.
//
// The owner's report, twice. First: he asked how much disk was free and got,
// in this order, the question, a bare `▸ run` line carrying neither arguments
// nor result, the finished answer, and THEN — below the whole turn — the
// `df -h` block with the twelve lines the answer was distilled from. One
// causal sequence drawn as a different one. Then, once the block had been
// moved into place: a turn that ran commands drew as several top-level
// blocks, each repeating the question in its header under a `continued`
// badge. Then, in the same week: four calls drawn as `readScreen home/dev`,
// `blocks.list home/dev`, `blocks.read home/dev`, `blocks.read home/dev` —
// two reads of different finished commands, indistinguishable.
//
// Every assertion here reads DOCUMENT ORDER and the text a person actually
// reads, because that is the claim the product is making.

import { describe, it, expect } from 'vitest'
import { BlockManager } from './blocks'
import { CommandSnapshotStore } from '../command-snapshot'

function newManager(sessionName?: (id: string) => string | null) {
  const inner = document.createElement('div')
  document.body.appendChild(inner)
  const xtermContainer = document.createElement('div')
  inner.appendChild(xtermContainer)
  const manager = new BlockManager(inner, xtermContainer, {
    snapshotStore: new CommandSnapshotStore(),
    sessionName,
  })
  return { inner, manager }
}

/** Every TOP-LEVEL block in the scrollback, as "kind:header". */
function topLevel(root: HTMLElement): string[] {
  return Array.from(root.children)
    .filter((c): c is HTMLElement => c.classList.contains('cmd-block'))
    .map((b) => `${b.dataset.blockKind ?? 'command'}:${headerOf(b)}`)
}

function headerOf(el: Element): string {
  return el.querySelector(':scope > .cmd-header .cmd-header-text')?.textContent ?? ''
}

/**
 * The turn's children, in DOM order, as "kind:what a person reads".
 *
 * A prose child is read from its body, a tool child from its header, a
 * command child from its header — which is exactly how a person meets them.
 */
function childrenOf(turn: HTMLElement): string[] {
  const box = turn.querySelector(':scope > .cmd-children')
  return Array.from(box?.children ?? []).map((c) => {
    const el = c as HTMLElement
    if (!el.classList.contains('cmd-block')) return el.className
    const kind = el.dataset.blockKind ?? 'command'
    if (kind === 'text') {
      const rows = Array.from(el.querySelectorAll('.term-line')).map((r) => r.textContent ?? '')
      return `text:${rows.join('\n')}`
    }
    return `${kind}:${headerOf(el)}`
  })
}

const QUESTION = 'Как мне проверить сколько места на диске?'
const COMMAND = 'df -h'
const SESSION = '9bb9a7602c27e8ba0741972c7049b54b'

describe('a turn draws the blocks it caused, in order', () => {
  // ── acceptance 1 ────────────────────────────────────────────────────────
  it('prose, the command block with its output, then the prose written from it', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.el.dataset.entryId = 'turn-1'
    turn.append('Сейчас посмотрю.', 'text-1')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    // The command really runs, through the ordinary path a person's line takes.
    const rec = manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    rec.el.appendChild(
      Object.assign(document.createElement('div'), {
        className: 'cmd-output',
        textContent: '41G free',
      }),
    )
    turn.append('41G свободно, занято 79%', 'text-2')
    turn.close('success')

    // ONE top-level block: the turn. The command is inside it.
    expect(topLevel(inner)).toEqual([`ask:${QUESTION}`])
    expect(childrenOf(turn.el)).toEqual([
      'text:Сейчас посмотрю.',
      `command:${COMMAND}`,
      'text:41G свободно, занято 79%',
    ])
    // And the command's real output is in the command's own block.
    const cmd = turn.el.querySelectorAll<HTMLElement>('.cmd-block')[1]
    expect(cmd.querySelector('.cmd-output')?.textContent).toBe('41G free')
  })

  // ── acceptance 2 — the case this whole epic started from ────────────────
  it('two calls to one tool with different arguments read differently', () => {
    const { manager } = newManager(() => 'home/dev')
    const turn = manager.addAnswerBlock('what have I been doing?', '/repo')
    for (const blockId of ['3', '4']) {
      turn.toolCall({
        callId: `c${blockId}`,
        tool: 'blocks.read',
        effect: 'observe',
        opensBlock: false,
        args: { sessionId: SESSION, blockId },
        resource: { kind: 'session', id: SESSION },
      })
    }
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual([
      'tool:blocks.read sessionId=home/dev blockId=3',
      'tool:blocks.read sessionId=home/dev blockId=4',
    ])
  })

  it('a run of calls is a run of blocks — five calls are five, never a summary', () => {
    const { manager } = newManager(() => 'home/dev')
    const turn = manager.addAnswerBlock('what have I been doing?', '/repo')
    for (const tool of ['readScreen', 'blocks.list', 'blocks.read', 'files.read', 'git.status']) {
      turn.toolCall({
        callId: tool,
        tool,
        effect: 'observe',
        opensBlock: false,
        args: { sessionId: SESSION },
        resource: { kind: 'session', id: SESSION },
      })
    }
    turn.append('you have been reading logs')
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual([
      'tool:readScreen sessionId=home/dev',
      'tool:blocks.list sessionId=home/dev',
      'tool:blocks.read sessionId=home/dev',
      'tool:files.read sessionId=home/dev',
      'tool:git.status sessionId=home/dev',
      'text:you have been reading logs',
    ])
  })

  // ── acceptance 3 ────────────────────────────────────────────────────────
  it("the run's command block is a child of its turn and keeps everything a block has", () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    const rec = manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.append('41G free')
    turn.close('success')

    const box = turn.el.querySelector(':scope > .cmd-children')!
    expect(box.contains(rec.el)).toBe(true)
    // Selection keeps its renderer-local numeric id internally; no such
    // counter is a backend identity.
    expect(rec.el.dataset.blockId).toBeUndefined()
    expect(rec.el.dataset.entryId).toBeUndefined()
    expect(rec.id).not.toBe(turn.id)
    // Who ran it: the assistant, said out loud.
    expect(rec.el.querySelector('.ui-badge[data-author="agent"]')?.textContent).toBe('agent')
    // Its own ⋮.
    expect(rec.el.querySelector(':scope > .cmd-header .cmd-overflow-btn')).not.toBeNull()

    // And it freezes with its own exit status, in place.
    const frozen = manager.freezeBlock(() => undefined, 0, 3)
    expect(frozen).not.toBeNull()
    const cmd = box.querySelector<HTMLElement>('.cmd-block[data-block-kind="command"]')!
    expect(cmd.querySelector('.cmd-header-exit')?.textContent).toBe('exit 3')
    // Selecting it selects IT, not the turn that contains it.
    cmd.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(inner.querySelectorAll('.cmd-block-selected')).toHaveLength(1)
    expect(cmd.classList.contains('cmd-block-selected')).toBe(true)
  })

  it('a run of prose is a real block: its own id, its own selection, no header', () => {
    // ADR-0040: the header is simply not drawn — there is nothing to name it,
    // because the intent was the question. Everything else a block has, it
    // has.
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.append('first', 'text-1')
    turn.toolCall({ callId: 'c1', tool: 'files.read', effect: 'observe', opensBlock: false })
    turn.append('second', 'text-2')
    turn.close('success')

    const prose = Array.from(
      turn.el.querySelectorAll<HTMLElement>('.cmd-block[data-block-kind="text"]'),
    )
    expect(prose).toHaveLength(2)
    const ids = prose.map((p) => p.dataset.entryId)
    expect(ids).toEqual(['text-1', 'text-2'])
    expect(prose.every((p) => p.dataset.blockId === undefined)).toBe(true)
    // No header, and therefore no ⋮ and no chips of its own.
    for (const p of prose) expect(p.querySelector('.cmd-header')).toBeNull()
    // And it selects, on its own, like any other block.
    prose[1].dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(inner.querySelectorAll('.cmd-block-selected')).toHaveLength(1)
    expect(prose[1].classList.contains('cmd-block-selected')).toBe(true)
  })

  // ── acceptance 4 ────────────────────────────────────────────────────────
  it('the question appears exactly once — no fragment, no continued badge', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.el.dataset.entryId = 'turn-1'
    turn.append('first', 'text-1')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.append('second', 'text-2')
    turn.close('success')

    const headers = Array.from(inner.querySelectorAll('.cmd-header-text')).map(
      (h) => h.textContent ?? '',
    )
    expect(headers.filter((h) => h === QUESTION)).toHaveLength(1)
    expect(inner.querySelector('[data-turn-continuation]')).toBeNull()
    expect(inner.querySelector('[data-turn-fragment]')).toBeNull()
    // Both runs of prose are there, in order, as blocks of their own.
    expect(childrenOf(turn.el)).toEqual(['text:first', `command:${COMMAND}`, 'text:second'])
  })

  it('the command text appears exactly once in the whole scrollback', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.append('41G free')
    turn.close('success')

    const mentions = Array.from(inner.querySelectorAll('.cmd-header-text')).filter((h) =>
      (h.textContent ?? '').includes(COMMAND),
    )
    expect(mentions).toHaveLength(1)
  })

  // ── acceptance 5 ────────────────────────────────────────────────────────
  it('a turn that made no calls is a question and its prose, one block', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock('who are you?', '/repo')
    turn.append('an assistant', 'text-1')
    turn.close('success', undefined, 'a-model')

    expect(topLevel(inner)).toEqual(['ask:who are you?'])
    expect(childrenOf(turn.el)).toEqual(['text:an assistant', 'cmd-answer-provenance'])
    expect(turn.el.querySelector('.cmd-header-exit')?.textContent).toBe('completed')
    expect(inner.querySelector('.cmd-answer-typing')).toBeNull()
    expect(inner.querySelector('.cmd-answer-waiting')).toBeNull()
  })

  // ── acceptance 6 ────────────────────────────────────────────────────────
  it('a call announced twice renders once', () => {
    const { manager } = newManager(() => 'home/dev')
    const turn = manager.addAnswerBlock('read it', '/repo')
    const call = {
      callId: 'c1',
      tool: 'files.read',
      effect: 'observe' as const,
      args: { path: '/repo/a.txt' },
      opensBlock: false,
    }
    turn.toolCall(call)
    // The approved egress resume puts the same call through the pipeline
    // again, and the backend announces the same callId.
    turn.toolCall(call)
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual(['tool:files.read path=/repo/a.txt'])
  })

  it('a run announced twice adopts one block, not two', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    const call = {
      callId: 'c1',
      tool: 'run',
      effect: 'mutate-destructive' as const,
      opensBlock: true,
    }
    turn.toolCall(call)
    turn.toolCall(call)
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual([`command:${COMMAND}`])
    expect(topLevel(inner)).toEqual([`ask:${QUESTION}`])
  })

  // ── acceptance 7 ────────────────────────────────────────────────────────
  it('a session no pane can name does not print the id', () => {
    const { manager } = newManager(() => null)
    const turn = manager.addAnswerBlock('what is on screen?', '/repo')
    turn.toolCall({
      callId: 'c1',
      tool: 'readScreen',
      effect: 'observe',
      opensBlock: false,
      args: { sessionId: SESSION },
      resource: { kind: 'session', id: SESSION },
    })
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual(['tool:readScreen'])
    expect(turn.el.textContent).not.toContain(SESSION)
  })

  it('a window with no pane list at all still never prints the id', () => {
    const { manager } = newManager()
    const turn = manager.addAnswerBlock('what is on screen?', '/repo')
    turn.toolCall({
      callId: 'c1',
      tool: 'blocks.read',
      effect: 'observe',
      opensBlock: false,
      args: { sessionId: SESSION, blockId: '7' },
      resource: { kind: 'session', id: SESSION },
    })
    turn.close('success')

    // The other arguments survive: what is dropped is the unnameable
    // session, not the call.
    expect(childrenOf(turn.el)).toEqual(['tool:blocks.read blockId=7'])
    expect(turn.el.textContent).not.toContain(SESSION)
  })

  // ── the boundary is the backend's ───────────────────────────────────────
  it('the backend decides where one run of prose ends: chunks of one block stay one block', () => {
    const { manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.append('one ', 'text-1')
    turn.append('sentence', 'text-1')
    turn.append('a second run', 'text-2')
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual(['text:one sentence', 'text:a second run'])
  })

  it('text written before a call stays above the block; text written after lands below it', () => {
    const { manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.append('before the call', 'text-1')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.append('while it was still running', 'text-2')
    turn.append(' and after', 'text-2')
    turn.close('success')

    expect(childrenOf(turn.el)).toEqual([
      'text:before the call',
      `command:${COMMAND}`,
      'text:while it was still running and after',
    ])
  })

  it('the effect rides the wire onto the block — never derived from the tool name', () => {
    const { manager } = newManager()
    const turn = manager.addAnswerBlock('delete it', '/repo')
    turn.toolCall({
      callId: 'c1',
      tool: 'files.read',
      effect: 'mutate-destructive',
      args: { path: '/repo/a.txt' },
      opensBlock: false,
    })
    turn.close('success')

    const call = turn.el.querySelector<HTMLElement>('.cmd-block[data-block-kind="tool"]')!
    expect(call.dataset.effect).toBe('mutate-destructive')
  })

  it('the typing dots do not outlive a turn whose first content was a call', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.append('41G free')
    turn.close('success')
    // The TURN's dots are gone with the turn. The query is scoped to the
    // turn: since nocx-vnirv.1 a still-RUNNING command (this sequence
    // closes the turn while its run call's block is live) carries its own
    // stand-in in the live region, which also lives under `inner`.
    expect(turn.el.querySelector('.cmd-answer-typing')).toBeNull()
    expect(inner.querySelector('.cmd-answer-waiting')).toBeNull()
  })

  it('a run that never reached a command does not adopt the next block a person opens', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    // The call was announced and the command never arrived — the run failed
    // between the two. The turn closes, and the claim dies with it.
    turn.close('failure', 'the run tool could not reach the session')
    manager.startBlock('ls', '/repo', 0, 0, 'shell')

    expect(topLevel(inner)).toEqual([`ask:${QUESTION}`, 'command:ls'])
    expect(childrenOf(turn.el)).toEqual(['cmd-answer-error'])
  })

  it('clearing the scrollback takes a turn and everything inside it', () => {
    const { inner, manager } = newManager()
    const turn = manager.addAnswerBlock(QUESTION, '/repo')
    turn.append('some prose', 'text-1')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    turn.close('success')

    manager.clearAll()
    expect(inner.querySelectorAll('.cmd-block')).toHaveLength(0)
  })
})
