# Kit foundation measurement (nocx-vxqj.3)

`2026-07-27-kit-foundation-measurement.md` is the measured answer to "what would a headless
component library cost us", produced against the real production entry with each primitive
built independently and bytes attributed by package.

The scripts are kept so the numbers can be re-derived rather than trusted. They are not wired
into the build: they require `@kobalte/core` and `corvu` to be installed first, which the app
deliberately does not depend on. Copy them into `frontend/`, install, run.

Superseded numbers: `.internal/specs/2026-07-27-kobalte-spike-report.md` measured cumulatively
and attributed Select's dependency closure to every primitive. It carries a correction header;
read this file for the figures.
