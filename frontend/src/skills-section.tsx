import { For, Show, createSignal, onCleanup, onMount } from 'solid-js'
import { Button, Checkbox, EmptyState, Section, Stack, StatusCard } from './ui'
import { showConfirm } from './ui/dialog'
import { showToast } from './ui/toast'
import type { Skill, SkillsState, SkillsStore } from './skills-store'

export interface SkillsSectionProps {
  store: SkillsStore
}

export function SkillsSection(props: SkillsSectionProps) {
  const [state, setState] = createSignal<SkillsState>({ kind: 'loading' })
  const [busy, setBusy] = createSignal<string | null>(null)

  onMount(() => {
    const unsubscribe = props.store.subscribe(setState)
    onCleanup(unsubscribe)
    void props.store.refresh()
  })

  async function toggle(skill: Skill, enabled: boolean): Promise<void> {
    setBusy(skill.name)
    try {
      await props.store.setEnabled(skill.name, enabled)
    } catch (err) {
      showToast({ level: 'danger', message: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(null)
    }
  }

  async function remove(skill: Skill): Promise<void> {
    if (!(await showConfirm(`Delete “${skill.name}”?`, 'Delete', 'Cancel'))) return
    setBusy(skill.name)
    try {
      await props.store.remove(skill.name)
      showToast({ level: 'success', message: `Deleted “${skill.name}”` })
    } catch (err) {
      showToast({ level: 'danger', message: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(null)
    }
  }

  async function approve(skill: Skill): Promise<void> {
    setBusy(skill.name)
    try {
      await props.store.approve(skill.name)
      showToast({ level: 'success', message: `Re-approved “${skill.name}”` })
    } catch (err) {
      showToast({ level: 'danger', message: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(null)
    }
  }

  const readySkills = () => {
    const current = state()
    return current.kind === 'ready' ? current.skills : []
  }
  const unavailable = () => {
    const current = state()
    return current.kind === 'unavailable' ? current : null
  }

  return (
    <Section title="Skills" divided dense>
      <Show when={state().kind === 'loading'}>
        <StatusCard
          tone="neutral"
          title="Loading skills"
          description="Reading the discovered skills."
        />
      </Show>
      <Show when={unavailable()}>
        <StatusCard
          tone="danger"
          title="Skills could not be read"
          description={`${unavailable()?.message ?? 'Unknown error'} Path: ${unavailable()?.documentPath || 'skills.json'}`}
        />
      </Show>
      <Show when={state().kind === 'ready'}>
        <Show
          when={readySkills().length > 0}
          fallback={
            <EmptyState
              title="No skills discovered"
              description="Add a SKILL.md under your skills directory."
            />
          }
        >
          <Stack gap="default">
            <For each={readySkills()}>
              {(skill) => (
                <div data-skill-name={skill.name}>
                  <div>{skill.name}</div>
                  <div>{skill.description}</div>
                  <div>
                    {skill.provenance} · {skill.path}
                  </div>
                  <Show when={skill.status === 'changed'}>
                    <StatusCard
                      tone="danger"
                      title="Changed since approval"
                      description={`The person approved different bytes at ${skill.path}.`}
                    />
                    <Button disabled={busy() === skill.name} onClick={() => void approve(skill)}>
                      Re-approve
                    </Button>
                  </Show>
                  <Checkbox
                    variant="switch"
                    checked={skill.enabled}
                    disabled={busy() === skill.name}
                    ariaLabel={`${skill.name} enabled`}
                    onChange={(enabled) => void toggle(skill, enabled)}
                  />
                  <Show when={skill.provenance === 'builtin'}>
                    <span>Built-in skills cannot be deleted.</span>
                  </Show>
                  <Show when={skill.provenance !== 'builtin'}>
                    <Button
                      variant="danger"
                      disabled={busy() === skill.name}
                      onClick={() => void remove(skill)}
                    >
                      Delete
                    </Button>
                  </Show>
                </div>
              )}
            </For>
          </Stack>
        </Show>
      </Show>
    </Section>
  )
}
