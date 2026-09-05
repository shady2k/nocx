// DOM scrollback serializer tests.
// Tests the 256-color palette, colorToCSS, attrsToStyle, and serializeLine.

import { describe, it, expect } from 'vitest'
import {
  paletteToRGB,
  colorToCSS,
  emptyAttrs,
  attrsEqual,
  attrsToStyle,
  serializeLine,
  serializeRange,
  serializeRangeSGR,
  serializeRangeText,
  DEFAULT_SNAPSHOT,
  fromITheme,
} from './serializer'
import { BufferLine, lineWith, XTERM_CM_P16, XTERM_CM_P256, XTERM_CM_RGB } from './test-helpers'

// ── Minimal mock of xterm's IBufferLine ────────────────────────────────────

function makeLine(s: string): BufferLine {
  return new BufferLine(s)
}

describe('DEFAULT_SNAPSHOT', () => {
  it('derives canonical values from DEFAULT_TERMINAL_THEME', async () => {
    // Cross-check: DEFAULT_SNAPSHOT must stay byte-identical to
    // fromITheme(DEFAULT_TERMINAL_THEME). Import dynamically to avoid
    // a hard dependency from theme-adapter at the module level.
    const { DEFAULT_TERMINAL_THEME } = await import('../renderers/theme-adapter')
    expect(DEFAULT_SNAPSHOT).toEqual(fromITheme(DEFAULT_TERMINAL_THEME))
  })
})

describe('paletteToRGB', () => {
  it('returns ANSI colors for indices 0-15', () => {
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 0)).toBe('#1a1b26') // Black
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 1)).toBe('#f7768e') // Red
    // White (#a9b1d6) and brightWhite (#c0caf5) are distinct keys in ITheme
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 7)).toBe('#a9b1d6') // White (theme.white)
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 15)).toBe('#c0caf5') // Bright White
  })

  it('returns 6×6×6 cube colors for indices 16-231', () => {
    // Index 16 = rgb(0,0,0) in cube = (0*40+55) for each
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 16)).toBe('rgb(0,0,0)')
    // Index 21 = 16+5 → (5,0,0) in cube coords: r=0,g=0,b=5 → 0,0,255
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 21)).toBe('rgb(0,0,255)')
    // Index 196 = red channel 5 = r=5*40+55=255,g=0,b=0
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 196)).toBe('rgb(255,0,0)')
    // Index 231 = white in cube = r=255,g=255,b=255
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 231)).toBe('rgb(255,255,255)')
  })

  it('returns grayscale ramp for indices 232-255', () => {
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 232)).toBe('rgb(8,8,8)')
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 255)).toBe('rgb(238,238,238)')
  })

  it('returns fallback for out-of-range indices', () => {
    expect(paletteToRGB(DEFAULT_SNAPSHOT, -1)).toBe('#c0caf5')
    expect(paletteToRGB(DEFAULT_SNAPSHOT, 256)).toBe('#c0caf5')
  })
})

describe('colorToCSS', () => {
  it('returns null for mode 0 (default)', () => {
    expect(colorToCSS(DEFAULT_SNAPSHOT, 7, 0)).toBeNull()
  })

  it('handles mode 1 (palette index)', () => {
    expect(colorToCSS(DEFAULT_SNAPSHOT, 1, 1)).toBe('#f7768e') // Red
    expect(colorToCSS(DEFAULT_SNAPSHOT, 7, 1)).toBe('#a9b1d6') // White
  })

  it('handles mode 2 (24-bit RGB)', () => {
    // xterm packs a truecolor cell as 0xRRGGBB — R in bits 16-23, G 8-15,
    // B 0-7 (measured from a real 5.5.0 buffer, nocx-07o7). The serializer
    // used to unpack R and B swapped; 0x0000ff00 is R/B-symmetric so it
    // passed either way, 0xff5500 is not and is the discriminator.
    expect(colorToCSS(DEFAULT_SNAPSHOT, 0x0000ff00, 2)).toBe('rgb(0,255,0)')
    expect(colorToCSS(DEFAULT_SNAPSHOT, 0xff5500, 2)).toBe('rgb(255,85,0)')
  })

  it('returns null for unknown modes', () => {
    expect(colorToCSS(DEFAULT_SNAPSHOT, 7, 3)).toBeNull()
  })
})

