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
import type {
  CollisionLabels,
} from '../ui/collision-dialog'
import type { DumpLabels } from '../ui/dump-panel'
import type { FileReadoutLabels } from '../ui/file-readout'
import type { FolderPickerLabels } from '../ui/folder-picker'

export const collisionLabels = (): CollisionLabels => ({
  title: t('kit.collision.title', 'File already exists'),
  overwrite: t('kit.collision.overwrite', 'Overwrite'),
  keepBoth: t('kit.collision.keepBoth', 'Keep both'),
  skip: t('kit.collision.skip', 'Skip'),
  existsIn: t('kit.collision.existsIn', 'already exists in'),
  applyOne: t('ui.collision.applyOne', 'Apply to the 1 remaining file'),
  applyMany: (count: number) =>
    tPlural('ui.collision.applyMany', count, 'Apply to the {count} remaining files'),
})

export const dumpLabels = (): DumpLabels => ({
  title: t('kit.dump.title', 'Model dump'),
  openPage: t('kit.dump.openPage', 'Open full page'),
  searchAria: t('kit.dump.search.aria', 'Search dump'),
  searchPh: t('kit.dump.search.ph', 'Search dump'),
  request: t('kit.dump.request', 'Request'),
  response: t('kit.dump.response', 'Response'),
  rawChunks: t('kit.dump.rawChunks', 'Raw chunks'),
  truncated: t('kit.dump.truncated', 'Truncated at the 1 MiB capture limit.'),
  noDrive: t('kit.dump.noDrive', 'No recorded drive.'),
  noMatch: t('kit.dump.noMatch', 'No dump content matches this search.'),
})

export const fileReadoutLabels = (): FileReadoutLabels => ({
  whatAria: t('kit.fileReadout.what.aria', 'What this file is'),
  notText: t('kit.fileReadout.notText', 'This file is not text'),
  tooLarge: t('kit.fileReadout.tooLarge', 'This file is larger than the read budget'),
  readFailed: t('kit.fileReadout.readFailed', 'This file could not be read'),
  scanNotice: t('kit.fileReadout.scanNotice', 'Highlighted lines below matched a static scan'),
})

export const folderPickerLabels = (): FolderPickerLabels => ({
  title: t('kit.folderPicker.title', 'Choose a folder'),
  cancel: t('kit.folderPicker.cancel', 'Cancel'),
  choose: t('kit.folderPicker.choose', 'Choose'),
  folder: t('kit.folderPicker.folder', 'Folder'),
  upOne: t('kit.folderPicker.upOne', 'Up one level'),
  contents: t('kit.folderPicker.contents', 'Folder contents'),
  empty: t('kit.folderPicker.empty', 'This folder is empty'),
})
