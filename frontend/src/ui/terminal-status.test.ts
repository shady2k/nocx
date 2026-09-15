// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { createTerminalStatus, updateTerminalStatus } from './terminal-status'

describe('terminal status facts', () => {
  it('shows the launched shell name and preserves its full path in the title', () => {
    const el = createTerminalStatus({ shell: '/bin/bash', encoding: 'UTF-8' })
    expect(el.textContent).toBe('bashUTF-8')
    expect(el.firstElementChild?.getAttribute('title')).toBe('/bin/bash')
  })
  it('removes a prior shell name when the caller no longer has that fact', () => {
    const el = createTerminalStatus({ shell: '/bin/zsh', encoding: 'UTF-8' })
    updateTerminalStatus(el, { encoding: 'UTF-8' })
    expect(el.textContent).toBe('UTF-8')
    expect(el.firstElementChild?.getAttribute('title')).toBe('')
  })
})
