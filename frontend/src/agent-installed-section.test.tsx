// @vitest-environment jsdom
/**
 * The agent section's own tests (nocx-a62cl.12.2, ADR-anntnxdj decision 6).
 *
 * What a person can do that they could not before: open Settings and SEE which
 * of the agents the assistant can run are on this computer, with the version
 * each reports and whether it says it can answer — and ask again after
 * installing one.
 *
 * Asserted through the seam a person reaches: the page renders the rows the
 * backend answered, each readiness state in the product's own words, the read
 * happens when the section opens, and the refresh control re-asks with
 * `refresh: true` rather than showing a cached answer.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { AgentInstalledSection } from './agent-installed-section'
import { setActiveLocale } from './i18n/locale-model'
import type { Agent } from './generated/agentRecords.detect'
import type { AgentRecordsClient } from './agent-records-client'

const PI: Agent = {
  id: 'pi',
  displayName: 'pi',
  found: true,
  version: 'pi 1.5.2',
  readiness: 'ready',
}

const CLAUDE: Agent = {
  id: 'claude',
  displayName: 'Claude Code',
  found: false,
  version: '',
  readiness: 'not_installed',
}

function agent(readiness: Agent['readiness']): Agent {
  return {
    id: `agent-${readiness}`,
    displayName: `Agent ${readiness}`,
    found: readiness !== 'not_installed',
    version: readiness === 'not_installed' ? '' : `1.0.0-${readiness}`,
    readiness,
  }
}

function client(answer: { agents: Agent[] } = { agents: [CLAUDE, PI] }) {
  const detect = vi.fn().mockResolvedValue(answer)
  return { api: { detect } as unknown as AgentRecordsClient, detect }
}

function row(container: HTMLElement, id: string): HTMLElement {
  const found = container.querySelector<HTMLElement>(`[data-agent="${id}"]`)
  if (found === null) throw new Error(`no row for ${id}`)
  return found
}

describe('AgentInstalledSection', () => {
  afterEach(() => {
    cleanup()
    setActiveLocale('en')
  })

  it('lists every agent the backend answered, with its version and what it says about being ready', async () => {
    const { api, detect } = client()
    const view = render(() => <AgentInstalledSection client={api} />)

    await waitFor(() => expect(view.container.textContent).toContain('pi 1.5.2'))
    expect(detect).toHaveBeenCalledTimes(1)
    expect(detect).toHaveBeenCalledWith(false)

    // Found: the name, the version the command printed, and the readiness its
    // own probe reported.
    const pi = row(view.container, 'pi')
    expect(pi.textContent).toContain('pi')
    expect(pi.textContent).toContain('pi 1.5.2')
    expect(pi.textContent).toContain('Ready')

    // Not found: the name, and the one word for it. No version, because there
    // is no command that could have printed one.
    const claude = row(view.container, 'claude')
    expect(claude.textContent).toContain('Claude Code')
    expect(claude.textContent).toContain('Not installed')
    expect(claude.textContent).not.toContain('pi 1.5.2')
  })

  it("renders every readiness state in the product's own words", async () => {
    const words: [Agent['readiness'], string][] = [
      ['not_installed', 'Not installed'],
      ['installed', 'Installed'],
      ['ready', 'Ready'],
      ['not_ready', 'Not ready'],
      ['invalid', 'Not usable'],
      ['unknown', 'Could not tell'],
    ]
    for (const [readiness, text] of words) {
      const { api } = client({ agents: [agent(readiness)] })
      const view = render(() => <AgentInstalledSection client={api} />)
      await waitFor(() => expect(view.container.textContent).toContain(text))
      cleanup()
    }
  })

  it('asks again with refresh when the refresh control is pressed, and shows the new answer', async () => {
    const detect = vi
      .fn()
      .mockResolvedValueOnce({ agents: [PI] })
      .mockResolvedValueOnce({ agents: [agent('not_installed')] })
    const api = { detect } as unknown as AgentRecordsClient
    const view = render(() => <AgentInstalledSection client={api} />)

    await waitFor(() => expect(view.container.textContent).toContain('Ready'))
    expect(detect).toHaveBeenCalledTimes(1)
    expect(detect).toHaveBeenLastCalledWith(false)

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => expect(detect).toHaveBeenCalledTimes(2))
    // `true`, not a second plain read: the backend caches the pass, so a
    // refresh that did not ask for one would redraw the answer that is already
    // on screen — which is what a person pressing it has just tried to escape.
    expect(detect).toHaveBeenLastCalledWith(true)
    await waitFor(() => expect(view.container.textContent).toContain('Not installed'))
    expect(view.container.textContent).not.toContain('Ready')
  })

  it.each([
    [
      'en',
      'Refresh',
      'Refreshing… Shown agents are from the previous result.',
      'Agent check complete.',
    ],
    [
      'ru',
      'Обновить',
      'Обновляем… Показаны агенты из предыдущего результата.',
      'Проверка агентов завершена.',
    ],
  ] as const)(
    'qualifies retained rows during refresh and announces success in %s',
    async (locale, refresh, refreshing, complete) => {
      setActiveLocale(locale)
      let finish!: (answer: { agents: Agent[] }) => void
      const pending = new Promise<{ agents: Agent[] }>((resolve) => {
        finish = resolve
      })
      const detect = vi
        .fn()
        .mockResolvedValueOnce({ agents: [PI] })
        .mockReturnValueOnce(pending)
      const view = render(() => (
        <AgentInstalledSection client={{ detect } as unknown as AgentRecordsClient} />
      ))
      await waitFor(() => expect(view.container.textContent).toContain(PI.version))

      fireEvent.click(screen.getByRole('button', { name: refresh }))
      await waitFor(() =>
        expect(
          screen.getAllByRole('status').some((status) => status.textContent === refreshing),
        ).toBe(true),
      )
      expect(screen.getByRole('list').getAttribute('aria-busy')).toBe('true')
      expect(row(view.container, 'pi').textContent).toContain(PI.version)
      expect(screen.getByRole<HTMLButtonElement>('button', { name: refresh }).disabled).toBe(true)

      finish({ agents: [CLAUDE] })
      await waitFor(() =>
        expect(
          screen.getAllByRole('status').some((status) => status.textContent === complete),
        ).toBe(true),
      )
      expect(screen.getByRole('list').getAttribute('aria-busy')).toBe('false')
      expect(view.container.textContent).not.toContain(PI.version)
      expect(row(view.container, 'claude').textContent).toContain(CLAUDE.displayName)
    },
  )

  it('says why a refresh failed and keeps the answer it already had', async () => {
    const detect = vi
      .fn()
      .mockResolvedValueOnce({ agents: [PI] })
      .mockRejectedValueOnce(new Error('the login shell /bin/bash exited: exit status 1'))
    const api = { detect } as unknown as AgentRecordsClient
    const view = render(() => <AgentInstalledSection client={api} />)

    await waitFor(() => expect(view.container.textContent).toContain('Ready'))
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    // The reason, on the page: a read that could not happen is not "nothing is
    // installed", and drawing that over the answer already on screen would be
    // the silent degrade AGENTS.md names.
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('exit status 1'))
    expect(view.container.textContent).toContain('Ready')
  })
})
