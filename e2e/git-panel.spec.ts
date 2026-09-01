// Git panel e2e — the epic's happy path plus the in-scope actions a store
// test cannot reach (design §7, testing rules 1–3).
//
// A store test mounts a component and asserts what it renders; none of these
// tests do that. Each one drives the REAL panel over the REAL transport
// against a REAL temporary git repository (cmd/nocx-server, no wails), and
// asserts a control EXISTS, is ENABLED from the state a user starts in, and
// DOES the thing — the shape of the connection-manager defect (1041 green
// frontend tests, no way to create a group) this suite exists to catch.
//
// Repo fixtures: every spec builds its own temp repository under the
// isolated home (headless path) or the system tmp dir (wails path) and sets
// user.email/user.name itself — nothing here relies on the machine's git
// config (brief; e2e/git-fixture.ts).
//
// Timing: the panel polls every 5 s while visible, so an assertion that
// races the poll waits for the CONDITION (a row appearing, a list emptying,
// a value landing) and never for a duration. Post-mutation status arrives
// immediately (design D12), so most waits resolve fast.
import { appReadyForInput, test, expect, promptReady, resolveBackend } from './harness'
import { execFileSync, spawn } from 'node:child_process'
import { appendFileSync, chmodSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import type { ChildProcess } from 'node:child_process'
import type { Page } from './harness'
import { createRepo, createUnbornRepo, cleanupRepo, git, gitAllow } from './git-fixture'

// ── Selectors (read from frontend/src/git/git-panel.tsx — not invented) ──

const VIEW_GIT = 'button[data-view="git"]'
const PANEL = '[data-testid="git-panel"]'
const REFRESH = '[data-testid="git-refresh"]'
const BRANCH = '[data-testid="git-branch"]'
const COUNT = '[data-testid="git-changed-count"]'
const STAGED = '[data-testid="git-staged-list"]'
const UNSTAGED = '[data-testid="git-unstaged-list"]'
const CONFLICTED = '[data-testid="git-conflicted-list"]'
const STAGE_ALL = '[data-testid="git-stage-all"]'
const UNSTAGE_ALL = '[data-testid="git-unstage-all"]'
const COMMIT = '[data-testid="git-commit"]'
const SUBJECT = '#git-commit-subject'
const BODY = '#git-commit-body'
const COMMIT_OUTPUT = '[data-testid="git-commit-output"]'
const CONFLICT_REFUSAL = '[data-testid="git-conflict-refusal"]'
const CONSENT = '[data-testid="git-consent-required"]'
const ACCEPT = '[data-testid="git-consent-accept"]'
const LOG_ROW = '[data-testid="git-log-row"]'
const ROW = '.ui-collection-row'
const TAB = '.nocx-tab'
const TAB_TITLE = '.nocx-tab-title'
const COPY_BRANCH = '[data-testid="git-copy-branch"]'
const OPEN_BRANCH = '[data-testid="git-open-branch"]'
const OPEN_COMMIT = '[data-testid="git-open-commit"]'
const TOAST_MESSAGE = '.ui-toast__message'
// ── Helpers ────────────────────────────────────────────────────────────────

/** Bring the app up, park the shell in `root` (OSC 7 makes the cwd verified
 *  — the tab title only updates once the frontend processed it), and open
 *  the Git view. Waits for the repository to be READY: the branch badge is
 *  the store's own word, not a guess at how long `git rev-parse` takes. */
async function openGitPanelAt(page: Page, root: string, basename: string): Promise<void> {
  await page.goto('/')
  await promptReady(page)
  await page.keyboard.type(`cd ${root}`)
  await page.keyboard.press('Enter')
  await expect(page.locator(TAB_TITLE).first()).toContainText(basename, { timeout: 20_000 })
  await page.locator(VIEW_GIT).click()
  await expect(page.locator(BRANCH)).toBeVisible({ timeout: 20_000 })
}

// ── The happy path — the epic's DONE WHEN (design §7) ─────────────────────

test('happy path: edit → unstaged row → diff tab → stage → commit empties both lists', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    await openGitPanelAt(page, repo.root, repo.basename)

    // Edit a tracked file from outside nocx. The panel polls; the row must
    // APPEAR — never wait for a duration.
    appendFileSync(path.join(repo.root, repo.file), 'second line\n')
    const unstagedRow = page.locator(UNSTAGED).locator(ROW, { hasText: repo.file })
    await expect(unstagedRow).toBeVisible({ timeout: 20_000 })
    await expect(page.locator(COUNT)).toHaveText('1 changed')

    // Click the row → a diff tab opens showing the change.
    const tabsBefore = await page.locator(TAB).count()
    await unstagedRow.click()
    await expect(page.locator(TAB)).toHaveCount(tabsBefore + 1)
    await expect(page.locator(TAB_TITLE).last()).toHaveText(`${repo.file} (unstaged)`)
    await expect(page.locator('.pane.active .git-diff')).toContainText('+second line')

    // Stage it from the row → it moves to Staged.
    await unstagedRow.getByTestId('git-row-stage').click()
    await expect(page.locator(STAGED).locator(ROW, { hasText: repo.file })).toBeVisible({
      timeout: 20_000,
    })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)

    // Type a subject and commit.
    await page.locator(SUBJECT).fill('add second line')
    await page.locator(COMMIT).click()

    // Both lists empty; the header reflects the new head (0 changed on the
    // same branch — and the object database agrees).
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(0, { timeout: 20_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)
    await expect(page.locator(COUNT)).toHaveText('0 changed')
    await expect(page.locator(BRANCH)).toHaveText('main')
    expect(git(repo.root, 'log', '-1', '--format=%s').trim()).toBe('add second line')
  } finally {
    cleanupRepo(repo)
  }
})

