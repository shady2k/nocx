/**
 * e2e: the FIRST SSH connection finishes from the tab that made it
 * (nocx-oalo1.25, ALX-2046, acceptance of ALX-2069).
 *
 * ## The state the defect left a person in
 *
 * A first connection to a saved host has two questions: the host key, and
 * whether nocx may use its helper there. The dialog asks both at once and each
 * has its own action, so a person who trusts the key and stops is telling nocx
 * one thing and nothing about the other.
 *
 * What the backend does with that is deliberate: the open refuses to decide the
 * method itself, releases its probe-only connection so a login nobody asked for
 * does not outlive the question, and reports `ended(released)` with a Reconnect
 * offer — the pane is the only interest left. Pressing it used to CLAIM that
 * connection, and nothing held it, so the supervisor answered
 * `hostconn: reconnect is no longer offered`: measured on ALX-2046 in 3/3 runs,
 * with the method dialog never appearing and the tab waiting forever.
 *
 * ## What this walks, in the order a person does
 *
 * A clean profile and a stand host whose key nobody has met. Trust the key,
 * press Reconnect in the ORIGINAL tab, answer the method question with HELPER,
 * and type a command in THAT tab — the marker has to come back from the shell
 * this tab opened, which is what proves the tab finished rather than a new one
 * appearing beside it. Then reopen the same saved connection: the method is
 * already the host's, so nothing asks again and the shell comes up on its own.
 *
 * Every assertion is on an observable state — the offer's button, the dialog's
 * title, the editor, the command's own block — and never on a duration.
 */
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { appReadyForInput, test, expect, resolveBackend, type Page } from './harness'
import { rpc, startSshd, stopSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const EDITOR = '.nocx-editor-input'
const QUICK_CONNECT = '.quick-connect__search input'
const HEAD = '[data-testid="connection-head"]'
const METHOD_ASK = 'Choose how nocx connects'
const UNKNOWN_KEY = 'Unknown host key'

/**
 * Forget THIS fixture's host key, and nothing else.
 *
 * A fresh profile is about the RUN, not the machine: the stand's `known_hosts`
 * is shared by every SSH spec that this run has in flight, and emptying it
 * takes the trusted key out from under a test that is connecting at the same
 * moment (ALX-2085 finding 5). The file is a line per host, so one line is
 * removed — the endpoint this fixture bound, spelled as ssh writes it.
 */
function forgetThisHostOnly(fixture: SshdFixture): void {
  const sshDir = path.join(readStand().home, '.ssh')
  const file = path.join(sshDir, 'known_hosts')
  const host = (fixture.knownHosts.trim().split(/\s+/)[0] ?? '') as string
  let lines: string[] = []
  try {
    lines = readFileSync(file, 'utf8').split('\n')
  } catch {
    // No file is the fresh-profile state: nothing has been trusted yet.
  }
  const kept = lines.filter((line) => line.trim() !== '' && line.trim().split(/\s+/)[0] !== host)
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(file, kept.length === 0 ? '' : `${kept.join('\n')}\n`)
}

/**
 * Open a saved connection from the palette and answer the id of the pane it
 * made. The id is the point: `.pane.active` would follow the focus and pass
 * even if the connection had opened a second tab, which is exactly what the
 * acceptance says must not happen (ALX-2085 finding 3).
 */
async function openProfileThroughQuickConnect(page: Page, profileName: string): Promise<string> {
  const before = await page.locator(TAB).count()
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator(QUICK_CONNECT)
  await expect(search).toBeVisible()
  await search.fill(profileName)
  const savedRow = page.locator('.quick-connect__item:not(.quick-connect__item--system)', {
    hasText: profileName,
  })
  await expect(savedRow).toBeVisible({ timeout: 10_000 })
  await expect(savedRow).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(page.locator(TAB)).toHaveCount(before + 1)
  const tab = page.locator(`${TAB}[aria-selected="true"]`)
  const paneId = await tab.getAttribute('data-pane-id')
  if (paneId === null) throw new Error('the tab this connection opened has no pane id')
  return paneId
}

/** The pane element a tab's `data-pane-id` names (panes.ts: `pane-<id>`). */
function paneOf(page: Page, paneId: string) {
  return page.locator(`#pane-${paneId}`)
}

/** The head's recovery offer, and only it: the head also carries the helper
 *  axis's own action, so the accessible name is what identifies this one. */
function recoveryButton(pane: ReturnType<typeof paneOf>) {
  return pane.locator(HEAD).getByRole('button', { name: /^(Reconnect|Retry shell)$/ })
}

test.describe('first SSH connection: trust the key, then recover in the tab', () => {
  test('trusting the key, choosing Helper in the original tab, then reopening with the saved method', async ({
    page,
  }) => {
    // The far helper prunes other generations from its home. Give it a
    // separate home so it cannot remove the stand's local helper.
    const remoteHome = mkdtempSync(path.join(readStand().home, 'ssh-first-connect-'))
    let fixture: SshdFixture | null = null
    let profileId: string | null = null
    try {
      fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
      forgetThisHostOnly(fixture)

      await page.goto('/')
      await appReadyForInput(page)
      await expect(page.locator(TAB)).toHaveCount(1)

      const endpoint = await resolveBackend(page)
      const profileName = `e2e-first-connect-${Date.now()}`
      const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
        type: 'ssh',
        name: profileName,
        options: {
          host: '127.0.0.1',
          port: fixture.port,
          user: 'e2e',
          keyPath: fixture.userKey,
        },
      })
      profileId = created.id

      const paneId = await openProfileThroughQuickConnect(page, profileName)
      const pane = paneOf(page, paneId)

      // 1. ONE DIALOG ASKS BOTH QUESTIONS. Trusting the key answers the key.
      const unknown = page.getByRole('dialog').filter({ hasText: UNKNOWN_KEY })
      await expect(unknown).toBeVisible({ timeout: 30_000 })
      await unknown.getByRole('button', { name: 'Trust host key' }).click()
      await expect(unknown).not.toBeVisible({ timeout: 20_000 })

      // 2. THE ORIGINAL TAB IS OFFERED THE WAY OUT, and takes it.
      const reconnect = recoveryButton(pane)
      await expect(reconnect).toBeVisible({ timeout: 30_000 })
      await reconnect.click()

      // 3. THE PENDING METHOD CHOICE IS PRESENTED — the defect: this dialog
      // never appeared, and the press answered hostconn's refusal instead.
      const method = page.getByRole('dialog').filter({ hasText: METHOD_ASK })
      await expect(method).toBeVisible({ timeout: 30_000 })
      await method.getByRole('radio', { name: 'Helper' }).click()
      await method.getByRole('button', { name: 'Continue' }).click()
      await expect(method).not.toBeVisible({ timeout: 60_000 })

      // 4. THE SAME TAB FINISHES. A helper connection installs the far helper
      // on this host before its shell exists, so the editor is the last thing
      // to arrive; the tab count says the shell did not arrive in a new one.
      const editor = pane.locator(EDITOR)
      await expect(editor).toBeVisible({ timeout: 180_000 })
      await expect(page.locator(TAB)).toHaveCount(2)

      // AND IT IS THIS TAB'S SHELL THAT ANSWERS: a command typed here, whose
      // block is in this pane with its output on screen.
      await editor.click()
      const marker = `ready-after-reconnect-${Date.now()}`
      await editor.pressSequentially(`echo ${marker}`)
      await editor.press('Enter')
      const block = pane.locator('.cmd-block', { hasText: marker }).first()
      await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 30_000 })
      await expect(block.locator(':scope > .cmd-output')).toContainText(marker, {
        timeout: 30_000,
      })

      // 5. THE ANSWER IS THE HOST'S (ADR-727g63tr decision 1), so opening the
      // same saved connection again asks nothing: not the method, not the key.
      const reopenedId = await openProfileThroughQuickConnect(page, profileName)
      const reopened = paneOf(page, reopenedId)
      await expect(reopened.locator(EDITOR)).toBeVisible({ timeout: 120_000 })
      await expect(page.getByRole('dialog').filter({ hasText: METHOD_ASK })).toHaveCount(0)
      await expect(page.getByRole('dialog').filter({ hasText: UNKNOWN_KEY })).toHaveCount(0)

      const reopenedEditor = reopened.locator(EDITOR)
      await reopenedEditor.click()
      const second = `reopened-with-saved-method-${Date.now()}`
      await reopenedEditor.pressSequentially(`echo ${second}`)
      await reopenedEditor.press('Enter')
      const secondBlock = reopened.locator('.cmd-block', { hasText: second }).first()
      await expect(secondBlock).toHaveAttribute('data-outcome', 'success', { timeout: 30_000 })
      await expect(secondBlock.locator(':scope > .cmd-output')).toContainText(second, {
        timeout: 30_000,
      })
    } finally {
      if (profileId !== null) {
        try {
          const endpoint = await resolveBackend(page)
          await rpc(page, endpoint, 'profiles.delete', { id: profileId })
        } catch {
          // A cleanup that throws would replace the real failure with its own.
        }
      }
      try {
        if (fixture !== null) await stopSshd(fixture)
      } finally {
        rmSync(remoteHome, { recursive: true, force: true })
      }
    }
  })
})
