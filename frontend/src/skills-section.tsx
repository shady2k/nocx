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
 *           is the only place to get — JOINED, for a skill that records one,
 *           by a sentence naming the address it was installed from (see
 *           `evidence` below), and for a skill that has been checked, by a
 *           third line naming when and what the model said (see
 *           `checkedLine` below). The rest of the source record — when the
 *           bytes were taken, and what the address served — is read on the
 *           skill's own tab (`openSkill`), where a record is read rather
 *           than scanned.
 *   status  shown ONLY when the bytes changed since the skill was installed.
 *           INSTALLATION, not approval (nocx-hzsxl): the digest was taken
 *           when the bytes landed, and approval only ever admitted them — it
 *           never certified them, so a row saying "since approval" claimed
 *           more for the person's click than the click was. Enabled state
 *           is not a status here — the switch beside it already says so, and
 *           a row that reports one fact twice is how the two come to disagree.
 *
 * THIS PAGE MANAGES SKILLS AND DOES NOT ACQUIRE THEM (nocx-ojfuc.4, policy
 * design §5). The list, the switch, deletion and the changed-bytes status
 * have no conversational substitute and live here. Reading a skill — its
 * file bundle, and the stored or fresh check — lives in the tab `openSkill`
 * opens (nocx-btg7d), which used to be a `Dialog` drawn inline on this page
 * and is deleted from here in nocx-54a2c: a modal is for "answer this now",
 * and reading a skill is the opposite, since nobody is blocked on it, and a
 * tab does not even cover the page underneath it. Acquisition is
 * conversational: the person gives the assistant a link of any kind, nocx
 * resolves it and `skills.install` writes what came back, and the person
 * decides in the approval window — which names the RESOLVED source, the
 * description, the digest and every file that would land, with its bytes.
 * (An earlier version of this comment said the assistant "searches, follows
 * a page to a repository and lists a directory". It does none of those: the
 * registry is 23 tools and only `fetch.url` looks outward, at one address,
 * and `internal/skill/bundle.go` says a bare URL cannot list a directory.
 * The same sentence deleted the resolver from the 09-04 spec §5, which is
 * why it is corrected here rather than quietly rewritten.) The paste box
 * that used to sit in
 * this Section's heading slot asked a person to go and find a raw address by
 * hand, which is exactly the labour the assistant removes; two surfaces
 * owning one input is the defect AGENTS.md names most often, and the one
 * that goes is the one with a substitute. There is deliberately no field on
 * this page a source address can be typed into, and nothing here calls
 * `skills.preview`.
 */
import { For, Show, createSignal, onCleanup, onMount } from 'solid-js'
import {
  ActionGroup,
  Checkbox,
  EmptyState,
  IconButton,
  RecordRow,
  Section,
  Stack,
  StatusCard,
} from './ui'
import { EyeIcon, RefreshIcon, TrashIcon } from './ui/icons'
import { showConfirm } from './ui/dialog'
import { showToast } from './ui/toast'
import { openSkill } from './skill-view'
import { provenanceTone, refusalLabel, shortDate, usageLine } from './skills-presentation'
import type { Skill, SkillsState, SkillsStore } from './skills-store'

export interface SkillsSectionProps {
  store: SkillsStore
}

