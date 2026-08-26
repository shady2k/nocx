import { fireEvent, waitFor } from '@solidjs/testing-library'

/** Drive a bound SecretSource picker as a person does: focus, narrow by label,
 * and commit the visible option with a click. */
export async function chooseBoundSuggestion(
  input: HTMLInputElement,
  label: string,
  root: ParentNode = document,
): Promise<void> {
  input.focus()
  fireEvent.input(input, { target: { value: label } })
  const option = await waitFor(() => {
    const offered = Array.from(root.querySelectorAll<HTMLElement>('[role="option"]')).find(
      (candidate) => candidate.textContent?.trim() === label,
    )
    if (!offered) throw new Error(`bound suggestion ${label} is not offered`)
    return offered
  })
  fireEvent.mouseDown(option)
  fireEvent.click(option)
  await waitFor(() => {
    if (input.value !== label) throw new Error(`bound suggestion ${label} was not committed`)
  })
}
