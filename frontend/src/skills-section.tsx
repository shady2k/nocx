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
import { CodeBlock, MarkerList } from './ui'
import { Dialog, showConfirm } from './ui/dialog'
import { showToast } from './ui/toast'
import type { BadgeTone } from './ui/badge'
import { classifyPastedSource } from './api/api-paths'
import { SkillsInstallDialog } from './skills-install-dialog'
import { scanPatternWords } from './scan-pattern-words'
import type { SkillsAudit } from './generated/skills.audit'
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

/**
 * The reading a person ASKED FOR (nocx-0bsa4.4), and the states it passes
 * through.
 *
 * A refusal is DRAWN and never thrown, for the manifest's reason — but here
 * the distinction is load-bearing rather than tidy: a reading that did not
 * happen must never look like a reading that found nothing. So `refused`
 * carries the backend's own sentence and renders INSTEAD of a report, and
 * there is deliberately no fourth state in which the block is on screen with
 * nothing in it.
 */
type AuditAsk =
  { kind: 'reading' } | { kind: 'read'; result: SkillsAudit } | { kind: 'refused'; message: string }

/**
 * What one omitted file says, in the person's words.
 *
 * The switch is total over the wire's closed union, so a fifth reason added
 * to the contract fails the compile here rather than rendering as a path with
 * no explanation beside it — which would be the silent degrade the whole
 * omission list exists to prevent.
 */
