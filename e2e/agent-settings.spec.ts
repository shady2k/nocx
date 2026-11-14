/**
 * e2e: Settings shows which of the agents the assistant can run are on this
 * computer, and a person can ask again after installing one
 * (nocx-a62cl.12.2, ADR-anntnxdj decision 6).
 *
 * WHAT THIS FILE IS FOR. The backend finds the agents and answers them over
 * `agentRecords.detect` (nocx-a62cl.12.1, green in its own layer), and the
 * section renders an answer it is handed (green in the component's own
 * tests). Neither is evidence that a person can see anything: the sentence
 * this stage is judged on is the one below, driven through the real backend.
 *
 *   With a `pi` on the login shell's PATH and no `claude`, Settings' agent
 *   section shows pi found, with the version it printed, and ready; Claude
 *   Code reads not installed. The stub is removed, Refresh is pressed, and pi
 *   reads not installed.
 *
 * THE SEAM IS THE LOGIN SHELL, because that is where the backend looks. The
 * app's own PATH is not a person's (nocx-4d6uf), so detection runs
 * `$SHELL -l -i -c` and reads the PATH that shell resolved. This spec
 * therefore writes the stub into its own directory and exports that directory
 * from the shell's OWN startup file in the backend's disposable home —
 * `~/.bash_profile`, and `~/.zprofile` for the zsh a stock Mac logs in with.
 * Putting it on the backend's PATH instead would prove nothing: /etc/profile
 * resets root's PATH, which is the reason the rc file is the seam a person's
 * installation actually uses.
 *
 * WHY BOTH SPELLINGS, and not only bash: the container this suite runs in has
 * bash for root and CI runs the same container, but the suite also runs on a
 * developer's Mac, where the account shell is zsh. Two files, one line each,
 * and the spec then asserts the same thing on both.
 *
 * A BACKEND OF ITS OWN (VaultBackend), not the shared stand: the rc file is
 * inside its disposable home, so the stub cannot leak into another spec's
 * shell — and the shared home is a directory several workers read at once.
 *
 * NOTHING HERE WAITS OUT A DURATION (AGENTS.md). The read is a pull the page
 * makes when it opens, and every assertion waits on what that read produced —
 * the version in the row, then the readiness word after Refresh. The second
 * half's premise is established first: pi is watched reading READY before the
 * stub is removed, so "not installed" afterwards is a change and not the only
 * answer the page ever had.
 */
import { expect, type Page } from '@playwright/test'
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  bindEndpoint,
  settingsReady,
  standalone as base,
  VaultBackend,
} from './harness'
import { readStand } from './stand'

const test = base

const TITLE = '.nocx-tab-title'
/** The rail row, addressed by the page's own id — the settings rail's
 *  `data-item` is the registry's id and never a translated title, which is
 *  what makes this locator survive the language. */
const NAV = '.ui-grouped-nav__item[data-item="agent-installed"] button'
const PI_ROW = '[data-agent="pi"]'
const CLAUDE_ROW = '[data-agent="claude"]'

/** The version the stub prints, unique to this spec so a row showing some
 *  other `pi` — the machine's own, if one were on the runner — cannot pass. */
const PI_VERSION = 'pi 9.9.9-e2e'

/** A pi that answers both questions detection asks it: `--version` prints a
 *  version, and `auth check` exits 0, which its record reads as ready. */
const PI_STUB = `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "${PI_VERSION}"
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "check" ]; then
  exit 0
fi
exit 1
`

let backend: VaultBackend
let endpoint: { port: number; token: string }
let stubDir = ''

/** Put the stub where the login shell will find it. */
function installPi(): void {
  writeFileSync(join(stubDir, 'pi'), PI_STUB)
  chmodSync(join(stubDir, 'pi'), 0o755)
}

test.beforeAll(async () => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-a62cl-12-2-e2e-'))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
  stubDir = join(root, 'bin')
  mkdirSync(stubDir, { recursive: true })
  installPi()
  // Written AFTER the backend is up, because the home is the one start()
  // resolved — and before the page opens, because detection runs when the
  // section asks it to, not when the backend boots.
  const line = `export PATH="${stubDir}:$PATH"\n`
  for (const name of ['.bash_profile', '.zprofile']) {
    writeFileSync(join(backend.isolatedHome, name), line)
  }
})

test.afterAll(() => {
  backend?.stop()
})

async function openAgentSection(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  // Up before the chord is pressed: a ⌘, that lands before the binding is
  // wired opens nothing, and the wait after it burns the whole budget on a
  // page that is never coming (snippets.spec.ts records the same).
  await appReadyForInput(page)
  await page.keyboard.press('ControlOrMeta+,')
  await settingsReady(page)
  await page.locator(NAV).click()
}

test.describe('Settings lists the agents this computer has (nocx-a62cl.12.2)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.setTimeout(120_000)

  test('shows pi found with its version and ready, Claude Code not installed, and re-reads after the stub is removed', async ({
    page,
  }) => {
    await openAgentSection(page)

    const pi = page.locator(PI_ROW)
    await expect(pi).toContainText(PI_VERSION, { timeout: 20_000 })
    await expect(pi).toContainText('Ready')
    // The other half of the same answer, and the one a person reads as "nocx
    // cannot use this agent at all": no claude on the login shell's PATH.
    await expect(page.locator(CLAUDE_ROW)).toContainText('Not installed')

    // The premise of the second half: pi is there BECAUSE the stub is, so
    // removing the stub has something to change.
    rmSync(join(stubDir, 'pi'), { force: true })
    await page.getByRole('button', { name: 'Refresh' }).click()

    await expect(pi).toContainText('Not installed', { timeout: 20_000 })
    await expect(pi).not.toContainText(PI_VERSION)
  })
})
