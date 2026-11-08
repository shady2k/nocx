// How a saved connection is named to the person who saved it (nocx-ouru5.1.2).
//
// The owner, 2026-11-08: an ssh tab opened from a saved connection showed the
// host alone — the IP — in the tab, and `dev@<ip> : ~` in the composer. What
// they wanted was BOTH, the name first and the host beside it, because the
// name is the thing they chose and the host is the thing the machine calls
// itself. A connection with no name keeps the host alone.
//
// Two surfaces say it — the tab strip's title and the composer's place line —
// so the wording has one owner here. The two disagreeing is how a tab and the
// prompt beside it start naming different connections.
//
// The composer does NOT use the string: it renders the two facts as separate
// parts of its PromptContext, so the name reads at the same weight as the rest
// of that line and the host keeps its own (strong) emphasis. What both share is
// the RULE — name first, host beside it, host alone when there is no name —
// and this function is it.

/** `Production · 10.0.0.5`, or `10.0.0.5` when the connection has no name,
 *  or `''` when there is no host either (a local pane is not a connection). */
export function connectionLabel(name: string | undefined, host: string): string {
  // A name of spaces is no name: printing " · host" would be a stray
  // separator for a connection whose name field was left blank.
  const chosen = name?.trim() ?? ''
  if (host === '') return ''
  return chosen === '' ? host : `${chosen} · ${host}`
}