/**
 * The row's verbatim evidence: the file, and where its bytes came from.
 *
 * THE JUDGEMENT (nocx-qja4m.9), because a record's parts are its argument.
 * The URL JOINS the path on the detail slot rather than replacing it, and is
 * not a new slot of its own:
 *
 * - They answer two different questions, and both are load-bearing for an
 *   installed skill. The path answers "which file am I looking at" — the file
 *   Delete removes and Re-approve adopts, and true of every row in the list.
 *   The URL answers "where did these bytes come from", which is the question
 *   this whole epic exists for. Replacing the path would make installed rows
 *   the only rows that cannot answer the first, and would give one list two
 *   row grammars — the exact defect RecordRow was built to prevent.
 * - The slot already takes several lines ("one line, or a few of them as an
 *   array"), so two is a supported shape, not a widening of the composite.
 *   Adding a slot for one surface is what the kit README forbids while an
 *   existing one fits, and this one fits for the reason the slot names: both
 *   strings are the RECORD's own words, not the composite's prose about it.
 * - Nothing is emitted when there is nothing recorded. A skill moved into the
 *   installed root by hand has no source, and a blank second line reads as a
 *   row that lost something.
 *
 * THE SECOND LINE IS A SENTENCE, NOT A BARE ADDRESS (nocx-ojfuc.3). It used
 * to be the URL alone, on the argument that the slot holds the record's own
 * words rather than the composite's prose about them. That argument is right
 * about the VALUE and wrong about the line: two monospace strings under a
 * title, one a path and one an address, leave the reader to work out which is
 * which and what the second one is a claim ABOUT — installed from, checked
 * against, offered by. Three words in front of it settle that, and the
 * address is still verbatim inside the sentence, which is the part that has
 * to be. It reads as something to act on: this is where these bytes came
 * from, and it is a place you can go and look.
 *
 * The words come FIRST because the slot is one nowrap line that ellipsises: a
 * long address loses its tail either way, and what must survive the cut is
 * what the line is saying. `Installed from` is also the skill's own tab's
 * name for this fact, so the row and the tab call it one thing.
 *
 * `source.installedAt` and `source.digest` travel on the wire because the
 * recorded source is ONE fact and half of it would be a wire that has to be
 * asked twice. They are not on this line: a row is scanned, and the whole
 * record — when it was taken, and what the address served — is read on the
 * skill's own tab, which is where a person goes when the scan raises a
 * question.
 *
 * THE THIRD LINE, WHEN A CHECK EXISTS, is `checkedLine` below: a date and
 * the model's own word, in the same "scanned, not read" register as the
 * other two lines — never a tick or a badge of our own, which would read as
 * an approval this fact never made (nocx-hzsxl's own argument, restated for
 * the check rather than the switch).
 */
const evidence = (skill: Skill): readonly string[] => {
  const lines = skill.source ? [skill.path, `Installed from ${skill.source.url}`] : [skill.path]
  const checked = checkedLine(skill)
  return checked ? [...lines, checked] : lines
}

/**
 * The row's third evidence line: what a stored check found, if anything —
 * moved here from the skill's card (deleted in nocx-54a2c), which used to be
 * the only place a check (nocx-0bsa4.4) or its stored form (`skill.check`,
 * nocx-dh14q) could be read at all.
 *
 * `skill.check` DELIBERATELY CARRIES NO CURRENCY FLAG (its own field comment
 * in generated/skills.list.ts): whether the checked bytes still match what
 * is on disk is `skills.check`'s own answer, computed on demand when the
 * skill's tab opens, because a digest recomputation on this row would put a
 * bundle walk behind every toggle, delete and approve that refreshes this
 * whole list. So "changed since" is read off `skill.status` instead — the
 * row's own existing signal that the bytes moved since installation or the
 * last approval, which is the only currency fact this list already tracks,
 * and close enough that a bundle walk here would buy nothing this row can
 * act on: Re-approve is the one control that ends `status:changed`, and it
 * sits right beside this line.
 */
