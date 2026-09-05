/**
 * e2e: a person installs a skill by ASKING for it, from the page they
 * actually have (nocx-ojfuc.5 — the epic's happy path).
 *
 * WHAT IT WATCHES, in the order the epic's criterion names it. A person
 * pastes a DOCS PAGE into the conversation — an HTML page, the shape
 * `https://www.agentmail.to/docs/integrations/skills` is — and says install
 * the skill from it. The assistant reads that page (one approval, because
 * reaching off this machine is a cross-boundary act and the policy is unset),
 * resolves it to the address that answers with the document itself, and
 * proposes the install. The approval window that follows names the RESOLVED
 * address and not the page, the skill's own name and description, the digest
 * the write is bound to, and EVERY file that would land with its bytes
 * readable before answering. Approving writes the bundle — SKILL.md and its
 * support file — under the installed root: the support file byte for byte,
 * and SKILL.md carrying the approved body under frontmatter the product
 * re-serialises on purpose (see the disk assertions). The row
 * in Settings afterwards records the address and the digest, through the
 * product's own write. And the skill is OFF: turning it on is a separate act,
 * performed here after the install, from the card.
 *
 * WHY IT CAN ONLY BE AN E2E CHECK. Every piece is unit-covered
 * (`internal/assistant/skills_install_test.go` drives the pipeline,
 * `internal/skill/install_test.go` the store, `agent-approval-prompt.test.tsx`
 * the window, `skills-management.spec.ts` the row). What none of them can ask
 * is whether the chain HOLDS: the question travels model → kernel resolution →
 * a network read of an address the model chose → the wire's `install` block →
 * a window → an answer → a second fetch bound to the first → bytes on disk →
 * a Settings row reading a document the install wrote. There is no seam short
 * of the whole product where "a person asked for a skill and got it" is a
 * question that can be asked at all.
 *
 * ══ THE SCRIPTED MODEL: WHAT IT PROVES AND WHAT IT CANNOT ══════════════════
 *
 * The model here is `FakeOpenAI`, and it is SCRIPTED. That proves the WIRING
 * and never the model's JUDGEMENT. Nothing in this file is evidence that a
 * real model would pick the right link off a real docs page, would prefer the
 * raw document to the page, or would reach for `skills.install` at all rather
 * than answering with instructions. Those are properties of a model and of
 * the tool's description, and no automated check in this repo can hold them.
 *
 * What IS proven about the resolution, and it is the load-bearing half: the
 * address the install used was DERIVED FROM THE PAGE'S BYTES AS THEY REACHED
 * THE MODEL, and was never typed by the person. The script is a function of
 * the request body (`fake-openai.ts` StreamScript): it looks for the fetch
 * result among the messages, finds the document address inside the page text
 * it carries, and calls `skills.install` with what it found there. If the
 * fetch never happened, if its result never reached the model, or if the page
 * text arrived without the address in it, the extraction yields nothing, no
 * install is ever proposed, and this spec goes red on the window that never
 * opened. The question a person types is asserted to name the page and NOT to
 * contain the document's address, so the person genuinely never constructed
 * one.
 *
 * AND THE PAGE STATES THE ADDRESS IN ITS TEXT, not only in an `href`, because
 * `fetch.url` renders HTML by extracting TEXT NODES (internal/assistant
 * render.go, `extractHTMLText`) — attributes, link targets included, are
 * dropped before the model ever sees them. So a docs page whose raw document
 * is reachable only through a link is a page no model can resolve, however
 * good it is. That is a product fact this spec had to be written around, and
 * it is recorded here rather than hidden inside a fixture: this check proves
 * the path for a page that SAYS the address, and says nothing about one that
 * only links it.
 *
 * TWO SERVERS, both owned by this spec process, both on 127.0.0.1, exactly as
 * the spec this one replaces did (`skill-install-url.spec.ts`, deleted with
 * the Settings paste box in nocx-ojfuc.4). `FakeOpenAI` is the model the
 * assistant dials; a plain `node:http` server holds the docs page, the
 * document and its support file. `http://` reaches them because
 * internal/httppolicy permits http to loopback, which is the intended shape
 * for a local endpoint.
 *
 * TWO APPROVALS, AND BOTH ARE ANSWERED `once`. The policy matrix is
 * deliberately untouched — an unset matrix ASKS, and being asked is what this
 * spec watches. A wider answer would be a real defect in the check rather
 * than a shortcut: `skills.install` is decided on its worst class, which is
 * the same cross-boundary row the fetch is decided on, so "allow in this
 * session" on the FIRST question would install the skill with no second
 * question at all — and the criterion is about that second question.
 *
 * WHAT WOULD MAKE THIS LIE, and what is done about each. A comparison against
 * an element that is not there passes by measuring nothing, so every count is
 * asserted BEFORE the text it qualifies — the two file readouts before their
 * bytes, the row before its evidence lines. Bytes are compared with
 * `textContent` and never `toHaveText`, which normalises whitespace: "the
 * file that landed" is a claim about bytes. The digest and the document's
 * bytes are read OFF THE WINDOW and compared against the row and the disk
 * afterwards, so the last assertions are relationships rather than two
 * constants of this file agreeing with each other. And every wait is an
 * observable state change — a dialog, a row, a completed turn, a recorded
 * model request. This spec deliberately contains no waitForTimeout.
 *
 * WHAT IT DOES NOT PROVE, beyond the model's judgement above: nothing here
 * exercises a scan finding riding along with a bundled file (that is
 * `read-every-byte.spec.ts` and nocx-872jc.4), the refusal when the bytes
 * move between the question and the answer (`internal/skill/install_test.go`),
 * or a page served over https by anything other than this fixture.
 *
 * ══ WHAT IT FOUND, AND WHY IT IS RED ═══════════════════════════════════════
 *
 * THE ANSWERS TO AN INSTALL QUESTION ARE OUT OF REACH IN AN ORDINARY WINDOW.
 * Measured, at the viewport below: the question is 977px tall in a 900px
 * window, and `.ui-prompt` (frontend/src/styles/components/prompt.css) has no
 * max-height while `.ui-prompt-overlay` — `position: fixed; inset: 0` — has no
 * overflow, so there is nothing to scroll and the part that falls past the
 * bottom edge simply cannot be reached. On a developer machine that is all
 * three DENY answers; in the e2e container — CI's own image, whose font
 * metrics run taller — the question is 1063px and ALL SIX answers are off the
 * bottom, allow and deny alike. Before this assertion existed the click sat
 * retrying against `scrollIntoViewIfNeeded` for four minutes and reported a
 * timeout that named none of it.
 *
 * It is not a fixture's fault and not this viewport's: an install question is
 * the tallest question nocx asks — every file that would land, with its bytes
 * — and this bundle is two small files. The skill this epic started from
 * (AgentMail) names SEVEN. Escape still refuses, which is the narrowest
 * refusal there is; what a person cannot do is DECIDE.
 *
 * So this spec is red on the last step of the epic's happy path, and that is
 * the report rather than a defect in the check: everything before the answer
 * passes, and the same spec passes end to end in a taller window. Fixing it
 * belongs to whoever owns the kit — a max-height and an internal scroll on
 * the prompt's body, which is a change every prompt in the app pays for and
 * therefore not one to make inside this bead.
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { createServer, type Server } from 'node:http'
import { mkdtempSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  documentDir,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type ScriptedToolCall } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_SKILLS_NAV = '.ui-grouped-nav__item[data-item="skills"]'
const ASSISTANT_GROUP = '.ui-grouped-nav__group[data-group="assistant"]'
const APPROVAL_TITLE = 'This action needs your approval'

const ENDPOINT_NAME = `E2E Ask Install ${nonce}`
const SKILL_NAME = `pager-drill-${nonce}`
const SKILL_DESCRIPTION = `What to do when the pager goes off ${nonce}`
const SKILL_FILE = 'SKILL.md'
const CHECKLIST_FILE = 'references/checklist.md'

/** The support file the document names. `references/` is one of the two
 *  directories a bundle may reach into (internal/skill/bundle.go), and a
 *  second file is the whole reason the manifest exists: a person told only
 *  about SKILL.md while another file lands beside it is approving a name. */
