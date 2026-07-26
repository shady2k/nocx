# command-editor.spec.ts — findings report

## Summary

- **1 test deleted** (submit button — feature removed by design)
- **1 test left untouched** (gutter glyph — feature still present, test describes a real bug)
- **3 tests unchanged and still passing** (editor visibility, mouse hit-test, double-click)

## Per-test evidence

### Deleted: "the submit button is clickable and submits" (was line 61)

| Evidence       | Detail                                                                                       |
| -------------- | -------------------------------------------------------------------------------------------- |
| Selector       | `.nocx-editor-submit`                                                                        |
| Source check   | Zero matches in `frontend/src/`                                                              |
| Removal commit | `7204aff` — "fix: alt-screen overlap, remove submit btn, font sizes 14px, submit transition" |
| Verdict        | Feature deliberately removed. Test was stale. Deleted.                                       |

No other test or helper referenced `.nocx-editor-submit`. No dead imports or constants left behind.

### Kept (probable bug): "a multi-line command is one gutter landmark, not three" (line 64)

| Evidence     | Detail                                                                                                                                                                                                                                                                                    |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Selector     | `.nocx-gutter-glyph`                                                                                                                                                                                                                                                                      |
| Source check | **Rendered** in `frontend/src/gutter.ts` lines 183, 193; styled in `frontend/src/style.css` line 1236                                                                                                                                                                                     |
| History      | Introduced in `ec0ab8e` ("feat(blocks): command-ledger model + gutter glyphs"), refined in `155262d` ("style(blocks): gutter glyph CSS")                                                                                                                                                  |
| Verdict      | Feature still exists. The test's `expect.poll(...).toBe(1)` failure receiving `0` indicates a real product bug — gutter glyphs either aren't created on submit, or are created but then immediately removed, or the commit-landmark wiring has a regression. **Do not delete this test.** |

## Files modified

Only `e2e/command-editor.spec.ts` — removed the submit-button test (one `test()` block). No other files touched.

## Product bug to file

The gutter-glyph multi-line landmark assertion at `e2e/command-editor.spec.ts:64` fails consistently: after submitting a 3-line command, `expect.poll(...).toBe(1)` receives `0`. The gutter glyph rendering code in `frontend/src/gutter.ts` exists and is wired into the editor lifecycle, but no glyph appears. This may be a broken connection between the command-ledger model and the gutter renderer, or a timing issue in glyph creation. Root cause is outside this worker's scope.
