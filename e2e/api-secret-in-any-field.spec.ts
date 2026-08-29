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

import {
  bindEndpoint,
  openImportDestination,
  settingsReady,
  VaultBackend,
  type DisposableRoot,
} from './harness'
import {
  startHeaderSecretServer,
  type HeaderSecretServer,
} from './fixtures/api-header-secret-server'
import { readStand } from './stand'
import { fieldChip, storeFieldValue } from './secret-field'

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
const SECRET_VALUE = 'e2e-header-secret-value-9f4c7a2d'
const AUTH_SECRET_VALUE = 'e2e-auth-bearer-value-6b1d8e3f'
const VAULT_PASSPHRASE = 'api-secret-any-field-e2e-master-pass'

/** Lazily: the stand is started by globalSetup, after this file is collected. */
const serverBin = (): string => readStand().server

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
            header: [{ key: HEADER_NAME, value: 'Bearer ' }],
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

function currentRequestText(collectionRoot: string): string {
  if (!existsSync(collectionRoot)) return ''
  const file = walk(collectionRoot).find((candidate) =>
    readFileSync(candidate, 'utf8').includes(`"name": "${REQUEST_NAME}"`),
  )
  return file === undefined ? '' : readFileSync(file, 'utf8')
}

async function assertRequestFileContainsOnlyHandles(
  collectionRoot: string,
  forbiddenValues: readonly string[],
  expectedHandleCount: number,
): Promise<string> {
  const handleCount = (): number =>
    currentRequestText(collectionRoot).match(/\{\{secret:secrow:[^}]+\}\}/g)?.length ?? 0
  await expect
    .poll(handleCount, {
      message: `the request file never contained ${expectedHandleCount} secret handles`,
      timeout: 15_000,
    })
    .toBe(expectedHandleCount)
  const requestText = currentRequestText(collectionRoot)
  expect(requestText).not.toBe('')
  const handles = requestText.match(/\{\{secret:secrow:[^}]+\}\}/g) ?? []
  expect(handles).toHaveLength(expectedHandleCount)
  expect(requestText.match(/\{\{secret:[^}]+\}\}/g) ?? []).toEqual(handles)
  for (const value of forbiddenValues) expect(requestText).not.toContain(value)
  return requestText
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
      expectedValue: `Bearer ${SECRET_VALUE}`,
    })
    collectionRoot = join(disposable.root, COLLECTION_NAME)
    backend = new VaultBackend(serverBin(), disposable)
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
    await (await openImportDestination(importAsk, page)).fill(collectionRoot)
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
    await expect(headerValue).toHaveValue('Bearer ')
    await headerValue.press('End')
    await headerValue.pressSequentially(SECRET_VALUE)
    await expect(headerValue).toHaveValue(`Bearer ${SECRET_VALUE}`)
    await page.keyboard.down('Shift')
    try {
      for (let index = 0; index < SECRET_VALUE.length; index += 1) {
        await page.keyboard.press('ArrowLeft')
      }
    } finally {
      await page.keyboard.up('Shift')
    }
    expect(
      await headerValue.evaluate((element) => {
        const input = element as HTMLInputElement
        return [input.selectionStart, input.selectionEnd]
      }),
    ).toEqual(['Bearer '.length, `Bearer ${SECRET_VALUE}`.length])

    await storeFieldValue(page, headerValue, SECRET_VALUE)
    const headerDialog = page.getByRole('dialog').filter({ hasText: 'Create secret' })
    await expect(headerDialog).toBeVisible()
    const headerNameField = headerDialog.locator('#secret-create-name')
    await expect(headerNameField).toHaveValue(/.+/)
    const proposedHeaderName = await headerNameField.inputValue()
    expect(proposedHeaderName).toBe(`${new URL(server.baseUrl).hostname} token`)
    expect(proposedHeaderName).not.toContain(SECRET_VALUE)
    await expect(headerDialog.locator('#secret-create-value')).toHaveValue(SECRET_VALUE)
    await headerDialog.getByRole('button', { name: 'Save to vault', exact: true }).click()
    await expect(headerDialog).not.toBeVisible()
    await expect(headerValue).toHaveValue(/^Bearer \{\{secret:secrow:[^}]+\}\}$/)
    await expect(fieldChip(headerValue)).toHaveText(proposedHeaderName)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const headerRun = workbench.locator('.api-run').first()
    await expect(headerRun).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(headerRun.locator('.api-run__stats')).toContainText('HTTP status 200')
    expect(server.headerValues()).toEqual([`Bearer ${SECRET_VALUE}`])
    const headerRequestText = await assertRequestFileContainsOnlyHandles(
      collectionRoot,
      [SECRET_VALUE, proposedHeaderName],
      1,
    )
    expect(headerRequestText).toMatch(/Bearer \{\{secret:secrow:[^}]+\}\}/)
    expect(await settingsWereVisible(page)).toBe(false)

    // ── AUTH TAB DOOR ──────────────────────────────────────────────────────
    await workbench.getByRole('tab', { name: /^Auth\b/ }).click()
    const authKind = workbench.locator('[data-api-field="auth-kind"] select')
    await authKind.selectOption('bearer')
    await expect(authKind).toHaveValue('bearer')
    // The Auth tab is the SAME field as every other value on this surface
    // (nocx-3o0ed.4): no source to declare first, and no standalone "Store"
    // button beside it. The value is typed and its own lock offers to keep
    // it — one door, the same one the header value above used.
    //
    // The tab shows its label ONCE. It used to draw a Field labelled "Token"
    // around the segmented control and a TextField labelled "Token" inside
    // it, which is the duplication the owner photographed.
    await expect(
      workbench.getByRole('radiogroup', { name: 'Authentication value source' }),
    ).toHaveCount(0)
    await expect(workbench.getByLabel('Token', { exact: true })).toHaveCount(1)
    const authToken = workbench.locator('#api-auth-var')
    await authToken.fill(AUTH_SECRET_VALUE)
    expect(
      await authToken.evaluate((element) => {
        const input = element as HTMLInputElement
        return input.selectionStart === input.selectionEnd
      }),
      'Auth credentials path must store the whole field with no selection',
    ).toBe(true)
    await storeFieldValue(page, authToken, AUTH_SECRET_VALUE)

    const authDialog = page.getByRole('dialog').filter({ hasText: 'Create secret' })
    await expect(authDialog).toBeVisible()
    const authNameField = authDialog.locator('#secret-create-name')
    await expect(authNameField).toHaveValue(/.+/)
    const proposedAuthName = await authNameField.inputValue()
    expect(proposedAuthName).not.toContain(AUTH_SECRET_VALUE)
    expect(proposedAuthName).toBe(`${new URL(server.baseUrl).hostname} token 2`)
    await expect(authDialog.locator('#secret-create-value')).toHaveValue(AUTH_SECRET_VALUE)
    await authDialog.getByRole('button', { name: 'Save to vault', exact: true }).click()
    await expect(authDialog).not.toBeVisible()
    // BOUND, and that is the whole of what "the existing-secret segment is
    // selected" asserted: the field holds the opaque reference and the chip
    // over it names the secret the store just created.
    await expect(authToken).toHaveValue(/^\{\{secret:secrow:[^}]+\}\}$/)
    await expect(fieldChip(authToken)).toHaveText(proposedAuthName)

    // The second send needs its OWN run as the subject, and until this waited
    // for one it did not have it. The list is newest-first (api-store.ts
    // prepends: `[{new}, ...prev]`), so when no new run appears `.first()` is
    // the PREVIOUS run — which is already `answered` with a 200. Both waits
    // below were therefore satisfiable by the first send, and a second send
    // that never started read as success all the way down to
    // `server.headerValues()`: one recorded request against two expected,
    // reported against the server rather than against the click that did not
    // land (nocx-dqgik).
    //
    // Waiting on the COUNT is what makes the new row the subject. The first
    // send needs no such guard — there is no earlier run for `.first()` to
    // match, so its wait cannot be satisfied by anything but its own.
    const runs = workbench.locator('.api-run')
    await expect(runs).toHaveCount(1)
    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    await expect(runs).toHaveCount(2, { timeout: 20_000 })
    const authRun = runs.first()
    await expect(authRun).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(authRun.locator('.api-run__stats')).toContainText('HTTP status 200')

    // ── THE SERVER RECEIVED BOTH REAL VALUES ───────────────────────────────
    expect(server.headerValues()).toEqual([`Bearer ${SECRET_VALUE}`, `Bearer ${SECRET_VALUE}`])
    expect(server.authorizationValues()).toEqual(['', `Bearer ${AUTH_SECRET_VALUE}`])

    // ── THE FILE HOLDS ONLY THE OPAQUE HANDLES ─────────────────────────────
    const requestText = await assertRequestFileContainsOnlyHandles(
      collectionRoot,
      [SECRET_VALUE, proposedHeaderName, AUTH_SECRET_VALUE, proposedAuthName],
      2,
    )
    expect(requestText).toMatch(/Bearer \{\{secret:secrow:[^}]+\}\}/)
    expect(await settingsWereVisible(page)).toBe(false)
  })
})
