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
 *           by the address it was installed from (see `evidence` below).
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
import { classifyPastedSource } from './api/api-paths'
import { SkillsInstallDialog } from './skills-install-dialog'
import type { SkillsPreview } from './generated/skills.preview'
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
 * `source.installedAt` travels on the wire because the recorded source is one
 * fact and half of it would be a wire that has to be asked twice; it is not
 * drawn here, because when a skill was fetched is not evidence about the
 * bytes, and a third string would compete with the two that are.
 */
const evidence = (skill: Skill): readonly string[] =>
  skill.source ? [skill.path, skill.source.url] : [skill.path]

export function SkillsSection(props: SkillsSectionProps) {
  const [state, setState] = createSignal<SkillsState>({ kind: 'loading' })
  const [busy, setBusy] = createSignal<string | null>(null)

  /* ── Installing a skill by its URL (nocx-qja4m.6) ──────────────────────
     The ask's state lives here rather than inside the dialog, the way the
     API pane owns the Postman ask's: the calls are the surface's, the
     dialog draws what it is handed, and a component that fetched on its own
     would be a second place the list can change from. */
  const [installOpen, setInstallOpen] = createSignal(false)
  const [installUrl, setInstallUrl] = createSignal('')
  const [installPreview, setInstallPreview] = createSignal<SkillsPreview | null>(null)
  const [installRefusal, setInstallRefusal] = createSignal('')
  const [installBusy, setInstallBusy] = createSignal(false)

  /** What the typed text IS — asked once, of the module that owns the
   *  question for the whole product. A regex here would be the second
   *  derivation of "is this a URL", which is the `ssh`-without-a-space
   *  defect in another costume (api-paths.ts says so itself). */
  const installSource = () => classifyPastedSource(installUrl())
  const installIsURL = () => installSource().kind === 'url'

  /** Why the typed text is not an address, or ''. Ours to say and free to
   *  say: a call that could only be refused is a round trip spent to learn
   *  what the form already knew. Silent while the box is empty — an ask
   *  does not open complaining about a field nobody has filled in. */
  const installUrlRefusal = (): string =>
    installUrl().trim() !== '' && !installIsURL()
      ? 'A skill is fetched from an http:// or https:// address'
      : ''

  const openInstall = (): void => {
    setInstallUrl('')
    setInstallPreview(null)
    setInstallRefusal('')
    setInstallOpen(true)
  }

  const closeInstall = (): void => {
    setInstallOpen(false)
    setInstallUrl('')
    setInstallPreview(null)
    setInstallRefusal('')
  }

  /** Editing the address DROPS the document that was read, because an
   *  address that is no longer the one that was fetched cannot describe the
   *  bytes on screen. That is what keeps "exactly one source is held" a fact
   *  rather than a rule in a comment. */
  const changeInstallUrl = (value: string): void => {
    setInstallUrl(value)
    setInstallRefusal('')
    const held = installPreview()
    if (held && held.url !== value.trim()) setInstallPreview(null)
  }

  const forgetInstallSource = (): void => {
    setInstallPreview(null)
    setInstallUrl('')
    setInstallRefusal('')
  }

  /** The message a refusal left, verbatim. Each of `skills.preview`'s and
   *  `skills.install`'s refusals already names the step that refused; a
   *  sentence of our own here would put our guess in front of the person
   *  instead of what happened. */
  const refusalSentence = (err: unknown): string =>
    err instanceof Error ? err.message : String(err)

  async function readSkill(): Promise<void> {
    const source = installSource()
    if (source.kind !== 'url' || installBusy()) return
    setInstallRefusal('')
    setInstallBusy(true)
    try {
      setInstallPreview(await props.store.preview(source.url))
    } catch (err) {
      setInstallPreview(null)
      setInstallRefusal(refusalSentence(err))
    } finally {
      setInstallBusy(false)
    }
  }

  /** The address that was READ is what gets installed — never the current
   *  contents of the box. The backend fetches it a second time and compares
   *  against the digest its own preview kept, so the two calls have to name
   *  one document; taking the address from the field would let an edit
   *  between reading and approving change what was approved. */
  async function installSkill(): Promise<void> {
    const held = installPreview()
    if (!held || installBusy()) return
    setInstallRefusal('')
    setInstallBusy(true)
    try {
      const installed = await props.store.install(held.url)
      closeInstall()
      showToast({ level: 'success', message: `Installed “${installed.name}”` })
    } catch (err) {
      setInstallRefusal(refusalSentence(err))
    } finally {
      setInstallBusy(false)
    }
  }

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
    <Section
      title="Discovered skills"
      /* ABOVE THE LIST, in the kit's own heading slot (spec §9). It belongs
         to the GROUP rather than to any row in it, which is what that slot
         is for, and it is on screen from the state the page opens in —
         including while the list is still loading and while the document
         cannot be read at all, because neither is a reason a person cannot
         install a skill. */
      actions={
        <Button variant="primary" onClick={openInstall}>
          Install from a URL
        </Button>
      }
    >
      {/* THE ASK ITSELF. Mounted with the list rather than conditionally
          rendered, the way the API pane mounts the Postman ask: the kit's
          Dialog owns opening and closing, and a surface that unmounted the
          component instead would be deciding that a second time. */}
      <SkillsInstallDialog
        open={installOpen()}
        url={installUrl()}
        onUrl={changeInstallUrl}
        sourceIsURL={installIsURL()}
        urlRefusal={installUrlRefusal()}
        refusal={installRefusal()}
        preview={installPreview()}
        onForget={forgetInstallSource}
        busy={installBusy()}
        onCancel={closeInstall}
        onRead={() => void readSkill()}
        onInstall={() => void installSkill()}
      />
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
                  detail={evidence(skill)}
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
