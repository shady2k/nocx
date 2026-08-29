/**
 * e2e: one browser-held Postman archive is previewed, imported atomically, and
 * read back from the filesystem the backend wrote (nocx-bvxf2.3).
 *
 * This is deliberately not a fixture export. The archive is assembled below
 * from four JSON documents owned by this test: one manifest, two collections,
 * and one environment. That keeps the acceptance proof independent of names,
 * hosts, credentials, and bytes copied from somebody else's export.
 *
 * The backend is new for every test. The shared stand keeps the collections it
 * receives, so using it here would make the destination and its counts depend
 * on whichever spec happened to run before this one. The test reads the
 * destination from the ask's field, then reads manifests, request files, and
 * environment files below that exact path after the import.
 *
 * No wait below is a duration. The preview, destination, dialog, directory,
 * and toast are all observable state transitions.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, readdirSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'

import { join, sep } from 'node:path'
import { openWorkbench } from './api-workbench'
import { VaultBackend } from './harness'
import { readStand } from './stand'

const test = base

const ARCHIVE_NAME = 'workspace.zip'
const COLLECTION_NAMES = ['Alpha collection', 'Beta collection'] as const
const ENVIRONMENT_NAME = 'Staging environment'
const REQUEST_WITH_UNSUPPORTED_FEATURE = 'Alpha request'
const MANIFEST_NAME = 'nocx-collection.json'
/** THE SHAPE POSTMAN ACTUALLY EXPORTS. A workspace export wraps everything in
 * one directory named for the export, and carries the directory entries too;
 * nothing sits beside it. The archive below is nested for that reason and not
 * for decoration — a flat one was the only shape anything exercised, and the
 * real one could not be imported at all (nocx-bvxf2.4). */
const EXPORT_DIR = '9e0a1c7c-4f2b-4c26-9c37-postman-export'
const ARCHIVE_MEMBER_NAMES = [
  `${EXPORT_DIR}/`,
  `${EXPORT_DIR}/archive.json`,
  `${EXPORT_DIR}/collection/alpha-document.json`,
  `${EXPORT_DIR}/collection/beta-document.json`,
  `${EXPORT_DIR}/environment/staging-document.json`,
] as const

/** A ZIP entry with method 0 (stored). No package or checked-in export bytes
 * are involved: every body is created by this test immediately below. */
interface ZipEntry {
  name: string
  body: Buffer
}

const CRC32_TABLE = Array.from({ length: 256 }, (_, seed) => {
  let value = seed
  for (let bit = 0; bit < 8; bit++) {
    value = value & 1 ? 0xedb88320 ^ (value >>> 1) : value >>> 1
  }
  return value >>> 0
})

function crc32(body: Buffer): number {
  let value = 0xffffffff
  for (const byte of body) value = CRC32_TABLE[(value ^ byte) & 0xff]! ^ (value >>> 8)
  return (value ^ 0xffffffff) >>> 0
}

function jsonBody(value: unknown): Buffer {
  return Buffer.from(JSON.stringify(value), 'utf8')
}

/** Build the smallest valid ZIP that archive/zip can read. The central
 * directory records are assembled from the same entries as the local records,
 * so the bytes selected by the browser are exactly these four named members. */
