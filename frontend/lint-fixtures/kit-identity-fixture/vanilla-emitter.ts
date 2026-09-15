// Kit-identity fixture: a vanilla-emitted component (ui/meta.ts, ui/secret-chip.ts
// shape). Its identities are what it stamps with `className =` and
// `classList.add(...)`, and the scanner must derive them the way it derives a
// .tsx file's JSX attributes.
export function createFixtureVanilla(): HTMLElement {
  const root = document.createElement('span')
  root.className = 'ui-fixture-vanilla'
  const part = document.createElement('span')
  part.classList.add('ui-fixture-vanilla__part')
  root.append(part)
  // A selector string is not an identity, in a .ts file as in a .tsx one.
  root.querySelector('.ui-fixture-vanilla-qs')
  return root
}
