// ═══════════════════════════════════════════════════════════════════════════
// SkillViewHeader — the warning above the split, not the skill's identity.
//
// The identity (name, provenance, path and enable switch) lives in the left
// rail with the Files and Check rows. That makes the right side purely the
// selected document and lets it reach the pane's top. THE CHANGED WARNING
// STAYS FULL-WIDTH ABOVE THE SPLIT: a 260px rail cannot hold a danger card
// with its sentence and Re-approve action without squeezing it into a box
// nobody can read. This is the only warning above the split; loading and
// unavailable states use the same full-width slot.
//
// Moved out of skill-view-content.tsx (nocx-4m1n1) when the body landed
// beside it: the content class was becoming two unrelated concerns in one
// file — the tab's lifecycle (store subscription, resolution, re-read on
// activation) and the view's markup. Splitting them is not a refactor for
// its own sake; it is what keeps the lifecycle file readable.
//
// THE CHECK/RE-CHECK BUTTON LIVES IN THE CHECK PANE, NOT HERE (nocx-dh14q).
// An earlier version of this header carried a disabled placeholder for it;
// the real button, and the model call it spends, now belong entirely to
// SkillViewCheck (skill-view-body.tsx's "Check" group) — one control for
// one action, rather than a second surface naming an action a different
// one performs.
// ═══════════════════════════════════════════════════════════════════════════

import { Show, type JSX } from 'solid-js'
import { Badge, Button, Checkbox, FactList, Stack, StatusCard, type Fact } from '../ui'
import { provenanceTone } from '../skills-presentation'
import { formatTimestamp } from '../ui/format-time'
import type { Skill } from '../skills-store'

export type ViewState =
  { kind: 'loading' } | { kind: 'unavailable'; message: string } | { kind: 'ready'; skill: Skill }

export interface SkillViewHeaderProps {
  name: string
  state: ViewState
  busy: boolean
  onApprove: () => void
}

export interface SkillViewIdentityProps {
  skill: Skill
  busy: boolean
  onToggle: (enabled: boolean) => void
}

/**
 * Where the skill lives, and — when a stranger's document put it there — THE
 * WHOLE RECORD of what that acquisition resolved to (nocx-ojfuc.3): the
 * address, when the bytes were taken, and what the address served. Moved
 * here from the modal card's own `cardFacts` (skills-section.tsx, deleted in
 * nocx-54a2c) — the record was readable only by opening skills.json by hand,
 * or by opening the card the row's Open button used to show; it is now in the
 * identity rail, since a person deciding about a skill cannot read this off
 * anything else.
 *
 * WHAT IS NOT HERE, and is not missing either: how the skill was found. The
 * search, the page the model read, the links it followed are not recorded
 * anywhere (internal/skill/store_doc.go says why at length), so there is
 * nothing to draw and no row that would quietly imply there was.
 *
 * The digest carries its qualification ON its row rather than in a
 * paragraph underneath, which is what `note` is for: a caveat two elements
 * away is a caveat free to end up describing the wrong value. And the
 * qualification is the honest one — a hash of bytes a stranger served is
 * change detection, not a vouch. A row is drawn only for a digest that was
 * recorded: a source predating the field has none, and an empty value would
 * read as a skill whose bytes hashed to nothing.
 */
const recordFacts = (skill: Skill): Fact[] => {
  const facts: Fact[] = [{ name: 'Where it is', value: skill.path }]
  const source = skill.source
  if (source) {
    facts.push({ name: 'Installed from', value: source.url })
    // An unparseable time draws no row rather than an empty one. The backend
    // refuses a source row whose installedAt is not RFC3339, so this cannot
    // arrive from the product — but a fact whose value is the empty string
    // is a row asserting nothing, and the list's contract is that every fact
    // given is a row.
    const takenOn = formatTimestamp(Date.parse(source.installedAt))
    if (takenOn) facts.push({ name: 'Taken on', value: takenOn })
    if (source.digest) {
      facts.push({
        name: 'What that address served',
        value: source.digest,
        note: 'A sha256 of the bytes as they arrived, not a verdict on them.',
      })
    }
  }
  return facts
}

export function SkillViewIdentity(props: SkillViewIdentityProps): JSX.Element {
  return (
    <div class="skill-view__identity">
      <Stack gap="loose">
        <div class="skill-view__title">
          <h1 class="skill-view__name">{props.skill.name}</h1>
          <Badge tone={provenanceTone(props.skill.provenance)}>{props.skill.provenance}</Badge>
        </div>
        <FactList facts={recordFacts(props.skill)} ariaLabel="Where this skill lives" />
        <Checkbox
          variant="switch"
          label="Offer this skill to the assistant"
          checked={props.skill.enabled}
          disabled={props.busy}
          onChange={(enabled) => props.onToggle(enabled)}
        />
      </Stack>
    </div>
  )
}

export function SkillViewHeader(props: SkillViewHeaderProps): JSX.Element {
  const readySkill = (): Skill | null => (props.state.kind === 'ready' ? props.state.skill : null)
  const unavailableMessage = (): string =>
    props.state.kind === 'unavailable' ? props.state.message : ''
  const warningVisible = (): boolean =>
    props.state.kind !== 'ready' || readySkill()?.status === 'changed'

  return (
    <div class="skill-view__warning" data-populated={warningVisible() ? 'true' : undefined}>
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
      <Show when={readySkill()?.status === 'changed'}>
        <StatusCard
          tone="danger"
          title="The bytes under this skill have changed"
          description="They are no longer the bytes recorded for it, so the assistant is not offered it whatever the switch says. Read what is here now, and re-approve it if you want it back."
          action={
            <Button size="sm" disabled={props.busy} onClick={props.onApprove}>
              Re-approve
            </Button>
          }
        />
      </Show>
    </div>
  )
}