// ── Every changed row says how much it changed (brief nocx-i4ki) ──────────

test('a changed row reads +3 −1, and an untracked row reads nothing', async ({ page }) => {
  const repo = createRepo({
    file: 'notes.md',
    initialContent: 'a\nb\nc\nd\n',
  })
  try {
    await openGitPanelAt(page, repo.root, repo.basename)

    // Edit the tracked file from outside nocx: gained three lines, lost
    // one. The counts ride the polled status — the row must read +3 −1,
    // never a bare M (U+2212 MINUS SIGN, the glyph the brief's acceptance
    // reads).
    writeFileSync(path.join(repo.root, repo.file), 'a\nx\nb\nc\ny\nz\n')
    const unstagedRow = page.locator(UNSTAGED).locator(ROW, { hasText: repo.file })
    await expect(unstagedRow).toBeVisible({ timeout: 20_000 })
    await expect(unstagedRow).toContainText('+3 −1')

    // Stage it → the staged row carries the same counts: the post-mutation
    // status (D12) is the same answer the panel already scopes by epoch,
    // and the counts were never fetched separately.
    await unstagedRow.getByTestId('git-row-stage').click()
    const stagedRow = page.locator(STAGED).locator(ROW, { hasText: repo.file })
    await expect(stagedRow).toBeVisible({ timeout: 20_000 })
    await expect(stagedRow).toContainText('+3 −1')

    // An untracked file has no counts by design — a numstat per untracked
    // file is one git process per file, which is exactly what the work
    // ceiling exists for — so the row renders the status letter and
    // nothing after it.
    writeFileSync(path.join(repo.root, 'fresh.txt'), 'fresh\n')
    const untrackedRow = page.locator(UNSTAGED).locator(ROW, { hasText: 'fresh.txt' })
    await expect(untrackedRow).toBeVisible({ timeout: 20_000 })
    await expect(untrackedRow).not.toContainText('+')
  } finally {
    cleanupRepo(repo)
  }
})

// ── Commits (brief, git.log) — the DONE WHEN ──────────────────────────────

test('a commit made from the panel appears at the top of the Commits list', async ({ page }) => {
  const repo = createRepo()
  try {
    await openGitPanelAt(page, repo.root, repo.basename)

    // The Commits section already lists the fixture's initial commit.
    await expect(page.locator(LOG_ROW).first()).toContainText('initial', { timeout: 20_000 })

    // Edit, stage and commit — the mutation lane's post-commit log read.
    appendFileSync(path.join(repo.root, repo.file), 'second line\n')
    const unstagedRow = page.locator(UNSTAGED).locator(ROW, { hasText: repo.file })
    await expect(unstagedRow).toBeVisible({ timeout: 20_000 })
    await unstagedRow.getByTestId('git-row-stage').click()
    await expect(page.locator(STAGED).locator(ROW, { hasText: repo.file })).toBeVisible({
      timeout: 20_000,
    })
    await page.locator(SUBJECT).fill('add second line')
    await page.locator(COMMIT).click()

    // The fresh subject sits at the TOP of the list, above the initial one.
    await expect(page.locator(LOG_ROW).first()).toContainText('add second line', {
      timeout: 20_000,
    })
    await expect(page.locator(LOG_ROW).nth(1)).toContainText('initial')
    // The backend agrees: the same log, off the real socket.
    expect(git(repo.root, 'log', '-1', '--format=%s').trim()).toBe('add second line')
  } finally {
    cleanupRepo(repo)
  }
})

// ── Layout: the one property no other gate can see ────────────────────────

