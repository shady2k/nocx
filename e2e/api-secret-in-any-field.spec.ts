/**
 * e2e: a person creates a vault secret from both the Auth tab and a header
 * field, sends the request, and keeps both values out of the collection file
 * (the secret-in-any-field epic's DONE WHEN).
 *
 * The two assertions that decide each journey pull in opposite directions:
 *
 *   1. THE SERVER RECEIVED THE VALUE. Its route answers 200 only when the
 *      expected header carries the real value, so a literal `@` or
 *      `{{secret:...}}` cannot satisfy the run. The Auth journey separately
 *      asserts that the Authorization header carried its real value.
 *   2. AND THE REQUEST FILE HOLDS NEITHER VALUE NOR DISPLAY NAME. The bytes
 *      on disk must contain only the opaque `secrow:` handles.
 *
 * The walk starts by explicitly selecting "No environment". That state is
 * load-bearing: before this epic it had no way to address a secret at all.
 * Every wait is on a UI or filesystem state, never a duration.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, settingsReady, VaultBackend, type DisposableRoot } from './harness'
import {
  startHeaderSecretServer,
  type HeaderSecretServer,
} from './fixtures/api-header-secret-server'
import { readStand } from './stand'

const test = base
interface SettingsAbsenceGuard {
  readonly isVisible: () => boolean
  readonly observer: MutationObserver
  readonly state: { violated: boolean }
}

declare global {
  interface Window {
    __nocxSettingsAbsenceGuard?: SettingsAbsenceGuard
  }
}

const COLLECTION_NAME = 'header-secret-api'
const REQUEST_NAME = 'send header secret'
const HEADER_NAME = 'X-E2E-Secret'
const SECRET_DISPLAY_NAME = 'e2e-header-token'
const SECRET_VALUE = 'e2e-header-secret-value-9f4c7a2d'
const AUTH_SECRET_VALUE = 'e2e-auth-bearer-value-6b1d8e3f'
const VAULT_PASSPHRASE = 'api-secret-any-field-e2e-master-pass'

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const devharnessBin = (): string => readStand().devharness

/** Every file under `root`, recursively. */
function walk(root: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(root)) {
    const full = join(root, entry)
    if (statSync(full).isDirectory()) out.push(...walk(full))
    else out.push(full)
  }
  return out
}

function collectionExport(baseUrl: string): string {
  const base = new URL(baseUrl)
  return JSON.stringify(
    {
      info: {
        _postman_id: '7b9e2f10-4e44-4d2c-9d6a-8c4f1b2e7a30',
        name: COLLECTION_NAME,
        schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json',
      },
      // A collection variable makes the imported environment visible in the
      // rail, which lets the person choose the explicit No environment row.
      variable: [{ key: 'baseUrl', value: baseUrl, type: 'string' }],
      item: [
        {
          name: REQUEST_NAME,
          request: {
            method: 'GET',
            header: [{ key: HEADER_NAME, value: '' }],
            url: {
              raw: `${baseUrl}/header-secret`,
              host: [base.hostname],
              path: ['header-secret'],
              port: base.port || undefined,
            },
          },
          response: [],
        },
      ],
    },
    null,
    2,
  )
}

/**
 * Settings is allowed during vault setup, but not while either request is
 * being edited or sent. The observer catches a transient visible mount that a
 * final one-time locator assertion would miss; the final visibility check also
 * catches a mounted pane whose visibility changed without a DOM mutation.
 */
async function installSettingsAbsenceGuard(page: Page): Promise<void> {
  await page.evaluate(() => {
    const isVisible = (): boolean => {
      const root = document.querySelector<HTMLElement>('.ui-settings')
      if (root === null) return false
      const style = window.getComputedStyle(root)
      return (
        style.display !== 'none' &&
        style.visibility !== 'hidden' &&
        root.getClientRects().length > 0
      )
    }

    const state = { violated: false }
    const inspect = (): void => {
      if (isVisible()) state.violated = true
    }
    const observer = new MutationObserver(inspect)
    observer.observe(document.body, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: ['aria-hidden', 'class', 'hidden', 'style'],
    })
    inspect()
    Object.defineProperty(window, '__nocxSettingsAbsenceGuard', {
      configurable: true,
      value: { isVisible, observer, state },
    })
  })
}

