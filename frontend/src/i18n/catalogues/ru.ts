/**
 * The Russian catalogue — deliberately PARTIAL by design.
 *
 * A key missing here is not an error: `t()` falls back to the English entry,
 * and the parity lint fixture (lint-fixtures/check-i18n-catalogues.mjs)
 * warns about the gap so it is visible in review without blocking a commit
 * that adds an untranslated key. After stage 2 (nocx-dej6.2) the only
 * deliberate gap is `i18n.sample.untranslated`, which exists to prove the
 * fallback at runtime.
 *
 * Everything committed in this repo is English — comments included — but
 * catalogue VALUES are the product's Russian, the one place the repository
 * language rule does not reach (AGENTS.md "Language": what is committed is
 * English; what the app renders is the person's language).
 */
import type { CatalogueEntry } from '../locale-model'

/**
 * Flat key → string (or plural object) map. Same key set as `en` except
 * where a translation has not landed yet.
 */
export const ru: Readonly<Record<string, CatalogueEntry>> = {
  // ── Settings → Interface → Language chrome ──────────────────────────────
  'settings.ui.language.label': 'Язык',
  'settings.ui.language.description':
    'Язык, на котором nocx говорит на своих экранах — настройки, приглашения и уведомления. Вывод терминала не переводится.',
  // Endonyms by design (Q4): each language named in itself, in both locales.
  'settings.ui.language.option.en': 'English',
  'settings.ui.language.option.ru': 'Русский',

  // ── Settings declarations (Go: internal/settings/settings.go) ───────────
  // Order follows the declaration sites in settings.go.
  'settings.history.enabled.label': 'Хранить историю команд',
  'settings.history.enabled.description':
    'Записывать команды для вызова после перезапуска. Когда выключено, новые команды не сохраняются, а панель вызова показывает только текущий сеанс; уже накопленная история остаётся до истечения её срока.',
  'settings.skills.enabled.label': 'Включить навыки',
  'settings.skills.enabled.description':
    'Разрешить ассистенту обнаруживать и использовать локальные навыки.',
  'settings.skills.idleDays.label': 'Отключать неиспользуемые навыки после',
  'settings.skills.idleDays.description':
    'Навык, которым никто не пользовался так долго, отключается, и в строке сказано, что это сделал nocx. Повторное включение снимает эту пометку. Встроенные навыки таким способом не отключаются.',
  'settings.worktrees.idleDays.label': 'Удалять неиспользуемые рабочие checkout после',
  'settings.worktrees.idleDays.description':
    'Рабочий checkout, созданный nocx и не использовавшийся так долго, удаляется ежедневной очисткой. Уходит только checkout — его ветка и все коммиты в ней остаются, — а checkout с незафиксированной работой никогда не удаляется.',
  'settings.skills.readingMinutes.label': 'Прекращать чтение навыка после',
  'settings.skills.readingMinutes.description':
    'Сколько может длиться одна проверка навыка — суммарно по всем читаемым файлам и выводу. Когда время истекает, проверка останавливается и сообщает об этом, ничего не сохраняя. Локальной модели на большом навыке может понадобиться несколько минут; размещённая обычно укладывается в одну.',
  'settings.history.retentionDays.label': 'Хранить историю',
  'settings.history.retentionDays.description':
    'Как долго хранится завершённая команда. Более старые команды удаляются из nocx — удаление не является безопасным стиранием, удалённые строки могут оставаться в свободном месте файла базы данных.',
  'settings.history.retentionMiB.label': 'Размер истории команд',
  'settings.history.retentionMiB.description':
    'Какой объём текста команд хранить. Когда он достигнут, самые старые команды удаляются из nocx (опять же, не безопасным стиранием). Занимаемое на диске место больше этого числа: по замерам ~3,2x — 256 МиБ содержимого занимают около 811 МиБ на диске — из-за поискового индекса и шифрованных страниц.',
  'settings.history.diskCeilingMiB.label': 'Лимит места на диске',
  'settings.history.diskCeilingMiB.description':
    'Физический потолок для базы данных истории и её журнала упреждающей записи. Число отдельное от размера содержимого: удаление содержимого уменьшает то, что вы храните, но не обязательно сам файл. Когда лимит достигнут, хранилище уплотняется, а не удаляет больше.',
  'settings.history.outputEnabled.label': 'Хранить вывод команд',
  'settings.history.outputEnabled.description':
    'Хранится ли текст, напечатанный командами, вместе с самими командами. Вывод больше по размеру и чувствительнее командной строки; при выключенной настройке команды хранятся без вывода.',
  'settings.history.outputCapKB.label': 'Хранить на команду, не более',
  'settings.history.outputCapKB.description':
    'Какая часть вывода одной команды хранится. Сверх этого сохраняются начало и конец, середина отбрасывается, и блок сообщает об этом.',
  'settings.history.helperBufferMB.label': 'Буфер вывода на машине shell',
  'settings.history.helperBufferMB.description':
    'Сколько вывода команд машина, где работает shell, держит для nocx, пока соединение медленное, на один сеанс. Сверх этого выполняющаяся в тот момент команда сохраняется как «Вывод неполный», и вывод снова сохраняется со следующей команды. Применяется к сеансам, открытым после изменения.',
  'settings.history.coordinatorBufferMB.label': 'Буфер вывода в nocx',
  'settings.history.coordinatorBufferMB.description':
    'Сколько вывода команд nocx держит в памяти, пока сохранение в историю идёт медленно, на один сеанс. Сверх этого выполняющаяся в тот момент команда сохраняется как «Вывод неполный», и вывод снова сохраняется со следующей команды. Применяется к сеансам, открытым после изменения.',
  'settings.clipboard.osc52Suppressed.label': 'Скрывать предупреждения буфера обмена OSC 52',
  'settings.clipboard.osc52Suppressed.description':
    'Когда включено, баннер разрешения на запись в буфер обмена через OSC 52 больше не показывается. Буфер обмена остаётся защищён — это лишь скрывает повторяющийся запрос.',
  'settings.update.channel.label': 'Канал обновлений',
  'settings.update.channel.description':
    'Стабильный проверяет подписанный манифест последнего выпуска. Ночные сборки проверяют ночной канал — сборку из main каждую ночь, когда он менялся, не проверенную никем до вас. Возврат на стабильный канал никогда не понижает версию: вы ждёте следующего стабильного выпуска или ставите его вручную.',
  'settings.update.channel.option.stable': 'Стабильный',
  'settings.update.channel.option.nightly': 'Ночные сборки',
  'settings.tab.placement.label': 'Расположение вкладок',
  'settings.tab.placement.description':
    'Горизонтально располагает вкладки полосой сверху окна, как в классическом терминале. Вертикально — списком сбоку, как в IDE.',
  'settings.tab.placement.option.horizontal': 'Горизонтально',
  'settings.tab.placement.option.vertical': 'Вертикально',
  'settings.ssh.reconnect.label': 'При потере SSH-соединения',
  'settings.ssh.reconnect.description':
    'Спросить показывает кнопку на вкладке. Автоматически открывает новую оболочку на той же конечной точке сам и возвращается к кнопке, когда попытки исчерпаны. Никогда оставляет вкладку наедине с кнопкой. В любом случае вкладка остаётся открытой со всем напечатанным, а вернувшаяся оболочка — НОВАЯ: то, что выполняла старая, может всё ещё работать на сервере.',
  'settings.ssh.reconnect.option.ask': 'Спросить',
  'settings.ssh.reconnect.option.auto': 'Автоматически',
  'settings.ssh.reconnect.option.never': 'Никогда',
  'settings.restore.onStartup.label': 'Восстанавливать вкладки и панели при запуске',
  'settings.restore.onStartup.description':
    'Заново открыть оставленные рабочие пространства, вкладки и панели — каждую с её командами. Сами оболочки новы: локальная панель запускает свежую оболочку в том же каталоге, и ничего выполнявшегося не возвращается. Выключено — окно открывается пустым: прежние вкладки закрыты, а не удалены, и выполненные в них команды остаются в истории — но они не ждут вас, если включить это обратно.',
  'settings.terminal.wrapOutput.label': 'Переносить длинные строки вывода',
  'settings.terminal.wrapOutput.description':
    'Переносить вывод команды по ширине блока вместо горизонтальной прокрутки. Любой блок можно переключить обратно в его собственном меню ⋮; это действует, пока вы этого не сделали.',
  'settings.ui.theme.label': 'Тема',
  'settings.ui.theme.description':
    'Цветовая тема интерфейса и панелей терминала. Смена темы перекрашивает все открытые терминалы на месте, не перезапуская их.',
  'settings.ui.theme.option.graphite': 'Графит',
  'settings.ui.theme.option.tokyo-night': 'Токио-Ночь',
  'settings.ui.theme.option.light': 'Светлая',
  'settings.ui.theme.option.ayu-dark': 'Ayu Dark',
  'settings.ui.theme.option.catppuccin-latte': 'Catppuccin Latte',
  'settings.ui.theme.option.catppuccin-mocha': 'Catppuccin Mocha',
  'settings.ui.theme.option.dracula': 'Dracula',
  'settings.ui.theme.option.gruvbox-dark': 'Gruvbox Dark',
  'settings.ui.theme.option.nord': 'Nord',
  'settings.ui.theme.option.one-dark': 'One Dark',
  'settings.ui.theme.option.rose-pine': 'Rosé Pine',
  'settings.ui.theme.option.solarized-dark': 'Solarized Dark',
  'settings.ui.theme.option.solarized-light': 'Solarized Light',
  'settings.assistant.personalInstructions.label': 'Ваши инструкции ассистенту',
  'settings.assistant.personalInstructions.description':
    'Постоянные инструкции, добавляемые в конец каждого вашего вопроса ассистенту: как вам отвечать, что это за машина — всё, что пришлось бы повторять. Они идут последними, поэтому при противоречии с собственными инструкциями nocx ваши побеждают. Это не разрешения: что ассистенту можно, решается на странице прав ассистента, и написанное здесь этого не меняет.',
  'settings.assistant.expandReasoning.label': 'Всегда показывать размышления модели',
  'settings.assistant.expandReasoning.description':
    'Раскрывать заметку с размышлениями у каждого ответа, а не оставлять свёрнутой. Любую отдельную заметку можно по-прежнему закрыть щелчком, а модель без размышлений всё так же ничего не показывает.',
  'settings.assistant.runWallClockMinutes.label': 'Останавливать команду ассистента после',
  'settings.assistant.runWallClockMinutes.description':
    'Сколько может длиться одна команда, запущенная ассистентом: отсчёт от её начала, ничем не сбрасываемый. Когда время истекает, команда останавливается, и ассистенту сообщается, какой лимит её завершил. Ассистент может попросить меньше на одну команду; больше этого — никогда.',
  'settings.assistant.runQuietMinutes.label': 'Спрашивать о молчащей команде после',
  'settings.assistant.runQuietMinutes.description':
    'Как долго команда ассистента может ничего не печатать, прежде чем nocx спросит ассистента, стоит ли ещё ждать. Команда не останавливается: отвечает ассистент, и прерывают именно ассистента — не вас. Таймер перезапускается на каждой напечатанной строке, так что медленная, но разговорчивая команда до него не доходит.',
  'settings.terminal.scrollbackLines.label': 'Прокручивать назад, не более',
  'settings.terminal.scrollbackLines.description':
    'Сколько строк вывода живой терминал хранит над текущим экраном. Чистка идёт целыми страницами (примерно по 400 КБ), поэтому терминал обычно хранит немного больше — на десятки строк, иногда сотню. Сильно размеченный вывод хранит меньше: раньше упирается во внутренний потолок памяти эмулятора. Уменьшение значения немедленно чистит живую прокрутку. 0 — без живой прокрутки; вывод остаётся доступным постоянному захвату.',
  'settings.terminal.fontFamily.label': 'Гарнитура шрифта',
  'settings.terminal.fontFamily.description':
    'Моноширинный шрифт для вывода терминала и редактора команд. Nordwand Mono поставляется вместе с nocx и работает без сети; «Системный» использует исходный шрифтовой стек темы.',
  'settings.terminal.fontFamily.option.system': 'Системный',
  'settings.terminal.fontFamily.option.nordwand-mono': 'Nordwand Mono',
  'settings.pets.enabled.label': 'Держать питомца',
  'settings.pets.enabled.description':
    'Маленькое животное живёт на верхних краях завершённых блоков команд и реагирует на то, как закончилась каждая команда. Его нельзя щёлкнуть, оно никогда не забирает фокус и не заслоняет ничего, с чем можно взаимодействовать.',
  'settings.pets.size.label': 'Размер питомца',
  'settings.pets.size.description':
    'Какой высоты рисуется животное. Это же решает, на какие блоки команд оно может встать: блок, над которым чистого места меньше, чем рост животного, землёй не предлагается.',
  'settings.pets.pack.label': 'Питомец',
  'settings.pets.pack.description':
    'Какое животное живёт в вашем терминале. Все ведут себя одинаково; цвет, у которого нет рисунка, просто не умеет этого одного.',
  'settings.pets.pack.option.cat-1': 'Рыжий кот',
  'settings.pets.pack.option.cat-2': 'Чёрный кот',
  'settings.pets.pack.option.cat-3': 'Коричневый кот',
  'settings.pets.pack.option.cat-4': 'Кот сфинкс',
  'settings.pets.pack.option.cat-5': 'Белый длинношёрстный кот',
  'settings.pets.pack.option.cat-6': 'Серый кот',

  // ── Settings rail groups (Go: RegisterGroup titles) ─────────────────────
  'settings.group.assistant': 'Ассистент',
  'settings.group.vault': 'Сейф',
  'settings.group.application': 'Приложение',
  'settings.group.developer': 'Разработчик',

  // ── Notification kinds (Go: internal/notify/catalogue.go) ───────────────
  'notify.kind.blockFinished.label': 'Команда завершена',
  'notify.kind.blockFinished.description':
    'Собственный журнал блоков nocx записал, что команда завершилась.',
  'notify.kind.sessionEnded.label': 'Сеанс завершён',
  'notify.kind.sessionEnded.description':
    'Собственный реестр сеансов nocx записал, что сеанс завершился.',
  'notify.kind.transferFinished.label': 'Передача файла завершена',
  'notify.kind.transferFinished.description':
    'Собственный реестр передач nocx записал, что загрузка или скачивание достигло конца.',
  'notify.kind.programNotify.label': 'Запрос уведомления от программы',
  'notify.kind.programNotify.description':
    'Программа напечатала OSC 9 или OSC 777 с таким запросом.',
  'notify.kind.bell.label': 'Звонок терминала',
  'notify.kind.bell.description': 'Программа напечатала BEL.',
  'notify.kind.coordinatorStalled.label': 'Координатор не читает своих рабочих',
  'notify.kind.coordinatorStalled.description':
    'nocx писал координатору о почте его рабочих, а координатор её не читал, либо координатор блокирован на собственном экране, пока его рабочие живы.',
  'notify.kind.paneWorkFinished.label': 'Работа, кажется, завершена',
  'notify.kind.paneWorkFinished.description':
    'nocx сделал вывод из заголовка панели, что её работа завершилась. Это вывод, поэтому он никогда не покидает эту машину.',

  // ── Mechanism samples ───────────────────────────────────────────────────
  // A plain string.
  'i18n.sample.greeting': 'С возвращением',
  // A plural object: Russian needs one/few/many (1 команда / 2 команды /
  // 5 команд), resolved with Intl.PluralRules('ru').
  'i18n.sample.commandCount': {
    one: '{count} команда',
    few: '{count} команды',
    many: '{count} команд',
    other: '{count} команды',
  },
  // 'i18n.sample.untranslated' is deliberately absent — see the header.

  // ── Kit chrome, passed as props from the surfaces (kit never imports i18n)
  'ui.collision.title': 'Файл уже существует',
  'ui.collision.overwrite': 'Перезаписать',
  'ui.collision.keepBoth': 'Оставить оба',
  'ui.collision.skip': 'Пропустить',
  'ui.collision.existsIn': 'уже существует в',
  'ui.collision.applyOne': 'Применить к 1 оставшемуся файлу',
  'ui.collision.applyMany': {
    one: 'Применить к {count} оставшемуся файлу',
    few: 'Применить к {count} оставшимся файлам',
    many: 'Применить к {count} оставшимся файлам',
    other: 'Применить к {count} оставшимся файлам',
  },
  'ui.toast.dismiss': 'Закрыть уведомление',
  'ui.connection.status': 'Состояние подключения',
  'ui.fileReadout.factsAria': 'Что это за файл',
  'ui.fileReadout.legendTitle': 'Подсвеченные ниже строки совпали со статическим сканом',
  'ui.fileReadout.legendTail':
    'Совпадение подсвечено там, где оно находится, в самих байтах — это улика, которую читают там, а не отказ, и ничего не изменилось из-за того, что шаблон совпал. Строки без совпадений — не строки, за которые кто-то поручился.',

  // ── Terminal surface (frontend/src/terminal-content.ts) ─────────────────
  'terminal.warning.connectionLost': 'Соединение потеряно',
  'terminal.warning.hostNotAnswering': 'Хост не отвечает',
  'terminal.pasteMultiline.title': 'Вставить многострочный текст?',
  'terminal.pasteMultiline.paste': 'Вставить',
  'terminal.pasteMultiline.cancel': 'Отмена',
  'terminal.inputDropped':
    'Это соединение перестало принимать ввод — нажатия клавиш отбрасываются.',
  'terminal.nativeInputActive':
    'Активен нативный ввод — включите редактор команд, чтобы спросить ассистента.',
  'terminal.noAssistant': 'В этой панели нет ассистента.',
  'terminal.assistant.closeAria': 'Закрыть ассистента и вернуться к программе',
  'terminal.assistant.closeTitle': 'Закрыть ассистента (Esc)',
  'terminal.assistant.resizeAria': 'Изменить размер ассистента поверх замороженного экрана',
  'terminal.startFailed': 'Не удалось запустить терминал:',
  'terminal.stop.armed':
    'Команда ещё не запустилась, поэтому остановка взведена: она сработает в момент запуска команды.',
  'terminal.stop.remote':
    'Эта команда выполняется на удалённом хосте, и nocx не может подать ей сигнал отсюда.',
  'terminal.stop.unreconciled':
    'Команда всё ещё числится запущенной, и nocx не смог её остановить — программа делит оболочку, из которой была запущена, так что принудительная остановка унесла бы и оболочку. Воспользуйтесь собственным способом выхода из программы.',
  'terminal.stop.nothingRunning':
    'В этой панели больше ничего не выполняется, так что останавливать было нечего.',
  'terminal.stop.unknown': 'nocx не смог определить, остановилась ли команда.',
  'terminal.stop.unreached': 'Команда недоступна, поэтому она может всё ещё выполняться.',
  'terminal.secrets.offerGone':
    'Предложение больше не действует — команда остаётся сохранённой в маскированном виде.',
  'terminal.secrets.saveFailed':
    'Не удалось сохранить секрет — команда остаётся сохранённой в маскированном виде.',
  'terminal.secrets.unlockVault': 'Разблокируйте хранилище, чтобы сохранить этот ключ.',
  'terminal.secrets.dismissFailed': 'Не удалось отклонить предложение — попробуйте ещё раз.',
  'terminal.secrets.dismissOfferSession':
    'Отклонено — этот ключ больше не будет предлагаться в этой сессии.',
  'terminal.secrets.vaultLocked':
    'Хранилище заблокировано. Разблокируйте его и выполните команду снова.',
  'terminal.secrets.resolveFailed': 'Не удалось разрешить команду. Она не была отправлена.',
  'terminal.secrets.unknown': 'Неизвестный секрет (секреты)',
  'terminal.secrets.notSent': 'Команда не была отправлена.',
  'terminal.secrets.stored': 'Сохранено:',
  'terminal.secrets.storedInVault': 'в хранилище.',
  'terminal.connection.dismissed': 'Отклонено —',
  'terminal.connection.dismissedTail': 'больше не будет предлагаться как подключение.',
  'terminal.connection.saved': 'Сохранено:',
  'terminal.connection.savedAs': 'как подключение.',
  'terminal.connection.saveFailed': 'Не удалось сохранить подключение:',

  // ── Connections surface, portion 1 (frontend/src/connections.tsx) ──────
  'connections.load.failedHead': 'Не удалось загрузить подключения:',
  'connections.load.failedTail': '. Список может быть устаревшим',
  'connections.password.bindingFailedHead':
    'Пароль был сохранён, но это подключение не обновлено для его использования:',
  'connections.password.bindingFailedTail': '. Нажмите «Сохранить подключение», чтобы повторить.',
  'connections.password.cancelled': 'Пароль не сохранён — запрос хранилища был отменён.',
  'connections.password.saveFailedHead': 'Не удалось сохранить пароль:',
  'connections.import.chooseFile': 'Выберите файл для импорта',
  'connections.import.nothingToImport': 'В ~/.ssh/config нет алиасов для импорта',
  'connections.import.importedFromSsh': 'Импортировано подключений из ~/.ssh/config:',
  'connections.import.skippedHead': 'пропущено (имя или хост уже сохранены):',
  'connections.import.failedHead': 'Импорт не удался:',
  'connections.import.importedProfilesHead': 'Импортировано подключений:',
  'connections.import.importedGroupsHead': 'групп:',
  'connections.import.tabbyFailedHead': 'Не удалось импортировать из Tabby:',
  'connections.toasts.saveKeyFailedHead': 'Не удалось сохранить ключ:',
  'connections.toasts.savedGroup': 'Группа сохранена:',
  'connections.toasts.saveGroupFailedHead': 'Не удалось сохранить группу:',
  'connections.toasts.previewDeleteFailedHead': 'Не удалось предпросмотреть удаление:',
  'connections.toasts.groupDeleted': 'Группа удалена',
  'connections.toasts.deleteGroupFailedHead': 'Не удалось удалить группу:',
  'connections.toasts.savePassphraseFailedHead': 'Не удалось сохранить парольную фразу:',
  'connections.toasts.testFailedHead': 'Проверка не удалась:',
  'connections.toasts.trustHostKeyFailedHead': 'Не удалось доверять ключу хоста:',
  'connections.toasts.enterHost': 'Введите хост, алиас или строку подключения',
  'connections.toasts.parseFailedHead': 'Не удалось разобрать',
  'connections.toasts.parseFailedTail':
    'попробуйте формат "user@host:port" или "ssh://user@host:port"',
  'connections.toasts.saved': 'Сохранено:',
  'connections.toasts.saveFailedHead': 'Не удалось сохранить подключение:',
  'connections.toasts.deleted': 'Удалено:',
  'connections.toasts.deleteFailedHead': 'Не удалось удалить',
  'connections.actions.edit': 'Изменить',
  'connections.actions.testConnectionTo': 'Проверить подключение к',
  'connections.actions.connect': 'Подключиться',
  'connections.actions.connectTo': 'Подключиться к',
  'connections.group.expand': 'Развернуть',
  'connections.group.collapse': 'Свернуть',
  'connections.group.groupWord': 'группу',
  'connections.group.edit': 'Изменить группу',
  'connections.group.delete': 'Удалить группу',
  'connections.list.emptyTitle': 'Нет подключений',
  'connections.list.emptyDescription':
    'Добавьте вручную или импортируйте из ~/.ssh/config, Tabby или экспорта.',
  'connections.list.ariaLabel': 'Список подключений',
  'connections.list.searchPlaceholder': 'Фильтр подключений',
  'connections.list.newConnection': '+ Новое подключение',
  'connections.list.newGroup': 'Новая группа',
  'connections.list.import': 'Импорт…',
  'connections.list.ungrouped': 'Без группы',
  'connections.list.connections': 'Подключения',
  'connections.filter.emptyTitle': 'Ничего не найдено по этому фильтру',
  'connections.filter.emptyHead': 'Ни имя, ни хост, ни пользователь подключения не содержат',
}