describe('emptyAttrs', () => {
  it('returns all-false/nulls', () => {
    const a = emptyAttrs()
    expect(a.fg).toBeNull()
    expect(a.bg).toBeNull()
    expect(a.bold).toBe(false)
    expect(a.inverse).toBe(false)
    expect(a.strikethrough).toBe(false)
  })
})

describe('attrsEqual', () => {
  it('returns true for two empty attrs', () => {
    expect(attrsEqual(emptyAttrs(), emptyAttrs())).toBe(true)
  })

  it('returns false when fg differs', () => {
    const a = emptyAttrs()
    const b = { ...emptyAttrs(), fg: '#ff0000' }
    expect(attrsEqual(a, b)).toBe(false)
  })

  it('returns false when bold differs', () => {
    const a = emptyAttrs()
    const b = { ...emptyAttrs(), bold: true }
    expect(attrsEqual(a, b)).toBe(false)
  })

  it('returns true when all fields match', () => {
    const a = {
      fg: '#fff',
      bg: '#000',
      bold: true,
      italic: false,
      dim: false,
      underline: true,
      inverse: false,
      blink: false,
      strikethrough: false,
      overline: false,
    }
    const b = { ...a }
    expect(attrsEqual(a, b)).toBe(true)
  })
})

describe('attrsToStyle', () => {
  it('writes nothing for a cell that asked for nothing', () => {
    // Reversed deliberately. This used to paint the snapshot's default fg and bg
    // onto every run, for snapshot fidelity — and the result was a finished block
    // rendered on a slab of the terminal's background sitting inside an app with
    // its own, while the live region next to it, which paints nothing, matched.
    // A block inherits `.cmd-output` and overrides only where the program spoke
    // (nocx-6w4z).
    expect(attrsToStyle(DEFAULT_SNAPSHOT, emptyAttrs())).toBe('')
  })

  it('still resolves defaults when inverse swaps them', () => {
    // The defaults are what `inverse` swaps IN, so they must still be resolved —
    // they just stop being written back out when they land where they started.
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), inverse: true })
    expect(style).toContain('color:#1a1b26')
    expect(style).toContain('background:#c0caf5')
  })

  it('includes foreground color', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), fg: '#ff0000' })
    expect(style).toContain('color:#ff0000')
    expect(style).not.toContain('background:') // default bg still applied
  })

  it('includes background color', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), bg: '#0000ff' })
    expect(style).toContain('background:#0000ff')
    expect(style).not.toContain('color:') // the default fg is no longer written
  })

  it('includes bold', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), bold: true })
    expect(style).toContain('font-weight:bold')
  })

  it('includes italic', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), italic: true })
    expect(style).toContain('font-style:italic')
  })

  it('includes underline', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), underline: true })
    expect(style).toContain('text-decoration:underline')
  })

  it('includes strikethrough', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), strikethrough: true })
    expect(style).toContain('text-decoration:line-through')
  })

  it('combines underline and strikethrough', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, {
      ...emptyAttrs(),
      underline: true,
      strikethrough: true,
    })
    expect(style).toContain('text-decoration:underline line-through')
  })

  it('swaps fg/bg on inverse', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, {
      ...emptyAttrs(),
      fg: '#ff0000',
      bg: '#0000ff',
      inverse: true,
    })
    expect(style).toContain('color:#0000ff')
    expect(style).toContain('background:#ff0000')
  })

  it('handles inverse with only fg', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), fg: '#ff0000', inverse: true })
    expect(style).toContain('color:#1a1b26') // bg becomes default bg
    expect(style).toContain('background:#ff0000')
  })

  it('handles inverse with only bg', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), bg: '#00ff00', inverse: true })
    expect(style).toContain('color:#00ff00')
    expect(style).toContain('background:#c0caf5') // fg becomes default fg
  })

  it('includes overline', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), overline: true })
    expect(style).toContain('text-decoration:overline')
  })

  it('combines underline with overline', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, {
      ...emptyAttrs(),
      underline: true,
      overline: true,
    })
    expect(style).toContain('text-decoration:underline overline')
  })
})

