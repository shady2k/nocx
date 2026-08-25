/**
 * The Postman v2.1 export for pathvar-7731.
 *
 * The request owns `id` through `url.variable`; there is deliberately no
 * collection-level `variable` list, so importing this fixture does not create
 * or select an environment. Postman's `:id` spelling is rewritten by the
 * importer to the request model's `{{id}}` spelling.
 */

/** The value the request-local `:id` variable must resolve to on send. */
export const PATH_VARIABLE_ID = 'e2e-request-path-id-42'

/** The name Postman's `info.name` carries, and therefore the collection's. */
export const PATH_VARIABLE_COLLECTION_NAME = 'request-path-variable-api'

/** The item name — the row a person clicks in the workbench tree. */
export const PATH_VARIABLE_REQUEST_NAME = 'get item by id'

/** The export document pasted into the import ask. */
export function pathVariableExport(baseUrl: string): string {
  const base = new URL(baseUrl)
  return JSON.stringify(
    {
      info: {
        _postman_id: 'f7b2c9a1-6d4e-48f0-93ab-5c1e8d2f7046',
        name: PATH_VARIABLE_COLLECTION_NAME,
        schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json',
      },
      item: [
        {
          name: PATH_VARIABLE_REQUEST_NAME,
          request: {
            method: 'GET',
            header: [],
            url: {
              raw: `${baseUrl}/users/:id`,
              host: [base.hostname],
              port: base.port || undefined,
              path: ['users', ':id'],
              variable: [{ key: 'id', value: PATH_VARIABLE_ID, type: 'string' }],
            },
          },
          response: [],
        },
      ],
    },
    null,
    2,
  )
}
