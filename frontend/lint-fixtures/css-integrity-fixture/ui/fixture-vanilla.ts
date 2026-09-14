// Rule-3 fixture for a vanilla-emitted component: its identity is derived from
// `className =`, so a surface repainting it must be reported exactly like a
// surface repainting fixture-widget.tsx.
export function createFixtureVanilla(): HTMLElement {
  const el = document.createElement('span')
  el.className = 'fixture-vanilla'
  return el
}