describe('serializeLine', () => {
  it('returns empty line for undefined', () => {
    expect(serializeLine(DEFAULT_SNAPSHOT, undefined)).toBe('<span class="term-line"></span>')
  })

  it('handles empty line', () => {
    const line = makeLine('')
    const html = serializeLine(DEFAULT_SNAPSHOT, line)
    expect(html).toBe('<span class="term-line"></span>')
  })

  it('wraps plain text with snapshot defaults', () => {
    const line = makeLine('hello')
    const html = serializeLine(DEFAULT_SNAPSHOT, line)
    // Mode-0 cells now resolve to snapshot defaults
    expect(html).toBe('<span class="term-line">hello</span>')
  })

  it('escapes HTML entities', () => {
    const line = makeLine('<script>alert("xss")</script>')
    const html = serializeLine(DEFAULT_SNAPSHOT, line)
    expect(html).toContain('&lt;script&gt;')
    expect(html).toContain('&lt;/script&gt;')
    expect(html).not.toContain('<script>')
  })

  it('escapes ampersands', () => {
    const line = makeLine('a & b')
    const html = serializeLine(DEFAULT_SNAPSHOT, line)
    expect(html).toContain('a &amp; b')
  })
})

describe('serializeRange', () => {
  it('trims trailing empty lines (no dangling empty term-line at block bottom)', () => {
    const lines = [makeLine('output'), makeLine(''), makeLine('')]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 2)
    expect(html).toBe('<span class="term-line">output</span>')
  })

  it('trims leading empty lines — the rows readline erased its own echo from', () => {
    // Measured 2026-08-02: an eight-line curl left sixteen blank rows above
    // its output and a single-line one four. They are where readline blanked
    // its rendering of the pasted command before redrawing lower, and the
    // count tracks the rows the command occupied.
    const lines = [makeLine(''), makeLine(''), makeLine('output')]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 2)
    expect(html).toBe('<span class="term-line">output</span>')
  })

  it('preserves interior blank lines', () => {
    const lines = [makeLine('a'), makeLine(''), makeLine('b'), makeLine('')]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 3)
    expect(html).toBe(
      '<span class="term-line">a</span>' +
        '<span class="term-line"></span>' +
        '<span class="term-line">b</span>',
    )
  })

  it('returns empty string when every line is empty', () => {
    const lines = [makeLine(''), makeLine('')]
    expect(serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 1)).toBe('')
  })
})

describe('serializeRange reflow (isWrapped)', () => {
  it('joins soft-wrapped physical lines into one logical line', () => {
    const lines = [
      new BufferLine('Quick safety check: is this a', false),
      new BufferLine('project you created?', true), // continuation
      new BufferLine('', false),
    ]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 2)
    expect(html).toBe(
      '<span class="term-line">Quick safety check: is this aproject you created?</span>',
    )
  })

  it('keeps hard newlines (table rows) as separate lines', () => {
    const lines = [new BufferLine('PID TTY', false), new BufferLine('123 pts/1', false)]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 1)
    expect(html).toBe(
      '<span class="term-line">PID TTY</span>' + '<span class="term-line">123 pts/1</span>',
    )
  })

  it('keeps the trailing space of a full soft-wrapped line', () => {
    const lines = [
      new BufferLine('word ', false), // full line, wraps at the space
      new BufferLine('next', true),
    ]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 1)
    expect(html).toBe('<span class="term-line">word next</span>')
  })

  it('trims trailing empty logical lines after reflow', () => {
    const lines = [new BufferLine('a', false), new BufferLine('', false), new BufferLine('', false)]
    expect(serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 2)).toBe(
      '<span class="term-line">a</span>',
    )
  })
})

// ── Theme snapshot freeze tests ─────────────────────────────────────────

