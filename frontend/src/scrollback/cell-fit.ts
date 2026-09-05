// ВЛАДЕЛЕЦ ВОПРОСА «ложится ли эта ячейка на сетку» (nocx-ec18).
//
// xterm говорит, сколько колонок занимает ячейка (getWidth()); DOM кладёт
// глиф с продвижкой того шрифта, который для него выбрал браузер. Для
// одноколоночной латиницы эти числа сведены глобальной коррекцией
// --term-cell-delta, применяемой как letter-spacing ко всей строке. Для
// глифа, ушедшего в другой шрифт, единой дельты не существует: U+1F5D1
// меряется 13.572px против ячейки 8, U+27F3 и U+27F2 по 9.087, U+2B22 8.903
// (macOS, 2026-09-05). Сумма их промахов на одной строке дала 8.649px —
// ровно одну колонку, и это разъехавшийся угол рамки.
//
// Здесь решается ТОЛЬКО кого запирать в коробку. Как её нарисовать — вопрос
// стилей (.term-cell), кто её ставит — вопрос сериализатора.
//
// ПОЧЕМУ ПАКЕТОМ. Замер — это запись в DOM и чтение ректа, а чтение после
// записи форсирует раскладку. Поштучно на заморозке это N раскладок в тот
// самый момент, когда блок подменяет живую область. Пакет делает ВСЕ записи,
// потом ВСЕ чтения: одна раскладка независимо от N. Поэтому заморозка сперва
// собирает кандидатов (тем же обходом ячеек, с пустыми атрибутами), греет
// кэш, и только потом сериализует — на этом проходе boxColumns уже чистое
// чтение Map.
//
// ПОЧЕМУ ПО ЗАМЕРУ, А НЕ ПО КОДПОИНТУ. Кодпоинт не определяет ни выбранный
// face (VS15/VS16 переключают представление), ни продвижку: цепочка шрифтов
// различается по машине, обновление ОС меняет метрики.
//
// ПОЧЕМУ НЕ ЗАПЕРЕТЬ ВСЁ НЕ-ASCII БЕЗ ЗАМЕРА. Псевдографика в моноширинном
// шрифте есть и ложится точно; на строке рамки это 150 коробок без единого
// исправленного пикселя.
//
// ГДЕ МЕРЯЕТСЯ — И ЭТО НЕ МЕЛОЧЬ. font-family живёт на .cmd-output
// (style.css), а .term-line задаёт только font-size; контейнер скроллбэка
// шрифта не имеет вовсе. Зонд, повешенный на контейнер с одним классом
// .term-line, унаследовал бы UI-шрифт. Поэтому зонд — настоящий .cmd-output
// с .term-line внутри, и сигнатура кэша снимается С НЕГО, а не с контейнера:
// иначе замер делается правильным шрифтом, а ключ описывает другой.

export const FIT_EPSILON_PX = 0.05

/** Граница кэша. Ключ несёт сигнатуру, поэтому смена шрифта или метрики
 *  оставляет мёртвые записи — их вытесняет эта граница. */
export const MAX_CACHE_ENTRIES = 512

export interface FitFace {
  bold: boolean
  italic: boolean
}

export interface FitCandidate {
  chars: string
  width: number
  face: FitFace
}

export type BatchMeasure = (host: HTMLElement, candidates: readonly FitCandidate[]) => number[]

export interface CellFit {
  /** Открыть заморозку: разрешить зонд, снять ширину ячейки и сигнатуру.
   *  false — мерить негде, вся заморозка пройдёт как сегодня. */
  begin(): boolean
  /** Измерить неизвестных кандидатов одним пакетом. */
  warm(candidates: Iterable<FitCandidate>): void
  /** Чистое чтение кэша. */
  boxColumns(chars: string, width: number, face: FitFace): number | null
  /** Убрать зонд из DOM. */
  dispose(): void
  /** Размер кэша. Для тестов границы. */
  size(): number
}

const PROBE_CLASS = 'cell-fit-probe'
const SIGNATURE_CLASS = 'cell-fit-witness'

/** Одноколоночная латиница — ровно то подмножество, на котором
 *  --term-cell-delta откалиброван. Держится это только потому, что
 *  .term-line глушит лигатуры и кернинг (style.css): иначе DOM мог бы сшить
 *  `->` или `ffi` не так, как xterm рисует их по ячейкам. */
function isCalibratedAscii(chars: string, width: number): boolean {
  return width === 1 && chars.length === 1 && chars.charCodeAt(0) < 0x80
}

/** Зонд: НАСТОЯЩИЙ .cmd-output, чтобы шрифт пришёл из того же правила, что
 *  и у блока. `.cmd-output` вне `.cmd-block` не подпадает под правило
 *  переноса по настройке (:root[data-output-wrap='on'] требует предка
 *  .cmd-block), так что здесь всегда `white-space: pre` — что зонду и нужно. */
