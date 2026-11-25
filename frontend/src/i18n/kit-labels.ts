/**
 * kit-labels — the four ui kit surfaces' copy, resolved through the active
 * catalogue and handed to the kit as props.
 *
 * The kit never imports i18n (ui/README.md): its components are placed and
 * never repainted, so a catalogue read inside them would freeze at the
 * locale of the first mount. The consumers call these builders at render
 * time and pass the result down; the EN literals here are the same visible
 * fallbacks the kit used to hold inline.
 */
import { t, tPlural } from './locale-model'
import type { CollisionLabels } from '../ui/collision-dialog'
import type { CodeBlockCopy } from '../ui/code-block'
import type { SecretChipTitles } from '../ui/secret-chip'
import type { BlockReceiptLabels } from '../ui/block-receipt'
import type { PersonMessageLabels } from '../ui/person-message'
import type { FileReadoutLabels } from '../ui/file-readout'
import type { FolderPickerLabels } from '../ui/folder-picker'
import type { ConfirmLabels } from '../ui/dialog'

export const collisionLabels = (): CollisionLabels => ({
  title: t('kit.collision.title', 'File already exists'),
  overwrite: t('kit.collision.overwrite', 'Overwrite'),
  keepBoth: t('kit.collision.keepBoth', 'Keep both'),
  skip: t('kit.collision.skip', 'Skip'),
  existsIn: t('kit.collision.existsIn', 'already exists in'),
  applyOne: t('ui.collision.applyOne', 'Apply to the 1 remaining file'),
  applyMany: (count: number) =>
    tPlural('ui.collision.applyMany', count, 'Apply to the {count} remaining files'),
  closeAria: t('ui.dialog.closeAria', 'Close dialog'),
  closeTitle: t('ui.dialog.closeTitle', 'Close'),
})

/** The words of a confirm: its two buttons (the caller's own) and the close
 *  control's, which every confirm shares. Built when the dialog opens. */
export const confirmLabels = (ok: string, cancel: string): ConfirmLabels => ({
  ok,
  cancel,
  closeAria: t('ui.dialog.closeAria', 'Close dialog'),
  closeTitle: t('ui.dialog.closeTitle', 'Close'),
})

export const fileReadoutLabels = (): FileReadoutLabels => ({
  whatAria: t('kit.fileReadout.what.aria', 'What this file is'),
  notText: t('kit.fileReadout.notText', 'This file is not text'),
  tooLarge: t('kit.fileReadout.tooLarge', 'This file is larger than the read budget'),
  readFailed: t('kit.fileReadout.readFailed', 'This file could not be read'),
  scanNotice: t('kit.fileReadout.scanNotice', 'Highlighted lines below matched a static scan'),
  notTextDesc: t(
    'kit.fileReadout.notTextDesc',
    'Its bytes are not something that can be shown as lines, so there is nothing here to read. The file is on disk exactly as it was — this is a fact about the file, not a refusal of the request.',
  ),
  tooLargeDesc: (size) =>
    t(
      'kit.fileReadout.tooLargeDesc',
      'The budget is {size}, and the read stops there. The file is on disk exactly as it was; what is bounded is the reading, not the file.',
      { size },
    ),
  scanDesc: (words) =>
    t(
      'kit.fileReadout.scanDesc',
      '{words}. The match is highlighted where it sits, in the bytes themselves \u2014 it is evidence to read there, not a refusal, and nothing changed because a pattern matched. Lines nothing matched are not lines anything has vouched for.',
      { words },
    ),
})

/** The WORDS one copy control says — CodeBlock's header button
 *  (ui/code-block.tsx). The caller spreads them beside its own clipboard
 *  write, so both copy paths (the Solid component and the imperative streaming
 *  mount) word it the same and neither has to know the catalogue. */
export const codeBlockCopyWords = (): Omit<CodeBlockCopy, 'write'> => ({
  label: t('kit.codeBlock.copy', 'Copy code'),
  copiedLabel: t('kit.codeBlock.copied', 'Copied'),
  copiedToast: t('kit.codeBlock.copiedToast', 'Code copied'),
  failedToast: t('kit.codeBlock.failedToast', 'Could not copy code'),
})

/** What a secret chip says on hover, per variant (ui/secret-chip.ts). */
export const secretChipTitles = (): SecretChipTitles => ({
  resolved: (name) => t('kit.secretChip.resolved', 'secret from the vault: {name}', { name }),
  unresolved: () =>
    t('kit.secretChip.unresolved', 'a masked secret — pick a live value to run this command'),
  damaged: (name, damage) =>
    t('kit.secretChip.damaged', '{name}: these bytes are no longer the secret — {damage}', {
      name,
      damage,
    }),
})

/** The after-submit capture receipt's words (ui/block-receipt.ts). */
export const blockReceiptLabels = (): BlockReceiptLabels => ({
  aria: t('kit.blockReceipt.aria', 'save detected secret'),
  nameFor: (kindLabel) =>
    t('kit.blockReceipt.nameFor', 'vault name for {kind}', { kind: kindLabel }),
  dismiss: t('kit.blockReceipt.dismiss', 'Dismiss'),
  dismissAria: (kindLabel) =>
    t('kit.blockReceipt.dismissAria', 'do not save this {kind}', { kind: kindLabel }),
  save: (count) => tPlural('kit.blockReceipt.save', count, 'Save {count}'),
})

/** The words a person's in-turn message draws (ui/person-message.ts). */
export const personMessageLabels = (): PersonMessageLabels => ({
  label: t('kit.personMessage.label', 'You'),
  aria: t('kit.personMessage.aria', 'your message while the answer was written'),
  queued: t('kit.personMessage.queued', 'asked when this answer ends'),
})

export const folderPickerLabels = (): FolderPickerLabels => ({
  title: t('kit.folderPicker.title', 'Choose a folder'),
  cancel: t('kit.folderPicker.cancel', 'Cancel'),
  choose: t('kit.folderPicker.choose', 'Choose'),
  folder: t('kit.folderPicker.folder', 'Folder'),
  upOne: t('kit.folderPicker.upOne', 'Up one level'),
  contents: t('kit.folderPicker.contents', 'Folder contents'),
  empty: t('kit.folderPicker.empty', 'This folder is empty'),
  go: t('kit.folderPicker.goLabel', 'Go'),
  closeAria: t('ui.dialog.closeAria', 'Close dialog'),
  closeTitle: t('ui.dialog.closeTitle', 'Close'),
  pathPlaceholder: t('kit.folderPicker.pathPlaceholder', '/path/to/folder'),
  cannotOpenPath: (path) => t('kit.folderPicker.cannotOpenPath', 'Cannot open {path}', { path }),
})