describe('theme snapshot freezing', () => {
  it('before-freeze snapshot re-colours when theme changes', () => {
    const themeA = {
      foreground: '#111111',
      background: '#000000',
      black: '#000000',
      red: '#aa0000',
      green: '#00aa00',
      yellow: '#aaaa00',
      blue: '#0000aa',
      magenta: '#aa00aa',
      cyan: '#00aaaa',
      white: '#aaaaaa',
      brightBlack: '#555555',
      brightRed: '#ff5555',
      brightGreen: '#55ff55',
      brightYellow: '#ffff55',
      brightBlue: '#5555ff',
      brightMagenta: '#ff55ff',
      brightCyan: '#55ffff',
      brightWhite: '#ffffff',
      cursor: '#ffffff',
      cursorAccent: '#000000',
      selectionBackground: '#335577',
    }
    const themeB = {
      foreground: '#cccccc',
      background: '#222222',
      black: '#222222',
      red: '#cc0000',
      green: '#00cc00',
      yellow: '#cccc00',
      blue: '#0000cc',
      magenta: '#cc00cc',
      cyan: '#00cccc',
      white: '#cccccc',
      brightBlack: '#666666',
      brightRed: '#ff6666',
      brightGreen: '#66ff66',
      brightYellow: '#ffff66',
      brightBlue: '#6666ff',
      brightMagenta: '#ff66ff',
      brightCyan: '#66ffff',
      brightWhite: '#eeeeee',
      cursor: '#eeeeee',
      cursorAccent: '#222222',
      selectionBackground: '#446688',
    }

    const snapA = fromITheme(themeA)
    const snapB = fromITheme(themeB)

    // An ANSI colour, which is what the snapshot is for: index 1 means whatever
    // "red" is in the theme that was live when the block froze. The mock cell
    // models xterm 5.5's getFgColorMode(): the raw CM_P16 flag, not a small
    // integer (nocx-07o7).
    const line = new BufferLine(
      [...'coloured'].map((ch) => ({
        chars: ch,
        width: 1,
        fg: 1,
        fgMode: XTERM_CM_P16,
        bg: 0,
        bgMode: 0,
        bold: false,
        italic: false,
        dim: false,
        underline: false,
        inverse: false,
        blink: false,
        strikethrough: false,
        overline: false,
      })),
    )
    const outputA = serializeLine(snapA, line)
    const outputB = serializeLine(snapB, line)

    expect(outputA).not.toBe(outputB)

    // What is NOT frozen any more: the defaults. A cell that asked for nothing
    // writes nothing, so ordinary text in an old block follows the app's current
    // colours instead of carrying a slab of the palette it was captured under
    // (nocx-6w4z).
    const plain = makeLine('plain')
    expect(serializeLine(snapA, plain)).toBe(serializeLine(snapB, plain))
    expect(serializeLine(snapA, plain)).not.toContain('#111111')
  })

  it('after-freeze snapshot does not re-colour when theme changes (256/truecolor)', () => {
    // 256-color index and truecolor are ALGORITHMIC — they should never
    // change regardless of snapshot. Snapshot only affects ANSI 0-15
    // and default-mode fallbacks.
    const snapA = fromITheme({
      foreground: '#111111',
      background: '#000000',
      black: '#000000',
      red: '#aa0000',
      green: '#00aa00',
      yellow: '#aaaa00',
      blue: '#0000aa',
      magenta: '#aa00aa',
      cyan: '#00aaaa',
      white: '#aaaaaa',
      brightBlack: '#555555',
      brightRed: '#ff5555',
      brightGreen: '#55ff55',
      brightYellow: '#ffff55',
      brightBlue: '#5555ff',
      brightMagenta: '#ff55ff',
      brightCyan: '#55ffff',
      brightWhite: '#ffffff',
      cursor: '#ffffff',
      cursorAccent: '#000000',
      selectionBackground: '#335577',
    })
    const snapB = fromITheme({
      foreground: '#cccccc',
      background: '#222222',
      black: '#222222',
      red: '#cc0000',
      green: '#00cc00',
      yellow: '#cccc00',
      blue: '#0000cc',
      magenta: '#cc00cc',
      cyan: '#00cccc',
      white: '#cccccc',
      brightBlack: '#666666',
      brightRed: '#ff6666',
      brightGreen: '#66ff66',
      brightYellow: '#ffff66',
      brightBlue: '#6666ff',
      brightMagenta: '#ff66ff',
      brightCyan: '#66ffff',
      brightWhite: '#eeeeee',
      cursor: '#eeeeee',
      cursorAccent: '#222222',
      selectionBackground: '#446688',
    })

    // 256-color palette index (mode 1) in the algorithmic range
    expect(paletteToRGB(snapA, 16)).toBe('rgb(0,0,0)')
    expect(paletteToRGB(snapB, 16)).toBe('rgb(0,0,0)')

    // 256-color cube index
    expect(paletteToRGB(snapA, 196)).toBe('rgb(255,0,0)')
    expect(paletteToRGB(snapB, 196)).toBe('rgb(255,0,0)')

    // 256-color grayscale
    expect(paletteToRGB(snapA, 232)).toBe('rgb(8,8,8)')
    expect(paletteToRGB(snapB, 232)).toBe('rgb(8,8,8)')

    // Truecolor (mode 2). 0x00ff0000 packs R=0xff, G=0, B=0 (0xRRGGBB) —
    // the serializer used to unpack it as B=0xff and emit blue.
    expect(colorToCSS(snapA, 0x00ff0000, 2)).toBe('rgb(255,0,0)')
    expect(colorToCSS(snapB, 0x00ff0000, 2)).toBe('rgb(255,0,0)')
  })
})

