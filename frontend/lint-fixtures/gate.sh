#!/bin/sh
# Negative fixture gate — assert ALL required eslint-plugin-solid rules fire,
# that the nocx/no-raw-controls and nocx/no-color-literals rules fire,
# that the AST kit-identity scanner matches fixture expectations,
# that the CSS colour grammar checker catches violation patterns,
# that the menu-icons checker catches a context-menu row built with no mark,
# and that the CSS integrity checker catches every one of its violation classes —
# ten as of nocx-pp3y.2, each asserted by name below, several in both directions.
# Run from the frontend/ directory (e.g. via `npm run lint:fixture-check`).
# Exits 0 if all rules fire, 1 otherwise.
set -eu

fixture_dir="lint-fixtures"
expected_rules="solid/no-destructure solid/reactivity solid/no-react-deps solid/no-react-specific-props solid/prefer-for solid/prefer-show solid/components-return-once nocx/no-raw-controls nocx/no-color-literals nocx/no-inline-markup"

# ── CSS fixture check ─────────────────────────────────────────────────────────
# Run the colour grammar checker on the fixture directory (NOCX_BASELINE_UPDATE
# bypasses baseline filtering so intentional violations are always reported).
css_check=$(NOCX_BASELINE_UPDATE=1 node "${fixture_dir}/check-css-colors.mjs" --dir="${fixture_dir}" 2>/dev/null)
css_violations=$(echo "$css_check" | grep -c '^{' || true)

if [ "$css_violations" -lt 1 ]; then
  echo "CSS COLOUR GATE FAILED — no violations produced by CSS fixture"
  exit 1
fi

# color-mix with red literal (laundering case)
if ! echo "$css_check" | grep -q '"red"'; then
  echo "CSS COLOUR GATE FAILED — color-mix with red literal was not detected (laundering regression)"
  exit 1
fi

# standalone white outside color-mix
if ! echo "$css_check" | grep -q '"white"'; then
  echo "CSS COLOUR GATE FAILED — standalone white was not detected"
  exit 1
fi

# standalone black outside color-mix
if ! echo "$css_check" | grep -q '"black"'; then
  echo "CSS COLOUR GATE FAILED — standalone black was not detected"
  exit 1
fi

# ── CSS integrity fixture check ──────────────────────────────────────────────
# Every rule in check-css-integrity.mjs must fire against the fixture tree.
# All of these defects are valid CSS that the browser accepts silently, so a
# checker that quietly stopped firing would look exactly like a clean codebase.
integrity_check=$(node "${fixture_dir}/check-css-integrity.mjs" \
  --entry="${fixture_dir}/css-integrity-fixture/entry.css" \
  --styles="${fixture_dir}/css-integrity-fixture/styles" \
  --ui="${fixture_dir}/css-integrity-fixture/ui" 2>/dev/null || true)

for rule in unreachable escaped-dot undefined-var theme-scope bare-type-selector control-css-outside-kit kit-scope-selector surface-paints-kit surface-spacing-kit untokenised-type px-font-size-token; do
  if ! echo "$integrity_check" | grep -q "\"rule\":\"${rule}\""; then
    echo "CSS INTEGRITY GATE FAILED — rule '${rule}' did not fire on the fixture"
    exit 1
  fi
done

# ── Row-grammar fixture check (nocx-pp3y.3, acceptance 4) ─────────────────
# The kit owns the record-row grammar (RecordRow's title / one kind badge /
# meta / status); a surface declaring its own *-item-name / *-item-meta
# family is the second dialect the composite exists to make impossible.
# The fixture's three intentional families must fire; the kit's own
# composite part and an unrelated surface class must stay silent.
row_grammar_check=$(node "${fixture_dir}/check-row-grammar.mjs" \
  --dir="${fixture_dir}/row-grammar-fixture" 2>/dev/null || true)

for family in foo-item-name bar-row__meta baz-item-name__inner; do
  if ! echo "$row_grammar_check" | grep -q "$family"; then
    echo "ROW-GRAMMAR GATE FAILED — family '${family}' did not fire on the fixture"
    exit 1
  fi
done

if echo "$row_grammar_check" | grep -q 'ui-record-row__title\|plain-widget'; then
  echo "ROW-GRAMMAR GATE FAILED — reported the kit's own composite part or an unrelated class"
  exit 1
