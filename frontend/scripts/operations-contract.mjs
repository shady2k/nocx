// A dependency-free validator for the JSON Schema subset the operation
// catalogue uses. The repo has no general schema validator, and adding one for
// a single manifest would be a second vocabulary for the same question. The
// subset is closed here on purpose: a schema feature not implemented below is
// a loud error, never a silent pass.

function fail(path, message) {
  throw new Error(`${path}: ${message}`)
}

function resolveRef(root, ref) {
  if (!ref.startsWith('#/')) fail(ref, 'only local $ref is supported')
  let node = root
  for (const part of ref.slice(2).split('/')) {
    node = node?.[part.replace(/~1/g, '/').replace(/~0/g, '~')]
    if (node === undefined) fail(ref, 'unresolvable $ref')
  }
  return node
}

export function validate(schema, value, path = '$', root = schema) {
  if (schema.$ref) return validate(resolveRef(root, schema.$ref), value, path, root)
  if (schema.allOf) {
    for (const [i, sub] of schema.allOf.entries()) validate(sub, value, `${path}.allOf[${i}]`, root)
  }
  if (schema.if) {
    let matched = true
    try {
      validate(schema.if, value, path, root)
    } catch {
      matched = false
    }
    if (matched && schema.then) validate(schema.then, value, `${path}.then`, root)
  }
  if (schema.const !== undefined && value !== schema.const) {
    fail(path, `expected ${JSON.stringify(schema.const)}, got ${JSON.stringify(value)}`)
  }
  if (schema.enum && !schema.enum.includes(value)) {
    fail(path, `${JSON.stringify(value)} is not one of ${schema.enum.join(', ')}`)
  }
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    const actual = value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value
    if (
      !types.includes(actual) &&
      !(types.includes('integer') && actual === 'number' && Number.isInteger(value))
    ) {
      fail(path, `expected ${types.join('|')}, got ${actual}`)
    }
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength)
      fail(path, `shorter than ${schema.minLength}`)
    if (schema.pattern && !new RegExp(schema.pattern).test(value))
      fail(path, `does not match ${schema.pattern}`)
  }
  if (typeof value === 'number' && schema.minimum !== undefined && value < schema.minimum) {
    fail(path, `${value} is below ${schema.minimum}`)
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems)
      fail(path, `fewer than ${schema.minItems} items`)
    if (schema.items)
      value.forEach((item, i) => validate(schema.items, item, `${path}[${i}]`, root))
    return
  }
  if (value && typeof value === 'object') {
    for (const key of schema.required ?? []) {
      if (!(key in value)) fail(path, `missing required ${key}`)
    }
    const properties = schema.properties ?? {}
    if (schema.additionalProperties === false) {
      for (const key of Object.keys(value)) {
        if (!(key in properties)) fail(`${path}.${key}`, 'additional property is not allowed')
      }
    }
    for (const [key, sub] of Object.entries(properties)) {
      if (key in value) validate(sub, value[key], `${path}.${key}`, root)
    }
  }
}

/** Validate the catalogue manifest and return it. */
export function loadCatalogue(schema, manifest) {
  validate(schema, manifest)
  return manifest
}