describe('the colour modes a frozen block preserves (nocx-07o7)', () => {
  // While codex or omp runs, the output is coloured — the moment the block
  // freezes, the same content is monochrome. Measured from a real xterm
  // 5.5.0 buffer first: getFgColorMode()/getBgColorMode() return the RAW
  // attribute bits (CM_P16 = 0x01000000, CM_P256 = 0x02000000, CM_RGB =
  // 0x03000000), not small integers, and truecolor cells pack 0xRRGGBB.
  // The fixture cells below encode exactly what xterm hands back, one per
  // mode; each asserts the span the frozen block must emit.
  it('preserves a 16-colour palette cell (raw CM_P16)', () => {
    const line = lineWith({ chars: 'X', fg: 4, fgMode: XTERM_CM_P16 }) // palette blue
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe(
      '<span class="term-line"><span style="color:#7aa2f7">X</span></span>',
    )
  })

  it('preserves a 256-colour cell (raw CM_P256)', () => {
    const line = lineWith({ chars: 'X', fg: 196, fgMode: XTERM_CM_P256 }) // cube red
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe(
      '<span class="term-line"><span style="color:rgb(255,0,0)">X</span></span>',
    )
  })

  it('preserves a 24-bit RGB cell (raw CM_RGB, 0xRRGGBB)', () => {
    // 0xff5500 is orange in xterm's packing; an R/B swap turns it blue.
    const line = lineWith({ chars: 'X', fg: 0xff5500, fgMode: XTERM_CM_RGB })
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe(
      '<span class="term-line"><span style="color:rgb(255,85,0)">X</span></span>',
    )
  })

  it('keeps a default-mode cell plain — the paired assertion that the working mode still works', () => {
    const line = lineWith({ chars: 'X', fg: 7, fgMode: 0 })
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe('<span class="term-line">X</span>')
  })

  it('preserves a background colour through the same mode path', () => {
    const line = lineWith({ chars: 'X', fg: 0, fgMode: 0, bg: 1, bgMode: XTERM_CM_P16 }) // red bg
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe(
      '<span class="term-line"><span style="background:#f7768e">X</span></span>',
    )
  })

  it('carries the dim attribute the live terminal renders', () => {
    // xterm dims to 50% (multiplyOpacity(color, 0.5)); opacity is the
    // static-block equivalent and dims a default-colour cell too.
    const line = lineWith({ chars: 'X', fg: 7, fgMode: 0, dim: true })
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe(
      '<span class="term-line"><span style="opacity:0.5">X</span></span>',
    )
  })

  it('carries italic — the paired assertion that the working attribute still works', () => {
    const line = lineWith({ chars: 'X', fg: 7, fgMode: 0, italic: true })
    expect(serializeLine(DEFAULT_SNAPSHOT, line)).toBe(
      '<span class="term-line"><span style="font-style:italic">X</span></span>',
    )
  })
})

// ── The three emissions (nocx-2f0f) ────────────────────────────────────────
// One walk, three emitters. These tests exist to catch the drift a second
// implementation of the walk would cause: a restored block that does not
// match the block a person saw.