fi

# ── Menu-icons fixture check (nocx-inbw1) ────────────────────────────────
# The kit's ContextMenu reserves the icon column whether or not an icon is
# passed, so an unmarked row compiles, renders, passes its unit tests and
# reaches a person as a menu with an empty gutter — three of the four call
# sites shipped exactly that. The fixture's three intentional omissions must
# fire; the marked row and the option object that is not a menu row must stay
# silent, because a rule that reported those would be turned off.
menu_icons_check=$(node "${fixture_dir}/check-menu-icons.mjs" \
  "${fixture_dir}/menu-icons-fixture/menu.tsx" 2>&1 || true)

for row in bare-row pushed-row undefined-row; do
  if ! echo "$menu_icons_check" | grep -q "$row"; then
    echo "MENU-ICONS GATE FAILED — row '${row}' did not fire on the fixture"
    exit 1
  fi
done

if echo "$menu_icons_check" | grep -q 'marked-row\|not-a-menu-row'; then
  echo "MENU-ICONS GATE FAILED — reported a marked row or an object that is not a menu row"
  exit 1
fi

# Exactly three, and no PARSE violation: the fixture is a real .tsx the
# checker read, not a file it failed closed on and counted as a hit.
menu_icons_hits=$(echo "$menu_icons_check" | grep -c '^lint-fixtures/menu-icons-fixture' || true)
if [ "$menu_icons_hits" -ne 3 ]; then
  echo "MENU-ICONS GATE FAILED — expected exactly 3 unmarked rows, got ${menu_icons_hits}"
  exit 1
fi

if echo "$menu_icons_check" | grep -q 'PARSE ERROR'; then
  echo "MENU-ICONS GATE FAILED — the fixture did not parse; the hits above came from failing closed"
  exit 1
fi

# The other direction, on the REAL tree: the rule must be silent where the
# call sites are correct. A gate that only ever ran against a file built to
# fail cannot tell a working rule from one that reports everything.
if ! node "${fixture_dir}/check-menu-icons.mjs" >/dev/null 2>&1; then
  echo "MENU-ICONS GATE FAILED — the rule reports un-baselined rows on the real tree"
  exit 1
fi

# The narrowed form must NOT be reported: `button.ui-fixture` addresses the component,
# and a rule that forbade it would forbid the correct spelling along with the wrong one.
if [ "$(echo "$integrity_check" | grep -c '"rule":"bare-type-selector"')" -ne 1 ]; then
  echo "CSS INTEGRITY GATE FAILED — expected exactly 1 bare-type-selector hit (the narrowed selector must not be reported)"
  exit 1
fi

# A var() with a fallback is legitimate; reporting it would make the rule noise.
if echo "$integrity_check" | grep -q 'fixture-also-never-declared'; then
  echo "CSS INTEGRITY GATE FAILED — var() with a fallback was reported as undefined"
  exit 1
fi

# Rule 3, both directions. Exactly two hits: the surface painting the component
# (tier A) and the surface reaching into markup the component renders (tier B). The
# fixture's placement rules — display, gap, width, margin, and a subject carrying the
# surface's own class — must stay silent, because placement is the one thing a parent
# has no other way to express and a rule that reported it would be turned off.
integrity_kit_hits=$(echo "$integrity_check" | grep -c '"rule":"surface-paints-kit"' || true)
if [ "$integrity_kit_hits" -ne 2 ]; then
  echo "CSS INTEGRITY GATE FAILED — expected exactly 2 surface-paints-kit hits (tier A + tier B), got ${integrity_kit_hits}"
  exit 1
fi

if echo "$integrity_check" | grep -q 'fixture-own-child'; then
  echo "CSS INTEGRITY GATE FAILED — rule 3 reported a subject carrying the surface's own class"
  exit 1
fi

# The identity set must have been DERIVED. An empty scan would silently disable rule 3,
# so the checker reports it as its own violation; seeing it here means the fixture's
# components were not read and the two hits above came from somewhere else.
if echo "$integrity_check" | grep -q '"rule":"kit-identities-empty"'; then
  echo "CSS INTEGRITY GATE FAILED — the kit identity scan came back empty; rule 3 did not really run"
  exit 1
fi

