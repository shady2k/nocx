/**
 * One W3C trace per Playwright test (nocx-n14oo.11).
 *
 * Today a failed run shows the assertion and a trace.zip; the backend log the
 * CI artifact carries belongs to whichever spec's backend stopped last
 * (nocx-ky68q), so reading WHY a test failed means downloading artifacts and
 * guessing. The fix starts here: every test gets its own trace id, derived
 * from Playwright's own `testInfo.testId` rather than minted fresh, so a
 * retry of the same test — and a second look at the same failure — both land
 * on the identical id without anything having to remember it across a
 * process boundary.
 *
 * The id is the spec's own shape (internal/log/span.go): 32 lowercase hex
 * characters for the trace, 16 for a span, joined into the `traceparent`
 * header the backend already knows how to continue
 * (internal/transport/ws.go, log.ContinueTrace). This module only mints the
 * header; e2e/harness.ts is what gets it onto the wire, by handing it to the
 * renderer as the endpoint's `traceparent` field
 * (frontend/src/endpoint.ts), which Dispatcher appends to the WebSocket URL.
 *
 * No Playwright import here on purpose: every function takes a plain seed
 * string, so this module has no dependency Node's own test runner needs to
 * fake, and `node --test e2e/trace-context.unit.mts` is the whole unit-test
 * story for it. Its file is `.mts` and named `*.unit.mts` rather than
 * `*.test.mts` on purpose: Playwright's own testMatch
 * (`*.@(spec|test).?(c|m)[jt]s?(x)`) would otherwise collect it as ONE OF
 * ITS OWN spec files and fail trying to run node:test's API through
 * @playwright/test's runner — found by running the full suite listing
 * (`npx playwright test --list`) after adding it, which dropped to zero
 * collected tests with a syntax error pointing at this file.
 */
import { createHash } from 'node:crypto'

const TRACE_SEED_PREFIX = 'nocx/trace:e2e:'
const SPAN_SEED_PREFIX = 'nocx/span:e2e:'

// The spec's reserved "no id" values (span.go's zeroTrace/zeroSpan). A hash
// landing on either by construction is astronomically unlikely and untestable
// as a real collision, so the fallback below exists to make the function
// total rather than to ever actually fire.
const ZERO_TRACE = '0'.repeat(32)
const ZERO_SPAN = '0'.repeat(16)

/** A trace id for this seed, stable across repeated calls with the same
 *  seed and — for any two seeds this suite actually produces — distinct from
 *  every other. Never the reserved all-zero value. */
export function deriveTraceId(seed: string): string {
  const hex = createHash('sha256')
    .update(TRACE_SEED_PREFIX + seed)
    .digest('hex')
    .slice(0, 32)
  return hex === ZERO_TRACE ? '1'.repeat(32) : hex
}

/** A span id for this seed — see deriveTraceId. A different prefix keeps the
 *  trace and span hashes independent, so two seeds cannot collide on one
 *  while differing on the other. */
export function deriveSpanId(seed: string): string {
  const hex = createHash('sha256')
    .update(SPAN_SEED_PREFIX + seed)
    .digest('hex')
    .slice(0, 16)
  return hex === ZERO_SPAN ? '1'.repeat(16) : hex
}

/** The full W3C header for this seed: version "00", always sampled — there is
 *  no exporter here to spare and no volume to shed (span.go says the same of
 *  the backend's own spans). */
export function traceparentFor(seed: string): string {
  return `00-${deriveTraceId(seed)}-${deriveSpanId(seed)}-01`
}

/** The trace id a test's own backend lines will carry, once its connection
 *  sends traceparentForTest(testId) as this test's own header. Exposed
 *  separately from traceparentFor because a caller filtering a log by
 *  trace_id never needs the span half. */
export function traceIdForTest(testId: string): string {
  return deriveTraceId(testId)
}

/** The traceparent this test's renderer connection should send. */
export function traceparentForTest(testId: string): string {
  return traceparentFor(testId)
}

/**
 * The lines of a backend log that this trace id caused.
 *
 * The backend's text handler (internal/log's SlogAdapter over
 * slog.NewTextHandler) writes `trace_id=<hex>` as a bare, unquoted key=value
 * pair — slog only quotes a value that needs it, and a lowercase-hex string
 * never does. The match requires that no further hex character follows the
 * id: every id this module mints is a fixed length, but a log can carry
 * ids minted elsewhere (a run id, another test's shorter fixture value in a
 * unit test), and one being a hex-digit prefix of another must not match.
 */
export function linesForTrace(log: string, traceId: string): string[] {
  if (!traceId) return []
  const pattern = new RegExp(`trace_id=${escapeRegExp(traceId)}(?![0-9a-f])`)
  return log.split('\n').filter((line) => pattern.test(line))
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
