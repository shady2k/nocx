import { log } from './log'
import { showToast } from './ui/toast'

export const TERMINAL_FONT_KEY = 'terminal.fontFamily'
let revision = 0
const subscribers = new Set<() => void>()

/** A font can change its ink metrics without changing the probe's size. */
export function subscribeTerminalFontChanges(fn: () => void): () => void {
  subscribers.add(fn)
  return () => {
    subscribers.delete(fn)
  }
}

function notifyFontChanged(): void {
  for (const subscriber of subscribers) subscriber()
}

/** The private-use face every terminal stack ends with (nocx-15b13): the
 *  family `styles/fonts.css` declares for U+E000-F8FF and U+F0000-FFFFD, and a
 *  sample inside that range. A bare `load()` names no text, and a face whose
 *  `unicode-range` excludes U+0020 is not needed for it — so the sample is
 *  what makes the call actually fetch the face. */
const FALLBACK_FACE = '400 14px "Nerd Symbols Mono"'
const FALLBACK_SAMPLE = '\ue0b0'

/** Load the terminal chain's private-use face before the first pane. No font
 *  in the app's own stacks carries Nerd Font symbols, so the browser draws
 *  their codepoints from the primary font's .notdef until this face arrives —
 *  and .notdef is exactly one cell wide. cell-fit measures a cell when it is
 *  first painted, verdicts "lands on its own", and boxes nothing; the real
 *  glyph then swaps in at 1em, the row lays out 0.4em wider than the grid, and
 *  every column after it moves (nocx-peg8e.1.3: yazi's rows with private-use
 *  icons, one cell right of the rows without them). WebKit — the shipped
 *  webview — dispatches no `loadingdone` for this face, so waiting for an
 *  event is not a fix; the face is fetched before anything can measure it.
 *  A failure is the old degrade (tofu) and is logged; the panes' own re-measure
 *  on a later load event still covers a face that arrives afterwards on an
 *  engine that reports it. */
async function loadTerminalFallback(): Promise<void> {
  const fonts = document.fonts
  if (fonts === undefined || typeof fonts.load !== 'function') return
  try {
    await fonts.load(FALLBACK_FACE, FALLBACK_SAMPLE)
  } catch (error) {
    log.warn('the terminal’s private-use font could not be loaded', { error: String(error) })
  }
}

/** Load all terminal faces before changing the shared font. A later choice
 * wins even when an earlier font request is still in flight. */
export async function applyTerminalFont(
  value: unknown,
  root: HTMLElement = document.documentElement,
): Promise<void> {
  const request = ++revision
  // THE CHAIN'S PRIVATE-USE FACE, WHATEVER FAMILY IS CHOSEN (nocx-peg8e.1.3).
  // It belongs to the chain rather than to one selection, so it is loaded
  // before the branch that decides between the theme stack and Nordwand — and
  // before the first pane, because the startup path awaits this function.
  await loadTerminalFallback()
  if (request !== revision) return
  if (value !== 'nordwand-mono') {
    if (root.dataset.terminalFont === undefined) return
    delete root.dataset.terminalFont
    notifyFontChanged()
    return
  }
  if (root.dataset.terminalFont === value) return
  try {
    const faces = await Promise.all([
      document.fonts.load('400 14px "Nordwand Mono"'),
      document.fonts.load('italic 400 14px "Nordwand Mono"'),
      document.fonts.load('700 14px "Nordwand Mono"'),
      document.fonts.load('italic 700 14px "Nordwand Mono"'),
    ])
    if (request !== revision) return
    if (faces.some((loaded) => loaded.length === 0)) throw new Error('Missing bundled font face')
    root.dataset.terminalFont = value
    notifyFontChanged()
  } catch (error) {
    if (request !== revision) return
    log.warn('Nordwand Mono could not be loaded', { error: String(error) })
    showToast({
      level: 'danger',
      message: 'Nordwand Mono could not be loaded. The current font is unchanged.',
    })
  }
}
