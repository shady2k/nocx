// @vitest-environment jsdom
/**
 * updates-section — the honest half of the Updates page (nocx-1ebrs.35.1).
 *
 * Linux AppImage installs can self-update after nocx-azxe.8, while deb and rpm
 * installs remain package-manager-managed. The page states that distinction
 * on the platform the wire reports. These assert the card is
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
    build: '202611012200',
    date: 'today',
    go: '1.26',
    wails: 'v3.0.0',
    platform,
    development: false,
  }
  return () => Promise.resolve(about)
}

describe('UpdatesSection', () => {
  it('says Linux AppImage installs can self-update, while package installs use their manager', async () => {
    const view = render(() => <UpdatesSection load={loadAbout('linux/amd64')} />)
    expect(await view.findByText(/AppImage installs can update themselves/i)).toBeTruthy()
    expect(
      view.getByText(/deb and rpm installs update through their package manager/i),
    ).toBeTruthy()
  })

  it('makes no Linux claim on macOS', async () => {
    const view = render(() => <UpdatesSection load={loadAbout('darwin/arm64')} />)
    // The resource resolves before the query runs; give it one microtask.
    await Promise.resolve()
    expect(view.queryByText('Linux updates')).toBeNull()
  })

  it('makes no platform claim at all when the build description is unavailable', () => {
    const { container } = render(() => <UpdatesSection />)
    expect(container.textContent).toBe('')
  })
})