const CHECKLIST_BODY = [
  '# Checklist',
  '',
  '1. Acknowledge the page.',
  `2. Post in the incident channel (${nonce}).`,
  '',
].join('\n')

/** The document the fixture serves, whole — frontmatter included, because
 *  that is what SKILL.md is and what the approval window shows. The link is
 *  what makes the bundle two files: bundle.go reads the document's own body
 *  for what belongs to it, and never enumerates a repository. */
const SKILL_DOCUMENT = [
  '---',
  `name: ${SKILL_NAME}`,
  `description: "${SKILL_DESCRIPTION}"`,
  '---',
  '',
  `Acknowledge the page, then work through [the checklist](${CHECKLIST_FILE}).`,
  '',
].join('\n')

/** A sentence that appears only in the docs page, so a request body carrying
 *  it can only be carrying the fetch of that page. */
const PAGE_MARKER = `PAGER_DRILL_DOCS_${nonce}`

const PAGE_PATH = '/docs/integrations/skills'
const RAW_DIR = `/raw/pager-drill/${nonce}`
const RAW_PATH = `${RAW_DIR}/${SKILL_FILE}`
const CHECKLIST_PATH = `${RAW_DIR}/${CHECKLIST_FILE}`

let backend: VaultBackend
let fake: FakeOpenAI
let fixture: Server
let endpoint: { port: number; token: string }
/** The page a person has. This is the ONLY address the question names. */
let pageURL = ''
/** The address that answers with the document. This spec knows it because it
 *  serves it; the model is never told it, and the person never types it. */
