/**
 * e2e: the assistant PROPOSES a tool and a person is asked (nocx-aospw).
 *
 * What this file is for. Until now `e2e/fake-openai.ts` could only make the
 * model speak — its `StreamScript` was `{chunks, holdAfter, model}` and
 * nothing else — so no e2e had ever driven a tool call and the approval
 * prompt had no end-to-end coverage at all (`grep -rn "approv" e2e/*.ts`
 * answered nothing). This spec is the check that the new facility is real:
 * a scripted `session.read` proposal, made by the fake model, reaching the
 * policy gate and raising the question a person answers.
 *
 * The seam, named where each half is decided:
 *
 * - The WIRE. FakeOpenAI serves OpenAI-compatible streaming responses to
 *   real pi. Its models.json is written into this backend's disposable HOME
 *   before startup; no endpoint or provider outside that home is used.
 * - The TOOL. `session.read`, declared in `internal/agenttools/registry.go`
 *   with `Effect: observe`, `Resources: [session]` and
 *   `ResourceArg: "sessionId"`.
 * - The GRANT. `WSServer.runGrantFor` mints one grant per run from the
 *   global policy (`internal/app/app.go` wires the store), with the run's
 *   OWN session as the base scope of every row. An unset policy IS a policy
 *   — the zero matrix, which ASKS — so a proposed `session.read` on the run's
 *   own session suspends the run and the backend sends
 *   `agent.approvalRequested` (`internal/transport/ws_agent.go`
 *   suspendForApproval), which `main.tsx` renders as AgentApprovalPrompt.
 *
 * WHY THE SESSION ID IS LEARNED, NEVER INVENTED. The policy's scope check
 * (`internal/assistant/policy.go` inScope) compares a session resource for
 * EXACT IDENTITY against the grant's scopes, so a made-up id is refused
 * before it can ask — the run would fail instead of asking, and the prompt
 * would never appear. Session ids are server-authoritative (AD-7), so the
 * spec reads the one the product itself spelled: the first ask's
 * `agent.ask` frame, off the real socket. That first ask is content-only
 * and deliberately so — it is also this file's evidence that a script
 * without `toolCalls` behaves exactly as it did before.
 *
 * NOT ASSERTED HERE, ON PURPOSE: the prompt's answers and its effect
 * wording. Those belong to the epic's other work and do not exist yet; a
 * check written against them now would be a check written from a plan
 * rather than from the product.
 *
 * Every wait below is on an observable state change — a frame on the wire,
 * a request recorded by the fake, an element on screen. Nothing waits out a
 * duration.
 */
import { expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  switchLanguage,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'
import { configurePiHome, readScreenTool } from './pi-home'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
/** The approval prompt is a kit Prompt: a role="dialog" carrying the
 *  policy title AgentApprovalPrompt gives it. */
const APPROVAL_TITLE = 'This action needs your approval'

const test = base

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-aospw-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start(configurePiHome(root, fake.baseUrl()))
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/**
 * Every `agent.ask` this page sends, as it went over the socket.
 *
 * The subscription is installed BEFORE the navigation, because a listener
 * attached afterwards would miss the socket the app opens on load. The
 * control plane is JSON text and the data plane is binary (AD-1), so the
 * string check is also the plane filter.
 */
function recordAsks(page: Page): string[] {
  const asks: string[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (e) => {
      const p = e.payload
      if (typeof p === 'string' && p.includes('"method":"agent.ask"')) asks.push(p)
    })
  })
  return asks
}

/** Point the page at this file's backend, open the app, wait for the first
 *  tab. */
async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

/** Send the drafted line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter
 *  goes, then Enter is the one send key. Idempotent on the flip. */
async function askFromPrompt(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  // `:visible` on purpose: CM6 keeps a hidden measurement spacer beside the
  // real marker, carrying an identical button.
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
}