# Rule: surface-spacing-kit — exactly 1 hit. The existing .fixture-host > .fixture-widget
# declares gap and margin-bottom on a kit identity, which the new rule must report as
# spacing on the component. The .fixture-own-group with gap+margin on a surface class
# (not a kit identity) must stay silent.
integrity_spacing_hits=$(echo "$integrity_check" | grep -c '"rule":"surface-spacing-kit"' || true)
if [ "$integrity_spacing_hits" -ne 1 ]; then
  echo "CSS INTEGRITY GATE FAILED — expected exactly 1 surface-spacing-kit hit (gap + margin-bottom on .fixture-widget), got ${integrity_spacing_hits}"
  exit 1
fi

if echo "$integrity_check" | grep -q 'fixture-own-group'; then
  echo "CSS INTEGRITY GATE FAILED — surface-spacing-kit reported a surface class spacing its own elements"
  exit 1
fi

# Rule 7, all three shapes. The px font-size and the literal font-family are the rule
# itself; the third is the half that keeps the exemption list honest — the fixture's
# entry allows two 9px declarations and the file has one, so a list that had rotted into
# a permission slip would say so. And a declaration that READS the token layer must stay
# silent, or the rule forbids the correct spelling along with the wrong one.
for want in 'font-size: 13px' 'font-family: ' 'still allows' 'font (shorthand): 7px' 'font (shorthand): literal family'; do
  if ! echo "$integrity_check" | grep -qF "$want"; then
    echo "CSS INTEGRITY GATE FAILED — rule 7 did not report: ${want}"
    exit 1
  fi
done

# `var(--token)` in either property, and `font: inherit`, are the correct spellings.
if echo "$integrity_check" | grep -q 'fixture-tokenised\|inherit'; then
  echo "CSS INTEGRITY GATE FAILED — rule 7 reported a declaration that reads a token or inherits"
  exit 1
fi

# Rule: px font-size tokens in tokens.css must be relative (rem).
if ! echo "$integrity_check" | grep -q 'font-size-bad'; then
  echo "CSS INTEGRITY GATE FAILED — px-font-size-token did not report --font-size-bad: 14px"
  exit 1
fi
# The rem token (--font-size-good: 0.875rem) must NOT be reported.
if echo "$integrity_check" | grep -q '"font-size-good"'; then
  echo "CSS INTEGRITY GATE FAILED — px-font-size-token reported a rem token (--font-size-good)"
  exit 1
fi
# The terminal exception (--font-size-terminal: 13px) must NOT be reported.
if echo "$integrity_check" | grep -q 'font-size-terminal'; then
  echo "CSS INTEGRITY GATE FAILED — px-font-size-token reported the terminal exception (--font-size-terminal)"
  exit 1
fi

# A correctly scoped theme rule must not be reported alongside the bare :root.
integrity_theme_hits=$(echo "$integrity_check" | grep -c '"rule":"theme-scope"' || true)
if [ "$integrity_theme_hits" -ne 1 ]; then
  echo "CSS INTEGRITY GATE FAILED — expected exactly 1 theme-scope hit, got ${integrity_theme_hits}"
  exit 1
fi

# ── Kit identity fixture check ──────────────────────────────────────────────
# The AST scanner must find the expected classes and not pick up comment-only
# or querySelector patterns. See check-kit-identities.mjs.
if ! node "${fixture_dir}/check-kit-identities.mjs" 2>&1; then
  echo "FAIL — kit identity scanner did not match fixture expectations"
  exit 1
fi

# ── Role-impersonation fixture check ─────────────────────────────────────────
# nocx/no-role-impersonation must fire on every control hand-rolled from a neutral
# element, and must NOT fire on role=option / role=listbox, which are composite
# domain semantics no kit primitive replaces. Both directions are asserted: a rule
# that over-reaches gets disabled, which is the same outcome as not having it.
# No --format compact: it was dropped from core ESLint, and with `|| true`
# swallowing the error the gate silently measured zero and reported a pass.
role_check=$(npx eslint --no-ignore \
  "${fixture_dir}/nocx-no-role-impersonation.tsx" 2>&1 || true)

role_hits=$(echo "$role_check" | grep -c 'no-role-impersonation' || true)
if [ "$role_hits" -lt 7 ]; then
  echo "ROLE GATE FAILED — expected 7 impersonation reports, got ${role_hits}"
  exit 1
fi

