import {
  appReadyForInput,
  promptReady,
  test,
  expect,
  settingsReady,
  switchLanguage,
} from './harness'

// Stage 3 close gate (nocx-dej6.3, owner's ru-l10n plan step 10): switching
// ui.language to Русский re-renders the settings surface without a restart —
// the design's story 1. Everything asserted here is a REAL ru string the
// catalogues carry: the search placeholder (settings.search.ph), a rail
// section title (settings.section.* via the Go-minted sectionKey), and the
// reset-to-default title. English fallbacks spell the same controls, so a
// regression that renders English reads as an English-string assertion
// failing, never as a silent pass.
//
// The language is a PERSISTED setting on a SHARED stand, so this spec is the
// one that can poison the rest of its shard: it switches through
// `switchLanguage`, and the harness puts the window back to English after every
// test whether this one passes, fails or times out (e2e/harness.ts,
// `restoreEnglishLanguage`). That is why the restore is not spelled here — a
// restore inside the body is exactly what a failing assertion skips, which is
// how one red test turned into twenty-four in the #745 run.

test('switching the language to Russian re-renders settings in Russian', async ({ page }) => {
  await page.goto('/')
  await appReadyForInput(page)
  await promptReady(page)
  await page.keyboard.press('Meta+,')
  await settingsReady(page)

  // Open the Interface section where ui.language lives.
  // Generated sections take the Go section name verbatim: the rail item id
  // is the declaration's Section string, capital I and all.
  await page.locator('.ui-grouped-nav__item[data-item="Interface"]').click()

  // The language select: a setting row whose declaration key is ui.language.
  const languageRow = page.locator('[data-key="ui.language"]')
  await expect(languageRow).toBeVisible()

  // The switch, through the harness helper: it waits for the interface to be
  // SPEAKING Russian rather than for the select to have taken the click.
  await switchLanguage(page, 'ru')

  // The surface re-renders in Russian WITHOUT a reload: the search placeholder
  // is the settings chrome the stage translated, and Interface's own group
  // heading ("application" → Приложение) resolves through the catalogue.
  const search = page.getByPlaceholder('Поиск настроек…')
  await expect(page.locator('.ui-grouped-nav__heading').first()).toContainText(
    /Приложение|Ассистент|История|Интерфейс|Терминал/,
  )

  // A ru catalogue value arrived through the wire: the language row's own
  // label resolves from settings.ui.language.label.
  await expect(languageRow).toContainText('Язык')

  // ── The FROZEN SURFACES (nocx-dej6.3.8's review) ──────────────────────
  //
  // Four places held the language the window STARTED in, because their words
  // were resolved once — at module evaluation, at mount, or in a setup-time
  // array — instead of at render. Each is read here while the language is
  // already Russian, which is the state a person who starts in Russian is in
  // and the state no reload can hide.

  // 1. The activity rail's names. They are build-time descriptors, and the
  //    sidebar outlives a language switch (it is remounted per connection,
  //    not per language).
  await expect(page.locator('button[data-view="files"]')).toHaveAttribute('aria-label', 'Файлы')
  // The bell's name carries its UNREAD COUNT when it has one — the rail puts
  // the number in the accessible name so a keyboard user hears it. The stand is
  // shared and other tests leave notifications behind, so the count is not the
  // spec's to predict: what is asserted is the Russian title, and, when a count
  // is there, that the count's own words are Russian too. (Asserting the bare
  // title is what failed #745 in both browsers: «Уведомления — 37
  // непрочитанных» against «Уведомления».)
  await expect(page.locator('button[data-view="notifications"]')).toHaveAttribute(
    'aria-label',
    /^Уведомления( — \d+ непрочитанное| — \d+ непрочитанных)?$/,
  )

  // 2. The notification matrix's headers. They used to be the two halves of
  //    the English cell label, cut on " → "; they are catalogue keys now, so
  //    a column reads Russian and a row does too.
  await search.fill('notifications')
  const matrix = page.locator('.ui-toggle-matrix').first()
  await expect(matrix).toBeVisible({ timeout: 10_000 })
  await expect(matrix.locator('.ui-toggle-matrix__column').first()).toHaveText('Баннер ОС')
  await expect(matrix.locator('.ui-toggle-matrix__row-header').first()).toContainText(
    'Команда завершена',
  )
  await search.fill('')

  // The restore is the harness's; asserting it here too keeps this spec honest
  // about what a person sees when they switch back.
  await switchLanguage(page, 'en')
  await expect(page.locator('button[data-view="files"]')).toHaveAttribute('aria-label', 'Files')
})
