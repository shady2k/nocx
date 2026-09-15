// iconElement — resolves a kit icon Component into a real, detached
// SVGElement, for imperative code that renders no Solid tree of its own
// (ADR-0012): the terminal screen's vanilla-emitted controls build an icon
// once per block/control and append it, rather than mounting one through
// JSX.
//
// WHY THIS EXISTS RATHER THAN `Icon({}) as Element` (nocx-9bpeq.12 round 3).
//
// vite-plugin-solid's dev transform runs `solid-refresh` on every `.tsx`
// file whenever Vite's `command` is `'serve'` and the mode is not
// `'production'` — true for `vitest` (it drives its own Vite dev server)
// under this repo's `resolve.conditions: ['development', ...]`, and true
// for the dev-web stand. `solid-refresh` wraps every PascalCase top-level
// component export (`isComponentishName`, matched at the Babel level — see
// `solid-refresh/dist/babel.cjs`'s `transformVariableDeclarator`) behind an
// HMR proxy (`solid-refresh/dist/solid-refresh.cjs`'s `createProxy`): the
// real function sits behind a signal, and the proxy's `HMRComp(props)`
// either calls it directly or, when the underlying function carries
// solid-js's `$DEVCOMP` marker, returns `createMemo(() => untrack(() =>
// realFn(props)))` — an ACCESSOR, not the rendered value — because that is
// what lets `<item.icon />` reactively hot-swap the implementation when
// `insert()` calls it.
//
// solid-js's dev-mode `createComponent` (`solid-js/dist/dev.js`'s
// `devComponent`) sets that `$DEVCOMP` marker on the function it is given —
// PERMANENTLY, for the life of that function object — the first time (and
// every time) a component is rendered through Solid's OWN pipeline, which
// is exactly what a computed JSX tag compiles to (`<item.icon />` for a
// ContextMenuItem's icon, e.g. the block header's overflow menu). So an
// icon that is EVER used both ways — `icon: SquareIcon` in a menu item, and
// `SquareIcon({})` called bare elsewhere — has its bare call silently start
// returning the memo accessor instead of an element, from the moment the
// menu first renders it onward: fine alone, broken once the file's other
// tests have opened that menu once. Nothing about the icon itself
// (SquareIcon.tsx is byte-for-byte the shape every other icon is) makes
// this possible; the USAGE PATTERN does.
//
// The fix: run the call inside its OWN detached root (so a memo the proxy
// creates has somewhere to be owned and disposed, instead of leaking with
// "computations created outside a createRoot" on stderr), and RESOLVE the
// result if it comes back as a function — the one shape both the healthy
// path (a raw element, left alone) and the poisoned path (an accessor,
// called once) can produce. Anything else is a loud failure: silently
// stringifying a stray function into a text node is the defect this
// replaces, not a shape to tolerate.
import { createRoot, untrack, type Component } from 'solid-js'

export function iconElement(Icon: Component): SVGElement {
  let result: unknown
  createRoot((dispose) => {
    try {
      result = untrack(() => Icon({}))
      // The poisoned path's shape: an accessor standing in for the
      // rendered element (see the file header). Resolve it once — the
      // healthy path never returns a function, so this never fires there.
      if (typeof result === 'function') result = (result as () => unknown)()
    } finally {
      dispose()
    }
  }, null)
  if (!(result instanceof Element)) {
    throw new Error(
      `iconElement: ${Icon.name || 'the icon'} did not produce an Element (got ${typeof result})`,
    )
  }
  return result as SVGElement
}
