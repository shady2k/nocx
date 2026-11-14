// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { RolesSection } from './roles-section'
import { EndpointClient } from './endpoints'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
afterEach(cleanup)
function mount(
  model: string | null = null,
  models = [
    { id: 'pi/a', name: 'Alpha' },
    { id: 'pi/b', name: 'Beta' },
  ],
) {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  const call = vi.spyOn(dispatcher, 'call').mockImplementation((method, params) => {
    if (method === 'roles.assign') model = (params as { model: string | null }).model
    return Promise.resolve({ agentId: 'pi', roles: [{ role: 'answering', model }] })
  })
  render(() => (
    <RolesSection client={new EndpointClient(dispatcher)} agentId="pi" models={models} />
  ))
  return call
}
it('offers the agent catalogue and writes the selected model with its agent', async () => {
  const call = mount()
  const select = await screen.findByRole('combobox', { name: 'Answering' })
  expect((select as HTMLSelectElement).value).toBe('')
  fireEvent.change(select, { target: { value: 'pi/b' } })
  await vi.waitFor(() =>
    expect(call).toHaveBeenCalledWith('roles.assign', {
      agentId: 'pi',
      role: 'answering',
      model: 'pi/b',
    }),
  )
  await vi.waitFor(() => expect(screen.getByRole<HTMLSelectElement>('combobox').value).toBe('pi/b'))
  fireEvent.change(screen.getByRole('combobox'), { target: { value: '' } })
  await vi.waitFor(() =>
    expect(call).toHaveBeenCalledWith('roles.assign', {
      agentId: 'pi',
      role: 'answering',
      model: null,
    }),
  )
})
it('reports a removed model without replacing or writing it', async () => {
  const call = mount('pi/gone')
  await screen.findByRole('alert')
  expect(screen.getByRole('alert').textContent).toContain('pi/gone')
  expect(screen.getByRole<HTMLSelectElement>('combobox').value).toBe('pi/gone')
  expect(call).toHaveBeenCalledTimes(1)
})
it('says when the agent cannot offer a model picker', async () => {
  mount(null, [])
  await screen.findByRole('combobox')
  expect(screen.getByRole<HTMLSelectElement>('combobox').disabled).toBe(false)
  expect(
    screen.getByText('This agent does not offer model choices. It uses its own default.'),
  ).toBeTruthy()
})

it('keeps declared roles visible but does not offer unused roles as working features', async () => {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  vi.spyOn(dispatcher, 'call').mockResolvedValue({
    agentId: 'pi',
    roles: [{ role: 'classifier', model: null }],
  })
  render(() => (
    <RolesSection
      client={new EndpointClient(dispatcher)}
      agentId="pi"
      models={[{ id: 'pi/a', name: 'Alpha' }]}
    />
  ))
  const select = await screen.findByRole('combobox', { name: 'Classifier' })
  expect((select as HTMLSelectElement).disabled).toBe(true)
  expect(screen.getByText('Declared for a future feature; not used in this version.')).toBeTruthy()
})

it('keeps the saved model visible when a write fails and reports the failure', async () => {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  vi.spyOn(dispatcher, 'call').mockImplementation((method) =>
    method === 'roles.assign'
      ? Promise.reject(new Error('store unavailable'))
      : Promise.resolve({ agentId: 'pi', roles: [{ role: 'answering', model: 'pi/a' }] }),
  )
  render(() => (
    <RolesSection
      client={new EndpointClient(dispatcher)}
      agentId="pi"
      models={[
        { id: 'pi/a', name: 'Alpha' },
        { id: 'pi/b', name: 'Beta' },
      ]}
    />
  ))
  const select = await screen.findByRole('combobox', { name: 'Answering' })
  fireEvent.change(select, { target: { value: 'pi/b' } })
  await screen.findByRole('alert')
  expect(screen.getByRole('alert').textContent).toContain('store unavailable')
  expect(screen.getByRole<HTMLSelectElement>('combobox').value).toBe('pi/a')
})

it('allows clearing a removed model even when the catalogue is empty', async () => {
  const call = mount('pi/gone', [])
  const select = await screen.findByRole('combobox', { name: 'Answering' })
  fireEvent.change(select, { target: { value: '' } })
  await vi.waitFor(() =>
    expect(call).toHaveBeenCalledWith('roles.assign', {
      agentId: 'pi',
      role: 'answering',
      model: null,
    }),
  )
})
