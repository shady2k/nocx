/**
 * e2e: a person creates a vault secret from a header field with no
 * environment selected, sends the request, and keeps the value out of the
 * collection file (the secret-in-any-field epic's DONE WHEN).
 *
 * The two assertions that decide the feature pull in opposite directions:
 *
 *   1. THE SERVER RECEIVED THE VALUE. Its route answers 200 only when the
 *      expected header carries the real value, so a literal `@` or
 *      `{{secret:...}}` cannot satisfy the run.
 *   2. AND THE REQUEST FILE HOLDS NEITHER VALUE NOR DISPLAY NAME. The bytes
 *      on disk must contain only the opaque `secrow:` handle.
 *
 * The walk starts by explicitly selecting "No environment". That state is
 * load-bearing: before this epic it had no way to address a secret at all.
 * Every wait is on a UI or filesystem state, never a duration.
 */
import { test as base, expect } from '@playwright/test'
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

const COLLECTION_NAME = 'header-secret-api'
const REQUEST_NAME = 'send header secret'
const HEADER_NAME = 'X-E2E-Secret'
const SECRET_DISPLAY_NAME = 'e2e header token'
const SECRET_VALUE = 'e2e-header-secret-value-9f4c7a2d'
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

test.describe('a vault secret in a header with no environment', () => {
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

  test('creates and sends a header secret without writing its value to the request file', async ({
    page,
  }) => {
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // The vault must be real: the picker-created secret goes through the
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
    // the scenario starts from the once-dead No environment state.
    const environmentRail = workbench.locator('.api-environments-rail')
    const noEnvironment = environmentRail.getByRole('button', {
      name: 'No environment',
      exact: true,
    })
    await expect(noEnvironment).toBeVisible()
    await noEnvironment.click()
    await expect(noEnvironment).toHaveAttribute('aria-selected', 'true')

    await workbench.getByRole('tab', { name: /^Headers\b/ }).click()
    const headerValue = workbench.locator('#api-header-value-0')
    await headerValue.fill('@')
    const picker = page.getByRole('listbox', { name: 'vault secrets' })
    await expect(picker).toBeVisible()
    const addSecret = picker.getByRole('option').filter({ hasText: 'Add a secret' })
    await expect(addSecret).toBeVisible()
    // The selected row is the only row in an empty vault. Pressing Enter is
    // the person's keyboard activation and avoids relying on the floating
    // panel being scrollable inside the narrow header column.
    await headerValue.press('Enter')

    const addDialog = page.getByRole('dialog').filter({ hasText: 'Add secret' })
    await expect(addDialog).toBeVisible()
    await addDialog.locator('#sr-add-name').fill(SECRET_DISPLAY_NAME)
    await addDialog.locator('#sr-add-value').fill(SECRET_VALUE)
    await addDialog.getByRole('button', { name: 'Add secret', exact: true }).click()
    await expect(addDialog).not.toBeVisible()

    // The create handoff returns to Settings. Come back to the request, ask
    // for the new row, and assert the field now holds the opaque reference.
    await page.locator('.activity-bar button[data-action="api"]').click()
    await expect(workbench).toBeVisible()
    await headerValue.fill('')
    await headerValue.fill('@')
    await expect(picker).toBeVisible()
    const createdSecret = picker.getByRole('option', { name: SECRET_DISPLAY_NAME, exact: true })
    await expect(createdSecret).toBeVisible()
    await expect(createdSecret).toHaveAttribute('aria-selected', 'true')
    // The created record is the first vault row, so Enter accepts this
    // selected option without clicking a floating panel outside the viewport.
    await headerValue.press('Enter')
    await expect(headerValue).toHaveValue(/\{\{secret:secrow:[^}]+\}\}/)

    await workbench.getByRole('button', { name: 'Send', exact: true }).click()
    const run = workbench.locator('.api-run').first()
    await expect(run).toHaveAttribute('data-outcome', 'answered', { timeout: 20_000 })
    await expect(run.locator('.api-run__stats')).toContainText('HTTP status 200')

    // ── 1. THE SERVER RECEIVED THE REAL VALUE ─────────────────────────────
    expect(server.headerValues()).toEqual([SECRET_VALUE])

    // ── 2. THE FILE HOLDS ONLY THE OPAQUE HANDLE ──────────────────────────
    await expect
      .poll(() => existsSync(collectionRoot), {
        message: `the import never produced ${collectionRoot}`,
        timeout: 15_000,
      })
      .toBe(true)
    const savedFiles = walk(collectionRoot)
    expect(savedFiles.length, 'the collection wrote no files').toBeGreaterThan(0)
    const requestFile = savedFiles.find((file) =>
      readFileSync(file, 'utf8').includes(`"name": "${REQUEST_NAME}"`),
    )
    if (requestFile === undefined) throw new Error('imported request file was not found')
    const requestText = readFileSync(requestFile, 'utf8')
    expect(requestText).toContain('{{secret:secrow:')
    expect(requestText).not.toContain(SECRET_VALUE)
    expect(requestText).not.toContain(SECRET_DISPLAY_NAME)
  })
})