// A row's geometry is invisible to every check we have. jsdom computes no
// layout, so a component test asserting classes, roles and text passes on a
// row whose parts have wrapped onto three lines; the specs above assert rows
// are visible and clickable, and a wrapped row is both. This is the assertion
// that fails on the broken row (nocx-uf0p) — the parts carried flex-item
// declarations with no flex parent, so they laid out as inline content.
test('a row is one line — letter, glyph, path — and a long path clips instead of overflowing', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    // A short name under a directory far too deep for the sidebar: the name
    // must survive whole and the directory must be what gives way.
    const dir = 'graphify-out/cache/ast/v0.9.3'
    mkdirSync(path.join(repo.root, dir), { recursive: true })
    writeFileSync(path.join(repo.root, `${dir}/chunk.json`), '{}\n')

    await openGitPanelAt(page, repo.root, repo.basename)

    const row = page.locator(UNSTAGED).locator(ROW, { hasText: 'chunk.json' })
    await expect(row).toBeVisible({ timeout: 20_000 })

    const nameEl = row.locator('.ui-file-status-row__name')
    const letterBox = await row.locator('.ui-file-status-row__status').boundingBox()
    const pathBox = await row.locator('.ui-file-status-row__path').boundingBox()
    const rowBox = await row.boundingBox()
    expect(letterBox).not.toBeNull()
    expect(pathBox).not.toBeNull()
    expect(rowBox).not.toBeNull()

    // The file name is rendered IN FULL — the directory is what gets spent.
    // This is the property the panel exists for: twelve files under one deep
    // directory must be twelve distinguishable rows.
    const clipped = await nameEl.evaluate((el) => el.scrollWidth > el.clientWidth + 1)
    expect(clipped).toBe(false)
    await expect(nameEl).toHaveText('chunk.json')

    // Same line: the two centres agree within half a letter's height. A
    // wrapped path sits a full line below and fails this by construction.
    const letterMid = letterBox!.y + letterBox!.height / 2
    const pathMid = pathBox!.y + pathBox!.height / 2
    expect(Math.abs(letterMid - pathMid)).toBeLessThan(letterBox!.height / 2)

    // And beside it, not under it.
    expect(pathBox!.x).toBeGreaterThanOrEqual(letterBox!.x + letterBox!.width)

    // The path is bounded by the row: it clips (and ellipsises) rather than
    // running past the panel into the terminal, which is what the screenshot
    // that opened this bug showed.
    expect(pathBox!.x + pathBox!.width).toBeLessThanOrEqual(rowBox!.x + rowBox!.width + 1)
  } finally {
    cleanupRepo(repo)
  }
})

// ── Amend (design §7) ──────────────────────────────────────────────────────

test('amend: ticked with a commit on HEAD it prefills the form and commits once, not twice', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    git(repo.root, 'commit', '--allow-empty', '-m', 'second', '-m', 'body of second')
    // The panel's Commit gate requires a staged change (design §5.4: the
    // button is enabled from "staged changes exist + subject typed"), so
    // the user flow is edit → stage → amend, not amend-on-a-clean-tree.
    appendFileSync(path.join(repo.root, repo.file), 'amend me\n')
    git(repo.root, 'add', '.')
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(STAGED).locator(ROW, { hasText: repo.file })).toBeVisible({
      timeout: 20_000,
    })

    // Present and enabled with a commit on HEAD.
    const amendBox = page.getByLabel('Amend last commit')
    await expect(amendBox).toBeVisible()
    await expect(amendBox).toBeEnabled()

    // Ticking fills the form from HEAD.
    await amendBox.check()
    await expect(page.locator(SUBJECT)).toHaveValue('second', { timeout: 20_000 })
    await expect(page.locator(BODY)).toHaveValue('body of second')

    // The user edits the message — amend is a rewrite, not a re-commit.
    await page.locator(SUBJECT).fill('second (amended)')
    await page.locator(COMMIT).click()

    // The form cleared = the mutation landed.
    await expect(page.locator(SUBJECT)).toHaveValue('', { timeout: 20_000 })

    // ONE commit: total stays 2 (initial + amended second) — an amend that
    // silently became a second commit would make it 3.
    expect(git(repo.root, 'rev-list', '--count', 'HEAD').trim()).toBe('2')
    const subjects = git(repo.root, 'log', '-2', '--format=%s').trim().split('\n')
    expect(subjects[0]).toBe('second (amended)')
    expect(subjects[1]).toBe('initial')
  } finally {
    cleanupRepo(repo)
  }
})

// ── Stage-all / unstage-all (design D19, §7) ───────────────────────────────

test('stage-all and unstage-all are operable from the panel', async ({ page }) => {
  const repo = createRepo()
  try {
    // Two changes: one modified tracked file, one untracked file.
    appendFileSync(path.join(repo.root, repo.file), 'edited\n')
    writeFileSync(path.join(repo.root, 'new-file.txt'), 'new\n')
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(COUNT)).toHaveText('2 changed', { timeout: 20_000 })

    // Stage-all moves both rows into Staged.
    await page.locator(STAGE_ALL).click()
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(2, { timeout: 20_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)

    // Unstage-all moves them back to Unstaged.
    await page.locator(UNSTAGE_ALL).click()
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(2, { timeout: 20_000 })
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(0)
  } finally {
    cleanupRepo(repo)
  }
})

