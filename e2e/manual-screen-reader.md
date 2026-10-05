# Joined terminal accessibility acceptance: manual screen-reader pass

The automated browser acceptance is `e2e/live-to-card-joined.spec.ts`. This
procedure is the paired manual check. Run it on the macOS desktop build at the
stage-acceptance commit, not in the Linux container. Record the macOS version,
app build/commit, screen-reader name and version, tester, date, and each result
in the stage acceptance record. The owner performs this named-reader pass.

## VoiceOver on macOS

1. Start VoiceOver with **Command-F5**. Open a fresh nocx session and run a
   command that prints at least two output lines, then run a second command so
   the first command is a historical card and the second is live output.
2. Use VoiceOver navigation (Control-Option-Right Arrow) to reach the live
   terminal output. Confirm VoiceOver identifies the `Terminal output` grid and
   announces the focused row's text. Use Up/Down Arrow while interacting with
   the grid; confirm focus moves to the adjacent row and does not send a key to
   the shell or activate a block.
3. Navigate to the historical command block. Confirm it is announced as a
   named group and that its output rows can be read. Return to the live grid and
   confirm the latest output is still present after leaving the card.
4. Leave the live grid focused while a command emits several successive screen
   updates (for example, a short loop printing distinct counter values). Confirm
   ordinary changed cells do not repeatedly interrupt or re-announce the row.
   Navigate away and back; confirm the row reports the latest screen contents.
5. Repeat steps 1–4 once with a live-to-card drag/copy while the second command
   emits output. Confirm the copied selection is readable and the newest live
   output remains visible after release.

## Acceptance record

Record explicit pass/fail for: named grid, row text, Up/Down row navigation,
named historical block, latest screen after release, and absence of repeated
announcements during ordinary frame updates. If any item fails, record the
exact utterance or missing state; do not infer a pass from the automated test.
