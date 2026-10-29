# P3 publisher measurement

This report records the fixed publish footprint measured by `TestMeasuredMaximumIsStill`
in `publisher_measure_test.go`. The test measures production bundle bytes written through
the filesystem seam and counts calls across publish paths.

## Current maximum

- Maximum filesystem-seam calls per publish: **63**.
- Maximum bytes written per publish: **92,030**.
- Measured worst path: replacement plus sweep, with the launch carrier absent.
- Fault position used for the reported worst case: `remove#4`.

The 2026-10-16 measurement is 518 bytes below the previous 86,535-byte maximum. The
reduction comes from removing the redundant one-time waiting-question text from the bash
and zsh shell wrappers (nocx-xn63t.4.2). The filesystem-call maximum did not change.

The later 2026-10-16 measurement is 980 bytes above that 86,017-byte figure. The growth
comes from the bash and zsh wrappers installing their Ctrl+C trap for the whole agent
enrolment instead of only the wait for a question, and from closing an interval a cancel
lands on after admission (nocx-xn63t.6.29). The filesystem-call maximum did not change.

The 2026-10-18 measurement is 64 bytes above that 86,997-byte figure, at 87,061. The growth
is the launch state a spawned worker's tool surface needs: the shell records the
`tool_surface` classification beside `local`, clears it with the rest of the launch state,
and stages the private MCP config on it. Both wrappers carry it, so both moved
(nocx-xn63t.6.49). The filesystem-call maximum did not change.

The later 2026-10-19 measurement is **700 bytes above** that 87,203-byte figure, at **87,903**.
The growth is the zsh tier's new delivery (nocx-1ebrs.8): the typed `. /dev/fd/3` bootstrap is
gone, and a transient `ZDOTDIR` phase now runs before any user code - it reads the two bearer
values off descriptor 3 and closes it, moves the lifecycle descriptor to zsh's first free
descriptor above 10, and replays the three phase files from the user's own. That is the
deliberate trade: a bootstrap no user startup file can break, and a line the person never sees.
The filesystem-call maximum did not change.

The 2026-10-19 measurement is 142 bytes above that 87,061-byte figure, at 87,203. The growth
is the cancel trap moving into one helper the launch resolution and the enrolment are both
run under, so an interrupt during the resolution is the cancel too instead of an aborted
wrapper with no sentence (nocx-xn63t.6.30). Both wrappers carry the helper; what they lose is
each enrol's own copy of the trap dance, which is why the move is smaller than the helper.
The filesystem-call maximum did not change.

Keep these values in sync with `measuredMaxPublishCalls` and `measuredMaxPublishBytes` in
`publisher_measure_test.go`. Update this report and both constants whenever the measured
maximum changes.

The 2026-10-20 measurement is **299 bytes above** that 87,903-byte figure, at **88,202**,
and it moves with the launch carrier's new terminal outcome rather than with a generation
script's growth: the bootstrap's markers now travel in a private OSC instead of as lines of
text (nocx-1ebrs.22), which is one printf in the carrier, and `extdebug` moved out of bash's
startup and into the first prompt with the guards that keep the move inert under a person's
`set -e` (nocx-1ebrs.23), which is a function and two guards in each wrapper. The
filesystem-call maximum did not change.

The 2026-10-24 measurement is **10 bytes above** that 88,202-byte figure, at **88,212**.
The shared Bash rcfile template now reserves the pre-startup tool-token reader slot
(nocx-1ebrs.21). Its empty remote form adds a newline, which is escaped in the launch
carrier and measured again when that carrier is published. The local bearer itself
never enters the published text. The identical measurement on `origin/main` before
this change remains 88,202 bytes, and the filesystem-call maximum remains 63.

The 2026-10-25 CI measurement (run `40186714654`, job `121149032510`,
`TestMeasuredMaximumIsStill`) is **1,718 bytes above** 88,212, at **89,930**.
The growth is the Bash/zsh same-owner lifecycle guard and Bash's idempotent
hook captures (nocx-5qa8h). The archive and client stack are unchanged. The
worst path and maximum of 63 filesystem calls are unchanged.

The next CI run (`40188458844`) measured the fork-send ownership guard at
**90,434 bytes**, still **63 calls**. That is the footprint of `6fafa80a9`,
not of the later in-band correction.

CI run `40189752017`, job `121158149486`, then measured the corrected scripts
at `bab4ae8eb`: **90,596 bytes**, still **63 calls**. This supersedes both
snapshots above: **+2,384 bytes** over v0.7.0's 88,212. All growth is shell
script code: the idempotence guard, ownership predicate, fork isolation and
in-band address lifetime correction. The archive and client stack are unchanged.
The worst path remains replacement plus sweep with the launch carrier absent,
at fault `remove#4`. All functional tests in that CI job passed; its only
failure was the stale measured-byte constant, updated with this report.

CI run `40226370660`, job `121268634002`, measured the final scripts at
`452281925`: **91,008 bytes**, still **63 calls**. This is **412 bytes above**
90,596. The growth is injected shell code (nocx-1kejz.2): startup reserves the
legacy loader latch, the sourced script records its own version, and Bash
parses its own quoted trap declarations instead of stripping quotes with text
replacement. The archive and client stack are unchanged. The worst path remains
replacement plus sweep with the launch carrier absent, at fault `remove#4`.
`TestInjectedBashStartupPreservesUserExitTrap` and all other functional tests
in this job passed; only the stale byte constant failed. This report and the
constant are updated together from that observed final-tree measurement.

The 2026-10-29 measurement on branch `fix/nocx-1ebrs-66-1-18-lane-diagnostics`
is **1,022 bytes above** that 91,008-byte figure, at **92,030**, and keeps the
**63**-call maximum. The growth is the Bash tier's prompt-hook probe
(nocx-1ebrs.66.1.18): a `PROMPT_COMMAND` element installed after ours runs
outside the prompt window, where the command-start latch is armed, so the
preexec wrapper now asks whether the line it is about to announce is one of
the prompt hooks and returns without spending the latch when it is. Only
`nocx.bash` moved; `nocx.zsh` and `nocx.posix` publish the same bytes, and the
worst path is unchanged. The source file grew by more than the measured
figure because the comment explaining the probe is stripped at publish time.
