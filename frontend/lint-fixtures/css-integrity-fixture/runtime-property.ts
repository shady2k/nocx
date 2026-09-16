/* The property below is set from script, the way sidebar-width.ts sets
   --sidebar-width. Its stylesheet reference carries a fallback and no rule
   declares it, and the integrity checker must NOT report it: the fallback is
   the value before the first write, not the only value there will ever be. */

export function applyFixtureWidth(el: HTMLElement, width: number): void {
  el.style.setProperty('--fixture-runtime-width', `${width}px`)
}