test('stage-all and unstage-all are refused, visibly, while a conflict is unresolved (D19)', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(COUNT)).toHaveText('0 changed', { timeout: 20_000 })

    // The user runs a merge in the terminal next to the open panel. The
    // measured hazards (D19): `git add -A` marks a conflict resolved using
    // a worktree that still holds its markers, and bare `git reset` deletes
    // MERGE_HEAD, aborting the merge — so the panel must refuse to touch
    // the index as soon as it SEES the conflict, not only when one was
    // present at open.
    git(repo.root, 'checkout', '-b', 'feature')
    writeFileSync(path.join(repo.root, 'conflict.txt'), 'feature version\n')
    git(repo.root, 'add', '.')
    git(repo.root, 'commit', '-m', 'feature change')
    git(repo.root, 'checkout', 'main')
    writeFileSync(path.join(repo.root, 'conflict.txt'), 'main version\n')
    // conflict.txt is untracked on main (it was born on feature), so the
    // main-side commit must add it — `-am` would stage nothing and fail.
    git(repo.root, 'add', 'conflict.txt')
    git(repo.root, 'commit', '-m', 'main change')
    gitAllow(repo.root, 'merge', 'feature')

    // The merge happened on disk; the panel must see it — refresh, then
    // assert the refusal: the conflicted row AND, with it, a visible reason
    // and the whole-index controls disabled (D19's "refused, visibly").
    await page.locator(REFRESH).click()
    await expect(page.locator(CONFLICTED).locator(ROW, { hasText: 'conflict.txt' })).toBeVisible({
      timeout: 20_000,
    })
    await expect(page.locator(CONFLICT_REFUSAL)).toBeVisible()
    await expect(page.locator(STAGE_ALL)).toBeDisabled()
    await expect(page.locator(UNSTAGE_ALL)).toBeDisabled()

    // The refusal held: the merge is still in progress (MERGE_HEAD exists)
    // and the record is still unmerged (`git ls-files -u` non-empty) —
    // nothing resolved the conflict or aborted the merge.
    expect(existsSync(path.join(repo.root, '.git', 'MERGE_HEAD'))).toBe(true)
    expect(git(repo.root, 'ls-files', '-u').trim()).not.toBe('')
  } finally {
    cleanupRepo(repo)
  }
})

// ── Unstage-all on an unborn branch (design D19) ───────────────────────────

test('unstage-all succeeds on an unborn branch — the case that dictated bare `git reset`', async ({
  page,
}) => {
  const repo = createUnbornRepo()
  try {
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(BRANCH)).toHaveText('no commits yet', { timeout: 20_000 })
    await expect(page.locator(STAGED).locator(ROW, { hasText: repo.file })).toBeVisible()

    await page.locator(UNSTAGE_ALL).click()
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(0, { timeout: 20_000 })
    await expect(page.locator(UNSTAGED).locator(ROW, { hasText: repo.file })).toBeVisible()

    // On disk the file is untracked again — unstaged, not deleted.
    expect(git(repo.root, 'status', '--porcelain').trim()).toBe(`?? ${repo.file}`)
  } finally {
    cleanupRepo(repo)
  }
})

// ── A failing commit is visible (design D11, §7) ───────────────────────────

test("a failing commit shows git's own output and keeps the typed message", async ({ page }) => {
  const repo = createRepo()
  try {
    appendFileSync(path.join(repo.root, repo.file), 'second line\n')
    git(repo.root, 'add', '.')
    // A hook that refuses every commit. git's own output is the account the
    // panel must render (D11 — the panel does not classify why).
    const hook = path.join(repo.root, '.git', 'hooks', 'pre-commit')
    writeFileSync(hook, '#!/bin/sh\necho "blocked by the pre-commit hook"\nexit 1\n')
    chmodSync(hook, 0o755)

    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(STAGED).locator(ROW, { hasText: repo.file })).toBeVisible({
      timeout: 20_000,
    })
    await page.locator(SUBJECT).fill('should not land')
    await page.locator(COMMIT).click()

    // git's own output appears in the panel…
    const output = page.locator(COMMIT_OUTPUT)
    await expect(output).toBeVisible({ timeout: 20_000 })
    await expect(output).toContainText('blocked by the pre-commit hook')
    // …and the typed message is still in the form.
    await expect(page.locator(SUBJECT)).toHaveValue('should not land')

    // Nothing landed: the repository still has exactly the initial commit.
    expect(git(repo.root, 'rev-list', '--count', 'HEAD').trim()).toBe('1')
  } finally {
    cleanupRepo(repo)
  }
})

// ── Collapse (nocx-nak2) ──────────────────────────────────────────────────

test('a user collapses Unstaged and the commit form is reachable; the heading and count stay', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    // A tall Unstaged list pushes the commit form below the fold — the
    // situation the owner asked about (17 unstaged files). One staged
    // change keeps the commit button enabled from the start.
    for (let i = 0; i < 8; i++) writeFileSync(path.join(repo.root, `bulk-${i}.txt`), `${i}\n`)
    appendFileSync(path.join(repo.root, repo.file), 'second line\n')
    git(repo.root, 'add', repo.file)

    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(8, { timeout: 20_000 })

    const panel = page.locator(PANEL)
    const scrollBefore = await panel.evaluate((el) => el.scrollHeight)
    const unstagedSection = page.locator('.ui-section', { hasText: 'Unstaged' })
    const disclosure = unstagedSection.locator('.ui-section__disclosure')
    await expect(disclosure).toHaveAttribute('aria-expanded', 'true')

    // Collapse Unstaged from its heading's disclosure: the rows fold away,
    // the heading and its count stay.
    await disclosure.click()
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)
    await expect(unstagedSection).toContainText('Unstaged (8)')
    await expect(disclosure).toHaveAttribute('aria-expanded', 'false')

    // The fold moved up: the panel needs less scrolling, and the commit
    // form — the part the collapse exists to reach — is usable.
    const scrollAfter = await panel.evaluate((el) => el.scrollHeight)
    expect(scrollAfter).toBeLessThan(scrollBefore)
    await page.locator(SUBJECT).fill('collapse test')
    await expect(page.locator(COMMIT)).toBeEnabled()

    // Expanding restores the rows.
    await disclosure.click()
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(8)
    await expect(disclosure).toHaveAttribute('aria-expanded', 'true')
  } finally {
    cleanupRepo(repo)
  }
})