describe('serializeRangeSGR and serializeRangeText', () => {
  // ESC is what an SGR sequence starts with, so a rule about accidental
  // control characters does not apply to the one place deliberately about
  // them.
  // eslint-disable-next-line no-control-regex
  const SGR = /\u001b\[[0-9;]*m/g
  const stripSGR = (s: string) => s.replace(SGR, '')

  it('gives the characters with no markup and no escape sequences', () => {
    const lines = [makeLine('ok'), makeLine('done')]
    const getLine = (y: number) => lines[y]
    expect(serializeRangeText(getLine, 0, 1)).toBe('ok\ndone')
  })

  it('emits the colour the program named, and strips back to the plain text', () => {
    // bgMode 0 deliberately: lineWith defaults a cell's background to the
    // PALETTE, so without it the fixture is "red on black" and the emitter is
    // right to say 31;40. The case under test is a foreground on the
    // terminal's own background.
    const lines = [
      lineWith(
        { chars: 'r', fg: 1, fgMode: XTERM_CM_P16, bgMode: 0 },
        { chars: 'e', fg: 1, fgMode: XTERM_CM_P16, bgMode: 0 },
      ),
    ]
    const getLine = (y: number) => lines[y]
    const sgr = serializeRangeSGR(getLine, 0, 0)
    expect(sgr).toContain('\u001b[31m')
    expect(stripSGR(sgr)).toBe(serializeRangeText(getLine, 0, 0))
  })

  it('does not escape HTML entities — that transform belongs to the HTML path', () => {
    const lines = [makeLine('a<b&c')]
    const getLine = (y: number) => lines[y]
    expect(serializeRangeText(getLine, 0, 0)).toBe('a<b&c')
    expect(stripSGR(serializeRangeSGR(getLine, 0, 0))).toBe('a<b&c')
    expect(serializeRange(DEFAULT_SNAPSHOT, getLine, 0, 0)).toContain('a&lt;b&amp;c')
  })

  it('joins wrapped rows and trims the same way in all three modes', () => {
    const lines = [makeLine(''), makeLine('a'), new BufferLine('b', true), makeLine('')]
    const getLine = (y: number) => lines[y]
    expect(serializeRangeText(getLine, 0, 3)).toBe('ab')
    expect(stripSGR(serializeRangeSGR(getLine, 0, 3))).toBe('ab')
    const html = serializeRange(DEFAULT_SNAPSHOT, getLine, 0, 3)
    expect(html.match(/term-line/g)?.length).toBe(1)
  })

  it('closes an open attribute at the end of a row', () => {
    const lines = [lineWith({ chars: 'x', fg: 2, fgMode: XTERM_CM_P16, bgMode: 0 }), makeLine('y')]
    const getLine = (y: number) => lines[y]
    const sgr = serializeRangeSGR(getLine, 0, 1)
    expect(sgr.split('\n')[0].endsWith('\u001b[0m')).toBe(true)
  })
})

// ── Column accounting for the drift instrument (nocx-4n6sj) ────────────────
//
// The walk already steps by getWidth(); it just threw the number away. The
// drift instrument needs it, because "is this frozen line wider than its
// columns" cannot be asked of the DOM alone: serializeRange deliberately
// JOINS soft-wrapped rows, so a line legitimately wider than the block is
// indistinguishable from a mislaid one without the column count the grid
// itself used.
describe('serializeRange column accounting', () => {
  it('reports one column per single-width cell', () => {
    const lines = [makeLine('abc')]
    const cols: number[] = []
    serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0, cols)
    expect(cols).toEqual([3])
  })

  it('counts a wide cell as the two columns the grid gives it', () => {
    // xterm hands a CJK cell over as ONE cell of width 2; the spacer that
    // follows it is skipped by the walk. Two columns, one character — the
    // exact case a per-character correction cannot express.
    const lines = [lineWith({ chars: 'あ', width: 2 }, { chars: '', width: 0 }, { chars: 'x' })]
    const cols: number[] = []
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0, cols)
    // Two characters printed, three columns occupied. This is the pair a
    // single letter-spacing delta cannot reconcile, and the reason the
    // count has to come from the grid rather than from the string.
    expect(html).toContain('あx')
    expect(cols).toEqual([3])
  })

  it('drops the columns of the trailing spaces the walk trims', () => {
    // The emitted text is "ab"; a count of 5 would report drift on every
    // padded row in the buffer.
    const lines = [makeLine('ab   ')]
    const cols: number[] = []
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0, cols)
    expect(html).toBe('<span class="term-line">ab</span>')
    expect(cols).toEqual([2])
  })

  it('sums the columns of soft-wrapped rows joined into one logical line', () => {
    const lines = [makeLine('abc'), new BufferLine('de', true)]
    const cols: number[] = []
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 1, cols)
    expect(html).toBe('<span class="term-line">abcde</span>')
    expect(cols).toEqual([5])
  })

  it('stays aligned with the emitted rows when leading blanks are trimmed', () => {
    // The leading empties are readline's erased echo, dropped by the walk.
    // A cols array that still carried them would pair every line with the
    // wrong count — the failure that makes an instrument worse than none.
    const lines = [makeLine('   '), makeLine('   '), makeLine('xy')]
    const cols: number[] = []
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 2, cols)
    expect(html).toBe('<span class="term-line">xy</span>')
    expect(cols).toEqual([2])
  })

  it('is optional: the shipped call sites pass nothing and get today’s HTML', () => {
    const lines = [makeLine('abc')]
    expect(serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 0)).toBe(
      '<span class="term-line">abc</span>',
    )
  })
})
