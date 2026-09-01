// @vitest-environment jsdom
//
// What the lost-session card SAYS. The card had no unit coverage at all: the
// e2e spec asserts that it appears and that its title ends in "is gone", and
// the description — the whole reason this card has one — was asserted nowhere.
// That is how the remote sentence went on being shown to local panes.
import { cleanup } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountReconnectOffer, type ReconnectOfferProps } from './reconnect-offer'

const mounted: { dispose(): void }[] = []

afterEach(() => {
  while (mounted.length > 0) mounted.pop()!.dispose()
  cleanup()
})

function card(props: Partial<ReconnectOfferProps> = {}): HTMLElement {
  const target = document.createElement('div')
  document.body.append(target)
  mounted.push(
    mountReconnectOffer(target, {
      host: '',
      attempting: false,
      onReconnect: vi.fn(),
      ...props,
    }),
  )
  return target
}

function said(target: HTMLElement, part: 'title' | 'desc'): string {
  return target.querySelector(`.ui-status-card__${part}`)?.textContent ?? ''
}

describe('ReconnectOffer says what became of the old shell', () => {
  it('tells a REMOTE pane its work may still be running, and names the host', () => {
    const target = card({ host: 'prod' })

    // The far side is alive and the session ended between here and there, so
    // reading the scrollback as one continuous session is the mistake this
    // sentence exists to prevent.
    expect(said(target, 'title')).toBe('The connection to prod is gone')
    expect(said(target, 'desc')).toContain('may still be going on prod')
    expect(said(target, 'desc')).toContain('What it printed stays above.')
  })

  it('tells a LOCAL pane the shell ended, rather than inventing a host', () => {
    const target = card({ host: '' })

    // There is no far host. The shell was the backend's child; the PTY master
    // closed when the backend went, so it took a SIGHUP with it. The remote
    // sentence was shown here too, and promised a machine on the other end
    // that does not exist — reported after a dev-stand restart, where the
    // coordinator restarting IS the disconnection (nocx-ypbii).
    expect(said(target, 'title')).toBe('The connection is gone')
    const description = said(target, 'desc')
    expect(description).toContain('The old one ended with the backend')
    expect(description).toContain('anything it had detached may still be running')
    expect(description).toContain('What it printed stays above.')
    expect(description).not.toContain('on the host')
    expect(description).not.toContain('at the same endpoint')
  })

  it('keeps the spent-attempts prefix in front of either sentence', () => {
    const local = card({ host: '', attempt: { spent: 3, of: 3 } })
    const remote = card({ host: 'prod', attempt: { spent: 3, of: 3 } })

    expect(said(local, 'desc')).toMatch(/^Automatic attempts are spent\. /)
    expect(said(remote, 'desc')).toMatch(/^Automatic attempts are spent\. /)
  })

  it('says what is happening while an attempt is in flight, for either pane', () => {
    const counted = card({ host: 'prod', attempting: true, attempt: { spent: 2, of: 3 } })
    const uncounted = card({ host: '', attempting: true })

    // Mid-attempt there is nothing to say about the old shell yet, so both
    // panes get the same progress sentence.
    expect(said(counted, 'title')).toBe('Reconnecting…')
    expect(said(counted, 'desc')).toBe('Attempt 2 of 3.')
    expect(said(uncounted, 'desc')).toBe('Opening a new shell at the same endpoint.')
  })
})