async function settingsWereVisible(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const guard = window.__nocxSettingsAbsenceGuard
    return guard?.state.violated === true || guard?.isVisible() === true
  })
}

function requestFile(collectionRoot: string): string {
  const savedFiles = walk(collectionRoot)
  expect(savedFiles.length, 'the collection wrote no files').toBeGreaterThan(0)
  const file = savedFiles.find((candidate) =>
    readFileSync(candidate, 'utf8').includes(`"name": "${REQUEST_NAME}"`),
  )
  if (file === undefined) throw new Error('imported request file was not found')
  return file
}

async function assertRequestFileContainsOnlyHandles(
  collectionRoot: string,
  forbiddenValues: readonly string[],
  expectedHandleCount: number,
): Promise<void> {
  await expect
    .poll(() => existsSync(collectionRoot), {
      message: `the import never produced ${collectionRoot}`,
      timeout: 15_000,
    })
    .toBe(true)
  const requestText = readFileSync(requestFile(collectionRoot), 'utf8')
  expect(requestText.match(/\{\{secret:secrow:[^}]+\}\}/g) ?? []).toHaveLength(expectedHandleCount)
  for (const value of forbiddenValues) expect(requestText).not.toContain(value)
}

test.describe('vault secrets in Auth and header fields with no environment', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend
  let server: HeaderSecretServer
  let collectionRoot: string

  test.beforeAll(async () => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-secret-any-field-')) }
    server = await startHeaderSecretServer({
      expectedHeader: HEADER_NAME,
      expectedValue: SECRET_VALUE,
    })
    collectionRoot = join(disposable.root, COLLECTION_NAME)
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterAll(async () => {
    backend?.stop()
    await server?.stop()
  })

  test('creates and sends secrets from Auth and header fields without writing values to the request file', async ({
    page,
  }) => {
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // The vault must be real: the picker-created secrets go through the
    // product's own setup and create paths, never through RPC state seeding.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="secrets"]').click()
    await page.getByRole('button', { name: 'Set up protection' }).click()
    const setup = page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })
    await expect(setup).toBeVisible({ timeout: 10_000 })
    await page.locator('#vault-setup-passphrase').fill(VAULT_PASSPHRASE)
    await page.locator('#vault-setup-confirm').fill(VAULT_PASSPHRASE)
    await setup.getByRole('button', { name: /Set Up/i }).click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Recovery Code' })).toBeVisible({
      timeout: 10_000,
    })
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    await installSettingsAbsenceGuard(page)

    await workbench.locator('#api-collections-menu').click()
    await page.getByRole('menuitem', { name: 'Import collection…' }).click()
    const importAsk = page.getByRole('dialog').filter({ hasText: 'Import collection' })
    await expect(importAsk).toBeVisible()
    await page.locator('#api-import-paste').fill(collectionExport(server.baseUrl))
    await importAsk.getByRole('button', { name: 'Change where this goes' }).click()
    await page.locator('#api-import-postman-dest').fill(collectionRoot)
    await importAsk.getByRole('button', { name: 'Import', exact: true }).click()

    const requestRow = workbench.locator('.api-tree__row').filter({ hasText: REQUEST_NAME })
    await expect(requestRow).toBeVisible({ timeout: 15_000 })
    await requestRow.click()

    // Do not rely on the default. The explicit selected row is the proof that
    // both journeys start from the once-dead No environment state.
    const environmentRail = workbench.locator('.api-environments-rail')
    const noEnvironment = environmentRail.getByRole('button', {
      name: 'No environment',
      exact: true,
    })
    await expect(noEnvironment).toBeVisible()
    await noEnvironment.click()
    await expect(noEnvironment).toHaveAttribute('aria-selected', 'true')

    // ── HEADER TAB DOOR ────────────────────────────────────────────────────
    await workbench.getByRole('tab', { name: /^Headers\b/ }).click()
    const headerValue = workbench.locator('#api-header-value-0')
    await headerValue.fill(`@${SECRET_DISPLAY_NAME}`)
    const picker = page.getByRole('listbox', { name: 'vault secrets' })
    await expect(picker).toBeVisible()
    const addSecret = picker.getByRole('option', { name: /Add .* to the vault/ })
    await expect(addSecret).toBeVisible()
    // The selected row is the only row in an empty vault. Pressing Enter is
    // the person's keyboard activation and avoids relying on the floating
    // panel being scrollable inside the narrow header column.
    await headerValue.press('Enter')

    const addDialog = page.getByRole('dialog').filter({ hasText: 'Create secret' })
    await expect(addDialog).toBeVisible()
    // The typed @ word is the proposed name; accepting it proves the dialog
    // opened over the request rather than navigating to Settings.
    await expect(addDialog.locator('#secret-create-name')).toHaveValue(SECRET_DISPLAY_NAME)
    await addDialog.locator('#secret-create-value').fill(SECRET_VALUE)
    await addDialog.getByRole('button', { name: 'Save to vault', exact: true }).click()
    await expect(addDialog).not.toBeVisible()
    await expect(headerValue).toHaveValue(/\{\{secret:secrow:[^}]+\}\}/)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const headerRun = workbench.locator('.api-run').first()
    await expect(headerRun).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(headerRun.locator('.api-run__stats')).toContainText('HTTP status 200')
    expect(server.headerValues()).toEqual([SECRET_VALUE])
    await assertRequestFileContainsOnlyHandles(
      collectionRoot,
      [SECRET_VALUE, SECRET_DISPLAY_NAME],
      1,
    )
    expect(await settingsWereVisible(page)).toBe(false)

    // ── AUTH TAB DOOR ──────────────────────────────────────────────────────
    await workbench.getByRole('tab', { name: /^Auth\b/ }).click()
    const authKind = workbench.locator('[data-api-field="auth-kind"] select')
    await authKind.selectOption('bearer')
    await expect(authKind).toHaveValue('bearer')
    const authSource = workbench.getByRole('radiogroup', {
      name: 'Authentication value source',
    })
    const newAuth = authSource.getByRole('radio', { name: 'Type a new one', exact: true })
    await newAuth.click()
    await expect(newAuth).toHaveAttribute('aria-checked', 'true')
    const authToken = workbench.locator('#api-auth-var')
    await authToken.fill(AUTH_SECRET_VALUE)
    const storeAuth = workbench.getByRole('button', { name: 'Store', exact: true })
    await expect(storeAuth).toBeEnabled()
    await storeAuth.click()

    const authDialog = page.getByRole('dialog').filter({ hasText: 'Create secret' })
    await expect(authDialog).toBeVisible()
    const authNameField = authDialog.locator('#secret-create-name')
    await expect(authNameField).toHaveValue(/.+/)
    const proposedAuthName = await authNameField.inputValue()
    expect(proposedAuthName).not.toContain(AUTH_SECRET_VALUE)
    await authDialog.locator('#secret-create-value').fill(AUTH_SECRET_VALUE)
    await authDialog.getByRole('button', { name: 'Save to vault', exact: true }).click()
    await expect(authDialog).not.toBeVisible()
    const existingAuth = authSource.getByRole('radio', {
      name: 'Use existing secret',
      exact: true,
    })
    await expect(existingAuth).toHaveAttribute('aria-checked', 'true')
    await expect(workbench.locator('#api-auth-secret')).toHaveValue(proposedAuthName)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const authRun = workbench.locator('.api-run').first()
    await expect(authRun).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(authRun.locator('.api-run__stats')).toContainText('HTTP status 200')

    // ── THE SERVER RECEIVED BOTH REAL VALUES ───────────────────────────────
    expect(server.headerValues()).toEqual([SECRET_VALUE, SECRET_VALUE])
    expect(server.authorizationValues()).toEqual(['', `Bearer ${AUTH_SECRET_VALUE}`])

    // ── THE FILE HOLDS ONLY THE OPAQUE HANDLES ─────────────────────────────
    await assertRequestFileContainsOnlyHandles(
      collectionRoot,
      [SECRET_VALUE, SECRET_DISPLAY_NAME, AUTH_SECRET_VALUE, proposedAuthName],
      2,
    )
    expect(await settingsWereVisible(page)).toBe(false)
  })
})
