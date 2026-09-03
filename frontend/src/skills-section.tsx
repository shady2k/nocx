/**
 * The Skills page's list (nocx-fe7fe.3).
 *
 * Every row is the kit's RecordRow. It was not, and the page paid for it: the
 * row was a bare `<div>` wrapping four naked `<div>`s — name, description,
 * `provenance · path` — with the enable switch and the sentence "Built-in
 * skills cannot be deleted" glued to each other underneath, no spacing, no
 * columns, no separators. Nothing in the kit permitted that so much as failed
 * to SEE it: `nocx/no-raw-controls` looks for a reimplemented control, the
 * role rule for a role that duplicates a primitive, the class rule for a
 * kit class copied inline, and a classless roleless div is none of those.
 *
 * The mapping onto the composite, since a record's parts are its argument:
 *
 *   title   the skill's name, which is what it is addressed by.
 *   kind    its provenance, one badge — where the bytes came from is the
 *           record's category, and it is what decides whether Delete exists.
 *   meta    the skill's own description line, from its front matter.
 *   detail  the path, in the detail slot's monospace: verbatim evidence, and
 *           the answer to "which file am I looking at" that the Settings page
 *           is the only place to get.
 *   status  shown ONLY when the bytes changed since approval. Enabled state
 *           is not a status here — the switch beside it already says so, and
 *           a row that reports one fact twice is how the two come to disagree.
 */
import { For, Show, createSignal, onCleanup, onMount } from 'solid-js'
import {
  ActionGroup,
  Button,
  Checkbox,
  EmptyState,
  RecordRow,
  Section,
  Stack,
  StatusCard,
} from './ui'
import { showConfirm } from './ui/dialog'
import { showToast } from './ui/toast'
import type { BadgeTone } from './ui/badge'
import type { Skill, SkillsState, SkillsStore } from './skills-store'

export interface SkillsSectionProps {
  store: SkillsStore
}

/** Provenance as a badge tone: `builtin` is neutral because it is the state
 *  nobody chose, `authored` is what the person wrote, `managed` what the
 *  assistant wrote after they approved it, and `installed` warning because it
 *  is the one provenance whose bytes a stranger wrote — the row should say so
 *  before the person reads the description as though it were their own.
 *
 *  The switch has no default ON PURPOSE: `Skill['provenance']` is a closed
 *  union generated from the contract, so a fifth value fails the return-type
 *  check here rather than rendering as an untoned badge nobody notices. */
function provenanceTone(provenance: Skill['provenance']): BadgeTone {
  switch (provenance) {
    case 'builtin':
      return 'neutral'
    case 'authored':
      return 'info'
    case 'managed':
      return 'success'
    case 'installed':
      return 'warning'
  }
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
    /* The Section is NOT divided: its children are the status cards and the
       list, and the list draws its own separators. Two nested divided stacks
       put the dense rhythm on the whole list as one child and again on every
       row inside it. */
    <Section title="Discovered skills">
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
              description="Add a SKILL.md under your skills directory, or ask the assistant to remember a procedure."
            />
          }
        >
          <Stack divided dense>
            <For each={readySkills()}>
              {(skill) => (
                <RecordRow
                  title={skill.name}
                  kind={{ label: skill.provenance, tone: provenanceTone(skill.provenance) }}
                  meta={skill.description}
                  detail={skill.path}
                  status={
                    skill.status === 'changed'
                      ? { tone: 'error', text: 'Changed since approval' }
                      : undefined
                  }
                  actions={
                    <ActionGroup ariaLabel={`${skill.name} actions`}>
                      <Checkbox
                        variant="switch"
                        checked={skill.enabled}
                        disabled={busy() === skill.name}
                        ariaLabel={`${skill.name} enabled`}
                        onChange={(enabled) => void toggle(skill, enabled)}
                      />
                      {/* Only when the bytes moved. A permanent Re-approve
                          would invite re-approving a skill nobody changed,
                          which is a person clicking past the one prompt that
                          is load-bearing. */}
                      <Show when={skill.status === 'changed'}>
                        <Button
                          size="sm"
                          disabled={busy() === skill.name}
                          onClick={() => void approve(skill)}
                        >
                          Re-approve
                        </Button>
                      </Show>
                      {/* A builtin ships inside the binary, so there is
                          nothing on disk to delete and no button to explain
                          away. The sentence that used to say so sat in the
                          row's body as loose text on every builtin row; the
                          absence says it once and says it everywhere. */}
                      <Show when={skill.provenance !== 'builtin'}>
                        <Button
                          variant="danger"
                          size="sm"
                          disabled={busy() === skill.name}
                          onClick={() => void remove(skill)}
                        >
                          Delete
                        </Button>
                      </Show>
                    </ActionGroup>
                  }
                />
              )}
            </For>
          </Stack>
        </Show>
      </Show>
    </Section>
  )
}
