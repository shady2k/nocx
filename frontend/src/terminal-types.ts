/** Structural terminal types shared by renderer, capture, and scrollback code.
 * They describe only the cell and theme data nocx consumes; an engine can
 * satisfy these contracts without making its package types part of the app.
 */
export interface TerminalCell {
  getChars(): string
  getWidth(): number
  getFgColor(): number
  getFgColorMode(): number
  getBgColor(): number
  getBgColorMode(): number
  isBold(): number
  isItalic(): number
  isDim(): number
  isUnderline(): number
  isBlink(): number
  isInverse(): number
  isStrikethrough(): number
  isOverline(): number
}

export interface TerminalBufferLine {
  readonly length: number
  readonly isWrapped: boolean
  getCell(index: number): TerminalCell | undefined
  translateToString(trimRight?: boolean, startCol?: number, endCol?: number): string
}

/** Optional colour fields match the theme values consumed by nocx. */
export interface TerminalTheme {
  background?: string
  foreground?: string
  cursor?: string
  cursorAccent?: string
  selectionBackground?: string
  black?: string
  red?: string
  green?: string
  yellow?: string
  blue?: string
  magenta?: string
  cyan?: string
  white?: string
  brightBlack?: string
  brightRed?: string
  brightGreen?: string
  brightYellow?: string
  brightBlue?: string
  brightMagenta?: string
  brightCyan?: string
  brightWhite?: string
}
