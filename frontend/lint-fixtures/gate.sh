#!/bin/sh
# Negative fixture gate — assert ALL required eslint-plugin-solid rules fire,
# that the nocx/no-raw-controls and nocx/no-color-literals rules fire,
# and that the CSS colour grammar checker catches violation patterns.
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

# ── ESLint fixture check ─────────────────────────────────────────────────────
# Run eslint on .tsx and .ts files (not .css — espree cannot parse CSS).
# The .ts glob is needed for the solid/reactivity .ts fixture.
eslint_json=$(npx eslint --no-ignore "${fixture_dir}/"*.tsx "${fixture_dir}/"*.ts --quiet --format json 2>/dev/null) || true

# Collect every rule ID that fired
fired_rules=$(echo "$eslint_json" | node -e "
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
ts_reactivity=$(echo "$eslint_json" | node -e "
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

echo "OK — all 10 lint rules fired (solid/reactivity confirmed from .ts, CSS colour grammar verified, color-mix laundering blocked)"
exit 0
