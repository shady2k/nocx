# P3 publisher measurement

This report records the fixed publish footprint measured by `TestMeasuredMaximumIsStill`
in `publisher_measure_test.go`. The test measures production bundle bytes written through
the filesystem seam and counts calls across publish paths.

## Current maximum

- Maximum filesystem-seam calls per publish: **63**.
- Maximum bytes written per publish: **86,017**.
- Measured worst path: replacement plus sweep, with the launch carrier absent.
- Fault position used for the reported worst case: `remove#4`.

The 2026-10-16 measurement is 518 bytes below the previous 86,535-byte maximum. The
reduction comes from removing the redundant one-time waiting-question text from the bash
and zsh shell wrappers (nocx-xn63t.4.2). The filesystem-call maximum did not change.

Keep these values in sync with `measuredMaxPublishCalls` and `measuredMaxPublishBytes` in
`publisher_measure_test.go`. Update this report and both constants whenever the measured
maximum changes.
