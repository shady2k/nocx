// ESLint flat config — the frontend's golangci-lint (AGENTS.md: Go and
// TypeScript are held to the same bar). Type-checked rules are on: without the
// type information most of what golangci-lint catches on the Go side has no
// TypeScript equivalent. Formatting is prettier's job — eslint-config-prettier
// switches off every stylistic rule that would fight it.
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import prettier from 'eslint-config-prettier'
import solid from 'eslint-plugin-solid'
import globals from 'globals'
import { readFileSync, existsSync } from 'node:fs'
import { relative, resolve, basename } from 'node:path'
import { createHash } from 'node:crypto'
import { scanKitIdentities } from './lint-fixtures/scan-kit-identities.mjs'

// ─── Baseline loader (ADR-0014 §"The guard") ───────────────────────────────────────
// Per-violation baseline: each raw-control violation still present in application
// surfaces is enumerated by {file, id} where id is a hash of the node's source text.
// Stale baseline entries (violation no longer present) are harmless; only growth
// (a violation without a matching baseline entry) is an error.
const CONFIG_DIR = import.meta.dirname
const PROJECT_ROOT = resolve(CONFIG_DIR, '..')
const BASELINE_PATH = resolve(CONFIG_DIR, 'lint-fixtures/raw-controls-baseline.json')
const COLOR_BASELINE_PATH = resolve(CONFIG_DIR, 'lint-fixtures/color-literals-baseline.json')
/** Map of "${relativePath}:${hashId}" → entry for fast lookup. */
function loadBaseline() {
  try {
    if (!existsSync(BASELINE_PATH)) return new Map()
    const raw = JSON.parse(readFileSync(BASELINE_PATH, 'utf-8'))
    const map = new Map()
    for (const v of raw.violations || []) {
      map.set(`${v.file}:${v.id}`, v)
    }
    return map
  } catch {
    return new Map()
  }
}
function loadColorBaseline() {
  const map = new Map()
  try {
    const data = JSON.parse(readFileSync(COLOR_BASELINE_PATH, 'utf8'))
    for (const v of data.violations) {
      map.set(`${v.file}:${v.id}`, v)
    }
  } catch {
    // No baseline yet — every violation is an error
  }
  return map
}

// Cache the baseline once; reloaded only on restart. Regeneration is a deliberate
// script, not a side effect of lint.
const baseline = loadBaseline()
const colorBaseline = loadColorBaseline()
/**
 * Declared feature components — owned code that legitimately carries a role a kit
 * primitive also provides. Today that is only Tab with `role=tab` (nocx-olav).
 *
 * A register of deliberate exceptions, not a baseline: it does not have to shrink, but
 * every entry states the composite contract it owns, so adding one is a reviewable act
 * rather than a number going up.
 *
 * @type {Array<{ file: string, identity: string, roles: string[], contract: string }>}
 */
const FEATURE_COMPONENTS_PATH = resolve(CONFIG_DIR, 'src/ui/feature-components.json')
let featureComponents = []
try {
  featureComponents = JSON.parse(readFileSync(FEATURE_COMPONENTS_PATH, 'utf-8'))
} catch {
  // Absent file means no exceptions, which is the safe default.
  featureComponents = []
}

// ─── Inline-markup guard: class ownership ───────────────────────────────────────────
// Derived by AST from static class=/className=/classList= on JSX elements in each
// ui/*.tsx file. See scan-kit-identities.mjs for the full derivation logic.
const UI_DIR = resolve(PROJECT_ROOT, 'frontend/src/ui')
const { byClass: classOwnership } = scanKitIdentities(UI_DIR)

const INLINE_MARKUP_BASELINE_PATH = resolve(CONFIG_DIR, 'lint-fixtures/inline-markup-baseline.json')

function loadInlineMarkupBaseline() {
  const map = new Map()
  try {
    const data = JSON.parse(readFileSync(INLINE_MARKUP_BASELINE_PATH, 'utf8'))
    for (const v of data.violations) {
      map.set(`${v.file}:${v.id}`, v)
    }
  } catch {
    // No baseline yet — every violation is an error
  }
  return map
}

const inlineMarkupBaseline = loadInlineMarkupBaseline()

