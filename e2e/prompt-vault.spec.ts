/**
 * e2e: the vault reaches the prompt (the renderer half of "secrets in the
 * prompt", ADR-0021), and the offer arrives AFTER the command has run.
 *
 * The owner's acceptance in one sentence, as the product now works: run a
 * command carrying an API key, be offered to save it on the block that just
 * finished — never interrupted while typing — accept, and press Up a week
 * later (figuratively: after a restart) to get a command that still runs,
 * because what came back is the reference and not the key.
 *
 * The path exercised, end to end through a real backend:
 *   1. set the vault up (Settings -> Secrets -> "Set up protection");
 *   2. type a command carrying a key: NOTHING interrupts composition — no
 *      panel above the prompt, focus untouched — and Enter runs it with the
 *      literal the user typed, which the shell echoes back;
 *   3. the record ack lands: the block's own command line is redrawn as the
 *      masked text with a chip, and the receipt appears INSIDE that block
 *      with the backend's suggested name, while focus stays in the editor;
 *   4. save it: the vault holds the value and the stored row becomes the
 *      reference;
 *   5. restart the backend (the vault seals), press Up: the recalled row is
 *      the REFERENCE, not a mask; Enter raises the unlock prompt, and after
 *      unsealing the command runs again with the live value.
 *
 * The key never reaches the ledger and the reference resolves at submit:
 * both halves of the invariant are asserted from the outside.
 *
 * The pre-submit offer this file used to drive is DELETED, and its absence
 * is asserted rather than assumed — a panel that interrupts composition is
 * the defect the round existed to remove.
 */
import { test as base, expect } from '@playwright/test'
import { mkdtempSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, bindEndpoint, type DisposableRoot } from './harness'
import { readStand } from './stand'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

// Two distinct ports so restart never conflicts with the first instance's
// TIME_WAIT. Both are outside the ranges used by the rest of the suite
// (vault.spec 19876/19877, history-persistence 19878/19879, recall-search
// 19880, wails 34115, the e2e default 9876).

const TITLE = '.nocx-tab-title'
const INPUT = '.nocx-editor-input'

interface XdgDirsResult {
  root: string
  data: string
  config: string
  cache: string
}

/** Create a temp directory with data/config/cache subdirs for one test case. */
function createXdgDirs(): XdgDirsResult {
  const root = mkdtempSync(join(tmpdir(), 'nocx-prompt-vault-'))
  const data = join(root, 'data')
  const config = join(root, 'config')
  const cache = join(root, 'cache')
  mkdirSync(data, { recursive: true })
  mkdirSync(config, { recursive: true })
  mkdirSync(cache, { recursive: true })
  return { root, data, config, cache }
}

function asDisposableRoot(r: XdgDirsResult): DisposableRoot {
  return { root: r.root }
}

const test = base

