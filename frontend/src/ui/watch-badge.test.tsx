// @vitest-environment jsdom
// WatchBadge — one owner for "is this surface's view of a folder live".
//
// The behaviour it owns is a judgement, not a shape: `polling` on a LOCAL
// binding WITH a reason is a real degrade and gets a persistent badge; every
// other combination warns about nothing. Two surfaces answering that
// judgement separately is the shape AGENTS.md names — they agree everywhere
// anybody looks and disagree the day one of them is edited.
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { WatchBadge, type WatchBadgeProps } from './watch-badge'

afterEach(() => cleanup())

function subject(over: Partial<WatchBadgeProps> = {}) {
  const props: WatchBadgeProps = {
    testId: 'files-polling-badge',
    mode: 'polling',
    reason: 'inotify watch limit reached',
    local: true,
    ...over,
  }
  const { container } = render(() => <WatchBadge {...props} />)
  return {
    slot: container.querySelector<HTMLElement>('[data-testid="files-polling-badge-slot"]'),
    badge: container.querySelector<HTMLElement>('[data-testid="files-polling-badge"]'),
  }
}

describe('WatchBadge — the state carrier', () => {
  it('names itself and carries the established mode, which is what says files.watch returned', () => {
    const { slot } = subject()
    expect(slot).not.toBeNull()
    expect(slot?.classList.contains('ui-watch-badge')).toBe(true)
    expect(slot?.getAttribute('data-watch-mode')).toBe('polling')
  })

  it('carries no mode before the first answer, and is still on the page', () => {
    const { slot, badge } = subject({ mode: null, reason: null })
    expect(slot).not.toBeNull()
    expect(slot?.getAttribute('data-watch-mode')).toBeNull()
    expect(badge).toBeNull()
  })

  it('says watching when live notifications are established', () => {
    const { slot } = subject({ mode: 'watching', reason: null })
    expect(slot?.getAttribute('data-watch-mode')).toBe('watching')
  })
})

describe('WatchBadge — the warning', () => {
  it('warns, and the reason is reachable on the badge itself', () => {
    const { badge } = subject()
    expect(badge).not.toBeNull()
    expect(badge?.textContent).toBe('Polling')
    expect(badge?.getAttribute('title')).toBe('inotify watch limit reached')
    expect(badge?.getAttribute('data-tone')).toBe('warning')
  })

  it('clears the instant watching recovers', () => {
    const { badge } = subject({ mode: 'watching', reason: null })
    expect(badge).toBeNull()
  })

  it('stays silent for designed-mode polling — a reasonless fallback warns about nothing', () => {
    const { badge } = subject({ reason: null })
    expect(badge).toBeNull()
  })

  it('stays silent for a remote binding, whose designed mode IS polling', () => {
    const { badge } = subject({ local: false })
    expect(badge).toBeNull()
  })

  it('takes the testId prefix from its caller, so two surfaces are addressable apart', () => {
    const { container } = render(() => (
      <WatchBadge testId="api-polling-badge" mode="polling" reason="watcher refused" local />
    ))
    expect(container.querySelector('[data-testid="api-polling-badge-slot"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="api-polling-badge"]')).not.toBeNull()
  })
})
