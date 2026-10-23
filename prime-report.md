# nocx-1ebrs.70: painted restore glide assertion

The first PR #469 version constrained its painted-motion samples to `frames.slice(lastAbsent + 1, restoredAt + 1)`. CI run 39921480327, WebKit shard 2, failed because that slice ended when the composer became fully opaque, not when the scrollback settle animation finished. The attached frame data showed a real `.scrollback-inner` Web Animations API glide of 140 ms: the panel was fully restored at t=908 ms, while the stack continued moving at t=964 (492.7 px), t=1013 (481.5 px), t=1029 (480.4 px), and t=1039 (480.1 px); the animation ended at t=1056 ms. Thus the product glide existed; the assertion truncated its observation interval.

Changed the test to inspect active, finite, nonzero animation samples from the last absent-panel frame through the settled end of `sampleAcrossSubmit`, which waits for `inner.getAnimations()` to reach zero. It asserts changing painted positions during those samples and retains the final settled-pair stability and fold-at-rest assertions. No product code or timing threshold changed.

Evidence:

- Before interval fix: CI run 39921480327 WebKit shard 2 failed `restore painted changing positions while the glide was running`; the actual trace showed the post-opacity glide motion above.
- Targeted WebKit after fix: `PW_PROJECTS=webkit e2e/run-in-container.sh e2e/composer-submit-flicker.spec.ts` — exit 0, 2 passed.
- Targeted Chromium after fix: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/composer-submit-flicker.spec.ts` — exit 0, 2 passed.
- RED mutation: test-only override set `duration: 0` only for `.scrollback-inner` calls to `Element.animate`; targeted WebKit exited 1 at `scrollbackInner restore glide ran with finite nonzero duration` (expected >0, received 0). The override was removed before green runs.
- `npx prettier --write e2e/composer-submit-flicker.spec.ts` and `git diff --check` passed.