function buildStoredZip(entries: readonly ZipEntry[]): Buffer {
  const localParts: Buffer[] = []
  const centralParts: Buffer[] = []
  let offset = 0

  for (const entry of entries) {
    const name = Buffer.from(entry.name, 'utf8')
    const checksum = crc32(entry.body)
    const local = Buffer.alloc(30)
    local.writeUInt32LE(0x04034b50, 0)
    local.writeUInt16LE(20, 4)
    local.writeUInt16LE(0, 6)
    local.writeUInt16LE(0, 8)
    local.writeUInt16LE(0, 10)
    local.writeUInt16LE(0, 12)
    local.writeUInt32LE(checksum, 14)
    local.writeUInt32LE(entry.body.length, 18)
    local.writeUInt32LE(entry.body.length, 22)
    local.writeUInt16LE(name.length, 26)
    local.writeUInt16LE(0, 28)
    localParts.push(local, name, entry.body)

    const central = Buffer.alloc(46)
    central.writeUInt32LE(0x02014b50, 0)
    central.writeUInt16LE(20, 4)
    central.writeUInt16LE(20, 6)
    central.writeUInt16LE(0, 8)
    central.writeUInt16LE(0, 10)
    central.writeUInt16LE(0, 12)
    central.writeUInt16LE(0, 14)
    central.writeUInt32LE(checksum, 16)
    central.writeUInt32LE(entry.body.length, 20)
    central.writeUInt32LE(entry.body.length, 24)
    central.writeUInt16LE(name.length, 28)
    central.writeUInt16LE(0, 30)
    central.writeUInt16LE(0, 32)
    central.writeUInt16LE(0, 34)
    central.writeUInt16LE(0, 36)
    central.writeUInt32LE(0, 38)
    central.writeUInt32LE(offset, 42)
    centralParts.push(central, name)

    offset += local.length + name.length + entry.body.length
  }

  const centralDirectory = Buffer.concat(centralParts)
  const end = Buffer.alloc(22)
  end.writeUInt32LE(0x06054b50, 0)
  end.writeUInt16LE(0, 4)
  end.writeUInt16LE(0, 6)
  end.writeUInt16LE(entries.length, 8)
  end.writeUInt16LE(entries.length, 10)
  end.writeUInt32LE(centralDirectory.length, 12)
  end.writeUInt32LE(offset, 16)
  end.writeUInt16LE(0, 20)

  return Buffer.concat([...localParts, centralDirectory, end])
}

function postmanArchive(): Buffer {
  const entries: ZipEntry[] = [
    // The export directory itself, as a ZIP directory entry: it is skipped
    // rather than read, and an archive that refuses one refuses every real
    // export along with it.
    { name: `${EXPORT_DIR}/`, body: Buffer.alloc(0) },
    {
      name: `${EXPORT_DIR}/archive.json`,
      body: jsonBody({
        collection: {
          'alpha-document': true,
          'beta-document': true,
        },
        environment: { 'staging-document': true },
      }),
    },
    {
      name: `${EXPORT_DIR}/collection/alpha-document.json`,
      body: jsonBody({
        info: { name: COLLECTION_NAMES[0] },
        item: [
          {
            name: REQUEST_WITH_UNSUPPORTED_FEATURE,
            event: [{ listen: 'test', script: { exec: ['the test script is not portable'] } }],
            request: {
              method: 'GET',
              url: 'https://example.invalid/alpha',
            },
          },
        ],
      }),
    },
    {
      name: `${EXPORT_DIR}/collection/beta-document.json`,
      body: jsonBody({
        info: { name: COLLECTION_NAMES[1] },
        item: [
          {
            name: 'Beta request',
            request: {
              method: 'POST',
              url: 'https://example.invalid/beta',
            },
          },
        ],
      }),
    },
    {
      name: `${EXPORT_DIR}/environment/staging-document.json`,
      body: jsonBody({
        name: ENVIRONMENT_NAME,
        values: [
          {
            key: 'baseUrl',
            value: 'https://example.invalid',
            enabled: true,
          },
        ],
        _postman_variable_scope: 'environment',
      }),
    },
  ]
  expect(entries.map((entry) => entry.name)).toEqual(ARCHIVE_MEMBER_NAMES)
  return buildStoredZip(entries)
}

interface DiskInventory {
  collectionNames: string[]
  environmentNames: string[]
}

function filesBelow(root: string, relative = ''): string[] {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const child = join(relative, entry.name)
    return entry.isDirectory() ? filesBelow(join(root, entry.name), child) : [child]
  })
}

/** Read the persisted manifests and environment JSON, and classify collection
 * documents by their persisted request files. This intentionally never reads
 * the tree as proof of the write. */
function readDiskInventory(destination: string): DiskInventory {
  const documents = readdirSync(destination, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => {
      const root = join(destination, entry.name)
      const files = filesBelow(root)
      const manifest = JSON.parse(readFileSync(join(root, MANIFEST_NAME), 'utf8')) as {
        name?: unknown
      }
      const requestFiles = files.filter(
        (file) =>
          file.endsWith('.json') &&
          file !== MANIFEST_NAME &&
          !file.startsWith(`environments${sep}`),
      )
      const environmentFiles = files.filter(
        (file) => file.startsWith(`environments${sep}`) && file.endsWith('.json'),
      )
      const environments = environmentFiles.map(
        (file) => JSON.parse(readFileSync(join(root, file), 'utf8')) as { name?: unknown },
      )
      return {
        name: typeof manifest.name === 'string' ? manifest.name : '',
        requestFiles,
        environments,
      }
    })

  return {
    collectionNames: documents
      .filter((document) => document.requestFiles.length > 0)
      .map((document) => document.name),
    environmentNames: documents.flatMap((document) =>
      document.environments.flatMap((environment) =>
        typeof environment.name === 'string' ? [environment.name] : [],
      ),
    ),
  }
}