let documentURL = ''
/** What the scripted model actually extracted from the page's bytes. Recorded
 *  by the script, asserted below — evidence that the address the install used
 *  came out of the page rather than out of this file. */
let resolvedByTheModel: string | null = null

test.describe.configure({ mode: 'serial' })
test.setTimeout(240_000)

/** The docs page, in the shape a person meets one: prose, the repository, and
 *  the address of the document itself both as text and as a link. See the
 *  header for why the text matters and the link cannot. */
function docsPage(): string {
  return [
    '<!doctype html>',
    '<html><head><title>Skills — pager drill</title></head><body>',
    '<h1>Skills</h1>',
    `<p>${PAGE_MARKER}</p>`,
    '<p>The pager drill skill lives in the <code>pager-drill</code> repository.',
    'Its document is served at:</p>',
    `<pre><code>${documentURL}</code></pre>`,
    `<p><a href="${documentURL}">SKILL.md</a></p>`,
    '</body></html>',
  ].join('\n')
}

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()

  fixture = createServer((req, res) => {
    // Three addresses, and everything else is 404: what lands is what this
    // fixture published, never whatever happened to answer.
    if (req.url === PAGE_PATH) {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
      res.end(docsPage())
      return
    }
    if (req.url === RAW_PATH) {
      res.writeHead(200, { 'Content-Type': 'text/markdown; charset=utf-8' })
      res.end(SKILL_DOCUMENT)
      return
    }
    if (req.url === CHECKLIST_PATH) {
      res.writeHead(200, { 'Content-Type': 'text/markdown; charset=utf-8' })
      res.end(CHECKLIST_BODY)
      return
    }
    res.writeHead(404).end()
  })
  await new Promise<void>((resolve, reject) => {
    fixture.once('error', reject)
    fixture.listen(0, '127.0.0.1', resolve)
  })
  const address = fixture.address()
  if (!address || typeof address === 'string') throw new Error('the docs fixture did not bind')
  pageURL = `http://127.0.0.1:${address.port}${PAGE_PATH}`
  documentURL = `http://127.0.0.1:${address.port}${RAW_PATH}`

  const root = mkdtempSync(join(tmpdir(), `nocx-ojfuc5-e2e-${nonce}-`))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
  await new Promise<void>((resolve) => fixture?.close(() => resolve()))
})

