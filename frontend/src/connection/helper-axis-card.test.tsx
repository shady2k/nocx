// @vitest-environment jsdom
//
// The shared helper-axis card (connection/helper-axis-card.tsx). Two panels
// draw it — Git and Files — so what it draws has to be the ONE fold's answer,
// whichever surface asks: a reason the wire can send draws its own sentence, an
// action is offered only where the backend offered it, and no wire token reaches
// a person's window.
//
// The tests walk the CONTRACT's closed enum rather than a list invented here: a
// branch added to the wire and unhandled by the card fails at the type check, and
// a branch that reaches the window as `mode-raw` fails here.
import { afterEach, describe, expect, it } from 'vitest'
import { render } from 'solid-js/web'

import type { PaneConnection } from '../generated/pane.connection'
import { setActiveLocale } from '../i18n/locale-model'
import { HelperAxisCard } from './helper-axis-card'

type Helper = PaneConnection['helper']

const TOKEN =
  /mode-raw|mode-script|helper-lost|install-failed|consent-pending|consent-denied|unsupported-platform|exec-forbidden|version-mismatch/i

/** Every branch the contract can carry, except `serving` — a serving axis has
 *  no card at all (the panel draws its content). */
const REFUSED: Helper[] = [
  { state: 'starting', action: 'none' },
  { state: 'paused', reason: 'connection', action: 'none' },
  { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' },
  { state: 'unavailable', reason: 'mode-script', action: 'host-settings' },
  { state: 'unavailable', reason: 'helper-lost', action: 'refresh' },
  { state: 'unavailable', reason: 'helper-lost', action: 'none' },
  { state: 'unavailable', reason: 'consent-pending', action: 'none' },
  { state: 'unavailable', reason: 'consent-denied', action: 'none' },
  { state: 'unavailable', reason: 'unsupported-platform', action: 'none' },
  { state: 'unavailable', reason: 'install-failed', action: 'none' },
  { state: 'unavailable', reason: 'exec-forbidden', action: 'none' },
  { state: 'unavailable', reason: 'version-mismatch', action: 'none' },
  { state: 'unavailable', reason: 'connection', action: 'none' },
]

const mounts: (() => void)[] = []

interface CardOpts {
  helper: Helper | null | undefined
  paneId?: string | null
  livePaneId?: string | null
  profileId?: string | null
  onRefresh?: (paneId: string) => void
  onHostSettings?: (profileId: string) => void
}

function mount(opts: CardOpts): HTMLElement {
  const host = document.createElement('div')
  document.body.append(host)
  const unmount = render(
    () => (
      <HelperAxisCard
        surface="files"
        title="Files needs the nocx helper on this connection"
        startingTitle="Waiting for the nocx helper"
        startingDescription="The files of this machine open when the nocx helper starts serving this connection."
        helper={opts.helper}
        paneId={opts.paneId === undefined ? 'pane-1' : opts.paneId}
        livePaneId={() => (opts.livePaneId === undefined ? 'pane-1' : opts.livePaneId)}
        profileId={opts.profileId === undefined ? null : opts.profileId}
        onRefresh={opts.onRefresh}
        onHostSettings={opts.onHostSettings}
      />
    ),
    host,
  )
  mounts.push(() => {
    unmount()
    host.remove()
  })
  return host
}

afterEach(() => {
  while (mounts.length > 0) mounts.pop()?.()
  setActiveLocale('en')
})

describe('the shared helper card draws the one fold', () => {
  it('gives every refused branch its sentence, with no wire token in it', () => {
    for (const helper of REFUSED) {
      const host = mount({ helper })
      const card = host.querySelector('[data-testid="files-helper-axis"]')
      expect(card, JSON.stringify(helper)).not.toBeNull()
      expect(card?.getAttribute('data-helper'), JSON.stringify(helper)).toBe(helper.state)
      const text = card?.textContent ?? ''
      expect(text.trim(), JSON.stringify(helper)).not.toBe('')
      expect(text, JSON.stringify(helper)).not.toMatch(TOKEN)
    }
  })

  it('waits NEUTRALLY while starting, and while nothing has been said yet', () => {
    // `starting` is a moment every healthy SSH connection passes through, and
    // an observation that has not arrived is the same "hold" the gate gives
    // it: a warning on either would be a warning nobody reads by the time one
    // is true. Two states, one card, one tone.
    for (const helper of [{ state: 'starting', action: 'none' } as Helper, null, undefined]) {
      const host = mount({ helper })
      const card = host.querySelector('[data-testid="files-helper-axis"]')
      expect(card, JSON.stringify(helper)).not.toBeNull()
      expect(card?.getAttribute('data-helper'), JSON.stringify(helper)).toBe('starting')
      expect(card?.querySelector('.ui-status-card')?.getAttribute('data-tone')).toBe('neutral')
      expect(card?.textContent).toContain('Waiting for the nocx helper')
      expect(card?.textContent, JSON.stringify(helper)).not.toMatch(TOKEN)
    }
  })

  it('draws the tone the fold gives a refusal, not a hard-coded warning', () => {
    const host = mount({
      helper: { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' },
    })
    expect(host.querySelector('.ui-status-card')?.getAttribute('data-tone')).toBe('warning')
  })

  it('draws no card at all while the axis serves', () => {
    const host = mount({ helper: { state: 'serving', action: 'none' } })
    expect(host.querySelector('[data-testid="files-helper-axis"]')).toBeNull()
  })

  it('offers Refresh exactly where the backend did, and presses for the pane it was drawn for', () => {
    const pressed: string[] = []
    const host = mount({
      helper: { state: 'unavailable', reason: 'helper-lost', action: 'refresh' },
      onRefresh: (paneId) => pressed.push(paneId),
    })
    const button = host.querySelector<HTMLButtonElement>('[data-testid="files-helper-refresh"]')
    expect(button).not.toBeNull()
    button?.click()
    expect(pressed).toEqual(['pane-1'])
  })

  it('drops a Refresh press whose pane is no longer in front (ALX-1019)', () => {
    const pressed: string[] = []
    const host = mount({
      helper: { state: 'unavailable', reason: 'helper-lost', action: 'refresh' },
      livePaneId: 'pane-2',
      onRefresh: (paneId) => pressed.push(paneId),
    })
    host.querySelector<HTMLButtonElement>('[data-testid="files-helper-refresh"]')?.click()
    expect(pressed).toEqual([])
  })

  it('draws no Refresh for a reason that offers no remedy', () => {
    for (const helper of REFUSED.filter((h) => h.action !== 'refresh')) {
      const host = mount({ helper })
      expect(
        host.querySelector('[data-testid="files-helper-refresh"]'),
        JSON.stringify(helper),
      ).toBeNull()
    }
  })

  it('offers Host settings only with a saved connection behind the tab, and hands over its id', () => {
    const asked: string[] = []
    const helper: Helper = { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' }
    const withProfile = mount({
      helper,
      profileId: 'profile-7',
      onHostSettings: (profileId) => asked.push(profileId),
    })
    withProfile.querySelector<HTMLButtonElement>('[data-testid="files-host-settings"]')?.click()
    expect(asked).toEqual(['profile-7'])

    // A hand-typed tab has no host record to edit: the button is ABSENT, and
    // the fold's own sentence says to save the connection instead.
    const adHoc = mount({ helper, profileId: null })
    expect(adHoc.querySelector('[data-testid="files-host-settings"]')).toBeNull()
    expect(adHoc.textContent).toContain('Save this connection')
  })

  it('never offers a button a surface handed it no seam for', () => {
    // A panel wired without the action draws the card, never a button that
    // reaches nothing (the component's own promise).
    const host = mount({
      helper: { state: 'unavailable', reason: 'helper-lost', action: 'refresh' },
    })
    expect(host.querySelector('[data-testid="files-helper-axis"]')).not.toBeNull()
    expect(host.querySelector('[data-testid="files-helper-refresh"]')).toBeNull()
  })

  it('uses the surface identity it is handed, so two panels address one component', () => {
    const host = document.createElement('div')
    document.body.append(host)
    const unmount = render(
      () => (
        <HelperAxisCard
          surface="git"
          title="Git needs the nocx helper on this connection"
          startingTitle="Waiting for the nocx helper"
          startingDescription="The repository opens when the nocx helper starts serving this connection."
          helper={{ state: 'unavailable', reason: 'helper-lost', action: 'refresh' }}
          paneId="pane-9"
          livePaneId={() => 'pane-9'}
          profileId={null}
          onRefresh={() => {}}
          onHostSettings={() => {}}
        />
      ),
      host,
    )
    mounts.push(() => {
      unmount()
      host.remove()
    })
    expect(host.querySelector('[data-testid="git-helper-axis"]')).not.toBeNull()
    expect(host.querySelector('[data-testid="git-helper-refresh"]')).not.toBeNull()
    expect(host.querySelector('[data-testid="files-helper-axis"]')).toBeNull()
  })
})

describe('the shared helper card in another locale', () => {
  it('draws the catalogue sentence, not an English literal', () => {
    setActiveLocale('ru')
    const host = mount({
      helper: { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' },
    })
    expect(host.textContent).toContain('Raw')
    expect(host.textContent).not.toContain('This host uses Raw integration')
  })
})
