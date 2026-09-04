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

/** The exact characters a `<pre>` holds — `toHaveText` normalises whitespace,
 *  and what a file viewer shows is a claim about bytes. */
function exactText(pre: Locator): Promise<string> {
  return pre.evaluate((el) => el.textContent ?? '')
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
    await row.getByRole('button', { name: 'Re-approve', exact: true }).click()
    await expect(row.locator('.ui-record-row__status')).toHaveCount(0, { timeout: 15_000 })
    // The button goes with the state: a permanent Re-approve would invite
    // re-approving a skill nobody changed.
    await expect(row.getByRole('button', { name: 'Re-approve', exact: true })).toHaveCount(0)

    // ── THE CARD, AND THE FILE VIEWER IN IT ────────────────────────────────
    await row.getByRole('button', { name: 'Open', exact: true }).click()
    const card = page.getByRole('dialog', { name: SKILL_NAME })
    await expect(card).toBeVisible({ timeout: 10_000 })
    // Where it is, and where it came from — the two facts the modal covers by
    // being open over the row that carries them. Addressed by the list's own
    // accessible name: the card draws a second fact list over the file below,
    // and "the card states the source" is a claim about the first of them.
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
    // Every file it carries, in the manifest's own order.
    await expect(card.locator('.ui-record-row__title')).toHaveText([SKILL_FILE, NOTES_FILE], {
      timeout: 15_000,
    })
    const document = card.locator(
      `pre.ui-code-block[aria-label="${SKILL_FILE} of “${SKILL_NAME}”, verbatim"]`,
    )
    await expect.poll(() => exactText(document), { timeout: 15_000 }).toBe(SKILL_DOCUMENT)
    // Opening another file REPLACES the bytes, so the viewer can never show
    // one file under another's name.
    await card.locator('.ui-record-row__title', { hasText: NOTES_FILE }).click()
    const notes = card.locator(
      `pre.ui-code-block[aria-label="${NOTES_FILE} of “${SKILL_NAME}”, verbatim"]`,
    )
    await expect.poll(() => exactText(notes), { timeout: 15_000 }).toBe(NOTES_BODY)
    await expect(document).toHaveCount(0)

    // ── THE AUDIT, WHICH IS ASKED FOR AND CHANGES NOTHING ──────────────────
    // Opening the card asked for bytes the person already owns, which costs
    // nothing; the reading is a model call and waits for the button. So the
    // count is taken HERE, with the card open and its files read, and the one
    // request below is the button's.
    const requestBase = fake.requests().length
    fake.setScript({ chunks: [AUDIT_REPORT] })
    await card.getByRole('button', { name: 'Audit this skill' }).click()
    await fake.waitForRequests(requestBase + 1)
    await expect(card).toContainText(AUDIT_REPORT, { timeout: 30_000 })
    // It is a description and not a verdict, it names the model that was
    // billed, and it says which files it was about — a reading of a subset
    // that did not say so would read exactly like a reading of the whole
    // skill. It claims no safety either: the scan matched nothing here, and
    // "nothing matched" is what the card says rather than "nothing is wrong".
    await expect(card).toContainText('A description, not a verdict')
    await expect(card.getByLabel('Which model read this skill')).toContainText('e2e-model')
    await expect(card.getByLabel('Which model read this skill')).toContainText(ENDPOINT_NAME)
    await expect(card.locator('.ui-marker-list')).toContainText(SKILL_FILE)
    await expect(card.locator('.ui-marker-list')).toContainText(NOTES_FILE)
    await expect(card).toContainText('The static scan matched nothing in these files')
    // And it moved nothing: the skill is still off, because a reading is not
    // a decision.
    const cardSwitch = card.locator('[role="switch"]')
    await expect(cardSwitch).not.toBeChecked()

    // ── ENABLE, FROM THE CARD, WHERE THE EVIDENCE IS ───────────────────────
    await expect(card).toContainText('This skill is off')
    await cardSwitch.click()
    await expect(cardSwitch).toBeChecked({ timeout: 15_000 })
    await card.getByRole('button', { name: 'Close', exact: true }).click()
    await expect(card).toBeHidden({ timeout: 10_000 })

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
    await row.getByRole('button', { name: 'Delete', exact: true }).click()
    const confirm = page.getByRole('dialog').filter({ hasText: `Delete “${SKILL_NAME}”?` })
    await expect(confirm).toBeVisible({ timeout: 10_000 })
    await confirm.getByRole('button', { name: 'Delete', exact: true }).click()
    await expect(row).toHaveCount(0, { timeout: 15_000 })
  })
})