const omissionWords = (omission: SkillsAudit['omitted'][number]): string => {
  switch (omission.reason) {
    case 'too-large':
      return `${omission.path} was not read: it is larger than one file's read budget.`
    case 'not-text':
      return `${omission.path} was not read: its bytes are not text, so there was nothing to describe.`
    case 'budget-spent':
      return `${omission.path} was not read: the reading was already full when its turn came.`
    case 'unreadable':
      return `${omission.path} was not read: it could not be opened just now.`
  }
}

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
  const [auditAsk, setAuditAsk] = createSignal<AuditAsk | null>(null)

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

  /** Which OPEN CARD an audit belongs to. It is a counter of its own rather
   *  than `fileGeneration`, because the two are about different things:
   *  opening another file of the same skill abandons the bytes on screen and
   *  must NOT abandon a reading of the skill those files belong to — and
   *  re-asking would be a second model call for a click that spent nothing.
   *  It moves only when the card does. */
  let auditGeneration = 0

  /** Opening the card asks for both halves at once: the document the person
   *  came for, and the manifest of everything else the skill carries. The
   *  contract puts SKILL.md first in that manifest, so reading it before the
   *  list arrives can never show a file the person did not choose — and
   *  waiting for the list first would leave the card blank for one round
   *  trip on the ordinary skill, which carries exactly that one file. */
  function openCard(skill: Skill): void {
    const generation = ++fileGeneration
    auditGeneration++
    setCardName(skill.name)
    setManifest({ kind: 'reading' })
    // NOT the audit. Opening a card asks for bytes the person already owns,
    // which costs nothing; the reading is a model call and waits for the
    // button. That is design §8's rule and role.go's, and it is enforced by
    // there being no call here rather than by anybody remembering it.
    setAuditAsk(null)
    void readFile(skill.name, SKILL_FILE, generation)
    void readManifest(skill.name, generation)
  }

  /** The button. It spends money, so it is pressed rather than triggered, and
   *  a second press while one is in flight is ignored rather than queued. */
  async function askForAudit(name: string): Promise<void> {
    if (auditAsk()?.kind === 'reading') return
    const generation = ++auditGeneration
    setAuditAsk({ kind: 'reading' })
    try {
      const result = await props.store.audit(name)
      if (generation !== auditGeneration) return
      setAuditAsk({ kind: 'read', result })
    } catch (err) {
      if (generation !== auditGeneration) return
      setAuditAsk({ kind: 'refused', message: refusalSentence(err) })
    }
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
    auditGeneration++
    setCardName(null)
    setFileAsk(null)
    setManifest(null)
    setAuditAsk(null)
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
        // THE FINDINGS TRAVEL WITH THE BYTES, and they came back from the
        // same read: opening a file to look at it costs no model call, and
        // learning that a line in it matched must not cost one either
        // (nocx-872jc.4). A refused file's branch below carries none,
        // because nothing was read, so nothing was scanned.
        return {
          kind: 'text',
          text: ask.result.text,
          marks: ask.result.findings.map((finding) => ({
            lineNumber: finding.lineNumber,
            label: scanPatternWords(finding.patternId),
          })),
        }
      case 'not-text':
        return { kind: 'not-text' }
      case 'too-large':
        return { kind: 'too-large', maxBytes: ask.result.maxBytes }
    }
  }

  /** The reading the person asked for, or null. Read out of the ask so every
   *  sentence below draws from one answer rather than from three accessors
   *  that could disagree about whether a reading is on screen. */
  const reading = (): SkillsAudit | null => {
    const held = auditAsk()
    return held?.kind === 'read' ? held.result : null
  }

  /** Why there is no reading, or ''. It mirrors `manifestRefusal` rather
   *  than narrowing the union inline, because a `Show` that carries the
   *  discriminated value cannot narrow it for its child. */
  const auditRefusal = (): string => {
    const held = auditAsk()
    return held?.kind === 'refused' ? held.message : ''
  }

  /** WHAT WENT INTO THE READING, as a stance per file: read, or not read and
   *  why. It is the kit's MarkerList because that is the component for
   *  exactly this vocabulary — this is included, this is not, and here is the
   *  caveat — and the install ask already states its manifest with it, so a
   *  person meets one grammar for "what is in and what is out" in both
   *  places. */
  const auditManifest = (result: SkillsAudit) => [
    ...result.read.map((path) => ({ text: path, tone: 'included' as const })),
    ...result.omitted.map((omission) => ({
      text: omissionWords(omission),
      tone: 'excluded' as const,
    })),
  ]

  /** Which model was billed, said beside what it wrote. Facts rather than
   *  prose because that is what they are, and because a reader checking a
   *  reading needs to know which model made it before they weigh a word of
   *  it. */
  const auditFacts = (result: SkillsAudit): Fact[] => [
    { name: 'Read by', value: result.model },
    { name: 'Endpoint', value: result.endpoint },
  ]

  /** THE NOTE ROLE.GO INSISTS ON, or ''. An unassigned auditing role spends
   *  the answering role's endpoint, and it may never do that quietly: the
   *  person pressed a button and is entitled to know which model they were
   *  billed for. Silent when the role they assigned is the one that ran —
   *  a note under every reading is a note nobody reads. */
  const auditFallbackNote = (result: SkillsAudit): string =>
    result.role === 'answering'
      ? `No model is assigned to the auditing role, so this reading was made by ${result.model} on ${result.endpoint} — the answering role's endpoint. Assign an auditing model under Model roles if you want a different one reading your skills.`
      : ''

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
              {/* THE AUDIT (nocx-0bsa4.4). Last on the card, and that is the
                  argument for where it sits: the card exists so a person can
                  READ the skill, and this is what they ask for when reading
                  it did not settle the question. Putting it above the file
                  would offer a model's paragraph before the bytes it is
                  about, which is the opposite of what design §7 is for.

                  It is a BUTTON. Opening the card asks for nothing here, and
                  that is enforced by there being no call in `openCard`
                  rather than by anybody remembering the rule: an audit is a
                  model call, and `internal/profile/role.go` refuses to spend
                  that silently.

                  It CHANGES NOTHING. There is no control in this block, no
                  branch anywhere reads what comes back, and the switch above
                  is exactly where it was — which is asserted rather than
                  assumed, both here and off the socket. */}
              <Button
                onClick={() => void askForAudit(skill().name)}
                disabled={auditAsk()?.kind === 'reading'}
              >
                Audit this skill
              </Button>
              <Show when={auditAsk()?.kind === 'reading'}>
                <StatusCard
                  tone="neutral"
                  title="Reading this skill"
                  description={`A model is reading the files of \u201c${skill().name}\u201d and writing a description of them.`}
                />
              </Show>
              {/* The backend's own sentence, and NO report beside it. A
                  reading that did not happen must never look like a reading
                  that found nothing — the same rule the wire keeps by
                  refusing rather than answering with an empty one. */}
              <Show when={auditRefusal()}>
                <StatusCard
                  tone="danger"
                  title="This skill was not read"
                  description={auditRefusal()}
                />
              </Show>
              <Show when={reading()}>
                {(result) => (
                  <>
                    {/* WHAT THIS IS, before what it says. The heading is the
                        claim being made and it is deliberately small: a
                        model read some files and wrote paragraphs about
                        them. Nothing here is certified, and the sentence
                        says so in the words a person can act on rather than
                        in a tone or a colour they have to interpret. */}
                    <StatusCard
                      tone="neutral"
                      title="A description, not a verdict"
                      description="A model read the files below and wrote the paragraphs under this. It decides nothing: what the assistant is offered is still your switch and the bytes on disk, and neither moved when you pressed the button. A skill's own text can address whoever reads it, so read this beside the files rather than instead of them."
                    />
                    <FactList
                      facts={auditFacts(result())}
                      ariaLabel="Which model read this skill"
                    />
                    <Show when={auditFallbackNote(result())}>
                      <StatusCard
                        tone="warning"
                        title="This was read by the answering model"
                        description={auditFallbackNote(result())}
                      />
                    </Show>
                    <StatusCard
                      tone="neutral"
                      prose
                      title={`What a model read in \u201c${result().name}\u201d`}
                      description={result().report}
                    />
                    {/* WHICH FILES IT WAS ABOUT, and which it was not. A
                        reading of a subset that did not say so would read
                        exactly like a reading of the whole skill. */}
                    <MarkerList items={auditManifest(result())} />
                    {/* THE SCAN'S OWN MATCHES, ours and not the model's, so
                        the line and its number are checkable against the
                        file above rather than reported by the thing being
                        checked. Warning and never danger: a finding is
                        evidence and never a refusal
                        (internal/skill/scan.go). */}
                    <For each={result().findings}>
                      {(finding) => (
                        <Stack>
                          <StatusCard
                            tone="warning"
                            title={scanPatternWords(finding.patternId)}
                            description={`Line ${finding.lineNumber} of ${finding.path} matched the static scan. It is evidence to read beside the bytes, not a refusal — what to do about it is yours.`}
                          />
                          <CodeBlock
                            ariaLabel={`Line ${finding.lineNumber} of ${finding.path}, which the static scan matched`}
                          >
                            {finding.line}
                          </CodeBlock>
                        </Stack>
                      )}
                    </For>
                    <Show when={result().findings.length === 0}>
                      {/* ABSENCE OF A MATCH IS NOT SAFETY, and this is the
                          sentence that keeps the surface from implying it
                          is. The scan is eleven patterns looking for known
                          phrasings; a file none of them hit is a file they
                          had nothing to say about. Saying "no issues found"
                          here would put our signature on bytes nobody
                          certified, which is what design §4 removed. */}
                      <StatusCard
                        tone="neutral"
                        title="The static scan matched nothing in these files"
                        description="That is not the same as safe. The scan looks for a fixed set of known phrasings, so files it matched nothing in are files it had nothing to say about — not files anything has vouched for."
                      />
                    </Show>
                  </>
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
