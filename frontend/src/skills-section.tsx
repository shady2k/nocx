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
 *   status  shown ONLY when the bytes changed since the skill was installed.
 *           INSTALLATION, not approval (nocx-hzsxl): the digest was taken
 *           when the bytes landed, and approval only ever admitted them — it
 *           never certified them, so a row saying "since approval" claimed
 *           more for the person's click than the click was. Enabled state
 *           is not a status here — the switch beside it already says so, and
 *           a row that reports one fact twice is how the two come to disagree.
 */
import { For, Show, createSignal, onCleanup, onMount } from 'solid-js'
import {
  ActionGroup,
  Button,
  Checkbox,
  EmptyState,
  FactList,
  FileReadout,
  RecordRow,
  Section,
  Stack,
  StatusCard,
  type Fact,
  type FileReadoutOutcome,
} from './ui'
import { Dialog, showConfirm } from './ui/dialog'
import { showToast } from './ui/toast'
import type { BadgeTone } from './ui/badge'
import { classifyPastedSource } from './api/api-paths'
import { SkillsInstallDialog } from './skills-install-dialog'
import type { SkillsFile } from './generated/skills.file'
import type { SkillsFiles } from './generated/skills.files'
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

/**
 * The one file every skill has, and the file the card opens on.
 *
 * A skill may now carry `references/` and `scripts/` too (nocx-0bsa4.1), and
 * `skills.files` is what names them; this constant stays because the document
 * is what the person opened the card FOR, so it is read straight away rather
 * than waited for behind the manifest. The contract guarantees it is also the
 * manifest's first entry, so the two can be asked for at once without the
 * card ever showing a file nobody chose.
 *
 * It is a path RELATIVE to the skill's own directory, which is why it is not
 * `skill.path` — the row's path is absolute for a skill on disk and is not a
 * path at all for a builtin, whose bytes live inside the binary. Where the
 * file is, and whether that place is inside the skill, is settled once by the
 * backend through the same containment the assistant's read goes through
 * (see `SkillsClient.file`); joining or cleaning anything here would be a
 * second answer to that question.
 */
const SKILL_FILE = 'SKILL.md'

/**
 * What the reader is holding: one PATH, and how far its bytes have got.
 *
 * The skill used to travel in every member so the facts and the bytes on
 * screen could not come from two different rows. It travels differently now
 * and for a stronger reason: the card reads the skill out of the LIST, live,
 * so the switch on it moves when a toggle lands and the sentences beside it
 * follow the same refresh every other reader of that list follows. A copy
 * captured when the card opened would be a second, staler answer to what the
 * skill's state is — and the switch is exactly the control that must not
 * show one. Which skill an answer belongs to is settled by `fileGeneration`,
 * which every open, every close and every file change bumps.
 */
type FileAsk =
  | { kind: 'reading'; path: string }
  | { kind: 'read'; path: string; result: SkillsFile }
  | { kind: 'unreadable'; path: string; message: string }

/**
 * What the card knows about the skill's manifest — the list of files it is
 * made of, which is the whole point of design §8 and which nothing on the
 * wire could answer before `skills.files`.
 *
 * A refusal is DRAWN and never thrown: the card is still a card, the switch
 * on it is still the person's, and a manifest that could not be read is one
 * true sentence among the others rather than a reason to show nothing.
 */
