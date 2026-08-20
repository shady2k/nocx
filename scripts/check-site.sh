#!/usr/bin/env bash
# Structural gate for site/. Three checks, each bought by a defect with a
# known way of happening; see spec §11 in
# .internal/specs/2026-08-20-github-landing-design.md.
#
# What this cannot do is verify that a claim on the page is true. Check 3 is
# the closest available: it refuses wordings already established as false.
# The claims themselves are read by eye against spec §7.
#
# bash 3.2 compatible on purpose: this runs from the pre-commit hook, and
# macOS still ships bash 3.2, where `mapfile` does not exist. Filenames under
# site/ are ours and contain no newlines, so word-splitting on \n is safe here
# and buys portability that a -print0 pipeline would cost.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
site="$root/site"
phrases="$root/scripts/site-forbidden-phrases.txt"
fail=0

if [ ! -d "$site" ]; then
  echo "FAIL: $site does not exist"
  exit 1
fi

# Newline-separated, iterated with IFS set to newline only. Every loop below
# runs in THIS shell, never behind a pipe: a `while read` on the right of a |
# runs in a subshell, so `fail=1` set inside it is discarded and the script
# exits 0 while printing FAIL. That bug is silent and total.
pages="$(find "$site" -name '*.html' | sort)"

if [ -z "$pages" ]; then
  echo "FAIL: no HTML under $site"
  exit 1
fi

page_count="$(printf '%s\n' "$pages" | wc -l | tr -d ' ')"

IFS='
'

# 1. Root-absolute paths. The site is served from /nocx/, so href="/style.css"
#    resolves against the *user* page and yields an unstyled document. This is
#    the classic Project Pages failure and it is invisible in local preview,
#    where the page usually sits at the server root.
for f in $pages; do
  hits="$(grep -nE '(href|src)="/' "$f" | grep -v '="//' || true)"
  if [ -n "$hits" ]; then
    echo "FAIL: ${f#$root/} has a root-absolute path; the site is served from a subdirectory"
    printf '%s\n' "$hits" | sed 's/^/       /'
    fail=1
  fi
done

# 2. Every referenced local asset exists, resolved from the page's OWN
#    directory rather than from site/ — site/ru/index.html reaches its
#    stylesheet as ../style.css, and a check anchored at the site root would
#    silently skip every reference the translated page makes.
for f in $pages; do
  dir="$(dirname "$f")"
  refs="$(grep -oE '(src|href)="\.\.?/[^"]+"' "$f" |
    sed -E 's/^(src|href)="//; s/"$//' || true)"
  for rel in $refs; do
    [ -n "$rel" ] || continue
    # A link is not always a file: the language switcher points at a directory
    # and carries ?lang=, which the server never sees as part of a path.
    target="${rel%%\?*}"
    target="${target%%#*}"
    case "$target" in
      */) target="${target}index.html" ;;
    esac
    if [ ! -f "$dir/$target" ]; then
      echo "FAIL: ${f#$root/} references a missing asset: $rel"
      fail=1
    fi
  done
done

# 3. The version on the page is the version that was released. Both pages
#    carry it in data-version / data-released, the deploy workflow rewrites
#    those from the newest tag, and this compares the committed text with the
#    same tag — so the repository cannot quietly fall behind its own release
#    while the deployed page looks current.
tag="$(git -C "$root" describe --tags --abbrev=0 2>/dev/null || true)"
if [ -n "$tag" ]; then
  for f in $pages; do
    shown="$(grep -oE '<b data-version>[^<]+</b>' "$f" |
      sed -E 's/<[^>]+>//g' | head -1 || true)"
    if [ -n "$shown" ] && [ "$shown" != "$tag" ]; then
      echo "FAIL: ${f#$root/} shows $shown; the newest tag is $tag"
      echo "       Update data-version and data-released, or tag the release."
      fail=1
    fi
  done
fi

# 4. The translated page carries the same sections as the canonical one. Two
#    near-identical files drift, and the safety caveats drift first.
en="$site/index.html"
ru="$site/ru/index.html"
if [ -f "$ru" ]; then
  ids_en="$(grep -oE 'id="[^"]+"' "$en" | sort)"
  ids_ru="$(grep -oE 'id="[^"]+"' "$ru" | sort)"
  if [ "$ids_en" != "$ids_ru" ]; then
    echo "FAIL: site/index.html and site/ru/index.html have different sections"
    diff <(printf '%s\n' "$ids_en") <(printf '%s\n' "$ids_ru") |
      sed 's/^/       /' || true
    fail=1
  fi
fi

# 5. Forbidden phrasings (spec §7).
if [ ! -f "$phrases" ]; then
  echo "FAIL: $phrases is missing"
  exit 1
fi

phrase_list="$(grep -vE '^[[:space:]]*(#|$)' "$phrases" || true)"

for phrase in $phrase_list; do
  for f in $pages; do
    hits="$(grep -niF -- "$phrase" "$f" || true)"
    if [ -n "$hits" ]; then
      echo "FAIL: ${f#$root/} contains the forbidden phrase \"$phrase\" (spec §7)"
      printf '%s\n' "$hits" | sed 's/^/       /'
      fail=1
    fi
  done
done

unset IFS

if [ "$fail" -ne 0 ]; then
  echo
  echo "check-site failed. The honesty ledger is spec §7 in"
  echo ".internal/specs/2026-08-20-github-landing-design.md — read the row before"
  echo "editing either the page or the phrase list."
  exit 1
fi

echo "OK:   check-site ($page_count page(s))"
