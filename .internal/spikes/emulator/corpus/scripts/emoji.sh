#!/usr/bin/env bash
# The emoji capture's program: one grapheme cluster per line, each wrapped in
# a pair of ASCII markers so a width error moves '<' and '>' rather than
# silently shifting unseen text, and a line of ordinary text after each so the
# next line's column 0 is exercised too.
printf '%s\n' 'family <👨‍👩‍👧‍👦>'
printf '%s\n' 'after family'
printf '%s\n' 'skin <👍🏽>'
printf '%s\n' 'after skin'
printf '%s\n' 'flag <🇷🇺>'
printf '%s\n' 'after flag'
printf '%s\n' 'heart <❤️>'
printf '%s\n' 'after heart'
printf '%s\n' 'plain <ab>'
printf '%s\n' 'after plain'