// ─── Path-based exemption patterns (ADR-0014) ──────────────────────────────────────
// Application surfaces: files matching an exempt pattern are allowed to use raw
// controls / innerHTML. ESLint's own ignores (dist, lint-fixtures) handle the
// rest; this function only checks explicit ADR exemptions.
const EXEMPT_PATTERNS = [
  // The kit — native implementation details live here intentionally
  (rel) => rel.includes('/src/ui/'),

  // Terminal-owned files (ADR-0012 §"What is deliberately still imperative")
  (rel) => rel.endsWith('/src/tabs.ts'),
  (rel) => rel.endsWith('/src/tab-content.ts'),
  (rel) => rel.endsWith('/src/terminal-content.ts'),
  (rel) => rel.includes('/src/renderers/'),
  (rel) => rel.includes('/src/scrollback/'),
  (rel) => rel.endsWith('/src/editor.ts'),
  (rel) => rel.endsWith('/src/gutter.ts'),
  (rel) => /\/src\/input-[\w-]+\.ts$/.test(rel),
  (rel) => rel.endsWith('/src/dispatcher.ts'),
  (rel) => rel.endsWith('/src/command-ledger.ts'),
  (rel) => rel.endsWith('/src/clipboard.ts'),
  (rel) => rel.endsWith('/src/frame.ts'),
  (rel) => rel.endsWith('/src/submit.ts'),
  (rel) => rel.endsWith('/src/ipc.ts'),

  // Test files and test support
  (rel) => /\.(test|spec)\.(ts|tsx)$/.test(rel),
  (rel) => rel.includes('/test-support/'),
]

function isExempt(relPath) {
  return EXEMPT_PATTERNS.some((p) => p(relPath))
}

function hashNode(sourceCode, node) {
  const text = sourceCode.getText(node)
  return createHash('sha256').update(text).digest('hex').slice(0, 12)
}

