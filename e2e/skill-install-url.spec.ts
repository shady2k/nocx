/**
 * e2e: a person installs a skill somebody else wrote, by its URL, and the
 * assistant follows it in another pane (nocx-qja4m.7, install design §10).
 *
 * This is the epic's happy path watched end to end, written from what a
 * person does rather than from what the code does. The whole sentence, in
 * order: Settings opens, Skills is the one entry under Assistant, an address
 * is pasted, the document that comes back is READ — its name, its description
 * and its whole body on screen — the install is approved, the row appears
 * saying `installed` and where the bytes came from, the skill's CARD is opened
 * and its file read there, the switch on that card is turned on — the skill
 * arrives inert, because the bytes came from outside — and a later ask in
 * another pane answers from that skill's content.
 *
 * TWO LOCAL SERVERS, both owned by this spec process, both on 127.0.0.1.
 * `FakeOpenAI` is the model the assistant dials. The second is a plain
 * `node:http` server holding one SKILL.md — the shape `fetch-url.spec.ts`
 * already uses for "the backend goes and gets a document from this machine",
 * kept inline for the same reason it is inline there: the fixture is one
 * string, and only this spec serves it. `http://` reaches it because
 * internal/httppolicy permits http to loopback and private addresses, which
 * is the intended shape for a local endpoint.
 *
 * THE TWO CALLS MUST REACH ONE BACKEND. `skills.install` compares its own
 * second fetch against the digest `skills.preview` kept on the server, so a
 * restart between reading and approving is refused with "read the document
 * first". Nothing here restarts the backend between them, and the spec is
 * serial so nothing else can.
 *
 * THE PANE-B ANSWER IS DERIVED FROM THE TOOL MESSAGE, exactly as
 * `skills.spec.ts` derives it: the fake's final answer is written from the
 * request body it is actually answering, so it can only say the skill's body
 * if the body reached the model. A fixed answer would pass while the prompt
 * omitted the skill, the read was refused, or its result was dropped — which
 * is the whole question this spec exists to ask.
 *
 * Every wait is an observable state change: a rail on screen, a dialog, a
 * row, a recorded fake request, a completed ledger block. This spec
 * deliberately contains no waitForTimeout.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { createServer, type Server } from 'node:http'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const NEW_TAB = '[aria-label="New tab"]'
const SETTINGS_NAV = '[aria-label="Settings sections"]'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const SETTINGS_SKILLS_NAV = '.ui-grouped-nav__item[data-item="skills"]'
const ASSISTANT_GROUP = '.ui-grouped-nav__group[data-group="assistant"]'
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'
const INSTALL_DIALOG = 'Install a skill from a URL'

const ENDPOINT_NAME = `E2E Install ${nonce}`
const SKILL_NAME = `pager-drill-${nonce}`
const SKILL_DESCRIPTION = `What to do when the pager goes off ${nonce}`
const SKILL_BODY = `Acknowledge the page, then run make drill ${nonce}.`
/** The document the fixture serves. The name and description live in YAML
 *  frontmatter because that is what a SKILL.md is; the description is quoted
 *  so the fixture stays a document rather than becoming a YAML puzzle. */
const SKILL_DOCUMENT = [
  '---',
  `name: ${SKILL_NAME}`,
  `description: "${SKILL_DESCRIPTION}"`,
  '---',
  '',
  SKILL_BODY,
  '',
].join('\n')
const QUESTION = `What do I do when the pager goes off, ${nonce}?`

let backend: VaultBackend
let fake: FakeOpenAI
let documentServer: Server
let skillURL: string
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })
test.setTimeout(120_000)

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()

  documentServer = createServer((req, res) => {
    // One address, and everything else is 404: the skill that arrives is the
    // one the person pasted, not whatever the fixture happened to hold.
    if (req.url !== '/skills/pager-drill/SKILL.md') {
      res.writeHead(404).end()
      return
    }
    res.writeHead(200, { 'Content-Type': 'text/markdown; charset=utf-8' })
    res.end(SKILL_DOCUMENT)
  })
  await new Promise<void>((resolve, reject) => {
    documentServer.once('error', reject)
    documentServer.listen(0, '127.0.0.1', resolve)
  })
  const address = documentServer.address()
  if (!address || typeof address === 'string') throw new Error('skill fixture did not bind')
  skillURL = `http://127.0.0.1:${address.port}/skills/pager-drill/SKILL.md`

  const root = mkdtempSync(join(tmpdir(), `nocx-qja4m-e2e-${nonce}-`))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
  await new Promise<void>((resolve) => documentServer?.close(() => resolve()))
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

