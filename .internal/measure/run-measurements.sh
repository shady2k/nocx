#!/usr/bin/env bash
# Independent per-primitive measurements against the real production build.
# Each primitive is imported by name and referenced to prevent tree-shaking.
# Total JS = sum across ALL dist/assets/*.js files.
# Usage: bash measure/run-measurements.sh
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE/.."

RESULTS="$HERE/results.txt"
rm -f "$RESULTS" "$RESULTS.tmp" 2>/dev/null || true

KOBALTE_VER=$(cat node_modules/@kobalte/core/package.json | grep '"version"' | cut -d'"' -f4)
CORVU_VER=$(cat node_modules/corvu/package.json 2>/dev/null | grep '"version"' | cut -d'"' -f4 || echo "n/a")

echo "=== Kobalte Measurement Run ===" | tee -a "$RESULTS"
echo "Date:    $(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "$RESULTS"
echo "Kobalte: $KOBALTE_VER" | tee -a "$RESULTS"
echo "Corvu:   $CORVU_VER" | tee -a "$RESULTS"
echo "" | tee -a "$RESULTS"

# Format: "Label:ExportName:PackagePath"
PRIMITIVES=(
  "Dialog:Dialog:@kobalte/core/dialog"
  "Popover:Popover:@kobalte/core/popover"
  "Tooltip:Tooltip:@kobalte/core/tooltip"
  "Select:Select:@kobalte/core/select"
  "Combobox:Combobox:@kobalte/core/combobox"
  "ContextMenu:ContextMenu:@kobalte/core/context-menu"
)

COMBINATIONS=(
  "DPT:Dialog+Popover+Tooltip:@kobalte/core/dialog+@kobalte/core/popover+@kobalte/core/tooltip"
  "All6:Dialog+Popover+Tooltip+Select+Combobox+ContextMenu:@kobalte/core/dialog+@kobalte/core/popover+@kobalte/core/tooltip+@kobalte/core/select+@kobalte/core/combobox+@kobalte/core/context-menu"
)
CORVU=(
  "CorvuDialog:Root:corvu/dialog"
)

# Measure total JS in dist/assets/ (all .js files summed)
measure_js() {
  local TOTAL=0
  for f in dist/assets/*.js; do
    [ -f "$f" ] && TOTAL=$((TOTAL + $(stat -c%s "$f")))
  done
  echo $TOTAL
}

measure_gzip() {
  cat dist/assets/*.js | gzip -c | wc -c
}

# ---- Baseline ----
echo "--- Build 0: Baseline (no Kobalte) ---" | tee -a "$RESULTS"
npm run build 2>/dev/null
BASELINE_RAW=$(measure_js)
BASELINE_GZIP=$(measure_gzip)
echo "  Raw: $BASELINE_RAW B | Gzip: $BASELINE_GZIP B" | tee -a "$RESULTS"
echo "" | tee -a "$RESULTS"

# ---- Build function ----
build_one() {
  local LABEL="$1"
  local EXPORTS_STR="$2"
  local PKGS_STR="$3"
  local VITE_ENTRY="kobalte-measure-$LABEL.html"
  local TSX="src/kobalte-measure-$LABEL.tsx"

  IFS='+' read -ra EXPORTS <<< "$EXPORTS_STR"
  IFS='+' read -ra PKGS <<< "$PKGS_STR"

  # Generate HTML entry
  cp kobalte-measure.html "$VITE_ENTRY"
  sed -i "s|src/kobalte-measure\\.tsx|src/kobalte-measure-$LABEL.tsx|" "$VITE_ENTRY"

  # Generate TSX entry
  local IMPORT_NAMES=""
  {
    echo "// Auto-generated; do not edit"
    echo "import './main'"
    echo "import App from './App'"

    for i in "${!EXPORTS[@]}"; do
      local EXP="${EXPORTS[$i]}"
      local PKG="${PKGS[$i]}"
      local VAR="K${i}_${EXP}"
      echo "import { $EXP as $VAR } from '$PKG'"
      if [ -n "$IMPORT_NAMES" ]; then
        IMPORT_NAMES="$IMPORT_NAMES, $VAR"
      else
        IMPORT_NAMES="$VAR"
      fi
    done

    echo "const _k = { $IMPORT_NAMES }"
    echo "console.log('measure:', '$LABEL', _k)"
    echo "export default App"
  } > "$TSX"

  echo "--- Build: $LABEL ---" | tee -a "$RESULTS"

  VITE_ENTRY="$VITE_ENTRY" npx vite build --config vite.measure.config.ts 2>/dev/null || {
    echo "  ** BUILD FAILED for $LABEL **" | tee -a "$RESULTS"
    echo "$LABEL|FAILED|FAILED|FAILED|FAILED" >> "$RESULTS.tmp"
    rm -f "$VITE_ENTRY" "$TSX"
    return
  }

  local RAW=$(measure_js)
  local GZIP=$(measure_gzip)
  local DELTA_RAW=$((RAW - BASELINE_RAW))
  local DELTA_GZIP=$((GZIP - BASELINE_GZIP))

  echo "  Raw: $RAW B (Δ +$DELTA_RAW B) | Gzip: $GZIP B (Δ +$DELTA_GZIP B)" | tee -a "$RESULTS"
  echo "$LABEL|$RAW|$GZIP|$DELTA_RAW|$DELTA_GZIP" >> "$RESULTS.tmp"

  # Save visualizer output for key builds
  if [ "$LABEL" = "Dialog" ] || [ "$LABEL" = "DPT" ] || [ "$LABEL" = "All6" ] || [ "$LABEL" = "CorvuDialog" ]; then
    if [ -f dist/stats-gzip.html ]; then
      cp dist/stats-gzip.html "$HERE/stats-$LABEL.html" 2>/dev/null || true
    fi
  fi

  rm -f "$VITE_ENTRY" "$TSX"
}

# ---- Solo builds ----
for prim in "${PRIMITIVES[@]}"; do
  IFS=':' read -r LABEL EXPORTS PKGS <<< "$prim"
  build_one "$LABEL" "$EXPORTS" "$PKGS"
done

# ---- Combinations ----
for combo in "${COMBINATIONS[@]}"; do
  IFS=':' read -r LABEL EXPORTS PKGS <<< "$combo"
  build_one "$LABEL" "$EXPORTS" "$PKGS"
done

# ---- Corvu ----
for item in "${CORVU[@]}"; do
  IFS=':' read -r LABEL EXPORTS PKGS <<< "$item"
  build_one "$LABEL" "$EXPORTS" "$PKGS"
done

# ---- Summary ----
echo ""
echo "=== Summary ===" | tee -a "$RESULTS"
echo "Baseline: Raw $BASELINE_RAW B / Gzip $BASELINE_GZIP B" | tee -a "$RESULTS"
echo "" | tee -a "$RESULTS"
printf "%-20s %10s %10s %10s %10s\n" "Primitive" "Raw(B)" "Gzip(B)" "ΔRaw(B)" "ΔGzip(B)" | tee -a "$RESULTS"
printf -- "-------------------- ---------- ---------- ---------- ----------\n" | tee -a "$RESULTS"
cat "$RESULTS.tmp" 2>/dev/null | tee -a "$RESULTS" || true

echo "" | tee -a "$RESULTS"
echo "Visualizer HTML saved in measure/ for Dialog, DPT, All6, CorvuDialog" | tee -a "$RESULTS"
echo "Full results: $RESULTS" | tee -a "$RESULTS"

rm -f "$RESULTS.tmp"
