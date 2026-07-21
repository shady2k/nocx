#!/usr/bin/env bash
# Renderer glyph probe — paste into each bake-off tab and compare.
# Any cell that shows '?' or a tofu box tells us which glyph class the
# renderer (or the font stack in frontend/src/renderers/font.ts) misses.
set -u

row() { printf '  %-18s %s\n' "$1" "$2"; }

echo
echo "── locale / term ─────────────────────────────"
row "TERM"     "${TERM:-<unset>}"
row "LANG"     "${LANG:-<unset>}"
row "LC_ALL"   "${LC_ALL:-<unset>}"
row "LC_CTYPE" "${LC_CTYPE:-<unset>}"

echo
echo "── glyph classes ─────────────────────────────"
row "box drawing"    "─ │ ┌ ┐ └ ┘ ├ ┤ ┬ ┴ ┼ ═ ║ ╔ ╗ ╚ ╝"
row "rounded corner" "╭ ╮ ╯ ╰"
row "blocks/shades"  "█ ▓ ▒ ░ ▌ ▐ ▀ ▄ ▁ ▂ ▃"
row "braille"        "⠀ ⠁ ⠉ ⠿ ⣿"
row "arrows/marks"   "→ ← ✓ ✗ • · … × » § ✎"
row "misc symbols"   "⚠ ⚡ ⚕ ⌘ ⚙ ⌛ ★ ☆ ● ○ ◉"
row "emoji"          "✅ 🔐 🔑 🎤 🧠 🚀"
row "powerline"      "     "
row "nerd font PUA"  "󰌾 󰋼 󰅚 "

echo
echo "── how each class should look ────────────────"
echo "  '?'  = the byte stream already contains a literal '?' (sender's fault,"
echo "         not the renderer — it will look identical in every tab)"
echo "  tofu = renderer/font has no glyph for it (renderer's fault — a real"
echo "         differentiator between the three tabs)"
echo "  ok   = fine"
echo
