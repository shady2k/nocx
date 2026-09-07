/**
 * e2e: a quiet skill ages out, records that nocx switched it off, and can be
 * returned to service by the person. The second half proves the other machine
 * pin through the assistant's real skills.update path.
 *
 * Every wait observes a DOM state, a model request, or a file written by the
 * backend. This spec deliberately contains no waitForTimeout or sleep.
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  createAiEndpoint,
  documentDir,
  bindEndpoint,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

function directoryDigest(document: string): string {
  const pathBytes = Buffer.from('SKILL.md')
  const documentBytes = Buffer.from(document)
  return createHash('sha256')
    .update(Buffer.from([pathBytes.length]))
    .update(pathBytes)
    .update(Buffer.from([documentBytes.length]))
    .update(documentBytes)
    .digest('hex')
}

const test = base
const nonce = Date.now().toString(36)
const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const ASSISTANT_GROUP = '.ui-grouped-nav__group[data-group="assistant"]'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_SKILLS_NAV = '.ui-grouped-nav__item[data-item="skills"]'
const APPROVAL_TITLE = 'This action needs your approval'

const QUIET_NAME = `quiet-${nonce}`
const KEEP_ENABLED_NAME = `keep-enabled-${nonce}`
const GUARDED_NAME = `keep-unchanged-${nonce}`
const ORDINARY_NAME = `ordinary-${nonce}`
const DESCRIPTION = `Ageing e2e skill ${nonce}`
const ENDPOINT_NAME = `Ageing e2e endpoint ${nonce}`
const UPDATED_DESCRIPTION = `Updated ordinary skill ${nonce}`
const UPDATED_BODY = `The ordinary skill was updated by the assistant ${nonce}.`
const OLD_DATE = '2026-01-01T00:00:00Z'
const NOW_DATE = '2026-09-07T00:00:00Z'

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
let configDir = ''

interface StoredSkillsDocument {
  disabled?: string[]
  autoOff?: Record<string, unknown>
}

test.describe.configure({ mode: 'serial' })
test.setTimeout(180_000)

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()

  const root = mkdtempSync(join(tmpdir(), `nocx-skill-ageing-${nonce}-`))
  // HomeIsolation derives HOME as <root>/home. Seed the fixture before the
  // binary starts so discovery reads exactly the document this test authored.
  configDir = documentDir(join(root, 'home'))
  const authoredDir = join(configDir, 'skills')
  const managedDir = join(configDir, 'managed-skills')
  mkdirSync(authoredDir, { recursive: true })
  mkdirSync(managedDir, { recursive: true })
  const digests: Record<string, string> = {}
  for (const [name, description] of [
    [QUIET_NAME, DESCRIPTION],
    [KEEP_ENABLED_NAME, DESCRIPTION],
    [GUARDED_NAME, DESCRIPTION],
    [ORDINARY_NAME, DESCRIPTION],
  ] as const) {
    const body = `Remember ${name} for this ageing proof (${nonce}).`
    const document = `---\nname: ${name}\ndescription: "${description}"\n---\n\n${body}\n`
    const rootDir = name === QUIET_NAME || name === KEEP_ENABLED_NAME ? authoredDir : managedDir
    const dir = join(rootDir, name)
    mkdirSync(dir, { recursive: true })
    writeFileSync(join(dir, 'SKILL.md'), document)
    if (rootDir === managedDir) digests[name] = directoryDigest(document)
  }

  writeFileSync(
    join(configDir, 'skills.json'),
    JSON.stringify(
      {
        schemaVersion: 5,
        disabled: [],
        enabled: [],
        digests,
        sources: {},
        usage: {
          [QUIET_NAME]: { count: 0, firstSeenAt: OLD_DATE },
          [KEEP_ENABLED_NAME]: { count: 0, firstSeenAt: OLD_DATE },
          [GUARDED_NAME]: { count: 0, firstSeenAt: NOW_DATE },
          [ORDINARY_NAME]: { count: 0, firstSeenAt: NOW_DATE },
        },
        pins: {
          [KEEP_ENABLED_NAME]: { keepEnabled: true },
          [GUARDED_NAME]: { keepUnchanged: true },
        },
      },
      null,
      2,
    ),
  )

  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

function rowFor(page: Page, name: string): Locator {
  return page
    .locator('.ui-collection-row')
    .filter({ has: page.locator('.ui-record-row__title', { hasText: name }) })
}

async function openSkills(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(`${ASSISTANT_GROUP} ${SETTINGS_SKILLS_NAV}`).click()
}

async function configureAssistant(page: Page): Promise<void> {
  await page.locator(SETTINGS_AI_NAV).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: ENDPOINT_NAME,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: `ageing-key-${nonce}`,
    vaultPassphrase: `ageing-vault-${nonce}`,
  })
  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, ENDPOINT_NAME, 'e2e-model')
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

async function askFromPrompt(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
}

function answerBlock(page: Page, question: string): Locator {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question }).first()
}

async function answerFinished(page: Page, question: string): Promise<void> {
  const turn = answerBlock(page, question)
  await expect(turn).toBeVisible({ timeout: 30_000 })
  await expect(turn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
}

function readSkillsDocument(): StoredSkillsDocument {
  return JSON.parse(readFileSync(join(configDir, 'skills.json'), 'utf8')) as StoredSkillsDocument
}

test.describe('a skill ages and nocx records the machine decision (nocx-dzy7l)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('switches off only quiet skills, clears the mark on re-enable, and obeys both pins', async ({
    page,
  }) => {
    await openApp(page)
    await openSkills(page)

    const quiet = rowFor(page, QUIET_NAME)
    const keepEnabled = rowFor(page, KEEP_ENABLED_NAME)
    const guarded = rowFor(page, GUARDED_NAME)
    const ordinary = rowFor(page, ORDINARY_NAME)
    await expect(quiet).toHaveCount(1, { timeout: 15_000 })
    await expect(keepEnabled).toHaveCount(1)
    await expect(guarded).toHaveCount(1)
    await expect(ordinary).toHaveCount(1)

    await expect(quiet.locator('.ui-record-row__status')).toContainText('Switched off by nocx on')
    await expect(keepEnabled.locator('.ui-record-row__status')).toHaveCount(0)
    await expect(rowFor(page, 'skill-authoring').locator('.ui-record-row__status')).toHaveCount(0)

    const agedDocument = readSkillsDocument()
    expect(agedDocument.disabled ?? []).not.toContain(QUIET_NAME)
    expect(agedDocument.autoOff?.[QUIET_NAME]).toBeDefined()

    const quietSwitch = quiet.locator('[role="switch"]')
    await quietSwitch.check()
    await expect(quietSwitch).toBeChecked()
    await expect(quiet.locator('.ui-record-row__status')).toHaveCount(0)
    const reenabledDocument = readSkillsDocument()
    expect(reenabledDocument.disabled ?? []).not.toContain(QUIET_NAME)
    expect(reenabledDocument.autoOff?.[QUIET_NAME]).toBeUndefined()

    await configureAssistant(page)

    const pinnedQuestion = `Update the pinned skill ${nonce}.`
    fake.setScript({
      chunks: [],
      toolCalls: [
        {
          name: 'skills.update',
          id: 'call_pinned_update',
          arguments: {
            name: GUARDED_NAME,
            description: UPDATED_DESCRIPTION,
            body: UPDATED_BODY,
          },
        },
      ],
    })
    const pinnedBase = fake.requests().length
    await askFromPrompt(page, pinnedQuestion)
    const pinnedApproval = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(pinnedApproval).toBeVisible({ timeout: 30_000 })
    await pinnedApproval.getByRole('button', { name: 'Allow once' }).click()
    const pinnedRequests = await fake.waitForRequests(pinnedBase + 1)
    expect(pinnedRequests[pinnedBase].body).toContain('skills.update')
    const pinnedTurn = answerBlock(page, pinnedQuestion)
    await expect(pinnedTurn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('failed', {
      timeout: 30_000,
    })
    await expect(pinnedTurn).toContainText('pinned unchanged')

    const ordinaryQuestion = `Update the ordinary skill ${nonce}.`
    fake.setScript({
      chunks: [],
      toolCalls: [
        {
          name: 'skills.update',
          id: 'call_ordinary_update',
          arguments: {
            name: ORDINARY_NAME,
            description: UPDATED_DESCRIPTION,
            body: UPDATED_BODY,
          },
        },
      ],
    })
    fake.setScript({ chunks: [`Updated ${ORDINARY_NAME}.`] })
    const ordinaryBase = fake.requests().length
    await askFromPrompt(page, ordinaryQuestion)
    const ordinaryApproval = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(ordinaryApproval).toBeVisible({ timeout: 30_000 })
    await ordinaryApproval.getByRole('button', { name: 'Allow once' }).click()
    await fake.waitForRequests(ordinaryBase + 2)
    await answerFinished(page, ordinaryQuestion)

    const updatedFile = readFileSync(
      join(configDir, 'managed-skills', ORDINARY_NAME, 'SKILL.md'),
      'utf8',
    )
    expect(updatedFile).toContain(UPDATED_DESCRIPTION)
    expect(updatedFile).toContain(UPDATED_BODY)
    const pinnedFile = readFileSync(
      join(configDir, 'managed-skills', GUARDED_NAME, 'SKILL.md'),
      'utf8',
    )
    expect(pinnedFile).not.toContain(UPDATED_BODY)
  })
})
