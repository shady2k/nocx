import { test, expect } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { createHomeIsolation, assertResolvedIsolatedHome } from './home-isolation'

/**
 * The boundary itself, asserted.
 *
 * These need no browser: the module is pure, and it lives in the suite rather
 * than in a unit-test project because the suite is what depends on it. A
 * boundary nothing checks is exactly what nocx-ti8w was — e2e/harness.ts had
 * had correct XDG isolation the whole time, applied to three specs out of
 * twenty-five, and every gate stayed green while a suite run reset the
 * developer's theme and rewrote their profile.
 *
 * Modelled on orca's tests/e2e/helpers/electron-home-isolation.ts, which solves
 * the same problem and had already answered the objection that sent the first
 * draft of this down a longer road: the shell's own entry points (ZDOTDIR,
 * BASH_ENV, ENV) belong in the restricted set, because a terminal spawns the
 * user's login shell and that shell reads the user's rc files.
 */

function disposableRoot(): string {
  return mkdtempSync(join(tmpdir(), 'nocx-e2e-home-'))
}

test.describe('e2e home boundary', () => {
  test('points HOME at the disposable root, canonicalised', () => {
    const root = disposableRoot()
    const iso = createHomeIsolation({ inheritedEnv: {}, root, realHome: '/real/home' })

    expect(iso.env.HOME).toBe(iso.isolatedHome)
    expect(iso.isolatedHome.startsWith('/')).toBe(true)
    // Canonical, not merely joined: on macOS a tmpdir path resolves through the
    // /var → /private/var symlink, and a HOME the app canonicalises differently
    // from the one the test asserts is a boundary nobody can check.
    expect(iso.isolatedHome).not.toContain('..')
    expect(iso.env.NOCX_E2E_HOME_DIR).toBe(iso.isolatedHome)
  })

  test('strips an inherited variable rather than overriding it', () => {
    const root = disposableRoot()
    const iso = createHomeIsolation({
      inheritedEnv: {
        HOME: '/real/home',
        XDG_CONFIG_HOME: '/real/home/.config',
        ZDOTDIR: '/real/home/.zsh',
        BASH_ENV: '/real/home/.bashenv',
        PATH: '/usr/bin',
      },
      root,
      realHome: '/real/home',
    })

    // XDG_CONFIG_HOME wins over HOME on Linux, so leaving an inherited one in
    // place would route the config dir straight back out of the boundary. The
    // shell entry points would do the same for the login shell the PTY spawns.
    expect(iso.env.XDG_CONFIG_HOME).toBeUndefined()
    expect(iso.env.ZDOTDIR).toBeUndefined()
    expect(iso.env.BASH_ENV).toBeUndefined()
    // Everything unrelated is passed through untouched: this is a boundary, not
    // a clean room, and a backend with no PATH tests nothing.
    expect(iso.env.PATH).toBe('/usr/bin')
  })

  test('refuses an override of the boundary instead of quietly accepting it', () => {
    const root = disposableRoot()
    expect(() =>
      createHomeIsolation({
        inheritedEnv: {},
        overrideEnv: { HOME: '/real/home' },
        root,
        realHome: '/real/home',
      }),
    ).toThrow(/cannot override the e2e home boundary/i)

    expect(() =>
      createHomeIsolation({
        inheritedEnv: {},
        overrideEnv: { XDG_CONFIG_HOME: '/real/home/.config' },
        root,
        realHome: '/real/home',
      }),
    ).toThrow(/cannot override the e2e home boundary/i)
  })

  test('refuses to launch when the isolated home IS the real one', () => {
    const root = disposableRoot()
    const probe = createHomeIsolation({ inheritedEnv: {}, root, realHome: '/real/home' })

    expect(() =>
      createHomeIsolation({ inheritedEnv: {}, root, realHome: probe.isolatedHome }),
    ).toThrow(/developer home/i)
  })

  test('the post-launch assertion catches an escape without printing the real home', () => {
    const root = disposableRoot()
    const iso = createHomeIsolation({ inheritedEnv: {}, root, realHome: '/real/home/alice' })

    expect(() => assertResolvedIsolatedHome(iso.isolatedHome, iso)).not.toThrow()

    // A failed safety assertion can land in a shared CI artifact, so the message
    // must not carry the developer's username or native home path.
    let message = ''
    try {
      assertResolvedIsolatedHome('/real/home/alice', iso)
    } catch (e) {
      message = (e as Error).message
    }
    expect(message).toMatch(/escaped/i)
    expect(message).not.toContain('/real/home/alice')
  })
})