/** The assistant the second half of this spec asks. Not what is under test —
 *  it is the equipment the pane-B question needs — so it is the same
 *  configuration skills.spec.ts makes, for the same reasons: an endpoint, a
 *  default model, and observe allowed so the read demonstrates itself instead
 *  of opening a second approval. skills.read is an observe operation. */
async function configureAssistant(page: Page): Promise<void> {
  await page.locator(SETTINGS_AI_NAV).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: ENDPOINT_NAME,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: `e2e-key-${nonce}`,
    vaultPassphrase: `vault-pass-${nonce}`,
  })

  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, ENDPOINT_NAME, 'e2e-model')

  await page.locator(SETTINGS_POLICY_NAV).click()
  const observeRow = page.locator(OBSERVE_ROW)
  await expect(observeRow).toBeVisible({ timeout: 10_000 })
  await observeRow.locator('select').first().selectOption({ label: 'Allowed' })
  await expect(observeRow.locator('.st-policy__state')).toContainText('Allowed', {
    timeout: 10_000,
  })
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

function answerBlock(page: Page, question: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question }).first()
}

async function answerFinished(page: Page, question: string): Promise<void> {
  const turn = answerBlock(page, question)
  await expect(turn).toBeVisible({ timeout: 30_000 })
  await expect(turn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
}

/** The final answer, written FROM the request it is answering. It says the
 *  skill's body only if the skills.read result actually reached the model. */
function answerFromSkillRead(body: string): string[] {
  try {
    const payload = JSON.parse(body) as {
      messages?: { role?: string; content?: unknown }[]
    }
    const toolResult = (payload.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
      .find((content) => content.includes(SKILL_BODY))
    return toolResult
      ? [`The installed skill says: ${toolResult}`]
      : ['The skills.read result was missing.']
  } catch {
    return ['The skills.read result was malformed.']
  }
}

test.describe('a skill installed from a URL is followed in another pane (nocx-qja4m.7)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('paste a URL in Settings, approve what it holds, and answer from it', async ({ page }) => {
    await openApp(page)
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await configureAssistant(page)

    // ── SKILLS IS FOUND UNDER ASSISTANT, AND THERE IS ONE OF IT ────────────
    // (nocx-fe7fe.1). The page owns the `Skills` settings section, so a
    // regression there mints a second rail row of the same name holding one
    // switch. Counting is what reports that; clicking the first match would
    // not.
    const nav = page.locator(SETTINGS_NAV)
    await expect(nav.locator('.ui-grouped-nav__item').filter({ hasText: 'Skills' })).toHaveCount(1)
    const skillsNav = page.locator(`${ASSISTANT_GROUP} ${SETTINGS_SKILLS_NAV}`)
    await expect(skillsNav).toHaveCount(1)
    await expect(skillsNav).toContainText('Skills')
    await skillsNav.click()

    // ── THE ADDRESS IS PASTED AND THE DOCUMENT IS READ ─────────────────────
    await page.getByRole('button', { name: 'Install from a URL' }).click()
    const ask = page.getByRole('dialog', { name: INSTALL_DIALOG })
    await expect(ask).toBeVisible({ timeout: 10_000 })

    const urlField = ask.getByLabel("The skill's URL")
    await urlField.fill(skillURL)
    const read = ask.getByRole('button', { name: 'Read this skill' })
    await expect(read).toBeEnabled()
    await read.click()

    // The person reads what came back before they adopt it: the address the
    // ask is holding, the two things the frontmatter says about itself, and
    // the whole body they are about to give the assistant.
    await expect(ask).toContainText(skillURL, { timeout: 15_000 })
    await expect(ask.locator('.ui-fact-list')).toContainText(SKILL_NAME)
    await expect(ask.locator('.ui-fact-list')).toContainText(SKILL_DESCRIPTION)
    await expect(ask.locator('.ui-code-block')).toContainText(SKILL_BODY)

    // ── APPROVING INSTALLS IT, AND THE ROW SAYS WHERE IT CAME FROM ─────────
    await ask.getByRole('button', { name: 'Install', exact: true }).click()
    await expect(ask).toBeHidden({ timeout: 15_000 })

    const row = page
      .locator('.ui-record-row')
      .filter({ has: page.locator('.ui-record-row__title', { hasText: SKILL_NAME }) })
    await expect(row).toHaveCount(1, { timeout: 15_000 })
    await expect(row.locator('.ui-badge')).toHaveText('installed')
    await expect(row.locator('.ui-record-row__meta-text')).toHaveText(SKILL_DESCRIPTION)
    // Both lines of the record's own evidence: the file Delete removes, and
    // the address the bytes came from. The URL is the point of this epic.
    await expect(row.locator('.ui-record-row__detail')).toContainText(skillURL)
    await expect(row.locator('.ui-record-row__detail')).toContainText(SKILL_NAME)

    // ── AND IT ARRIVES OFF, SO THE PERSON OPENS IT AND TURNS IT ON ────────
    // The bytes came from outside, so the skill lands inert and the assistant
    // is not offered it until somebody has looked (nocx-0bsa4.2, design §8).
    // That step is part of this happy path rather than a detour: without it
    // the ask below would answer from a skill nobody in this test ever
    // reviewed, which is the state the whole design exists to remove.
    //
    // It is turned on FROM THE CARD (nocx-0bsa4.3), which is where §8 puts
    // the decision: the person opens the skill, sees what it is made of with
    // the file in front of them, and flips the switch beside it. Doing it
    // from the row would still pass and would prove less — the look would not
    // be in the path at all.
    //
    // From the COLLECTION row and not from `row` above: `.ui-record-row` fills
    // CollectionRow's info slot, and both the actions and the state cell hang
    // off the region on the other side of it, so they are sibling subtrees and
    // never descendants. record-row.css says so where the cell is declared;
    // this is that warning arriving as a failing locator.
    const collectionRow = page
      .locator('.ui-collection-row')
      .filter({ has: page.locator('.ui-record-row__title', { hasText: SKILL_NAME }) })
    const rowSwitch = collectionRow.locator('.ui-record-row__state [role="switch"]')
    await expect(rowSwitch).not.toBeChecked()

    await collectionRow.getByRole('button', { name: 'Open', exact: true }).click()
    const card = page.getByRole('dialog', { name: SKILL_NAME })
    await expect(card).toBeVisible({ timeout: 10_000 })
    // The bytes, on the card, before the decision: this skill carries one
    // file, so the card shows that file rather than a list with one row in it.
    await expect(card.locator('.ui-code-block')).toContainText(SKILL_BODY, { timeout: 15_000 })
    await expect(card).toContainText('This skill is off')

    const cardSwitch = card.locator('[role="switch"]')
    await expect(cardSwitch).not.toBeChecked()
    await cardSwitch.click()
    await expect(cardSwitch).toBeChecked({ timeout: 15_000 })
    await card.getByRole('button', { name: 'Close', exact: true }).click()
    await expect(card).toBeHidden({ timeout: 10_000 })

    // One control over one fact: the row's switch is the card's switch, and
    // the list behind the card has caught up with the decision taken on it.
    await expect(rowSwitch).toBeChecked({ timeout: 15_000 })

    // ── AND A LATER ASK IN ANOTHER PANE ANSWERS FROM IT ────────────────────
    // A newly created terminal session, not another way to address the pane
    // that was already open: the prompt index and the read are both observed
    // from the model requests belonging to this pane.
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
    await page.locator(NEW_TAB).click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(INPUT)).toBeFocused({ timeout: 15_000 })

    fake.setScript({
      chunks: [],
      toolCalls: [
        {
          name: 'skills.read',
          id: 'call_installed_skill_read',
          arguments: { name: SKILL_NAME },
        },
      ],
    })
    fake.setScript({ chunks: answerFromSkillRead })

    const requestBase = fake.requests().length
    await askFromPrompt(page, QUESTION)
    const requests = await fake.waitForRequests(requestBase + 2)
    const proposal = requests[requestBase]
    const followUp = requests[requestBase + 1]

    // The installed skill is in the run's prompt index with its description,
    // and the model proposes the real read by name.
    expect(proposal.body).toContain(SKILL_NAME)
    expect(proposal.body).toContain(SKILL_DESCRIPTION)
    expect(proposal.body).toContain('skills.read')

    const followUpPayload = JSON.parse(followUp.body) as {
      messages?: {
        role?: string
        content?: unknown
        tool_calls?: { function?: { name?: string } }[]
      }[]
    }
    const messages = followUpPayload.messages ?? []
    expect(
      messages.some((message) =>
        message.tool_calls?.some((call) => call.function?.name === 'skills.read'),
      ),
    ).toBe(true)
    const toolResult = messages.find(
      (message) => message.role === 'tool' && typeof message.content === 'string',
    )?.content as string | undefined
    expect(toolResult).toContain(SKILL_BODY)

    await answerFinished(page, QUESTION)
    await expect(answerBlock(page, QUESTION).locator('[data-answer-body]')).toContainText(
      SKILL_BODY,
    )
  })
})
