/**
 * The Postman v2.1 export the API-testing e2e imports.
 *
 * Built rather than checked in as a .json file, and the reason is the port:
 * the test server binds port 0 so nothing collides, so `baseUrl` is not
 * knowable until the run is under way. A committed fixture would have to
 * hard-code a port, which is the arrangement that made six specs fight over
 * six numbers (nocx-z9s9.11).
 *
 * The shape is Postman's own v2.1 export, not ours: `info.schema` names the
 * collection schema, collection-level `variable` carries `baseUrl`,
 * collection-level `auth` carries the bearer token, and the one item spells
 * its URL `{{baseUrl}}/users`. That combination is the whole scenario in one
 * document — a URL that CANNOT be sent without an environment, and a
 * credential that must not end up in a file.
 */

/**
 * The live credential in the export.
 *
 * It is the value the walk in the spec proves is absent from every file
 * under the collection root, and the value the test server demands before it
 * answers 201 — so it has to be one string in one place. Long and
 * distinctive so a substring search cannot match it by accident, and so a
 * partial leak (a prefix written into a file) is still a hit.
 */
export const POSTMAN_BEARER_TOKEN = 'e2e-api-testing-token-c4b7d9e35a60218f21ab'

/** The name Postman's `info.name` carries, and therefore the collection's. */
export const POSTMAN_COLLECTION_NAME = 'acme-api'

/** The item name, which is the row a person clicks in the workbench tree. */
export const POSTMAN_REQUEST_NAME = 'create user'

/** The request body the export declares, echoed back by the assertions. */
export const POSTMAN_REQUEST_BODY = '{"email":"a@b.c","name":"A"}'

/** The export document, as the text written to disk. */
export function postmanExport(baseUrl: string): string {
  return JSON.stringify(
    {
      info: {
        _postman_id: 'b0f2a1d4-7c3e-4f11-9a52-6d8e0c1b3f47',
        name: POSTMAN_COLLECTION_NAME,
        schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json',
      },
      auth: {
        type: 'bearer',
        bearer: [{ key: 'token', value: POSTMAN_BEARER_TOKEN, type: 'string' }],
      },
      variable: [{ key: 'baseUrl', value: baseUrl, type: 'string' }],
      item: [
        {
          name: POSTMAN_REQUEST_NAME,
          request: {
            method: 'POST',
            header: [{ key: 'Content-Type', value: 'application/json' }],
            body: { mode: 'raw', raw: POSTMAN_REQUEST_BODY },
            url: {
              raw: '{{baseUrl}}/users',
              host: ['{{baseUrl}}'],
              path: ['users'],
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