test('the disclosure is keyboard-operable — focus, Enter, Space', async ({ page }) => {
  const repo = createRepo()
  try {
    // One unstaged change so a collapse is observable.
    appendFileSync(path.join(repo.root, repo.file), 'second line\n')

    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(1, { timeout: 20_000 })
    const unstagedSection = page.locator('.ui-section', { hasText: 'Unstaged' })
    const disclosure = unstagedSection.locator('.ui-section__disclosure')

    // Enter on the focused disclosure collapses; Space expands again. The
    // heading stays in the DOM, so focus survives the fold.
    await disclosure.focus()
    await page.keyboard.press('Enter')
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)
    await expect(disclosure).toHaveAttribute('aria-expanded', 'false')

    await page.keyboard.press('Space')
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(1)
    await expect(disclosure).toHaveAttribute('aria-expanded', 'true')
  } finally {
    cleanupRepo(repo)
  }
})

// ── The filter (nocx-52by) ───────────────────────────────────────────────

test('typing part of a path leaves only the matching rows in both lists, and clearing restores them', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    // Three more files, one under a directory: several rows to filter, and
    // a directory component to match (the row renders the file NAME first
    // and its directory second — nocx-uf0p).
    mkdirSync(path.join(repo.root, 'src'), { recursive: true })
    writeFileSync(path.join(repo.root, 'alpha.txt'), 'alpha\n')
    writeFileSync(path.join(repo.root, 'src', 'beta.ts'), 'beta\n')
    writeFileSync(path.join(repo.root, 'gamma.md'), 'gamma\n')
    git(repo.root, 'add', 'alpha.txt')
    await openGitPanelAt(page, repo.root, repo.basename)

    const filter = page.getByRole('searchbox', { name: 'Filter changed files' })
    await expect(filter).toBeVisible()
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(1, { timeout: 20_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(2)

    // A directory component matches: the filter is over the whole path, so
    // "src" finds src/beta.ts while the file name is what the row leads with.
    await filter.fill('src')
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(0, { timeout: 20_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(1)
    await expect(page.locator(UNSTAGED).locator(ROW)).toContainText('beta.ts')
    await expect(page.locator(UNSTAGED).locator('.ui-file-status-row__dir')).toHaveText('src')
    // The heading counts what the list shows, so it never lies about the
    // list it heads; the header keeps the repository's total.
    await expect(page.getByText('Unstaged (1)')).toBeVisible()
    await expect(page.locator(COUNT)).toHaveText('3 changed')

    // A staged path matches too — the filter narrows BOTH lists.
    await filter.fill('alpha')
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(1)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)

    // Clearing brings everything back.
    await filter.fill('')
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(1)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(2)
  } finally {
    cleanupRepo(repo)
  }
})

test('a filter that matches nothing is a state, not a blank panel — and Clear filter recovers', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    writeFileSync(path.join(repo.root, 'alpha.txt'), 'alpha\n')
    writeFileSync(path.join(repo.root, 'beta.txt'), 'beta\n')
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(2, { timeout: 20_000 })

    const filter = page.getByRole('searchbox', { name: 'Filter changed files' })
    await filter.fill('no-such-file')
    // Each list says what happened; a panel showing nothing at all would be
    // indistinguishable from a panel that broke.
    await expect(page.getByText('No staged files match')).toBeVisible()
    await expect(page.getByText('No unstaged files match')).toBeVisible()
    await expect(page.locator(STAGED).locator(ROW)).toHaveCount(0)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(0)

    // The empty state's one recovery: drop the filter, rows return.
    await page.getByRole('button', { name: 'Clear filter' }).first().click()
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(2)
    await expect(page.getByText('No unstaged files match')).toBeHidden()
  } finally {
    cleanupRepo(repo)
  }
})

test('a filter survives a view switch — the store outlives the panel (design §5.5)', async ({
  page,
}) => {
  const repo = createRepo()
  try {
    writeFileSync(path.join(repo.root, 'alpha.txt'), 'alpha\n')
    writeFileSync(path.join(repo.root, 'beta.txt'), 'beta\n')
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(2, { timeout: 20_000 })

    const filter = page.getByRole('searchbox', { name: 'Filter changed files' })
    await filter.fill('beta')
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(1)

    // Away to the Files view, and back: the filter is still there, still
    // applied — the store outlives the panel, the way the commit form and
    // the section collapses do.
    await page.locator('button[data-view="files"]').click()
    await expect(page.locator(VIEW_GIT)).toBeVisible()
    await page.locator(VIEW_GIT).click()
    await expect(filter).toHaveValue('beta')
    await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(1, { timeout: 20_000 })
    await expect(page.locator(UNSTAGED).locator(ROW)).toContainText('beta.txt')
  } finally {
    cleanupRepo(repo)
  }
})