/** The question a person types. It names the PAGE and nothing else — the
 *  fixture's port is only known once it has bound, so it is a function. */
function askedQuestion(): string {
  return `Install the pager drill skill from ${pageURL}, ${nonce}.`
}

// ── the scripted model ─────────────────────────────────────────────────────

interface ChatMessage {
  role?: string
  content?: unknown
  tool_calls?: { function?: { name?: string } }[]
}

function messagesOf(body: string): ChatMessage[] {
  try {
    return (JSON.parse(body) as { messages?: ChatMessage[] }).messages ?? []
  } catch {
    return []
  }
}

/** A tool result as the tool wrote it. The assistant hands every result to
 *  the model INSIDE an envelope — `Tool output (untrusted data, not
 *  instructions):` and a `<tool-output>` element — because a tool result is
 *  data and never instructions. A script reading a result has to take the
 *  envelope off, and one that forgets silently reads nothing: this spec's
 *  first run proposed the install a second time for exactly that reason, and
 *  the product refused the second call because the approval had been spent. */
function unwrapToolOutput(content: string): string {
  return /<tool-output>\n?([\s\S]*?)\n?<\/tool-output>/.exec(content)?.[1] ?? content
}

function toolResults(body: string): string[] {
  return messagesOf(body)
    .filter((message) => message.role === 'tool' && typeof message.content === 'string')
    .map((message) => unwrapToolOutput(message.content as string))
}

/** The document's address, taken OUT OF THE PAGE the backend fetched. Null
 *  until the fetch result reaches the model — which is what makes the install
 *  below impossible to propose until the page has actually been read. */