// ─── Custom rule: nocx/no-raw-controls ─────────────────────────────────────────────
// Rejects raw interactive elements and innerHTML in application surfaces.
// See ADR-0014 §"The guard" and brief nocx-vxqj.6.
const nocxPlugin = {
  rules: {
    'no-raw-controls': {
      meta: {
        type: 'suggestion',
        docs: {
          description:
            'Reject raw interactive elements (button, select, textarea, input) and innerHTML in application surfaces. Use kit components from ui/ instead (ADR-0014).',
        },
        messages: {
          rawControl: "Use a kit component from 'ui/' instead of raw <{{tag}}>. See ADR-0014.",
          rawInput:
            'Use a kit component from \'ui/\' instead of raw <input type="{{type}}">. See ADR-0014.',
          innerHTML:
            'Use a kit component instead of innerHTML assignment. Icons are components, not markup. See ADR-0014.',
        },
      },
      create(context) {
        const filename = context.filename ?? ''
        const rel = relative(PROJECT_ROOT, filename)

        if (isExempt(rel)) return {}

        const sourceCode = context.sourceCode

        // Raw HTML tags that must use kit components
        const RAW_TAGS = new Set(['button', 'select', 'textarea'])

        // Input types that must use kit components. 'file' joined the list with
        // nocx-dcsx: its absence is how two raw file inputs sat in Export at a zero
        // baseline — the rule forbade the honest version and not the one that shipped.
        const RAW_INPUT_TYPES = new Set(['checkbox', 'radio', 'text', 'password', 'search', 'file'])

        function isBaselined(id) {
          // NOCX_BASELINE_UPDATE bypasses the baseline so the generator sees
          // every violation and can produce a complete baseline file.
          if (globalThis.process.env.NOCX_BASELINE_UPDATE) return false
          return baseline.has(`${rel}:${id}`)
        }

        function checkJSX(node) {
          const tagName = node.name.type === 'JSXIdentifier' ? node.name.name : null
          if (!tagName) return

          if (RAW_TAGS.has(tagName)) {
            const id = hashNode(sourceCode, node)
            if (!isBaselined(id)) {
              context.report({
                node,
                messageId: 'rawControl',
                data: { tag: tagName },
              })
            }
            return
          }

          if (tagName === 'input') {
            const typeAttr = node.attributes.find(
              (a) =>
                a.type === 'JSXAttribute' &&
                a.name.type === 'JSXIdentifier' &&
                a.name.name === 'type',
            )
            // No type attribute defaults to "text"
            const typeValue =
              typeAttr && typeAttr.value != null
                ? typeAttr.value.type === 'Literal'
                  ? String(typeAttr.value.value)
                  : typeAttr.value.type === 'StringLiteral'
                    ? typeAttr.value.value
                    : 'text'
                : 'text'

            if (RAW_INPUT_TYPES.has(typeValue)) {
              const id = hashNode(sourceCode, node)
              if (!isBaselined(id)) {
                context.report({
                  node,
                  messageId: 'rawInput',
                  data: { type: typeValue },
                })
              }
            }
          }
        }

        // innerHTML assignment: x.innerHTML = y
        function checkInnerHTML(node) {
          if (
            node.type === 'AssignmentExpression' &&
            node.left.type === 'MemberExpression' &&
            node.left.property.type === 'Identifier' &&
            node.left.property.name === 'innerHTML'
          ) {
            const id = hashNode(sourceCode, node)
            if (!isBaselined(id)) {
              context.report({
                node,
                messageId: 'innerHTML',
              })
            }
          }
        }

        return {
          JSXOpeningElement: checkJSX,
          AssignmentExpression: checkInnerHTML,
        }
      },
    },
    // ─── nocx/no-role-impersonation ─────────────────────────────────────────
    // ADR-0014's raw-control rule forbids <button>, <select>, <textarea> and
    // <input type=...>. It does not stop a surface hand-rolling the same control out
    // of neutral elements — <div role="button">, <span role="checkbox"> — which
    // satisfies the letter of the guard and defeats it entirely.
    //
    // Only the ARCHITECTURAL half lives here: a role a kit primitive already
    // provides. The behavioural half — "is this element actually interactive" —
    // needs a real focus/activation analysis, which is jsx-a11y's job; a
    // half-written version of it fires on legitimate event delegation and gets
    // disabled, which is worse than not having it.
    //
    // `listbox` and `option` are deliberately absent. A quick-connect row is
    // composite domain semantics and a native <select> does not replace an arbitrary
    // list row; forbidding the role would push the surface toward worse markup.
    'no-role-impersonation': {
      meta: {
        type: 'suggestion',
        docs: {
          description:
            'Reject ARIA roles that duplicate a kit primitive, outside ui/ and the declared feature components.',
        },
        messages: {
          impersonation:
            'role="{{role}}" duplicates a kit primitive. Use the component from \'ui/\', or declare this file in ui/feature-components.json with the composite contract it owns.',
        },
        schema: [],
      },
      create(context) {
        const KIT_ROLES = new Set([
          'button',
          'checkbox',
          'radio',
          'switch',
          'textbox',
          'searchbox',
          'combobox',
        ])
        const filename = context.filename ?? context.getFilename()
        const rel = relative(PROJECT_ROOT, filename).replace(/\\/g, '/')
        // ui/ owns the primitives; terminal-owned files are inside the xterm
        // boundary (AD-6) and are not application UI.
        if (rel.includes('frontend/src/ui/')) return {}
        if (/frontend\/src\/(terminal-content|tabs|renderers\/|scrollback\/)/.test(rel)) return {}
        const base = basename(filename)
        const declared = featureComponents.find((f) => f.file === base)
        const allowed = new Set(declared ? declared.roles : [])

        return {
          JSXAttribute(node) {
            if (node.name?.name !== 'role') return
            const v = node.value
            if (!v || v.type !== 'Literal' || typeof v.value !== 'string') return
            const role = v.value
            if (!KIT_ROLES.has(role) || allowed.has(role)) return
            context.report({ node, messageId: 'impersonation', data: { role } })
          },
        }
      },
    },

    'no-color-literals': {
      meta: {
        type: 'suggestion',
        docs: {
          description:
            'Reject colour literals outside themes/ — use theme tokens instead. See ADR-0013 §4 and ADR-0012.',
        },
        messages: {
          colorLiteral:
            'Colour literal "{{literal}}" found. Use a theme token instead (ADR-0013 §4).',
        },
      },
      create(context) {
        const filename = context.filename ?? ''
        const rel = relative(PROJECT_ROOT, filename)
        const sourceCode = context.sourceCode

        function isBaselined(id) {
          if (globalThis.process.env.NOCX_BASELINE_UPDATE) return false
          return colorBaseline.has(`${rel}:${id}`)
        }

        // CSS colour-function names (all prohibited) — used by checkValue below

        // Named CSS colours (excluding safe ones)
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
          'palegoldenrod',
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
        // CSS colour properties to check in style objects
        const COLOR_PROPS = new Set([
          'color',
          'background',
          'background-color',
          'backgroundcolor',
          'border-color',
          'bordercolor',
          'border-top-color',
          'bordertopcolor',
          'border-right-color',
          'borderrightcolor',
          'border-bottom-color',
          'borderbottomcolor',
          'border-left-color',
          'borderleftcolor',
          'outline-color',
          'outlinecolor',
          'fill',
          'stroke',
          'accent-color',
          'accentcolor',
          'caret-color',
          'caretcolor',
        ])

        /** Check a string value for colour literals. Returns array of literal strings. */
        function checkValue(value) {
          const findings = []
          const v = String(value)

          // Hex colours
          const hexRe = /#[0-9a-fA-F]{3,8}(?!\w)/g
          let m
          while ((m = hexRe.exec(v)) !== null) findings.push(m[0])

          // Colour functions
          const fnRe = /\b(rgba?|hsla?|oklch|lab|color)\s*\(/gi
          while ((m = fnRe.exec(v)) !== null) findings.push(m[0].replace(/\(.*/, '()'))

          // Named colours (excluding safe keywords)
          const lower = v.toLowerCase()
          // If the entire value is a safe keyword, skip
          const SAFE_WORDS = new Set(['currentcolor', 'transparent', 'inherit'])
          if (SAFE_WORDS.has(lower)) return findings

          // Check individual words
          const words = lower.split(/[^a-z]/)
          for (const w of words) {
            if (w && w !== 'white' && w !== 'black' && NAMED_COLORS.has(w)) {
              findings.push(w)
            }
          }

          // white/black: only allowed inside color-mix()
          // Simpler check: if value contains 'white' or 'black' NOT inside
          // 'color-mix(', it's a violation
          if (/white/.test(lower) && !/color-mix\(/.test(lower)) {
            findings.push('white')
          }
          if (/black/.test(lower) && !/color-mix\(/.test(lower)) {
            findings.push('black')
          }

          return findings
        }

        return {
          // Check SVG fill/stroke attributes
          JSXAttribute(node) {
            if (!node.name || !node.name.name) return
            const attrName = node.name.name.toLowerCase()
            if (attrName !== 'fill' && attrName !== 'stroke') return

            if (!node.value) return
            // String literal value
            if (node.value.type === 'Literal') {
              const val = String(node.value.value)
              const findings = checkValue(val)
              if (findings.length > 0) {
                const fullText = sourceCode.getText(node)
                const id = createHash('sha256').update(fullText).digest('hex').slice(0, 12)
                if (!isBaselined(id)) {
                  context.report({
                    node,
                    messageId: 'colorLiteral',
                    data: { literal: findings[0] },
                  })
                }
              }
            }
          },

          // Check style={...} object properties
          Property(node) {
            // Only check properties inside style objects in JSX
            const parent = node.parent
            if (!parent || parent.type !== 'ObjectExpression') return
            const grandparent = parent.parent
            if (!grandparent || grandparent.type !== 'JSXExpressionContainer') return
            const greatGP = grandparent.parent
            if (
              !greatGP ||
              greatGP.type !== 'JSXAttribute' ||
              !greatGP.name ||
              greatGP.name.name !== 'style'
            )
              return

            // Get the property name
            let propName = ''
            if (node.key.type === 'Identifier') propName = node.key.name
            else if (node.key.type === 'Literal') propName = String(node.key.value)
            else return

            // Only check colour-related properties
            const lower = propName.toLowerCase().replace(/[_-]/g, '')
            if (!COLOR_PROPS.has(lower)) return

            // Check the value
            if (!node.value) return
            let val = ''
            if (node.value.type === 'Literal') val = String(node.value.value)
            else return // Complex expression — skip

            const findings = checkValue(val)
            if (findings.length > 0) {
              const fullText = sourceCode.getText(node)
              const id = createHash('sha256').update(fullText).digest('hex').slice(0, 12)
              if (!isBaselined(id)) {
                context.report({
                  node,
                  messageId: 'colorLiteral',
                  data: { literal: findings[0] },
                })
              }
            }
          },
        }
      },
    },
    'no-inline-markup': {
      meta: {
        type: 'suggestion',
        docs: {
          description:
            "Reject inline markup that duplicates a kit component's class, and inline style props, in application surfaces. Use components from ui/ instead (ADR-0014).",
        },
        messages: {
          bypassComponent:
            'Use the \'ui/{{component}}\' component instead of duplicating the "{{class}}" class directly in markup. See ADR-0014.',
          inlineStyle:
            'Avoid inline style props in application surfaces. Layout and colour belong in CSS. See ADR-0014.',
        },
      },
      create(context) {
        const filename = context.filename ?? ''
        const rel = relative(PROJECT_ROOT, filename)

        if (isExempt(rel)) return {}

        const sourceCode = context.sourceCode

        function isBaselined(id) {
          if (globalThis.process.env.NOCX_BASELINE_UPDATE) return false
          return inlineMarkupBaseline.has(`${rel}:${id}`)
        }

        /** Extract `ui-*` class names from a class/className attribute value. */
        function extractUIClasses(attr) {
          if (!attr.value) return []

          // String literal: class="ui-page"
          if (attr.value.type === 'Literal' && typeof attr.value.value === 'string') {
            return attr.value.value.split(/\s+/).filter((c) => c.startsWith('ui-'))
          }

          // JSXExpressionContainer
          if (attr.value.type === 'JSXExpressionContainer') {
            const expr = attr.value.expression

            // Template literal: class={`ui-empty-state ${x}`}
            if (expr.type === 'TemplateLiteral') {
              const names = []
              for (const quasi of expr.quasis) {
                const parts = (quasi.value.raw || '').split(/\s+/)
                for (const p of parts) {
                  if (p.startsWith('ui-')) names.push(p)
                }
              }
              return names
            }

            // CallExpression on template literal: class={`ui-empty-state ${x}`.trim()}
            if (
              expr.type === 'CallExpression' &&
              expr.callee.type === 'MemberExpression' &&
              expr.callee.property.type === 'Identifier' &&
              expr.callee.property.name === 'trim'
            ) {
              const inner = expr.callee.object
              if (inner.type === 'TemplateLiteral') {
                const names = []
                for (const quasi of inner.quasis) {
                  const parts = (quasi.value.raw || '').split(/\s+/)
                  for (const p of parts) {
                    if (p.startsWith('ui-')) names.push(p)
                  }
                }
                return names
              }
            }
          }

          return []
        }

        function checkJSX(node) {
          const ownBasename = basename(filename)

          // ── Check 1: class/className duplicating a kit class ──────────────
          for (const attrName of ['class', 'className']) {
            const classAttr = node.attributes.find(
              (a) =>
                a.type === 'JSXAttribute' &&
                a.name.type === 'JSXIdentifier' &&
                a.name.name === attrName,
            )
            if (!classAttr) continue

            const classes = extractUIClasses(classAttr)
            for (const cls of classes) {
              const owners = classOwnership.get(cls)
              if (!owners) continue // not a kit-owned class

              // Owner file may render its own class
              if (owners.has(ownBasename)) continue

              // Files inside ui/ are exempt (composition)
              if (rel.includes('/src/ui/')) continue

              const id = hashNode(sourceCode, classAttr)
              if (!isBaselined(id)) {
                const ownerName = [...owners][0].replace('.tsx', '').replace('.ts', '')
                context.report({
                  node: classAttr,
                  messageId: 'bypassComponent',
                  data: { class: cls, component: ownerName },
                })
                return // one report per element
              }
            }
          }

          // ── Check 2: inline style props ──────────────────────────────────
          const styleAttr = node.attributes.find(
            (a) =>
              a.type === 'JSXAttribute' &&
              a.name.type === 'JSXIdentifier' &&
              a.name.name === 'style',
          )
          if (styleAttr) {
            const id = hashNode(sourceCode, styleAttr)
            if (!isBaselined(id)) {
              context.report({
                node: styleAttr,
                messageId: 'inlineStyle',
              })
            }
          }
        }

        return {
          JSXOpeningElement: checkJSX,
        }
      },
    },
  },
}

// ─── Config export ─────────────────────────────────────────────────────────────────
export default tseslint.config(
  // lint-fixtures/ holds the negative fixtures for eslint-plugin-solid and the
  // nocx/no-raw-controls fixture. They are excluded here and linted explicitly
  // by lint-fixtures/gate.sh with --no-ignore, which asserts each required rule fires.
  // `src/generated/**` is produced by `npm run contracts` from contracts/*.schema.json.
  // Linting it would be linting the generator's output style, and any fix would be
  // overwritten on the next run — the schema is the file to change.
  { ignores: ['dist/**', 'wailsjs/**', 'lint-fixtures/**', 'src/generated/**'] },
  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        // Both projects: tsconfig.json owns src/, tsconfig.node.json owns the
        // Vite config. A file in neither is a file nobody type-checks.
        project: ['./tsconfig.json', './tsconfig.node.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  {
    // Config files are checked by tsconfig.node.json and run in Node, not the
    // browser; they need no type-aware linting of their own.
    files: ['*.config.js', '*.config.ts'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  // Fixture files are outside tsconfig (not in src/) — disable type-checked
  // rules but keep Solid lint rules.
  {
    files: ['lint-fixtures/**'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  // Build scripts run in Node and are outside both tsconfigs, so type-aware
  // rules have no program to consult, and the browser globals the rest of the
  // config assumes are the wrong set for them.
  {
    files: ['scripts/**'],
    extends: [tseslint.configs.disableTypeChecked],
    languageOptions: { globals: globals.node },
  },
  // SolidJS lint rules (ADR-0012 §3). Combined with the recommended base
  // from the plugin into a single files-restricted block so severity and
  // scope cannot drift.
  {
    files: ['**/*.ts', '**/*.tsx', '**/*.jsx'],
    extends: [solid.configs['flat/recommended']],
    rules: {
      'solid/no-destructure': 'error',
      'solid/reactivity': 'error',
      'solid/no-react-deps': 'error',
      'solid/no-react-specific-props': 'error',
      'solid/prefer-for': 'error',
      'solid/prefer-show': 'error',
      'solid/components-return-once': 'error',
    },
  },
  // nocx/no-raw-controls — rejects raw interactive elements and innerHTML in
  // application surfaces (ADR-0014 §"The guard"). Path exemptions and baseline
  // matching are handled inside the rule itself.
  {
    plugins: { nocx: nocxPlugin },
    rules: { 'nocx/no-raw-controls': 'error' },
  },
  // Rule 8 — dependency direction. `ui/` is the vocabulary every surface imports;
  // it must not import back. A primitive that knows about a surface is no longer a
  // primitive, and the cycle is invisible until the day someone tries to reuse it.
  //
  // Expressed with the stock rule rather than a custom one: the constraint is
  // "these paths, from this directory", which no-restricted-imports states exactly,
  // and a hand-written rule here would be code to maintain for nothing.
  {
    files: ['src/ui/**/*.{ts,tsx}'],
    ignores: ['src/ui/**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: ['../*', '../../*'],
              message:
                'ui/ may not import from outside itself: surfaces, features and application state all depend on the kit, and the kit must not depend back. Move the shared thing into ui/, or pass it in as a prop.',
            },
          ],
        },
      ],
    },
  },
  // nocx/no-role-impersonation — closes the hole no-raw-controls leaves: a control
  // hand-rolled out of neutral elements with an ARIA role (design spec §4 rule 10).
  {
    plugins: { nocx: nocxPlugin },
    rules: { 'nocx/no-role-impersonation': 'error' },
  },
  // nocx/no-color-literals — rejects colour literals outside themes/
  // (ADR-0013 §4). Covers JSX style props and SVG attributes in TSX files.
  // CSS files outside themes/ are checked by check-css-colors.mjs.
  {
    plugins: { nocx: nocxPlugin },
    rules: { 'nocx/no-color-literals': 'error' },
  },
  // nocx/no-inline-markup — rejects inline markup that duplicates a kit
  // component's class, and inline style props, in application surfaces
  // (ADR-0014). Class ownership derived from ui/*.tsx at load time.
  {
    plugins: { nocx: nocxPlugin },
    rules: { 'nocx/no-inline-markup': 'error' },
  },
  prettier,
)