test.describe('a proposed tool reaches the approval prompt (nocx-aospw)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('a scripted session.read proposal suspends the run and asks the person', async ({
    page,
  }) => {
    const asks = recordAsks(page)
    await openApp(page)

    // ── Back to the terminal, where a person asks.
    await page.locator(TITLE).first().click()
    const input = page.locator(INPUT)
    await expect(input).toBeVisible({ timeout: 10_000 })

    // ── Ask one: CONTENT ONLY, and unscripted — the fake's own default
    // single 'ok' chunk. Two things come out of it. The answer arrives, so
    // a script with no toolCalls still behaves exactly as it did before;
    // and the ask went over the socket carrying the session id the run's
    // grant will be minted with, which is the only id the policy's scope
    // check will accept.
    const before = fake.requests().length
    await askFromPrompt(page, 'What is on the screen?')
    await fake.waitForRequests(before + 1)
    await expect.poll(() => fake.requests()[before]?.state, { timeout: 15_000 }).toBe('done')
    await expect(page.locator('.cmd-output[data-answer-body]').first()).toContainText('ok', {
      timeout: 15_000,
    })

    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(0)
    const sessionId = (JSON.parse(asks[0]) as { params: { sessionId: string } }).params.sessionId
    expect(sessionId).not.toBe('')

    // ── Ask two: the model PROPOSES session.read on that session. The
    // default policy asks for every effect, so the gate suspends the run
    // and the question reaches the person.
    fake.setScript({
      chunks: [],
      toolCalls: readScreenTool,
    })
    await askFromPrompt(page, 'Read the screen and tell me what is there.')

    // The request that carried the proposal was answered, and the tool was
    // genuinely OFFERED to the model — the grant declares session.read
    // because its session scope is the run's own session.
    const proposal = await fake.waitForRequests(before + 2)
    expect(proposal[before + 1].body).toContain('session.read')

    // THE POINT: the prompt is on screen and it names the tool.
    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 20_000 })
    await expect(prompt).toContainText('session.read')

    // A settled question must disappear instead of advertising an answer
    // the backend can no longer accept. Denying avoids another tool proposal.
    await prompt.getByRole('button', { name: /^Deny once/ }).click()
    await expect(prompt).toBeHidden()
  })

  // The same question, asked in Russian (nocx-dej6.3.8's review, defect 1).
  //
  // The prompt's title map and its width list were module-level literals:
  // `t()` ran when the module was imported, which is BEFORE main.tsx
  // bootstraps the locale, so a window reading Russian still showed the
  // question in English — and the widths with it. Nothing but a real switch
  // followed by a real question shows it, because the defect is invisible in
  // English and invisible to a unit test that mounts after the switch.
  test('the question and its widths follow the language a person switched to', async ({ page }) => {
    await openApp(page)

    // ── Switch the language through the surface that owns it. The helper is
    // the harness's, so the way back is the harness's too: `restoreEnglishLanguage`
    // runs after every test, on the failure path as well, which is what keeps
    // this persisted setting from reaching the next spec in the shard.
    await switchLanguage(page, 'ru')

    // ── Back to the terminal, and ask again. Real pi uses this file's
    // disposable provider, configured before the backend starts.
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
    fake.setScript({
      chunks: [],
      toolCalls: readScreenTool,
    })
    await askFromPrompt(page, 'Прочитай экран и скажи, что там.')

    // THE POINT: the title and the widths are Russian. The button's
    // accessible name carries the width, and `Allow` stays English because
    // the verb is the surface's own residue — what these assertions pin is
    // that the string the catalogue owns is resolved where it is READ.
    const prompt = page.getByRole('dialog', { name: 'Это действие требует вашего согласия' })
    await expect(prompt).toBeVisible({ timeout: 20_000 })
    await expect(prompt).toContainText('один раз')
    await expect(prompt).toContainText('в этом сеансе')

    // Leaving the window English is the harness's job, not this test's: the
    // afterEach in e2e/harness.ts restores the language after every test,
    // which is the only version of this that also runs when the assertions
    // above fail. Nothing here reads an English name while Russian is on
    // screen — `switchLanguage` drives the rail by `data-item`, the Go section
    // name, and the option by its endonym.
  })
})