if echo "$role_check" | grep -qE 'role="(option|listbox)"'; then
  echo "ROLE GATE FAILED — the rule reported role=option or role=listbox; it has over-reached"
  exit 1
fi

# ── Dependency-direction fixture check (§4 rule 8) ───────────────────────────
# The rule is `no-restricted-imports` scoped to `files: ['src/ui/**']`, so a fixture
# living in lint-fixtures/ cannot trigger it — the config would never apply. The only
# way to watch it fire is from inside that directory, so the gate writes a file there,
# reads the report, and removes it. The trap matters: a leftover would break `tsc` for
# everyone, and this is the one gate whose fixture cannot be a checked-in file.
import_fixture="src/ui/__gate_import_direction.tsx"
cleanup_import_fixture() { rm -f "$import_fixture"; }
trap cleanup_import_fixture EXIT INT TERM
cat > "$import_fixture" <<'FIXTURE'
// Temporary fixture written by lint-fixtures/gate.sh — see the note there. If you are
// reading this in a working tree, the gate crashed between writing and removing it, and
// deleting it is the whole fix.
import { SURFACE_ID_SETTINGS } from '../surface-registry'
export const dependencyDirection = SURFACE_ID_SETTINGS
FIXTURE
import_check=$(npx eslint --no-ignore "$import_fixture" 2>&1 || true)
cleanup_import_fixture
trap - EXIT INT TERM

if ! echo "$import_check" | grep -q 'no-restricted-imports'; then
  echo "IMPORT DIRECTION GATE FAILED — ui/ importing from outside itself was not reported"
  exit 1
fi

# ── ESLint fixture check ─────────────────────────────────────────────────────
# Run eslint on .tsx and .ts files (not .css — espree cannot parse CSS).
# The .ts glob is needed for the solid/reactivity .ts fixture.
eslint_json=$(npx eslint --no-ignore "${fixture_dir}/"*.tsx "${fixture_dir}/"*.ts --quiet --format json 2>/dev/null) || true

# Collect every rule ID that fired.
#
# printf '%s', never echo: /bin/sh's echo expands backslash escapes (macOS ships
# bash with xpg_echo on for /bin/sh, and dash does the same), which turns every
# \n inside the report's `source` field into a real newline and makes the JSON
# unparseable. The node scripts below answer that with exit 2, `set -eu` turns
# the failed assignment into an exit, and the gate dies here with no message of
# its own — reported by the hook as "solid negative fixture gate", which is not
# what broke (nocx-urfz).
fired_rules=$(printf '%s' "$eslint_json" | node -e "
let d='';
process.stdin.resume();
process.stdin.on('data',function(c){d+=c;});
process.stdin.on('end',function(){
  try {
    var r=JSON.parse(d);
    var rules=[...new Set(r.flatMap(function(f){return f.messages.map(function(m){return m.ruleId;}).filter(Boolean);}))];
    rules.sort().forEach(function(r){console.log(r);});
  } catch(e) {
    process.exit(2);
  }
});
")

missing=""
for rule in $expected_rules; do
  if ! echo "$fired_rules" | grep -qF "$rule"; then
    missing="$missing $rule"
  fi
done

if [ -n "$missing" ]; then
  echo "LINT FIXTURE GATE FAILED — the following rule(s) did not fire:$missing"
  exit 1
fi

# solid/reactivity must fire from a .ts file specifically
ts_reactivity=$(printf '%s' "$eslint_json" | node -e "
let d='';
process.stdin.resume();
process.stdin.on('data',function(c){d+=c;});
process.stdin.on('end',function(){
  try {
    var r=JSON.parse(d);
    var ts=r.filter(function(f){return f.filePath.endsWith('.ts');});
    var rules=[...new Set(ts.flatMap(function(f){return f.messages.map(function(m){return m.ruleId;}).filter(function(id){return id==='solid/reactivity';});}))];
    rules.forEach(function(r){console.log(r);});
  } catch(e) {
    process.exit(2);
  }
});
")

if [ -z "$ts_reactivity" ]; then
  echo "SOLID LINT FIXTURE GATE FAILED — solid/reactivity did not fire from a .ts file"
  exit 1
fi

echo "OK — all 10 lint rules fired; kit identities verified; CSS colour + integrity + row-grammar + menu-icons verified (11 integrity rules)"
exit 0
