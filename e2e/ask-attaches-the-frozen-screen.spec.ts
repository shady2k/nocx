/**
 * e2e: the person asks about a full-screen program and the MODEL IS TOLD the
 * screen came with the question (nocx-hp8p2.4).
 *
 * The sibling specs (ask-about-a-running-command, ask-about-full-screen-program)
 * script the fake model to call session.read, so they prove the frame is
 * READABLE. Neither proves the model is ever told there is anything to read —
 * and a model that is not told does not ask. The owner's report is exactly
 * that: `top` on the screen, Ctrl+Enter, "Привет! А что это?", and an answer
 * written from nothing at all, under a chip reading "marked for the question · 0".
 *
 * So this spec asserts the two halves of being told, and scripts NO tool call:
 *  - the chip a person reads says the frozen screen is attached, and
 *  - the system prompt the backend built names an automatic attachment with
 *    the id session.read accepts.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  promptReady,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const GRID = '.pane.active .xterm-live-container'
const GRANT_CHIP = '.pane.active .nocx-editor-grant'
const FREEZE = '.pane.active .nocx-editor [role="status"]'
const RUNNING_BLOCK = '.pane.active .cmd-block.cmd-block-running'
const APPROVAL_TITLE = 'This action needs your approval'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'

const test = base
const nonce = Date.now().toString(36)
const INITIAL = `NOCX-FROZEN-${nonce}`
const READY = `frozen-ready-${nonce}`
const ENDPOINT_NAME = `E2E Frozen Attachment ${nonce}`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
let fixtureDir = ''
let scriptPath = ''
let topLikePath = ''
let busyPath = ''
let exitPath = ''

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-hp8p2-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
  fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-hp8p2-job-'))
  scriptPath = join(fixtureDir, 'fullscreen-job.sh')
  topLikePath = join(fixtureDir, 'top-like-job.sh')
  busyPath = join(fixtureDir, 'busy-job.sh')
  exitPath = join(fixtureDir, 'exit')
  // `top`, reduced to what this spec is about: a program that takes the
  // alternate screen, paints, and keeps the pty until released.
  writeFileSync(
    scriptPath,
    [
      `printf '\\033[?1049h'`,
      `printf '%s' '${INITIAL}'`,
      `printf '\\033]9;%s\\007' '${READY}'`,
      `while [ ! -e '${exitPath}' ]; do sleep 0.1; done`,
      `printf '\\033[?1049l'`,
      'exit 0',
      '',
    ].join('\n'),
  )
  // AND THE PROGRAM THE OWNER ACTUALLY RAN. procps `top` owns the whole
  // viewport and never takes the alternate screen — it clears and paints in
  // the normal buffer — which is precisely the case the alternate-buffer gate
  // excluded. Same journey, no 1049.
  // AND `top` AS IT ACTUALLY BEHAVES: a program that keeps REPAINTING. The
  // owner's timeout arrived under a screen being rewritten every few seconds,
  // and a renderer that is mid-write is exactly what a capture fence waits
  // for — so a fixture that paints once and falls silent cannot reproduce it.
  writeFileSync(
    busyPath,
    [
      `printf '\\033[2J'`,
      // FILL THE LAST ROW TO THE RIGHT EDGE. A row that ends exactly at the
      // width leaves xterm's cursor HANGING past the last column awaiting a
      // wrap — cursorX === cols — which is the resting state of any real
      // full-screen TUI and the state that made the frame unsendable
      // (nocx-hp8p2.4). A fixture printing short rows parks the cursor at
      // column 0 and cannot reproduce it.
      `cols=$(stty size | cut -d' ' -f2)`,
      `pad=$(printf '%*s' "$cols" '' | tr ' ' '#')`,
      `printf '\\033]9;%s\\007' '${READY}'`,
      `while [ ! -e '${exitPath}' ]; do`,
      `  printf '\\033[H'`,
      `  i=1; while [ "$i" -le 20 ]; do printf 'TUI ROW %02d ${INITIAL}\\r\\n' "$i"; i=$((i+1)); done`,
      `  printf '%s' "$pad"`,
      `  sleep 0.3`,
      `done`,
      'exit 0',
      '',
    ].join('\n'),
  )
  writeFileSync(
    topLikePath,
    [
      `printf '\\033[2J\\033[H'`,
      `i=1; while [ "$i" -le 20 ]; do printf 'TUI ROW %02d ${INITIAL}\\r\\n' "$i"; i=$((i+1)); done`,
      `printf '\\033]9;%s\\007' '${READY}'`,
      `while [ ! -e '${exitPath}' ]; do sleep 0.1; done`,
      'exit 0',
      '',
    ].join('\n'),
  )
})

test.beforeEach(() => rmSync(exitPath, { force: true }))

test.afterAll(async () => {
  writeFileSync(exitPath, 'release\n')
  backend?.stop()
  await fake?.stop()
})

interface SpecRecord {
  raised: string[]
}
declare global {
  interface Window {
    __nocxFrozenAttachmentSpec?: SpecRecord
  }
}

async function recordSignals(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const rec: SpecRecord = { raised: [] }
    window.__nocxFrozenAttachmentSpec = rec
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (this: WebSocket, data: unknown) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as { method?: string; params?: { body?: string } }
          if (msg.method === 'notify.raise' && typeof msg.params?.body === 'string') {
            rec.raised.push(msg.params.body)
          }
        } catch {
          // Not ours; the data plane is binary and never lands here.
        }
      }
      return send.call(this, data as string)
    }
  })
}

async function recorded(page: Page): Promise<SpecRecord> {
  return page.evaluate(() => window.__nocxFrozenAttachmentSpec ?? { raised: [] })
}

/** The answer is derived from what the product actually sent, so it can only
 *  name the screen if the tool result carried it. */