// ── The remote no-consent path (remote-helper design D8; §7) ──────────────

/** The disposable home the backend was launched with (headless path exports
 *  it; the wails-dev path uses the config's fixed .e2e/home) — where the
 *  backend's ssh client reads known_hosts from. */
const LOCAL_HOME = process.env.NOCX_E2E_HOME_DIR || path.resolve(__dirname, '..', '.e2e', 'home')

interface SshFixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
  _wait: Promise<void>
}

/** Build (once per run) and spawn the in-process sshd; read its handshake. */
function startSshd(): SshFixture {
  const bin = path.resolve(
    process.env.TMPDIR ?? '/tmp',
    `nocx-e2e-sshd-${process.pid}-${Date.now()}`,
  )
  if (!existsSync(bin)) {
    execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], {
      cwd: path.resolve(__dirname, '..'),
    })
  }
  const proc = spawn(bin, [], { stdio: ['ignore', 'pipe', 'inherit'] })
  const lines: string[] = []
  let addr = ''
  let userKey = ''
  let knownHosts = ''
  const deadline = Date.now() + 15_000
  const reader = new Promise<void>((resolve, reject) => {
    proc.stdout?.on('data', (chunk: Buffer) => {
      for (const line of chunk.toString().split('\n')) {
        const trimmed = line.trim()
        if (!trimmed) continue
        lines.push(trimmed)
        if (trimmed.startsWith('ADDR=')) addr = trimmed.slice(5)
        if (trimmed.startsWith('USERKEY=')) userKey = trimmed.slice(8)
        if (trimmed.startsWith('KNOWNHOSTS=')) knownHosts = trimmed.slice(11)
        if (trimmed === 'READY') resolve()
      }
      if (Date.now() > deadline)
        reject(new Error(`e2e-sshd did not print READY: ${lines.join('|')}`))
    })
    proc.on('exit', (code) =>
      reject(new Error(`e2e-sshd exited early (${code}): ${lines.join('|')}`)),
    )
  })
  return {
    proc,
    get addr() {
      return addr
    },
    get userKey() {
      return userKey
    },
    get knownHosts() {
      return knownHosts
    },
    _wait: reader,
  }
}

/** Seed the isolated home's known_hosts so the backend's ssh client accepts
 *  the fixture's host key. REPLACED, not appended: every fixture spawn mints
 *  fresh keys, and a stale line for a dead key makes the backend refuse. */
function trustHostKey(fixture: SshFixture): void {
  const sshDir = path.join(LOCAL_HOME, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
}
async function rpc<T>(
  page: Page,
  port: number,
  token: string,
  method: string,
  params: unknown,
): Promise<T> {
  return page.evaluate(
    ({ port, token, method, params }) =>
      new Promise<T>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, [`nocx.token.${token}`])
        const timer = setTimeout(() => reject(new Error(`rpc ${method} timed out`)), 10_000)
        ws.onopen = () => {
          ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }))
        }
        ws.onmessage = (ev: MessageEvent) => {
          const msg = JSON.parse(String(ev.data)) as { result?: T; error?: { message?: string } }
          clearTimeout(timer)
          ws.close()
          if (msg.error) reject(new Error(`${method}: ${msg.error.message ?? 'rpc error'}`))
          else resolve(msg.result as T)
        }
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error(`${method}: websocket error`))
        }
      }),
    { port, token, method, params },
  )
}

