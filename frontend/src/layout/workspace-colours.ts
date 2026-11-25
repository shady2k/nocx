// The colours a workspace can be given (nocx-2mipw; workspaces UX design §3,
// which has said `id, name, colour` since the first draft).
//
// THE PALETTE FOR EVERYTHING A PERSON COLOURS IN THE STRIP — a workspace, and
// since 2026-08-18 a tab as well.
//
// It began as a second palette beside `tab-colours.ts`, which offered four
// THEME accents on the argument that a tab decorated under one theme must
// still read under another. A workspace's colour is the opposite requirement:
// it is the identity of a container the user made, so it must NOT shift when
// the theme does, any more than its name would. That reasoning was sound
// about workspaces and wrong about the difference: identity is what a colour
// on a TAB is for too, and offering two palettes for one act — "colour this
// thing in the strip" — was a distinction visible only from inside the code.
// One set now, and `tab-colours.ts` is gone.
//
// The hues are declared IDENTICALLY in every theme file (styles/themes/*.css,
// the `--ws-*` block), which is what makes "does not vary with the theme" true
// while leaving each theme free to override one its ground cannot show — the
// arrangement ADR-0013 asks for, rather than a single shared file the
// integrity gate would reject.
//
// NINE, AND NOT FIFTEEN, which is what Edge offers. Past roughly nine hues an
// 8px dot stops being tellable apart, and a colour the user can choose but
// cannot then read back is worse than one fewer choice.

/** The stored value of a coloured workspace. `null` is an uncoloured one —
 *  the default workspace, and any row the backend minted for a session
 *  nobody recorded. It is not a tenth colour. */
export type WorkspaceColour =
  'grey' | 'blue' | 'cyan' | 'green' | 'yellow' | 'orange' | 'red' | 'pink' | 'purple'

export interface WorkspaceColourChoice {
  readonly key: WorkspaceColour
  /** The catalogue key for what the picker's accessible name says. A colour
   *  word rather than a token role: the user is choosing a colour, and roles
   *  are the kit's vocabulary, not theirs. Resolved at render, where the
   *  English fallback is supplied, never when this table loads. */
  readonly labelKey: string
}

export const WORKSPACE_COLOURS: readonly WorkspaceColourChoice[] = [
  { key: 'grey', labelKey: 'workspaceColour.grey' },
  { key: 'blue', labelKey: 'workspaceColour.blue' },
  { key: 'cyan', labelKey: 'workspaceColour.cyan' },
  { key: 'green', labelKey: 'workspaceColour.green' },
  { key: 'yellow', labelKey: 'workspaceColour.yellow' },
  { key: 'orange', labelKey: 'workspaceColour.orange' },
  { key: 'red', labelKey: 'workspaceColour.red' },
  { key: 'pink', labelKey: 'workspaceColour.pink' },
  { key: 'purple', labelKey: 'workspaceColour.purple' },
]

/** Whether a stored value is one this renderer can draw.
 *
 *  A colour it does not recognise renders as NO colour rather than as a
 *  broken swatch — the same rule tabs already follow, and for the same
 *  reason: what is stored belongs to the store, and an unknown value is a
 *  fact about a newer or older renderer, never a reason to refuse to draw
 *  the strip. */
export function isWorkspaceColour(value: string | null | undefined): value is WorkspaceColour {
  return value != null && WORKSPACE_COLOURS.some((c) => c.key === value)
}

/**
 * The colour to offer for a workspace about to be created: the one fewest
 * existing workspaces are using, and the earliest in palette order among
 * equals.
 *
 * WHY LEAST-USED AND NOT A HASH OF THE ID. The first version derived a colour
 * by hashing the id, which is stable and requires no state — and gives a
 * collision on the second workspace as readily as on the ninth, at random. A
 * default that collides on a fresh install is a default nobody keeps, and
 * then the picker is not offering a suggestion, it is offering a chore.
 *
 * IT DEGRADES INSTEAD OF FAILING. Past nine workspaces every colour is taken,
 * so this begins handing out seconds — evenly, because it counts. Collisions
 * are allowed: a colour is a recognition aid and never the identifier, which
 * is the name and the position. A tenth workspace refused for want of a
 * colour would be absurd; a tenth workspace sharing blue with the second is
 * merely ordinary, and Chrome and Edge have always worked that way.
 *
 * `taken` may contain nulls and values this renderer does not know — the
 * default workspace's absence and a newer renderer's colour both land here —
 * and both are simply not counted, which is the honest reading: neither is a
 * palette entry this picker can offer.
 */
export function leastUsedWorkspaceColour(
  taken: readonly (string | null | undefined)[],
): WorkspaceColour {
  const counts = new Map<WorkspaceColour, number>(WORKSPACE_COLOURS.map((c) => [c.key, 0]))
  for (const value of taken) {
    if (isWorkspaceColour(value)) counts.set(value, (counts.get(value) ?? 0) + 1)
  }
  let best = WORKSPACE_COLOURS[0].key
  for (const { key } of WORKSPACE_COLOURS) {
    if ((counts.get(key) ?? 0) < (counts.get(best) ?? 0)) best = key
  }
  return best
}