function answerFromTheToolResult(body: string): string[] {
  let seen = ''
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: string }[] }
    seen = (parsed.messages ?? [])
      .filter((m) => m.role !== 'system' && m.role !== 'user')
      .map((m) => m.content ?? '')
      .join('\n')
  } catch {
    seen = ''
  }
  return seen.includes(INITIAL) ? [`The screen shows ${INITIAL}.`] : ['I could not see the screen.']
}

function modeIndicator(page: Page) {
  return page.locator('.pane.active .ui-mode-indicator:visible')
}

async function useTarget(page: Page, target: 'shell' | 'agent'): Promise<void> {
  await page.locator(INPUT).click()
  const indicator = modeIndicator(page)
  if ((await indicator.getAttribute('data-target')) !== target) {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', target, { timeout: 10_000 })
  }
}

test.describe('the assistant is told the frozen screen came with the question (nocx-hp8p2.4)', () => {
  test.use({ viewport: { width: 1600, height: 900 } })

  for (const program of [
    {
      name: 'a full-screen program on the alternate screen',
      script: () => scriptPath,
      fullscreen: true,
      endpoint: `${ENDPOINT_NAME} — alt`,
    },
    {
      name: 'a full-viewport program in the normal buffer (the `top` shape)',
      script: () => topLikePath,
      fullscreen: false,
      endpoint: `${ENDPOINT_NAME} — normal`,
    },
  ]) {
    test(`summoning over ${program.name} attaches its screen to the ask`, async ({ page }) => {
      test.setTimeout(120_000)
      await recordSignals(page)
      await bindEndpoint(page, endpoint)
      await page.goto('/')
      await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
      await appReadyForInput(page)

      await page.keyboard.press('Meta+,')
      await settingsReady(page)
      await page.locator(SETTINGS_AI_NAV).click()
      await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
      await createAiEndpoint(page, {
        name: program.endpoint,
        baseUrl: fake.baseUrl(),
        models: ['e2e-model'],
        key: `e2e-key-${nonce}`,
        vaultPassphrase: `vault-pass-${nonce}`,
      })
      await page.locator(SETTINGS_ROLES_NAV).click()
      await setDefaultModel(page, program.endpoint, 'e2e-model')
      await page.locator(TITLE).first().click()
      await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
      await promptReady(page)

      await useTarget(page, 'shell')
      await page.keyboard.type(`sh '${program.script()}'`)
      await page.keyboard.press('Enter')
      await expect(page.locator(RUNNING_BLOCK)).toHaveCount(1, { timeout: 20_000 })
      await expect
        .poll(async () => (await recorded(page)).raised.includes(READY), {
          timeout: 45_000,
          message: 'the full-screen fixture never painted its screen',
        })
        .toBe(true)
      if (program.fullscreen) {
        await expect(page.locator(GRID)).toHaveClass(/live-fullscreen/, { timeout: 20_000 })
      }

      await page.locator(GRID).click()
      await page.keyboard.press('ControlOrMeta+Enter')
      await expect(page.locator(FREEZE)).toBeVisible({ timeout: 15_000 })

      // HALF ONE: what the person reads. The chip is the product's own account
      // of what travels with the next question.
      await expect(page.locator(GRANT_CHIP)).toHaveAttribute(
        'aria-label',
        /frozen screen attached automatically/,
        { timeout: 10_000 },
      )
      await expect(page.locator(GRANT_CHIP)).toContainText('+ screen', { timeout: 10_000 })

      // AND THE CHIP STAYS IN ITS BOX. The chip had a fixed 13rem width and
      // no overflow rule, so the longer sentence was PAINTED straight over
      // the model chip and the freeze badge beside it (nocx-hp8p2.4).
      //
      // Measured as the content against its own box, not box against box: an
      // overflowing element keeps its declared border box, so neighbouring
      // rects stay disjoint while the row is unreadable. scrollWidth is the
      // content's width, so this is red both for the old paint-over-the-row
      // and for a label that only fits by being ellipsised away.
      const chipBox = await page.evaluate(() => {
        const chip = document.querySelector('.pane.active .nocx-editor-grant')
        if (!chip) return null
        return {
          content: chip.scrollWidth,
          box: chip.clientWidth,
          label: chip.textContent ?? '',
        }
      })
      expect(chipBox, 'the grant chip was not found').not.toBeNull()
      expect(
        chipBox!.content,
        `the grant chip does not fit its own box: ${chipBox!.label}`,
      ).toBeLessThanOrEqual(chipBox!.box + 1)

      // AND THE SEAM ACTUALLY DRAGS. A separator that is in the DOM, has the
      // right role and answers the keyboard can still be impossible to grab:
      // the kit's horizontal variant is stretched by its container, and a
      // container that does not stretch it leaves a zero-width strip nobody
      // can hit (nocx-hp8p2.6). Only a real pointer in a real layout can say
      // which of those two the person has.
      const seam = page.locator('.pane.active .nocx-summon-stack [role="separator"]')
      await expect(seam).toBeVisible({ timeout: 10_000 })
      const box = await seam.boundingBox()
      expect(box, 'the seam has no box to grab').not.toBeNull()
      expect(box!.width, 'the seam collapsed to nothing along its own axis').toBeGreaterThan(20)

      const answers = page.locator('.pane.active .nocx-summon-answers')
      const heightBefore = (await answers.boundingBox())!.height
      await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
      await page.mouse.down()
      await page.mouse.move(box!.x + box!.width / 2, box!.y - 120, { steps: 8 })
      await page.mouse.up()
      const heightAfter = (await answers.boundingBox())!.height
      expect(heightAfter, 'dragging the seam up gave the assistant no room').toBeGreaterThan(
        heightBefore + 40,
      )

      // HALF TWO: what the model reads. No tool call is scripted — this is a
      // model that answers rather than looking, which is the owner's model.
      fake.setScript({ chunks: ['I can see the pinned screen.'] })
      const base = fake.requests().length
      await page.locator(INPUT).fill('What is this?')
      await page.keyboard.press('Enter')
      const requests = await fake.waitForRequests(base + 1, 30_000)
      const body = requests.at(-1)!.body
      const parsed = JSON.parse(body) as { messages?: { role?: string; content?: string }[] }
      const system = (parsed.messages ?? []).find((m) => m.role === 'system')?.content ?? ''
      expect(system).toContain('A frozen screen was attached automatically')

      // Release the program and let the pane come back to a prompt: the
      // backend outlives this test and restores its tabs into the next one.
      writeFileSync(exitPath, 'release\n')
      await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 30_000 })
    })
  }

  test('the approved session.read reaches the renderer and comes back with the screen', async ({
    page,
  }) => {
    test.setTimeout(120_000)
    await recordSignals(page)
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)

    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(SETTINGS_AI_NAV).click()
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
    await createAiEndpoint(page, {
      name: `${ENDPOINT_NAME} — approved`,
      baseUrl: fake.baseUrl(),
      models: ['e2e-model'],
      key: `e2e-key-${nonce}`,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await page.locator(SETTINGS_ROLES_NAV).click()
    await setDefaultModel(page, `${ENDPOINT_NAME} — approved`, 'e2e-model')
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
    await promptReady(page)

    await useTarget(page, 'shell')
    await page.keyboard.type(`sh '${busyPath}'`)
    await page.keyboard.press('Enter')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(1, { timeout: 20_000 })
    await expect
      .poll(async () => (await recorded(page)).raised.includes(READY), {
        timeout: 45_000,
        message: 'the full-viewport fixture never painted its screen',
      })
      .toBe(true)

    await page.locator(GRID).click()
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(page.locator(FREEZE)).toBeVisible({ timeout: 15_000 })

    // The id the product itself attached — the same one the model is handed
    // in the system prompt and hands back to session.read.
    const itemId = await page.evaluate(
      () =>
        document
          .querySelector('.pane.active .cmd-block.cmd-block-running')
          ?.getAttribute('data-entry-id') ?? '',
    )
    expect(itemId, 'the running block carries no item id').not.toBe('')

    // The model does what the owner's model did: it reads the attached item
    // by id. The default policy ASKS, so the run suspends on the approval —
    // the path the sibling specs skip by setting the effect to Allowed.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.read', arguments: { id: itemId } }],
    })
    fake.setScript({ chunks: (body: string) => answerFromTheToolResult(body) })
    const base = fake.requests().length
    await page.locator(INPUT).fill('What is this?')
    await page.keyboard.press('Enter')

    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 30_000 })
    await prompt.getByRole('button', { name: /once/i }).first().click()

    // THE POINT: the approved read reaches the renderer and comes back. It
    // timed out for the owner — "the assistant's session.read call did not
    // finish: request timed out waiting for the renderer" — so this asserts
    // the tool result, not merely that an answer appeared.
    const requests = await fake.waitForRequests(base + 2, 60_000)
    const afterTool = requests.at(-1)!.body
    const parsed = JSON.parse(afterTool) as { messages?: { role?: string; content?: string }[] }
    const toolResults = (parsed.messages ?? [])
      .filter((m) => m.role !== 'system' && m.role !== 'user')
      .map((m) => m.content ?? '')
      .join('\n')
    expect(toolResults, 'the renderer never answered the approved read').toContain(INITIAL)

    writeFileSync(exitPath, 'release\n')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 30_000 })
  })
})