const checkedLine = (skill: Skill): string | null => {
  const check = skill.check
  if (!check) return null
  const date = shortDate(check.at)
  return skill.status === 'changed'
    ? `Checked ${date}, and the files have changed since`
    : `Checked ${date} — ${check.verdict}`
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
  const refused = () => {
    const current = state()
    return current.kind === 'ready' ? current.refused : []
  }
  const unavailable = () => {
    const current = state()
    return current.kind === 'unavailable' ? current : null
  }

  // The Section is NOT divided: its children are the status cards and the
  // list, and the list draws its own separators. Two nested divided stacks
  // put the dense rhythm on the whole list as one child and again on every
  // row inside it.
  //
  // The FRAGMENT is here because refused directories are a second Section
  // rather than rows in this one — see the comment on it below.
  return (
    <>
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
                    detail={[usageLine(skill.usage), ...evidence(skill)]}
                    status={
                      skill.autoOff
                        ? {
                            tone: 'warning',
                            text: `Switched off by nocx on ${shortDate(skill.autoOff.at)}`,
                          }
                        : skill.status === 'changed'
                          ? { tone: 'error', text: 'Changed since installation' }
                          : undefined
                    }
                    /* Enabling a skill is the record's STATE, not an action on
                     it, and the kit's state cell is where the row keeps it
                     (nocx-xa0cq). It used to be the first child of the action
                     group below, where the group's own contents decided its
                     position: a builtin row has no buttons at all, an authored
                     one has Delete, a changed one has Re-approve and Delete —
                     so the same switch stood in three places down a list that
                     is read by scanning. */
                    state={
                      <Checkbox
                        variant="switch"
                        checked={skill.enabled}
                        disabled={busy() === skill.name}
                        ariaLabel={`${skill.name} enabled`}
                        onChange={(enabled) => void toggle(skill, enabled)}
                      />
                    }
                    /* The group used to be drawn only when the row had an
                     action to put in it: with the switch moved into the state
                     cell, a builtin approved row had none, and a named
                     `role="group"` around nothing announces a boundary with
                     nothing on the other side of it. Read ended that — every
                     row can be read, so the group always has at least one
                     control and the guard would now be a condition that is
                     true on every row the product can produce. */
                    actions={
                      <ActionGroup ariaLabel={`${skill.name} actions`}>
                        {/* EVERY row, every provenance. Reading is not
                          writing, so a builtin is as readable as a skill the
                          person wrote — and the builtin is the row that needs
                          it most, because its path names a file nothing on
                          the machine can open. It is the first control
                          because it is the one that changes nothing; the two
                          that do follow it. And it is the one control here
                          that `busy` does not disable, for the same reason:
                          `busy` marks a row whose STATE is mid-change, and a
                          person is entitled to read the skill while a toggle
                          is in flight.

                          It opens the skill's own TAB (nocx-btg7d, nocx-54a2c)
                          rather than a card drawn inline on this page — the
                          modal this used to open cost the page a person was
                          on, and a check is now remembered, so a magnifier
                          that opened the card and started an audit in one
                          press (nocx-6jc4f) is gone: the tab shows a stored
                          check's verdict and a Check/Re-check button of its
                          own, and starting an audit from a list row would be
                          a second place that press could come from. The name
                          is the whole of what a person is told, because the
                          control is an eye — so it is on `title` for the
                          pointer and on `ariaLabel` for the screen reader,
                          and it names its ROW, since identical glyphs down a
                          list need to say which record they belong to. */}
                        <IconButton
                          size="sm"
                          title="Open"
                          ariaLabel={`Open ${skill.name}`}
                          onClick={() => openSkill(skill.name)}
                        >
                          <EyeIcon />
                        </IconButton>
                        {/* Only when the bytes moved. A permanent Re-approve
                            would invite re-approving a skill nobody changed,
                            which is a person clicking past the one prompt that
                            is load-bearing. */}
                        <Show when={skill.status === 'changed'}>
                          <IconButton
                            size="sm"
                            title="Re-approve"
                            ariaLabel={`Re-approve ${skill.name}`}
                            disabled={busy() === skill.name}
                            onClick={() => void approve(skill)}
                          >
                            <RefreshIcon />
                          </IconButton>
                        </Show>
                        {/* A builtin ships inside the binary, so there is
                            nothing on disk to delete and no button to explain
                            away. The sentence that used to say so sat in the
                            row's body as loose text on every builtin row; the
                            absence says it once and says it everywhere. */}
                        <Show when={skill.provenance !== 'builtin'}>
                          <IconButton
                            size="sm"
                            title="Delete"
                            ariaLabel={`Delete ${skill.name}`}
                            disabled={busy() === skill.name}
                            onClick={() => void remove(skill)}
                          >
                            <TrashIcon />
                          </IconButton>
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
      <Show when={refused().length > 0}>
        {/* A SECOND SECTION, not a badge on a row in the first (nocx-j0lei).
          These are not skills: nothing here can be enabled, deleted or
          opened, and none of it reaches the assistant. Putting them among
          the skills would give each a switch that governs nothing and a
          provenance badge that implies it is in use, which is the "soft
          degrade the UI contradicts" AGENTS.md names — the same defect as
          the silence it replaces, wearing better clothes.

          The row is the kit's RecordRow like every other row on this page.
          Its `status` is the closed reason, so a scan down the column says
          what KIND of problem each is; the sentence in `detail` is what the
          person acts on, and the path beside it is the file to open. There
          are no actions: nocx cannot fix any of these, and a button that
          could only fail is the shape this page removes elsewhere. */}
        <Section title="Not indexed">
          <Stack divided dense>
            <For each={refused()}>
              {(entry) => (
                <RecordRow
                  title={entry.directory}
                  kind={{ label: entry.provenance, tone: provenanceTone(entry.provenance) }}
                  meta={entry.detail}
                  detail={entry.path}
                  status={{ tone: 'error', text: refusalLabel(entry.reason) }}
                  /* NULL, which is the kit's own vocabulary rather than a
                     forgotten prop: `actions` is required so a row cannot
                     lose its controls by accident, and null is how a row
                     says it HAS none. */
                  actions={null}
                />
              )}
            </For>
          </Stack>
        </Section>
      </Show>
    </>
  )
}
