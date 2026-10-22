// @vitest-environment jsdom
/**
 * updates-section — the honest half of the Updates page (nocx-1ebrs.35.1).
 *
 * The Linux AppImage cannot self-update until nocx-azxe.8 lands, and the
 * bead forbids a dead update control: the page the channel select lives on
 * must say so, on the platform the wire reports. These assert the card is
 * there on Linux, absent on macOS, and absent — rather than guessed — when
 * no build description is available.
 */
import { describe, it, expect } from 'vitest'
import { render } from '@solidjs/testing-library'
import { UpdatesSection } from './updates-section'
import type { AppAbout } from './generated/app.about'

function loadAbout(platform: string): () => Promise<AppAbout> {
  const about: AppAbout = {
    version: '0.7.0',
    commit: 'abc',
    date: 'today',
    go: '1.26',
    wails: 'v3.0.0',
    platform,
    development: false,
  }
  return () => Promise.resolve(about)
}

describe('UpdatesSection', () => {
  it('says the build is install-only on Linux, where self-update cannot apply', async () => {
    const view = render(() => <UpdatesSection load={loadAbout('linux/amd64')} />)
    expect(await view.findByText('This build is install-only on Linux')).toBeTruthy()
  })

  it('makes no Linux claim on macOS', async () => {
    const view = render(() => <UpdatesSection load={loadAbout('darwin/arm64')} />)
    // The resource resolves before the query runs; give it one microtask.
    await Promise.resolve()
    expect(view.queryByText('This build is install-only on Linux')).toBeNull()
  })

  it('makes no platform claim at all when the build description is unavailable', () => {
    const { container } = render(() => <UpdatesSection />)
    expect(container.textContent).toBe('')
  })
})
