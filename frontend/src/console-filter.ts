// Temporary workaround: ghostty-web has no log-level option and
// console.logs every unhandled OSC (e.g. Fig/Amazon-Q OSC 697).
// Drop only messages matching "[ghostty-vt] ... warning(osc)".
// Remove once ghostty-web exposes log control.

const origLog = console.log
const origWarn = console.warn

function isGhosttyOSC(args: unknown[]): boolean {
  const s = args.map((a) => (typeof a === 'string' ? a : String(a))).join(' ')
  return s.includes('[ghostty-vt]') && s.includes('warning(osc)')
}

console.log = (...args: unknown[]) => {
  if (!isGhosttyOSC(args)) origLog.apply(console, args)
}

console.warn = (...args: unknown[]) => {
  if (!isGhosttyOSC(args)) origWarn.apply(console, args)
}