async function openImportAsk(page: Page, backend: VaultBackend) {
  const workbench = await openWorkbench(page, backend)
  await workbench.locator('#api-collections-menu').click()
  await page.getByRole('menuitem', { name: 'Import collection…' }).click()
  const ask = page.getByRole('dialog').filter({ hasText: 'Import collection' })
  await expect(ask).toBeVisible({ timeout: 15_000 })
  return { workbench, ask }
}

test.describe('a Postman ZIP imports as one complete archive', () => {
  test.use({ viewport: { width: 1400, height: 900 } })

  let backend: VaultBackend

  test.beforeEach(() => {
    backend = new VaultBackend(readStand().server, {
      root: mkdtempSync(join(tmpdir(), 'nocx-e2e-api-import-zip-')),
    })
  })

  test.afterEach(() => {
    backend.stop()
  })

  test('previews exact counts, writes them to the chosen destination, and reports loss afterwards', async ({
    page,
  }) => {
    const { ask } = await openImportAsk(page, backend)
    const input = ask.locator('.ui-file-input__native')
    await expect(input).toHaveCount(1)

    // This is the same file-input gesture a browser user makes. The bytes are
    // the locally built archive above, never a checked-in or foreign export.
    await input.setInputFiles({
      name: ARCHIVE_NAME,
      mimeType: 'application/zip',
      buffer: postmanArchive(),
    })

    // Preview is the pre-write observable: it must name exactly the three
    // documents before Import is enabled or clicked.
    const summary = ask.locator('.api-import-summary')
    await expect(summary).toHaveText('Archive contains 2 collections and 1 environment', {
      timeout: 15_000,
    })
    const importButton = ask.getByRole('button', { name: 'Import', exact: true })
    await expect(importButton).toBeEnabled()

    // Read the destination from the field the ask owns, rather than
    // re-deriving the backend's platform-specific collection path here.
    await ask.getByRole('button', { name: 'Change where this goes' }).click()
    const destinationField = page.locator('#api-import-postman-dest')
    await expect(destinationField).toBeVisible()
    const destination = await destinationField.inputValue()
    expect(destination).not.toBe('')
    expect(existsSync(destination), 'the selected destination existed before Import').toBe(false)

    await importButton.click()

    // The complete disk inventory is the backend writer's closing event. The
    // parent directory exists before its atomic child imports arrive, so
    // waiting on the parent alone would permit a partial read.
    await expect
      .poll(
        () => {
          if (!existsSync(destination)) return null
          try {
            return readDiskInventory(destination)
          } catch {
            return null
          }
        },
        {
          message: `archive inventory never arrived: ${backend.logTail()}`,
          timeout: 20_000,
        },
      )
      .toEqual({
        collectionNames: [...COLLECTION_NAMES].sort(),
        environmentNames: [ENVIRONMENT_NAME],
      })
    await expect(ask).toBeHidden({ timeout: 15_000 })

    // Disk is the source of truth. Manifests, request files, and environment
    // JSON are all read after import from the exact selected destination.
    const inventory = readDiskInventory(destination)
    expect(inventory.collectionNames.sort()).toEqual([...COLLECTION_NAMES].sort())
    expect(inventory.collectionNames).toHaveLength(2)
    expect(inventory.environmentNames).toEqual([ENVIRONMENT_NAME])
    expect(inventory.environmentNames).toHaveLength(1)

    // The Alpha collection carried an unsupported Postman script. The report
    // is asserted after the import and names the document, not just the fact
    // that a warning flashed while the ask was open.
    const report = page.locator('.ui-toast__message').filter({ hasText: 'Not imported from' })
    await expect(report).toContainText(REQUEST_WITH_UNSUPPORTED_FEATURE, { timeout: 15_000 })
    await expect(report).toContainText('scripts')
  })
})
