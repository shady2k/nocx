import { describe, expect, it } from 'vitest'
import { reconcileRailPages, type SettingsPage } from './settings-rail-pages'

function generated(id: string, groupId?: string): SettingsPage {
  return { kind: 'generated', id, title: id, groupId }
}

function component(id: string, title: string, ownsSection?: string): SettingsPage {
  return {
    kind: 'component',
    id,
    title,
    scrollMode: 'page',
    ownsSection,
    renderContent: () => null,
  }
}

describe('reconcileRailPages', () => {
  it('leaves a registry with no collision exactly as it was', () => {
    const pages = [
      component('connections', 'Connections'),
      generated('History'),
      component('about', 'About'),
    ]
    expect(reconcileRailPages(pages)).toEqual(pages)
  })

  it('drops the generated section a component page owns, keeping the page', () => {
    const skills = component('skills', 'Skills', 'Skills')
    const kept = reconcileRailPages([generated('History'), generated('Skills'), skills])
    expect(kept).toEqual([generated('History'), skills])
  })

  it('drops the owned section wherever it sits in the order', () => {
    const skills = component('skills', 'Skills', 'Skills')
    const kept = reconcileRailPages([generated('Skills'), skills, generated('History')])
    expect(kept).toEqual([skills, generated('History')])
  })

  // The defect this module exists for, in the shape it shipped in: a
  // declaration whose section is "Skills" beside a component page titled
  // "Skills", ids differing only in case, and nothing anywhere that could see
  // it. Without ownsSection it is two rail rows under one name.
  it('refuses a generated section that collides with a component page title', () => {
    expect(() => reconcileRailPages([generated('Skills'), component('skills', 'Skills')])).toThrow(
      /two pages answer to one name/,
    )
  })

  it('names both ends of the collision, and how to resolve it', () => {
    let message = ''
    try {
      reconcileRailPages([generated('Skills'), component('skills', 'Skills')])
    } catch (err) {
      message = err instanceof Error ? err.message : String(err)
    }
    expect(message).toContain('generated section "Skills"')
    expect(message).toContain('internal/settings')
    expect(message).toContain('component page "skills"')
    expect(message).toContain('settings.tsx')
    expect(message).toContain('ownsSection')
  })

  it('refuses two component pages with one title', () => {
    expect(() =>
      reconcileRailPages([component('endpoints', 'Endpoints'), component('models', 'Endpoints')]),
    ).toThrow(/both titled "Endpoints"/)
  })

  it('refuses ids that differ only in case, whatever the titles say', () => {
    expect(() =>
      reconcileRailPages([component('vault', 'Protection'), component('Vault', 'Secrets')]),
    ).toThrow(/differ only in case/)
  })

  // An ownsSection naming a section the backend did not declare is tolerated:
  // the page renders with no rows above its content, which is what an older
  // backend looks like. A typo cannot hide a duplicate, because the duplicate
  // it failed to absorb is what throws.
  it('tolerates a page owning a section no declaration produced', () => {
    const skills = component('skills', 'Skills', 'Skills')
    expect(reconcileRailPages([generated('History'), skills])).toEqual([
      generated('History'),
      skills,
    ])
  })
})
