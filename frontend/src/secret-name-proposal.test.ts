import { describe, expect, it } from 'vitest'
import type { ProposalContext, SecretProposal } from './secret-name-proposal'
import type { VaultSecretKind } from './vault-client'
import { proposeSecret } from './secret-name-proposal'

const url = 'https://api.example.com/v1/users'

describe('proposeSecret — the kind from the standing site', () => {
  it.each([
    {
      name: 'auth bearer maps to api-token',
      context: { site: { at: 'auth', scheme: 'bearer' }, url },
      kind: 'api-token',
    },
    {
      name: 'auth basic maps to password',
      context: { site: { at: 'auth', scheme: 'basic' }, url },
      kind: 'password',
    },
    {
      name: 'header maps to api-token',
      context: { site: { at: 'header', name: 'X-Api-Key' }, url },
      kind: 'api-token',
    },
  ] satisfies Array<{ name: string; context: ProposalContext; kind: VaultSecretKind }>)(
    '$name',
    ({ context, kind }) => {
      expect(proposeSecret(context).kind).toBe(kind)
    },
  )
})

describe('proposeSecret — the name from the standing site and where', () => {
  it.each([
    {
      name: 'header uses its own name on an absolute URL',
      context: { site: { at: 'header', name: 'X-Api-Key' }, url },
      proposal: { name: 'X-Api-Key for api.example.com', kind: 'api-token' },
    },
    {
      name: 'query uses its own name on an absolute URL',
      context: { site: { at: 'query', name: 'api_key' }, url },
      proposal: { name: 'api_key for api.example.com', kind: 'api-token' },
    },
    {
      name: 'auth bearer puts the where before token',
      context: { site: { at: 'auth', scheme: 'bearer' }, url },
      proposal: { name: 'api.example.com token', kind: 'api-token' },
    },
    {
      name: 'auth basic puts the where before password',
      context: { site: { at: 'auth', scheme: 'basic' }, url },
      proposal: { name: 'api.example.com password', kind: 'password' },
    },
    {
      name: 'a scheme-less host is still the where',
      context: { site: { at: 'header', name: 'X-Api-Key' }, url: 'api.example.com/users' },
      proposal: { name: 'X-Api-Key for api.example.com', kind: 'api-token' },
    },
    {
      name: 'a port and path never enter the where',
      context: {
        site: { at: 'query', name: 'api_key' },
        url: 'https://api.example.com:8443/v1/users',
      },
      proposal: { name: 'api_key for api.example.com', kind: 'api-token' },
    },
    {
      name: 'an unresolved host variable falls back to the folder',
      context: {
        site: { at: 'auth', scheme: 'bearer' },
        url: '{{baseUrl}}/v1/users',
        folder: 'tinkoff',
      },
      proposal: { name: 'tinkoff token', kind: 'api-token' },
    },
    {
      name: 'without a folder the unresolved host falls back to the request',
      context: {
        site: { at: 'auth', scheme: 'basic' },
        url: '{{baseUrl}}/v1/users',
        request: 'Create user',
      },
      proposal: { name: 'Create user password', kind: 'password' },
    },
    {
      name: 'without any where the site word stands alone',
      context: { site: { at: 'header', name: 'X-Api-Key' }, url: '{{baseUrl}}/v1/users' },
      proposal: { name: 'X-Api-Key', kind: 'api-token' },
    },
    {
      name: 'an entirely variable URL has no host',
      context: {
        site: { at: 'auth', scheme: 'bearer' },
        url: '{{baseUrl}}',
        resolveVariable: () => 'api.example.com',
        folder: 'tinkoff',
      },
      proposal: { name: 'tinkoff token', kind: 'api-token' },
    },
    {
      name: 'a host variable resolved by the environment becomes the where',
      context: {
        site: { at: 'query', name: 'api_key' },
        url: '{{baseUrl}}/v1/users',
        resolveVariable: (name: string) => (name === 'baseUrl' ? 'api.example.com' : undefined),
      },
      proposal: { name: 'api_key for api.example.com', kind: 'api-token' },
    },
  ] satisfies Array<{ name: string; context: ProposalContext; proposal: SecretProposal }>)(
    '$name',
    ({ context, proposal }) => {
      expect(proposeSecret(context)).toEqual(proposal)
    },
  )
})

describe('proposeSecret — occupied names', () => {
  it('keeps the first proposal unchanged when its name is free', () => {
    const context = {
      site: { at: 'auth', scheme: 'bearer' },
      url,
      occupiedNames: ['other secret'],
    } as ProposalContext

    expect(proposeSecret(context).name).toBe('api.example.com token')
  })

  it('adds the first free numeric suffix when the proposal is occupied', () => {
    const context = {
      site: { at: 'auth', scheme: 'bearer' },
      url,
      occupiedNames: ['api.example.com token'],
    } as ProposalContext

    expect(proposeSecret(context).name).toBe('api.example.com token 2')
  })
})

describe('proposeSecret — metadata purity', () => {
  it('takes one metadata context parameter and is deterministic', () => {
    expect(proposeSecret.length).toBe(1)

    const context: ProposalContext = {
      site: { at: 'header', name: 'X-Api-Key' },
      url: 'https://api.example.com/v1/users',
      request: 'sk-live',
    }
    const first = proposeSecret(context)
    const second = proposeSecret({ ...context, site: { ...context.site } })

    expect(first).toEqual(second)
    expect(first.name).toBe('X-Api-Key for api.example.com')
    expect(first.name).not.toContain('sk-live')
  })
})