type Manifest =
  | { kind: 'reading' }
  | { kind: 'read'; result: SkillsFiles }
  | { kind: 'unreadable'; message: string }

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
      // The toast names the state the skill actually lands in, because the
      // row alone does not: an installed skill arrives OFF (nocx-0bsa4.2),
      // and a person told only "Installed" would go and ask the assistant to
      // use it, get nothing, and conclude the install failed.
      showToast({
        level: 'success',
        message: `Installed “${installed.name}” — it is off until you turn it on`,
      })
    } catch (err) {
      setInstallRefusal(refusalSentence(err))
    } finally {
      setInstallBusy(false)
    }
  }

  /* ── The skill's card (nocx-872jc.2, nocx-0bsa4.3) ─────────────────────
     Reading writes nothing, so it refreshes nothing and touches neither
     `busy` (which disables the controls that CHANGE a row) nor the list.
     The ask's state lives here for the install ask's reason: the calls are
     the surface's, the card draws what it is handed.

     WHICH SKILL is a NAME, and the skill itself is read back out of the
     list. That is what keeps the switch on the card and the switch on the
     row one control over one fact: a toggle from either refreshes the list,
     and both read the result. A card holding its own copy of the skill would
     go on showing the state the list had when it opened. */
  const [cardName, setCardName] = createSignal<string | null>(null)
  const [fileAsk, setFileAsk] = createSignal<FileAsk | null>(null)
  const [manifest, setManifest] = createSignal<Manifest | null>(null)

  /** The skill the card is about, live from the list. Null once it is gone —
   *  a card left open over a Delete closes rather than describing a skill
   *  that is not there. */
  const cardSkill = (): Skill | null =>
    readySkills().find((skill) => skill.name === cardName()) ?? null

  /** Which read the answer on screen belongs to. A person who opens one
   *  skill, closes it and opens another has two reads in flight against one
   *  panel, and without this the slower one wins by arriving last — the same
   *  race `SkillsStore.refresh` counts for the list. It counts the manifest
   *  too, because both asks belong to the same open card and a manifest
   *  arriving for the skill before last is the same defect in the list. */
  let fileGeneration = 0

  /** Opening the card asks for both halves at once: the document the person
   *  came for, and the manifest of everything else the skill carries. The
   *  contract puts SKILL.md first in that manifest, so reading it before the
   *  list arrives can never show a file the person did not choose — and
   *  waiting for the list first would leave the card blank for one round
   *  trip on the ordinary skill, which carries exactly that one file. */
  function openCard(skill: Skill): void {
    const generation = ++fileGeneration
    setCardName(skill.name)
    setManifest({ kind: 'reading' })
    void readFile(skill.name, SKILL_FILE, generation)
    void readManifest(skill.name, generation)
  }

  async function readManifest(name: string, generation: number): Promise<void> {
    try {
      const result = await props.store.files(name)
      if (generation !== fileGeneration) return
      setManifest({ kind: 'read', result })
    } catch (err) {
      if (generation !== fileGeneration) return
      setManifest({ kind: 'unreadable', message: refusalSentence(err) })
    }
  }

  /** Opening another file of the SAME skill bumps the generation too, so the
   *  bytes of the file that was open cannot land after the ones the person
   *  just asked for. The manifest is re-read with it rather than kept,
   *  because the generation it was fetched under is the one being abandoned;
   *  it is one local call and the alternative is a second counter that has to
   *  stay in step with this one. */
  function openFile(name: string, path: string): void {
    const generation = ++fileGeneration
    void readFile(name, path, generation)
    void readManifest(name, generation)
  }

  async function readFile(name: string, path: string, generation: number): Promise<void> {
    setFileAsk({ kind: 'reading', path })
    try {
      const result = await props.store.file(name, path)
      if (generation !== fileGeneration) return
      setFileAsk({ kind: 'read', path, result })
    } catch (err) {
      if (generation !== fileGeneration) return
      setFileAsk({ kind: 'unreadable', path, message: refusalSentence(err) })
    }
  }

  /** Closing ABANDONS the reads in flight as well as the ones on screen: a
   *  panel that reopened itself because bytes arrived after it was dismissed
   *  would be the surface deciding to show something nobody asked for. */
  const closeCard = (): void => {
    fileGeneration++
    setCardName(null)
    setFileAsk(null)
    setManifest(null)
  }

  const cardTitle = (): string => `\u201c${cardName() ?? ''}\u201d`

  /** Where the skill lives, and — when a stranger's document put it there —
   *  where its bytes came from.
   *
   *  Two facts and not four. The provenance and the file are drawn by the
   *  readout below, over the bytes they are true of; repeating them here
   *  would be the card reporting one fact twice, which is how the two come to
   *  disagree. The address is here because it is the one thing the person
   *  deciding about a skill cannot read off anything else on this card, and
   *  the modal covers the row that carries it. */
  const cardFacts = (skill: Skill): Fact[] => {
    const facts: Fact[] = [{ name: 'Where it is', value: skill.path }]
    if (skill.source) facts.push({ name: 'Installed from', value: skill.source.url })
    return facts
  }

  /** The paths the card lists. Empty until the manifest lands, and empty for
   *  a manifest that could not be read — the sentence for that is drawn
   *  separately, because a card with no list and a card with a refused list
   *  are two different things and must not look alike. */
  const manifestPaths = (): readonly string[] => {
    const held = manifest()
    return held?.kind === 'read' ? held.result.files : []
  }

  /** THE CUT, said in the person's words. A card that quietly showed the
   *  first N files of a longer directory would be asserting a manifest it had
   *  not read — the soft degrade AGENTS.md refuses. The cap travels on the
   *  wire so this sentence names the number the backend actually applied. */
  const manifestCut = (): string => {
    const held = manifest()
    if (held?.kind !== 'read' || !held.result.truncated) return ''
    return `The list stops at the first ${held.result.maxFiles} files, and this skill carries more files than that. They are still on disk, still in a backup, and still readable — this card just does not name them.`
  }

  const manifestRefusal = (): string => {
    const held = manifest()
    return held?.kind === 'unreadable' ? held.message : ''
  }

  /** Which file is on screen, said in words beside it.
   *
   *  The RESOLVED values win where there are any: `skills.file` answers with
   *  the skill as root precedence resolved it and the path as it resolved it,
   *  which is what the bytes actually came from — the request is what was
   *  asked for. They differ exactly when two roots hold the same name, and
   *  that is the moment a reader most needs to know which one they got. A
   *  read that never arrived has no resolved anything, so it falls back to
   *  what was asked, which is still true about the request. */
  const fileFacts = (skill: Skill, ask: FileAsk): Fact[] => {
    const resolved = ask.kind === 'read' ? ask.result : null
    return [
      { name: 'Skill', value: resolved?.name ?? skill.name },
      { name: 'File', value: resolved?.path ?? ask.path },
      { name: 'Provenance', value: resolved?.provenance ?? skill.provenance },
    ]
  }

  /** The wire's outcome as the reader's. The `switch` is total over
   *  `refusal`'s closed union, so a third refusal added to the contract
   *  fails the compile here rather than falling through to a blank panel —
   *  which is the failure this whole bead exists to prevent. */
  const fileOutcome = (ask: FileAsk): FileReadoutOutcome | null => {
    if (ask.kind === 'reading') return null
    if (ask.kind === 'unreadable') return { kind: 'unreadable', message: ask.message }
    switch (ask.result.refusal) {
      case '':
        return { kind: 'text', text: ask.result.text }
      case 'not-text':
        return { kind: 'not-text' }
      case 'too-large':
        return { kind: 'too-large', maxBytes: ask.result.maxBytes }
    }
  }

  /** Why a skill that is switched off is off, in one sentence that is true of
   *  every skill it is drawn for. An installed skill ARRIVED off and the rest
   *  were switched off by the person, and the wire cannot tell a skill that
   *  arrived off from one the person turned off later — so the second
   *  sentence is conditioned on the only thing that is always true of the
   *  installed root, which is how a skill from it starts. */
  const offSentence = (skill: Skill): string =>
    skill.provenance === 'installed'
      ? 'The assistant is not offered it. A skill installed from outside this machine arrives off, so this look happens before it can act; turn it on above when you have taken it.'
      : 'The assistant is not offered it. Turn it on above when you want it back in play.'

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
      {/* THE CARD (nocx-0bsa4.3). Mounted beside the install ask and for
          the same reason: the kit's Dialog owns opening and closing, and a
          surface that unmounted the component instead would be deciding that
          a second time. It stays a Dialog rather than a workspace tab because
          reading a skill must not cost the page a person is on — and because
          `src/file-viewer/` reads through a live binding on some machine,
          which a builtin skill, whose bytes are inside the binary, does not
          have.

          It is where design §8 is paid for: an installed skill lands inert
          precisely so the person can come here, see what it is made of with
          any file readable, and turn it on themselves. So the switch is on
          this card, beside the bytes it is a decision about, and NOT on the
          install ask's success — two clicks in one flow is a reflex inside a
          week, and a reflex is not a look.

          THE SWITCH IS ALSO STILL ON THE ROW, and that is one control and
          not two: both call `toggle`, which is the store's one write, and
          both read `cardSkill()`/the list, which is the store's one answer.
          The row's is how a person turns a library on and off by scanning;
          this one is the same decision taken where the evidence is. What may
          never be duplicated is the STATE, and there is exactly one of it. */}
      <Dialog
        open={cardSkill() !== null}
        title={cardTitle()}
        /* `lg` for the install ask's reason: a file is on screen, and `md`
           would set a skill's instructions in a column narrower than the
           editor that wrote them. */
        size="lg"
        onClose={closeCard}
        footer={
          <Button variant="default" onClick={closeCard}>
            Close
          </Button>
        }
      >
        <Show when={cardSkill()}>
          {(skill) => (
            <>
              <FactList facts={cardFacts(skill())} ariaLabel="Where this skill lives" />
              {/* The decision, in the kit's own switch — the same component
                  the row uses, because it is the same setting. Its label says
                  what "on" MEANS rather than repeating the word enabled: what
                  a person is deciding is whether the assistant is offered
                  this skill at all. */}
              <Checkbox
                variant="switch"
                label="Offer this skill to the assistant"
                checked={skill().enabled}
                disabled={busy() === skill().name}
                onChange={(enabled) => void toggle(skill(), enabled)}
              />
              {/* THE TWO KINDS OF OFF, and they are drawn as two different
                  sentences because they send the person to two different
                  controls. Off-and-approved is the switch's business and
                  nothing has happened to the bytes; changed is the bytes'
                  business and the switch cannot fix it — which is why the
                  second one says the skill is out of play WHATEVER the switch
                  says, and carries the one action that ends the state. */}
              <Show when={!skill().enabled}>
                <StatusCard
                  tone="warning"
                  title="This skill is off"
                  description={offSentence(skill())}
                />
              </Show>
              <Show when={skill().status === 'changed'}>
                <StatusCard
                  tone="danger"
                  title="The bytes under this skill have changed"
                  description="They are no longer the bytes recorded for it, so the assistant is not offered it whatever the switch says. Read what is here now, and re-approve it if you want it back."
                  action={
                    <Button
                      size="sm"
                      disabled={busy() === skill().name}
                      onClick={() => void approve(skill())}
                    >
                      Re-approve
                    </Button>
                  }
                />
              </Show>
              {/* WHAT IT CARRIES. Drawn only when there is something to pick
                  between: a skill of one file gets the file, not a list
                  control with one row in it. The rows are the kit's record
                  rows and the record's own NAME is the control (RecordRow's
                  `onActivate`), so opening a file needs no invented button
                  and no second label. */}
              <Show when={manifestPaths().length > 1}>
                <Stack divided dense>
                  <For each={manifestPaths()}>
                    {(path) => (
                      <RecordRow
                        title={path}
                        density="dense"
                        selected={fileAsk()?.path === path}
                        actions={undefined}
                        onActivate={() => openFile(skill().name, path)}
                      />
                    )}
                  </For>
                </Stack>
              </Show>
              <Show when={manifestCut()}>
                <StatusCard
                  tone="warning"
                  title="This list is not the whole skill"
                  description={manifestCut()}
                />
              </Show>
              <Show when={manifestRefusal()}>
                <StatusCard
                  tone="danger"
                  title="What this skill carries could not be listed"
                  description={manifestRefusal()}
                />
              </Show>
              <Show when={fileAsk()}>
                {(ask) => (
                  <Show
                    when={fileOutcome(ask())}
                    /* The bytes are on their way, which is a fourth true
                       sentence about the same file — said in the page's own
                       words for a wait, the ones the list above already
                       uses. */
                    fallback={
                      <StatusCard
                        tone="neutral"
                        title="Reading this skill"
                        description={`Reading ${ask().path} of \u201c${skill().name}\u201d.`}
                      />
                    }
                  >
                    {(outcome) => (
                      <FileReadout
                        facts={fileFacts(skill(), ask())}
                        ariaLabel={`${ask().path} of \u201c${skill().name}\u201d, verbatim`}
                        outcome={outcome()}
                      />
                    )}
                  </Show>
                )}
              </Show>
            </>
          )}
        </Show>
      </Dialog>
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
                          person is entitled to read the file while a toggle
                          is in flight.

                          It says OPEN and no longer Read: what it opens is
                          the skill's card, and reading a file is one of the
                          things on it rather than all of it. */}
                      <Button size="sm" onClick={() => openCard(skill)}>
                        Open
                      </Button>
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
