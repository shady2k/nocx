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
 * - The WIRE. A proposal is one `chat.completion.chunk` frame whose
 *   `delta.tool_calls` carries `{id, type:"function", function:{name,
 *   arguments}}` and whose `finish_reason` is `tool_calls`. That shape is
 *   not read off the OpenAI docs — it is matched against the two Go fakes
 *   this repo already parses with (`internal/assistant/policy_test.go`
 *   streamToolCalls, `internal/transport/ws_readscreen_test.go`
 *   streamToolCallChunk), because eino's openai adapter is the single
 *   consumer of all three and a drift would be a second protocol variant
 *   with a delay fuse.
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
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
/** The approval prompt is a kit Prompt: a role="dialog" carrying the
 *  policy title AgentApprovalPrompt gives it. */
const APPROVAL_TITLE = 'This action needs your approval'

const test = base

/** One nonce per file, so this file's endpoint name and markers are unique
 *  in the whole run. */
const nonce = Date.now().toString(36)

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-aospw-e2e-'))
  // `true` = no Secret Service for this backend: the container has no
  // keychain to ask, and the derived content key makes the vault available
  // without user setup — the arrangement agent-ask.spec.ts uses.
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
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

    // ── The endpoint and the model that answers, through the surfaces a
    // person uses. A fresh home has no vault, so the first save stops on
    // the setup sheet and is retried once the vault exists; createAiEndpoint
    // reads which of the two happened rather than assuming.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(SETTINGS_AI_NAV).click()
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
    const endpointName = `E2E Tools ${nonce}`
    await createAiEndpoint(page, {
      name: endpointName,
      baseUrl: fake.baseUrl(),
      models: ['e2e-model'],
      key: `e2e-key-${nonce}`,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await page.locator(SETTINGS_ROLES_NAV).click()
    await setDefaultModel(page, endpointName, 'e2e-model')

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
      toolCalls: [{ name: 'session.read', arguments: { sessionId } }],
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
  })
})
