// ═══════════════════════════════════════════════════════════════════════════
// SkillViewHeader — what a person needs to answer "which skill is this, and
// is it on": name, provenance, path, the enable switch, and a Check/Re-check
// button that is not wired yet (Task 10 spends the model call).
//
// Moved out of skill-view-content.tsx (nocx-4m1n1) when the body landed
// beside it: the content class was becoming two unrelated concerns in one
// file — the tab's lifecycle (store subscription, resolution, re-read on
// activation) and the header's own markup. Splitting them is not a
// refactor for its own sake; it is what keeps the lifecycle file readable
// once it also wires the file list and the split.
// ═══════════════════════════════════════════════════════════════════════════

import { Show, type JSX } from 'solid-js'
import { Badge, Button, Checkbox, FactList, Stack, StatusCard } from '../ui'
import { provenanceTone } from '../skills-presentation'
import type { Skill } from '../skills-store'

export type ViewState =
  { kind: 'loading' } | { kind: 'unavailable'; message: string } | { kind: 'ready'; skill: Skill }

export interface SkillViewHeaderProps {
  name: string
  state: ViewState
  busy: boolean
  onToggle: (enabled: boolean) => void
}

export function SkillViewHeader(props: SkillViewHeaderProps): JSX.Element {
  const readySkill = (): Skill | null => (props.state.kind === 'ready' ? props.state.skill : null)
  const unavailableMessage = (): string =>
    props.state.kind === 'unavailable' ? props.state.message : ''

  return (
    <div class="skill-view__header">
      <Show when={props.state.kind === 'loading'}>
        <StatusCard
          tone="neutral"
          title="Loading this skill"
          description={`Reading “${props.name}” from the discovered skills.`}
        />
      </Show>
      <Show when={unavailableMessage()}>
        <StatusCard
          tone="danger"
          title="Skills could not be read"
          description={unavailableMessage()}
        />
      </Show>
      <Show when={readySkill()}>
        {(skill) => (
          <Stack gap="loose">
            <div class="skill-view__title">
              <h1 class="skill-view__name">{skill().name}</h1>
              <Badge tone={provenanceTone(skill().provenance)}>{skill().provenance}</Badge>
            </div>
            <FactList
              facts={[{ name: 'Where it is', value: skill().path }]}
              ariaLabel="Where this skill lives"
            />
            <Checkbox
              variant="switch"
              label="Offer this skill to the assistant"
              checked={skill().enabled}
              disabled={props.busy}
              onChange={(enabled) => props.onToggle(enabled)}
            />
            {/* Not wired: a model call belongs to Task 10's check panel, and
                internal/profile/role.go refuses to spend one silently. This
                button exists so the header names the action before the panel
                that performs it exists — disabled, rather than wired to
                nothing, so pressing it cannot look like it did something. */}
            <Show when={skill().provenance !== 'builtin'}>
              <Button
                disabled
                title="Reading this skill with a model is not wired up yet"
                onClick={() => {}}
              >
                {skill().check ? 'Re-check' : 'Check this skill'}
              </Button>
            </Show>
          </Stack>
        )}
      </Show>
    </div>
  )
}