test('on an SSH tab with no consent the consent offer is present and the mutation controls are absent', async ({
  page,
}) => {
  test.setTimeout(120_000)
  const fixture = startSshd()
  // Captured for the teardown below: the stand is shared, so this spec must
  // remove the profile it creates.
  let createdProfileId: string | null = null
  let wsPort: number | null = null
  let wsToken: string | null = null
  try {
    await fixture._wait
    expect(fixture.addr).not.toBe('')
    trustHostKey(fixture)

    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    await appReadyForInput(page)

    // Read the backend port/token through the bindings (stubbed on the
    // headless path, real under wails dev) — the same seam shell-mode uses.
    const wsInfo = await resolveBackend(page)

    // Seed the connection the way Settings would. The name is unique per
    // run: the nocx-server store persists across runs in this home.
    const profileName = `e2e-git-remote-${Date.now()}`
    wsPort = wsInfo.port
    wsToken = wsInfo.token
    const created = await rpc<{ id?: string }>(page, wsInfo.port, wsInfo.token, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.addr.split(':')[0],
        port: Number(fixture.addr.split(':')[1]),
        user: 'e2e',
        keyPath: fixture.userKey,
        // No desiredMode option: the default (script — N3) wraps and
        // installs the launcher automatically, which is what lands the
        // OSC 7 that makes the cwd verified and lets the git store reach
        // git.open — and with it the consent ask. The shellIntegration:
        // 'ask' this spec used to pass is dead vocabulary from the
        // pre-helper model, and a conventional session would strand the
        // panel on noCwd (the same recipe git-remote.spec.ts uses).
      },
    })
    createdProfileId = created?.id ?? null

    // Open the connection through quick connect: the palette's host search
    // reaches a saved profile and Enter opens it directly. Enter on an
    // empty list dismisses the palette and opens nothing, and the search is
    // async — wait for the result row before Enter.
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    const option = page.locator('.quick-connect__item', { hasText: profileName })
    await expect(option).toBeVisible({ timeout: 10_000 })
    await page.keyboard.press('Enter')
    // The SSH tab opens and becomes active (opening a tab activates it).
    // The git panel must answer for THAT tab, not the local one — the
    // consent card only renders for an ssh origin whose cwd landed
    // verified, so asserting it is also the proof of which tab is active.
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
    await page.locator(VIEW_GIT).click()

    // A fresh fixture spawn mints fresh host keys, so this machine has no
    // consent on record: git.open answers consentRequired and the panel
    // OFFERS the flow (remote-helper design D8). The accept is the positive
    // path e2e/git-remote.spec.ts already owns, so this spec stops at the
    // offer and asserts it is really there.
    await expect(page.locator(PANEL)).toBeVisible({ timeout: 30_000 })
    await expect(page.locator(CONSENT)).toBeVisible({ timeout: 30_000 })
    await expect(page.locator(ACCEPT)).toBeVisible()

    // D14: what the panel cannot do it does not draw — with no consent the
    // mutation controls are ABSENT, not disabled. Each asserted to count
    // zero in the DOM, beside the offer that is present.

    const mutationControls = [
      STAGE_ALL,
      UNSTAGE_ALL,
      COMMIT,
      SUBJECT,
      `${UNSTAGED} [data-testid="git-row-stage"]`,
      `${STAGED} [data-testid="git-row-unstage"]`,
    ]
    for (const sel of mutationControls) {
      await expect(page.locator(sel)).toHaveCount(0)
    }
  } finally {
    // Delete the profile this spec created. The stand is SHARED by the whole
    // run (e2e/stand.ts owns one backend, one home), so a profile left behind
    // is not this spec's private mess — quick-connect.spec.ts asserts the
    // picker says "No matches" on a stand with no profiles, and a leftover
    // makes it show a row instead. It failed in CI and not here, because the
    // shard that carries both is decided by the file list, and this branch
    // changed the file list.
    //
    // Best effort inside the existing finally: a cleanup that throws would
    // replace the real failure with its own, and the test result is what
    // somebody is about to read.
    if (createdProfileId !== null && wsPort !== null && wsToken !== null) {
      await rpc(page, wsPort, wsToken, 'profiles.delete', { id: createdProfileId }).catch(
        () => undefined,
      )
    }
    fixture.proc.kill('SIGKILL')
  }
})
// ── Copy the branch name, and open the repo on its hosting (brief, nocx-hc0m; nocx-0ybp) ──

// The clipboard lives behind the one seam (AD-8): createClipboardAccess
// picks the Wails runtime when present, else navigator.clipboard. This spec
// pins the browser path — the Wails runtime is disabled via init script,
// exactly like e2e/clipboard.spec.ts — and grants the two clipboard
// permissions, which are Chromium-only in Playwright; WebKit is checked by
// hand in a packaged build.
//
// Opening a link is the same class of thing behind its own seam
// (frontend/src/open-url.ts): on web — the only environment e2e runs in,
// nocx-server has no Wails runtime — the click calls window.open
// synchronously with noopener,noreferrer; in the packaged app it goes
// through shell.openUrl. The specs below assert AT THE SEAM on purpose: a
// spec must not actually navigate away or spawn tabs it cannot clean up,
// so window.open is stubbed by an init script that records the call, and
// the record is the assertion. The native path is unit-tested
// (open-url.test.ts) — e2e never has a Wails runtime to exercise it with.
/** Stub window.open to record what the click asked for instead of opening
 *  a real tab. The record is the assertion; a real open would escape the
 *  test's control. */
async function stubWindowOpen(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const opened: Array<{ url: string; target: string; features: string }> = []
    // The record rides on the real window so the spec can read it back
    // through page.evaluate; the cast is the DOM-boundary kind — window is
    // a well-known node, and the record is this page's own property.
    const recordHost = window as unknown as { __nocxOpened: typeof opened }
    recordHost.__nocxOpened = opened
    window.open = ((url?: string | URL, target?: string, features?: string) => {
      opened.push({ url: String(url), target: target ?? '', features: features ?? '' })
      return {} as Window
    }) as typeof window.open
  })
}

/** Stub window.open to refuse — the popup blocker's answer. */
async function stubWindowOpenBlocked(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.open = () => null
  })
}
async function disableWailsRuntime(page: Page): Promise<void> {
  await page.addInitScript(() => {
    Object.defineProperty(window, 'runtime', {
      get() {
        return undefined
      },
      set(_value: unknown) {
        void _value /* swallowed */
      },
      configurable: true,
      enumerable: true,
    })
  })
}

