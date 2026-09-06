/**
 * e2e: everything a person can do to a skill they already have, on the
 * Settings page (nocx-ojfuc.4, policy design §5).
 *
 * THIS FILE USED TO BE `skill-install-url.spec.ts`, and the surface it watched
 * has been deleted rather than moved. Settings kept management and lost
 * acquisition: the paste box, the classifier in front of it and the candidate
 * picker are gone, because a person hunting for a raw address is exactly the
 * labour the assistant now removes, and two surfaces owning one input is the
 * defect AGENTS.md names most often. What that spec proved about the ROW after
 * the install — that it says where the bytes came from, that a skill from
 * outside arrives off, that the person turns it on from the card after looking
 * — is proved here, so it is kept rather than deleted with the box. What is
 * NOT here and is not lost either: the acquisition happy path itself is the
 * assistant's now (`skills.install`, a tool with an approval window), and it
 * belongs to that half of the epic rather than to this page.
 *
 * WHAT IT WATCHES, which is the epic's second criterion in order: the list,
 * the changed-bytes status, re-approval, the card, the file viewer, the audit,
 * enable, disable, change detection AFTER an edit, and delete. Plus the
 * absence the deletion is about: nowhere on this page a source address can be
 * typed, under any name.
 *
 * THE FIXTURE IS A SKILL ON DISK, in the installed root, with a whole source
 * record for it — address, time and the digest of what that address served —
 * and an ADOPTED digest that does NOT match its bytes. The two digests are
 * different values on purpose: one says what the address gave, the other what
 * nocx took onto disk, and only the second is what change detection compares.
 * Three things follow from that one choice and each is deliberate:
 *
 *   - `installed` provenance and a recorded source are what make the row's
 *     second evidence line exist at all, and that line is what this page is
 *     the only place to read.
 *   - the page therefore OPENS in the changed state, which is the state a
 *     person is most likely to meet and the one no other spec watches. It is
 *     not a contrived digest: any byte moving under a skill produces exactly
 *     it, which the second half of this test then does for real.
 *     Re-approval is what ends it, and after that the status is earned by the
 *     product's own hash rather than by anything written here.
 *   - nothing in this file installs anything, so it cannot pass on the
 *     acquisition path's evidence.
 *
 * TWO FILES, because a skill is not one file and the card's list only exists
 * when there is something to pick between. The deep reading of a file — the
 * bytes verbatim, the scan's mark on the line it matched — belongs to
 * `read-every-byte.spec.ts` and is not repeated here; what this spec asks of
 * the viewer is the thing that spec cannot ask, which is that it is reachable
 * from a row of THIS list and that opening a second file replaces the first.
 *
 * THE AUDIT SPENDS A MODEL CALL, so it needs one: `FakeOpenAI` is the endpoint
 * the assistant dials, configured through the same Settings surfaces a person
 * uses. What is asserted about the reading is what it OWES a person — which
 * model was billed, which files it was about, that it decides nothing and
 * that a scan matching nothing is not a clean bill — rather than a word of
 * the report, which a fake wrote.
 *
 * Every wait is an observable state change: a rail on screen, a row, a dialog,
 * a status, a recorded fake request. This spec deliberately contains no
 * waitForTimeout.
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
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
import { FakeOpenAI } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)

const TITLE = '.nocx-tab-title'
const SETTINGS_NAV = '[aria-label="Settings sections"]'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_SKILLS_NAV = '.ui-grouped-nav__item[data-item="skills"]'
const ASSISTANT_GROUP = '.ui-grouped-nav__group[data-group="assistant"]'

const ENDPOINT_NAME = `E2E Manage ${nonce}`
const SKILL_NAME = `pager-drill-${nonce}`
const SKILL_DESCRIPTION = `What to do when the pager goes off ${nonce}`
const SKILL_FILE = 'SKILL.md'
const NOTES_FILE = 'references/notes.md'
const SKILL_BODY = `Acknowledge the page, then read ${NOTES_FILE} (${nonce}).`
const NOTES_BODY = `# Notes\n\nThe pager rota lives in the runbook (${nonce}).\n`
const SKILL_URL = `https://example.invalid/skills/pager-drill/SKILL.md`
/** What that address served, as an install records it — the digest the
 *  approval question showed. Deliberately a different value from the adopted
 *  digest below: one is what nocx took onto disk, the other is what the
 *  address gave, and a fixture that made them equal could not tell a surface
 *  confusing the two from one that does not. */
