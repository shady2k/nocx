import { appReadyForInput, promptReady, test, expect, settingsReady } from './harness'

// Stage 3 close gate (nocx-dej6.3, owner's ru-l10n plan step 10): switching
// ui.language to Русский re-renders the settings surface without a restart —
// the design's story 1. Everything asserted here is a REAL ru string the
// catalogues carry: the search placeholder (settings.search.ph), a rail
// section title (settings.section.* via the Go-minted sectionKey), and the
// reset-to-default title. English fallbacks spell the same controls, so a
// regression that renders English reads as an English-string assertion
// failing, never as a silent pass.
//
// Reset at the end: ui.language is a persisted setting, and the suite owns a
// disposable $HOME, but the next spec in the same worker inherits the page
// state — the language is switched back, and the switch is asserted, so a
// broken revert fails here and not three specs later.

test('switching the language to Russian re-renders settings in Russian', async ({
  page,
}) => {
  await page.goto('/')
  await appReadyForInput(page)
  await promptReady(page)
  await page.keyboard.press('Meta+,')
  await settingsReady(page)

  // Open the Interface section where ui.language lives.
  await page.locator('.ui-grouped-nav__item[data-item="interface"]').click()

  // The language select: a setting row whose declaration key is ui.language.
  const languageRow = page.locator('[data-key="ui.language"]')
  await expect(languageRow).toBeVisible()

  // The select inside it — open and pick Русский.
  const select = languageRow.locator('select')
  await select.selectOption({ label: 'Русский' })

  // The surface re-renders in Russian WITHOUT a reload: the search placeholder
  // is the settings chrome the stage translated, the rail section is a
  // Go-minted sectionKey resolving through t().
  const search = page.getByPlaceholder('Поиск настроек…')
  await expect(search).toBeVisible()
  await expect(page.locator('.ui-grouped-nav__heading').first()).toContainText(/—|Раздел|История|Интерфейс|Терминал/)

  // A ru catalogue value arrived through the wire: the language row's own
  // label resolves from settings.ui.language.label.
  await expect(languageRow).toContainText('Язык')

  // Switch back and assert the revert — the next spec must not inherit ru.
  await select.selectOption({ label: 'English' })
  await expect(page.getByPlaceholder('Search settings…')).toBeVisible()
})