test('copying the branch name copies it and the panel confirms', async ({ page, browserName }) => {
  test.skip(
    browserName !== 'chromium',
    'clipboard-read/write permissions are Chromium-only; WebKit by hand',
  )
  await disableWailsRuntime(page)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  const repo = createRepo()
  try {
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(BRANCH)).toHaveText('main')

    // One action: the copy affordance beside the branch. The confirmation
    // is the panel's own word — the toast — not a guess at the clipboard.
    await page.locator(COPY_BRANCH).click()
    await expect(page.locator(TOAST_MESSAGE)).toContainText('Branch name copied')

    // And the clipboard really holds the branch name, not a toast's echo.
    const copied = await page.evaluate(() => navigator.clipboard.readText())
    expect(copied).toBe('main')
  } finally {
    cleanupRepo(repo)
  }
})

test('a recognised remote draws the open links, and clicking one opens a new tab (web path)', async ({
  page,
}) => {
  const repo = createRepo()
  // A disposable repo with a tracked remote, configured directly — a fixture
  // has no origin/main ref, and %(upstream:remotename) answers from
  // branch.<name>.remote/.merge, not from the ref.
  git(repo.root, 'remote', 'add', 'origin', 'https://github.com/shady2k/nocx.git')
  git(repo.root, 'config', 'branch.main.remote', 'origin')
  git(repo.root, 'config', 'branch.main.merge', 'refs/heads/main')

  // The web path must not round-trip the backend: the click opens the tab
  // right here in the renderer, so no shell.openUrl frame may go over the
  // real socket. This is the regression guard for the platform split.
  const shellFrames: string[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (e) => {
      const p = e.payload
      if (typeof p === 'string' && p.includes('"method":"shell.openUrl"')) shellFrames.push(p)
    })
  })
  await stubWindowOpen(page)

  try {
    await openGitPanelAt(page, repo.root, repo.basename)

    // The branch link is drawn because the remote is a recognised web host
    // (D14: what the panel can do, it draws). The commit rows carry the
    // same affordance, one per row.
    await expect(page.locator(OPEN_BRANCH)).toBeVisible()
    await expect(page.locator(LOG_ROW).first()).toBeVisible()
    await expect(page.locator(OPEN_COMMIT).first()).toBeVisible()

    await page.locator(OPEN_BRANCH).click()

    // The seam: window.open was called synchronously from the click with
    // exactly the URL the panel derived — never one it invented — and the
    // noopener,noreferrer features, so a tab opened from the panel can
    // never get a handle back on the app's window. The record is the
    // assertion: the spec must not actually spawn tabs it cannot clean
    // up, so the stub records what a real browser would have opened.
    const opened = await page.evaluate(() => {
      const recordHost = window as unknown as {
        __nocxOpened?: Array<{ url: string; target: string; features: string }>
      }
      return recordHost.__nocxOpened ?? []
    })
    expect(opened).toEqual([
      {
        url: 'https://github.com/shady2k/nocx/tree/main',
        target: '_blank',
        features: 'noopener,noreferrer',
      },
    ])
    // And nothing went over the socket: the web path opens in-renderer.
    expect(shellFrames).toEqual([])
  } finally {
    cleanupRepo(repo)
  }
})

test('a popup-blocked open is told, never silent (web path failure)', async ({ page }) => {
  const repo = createRepo()
  git(repo.root, 'remote', 'add', 'origin', 'https://github.com/shady2k/nocx.git')
  git(repo.root, 'config', 'branch.main.remote', 'origin')
  git(repo.root, 'config', 'branch.main.merge', 'refs/heads/main')
  await stubWindowOpenBlocked(page)

  try {
    await openGitPanelAt(page, repo.root, repo.basename)
    await expect(page.locator(OPEN_BRANCH)).toBeVisible()

    await page.locator(OPEN_BRANCH).click()
    // The blocker refused the tab; the refusal is a toast, never a
    // silence — and on the next click, with the blocker gone, the same
    // link would open (the success path is the test above).
    await expect(page.locator(TOAST_MESSAGE)).toContainText(
      "Couldn't open the link in your browser",
    )
  } finally {
    cleanupRepo(repo)
  }
})

test('without a recognised remote the open links are absent, not disabled (D14)', async ({
  page,
}) => {
  const repo = createRepo() // no remote at all — the common case
  // framesent for the outgoing git.remote request, framereceived for the
  // backend's state:none answer — the request is never a received frame.
  const wireFrames: string[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (e) => {
      const p = e.payload
      if (typeof p === 'string') wireFrames.push(p)
    })
    ws.on('framereceived', (e) => {
      const p = e.payload
      if (typeof p === 'string') wireFrames.push(p)
    })
  })
  try {
    await openGitPanelAt(page, repo.root, repo.basename)

    // The remote read is issued with the log on open; wait for the none
    // answer ON THE WIRE — a DOM assertion alone could pass vacuously before
    // the read resolved. Then the affordances must be absent entirely: a
    // disabled control would advertise a capability the surface does not
    // have.
    await expect.poll(() => wireFrames.some((f) => f.includes('"method":"git.remote"'))).toBe(true)
    await expect.poll(() => wireFrames.some((f) => f.includes('"state":"none"'))).toBe(true)
    await expect(page.locator(OPEN_BRANCH)).toHaveCount(0)
    await expect(page.locator(OPEN_COMMIT)).toHaveCount(0)
  } finally {
    cleanupRepo(repo)
  }
})