const SOURCE_DIGEST = 'b'.repeat(64)
const SKILL_DOCUMENT = [
  '---',
  `name: ${SKILL_NAME}`,
  `description: "${SKILL_DESCRIPTION}"`,
  '---',
  '',
  SKILL_BODY,
  '',
].join('\n')
/** What a person's editor does to a skill after they approved it. */
const EDITED_LINE = `And page the on-call engineer twice (${nonce}).`
const AUDIT_REPORT = `It tells the assistant to acknowledge a page and read a note (${nonce}).`

// ═══════════════════════════════════════════════════════════════════════════
// THE EPIC'S HAPPY PATH (nocx-at05r) — a SEPARATE skill, directory and
// endpoint, on purpose. The test above ends by editing SKILL_NAME's bytes
// and then deleting it from disk (skills.remove); this file's tests run
// serially, so a second test appended after it must not depend on either
// surviving. Nothing below shares a name with anything above, so nothing
// this test does can break the assertions above it, and nothing above can
// leave this test with a skill, an endpoint or a role assignment it didn't
// ask for.
// ═══════════════════════════════════════════════════════════════════════════
const HAPPY_ENDPOINT_NAME = `E2E Happy ${nonce}`
const HAPPY_MODEL = 'e2e-happy-model'
const HAPPY_SKILL_NAME = `pager-happy-${nonce}`
const HAPPY_SUPPORT_FILE = 'references/steps.md'
/** One distinct sentence per file — "each file shows ITS OWN bytes" has to
 *  be checkable against something no other file in the bundle also says. */
const HAPPY_SKILL_LINE = `Run the pager drill first (${nonce}).`
const HAPPY_SUPPORT_LINE = `Then escalate to the second responder (${nonce}).`
const HAPPY_SKILL_DOCUMENT = [
  '---',
  `name: ${HAPPY_SKILL_NAME}`,
  `description: "A second pager-drill skill, kept only for the happy path (${nonce})"`,
  '---',
  '',
  `${HAPPY_SKILL_LINE} Then read ${HAPPY_SUPPORT_FILE}.`,
  '',
].join('\n')
const HAPPY_SUPPORT_BODY = `# Steps\n\n${HAPPY_SUPPORT_LINE}\n`
const HAPPY_AUDIT_REPORT = `It runs the pager drill and then escalates to the second responder (${nonce}).`
/** What a person's editor does to the skill after it has already been
 *  checked once. */
const HAPPY_EDITED_LINE = `And then double-check the escalation path (${nonce}).`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
let skillDir = ''

test.describe.configure({ mode: 'serial' })
test.setTimeout(180_000)

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), `nocx-ojfuc-e2e-${nonce}-`))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()

  // Written AFTER the backend starts, because that is when its disposable
  // home is known, and before the page loads, because discovery is a walk per
  // call and the list is read when the Skills section opens.
  const config = documentDir(backend.isolatedHome)
  skillDir = join(config, 'installed-skills', SKILL_NAME)
  mkdirSync(join(skillDir, 'references'), { recursive: true })
  writeFileSync(join(skillDir, SKILL_FILE), SKILL_DOCUMENT)
  writeFileSync(join(skillDir, NOTES_FILE), NOTES_BODY)
  // The document the product keeps beside the roots. The digest is one this
  // test made up, which is precisely what "the bytes under this skill are not
  // the bytes recorded for it" means; the source is what the row's second
  // evidence line reads, and no other provenance carries one.
  writeFileSync(
    join(config, 'skills.json'),
    JSON.stringify(
      {
        schemaVersion: 4,
        disabled: [],
        enabled: [],
        // 64 hex characters and no prefix: internal/skill reads this map as
        // strictly as it writes it, and a digest of another shape is a
        // refusal to read the whole document rather than one bad row.
        digests: { [SKILL_NAME]: '0'.repeat(64) },
        // The whole record of what the install resolved to (nocx-ojfuc.3):
        // the address, when the bytes were taken, and what that address
        // served. The row reads the first; the card reads all three.
        sources: {
          [SKILL_NAME]: {
            url: SKILL_URL,
            installedAt: '2026-09-04T12:00:00Z',
            digest: SOURCE_DIGEST,
          },
        },
      },
      null,
      2,
    ),
  )
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