function makeProbe(container: HTMLElement): HTMLElement {
  const existing = container.querySelector<HTMLElement>(`:scope > .${PROBE_CLASS}`)
  if (existing) return existing
  const host = container.ownerDocument.createElement('div')
  host.className = `cmd-output ${PROBE_CLASS}`
  // Внутри хоста живёт постоянная `.term-line` — С НЕЁ снимается сигнатура.
  // letter-spacing, font-variant-ligatures и font-kerning объявлены именно
  // на .term-line, а не на .cmd-output: снимая их с хоста, сигнатура
  // описывала бы не тот shaping, которым сделан замер. Это та же ошибка
  // «мерим одним, ключуем другим», что была в ревизиях 1 и 2, только на
  // третьем уровне вложенности.
  // `display:block` ЯВНО, инлайном. Зонд — прямой ребёнок контейнера, а
  // полноэкранный режим гасит всех прямых детей кроме живой области
  // (style.css:1018). Погашенный зонд меряется в ноль, а ноль дальше по
  // коду выглядит как «промахнулся на всю ячейку». Инлайновое объявление
  // перебивает правило из таблицы стилей.
  host.style.cssText =
    'display:block;position:absolute;left:-99999px;top:0;width:max-content;padding:0;white-space:pre;visibility:hidden;pointer-events:none'
  const witness = container.ownerDocument.createElement('span')
  witness.className = `term-line ${SIGNATURE_CLASS}`
  witness.style.display = 'inline-block'
  host.appendChild(witness)
  container.appendChild(host)
  return host
}

/** Постоянная `.term-line` внутри зонда, с которой снимается сигнатура.
 *  Отдельная от измеряемых спанов, потому что те создаются и удаляются
 *  пакетом, а сигнатура нужна до и после. */
function signatureWitness(host: HTMLElement): HTMLElement {
  const found = host.querySelector<HTMLElement>(`.${SIGNATURE_CLASS}`)
  if (found) return found
  const witness = host.ownerDocument.createElement('span')
  witness.className = `term-line ${SIGNATURE_CLASS}`
  witness.style.display = 'inline-block'
  host.appendChild(witness)
  return witness
}

/** ВСЕ ЗАПИСИ, ПОТОМ ВСЕ ЧТЕНИЯ — в этом весь смысл пакета. Меряется ОДИН
 *  кластер на кандидата, не повтор: рект инлайнового бокса это сумма
 *  продвижек и он дробный, а сравнение идёт с порогом 0.05px против промаха
 *  в 0.9px и выше — запас примерно восемнадцатикратный. Повтор дал бы лишний
 *  знак и заодно позволил бы копиям шейпиться между собой, чего в настоящей
 *  строке не происходит. */
const domBatchMeasure: BatchMeasure = (host, candidates) => {
  const doc = host.ownerDocument
  const spans = candidates.map((c) => {
    const span = doc.createElement('span')
    span.className = 'term-line'
    // inline-block, И НЕ block. `display:block` с шириной auto растягивает
    // ребёнка до ширины содержащего блока, а у хоста она `max-content`, то
    // есть ширина САМОГО ШИРОКОГО кандидата — измеритель вернул бы одно и то
    // же число для всех, и весь пакет был бы ложью. inline-block
    // сжимается по содержимому, что и есть искомая продвижка.
    span.style.display = 'inline-block'
    if (c.face.bold) span.style.fontWeight = 'bold'
    if (c.face.italic) span.style.fontStyle = 'italic'
    span.textContent = c.chars
    return span
  })
  // Добавляем и убираем ТОЛЬКО свои спаны: replaceChildren снёс бы вместе с
  // ними постоянную .term-line, с которой снимается сигнатура.
  for (const span of spans) host.appendChild(span)
  const widths = spans.map((s) => s.getBoundingClientRect().width)
  for (const span of spans) span.remove()
  return widths
}

/** Всё, что меняет ответ и не является самим кластером. В КЛЮЧЕ, а не в
 *  отдельном invalidate(): устаревшая запись тогда просто не находится, и
 *  «метрику не опубликовали, а кэш уже сбросили» не может случиться.
 *  Снимается С ЗОНДА — см. шапку. */
function signatureOf(witness: HTMLElement, cellWidth: number, fontEpoch: number): string {
  const cs = getComputedStyle(witness)
  return [
    cellWidth,
    fontEpoch,
    cs.getPropertyValue('--term-cell-delta').trim(),
    cs.fontFamily,
    cs.fontSize,
    cs.fontStretch,
    cs.letterSpacing,
    cs.fontVariantLigatures,
    cs.fontKerning,
    cs.fontFeatureSettings,
  ].join('|')
}

