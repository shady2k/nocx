import type { JSX } from 'solid-js'

/**
 * The settings rail's page registry, and the invariant that keeps it one
 * list (nocx-fe7fe.2).
 *
 * The rail is built from TWO sources that never see each other. Generated
 * pages are derived from the sections of the Go-declared settings — one page
 * per distinct `Declaration.section`, in declaration order. Component pages
 * are listed by hand in settings.tsx, each binding the context its surface
 * needs. Until this module existed the two were concatenated and nothing
 * looked at the join, so a declaration whose section happened to equal a
 * component page's title minted a SECOND rail row under the same name — which
 * is exactly what `skills.enabled` did beside the Skills page, both of them
 * filed under Application, one holding a lone toggle and the other the list.
 *
 * The Go side guards its own half thoroughly — `assertValidKey` refuses a
 * duplicate setting key, `RegisterGroup` a duplicate group id,
 * `RegisterSectionGroup` a section claimed twice — and none of them can see a
 * title the frontend invented. So the reconciliation belongs here, where both
 * halves are in one hand, and it answers the collision in two ways because
 * there are two kinds of collision:
 *
 *   INTENDED — a component page DECLARES that it owns a section
 *   (`ownsSection`). The section's declarations then render inside that page
 *   and no generated page is minted for it. This is how a surface with both
 *   controls and content keeps them on one screen: Skills owns the section
 *   holding `skills.enabled`, so the switch sits above the list it governs
 *   rather than on a page of its own.
 *
 *   UNINTENDED — anything else that leaves two rows answering to one name.
 *   That is a defect in code we ship, not a state a person can reach: both
 *   halves are build-time facts from one binary, so it throws, the way the Go
 *   registry panics at init. A rail that quietly shows one of two pages is
 *   how the loser goes on advertising what it can no longer deliver.
 */
export type SettingsPage =
  | { kind: 'generated'; id: string; title: string; groupId?: string }
  // A component page renders itself. It is a thunk rather than a bare
  // Component because such a page needs context the registry does not have —
  // Connections needs the ProfileClient and the connect callback — and binding
  // that at registration is what keeps the registry from having to know it.
  // scrollMode (design spec §3.8): 'page' — PageScroller owns vertical scroll;
  // 'contained' — Page provides a bounded content area and the surface assigns
  // its own scroll owners (e.g. Connections' two-column panels).
  // groupId names a group from the Go-declared catalogue (settings.describe);
  // undefined means the page renders at top level beside the groups.
  // ownsSection names a declared section whose rows this page renders itself,
  // above its own content. The section mints no generated page — see above.
  | {
      kind: 'component'
      id: string
      title: string
      groupId?: string
      description?: string
      actions?: JSX.Element
      scrollMode: 'page' | 'contained'
      ownsSection?: string
      renderContent: () => JSX.Element
    }

/** What a page is called in a refusal — enough for the reader to know which
 *  end to edit, because the two ends are in different languages. */
function describePage(page: SettingsPage): string {
  return page.kind === 'generated'
    ? `the generated section ${JSON.stringify(page.id)} (a Declaration.section in internal/settings)`
    : `the component page ${JSON.stringify(page.id)} (registered in settings.tsx)`
}

/**
 * Drop the generated pages component pages own, then refuse what is left if
 * two pages still answer to one name.
 *
 * Matching is on the TITLE, which is what a person reads in the rail, and on
 * the id compared case-insensitively, which is what the Skills pair differed
 * by — `"Skills"` against `'skills'` — and the reason no existing check could
 * see it.
 */
export function reconcileRailPages(pages: SettingsPage[]): SettingsPage[] {
  const owned = new Set<string>()
  for (const page of pages) {
    if (page.kind === 'component' && page.ownsSection !== undefined) owned.add(page.ownsSection)
  }
  const kept = pages.filter((page) => !(page.kind === 'generated' && owned.has(page.id)))

  const byTitle = new Map<string, SettingsPage>()
  const byId = new Map<string, SettingsPage>()
  for (const page of kept) {
    const sameTitle = byTitle.get(page.title)
    const sameId = byId.get(page.id.toLowerCase())
    const clash = sameTitle ?? sameId
    if (clash !== undefined) {
      const shared =
        sameTitle !== undefined
          ? `both titled ${JSON.stringify(page.title)}`
          : `their ids differ only in case (${JSON.stringify(clash.id)} and ${JSON.stringify(page.id)})`
      throw new Error(
        `settings rail: two pages answer to one name — ${describePage(clash)} and ` +
          `${describePage(page)}, ${shared}. A component page that means to absorb a ` +
          `section names it in ownsSection; otherwise one of the two has to be renamed.`,
      )
    }
    byTitle.set(page.title, page)
    byId.set(page.id.toLowerCase(), page)
  }
  return kept
}
