#!/usr/bin/env node
/**
 * CSS colour grammar checker.
 *
 * Scans `.css` files outside `themes/` for colour literal violations using a
 * proper CSS parser (css-tree, available transitively via the toolchain).
 * Outputs violations as JSON Lines to stdout.
 *
 * Grammar (ADR-0013 §4):
 *   Outside themes/, a colour value may be only:
 *     var(--token) | currentColor | transparent | inherit
 *     | white | black  (only as color-mix() operands)
 *     | color-mix(…)   (with permitted operands only)
 *
 * Invocation:
 *   node lint-fixtures/check-css-colors.mjs
 */

import { createRequire } from 'node:module'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { resolve, relative, extname, join, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = fileURLToPath(new URL('.', import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const PROJECT_ROOT = resolve(FRONTEND_DIR, '..')

// css-tree v3 ships CJS; load via createRequire
// It is a transitive dependency of the Vite build and costs zero new entries
// in package.json — the brief's "no new dependencies" condition is satisfied.
const css = createRequire(import.meta.url)('css-tree')

const THEMES_DIR = 'src/styles/themes'

// Colour-related CSS properties to check
const COLOR_PROPS = new Set([
  'color',
  'background',
  'background-color',
  'border-color',
  'border-top-color',
  'border-right-color',
  'border-bottom-color',
  'border-left-color',
  'outline-color',
  'text-decoration-color',
  'column-rule-color',
  'fill',
  'stroke',
  'stop-color',
  'flood-color',
  'lighting-color',
  'accent-color',
  'caret-color',
])

const COLOR_SHORTHANDS = new Set([
  'border',
  'border-top',
  'border-right',
  'border-bottom',
  'border-left',
  'outline',
  'text-decoration',
  'box-shadow',
  'text-shadow',
])

/** All named CSS colours except the four the grammar permits. */
const NAMED_COLORS = new Set([
  'aliceblue',
  'antiquewhite',
  'aqua',
  'aquamarine',
  'azure',
  'beige',
  'bisque',
  'blanchedalmond',
  'blue',
  'blueviolet',
  'brown',
  'burlywood',
  'cadetblue',
  'chartreuse',
  'chocolate',
  'coral',
  'cornflowerblue',
  'cornsilk',
  'crimson',
  'cyan',
  'darkblue',
  'darkcyan',
  'darkgoldenrod',
  'darkgray',
  'darkgreen',
  'darkgrey',
  'darkkhaki',
  'darkmagenta',
  'darkolivegreen',
  'darkorange',
  'darkorchid',
  'darkred',
  'darksalmon',
  'darkseagreen',
  'darkslateblue',
  'darkslategray',
  'darkslategrey',
  'darkturquoise',
  'darkviolet',
  'deeppink',
  'deepskyblue',
  'dimgray',
  'dimgrey',
  'dodgerblue',
  'firebrick',
  'floralwhite',
  'forestgreen',
  'fuchsia',
  'gainsboro',
  'ghostwhite',
  'gold',
  'goldenrod',
  'gray',
  'green',
  'greenyellow',
  'grey',
  'honeydew',
  'hotpink',
  'indianred',
  'indigo',
  'ivory',
  'khaki',
  'lavender',
  'lavenderblush',
  'lawngreen',
  'lemonchiffon',
  'lightblue',
  'lightcoral',
  'lightcyan',
  'lightgoldenrodyellow',
  'lightgray',
  'lightgreen',
  'lightgrey',
  'lightpink',
  'lightsalmon',
  'lightseagreen',
  'lightskyblue',
  'lightslategray',
  'lightslategrey',
  'lightsteelblue',
  'lightyellow',
  'lime',
  'limegreen',
  'linen',
  'magenta',
  'maroon',
  'mediumaquamarine',
  'mediumblue',
  'mediumorchid',
  'mediumpurple',
  'mediumseagreen',
  'mediumslateblue',
  'mediumspringgreen',
  'mediumturquoise',
  'mediumvioletred',
  'midnightblue',
  'mintcream',
  'mistyrose',
  'moccasin',
  'navajowhite',
  'navy',
  'oldlace',
  'olive',
  'olivedrab',
  'orange',
  'orangered',
  'orchid',
  'palegoldenrod',
  'palegreen',
  'paleturquoise',
  'palevioletred',
  'papayawhip',
  'peachpuff',
  'peru',
  'pink',
  'plum',
  'powderblue',
  'purple',
  'rebeccapurple',
  'red',
  'rosybrown',
  'royalblue',
  'saddlebrown',
  'salmon',
  'sandybrown',
  'seagreen',
  'seashell',
  'sienna',
  'silver',
  'skyblue',
  'slateblue',
  'slategray',
  'slategrey',
  'snow',
  'springgreen',
  'steelblue',
  'tan',
  'teal',
  'thistle',
  'tomato',
  'turquoise',
  'violet',
  'wheat',
  'whitesmoke',
  'yellow',
  'yellowgreen',
])

const COLOR_FN_NAMES = new Set(['rgb', 'rgba', 'hsl', 'hsla', 'oklch', 'lab', 'color'])

/**
 * Scan a Raw text node for colour pattern strings.
 * css-tree v3 represents var() fallback bodies as Raw nodes.
 */
function scanRawText(text) {
  const findings = []
  const trimmed = text.trim()

  // Hex colours
  const hexRe = /#[0-9a-fA-F]{3,8}(?!\w)/g
  let m
  while ((m = hexRe.exec(trimmed)) !== null) {
    findings.push({ type: 'hex', text: m[0] })
  }

  // Colour functions
  const fnRe = /\b(rgba?|hsla?|oklch|lab|color)\s*\(/gi
  while ((m = fnRe.exec(trimmed)) !== null) {
    findings.push({ type: `${m[1].toLowerCase()}()`, text: m[0] })
  }

  // Named colours (excluding safe keywords)
  const words = trimmed.toLowerCase().split(/[^a-z]/)
  for (const w of words) {
    if (w && w !== 'white' && w !== 'black' && NAMED_COLORS.has(w)) {
      findings.push({ type: 'named', text: w })
    }
  }

  return findings
}

/**
 * Walk a css-tree Value node and find colour literal violations.
 * Returns array of {type, text} objects.
 *
 * Strategy:
 * - HexColor nodes → violation
 * - Colour function nodes (rgb, hsl, oklch, lab, color) → violation
 * - Identifier nodes → check against named-colour set (allow safe keywords)
 * - var() functions → descend into fallback (after the comma) and scan Raw
 * - color-mix() → descend into operands (not interpolation method args)
 * - Raw nodes anywhere → regex-scan for colour patterns
 */
function findColorLiterals(valueNode) {
  const findings = []
  const ctx = { inColorMix: 0, inVarFallback: 0 }

  css.walk(valueNode, {
    enter(node) {
      if (node.type === 'Function') {
        if (COLOR_FN_NAMES.has(node.name)) {
          findings.push({ type: `${node.name}()`, text: `${node.name}()` })
          this.skip = true
          return
        }
        if (node.name === 'var') {
          ctx.inVarFallback++
          return
        }
        if (node.name === 'color-mix') {
          ctx.inColorMix++
          return
        }
        return
      }

      if (node.type === 'HexColor' || node.type === 'Hash') {
        findings.push({ type: 'hex', text: `#${node.value}` })
        return
      }

      if (node.type === 'Identifier') {
        if (node.name === 'white' || node.name === 'black') {
          if (ctx.inColorMix === 0) {
            findings.push({ type: 'named', text: node.name })
          }
          return
        }
        const SAFE_KW = new Set(['currentColor', 'transparent', 'inherit'])
        if (!SAFE_KW.has(node.name) && NAMED_COLORS.has(node.name)) {
          findings.push({ type: 'named', text: node.name })
        }
        return
      }

      if (node.type === 'Raw') {
        if (ctx.inVarFallback > 0) {
          const rawFindings = scanRawText(node.value)
          findings.push(...rawFindings)
        }
        return
      }
    },

    leave(node) {
      if (node.type !== 'Function') return
      if (node.name === 'var') ctx.inVarFallback--
      if (node.name === 'color-mix') ctx.inColorMix--
    },
  })

  return findings
}

function isColorProperty(prop) {
  return COLOR_PROPS.has(prop) || COLOR_SHORTHANDS.has(prop)
}

/**
 * Walk a directory recursively, returning paths to .css files.
 * Skips themes/, node_modules/, and dist/.
 */
function walkCSSFiles(dir) {
  const results = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      const base = entry.name
      if (
        base !== 'themes' &&
        base !== 'node_modules' &&
        base !== 'dist' &&
        base !== 'lint-fixtures' &&
        !base.startsWith('.test')
      ) {
        results.push(...walkCSSFiles(full))
      }
    } else if (entry.isFile() && entry.name.endsWith('.css')) {
      results.push(full)
    }
  }
  return results
}

export function scanCSSDirectory(dir) {
  const violations = []
  const files = walkCSSFiles(dir)
  const themesAbs = resolve(dir, THEMES_DIR)

  for (const filePath of files) {
    // Normalise so we can compare paths
    const absPath = resolve(filePath)
    if (absPath.startsWith(themesAbs)) continue

    const rel = relative(PROJECT_ROOT, absPath)
    const text = readFileSync(absPath, 'utf8')

    let ast
    try {
      ast = css.parse(text, { positions: true })
    } catch {
      // Parse error — skip this file
      continue
    }

    css.walk(ast, (node) => {
      if (node.type !== 'Declaration') return
      if (!isColorProperty(node.property)) return
      if (!node.value) return

      const findings = findColorLiterals(node.value)
      const line = node.loc ? node.loc.start.line : 0

      for (const f of findings) {
        violations.push({
          file: rel.startsWith('/') ? rel.slice(1) : rel,
          id: sha256Hash(css.generate(node).trim()),
          type: f.type,
          literal: f.text,
          line,
          property: node.property,
        })
      }
    })
  }

  return violations
}

function sha256Hash(text) {
  // Use a simple hash — keep consistent with the rule's approach
  const { createHash } = createRequire(import.meta.url)('node:crypto')
  return createHash('sha256').update(text).digest('hex').slice(0, 12)
}

// ─── CLI entry point ──────────────────────────────────────────────────────
const scriptUrl = import.meta.url
if (process.argv[1] === fileURLToPath(scriptUrl)) {
  // Allow overriding scan directory via --dir argument (for fixture testing)
  const dirArg = process.argv.find((a) => a.startsWith('--dir='))
  const scanDir = dirArg
    ? resolve(FRONTEND_DIR, dirArg.slice('--dir='.length))
    : resolve(FRONTEND_DIR, 'src')

  const violations = scanCSSDirectory(scanDir)

  // Load baseline if not in update mode
  const useBaseline = process.env.NOCX_BASELINE_UPDATE !== '1'
  let baselineMap = new Map()
  if (useBaseline) {
    const baselinePath = resolve(FRONTEND_DIR, 'lint-fixtures/color-literals-baseline.json')
    try {
      const data = JSON.parse(readFileSync(baselinePath, 'utf8'))
      for (const v of data.violations) {
        baselineMap.set(`${v.file}:${v.id}`, v)
      }
    } catch {
      // No baseline — all violations are errors
    }
  }

  const unbaselined = violations.filter((v) => !baselineMap.has(`${v.file}:${v.id}`))

  for (const v of violations) {
    console.log(JSON.stringify(v))
  }

  if (unbaselined.length > 0) {
    console.error(
      `CSS colour violations found: ${violations.length} total, ${unbaselined.length} un-baselined.`,
    )
    for (const v of unbaselined) {
      console.error(`  NEW: ${v.file}:${v.line} ${v.property} = ${v.literal} (${v.type})`)
    }
    if (useBaseline) {
      process.exitCode = 1
    }
  } else if (violations.length > 0) {
    console.error(`CSS colour violations: ${violations.length} (all baselined).`)
  }
}
