// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SessionPublishFailed } from './generated/session.publishFailed'
import {
  PUBLISH_FAILURE_REASONS,
  publishNoticeTitle,
  mountPublishNotice,
  publishFailureSentence,
  subscribePublishFailed,
  type PublishFailureReason,
  type PublishNoticeHandle,
} from './publish-notice'

let handle: PublishNoticeHandle | null = null
let pane: HTMLElement | null = null

afterEach(() => {
  handle?.dispose()
  handle = null
  pane?.remove()
  pane = null
})

function mount(reason: PublishFailureReason = 'write-refused') {
  pane = document.createElement('div')
  const terminal = document.createElement('div')
  terminal.className = 'scrollback-layout'
  pane.appendChild(terminal)
  document.body.appendChild(pane)
  handle = mountPublishNotice(pane, reason, vi.fn())
  return { host: pane, terminal }
}

const fact = (reason: PublishFailureReason): SessionPublishFailed => ({
  sessionId: 'sid-1',
  instanceId: 'instance-1',
  sessionEpoch: 1,
  reason,
})

describe('the publish-failure notice (nocx-1kejz.4)', () => {
  // Rule 1 of AGENTS.md's testing section: the assertion is about what a person
  // can see and do, not about which component was called. The card is the kit's,
  // placed in the pane's flow above the terminal, and it says what failed AND
  // what the tab is doing instead.
  it('places one kit StatusCard above the terminal, naming the cause and the outcome', () => {
    const { host, terminal } = mount('file-transfer-unavailable')

    const holder = host.querySelector('.nocx-publish-notice')
    expect(holder).not.toBeNull()
    expect(holder!.querySelectorAll('.ui-status-card')).toHaveLength(1)
    expect(holder!.querySelector('.ui-status-card__title')?.textContent).toBe(publishNoticeTitle())
    const description = holder!.querySelector('.ui-status-card__desc')?.textContent ?? ''
    expect(description).toContain('file-transfer channel')
    // …and what the pane is doing instead, which is the half a person cannot
    // see for themselves: the tab looks the same either way.
    expect(description).toContain('whatever copy of the integration the host already had')
    // In the flow, ahead of the terminal — a message about the session must
    // not hide the session.
    expect(
      holder!.compareDocumentPosition(terminal) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('stays ONE card when a later attempt fails for a different cause', () => {
    const { host } = mount('write-refused')
    const first = host.querySelector('.nocx-publish-notice .ui-status-card')
    expect(first!.textContent).toContain('refused the files')

    // A host that refuses every copy produces one failure per attempt. The
    // statement is reworded, never stacked (the bound nocx-z5lnt set).
    for (const reason of ['home-unavailable', 'connection-failed'] as const) {
      handle!.update(reason)
    }

    expect(host.querySelectorAll('.nocx-publish-notice')).toHaveLength(1)
    expect(host.querySelectorAll('.ui-status-card')).toHaveLength(1)
    const description = host.querySelector('.ui-status-card__desc')?.textContent ?? ''
    expect(description).toContain('did not stay up')
    expect(description).not.toContain('refused the files')
  })

  it('is dismissible, and dismissal touches nothing in the terminal', () => {
    pane = document.createElement('div')
    const terminal = document.createElement('div')
    terminal.textContent = 'the shell is still usable'
    pane.appendChild(terminal)
    document.body.appendChild(pane)
    const onDismiss = vi.fn()
    handle = mountPublishNotice(pane, 'write-refused', onDismiss)

    pane.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')?.click()

    expect(onDismiss).toHaveBeenCalledOnce()
    expect(terminal.textContent).toBe('the shell is still usable')
  })

  // The vocabulary is the contract's, read through the generated type: CAUSES
  // is a Record over it, so a member with no sentence is a type error, and this
  // walks the members the wire can carry so an empty table cannot pass.
  it('has a sentence for every reason the wire can carry', () => {
    expect(PUBLISH_FAILURE_REASONS.length).toBeGreaterThanOrEqual(5)
    expect(PUBLISH_FAILURE_REASONS).toContain('unknown')
    for (const reason of PUBLISH_FAILURE_REASONS) {
      const sentence = publishFailureSentence(reason)
      // Cause AND outcome, not a restatement of the reason's own name.
      expect(sentence.length, `${reason} says too little`).toBeGreaterThan(80)
      expect(sentence).toContain('whatever copy of the integration the host already had')
      // The raw error never reaches the pane (nocx-viil): no status codes, no
      // paths, nothing that reads like a backend message.
      expect(sentence.toLowerCase(), `${reason} quotes the backend`).not.toContain('sftp')
      expect(sentence, `${reason} carries a path`).not.toContain('/')
    }
  })

  it('refuses a payload that is not this fact, including a reason it has no words for', () => {
    const subs = new Map<string, (params: unknown) => void>()
    const dispatcher = {
      subscribe: (method: string, handler: (params: unknown) => void) => {
        subs.set(method, handler)
        return () => subs.delete(method)
      },
    }
    const seen: SessionPublishFailed[] = []
    subscribePublishFailed(dispatcher as never, (f) => seen.push(f))
    const deliver = (params: unknown) => subs.get('session.publishFailed')?.(params)

    deliver(fact('write-refused'))
    expect(seen).toEqual([fact('write-refused')])

    for (const broken of [
      {},
      { sessionId: 'sid-1' },
      { sessionId: 'sid-1', instanceId: 'instance-1' },
      // A reason from a NEWER build: refused rather than drawn as a card whose
      // sentence would be "undefined".
      { ...fact('write-refused'), reason: 'quota-exceeded' },
      { ...fact('write-refused'), sessionEpoch: '1' },
      null,
      42,
    ]) {
      deliver(broken)
    }
    expect(seen).toHaveLength(1)
  })
})
