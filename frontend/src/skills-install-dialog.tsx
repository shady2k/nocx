/**
 * The ask that installs a skill somebody else wrote, from its URL
 * (nocx-qja4m.6, install spec §9).
 *
 * The precedent is `PostmanImportDialog` (api/import-dialogs.tsx), the other
 * place in the product where a person pastes an address and the backend goes
 * and gets it, and three of its decisions are kept here verbatim:
 *
 *   - THE ASK DOES NOT CLASSIFY ITS OWN INPUT. What a pasted string is has
 *     one owner, `classifyPastedSource`, which asks the kit's
 *     `isAbsoluteHttpUrl` for the address half so that the endpoint form and
 *     this ask cannot come to different answers about the same text; a
 *     second derivation here would be the `ssh`-without-a-trailing-space
 *     defect in another costume. The surface asks once and hands the answer
 *     down as `sourceIsURL`, which is what the read button is gated on.
 *   - ONE SOURCE IS HELD, VISIBLY, AND CAN BE TAKEN BACK. Here the held
 *     source is the DOCUMENT that was read: its name, its description, its
 *     address and its bytes. A person who read the wrong skill must be able
 *     to see which one the ask is holding and drop it.
 *   - A REFUSAL STAYS IN THE ASK, in the backend's own sentence, in the kit's
 *     validation slot. Each refusal from `skills.preview` and `skills.install`
 *     already names the step that refused; re-wording it here would put our
 *     guess in front of the person instead of what happened.
 *
 * THE CONFIRMATION IS NOT `showConfirm`, and that is the whole shape of this
 * file. A skill body does not fit in a confirm's sentence, and the person is
 * not being asked to allow an operation — they are being asked to ADOPT
 * INSTRUCTIONS that the assistant will read and act on. The closest existing
 * shape is backup restore's preview-then-confirm, where what will happen is
 * spelled out before the button; here the preview IS the body, in a
 * `CodeBlock`, with the scan's findings above it.
 *
 * EVERY FINDING, NOT THE FIRST. `skills.preview` returns all of them
 * deliberately: the 8 KiB bound that makes the assistant's write path attach
 * one finding belongs to a tool result, not to a dialog, and for a body a
 * stranger wrote the person's reading of this evidence is the whole of the
 * defence rather than a backstop for it (skills spec §6 layer 2 does not
 * apply at all when the drafting step was somebody else's). The words for a
 * pattern come from `scan-pattern-words.ts`, which the approval prompt reads
 * too — one vocabulary for one scan.
 *
 * A SKILL IS NOT ONE FILE, so the ask names every file that will land
 * (`files`, spec §5 and §8). A body that says "read references/typescript.md"
 * brings that file with it, `scripts/` included, and approving without being
 * told so would be approving a name rather than an act. Paths only here: the
 * viewer that opens each of them is one capability in three places and belongs
 * to epic nocx-872jc, not to a fourth reader built inside this dialog.
 *
 * READ AND INSTALL TRAVEL TOGETHER, on one connection. The backend keeps the
 * digest of the document its own preview showed and `skills.install` compares
 * against that record, so a backend that restarted between the two refuses
 * with "read the document first". That is recoverable in one click — the
 * address is still in the field and Read is still on the footer — and the ask
 * is laid out so that reading and installing are one sitting rather than two.
 */
import { For, Show } from 'solid-js'
import {
  Button,
  CodeBlock,
  FactList,
  MarkerList,
  StatusCard,
  Stack,
  TextField,
  type Fact,
  type MarkerListItem,
} from './ui'
import { Dialog } from './ui/dialog'
import { scanPatternWords } from './scan-pattern-words'
import type { SkillsPreview } from './generated/skills.preview'

