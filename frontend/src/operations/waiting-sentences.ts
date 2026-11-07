// The catalogue's waiting sentence, marked for translation.
//
// ADR-8eej3e66 §2 gives every operation a `waiting` sentence "in the person's
// terms", and the catalogue is the one place it is written. The Operations row
// shows it verbatim, so it is a user-visible string and goes through `t()`:
// the key is the operation's own id under `operations.waiting.`, and the
// generated sentence is the fallback, so a surface rendering an untranslated
// locale still reads the catalogue's words rather than a key.
//
// The English catalogue carries one entry per method, and
// waiting-sentences.test.ts compares every one with the generated catalogue —
// a sentence reworded in Go cannot leave a stale translation behind.
import { t } from '../i18n/locale-model'

/** The sentence shown while `method` waits, translated when a catalogue entry
 *  exists. A method outside the catalogue has none; the caller decides. */
export function waitingSentence(method: string, fallback: string): string {
  return t(`operations.waiting.${method}`, fallback)
}