test.describe('vault secrets in the prompt — the owner’s acceptance', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    // `true` = no Secret Service for this backend: the passphrase path is the
    // deterministic one (setup always prompts, unseal always needs the
    // passphrase), exactly like vault.spec.ts's cases 1-2.
    backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  const PASS = 'prompt-vault-master-pass'

  test('run a key -> receipt on the block -> saved -> survives a restart as the reference', async ({
    page,
  }) => {
    // ── Phase 1: set the vault up (Settings -> Secrets) ──────────────────
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })

    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 10_000 })
    await page.locator('.ui-grouped-nav__item[data-item="secrets"]').click()
    await expect(page.getByRole('button', { name: 'Set up protection' })).toBeVisible({
      timeout: 10_000,
    })
    await page.getByRole('button', { name: 'Set up protection' }).click()

    await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).toBeVisible({
      timeout: 10_000,
    })
    await page.locator('#vault-setup-passphrase').fill(PASS)
    await page.locator('#vault-setup-confirm').fill(PASS)
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Recovery Code' })).toBeVisible({
      timeout: 10_000,
    })
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // ── Phase 2: type a key and run it — composition is NOT interrupted ──
    await page.locator(TITLE).first().click()
    const input = page.locator(INPUT)
    await expect(input).toBeVisible({ timeout: 10_000 })
    await expect(input).toBeFocused({ timeout: 10_000 })

    // The shell echoes the value back, which is what proves the PTY received
    // the literal the user typed — nothing was substituted on the way in.
    const KEY = 'sk-proj-abcdefghijklmnop'
    await input.fill(`echo ${KEY}`)

    // Nothing appears above the prompt and focus never moves. The old
    // pre-submit panel is gone; its absence is the assertion.
    await page.waitForTimeout(1_200) // past the detection settle
    await expect(page.locator('.ui-secret-offer')).toHaveCount(0)
    await expect(input).toBeFocused()

    await page.keyboard.press('Enter')

    // ── Phase 3: the receipt arrives ON the block that just finished ─────
    const receipt = page.locator('.ui-block-receipt')
    await expect(receipt).toBeVisible({ timeout: 15_000 })
    // Attached to the frozen block — never floated over the prompt, never in
    // the floating host.
    const receiptInBlock = page.locator('.cmd-block .ui-block-receipt')
    await expect(receiptInBlock).toHaveCount(1)
    await expect(page.locator('.ui-floating-panel .ui-block-receipt')).toHaveCount(0)
    // And it did not steal the keyboard.
    await expect(input).toBeFocused()

    // The block's own command line now reads as what was stored: the masked
    // text with a chip where the key was.
    // `:has()` rather than filter({ has: receiptInBlock }): that filter
    // resolves its locator INSIDE each candidate, so a page-rooted
    // `.cmd-block .ui-block-receipt` would be looking for a nested block.
    const block = page.locator('.cmd-block:has(.ui-block-receipt)').first()
    await expect(block.locator('.cmd-header-text .ui-secret-chip')).toHaveCount(1)
    await expect(block.locator('.cmd-header-text')).not.toContainText(KEY)
    // The OUTPUT still holds the echoed value — that is the program's own
    // bytes, which we neither touch nor retain (AD-6, ADR-0008).
    await expect(block).toContainText(KEY)

    // One credential, so one row and an action naming its scope.
    await expect(receipt.locator('.ui-block-receipt__row')).toHaveCount(1)
    await expect(receipt.locator('.ui-block-receipt__primary')).toHaveText('Save')
    // The name is the backend's suggestion: no host in `echo`, no env
    // assignment, so it falls back to the kind.
    const nameField = receipt.locator('.ui-text-field__input')
    await expect(nameField).toHaveValue('openai')
    await receipt.locator('.ui-block-receipt__primary').click()

    // Saved: the receipt is done with, and the vault holds the value.
    await expect(receipt).toBeHidden({ timeout: 10_000 })

    // ── Phase 4: restart (the vault seals), Up, run again ────────────────
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })

    // Up opens recall only from a focused prompt; after a reload the editor
    // takes a moment to own input (the harness's promptReady contract).
    await expect(input).toBeVisible({ timeout: 10_000 })
    await expect(input).toBeFocused({ timeout: 10_000 })
    await page.keyboard.press('ArrowUp')
    const panel = page.locator('.ui-floating-panel[data-variant="recall"]')
    await expect(panel).toBeVisible({ timeout: 10_000 })
    await expect(panel).toContainText('openai', { timeout: 10_000 })
    // The recalled row is the REFERENCE (rendered as the chip), never a mask.
    await expect(panel.locator('.ui-secret-chip')).toHaveCount(1)
    await expect(panel).not.toContainText(KEY)
    await expect(panel).not.toContainText('...')

    // Enter on the row submits through the same seam: resolveLine hits the
    await page.keyboard.press('Enter')
    await expect(page.getByRole('dialog').filter({ hasText: 'Unlock the vault' })).toBeVisible({
      timeout: 15_000,
    })
    await page.getByRole('dialog').getByRole('button', { name: 'Passphrase', exact: true }).click()
    await page.locator('#vault-unlock-passphrase').fill(PASS)
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Unlock the vault' })).not.toBeVisible({
      timeout: 10_000,
    })
    const block2 = page.locator('.cmd-block', { hasText: KEY }).last()
    await expect(block2).toBeVisible({ timeout: 15_000 })
    await expect(block2).toContainText(KEY)
  })
})
