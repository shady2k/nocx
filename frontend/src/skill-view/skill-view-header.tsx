// ═══════════════════════════════════════════════════════════════════════════
// SkillViewHeader — what a person needs to answer "which skill is this, and
// is it on": name, provenance, path, and the enable switch.
//
// Moved out of skill-view-content.tsx (nocx-4m1n1) when the body landed
// beside it: the content class was becoming two unrelated concerns in one
// file — the tab's lifecycle (store subscription, resolution, re-read on
// activation) and the header's own markup. Splitting them is not a
// refactor for its own sake; it is what keeps the lifecycle file readable
// once it also wires the file list and the split.
//
// THE CHECK/RE-CHECK BUTTON LIVES IN THE CHECK PANE, NOT HERE (nocx-dh14q).
// An earlier version of this header carried a disabled placeholder for it;
// the real button, and the model call it spends, now belong entirely to
// SkillViewCheck (skill-view-body.tsx's "Check" group) — one control for
// one action, rather than a second surface naming an action a different
// one performs.
// ═══════════════════════════════════════════════════════════════════════════

import { Show, type JSX } from 'solid-js'
import { Badge, Checkbox, FactList, Stack, StatusCard } from '../ui'
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
          </Stack>
        )}
      </Show>
    </div>
  )
}
