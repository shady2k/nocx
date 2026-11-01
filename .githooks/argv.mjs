/**
 * One reader for every option that takes a value, because each script here
 * parses its own arguments and each of them once took the next word as a value
 * without looking at it. An unset shell variable arrives as an empty string,
 * the check the option asked for quietly disappears, and the run ends green.
 *
 * A value is a word with something in it that is not itself an option: a word
 * that is missing, empty, blank or shaped like an option is misuse, the same way
 * in every script, and the refusal names the option so the invocation can be
 * repaired. A bare `-` is a value: it is how a script names stdin, and it is the
 * one dash-word no option can be mistaken for.
 *
 * A script keeps its own way of saying misuse (its exit 2 and its usage), so it
 * catches `ValueNeeded` and rethrows its own, or catches by `value.option`.
 */

/** Why this word cannot be a value, or null when it can be one. */
export const whyNotAValue = (word) => {
  if (typeof word !== 'string') return 'nothing follows it';
  if (word === '-') return null;
  if (word.trim() === '') return 'it is empty';
  if (word.startsWith('-')) return `"${word.trim()}" is another option, not a value`;
  return null;
};

/** A value an option needed and could not have: misuse, and which option. */
export class ValueNeeded extends Error {
  constructor(option, what, why) {
    super(`the option "${option}" needs ${what}: ${why}`);
    this.name = 'ValueNeeded';
    this.option = option;
  }
}

/**
 * The value of the option at `argv[index]`. Never a guess: the word after the
 * option is returned only when it can be one, so an option given no value stops
 * the run instead of dropping what it asked for.
 */
export function valueOf(argv, index, option, what = 'a value') {
  const word = argv[index + 1];
  const why = whyNotAValue(word);
  if (why !== null) throw new ValueNeeded(option, what, why);
  return word;
}

/** The same for a value carried in the option itself, `--option=value`. */
export function inlineValue(word, option, what = 'a value') {
  const why = whyNotAValue(word);
  if (why !== null) throw new ValueNeeded(option, what, why);
  return word;
}
