// @vitest-environment jsdom
/**
 * i18n tests for the approval question (nocx-dej6.3.8's review, defect 1).
 *
 * The surface built its title map and its scope list as module-level
 * literals, so every string in them was resolved once — while the module was
 * imported, which is before main.tsx bootstraps the locale from the persisted
 * setting. A window that starts in Russian therefore showed an English
 * question, and the only way to see it is to switch the catalogue BEFORE the
 * first render, which is what these tests do.
 */
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { cleanup, render } from '@solidjs/testing-library'
import { AgentApprovalPrompt } from './agent-approval-prompt'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import { setActiveLocale } from './i18n/locale-model'

const ASK: AgentApprovalRequested = {
  runId: '7',
  attempt: 1,
  tool: 'files.read',
  callId: 'call_1',
  argHash: 'hash-a',
  arguments: '{"path":"/repo/a.txt"}',
  reason: 'policy',
  effect: 'observe',
  standing: { available: true, rule: 'df -h', reason: '' },
  resource: { kind: 'path', id: '/repo/a.txt' },
}

function renderPrompt() {
  return render(() => <AgentApprovalPrompt open={true} ask={ASK} busy={false} onDecide={vi.fn()} />)
}

beforeEach(() => setActiveLocale('en'))
afterEach(() => {
  cleanup()
  setActiveLocale('en')
})

describe('the approval question is resolved in the language it is asked in', () => {
  it('names itself and its widths in Russian on a Russian start', () => {
    // A Russian start: the catalogue is switched before the first render, the
    // way main.tsx switches it before mounting anything.
    setActiveLocale('ru')
    const { container } = renderPrompt()
    expect(container.querySelector('.ui-prompt__title')?.textContent).toBe(
      'Это действие требует вашего согласия',
    )
    // The widths come from the same list: `once` first, then the standing one.
    const buttons = Array.from(container.querySelectorAll('button')).map((b) => b.textContent ?? '')
    expect(buttons.some((b) => b.includes('один раз'))).toBe(true)
    expect(buttons.some((b) => b.includes('в этом сеансе'))).toBe(true)
    expect(buttons.some((b) => b.includes('always'))).toBe(false)
  })

  it('and in English under English', () => {
    const { container } = renderPrompt()
    expect(container.querySelector('.ui-prompt__title')?.textContent).toBe(
      'This action needs your approval',
    )
    const buttons = Array.from(container.querySelectorAll('button')).map((b) => b.textContent ?? '')
    expect(buttons.some((b) => b.includes('once'))).toBe(true)
  })
})