/** The model the audit spends. Equipment, not what is under test, so it is
 *  arranged through the same Settings surfaces a person uses: one endpoint
 *  and a default model, which is what an ordinary machine has. The auditing
 *  role is never assigned by name here — the default is what answers it, and
 *  the reading says which model was billed either way. */
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

/** The collection row for a skill. Addressed off `.ui-collection-row` and not
 *  off `.ui-record-row`: the record fills the collection row's info slot, and
 *  the actions and the state cell hang off the region on the other side of
 *  it, so they are siblings of the record rather than descendants. */
function rowFor(page: Page, name: string): Locator {
  return page
    .locator('.ui-collection-row')
    .filter({ has: page.locator('.ui-record-row__title', { hasText: name }) })
}

test.describe('a person manages the skills they have (nocx-ojfuc.4)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('list, read, audit, switch, re-approve and delete — and nowhere to paste', async ({
    page,
  }) => {
    await openApp(page)
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await configureAssistant(page)

    // ── SKILLS IS FOUND UNDER ASSISTANT, AND THERE IS ONE OF IT ────────────
    // The page owns the `Skills` settings section, so a regression there mints
    // a second rail row of the same name holding one switch. Counting is what
    // reports that; clicking the first match would not.
    const nav = page.locator(SETTINGS_NAV)
    await expect(nav.locator('.ui-grouped-nav__item').filter({ hasText: 'Skills' })).toHaveCount(1)
    const skillsNav = page.locator(`${ASSISTANT_GROUP} ${SETTINGS_SKILLS_NAV}`)
    await expect(skillsNav).toHaveCount(1)
    await skillsNav.click()

    // ── THE LIST ───────────────────────────────────────────────────────────
    const row = rowFor(page, SKILL_NAME)
    await expect(row).toHaveCount(1, { timeout: 15_000 })
    await expect(row.locator('.ui-badge').first()).toHaveText('installed')
    await expect(row.locator('.ui-record-row__meta-text')).toHaveText(SKILL_DESCRIPTION)
    // Both lines of the record's own evidence: the file Delete removes, and
    // where the bytes came from. Settings is the only place either can be
    // read. The second is a SENTENCE (nocx-ojfuc.3) — a bare address under a
    // bare path leaves the reader to work out what the second line is a claim
    // about — with the address verbatim inside it.
    await expect(row.locator('.ui-record-row__detail')).toContainText(SKILL_NAME)
    await expect(row.locator('.ui-record-row__detail')).toContainText(`Installed from ${SKILL_URL}`)

    // ── AND NOWHERE TO PASTE AN ADDRESS ────────────────────────────────────
    // The deletion this whole bead is, asserted as an absence over the WHOLE
    // section rather than against one id: a box re-added under another name,
    // another label or another component would fail this just as hard. The
    // enable switches are `input` elements too, so the count is over the ones
    // that take text.
    // Scoped to the SECTION the page owns, which is everything it draws —
    // its list, its dialogs and its heading slot. Not to the settings shell:
    // the rail's own search box is a text field, and it belongs to Settings
    // rather than to this page.
    const skills = page.locator('.ui-section').filter({ hasText: 'Discovered skills' }).first()
    await expect(skills).toBeVisible()
    await expect(
      skills.locator('input:not([type="checkbox"]), textarea, [contenteditable="true"]'),
    ).toHaveCount(0)
    // And no control invites one under another word.
    for (const word of ['URL', 'address', 'Install', 'Import', 'Paste']) {
      await expect(skills.getByRole('button', { name: word })).toHaveCount(0)
    }

    // ── THE CHANGED-BYTES STATUS, FROM THE STATE THE PAGE OPENS IN ─────────
    // The bytes under this skill are not the bytes recorded for it, so the
    // row says so and the assistant is not offered it whatever the switch
    // says.
    await expect(row.locator('.ui-record-row__status')).toContainText('Changed since installation')

    // ── RE-APPROVAL ENDS IT ────────────────────────────────────────────────
    await row.getByRole('button', { name: `Re-approve ${SKILL_NAME}`, exact: true }).click()
    await expect(row.locator('.ui-record-row__status')).toHaveCount(0, { timeout: 15_000 })
    // The button goes with the state: a permanent Re-approve would invite
    // re-approving a skill nobody changed.
    await expect(
      row.getByRole('button', { name: `Re-approve ${SKILL_NAME}`, exact: true }),
    ).toHaveCount(0)

    // ── THE TAB, AND THE FILE VIEWER IN IT ──────────────────────────────────
    // The modal `Dialog` this used to open is gone (nocx-54a2c): a skill now
    // opens in its own tab (openSkill, skill-view-content.tsx).
    // `.pane.active .surface-host` is that tab's root — the SAME host class
    // Settings and the API workbench render into, so it is narrowed with
    // `:has(.skill-view__header)` (skill-view-header.tsx:108's own `<h1>`,
    // unconditional the instant the tab mounts) — without it, this locator
    // resolves to Settings' own `.surface-host` on every occasion this spec
    // closes the tab and Settings becomes the active pane again (`card`
    // keeps its name across the move: everything below is a claim about the
    // same record and the same bytes, only the container changed).
    await row.getByRole('button', { name: `Open ${SKILL_NAME}`, exact: true }).click()
    const card = page.locator('.pane.active .surface-host:has(.skill-view__header)')
    await expect(card).toBeVisible({ timeout: 10_000 })
    // Which skill this is: the header's own name, not merely "a tab exists".
    await expect(card.locator('.skill-view__name')).toHaveText(SKILL_NAME)
    // Where it is, and where it came from — the two facts the tab's header
    // covers by being open over the row that carries them
    // (skill-view-header.tsx). Addressed by the list's own accessible name,
    // unchanged by the move: the header draws the same FactList the deleted
    // card drew, under the same name.
    const record = card.getByLabel('Where this skill lives')
    await expect(record).toContainText(SKILL_URL)
    // AND THE REST OF WHAT RESOLVED (nocx-ojfuc.3): when the bytes were taken
    // and what that address served. Both were recorded and readable only by
    // opening skills.json by hand until now. The date is not asserted
    // verbatim — it is drawn in the reader's own locale — but its row is,
    // because a record missing its "when" is the half that rots first.
    await expect(record).toContainText('Taken on')
    await expect(record).toContainText(SOURCE_DIGEST)
    // The digest carries its qualification on its own row: a hash of bytes a
    // stranger served is change detection, never a vouch for them.
    await expect(record).toContainText('not a verdict')
    // And nothing about HOW it was found. The search, the page the model read
    // and the links it followed are deliberately recorded nowhere, so no
    // surface can imply they were.
    await expect(record).not.toContainText('Found via')
    // Every file it carries, in the manifest's own order. Scoped to the file
    // list rather than the whole tab: the tab also carries a "Check" row
    // sharing `.ui-record-row__title` (skill-view-body.tsx), which an
    // unscoped locator would fold into this count.
    const files = card.locator('.skill-view__file-list .ui-record-row__title')
    await expect(files).toHaveText([SKILL_FILE, NOTES_FILE], { timeout: 15_000 })
    // BOTH FILES ARE MARKDOWN, so the tab renders each as a document
    // (nocx-qfdy7). Semantic elements prove the markers are rendered and
    // hard-wrapped prose remains one flowing paragraph.
    const document = card.locator(
      `.ui-document-surface[aria-label="${SKILL_FILE} of “${SKILL_NAME}”"]`,
    )
    await expect(document.locator('.ui-md-front')).toHaveCount(1)
    await expect(document.locator('.ui-md-body p')).toHaveText(SKILL_BODY)
    // Opening another file REPLACES the view, so the viewer can never show
    // one file under another's name.
    await files.filter({ hasText: NOTES_FILE }).click()
    const notes = card.locator(
      `.ui-document-surface[aria-label="${NOTES_FILE} of “${SKILL_NAME}”"]`,
    )
    await expect(notes.locator('.ui-md-body h1')).toHaveText('Notes')
    await expect(notes.locator('.ui-md-body p')).toHaveText(
      `The pager rota lives in the runbook (${nonce}).`,
    )
    await expect(document).toHaveCount(0)

    // ── THE CHECK, WHICH IS ASKED FOR AND CHANGES NOTHING ───────────────────
    // Opening the tab asked for bytes the person already owns, which costs
    // nothing; the reading is a model call and waits for the button. So the
    // count is taken HERE, with the tab open and its files read, and the one
    // request below is the button's.
    //
    // A FILE ROW NO LONGER DOUBLES AS THE READING (nocx-54a2c review): the
    // deleted card's "Audit this skill" button sat beside whichever file was
    // on screen. skill-view-body.tsx now draws the reading as a THIRD thing
    // the right pane can show, selected the same way a file is — a "Check"
    // row of its own, in the same list column, above Files
    // (skill-view-check.tsx's module comment explains why it moved out of
    // the narrow list column and into the full-width right pane). So opening
    // it is a click on that row before the button is reachable.
    await card.locator('#skill-view-check .ui-record-row__title').click()
    const requestBase = fake.requests().length
    // `skills.audit` decodes the model's reply as JSON
    // (internal/assistant/skillaudit.go's parseSkillReading — exactly
    // {"verdict": "clear"|"suspect", "report": "..."}), never as prose. The
    // wire shape didn't change under this repointing; the fixture below did,
    // to keep sending something the parser accepts.
    fake.setScript({ chunks: [JSON.stringify({ verdict: 'clear', report: AUDIT_REPORT })] })
    await card.getByRole('button', { name: 'Check this skill' }).click()
    await fake.waitForRequests(requestBase + 1)
    await expect(card).toContainText(AUDIT_REPORT, { timeout: 30_000 })
    // It is the model's conclusion and not nocx's, and it names the model
    // that was billed. It claims no safety either: the scan matched nothing
    // here, and "nothing matched" is what the tab says rather than "nothing
    // is wrong".
    await expect(card).toContainText("the model's conclusion, not nocx's")
    // WHICH MODEL READ THIS SKILL — folded into the verdict line's own text
    // now (skill-view-check.tsx's verdictLine: "Clear — <model> · <endpoint>
    // · <date>") rather than a second FactList under that name. The deleted
    // card's separate `getByLabel('Which model read this skill')` region has
    // no replacement to address by name; the fact it read is still on
    // screen, so the assertion moves to the panel rather than being dropped.
    const checkPanel = card.locator('.skill-view__check')
    await expect(checkPanel).toContainText('e2e-model')
    await expect(checkPanel).toContainText(ENDPOINT_NAME)
    await expect(card).toContainText('The static scan matched nothing in these files')
    // WHICH FILES IT WAS ABOUT DID NOT SURVIVE THE MOVE AS ITS OWN
    // ASSERTION. The deleted card named the read files in a `.ui-marker-list`
    // beside the report; skill-view-check.tsx's module comment records why
    // that list is gone — "the files the model actually read ARE the left
    // column now, so only the omissions still need saying"
    // (omissionsSentence). With nothing omitted here, there is no sentence
    // naming files at all, and no product-drawn structure left to assert
    // against without reading the fake's own scripted prose — which the
    // deleted card's own spec text warned against ("What is asserted about
    // the reading is what it OWES a person... rather than a word of the
    // report, which a fake wrote"). See the report on nocx-at05r.
    //
    // And it moved nothing: the skill is still off, because a reading is not
    // a decision.
    const cardSwitch = card.locator('[role="switch"]')
    await expect(cardSwitch).not.toBeChecked()

    // ── ENABLE, FROM THE TAB, WHERE THE EVIDENCE IS ─────────────────────────
    // "This skill is off" DID NOT SURVIVE THE MOVE either. The deleted card
    // drew a StatusCard with that title whenever `!skill.enabled`; the
    // tab's header (skill-view-header.tsx) kept only the "bytes changed"
    // StatusCard and never rebuilt the plain-off one. The fact itself is
    // still readable — the switch above is unchecked — so this line is
    // dropped rather than pointed at text the product no longer has
    // anywhere.
    await cardSwitch.click()
    await expect(cardSwitch).toBeChecked({ timeout: 15_000 })
    // No "Close" button on a tab — Meta+w is how every other spec in this
    // suite leaves one. The tab TITLE naming this skill is what closing it
    // removes — asserted at 1 first, so a future rename of the title
    // (openSkill's `defaultTitle`) cannot turn this into a filter that
    // matched nothing before OR after and silently passes either way.
    const skillTabTitle = page.locator(TITLE).filter({ hasText: SKILL_NAME })
    await expect(skillTabTitle).toHaveCount(1)
    await page.keyboard.press('Meta+w')
    await expect(skillTabTitle).toHaveCount(0)

    // One control over one fact: the row's switch is the card's switch, and
    // the list behind the card caught up with the decision taken on it.
    const rowSwitch = row.locator('.ui-record-row__state [role="switch"]')
    await expect(rowSwitch).toBeChecked({ timeout: 15_000 })

    // ── DISABLE, FROM THE ROW ──────────────────────────────────────────────
    await rowSwitch.click()
    await expect(rowSwitch).not.toBeChecked({ timeout: 15_000 })

    // ── AND A BYTE MOVING IS NOTICED ───────────────────────────────────────
    // The real thing this time, hashed by the product: an editor appends a
    // line to a skill that was approved a moment ago. The switch is what
    // makes the list ask again — the status is computed per call, so the page
    // learns on its next answer rather than by watching the file.
    writeFileSync(join(skillDir, SKILL_FILE), `${SKILL_DOCUMENT}${EDITED_LINE}\n`)
    await rowSwitch.click()
    await expect(row.locator('.ui-record-row__status')).toContainText(
      'Changed since installation',
      { timeout: 15_000 },
    )
    // The person's switch was not turned off by a byte moving: the effective
    // state is computed, never written.
    await expect(rowSwitch).toBeChecked()

    // ── AND DELETE TAKES IT AWAY ───────────────────────────────────────────
    await row.getByRole('button', { name: `Delete ${SKILL_NAME}`, exact: true }).click()
    const confirm = page.getByRole('dialog').filter({ hasText: `Delete “${SKILL_NAME}”?` })
    await expect(confirm).toBeVisible({ timeout: 10_000 })
    await confirm.getByRole('button', { name: 'Delete', exact: true }).click()
    await expect(row).toHaveCount(0, { timeout: 15_000 })
  })

  // ═══════════════════════════════════════════════════════════════════════
  // THE EPIC'S HAPPY PATH (nocx-at05r) — the reason `skills.check` and
  // content.db's own store exist at all: a check spends a model call
  // exactly once, and a person can close the tab, come back, and even edit
  // the skill on disk, without paying for a second reading they never
  // asked for.
  //
  // STEP 3'S CLAIM IS ABOUT A CALL THAT MUST NOT HAPPEN, so it is measured
  // on the fake model's own request count (`fake.requests().length`) and
  // never inferred from the screen — an empty pane and a pane that did not
  // need refilling look identical. `FakeOpenAI` already exposes `requests()`
  // for this; no second counter was added.
  // ═══════════════════════════════════════════════════════════════════════
  test('a skill is checked once, and the verdict is there when you come back', async ({ page }) => {
    // Written before the page loads, into THIS backend's disposable home —
    // discovery walks the roots per call (skill.Discover), so the fixture
    // only has to exist before the Skills section is opened.
    const config = documentDir(backend.isolatedHome)
    const happyDir = join(config, 'installed-skills', HAPPY_SKILL_NAME)
    mkdirSync(join(happyDir, 'references'), { recursive: true })
    writeFileSync(join(happyDir, SKILL_FILE), HAPPY_SKILL_DOCUMENT)
    writeFileSync(join(happyDir, HAPPY_SUPPORT_FILE), HAPPY_SUPPORT_BODY)

    await openApp(page)
    await page.keyboard.press('Meta+,')
    await settingsReady(page)

    // A SEPARATE endpoint from `configureAssistant`'s (test above): reusing
    // ENDPOINT_NAME would either collide or leave this test's pass/fail
    // riding on the previous test's assistant configuration still being
    // there. The vault itself is already unlocked backend-side by the test
    // above, on this same shared backend — `createAiEndpoint` reads that
    // from the DOM (no setup sheet appears) rather than assuming it.
    await page.locator(SETTINGS_AI_NAV).click()
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
    await createAiEndpoint(page, {
      name: HAPPY_ENDPOINT_NAME,
      baseUrl: fake.baseUrl(),
      models: [HAPPY_MODEL],
      key: `e2e-happy-key-${nonce}`,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await page.locator(SETTINGS_ROLES_NAV).click()
    await setDefaultModel(page, HAPPY_ENDPOINT_NAME, HAPPY_MODEL)
    await page.locator(`${ASSISTANT_GROUP} ${SETTINGS_SKILLS_NAV}`).click()

    // ── THE TAB, AND THE BUNDLE IN IT ───────────────────────────────────
    const row = rowFor(page, HAPPY_SKILL_NAME)
    await expect(row).toHaveCount(1, { timeout: 15_000 })
    await row.getByRole('button', { name: `Open ${HAPPY_SKILL_NAME}`, exact: true }).click()

    // `.skill-view` names nothing in the DOM — the tab is SolidPaneContent's
    // own `.surface-host`, the same host element every other tab-shaped
    // surface (Settings, the API workbench) renders into; skill-view-*.tsx
    // only names its OWN children (`.skill-view__header`,
    // `.skill-view__body`, `.skill-view__file-list`, …). Narrowed with
    // `:has(.skill-view__header)` — that header's `<h1>` is unconditional
    // the instant the tab mounts (skill-view-header.tsx:108) — because an
    // unnarrowed `.pane.active .surface-host` also matches Settings' own
    // host once this test closes the tab and Settings becomes active again.
    const tab = page.locator('.pane.active .surface-host:has(.skill-view__header)')
    await expect(tab).toBeVisible({ timeout: 15_000 })
    // Which skill this is, not merely "a tab exists".
    await expect(tab.locator('.skill-view__name')).toHaveText(HAPPY_SKILL_NAME)
    const files = tab.locator('.skill-view__file-list .ui-record-row__title')
    await expect(files).toHaveText([SKILL_FILE, HAPPY_SUPPORT_FILE], { timeout: 15_000 })

    // Each file shows ITS OWN bytes — a viewer that showed the first file
    // whatever you clicked would pass a test that only opened one.
    await files.filter({ hasText: HAPPY_SUPPORT_FILE }).click()
    await expect(tab.locator('.ui-document-surface')).toContainText(HAPPY_SUPPORT_LINE, {
      timeout: 15_000,
    })
    await files.filter({ hasText: SKILL_FILE }).click()
    await expect(tab.locator('.ui-document-surface')).toContainText(HAPPY_SKILL_LINE)

    // ── CHECKED ONCE ──────────────────────────────────────────────────────
    // The reading is a third thing the right pane can show, selected the
    // same way a file is (skill-view-body.tsx) — a "Check" row above Files,
    // never a button floating beside whichever file happens to be open.
    await tab.locator('#skill-view-check .ui-record-row__title').click()
    const before = fake.requests().length
    fake.setScript({ chunks: [JSON.stringify({ verdict: 'clear', report: HAPPY_AUDIT_REPORT })] })
    await tab.getByRole('button', { name: 'Check this skill' }).click()
    await expect(tab.locator('.skill-view__check-verdict')).toContainText(/clear|suspect/i, {
      timeout: 30_000,
    })
    expect(fake.requests().length).toBe(before + 1)
    const verdict = await tab.locator('.skill-view__check-verdict').textContent()
    // AND THE REPORT — criterion 2 is "the verdict AND the report", and a
    // regression that stored and restored the verdict while losing the
    // report body would pass every check below that names only the verdict
    // line. `.skill-view__check-report` is its own class
    // (skill-view-check.tsx:251).
    await expect(tab.locator('.skill-view__check-report')).toHaveText(HAPPY_AUDIT_REPORT)

    // ── CLOSED, REOPENED, AND NOT PAID FOR TWICE ───────────────────────────
    // The tab TITLE naming this skill is what closing it removes — asserted
    // at 1 first, so a future rename of the title (openSkill's
    // `defaultTitle`) cannot turn this into a filter that matched nothing
    // before OR after and silently passes either way. NOT `.pane.active
    // .surface-host`: closing this tab activates the Settings tab
    // underneath it, which renders into its OWN `.surface-host` — the same
    // class, a different surface.
    const happySkillTabTitle = page.locator(TITLE).filter({ hasText: HAPPY_SKILL_NAME })
    await expect(happySkillTabTitle).toHaveCount(1)
    await page.keyboard.press('Meta+w')
    await expect(happySkillTabTitle).toHaveCount(0)
    await row.getByRole('button', { name: `Open ${HAPPY_SKILL_NAME}`, exact: true }).click()
    // A stored check is the default pane on open (skill-view-body.tsx's
    // "the check selected by default when one exists"), so the verdict is
    // on screen with no click needed to reach it. Narrowed the same way as
    // `tab` above, for the same reason.
    const reopened = page.locator('.pane.active .surface-host:has(.skill-view__header)')
    await expect(reopened.locator('.skill-view__check-verdict')).toHaveText(verdict!, {
      timeout: 15_000,
    })
    await expect(reopened.locator('.skill-view__check-report')).toHaveText(HAPPY_AUDIT_REPORT)
    // THE WHOLE POINT: no second call. Asserted on the model's own counter,
    // because the screen cannot tell a remembered verdict from a re-earned
    // one — an empty pane and a pane that did not need refilling look
    // identical.
    expect(fake.requests().length).toBe(before + 1)

    // ── THE BYTES MOVE, AND THE VERDICT SAYS WHAT IT IS ABOUT ──────────────
    writeFileSync(join(happyDir, SKILL_FILE), `${HAPPY_SKILL_DOCUMENT}${HAPPY_EDITED_LINE}\n`)
    await page.keyboard.press('Meta+w')
    await row.getByRole('button', { name: `Open ${HAPPY_SKILL_NAME}`, exact: true }).click()
    const reopenedAgain = page.locator('.pane.active .surface-host:has(.skill-view__header)')
    // Still there — a stale reading is still the reading.
    await expect(reopenedAgain.locator('.skill-view__check-verdict')).toHaveText(verdict!, {
      timeout: 15_000,
    })
    await expect(reopenedAgain.locator('.skill-view__check-report')).toHaveText(HAPPY_AUDIT_REPORT)
    await expect(reopenedAgain).toContainText('earlier version', { timeout: 15_000 })
    expect(fake.requests().length).toBe(before + 1)

    // ── AND A BUILTIN IS NOT CHECKED AT ALL ────────────────────────────────
    await page.keyboard.press('Meta+w')
    const builtin = rowFor(page, 'skill-authoring')
    await builtin.getByRole('button', { name: 'Open skill-authoring', exact: true }).click()
    // Narrowed the same way as `tab` above. This is the one place an
    // unnarrowed locator would have cost the most: after Meta+w, Settings
    // is the active pane and has its own `.surface-host`, so an unnarrowed
    // `builtinTab` would resolve to Settings — `toBeVisible()` would pass
    // there, and "no Check button" would be trivially true on a page that
    // never had one to begin with. Anchoring on the header's name is what
    // proves this is the builtin's own tab before its absence means
    // anything.
    const builtinTab = page.locator('.pane.active .surface-host:has(.skill-view__header)')
    await expect(builtinTab).toBeVisible({ timeout: 15_000 })
    await expect(builtinTab.locator('.skill-view__name')).toHaveText('skill-authoring')
    await expect(builtinTab.getByRole('button', { name: 'Check this skill' })).toHaveCount(0)
  })
})
