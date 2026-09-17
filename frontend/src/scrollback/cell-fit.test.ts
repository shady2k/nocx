// @vitest-environment jsdom
// Владелец вопроса «ложится ли эта ячейка на сетку» (nocx-ec18).

import { describe, it, expect, vi } from 'vitest'
import {
  createCellFit,
  FIT_EPSILON_PX,
  MAX_CACHE_ENTRIES,
  type FitCandidate,
  type FitFace,
} from './cell-fit'

const REGULAR: FitFace = { bold: false, italic: false }
const BOLD: FitFace = { bold: true, italic: false }

/** Контейнер несёт метрику; шрифт, как в продукте, живёт НЕ на нём, а на
 *  `.cmd-output`, которым будет зонд. jsdom раскладки не считает, поэтому
 *  getComputedStyle читает инлайн. */
function containerWith(cellWidth: number): HTMLElement {
  const el = document.createElement('div')
  el.style.setProperty('--term-cell-width', `${cellWidth}px`)
  document.body.appendChild(el)
  return el
}

/** Измеритель пакета: одно число на кандидата, по правилу. */
function batch(rule: (c: FitCandidate) => number) {
  return vi.fn((_host: HTMLElement, cands: readonly FitCandidate[]) => cands.map(rule))
}

describe('createCellFit', () => {
  it('меряет весь пакет одним вызовом и только неизвестных', () => {
    // Каждый замер — это запись в DOM и чтение ректа. Пакет делает все
    // записи, потом все чтения — одна принудительная раскладка вместо N на
    // критическом пути заморозки.
    const measure = batch(() => 13.572)
    const fit = createCellFit(() => containerWith(8), measure)
    expect(fit.begin()).toBe(true)
    fit.warm([
      { chars: '⬢', width: 1, face: REGULAR },
      { chars: '⟳', width: 1, face: REGULAR },
    ])
    expect(measure).toHaveBeenCalledTimes(1)
    expect(measure.mock.calls[0][1]).toHaveLength(2)

    fit.begin()
    fit.warm([
      { chars: '⬢', width: 1, face: REGULAR },
      { chars: '🗑', width: 1, face: REGULAR },
    ])
    expect(measure).toHaveBeenCalledTimes(2)
    // ⬢ уже известен — в пакет попадает только новый.
    expect(measure.mock.calls[1][1]).toEqual([{ chars: '🗑', width: 1, face: REGULAR }])
  })

  it('запирает кластер, чей шаг не сходится с его колонкой', () => {
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    expect(fit.boxOf('🗑', 1, REGULAR)).toEqual({ cols: 1, fit: 0.5894 })
  })

  it('ужимает краску ровно во столько, во сколько глиф не влез', () => {
    // Замер владельца, macOS 2026-09-05: ячейка 8px, а 🗑 меряется в 14.
    // Коробка держит продвижку 8, глиф рисуется на 14 и вылезает вправо —
    // сосед со своим фоном закрашивает вылезшую половину. 8/14 = 0.5714:
    // корзина станет мельче, зато целой.
    const fit = createCellFit(
      () => containerWith(8),
      batch((c) => ({ '🗑': 14, '⬢': 9.34, '⟳': 9.52, '⟲': 9.52 })[c.chars] ?? 8),
    )
    fit.begin()
    const owner = ['🗑', '⬢', '⟳', '⟲'].map((chars) => ({ chars, width: 1, face: REGULAR }))
    fit.warm(owner)
    expect(fit.boxOf('🗑', 1, REGULAR)).toEqual({ cols: 1, fit: 0.5714 })
    expect(fit.boxOf('⬢', 1, REGULAR)).toEqual({ cols: 1, fit: 0.8565 })
    expect(fit.boxOf('⟳', 1, REGULAR)).toEqual({ cols: 1, fit: 0.8403 })
    expect(fit.boxOf('⟲', 1, REGULAR)).toEqual({ cols: 1, fit: 0.8403 })
  })

  it('НЕ растягивает глиф, который у́же своей ячейки', () => {
    // Продвижка 6 при ячейке 8 — промах, и коробка нужна: без неё строка
    // недосчитается двух пикселей. Но множитель остаётся единицей, а не
    // 1.33: щель справа никто не заметит, а растянутая буква видна сразу, и
    // масштаб вверх — это уже не «вписать», а перерисовать.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 6),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(fit.boxOf('⬢', 1, REGULAR)).toEqual({ cols: 1, fit: 1 })
  })

  it('оставляет в потоке кластер, который ложится сам', () => {
    // Псевдографика U+2500 в коробке не нуждается: рамка из неё выглядела
    // верно, а коробка на каждый символ границы — 150 узлов на строку без
    // единого исправленного пикселя.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 8.02),
    )
    fit.begin()
    fit.warm([{ chars: '─', width: 1, face: REGULAR }])
    expect(fit.boxOf('─', 1, REGULAR)).toBeNull()
  })

  it('судит двухколоночную ячейку по ДВУМ колонкам', () => {
    const ok = createCellFit(
      () => containerWith(8),
      batch(() => 16.01),
    )
    ok.begin()
    ok.warm([{ chars: '漢', width: 2, face: REGULAR }])
    expect(ok.boxOf('漢', 2, REGULAR)).toBeNull()

    // Судится и ужимается по ДВУМ колонкам: цель 16, продвижка 18 —
    // множитель 16/18, а не 8/18.
    const off = createCellFit(
      () => containerWith(8),
      batch(() => 18),
    )
    off.begin()
    off.warm([{ chars: '漢', width: 2, face: REGULAR }])
    expect(off.boxOf('漢', 2, REGULAR)).toEqual({ cols: 2, fit: 0.8889 })
  })

  it('судит начертания порознь', () => {
    // Один кластер может ложиться в regular и не ложиться в bold, а
    // attrsToStyle вешает font-weight на саму коробку — значит вердикт
    // обязан относиться к тому же начертанию.
    const measure = batch((c) => (c.face.bold ? 9.4 : 8.01))
    const fit = createCellFit(() => containerWith(8), measure)
    fit.begin()
    fit.warm([
      { chars: '⬢', width: 1, face: REGULAR },
      { chars: '⬢', width: 1, face: BOLD },
    ])
    expect(fit.boxOf('⬢', 1, REGULAR)).toBeNull()
    expect(fit.boxOf('⬢', 1, BOLD)).toEqual({ cols: 1, fit: 0.8511 })
  })

  it('не находит вердикт, снятый при другой ширине ячейки', () => {
    // Сигнатура в КЛЮЧЕ, а не отдельный invalidate(): устаревшая запись
    // просто не находится, и «метрику не опубликовали, а кэш уже сбросили»
    // невозможно по построению.
    const measure = batch(() => 13.572)
    const host = containerWith(8)
    const fit = createCellFit(() => host, measure)
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    host.style.setProperty('--term-cell-width', '9px')
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    expect(measure).toHaveBeenCalledTimes(2)
  })

  it('снимает сигнатуру С ЗОНДА, а не с контейнера', () => {
    // Это ошибка ревизии 2, и она была невидима: замер уже делался
    // правильным шрифтом, а ключ описывал бы UI-шрифт контейнера — значит
    // смена ТЕРМИНАЛЬНОГО шрифта не меняла бы ключ и кэш оставался бы
    // «верным по документам».
    const measure = batch(() => 13.572)
    const container = containerWith(8)
    const fit = createCellFit(() => container, measure)
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    const probe = container.querySelector<HTMLElement>('.cell-fit-probe')
    expect(probe).not.toBeNull()
    probe!.style.fontFamily = 'Monaco'
    fit.begin()
    fit.warm([{ chars: '🗑', width: 1, face: REGULAR }])
    expect(measure).toHaveBeenCalledTimes(2)
  })

  it('не считает нулевой замер промахом на всю ячейку', () => {
    // Полноэкранный режим гасит прямых детей контейнера (style.css:1018).
    // Погашенный зонд меряется в ноль, а ноль отстоит от любой ячейки
    // дальше порога — коробку получил бы КАЖДЫЙ кластер. Ответ не
    // кэшируется: следующая заморозка спросит заново.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 0),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(fit.boxOf('⬢', 1, REGULAR)).toBeNull()
    expect(fit.size()).toBe(0)
  })

  it('никогда не спрашивает про одноколоночный ASCII', () => {
    const measure = batch(() => 999)
    const fit = createCellFit(() => containerWith(8), measure)
    fit.begin()
    fit.warm([
      { chars: 'a', width: 1, face: REGULAR },
      { chars: ' ', width: 1, face: REGULAR },
    ])
    expect(measure).not.toHaveBeenCalled()
    expect(fit.boxOf('a', 1, REGULAR)).toBeNull()
  })

  it('молчит, пока мерить негде, вместо того чтобы гадать', () => {
    // Коробка по догадке хуже её отсутствия: она СОЗДАЁТ смещение там, где
    // его не было.
    expect(createCellFit(() => null).begin()).toBe(false)
    const noMetric = document.createElement('div')
    document.body.appendChild(noMetric)
    const fit = createCellFit(
      () => noMetric,
      batch(() => 13.572),
    )
    expect(fit.begin()).toBe(false)
    expect(fit.boxOf('⬢', 1, REGULAR)).toBeNull()
  })

  it('без begin() не отвечает ничего', () => {
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    expect(fit.boxOf('🗑', 1, REGULAR)).toBeNull()
  })

  it('раздаёт вердикты по кандидатам, а не одним числом на всех', () => {
    // ЭТОТ ТЕСТ НЕ ЛОВИТ ту ловушку, ради которой он выглядит написанным, и
    // это надо знать: измеритель здесь подставной, а разница между
    // `display:block` и `display:inline-block` внутри хоста с
    // `width: max-content` — вопрос РАСКЛАДКИ, которой в jsdom нет. Тут
    // проверяется только разводка вердиктов по ключам. Настоящую ловушку
    // сторожит браузерный тест «the batch measures each cluster on its own».
    const fit = createCellFit(
      () => containerWith(8),
      (_h, cands) => cands.map((c) => (c.chars === '🗑' ? 13.572 : 8.01)),
    )
    fit.begin()
    fit.warm([
      { chars: '🗑', width: 1, face: REGULAR },
      { chars: '─', width: 1, face: REGULAR },
    ])
    expect(fit.boxOf('🗑', 1, REGULAR)).toEqual({ cols: 1, fit: 0.5894 })
    expect(fit.boxOf('─', 1, REGULAR)).toBeNull()
  })

  it('не вытесняет то, что положил в этой же заморозке', () => {
    // Один блок с очень пёстрым выводом может дать кандидатов больше, чем
    // граница кэша. Вытеснение внутри warm() съело бы вердикты, снятые
    // несколькими строками выше, и первые ячейки молча остались бы без
    // коробок. Поэтому чистка живёт в begin(), а кэш вправе временно
    // превысить границу ровно на один блок.
    const many = Array.from({ length: MAX_CACHE_ENTRIES + 1 }, (_, i) => ({
      chars: String.fromCodePoint(0x5000 + i),
      width: 1,
      face: REGULAR,
    }))
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    fit.begin()
    fit.warm(many)
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES + 1)
    expect(fit.boxOf(many[0].chars, 1, REGULAR)?.cols).toBe(1)
    expect(fit.boxOf(many[many.length - 1].chars, 1, REGULAR)?.cols).toBe(1)
    // Следующая заморозка приводит кэш в границы.
    fit.begin()
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES)
  })

  it('снимает слушатель загрузки шрифтов по dispose', () => {
    // Анонимный callback на document.fonts копился бы при каждом
    // пересоздании контроллера. jsdom своего FontFaceSet не имеет, поэтому
    // подставляем свой и убираем после — иначе тест падал бы на undefined,
    // а не на отсутствии снятия.
    const added: string[] = []
    const removed: string[] = []
    const stub = {
      addEventListener: (t: string) => added.push(t),
      removeEventListener: (t: string) => removed.push(t),
    }
    const doc = document as Document & { fonts?: unknown }
    const had = Object.prototype.hasOwnProperty.call(doc, 'fonts')
    const prior = doc.fonts
    Object.defineProperty(doc, 'fonts', { value: stub, configurable: true })
    try {
      const fit = createCellFit(
        () => containerWith(8),
        batch(() => 13.572),
      )
      fit.begin()
      fit.dispose()
      expect(added).toContain('loadingdone')
      expect(removed).toContain('loadingdone')
    } finally {
      if (had) Object.defineProperty(doc, 'fonts', { value: prior, configurable: true })
      // `Document` объявляет `fonts` обязательным, поэтому пересечение с
      // `{ fonts?: unknown }` остаётся обязательным и `delete doc.fonts` не
      // проходит тайпчекер (TS2790). Каст только на месте удаления —
      // поведение то же, вердикт плана не меняется.
      else delete (doc as { fonts?: unknown }).fonts
    }
  })

  it('вытесняет по-настоящему давно не использованное', () => {
    // Map хранит порядок ВСТАВКИ, поэтому наивное «удалить первый ключ» —
    // это FIFO, а не LRU: только что прочитанная запись вылетела бы первой.
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 13.572),
    )
    fit.begin()
    const first = { chars: '　', width: 1, face: REGULAR }
    fit.warm([first])
    for (let i = 1; i < MAX_CACHE_ENTRIES; i++) {
      fit.warm([{ chars: String.fromCodePoint(0x3000 + i), width: 1, face: REGULAR }])
    }
    // Трогаем самую старую — она обязана пережить следующее вытеснение.
    expect(fit.boxOf('　', 1, REGULAR)?.cols).toBe(1)
    fit.warm([{ chars: '䀀', width: 1, face: REGULAR }])
    // warm() НЕ вытесняет: иначе он съел бы вердикты, снятые для этой же
    // заморозки. Размер выходит за границу и возвращается в неё на
    // следующем begin().
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES + 1)
    fit.begin()
    expect(fit.size()).toBe(MAX_CACHE_ENTRIES)
    expect(fit.boxOf('　', 1, REGULAR)?.cols).toBe(1)
  })

  it('убирает зонд из DOM по dispose', () => {
    // BlockManager объявляет _own() единственным входом в контейнер, а
    // clearAll() удаляет только своё. Зонд, оставленный там навсегда, —
    // посторонний прямой ребёнок среди опорных узлов.
    const container = containerWith(8)
    const fit = createCellFit(
      () => container,
      batch(() => 13.572),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(container.querySelector('.cell-fit-probe')).not.toBeNull()
    fit.dispose()
    expect(container.querySelector('.cell-fit-probe')).toBeNull()
  })

  it('порог назван и мал: коробка на сходящемся глифе безвредна, промах — нет', () => {
    const fit = createCellFit(
      () => containerWith(8),
      batch(() => 8 + FIT_EPSILON_PX * 2),
    )
    fit.begin()
    fit.warm([{ chars: '⬢', width: 1, face: REGULAR }])
    expect(fit.boxOf('⬢', 1, REGULAR)?.cols).toBe(1)
  })
})