export interface SkillsInstallDialogProps {
  open: boolean
  /**
   * What is in the address box. The box's CONTENTS, never what they mean:
   * what the text is gets decided once, by `classifyPastedSource`, in the
   * surface that also owns the call (skills-section.tsx).
   */
  url: string
  onUrl: (value: string) => void
  /** Whether that text is an address at all — the single owner's answer,
   *  handed down. A boolean rather than the classified source, because the
   *  union has one owner and a second spelling of its members here would be
   *  a second answer to the same question. */
  sourceIsURL: boolean
  /**
   * Why the typed text is not an address, or ''. Said HERE rather than by
   * the backend: a round trip spent to learn what the form already knew is a
   * round trip spent for nothing, and the answer is the same one the button
   * is gated on.
   */
  urlRefusal: string
  /** The backend's own refusal, from either method, or ''. Rendered verbatim
   *  — each one already names the step that refused it. */
  refusal: string
  /** The document that was read, or null while the ask holds none. */
  preview: SkillsPreview | null
  onForget: () => void
  /** Whether a call is in flight — a read or an install. */
  busy: boolean
  onCancel: () => void
  onRead: () => void
  onInstall: () => void
}

export function SkillsInstallDialog(props: SkillsInstallDialogProps) {
  /** Reading is possible as soon as the text is an address. Installing needs
   *  a document that has actually been read: there is deliberately no path
   *  from a typed address straight to a write. */
  const canRead = (): boolean => props.sourceIsURL && !props.busy
  const held = (): SkillsPreview | null => props.preview

  /**
   * ONE VALIDATION SLOT, and the backend's sentence wins it.
   *
   * The two refusals answer different questions — "that is not an address"
   * is ours and costs nothing, "that document has frontmatter and no body"
   * is the backend's and cost a fetch — but they land in the same place,
   * because they are both the reason the ask has not moved on. The backend's
   * is preferred while both stand: it is the newer fact and the more
   * specific one.
   */
  const error = (): string | undefined => {
    if (props.refusal !== '') return props.refusal
    if (props.urlRefusal !== '') return props.urlRefusal
    return undefined
  }

  /** Name and description as named facts — the two things the frontmatter
   *  says about itself, read before the body they head. The address is not
   *  among them: it is on the held-source card, where the control that takes
   *  it back is, and a fact repeated in two places is two places for it to
   *  drift. */
  const facts = (): Fact[] => {
    const preview = held()
    if (!preview) return []
    return [
      { name: 'Name', value: preview.name },
      { name: 'Description', value: preview.description },
    ]
  }

  /** THE MANIFEST: every file that will land, in the order the backend sent
   *  them — SKILL.md first, because that is the one on screen below. A skill
   *  is no longer one file, and a person approving `references/` and
   *  `scripts/` they were never told about is approving a name rather than an
   *  act (spec §5, §8). `included` because that is exactly what the tone
   *  means: this comes with it. There is no `excluded` row — a file the
   *  backend refused is a refusal, not a bundle with a gap in it, so it
   *  arrives in the validation slot above and this list never renders one. */
  const manifest = (): MarkerListItem[] =>
    (held()?.files ?? []).map((path) => ({ text: path, tone: 'included' }))

  const read = (): void => {
    if (!canRead()) return
    props.onRead()
  }

  return (
    <Dialog
      open={props.open}
      title="Install a skill from a URL"
      /* The body is on screen here, so the panel is the wider one. `md` is
         the confirm/edit width and would set a skill's instructions in a
         column narrower than the editor that wrote them. */
      size="lg"
      onClose={props.onCancel}
      onSubmit={read}
      footer={
        <>
          <Button variant="default" onClick={props.onCancel}>
            Cancel
          </Button>
          {/* READ IS ALWAYS OFFERED, including while a document is held.
              That is the one-click recovery for the one refusal a person
              cannot avoid: install compares against a digest the SERVER
              kept, so a backend that restarted between the two answers
              "read the document first", and the address is still in the
              field. It is `default` rather than `primary` once a document is
              held, because at that point the action the ask exists for is
              the install. */}
          <Button variant={held() ? 'default' : 'primary'} disabled={!canRead()} onClick={read}>
            Read this skill
          </Button>
          <Show when={held()}>
            <Button variant="primary" disabled={props.busy} onClick={props.onInstall}>
              Install
            </Button>
          </Show>
        </>
      }
    >
      <TextField
        id="skills-install-url"
        label="The skill's URL"
        description="Fetched and read, never run. Nothing is written until you have read what it says and approved it."
        value={props.url}
        error={error()}
        onInput={props.onUrl}
        autoFocus
        required
      />
      <Show when={held()}>
        {(preview) => (
          <>
            {/* WHAT THE ASK IS HOLDING, AND HOW TO TAKE IT BACK. A
                StatusCard because that is the kit's "a state and the one
                action for it", and because the criterion for this work is
                that no element here carries appearance the kit did not give
                it — the Postman ask says the same thing through a surface
                class of its own, which is the shape this page is not
                allowed to grow. The address is the card's TITLE so it is
                verbatim: it is the one fact a person checks before adopting
                anything, and paraphrasing it would be the only place in
                this ask where the bytes are described rather than shown. */}
            <StatusCard
              tone="neutral"
              title={preview().url}
              description="The document this ask has read and is holding. Reading another address replaces it."
              action={
                <Button variant="default" disabled={props.busy} onClick={props.onForget}>
                  Forget this source
                </Button>
              }
            />
            <FactList facts={facts()} ariaLabel="What this document says it is" />
            {/* WHAT WILL LAND, above the evidence and the body. It is placed
                here rather than beside the body because it is a fact about
                the skill, like its name and its description, and not a
                reading of its contents. */}
            <MarkerList items={manifest()} />
            {/* EVERY FINDING IN THE WHOLE BUNDLE, above the body it is about.
                Each NAMES ITS FILE (nocx-872jc.4), because the manifest above
                is no longer a list of names whose contents nothing looked at:
                a bundled scripts/setup.sh is scanned like SKILL.md, and a
                finding that said only "line 4" would leave the reader with
                four files and one number. Warning and never danger: the
                scan's contract is that a finding is evidence and never a
                refusal (internal/skill/scan.go), so a tone claiming otherwise
                would remake a decision the spec already made. The matched
                line goes in a CodeBlock because it is verbatim evidence
                quoted out of the file, not prose about it — and it is quoted
                because a support file's bytes are not on this dialog at all,
                so there is nothing here to mark in place.

                THE NUMBER COUNTS THE FILE, NOT THE BLOCK BELOW. SKILL.md's
                findings are counted over the whole served document,
                frontmatter included, because that is the file the finding
                names — while the block below is the BODY, which is what a
                person adopting instructions reads. So the sentence says
                which of the two it is counting rather than leaving a reader
                to assume it indexes the block: what makes the number worth
                carrying is that it is checkable against the address they
                pasted, and the verbatim line beside it is checkable here. */}
            <For each={preview().findings}>
              {(finding) => (
                <Stack>
                  <StatusCard
                    tone="warning"
                    title={scanPatternWords(finding.patternId)}
                    description={`Line ${finding.lineNumber} of ${finding.path}, counted from the top of that file as it is served, matched the static scan. It is evidence to read beside the bytes, not a refusal — installing is still yours to do or not.`}
                  />
                  <CodeBlock
                    ariaLabel={`Line ${finding.lineNumber} of ${finding.path}, which the static scan matched`}
                  >
                    {finding.line}
                  </CodeBlock>
                </Stack>
              )}
            </For>
            {/* THE WHOLE BODY. Not an excerpt: a person adopting
                instructions reads all of them, and an excerpt is not
                something anybody can approve responsibly (spec §5 step 5).
                It is what the assistant will read, so it is shown as what it
                is — a file — rather than restated. */}
            <CodeBlock
              label="SKILL.md"
              ariaLabel={`The whole body of ${preview().name}, as it would be written`}
            >
              {preview().body}
            </CodeBlock>
          </>
        )}
      </Show>
    </Dialog>
  )
}