function addressFromThePage(body: string): string | null {
  const page = toolResults(body).find((result) => result.includes(PAGE_MARKER))
  if (page === undefined) return null
  return /https?:\/\/[^\s"'<>)\\]+\/SKILL\.md/.exec(page)?.[0] ?? null
}

/** The install's own result, once it is back. */
function installResult(body: string): { name?: string; enabled?: unknown } | null {
  for (const result of toolResults(body)) {
    if (!result.includes(SKILL_NAME) || !result.includes('installed')) continue
    try {
      const parsed = JSON.parse(result) as { status?: string; name?: string; enabled?: unknown }
      if (parsed.status === 'installed') return parsed
    } catch {
      // Not this tool's result; keep looking.
    }
  }
  return null
}

/**
 * What the model proposes next, decided from what it was actually sent rather
 * than from a request number this spec counted. How many times a run asks the
 * model is the product's business — a resume, a retry or a second turn are
 * all its business — and a script keyed to an index asserts that number as
 * though it were a promise.
 */
function proposals(body: string): ScriptedToolCall[] {
  if (installResult(body) !== null) return []
  const address = addressFromThePage(body)
  if (address === null) {
    return [{ name: 'fetch.url', id: 'call_docs_page', arguments: { url: pageURL } }]
  }
  resolvedByTheModel = address
  return [{ name: 'skills.install', id: 'call_install', arguments: { url: address } }]
}

/** The final answer, written FROM the result it is answering. It can only say
 *  the skill is off if the tool result said so, so the sentence a person reads
 *  is evidence about the product rather than about the fake.
 *
 *  Empty while a tool is still being proposed — the same condition `proposals`
 *  decides on — because a response that carries a tool call writes its content
 *  chunks too, and a sentence emitted beside a proposal would end up in the
 *  answer a person reads. */
function answer(body: string): string[] {
  const result = installResult(body)
  if (result === null) return []
  return [`Installed ${String(result.name)}, and it is ${result.enabled === false ? 'off' : 'on'}.`]
}

// ── the app ────────────────────────────────────────────────────────────────

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

/** The endpoint the assistant dials. Equipment, not what is under test, so it
 *  is arranged through the same Settings surfaces a person uses. The POLICY is
 *  deliberately not touched — an unset matrix asks, and being asked twice is
 *  what this spec is about. */
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

/** The exact characters an element holds. `toHaveText` normalises whitespace,
 *  and every claim below about a file is a claim about bytes. */
function exactText(element: Locator): Promise<string> {
  return element.evaluate((el) => el.textContent ?? '')
}

/** One file's readout inside a scope, addressed by the accessible name that
 *  surface gives the block of bytes — which is also the assertion that it is
 *  labelling the file it is showing. */
function readoutFor(scope: Locator, ariaLabel: string): Locator {
  return scope.locator(`pre.ui-code-block[aria-label="${ariaLabel}"]`)
}

/** A document's body — everything after the closing frontmatter fence, with
 *  the blank line the fence is followed by trimmed off. It exists because the
 *  frontmatter of an installed SKILL.md is the product's own, and the body is
 *  the half that travels unchanged. */
function bodyOf(document: string): string {
  const parts = document.split('\n---\n')
  return parts.slice(1).join('\n---\n').trim()
}

function rowFor(page: Page, name: string): Locator {
  return page
    .locator('.ui-collection-row')
    .filter({ has: page.locator('.ui-record-row__title', { hasText: name }) })
}

test.describe('a person installs a skill by asking for it (nocx-ojfuc.5)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('paste the page, approve what it resolved to, and get the skill — off', async ({ page }) => {
    // THE PERSON NEVER CONSTRUCTS A RAW ADDRESS. Stated as an assertion and
    // not as a comment: the whole epic is that the address in the question is
    // the one they had.
    expect(askedQuestion()).toContain(pageURL)
    expect(askedQuestion()).not.toContain(documentURL)

    await openApp(page)
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await configureAssistant(page)
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })

    // The model, scripted as a function of what it is sent. Four scripts is
    // headroom, not a count: the router answers whichever request arrives,
    // and an exhausted script list answers 'ok', which would fail the
    // assertions below rather than pass them quietly.
    for (let i = 0; i < 4; i += 1) fake.setScript({ chunks: answer, toolCalls: proposals })

    const question = askedQuestion()
    await askFromPrompt(page, question)

    // ══ THE PAGE IS READ, AND THAT IS ITS OWN QUESTION ═════════════════════
    // Reaching off this machine is cross-boundary and the policy is unset, so
    // the person is asked about the address they themselves pasted.
    const readPage = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(readPage).toBeVisible({ timeout: 60_000 })
    await expect(readPage).toContainText(pageURL)
    // `once`, never a standing answer: see the header — a session-wide yes
    // here would install the skill with no second question.
    await readPage.getByRole('button', { name: 'Allow once' }).click()

    // ══ AND THE INSTALL IS PROPOSED, RESOLVED ══════════════════════════════
    // The SECOND question, told from the first by its content rather than by
    // catching the moment the first one closed: only an install question says
    // this sentence, and only this one names this skill.
    const install = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(install).toContainText('The assistant read a skill at an address it resolved', {
      timeout: 90_000,
    })
    await expect(install).toContainText(SKILL_NAME)

    // THE ADDRESS CAME OUT OF THE PAGE. The script extracted it from the
    // fetch result, and the page's own sentence is in a request the model was
    // sent; this is the one place either can be said.
    expect(resolvedByTheModel).toBe(documentURL)
    expect(fake.requests().some((request) => request.body.includes(PAGE_MARKER))).toBe(true)

    // ── WHAT THE PERSON IS BEING ASKED ─────────────────────────────────────
    // A skill, not an address: the rows carry the name, the RESOLVED source
    // and the digest the write is bound to.
    const facts = install.getByLabel('What skills.install would do')
    await expect(facts).toContainText(SKILL_NAME)
    await expect(facts).toContainText(documentURL)
    await expect(facts).toContainText('the address that was fetched')
    // AND NOT THE PAGE. The person pasted a page; what they are deciding on
    // is what it resolved to, and a window naming both would be asking about
    // two things.
    await expect(install).not.toContainText(pageURL)
    // The description, which is the part that lives in the assistant's system
    // prompt afterwards, is stated verbatim rather than as a row.
    await expect(install).toContainText(SKILL_DESCRIPTION)
    // The digest is read OFF THE WINDOW so the Settings card below can be
    // compared against it rather than against a constant of this file.
    const shownDigest = /[0-9a-f]{64}/.exec(await exactText(facts))?.[0] ?? ''
    expect(shownDigest).toMatch(/^[0-9a-f]{64}$/)
    await expect(facts).toContainText('It says nothing about who wrote them')

    // ── EVERY FILE THAT WOULD LAND, AND ITS BYTES ──────────────────────────
    // The count FIRST: with the readouts missing, "each file is readable"
    // would be a loop over nothing.
    await expect(install.locator('.ui-marker-list__text')).toHaveText([SKILL_FILE, CHECKLIST_FILE])
    await expect(install.locator('.ui-file-readout')).toHaveCount(2)
    const documentPre = readoutFor(
      install,
      `${SKILL_FILE}, which installing “${SKILL_NAME}” would write`,
    )
    const checklistPre = readoutFor(
      install,
      `${CHECKLIST_FILE}, which installing “${SKILL_NAME}” would write`,
    )
    await expect.poll(() => exactText(documentPre), { timeout: 15_000 }).toBe(SKILL_DOCUMENT)
    await expect.poll(() => exactText(checklistPre), { timeout: 15_000 }).toBe(CHECKLIST_BODY)
    // Read-only: there is nothing in a readout a person could type into.
    await expect(install.locator('.ui-file-readout input, .ui-file-readout textarea')).toHaveCount(
      0,
    )
    // The bytes the window showed, kept so the files on disk can be compared
    // against WHAT WAS APPROVED rather than against the fixture.
    const approvedDocument = await exactText(documentPre)
    const approvedChecklist = await exactText(checklistPre)

    // ── AND THE PERSON CAN REACH AN ANSWER ────────────────────────────────
    // Asserted, because it is the one thing this window owes a person that
    // its content cannot supply: an install question is the tallest kind
    // nocx asks — every file that would land, with its bytes — and
    // `.ui-prompt` has no max-height while `.ui-prompt-overlay` has no
    // overflow, so a question taller than the window paints its answers past
    // the bottom edge with nothing to scroll. Playwright's
    // `scrollIntoViewIfNeeded` moves nothing there, and the click reports a
    // four-minute timeout that names none of this.
    const answers = await install.evaluate((panel) => {
      const buttons = [...panel.querySelectorAll('button')].filter((button) =>
        /^(Allow|Deny)/.test(button.getAttribute('aria-label') ?? ''),
      )
      return {
        height: Math.round(panel.getBoundingClientRect().height),
        viewport: window.innerHeight,
        offscreen: buttons
          .filter((button) => button.getBoundingClientRect().bottom > window.innerHeight)
          .map((button) => (button.getAttribute('aria-label') ?? '').split(' — ')[0]),
      }
    })
    expect(
      answers.offscreen,
      `the install question is ${answers.height}px tall in a ${answers.viewport}px window and the ` +
        `prompt does not scroll, so these answers cannot be reached: ${answers.offscreen.join(', ')}`,
    ).toEqual([])

    // ── APPROVING IS THE FIRST INTERACTION WITH THIS WINDOW ────────────────
    const allowInstall = install.getByRole('button', { name: 'Allow once' })
    await expect(allowInstall).toBeEnabled()
    await allowInstall.click()
    await expect(install).toBeHidden({ timeout: 30_000 })

    // ══ THE ANSWER SAYS WHAT LANDED, AND THAT IT IS OFF ════════════════════
    // Derived from the tool result, so it can only say `off` if the result
    // did (`enabled` is `const: false` in the tool's contract).
    const turn = page.locator('.pane.active .cmd-block').filter({ hasText: question }).first()
    await expect(turn).toBeVisible({ timeout: 30_000 })
    await expect(turn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
      timeout: 60_000,
    })
    await expect(turn.locator('[data-answer-body]')).toContainText(
      `Installed ${SKILL_NAME}, and it is off.`,
    )

    // ══ THE BUNDLE IS ON DISK, UNDER THE INSTALLED ROOT ════════════════════
    // Both files, and their bytes measured against WHAT THE QUESTION SHOWED
    // rather than against the fixture — the window could have shown anything
    // without writing it.
    const installed = join(documentDir(backend.isolatedHome), 'installed-skills', SKILL_NAME)
    // The support file lands verbatim.
    expect(readFileSync(join(installed, CHECKLIST_FILE), 'utf8')).toBe(approvedChecklist)
    // SKILL.md's FRONTMATTER IS RE-SERIALISED, DELIBERATELY, so this is not a
    // byte comparison of the whole file: `prepareSkill`
    // (internal/skill/write.go, reached from install.go) re-emits `name` and
    // `description` and drops every other key, so that a document cannot
    // carry a `provenance:` of its own and claim the root it sits in.
    // Asserting the served bytes here would be asserting that nocx does NOT
    // do that. What must hold — and is a relationship, not two constants — is
    // that the skill under this name is the one the window described and its
    // body is the body that was approved.
    const landed = readFileSync(join(installed, SKILL_FILE), 'utf8')
    expect(landed.startsWith(`---\nname: ${SKILL_NAME}\n`)).toBe(true)
    expect(landed).toContain(SKILL_DESCRIPTION)
    expect(landed).toContain(bodyOf(approvedDocument))

    // ══ AND THE ROW RECORDS WHAT RESOLVED ══════════════════════════════════
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(`${ASSISTANT_GROUP} ${SETTINGS_SKILLS_NAV}`).click()

    const row = rowFor(page, SKILL_NAME)
    await expect(row).toHaveCount(1, { timeout: 15_000 })
    await expect(row.locator('.ui-badge').first()).toHaveText('installed')
    await expect(row.locator('.ui-record-row__meta-text')).toHaveText(SKILL_DESCRIPTION)
    // The second evidence line: where the bytes came from, written by the
    // product's own install rather than by a fixture.
    await expect(row.locator('.ui-record-row__detail')).toContainText(
      `Installed from ${documentURL}`,
    )
    // And what was written is what is there: a skill whose recorded digest
    // did not match its bytes would say so here.
    await expect(row).not.toContainText('Changed since installation')

    // ══ IT IS OFF, AND TURNING IT ON IS A SEPARATE ACT ═════════════════════
    const rowSwitch = row.locator('.ui-record-row__state [role="switch"]')
    await expect(rowSwitch).not.toBeChecked()

    await row.getByRole('button', { name: 'Open', exact: true }).click()
    const card = page.getByRole('dialog', { name: SKILL_NAME })
    await expect(card).toBeVisible({ timeout: 15_000 })
    // The record of the install, on the card: the address, when it was taken,
    // and the digest — THE ONE THE WINDOW SHOWED, which is what makes this a
    // relationship rather than two constants agreeing.
    const record = card.getByLabel('Where this skill lives')
    await expect(record).toContainText(documentURL)
    await expect(record).toContainText('Taken on')
    await expect(record).toContainText(shownDigest)
    // Every file it carries, in the manifest's own order.
    await expect(card.locator('.ui-record-row__title')).toHaveText([SKILL_FILE, CHECKLIST_FILE], {
      timeout: 15_000,
    })

    // The person looks, then decides. The skill was inert until this click.
    await expect(card).toContainText('This skill is off')
    const cardSwitch = card.locator('[role="switch"]')
    await expect(cardSwitch).not.toBeChecked()
    await cardSwitch.click()
    await expect(cardSwitch).toBeChecked({ timeout: 15_000 })
    await card.getByRole('button', { name: 'Close', exact: true }).click()
    await expect(card).toBeHidden({ timeout: 10_000 })
    await expect(rowSwitch).toBeChecked({ timeout: 15_000 })
  })
})