function cellWidthOf(container: HTMLElement): number {
  const value = Number.parseFloat(getComputedStyle(container).getPropertyValue('--term-cell-width'))
  return Number.isFinite(value) && value > 0 ? value : 0
}

export function createCellFit(
  container: () => HTMLElement | null,
  measure: BatchMeasure = domBatchMeasure,
): CellFit {
  const cache = new Map<string, number>()
  let probe: HTMLElement | null = null
  let signature = ''
  let cellWidth = 0

  // ЗАГРУЗКА ШРИФТА НЕ МЕНЯЕТ НИ ОДНОЙ СТРОКИ ВЫЧИСЛЕННОГО СТИЛЯ: то же
  // семейство, тот же размер, другой файл. xterm ждёт document.fonts.ready
  // при монтировании (renderers/xterm.ts), поэтому начальное состояние
  // сигнатуры честное; всё, что грузится позже, ловится эпохой.
  let fontEpoch = 0
  const onFontsLoaded = (): void => {
    fontEpoch += 1
  }
  const fonts = (document as Document & { fonts?: FontFaceSet }).fonts
  fonts?.addEventListener?.('loadingdone', onFontsLoaded)

  const keyOf = (chars: string, width: number, face: FitFace): string =>
    `${signature}|${face.bold ? 'b' : ''}${face.italic ? 'i' : ''}|${width}|${chars}`

  /** Настоящий LRU: Map хранит порядок ВСТАВКИ, поэтому чтение обязано
   *  переставить запись в конец, иначе только что использованная вылетит
   *  первой — это FIFO под именем LRU. */
  const touch = (key: string): number | undefined => {
    const hit = cache.get(key)
    if (hit === undefined) return undefined
    cache.delete(key)
    cache.set(key, hit)
    return hit
  }

  return {
    begin() {
      const element = container()
      if (element === null) return false
      cellWidth = cellWidthOf(element)
      if (cellWidth === 0) {
        signature = ''
        return false
      }
      probe = makeProbe(element)
      signature = signatureOf(signatureWitness(probe), cellWidth, fontEpoch)
      // ВЫТЕСНЕНИЕ ЗДЕСЬ, А НЕ В warm(). Иначе очень пёстрый блок способен
      // вытеснить то, что warm() положил для него же несколькими строками
      // выше, и коробка не появится молча. Между заморозками кэш может
      // временно превышать границу ровно на один блок — это ограничено
      // числом РАЗЛИЧНЫХ кластеров в нём и несопоставимо дешевле, чем
      // потерянный вердикт.
      while (cache.size > MAX_CACHE_ENTRIES) {
        const oldest = cache.keys().next()
        if (oldest.done) break
        cache.delete(oldest.value)
      }
      return true
    },

    warm(candidates) {
      if (probe === null || signature === '') return
      const pending: FitCandidate[] = []
      const seen = new Set<string>()
      for (const c of candidates) {
        if (isCalibratedAscii(c.chars, c.width)) continue
        const key = keyOf(c.chars, c.width, c.face)
        if (seen.has(key) || touch(key) !== undefined) continue
        seen.add(key)
        pending.push(c)
      }
      if (pending.length === 0) return
      const widths = measure(probe, pending)
      for (let i = 0; i < pending.length; i++) {
        const advance = widths[i]
        // НОЛЬ — ЭТО НЕ ЗАМЕР, А ЕГО ОТСУТСТВИЕ, и разница стоит дорого:
        // ноль отстоит от любой ячейки дальше порога, поэтому наивная
        // проверка объявила бы коробку КАЖДОМУ кластеру всякий раз, когда
        // зонд не отрисовался. Не кэшируем — следующая заморозка спросит.
        if (!(advance > 0)) continue
        const c = pending[i]
        const miss = Math.abs(advance - c.width * cellWidth)
        cache.set(keyOf(c.chars, c.width, c.face), miss >= FIT_EPSILON_PX ? c.width : 0)
      }
    },

    boxColumns(chars, width, face) {
      if (signature === '' || isCalibratedAscii(chars, width)) return null
      const verdict = touch(keyOf(chars, width, face))
      // 0 в кэше означает «измерен и ложится сам»; отсутствие означает «не
      // измерен» — оба дают null, но по разным причинам, и обе законны.
      return verdict === undefined || verdict === 0 ? null : verdict
    },

    dispose() {
      // Слушатель снимается ИМЕНОВАННЫМ, иначе при каждом пересоздании
      // контроллера на документе оседает ещё один анонимный callback.
      fonts?.removeEventListener?.('loadingdone', onFontsLoaded)
      probe?.remove()
      probe = null
      signature = ''
      cache.clear()
    },

    size() {
      return cache.size
    },
  }
}
