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
  DEFAULT_SNAPSHOT,
  fromITheme,
} from './serializer'
import { BufferLine } from './test-helpers'

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
    // Color = 0x0000FF00 = green in RGB mode (0-7=R, 8-15=G, 16-23=B)
    expect(colorToCSS(DEFAULT_SNAPSHOT, 0x0000ff00, 2)).toBe('rgb(0,255,0)')
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
  it('resolves defaults for empty attrs (mode-0 cells get snapshot fg/bg)', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, emptyAttrs())
    expect(style).toContain('color:#c0caf5')
    expect(style).toContain('background:#1a1b26')
  })

  it('includes foreground color', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), fg: '#ff0000' })
    expect(style).toContain('color:#ff0000')
    expect(style).toContain('background:#1a1b26') // default bg still applied
  })

  it('includes background color', () => {
    const style = attrsToStyle(DEFAULT_SNAPSHOT, { ...emptyAttrs(), bg: '#0000ff' })
    expect(style).toContain('color:#c0caf5') // default fg still applied
    expect(style).toContain('background:#0000ff')
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
    expect(html).toBe(
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">hello</span></span>',
    )
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
    expect(html).toBe(
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">output</span></span>',
    )
  })

  it('preserves interior blank lines', () => {
    const lines = [makeLine('a'), makeLine(''), makeLine('b'), makeLine('')]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 3)
    expect(html).toBe(
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">a</span></span>' +
        '<span class="term-line"></span>' +
        '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">b</span></span>',
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
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">Quick safety check: is this a</span><span style="color:#c0caf5;background:#1a1b26">project you created?</span></span>',
    )
  })

  it('keeps hard newlines (table rows) as separate lines', () => {
    const lines = [new BufferLine('PID TTY', false), new BufferLine('123 pts/1', false)]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 1)
    expect(html).toBe(
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">PID TTY</span></span>' +
        '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">123 pts/1</span></span>',
    )
  })

  it('keeps the trailing space of a full soft-wrapped line', () => {
    const lines = [
      new BufferLine('word ', false), // full line, wraps at the space
      new BufferLine('next', true),
    ]
    const html = serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 1)
    expect(html).toBe(
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">word </span><span style="color:#c0caf5;background:#1a1b26">next</span></span>',
    )
  })

  it('trims trailing empty logical lines after reflow', () => {
    const lines = [new BufferLine('a', false), new BufferLine('', false), new BufferLine('', false)]
    expect(serializeRange(DEFAULT_SNAPSHOT, (y) => lines[y], 0, 2)).toBe(
      '<span class="term-line"><span style="color:#c0caf5;background:#1a1b26">a</span></span>',
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

    const line = makeLine('coloured')
    const outputA = serializeLine(snapA, line)
    const outputB = serializeLine(snapB, line)

    // Same input, different snapshot → different default fg/bg baked in
    expect(outputA).toContain('#111111')
    expect(outputB).toContain('#cccccc')
    expect(outputA).not.toBe(outputB)
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

    // Truecolor (mode 2)
    expect(colorToCSS(snapA, 0x00ff0000, 2)).toBe('rgb(0,0,255)')
    expect(colorToCSS(snapB, 0x00ff0000, 2)).toBe('rgb(0,0,255)')
  })
})
