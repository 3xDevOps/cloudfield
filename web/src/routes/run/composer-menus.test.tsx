import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ConfigOption } from '@/lib/session-types'
import { OptionPills } from '@/routes/run/composer-menus'

const mode: ConfigOption = { id: 'mode', name: 'Mode', category: 'mode', type: 'select', currentValue: 'plan', options: [{ value: 'plan', name: 'Plan mode' }, { value: 'default', name: 'Default' }] }
const model: ConfigOption = { id: 'model', name: 'Model', category: 'model', type: 'select', currentValue: 'fable', options: [{ value: 'fable', name: 'Fable 5.2' }, { value: 'opus', name: 'Opus 5.5' }] }
const effort: ConfigOption = { id: 'effort', name: 'Effort', category: 'thought_level', type: 'select', currentValue: 'high', options: [{ value: 'low', name: 'Low' }, { value: 'high', name: 'High', description: 'Thinks longer' }] }

function mount(options: ConfigOption[], onSet = vi.fn(async () => {})) {
  render(<OptionPills options={options} disabled={false} onSet={onSet} />)
  return onSet
}

describe('OptionPills', () => {
  it('names the model and the effort on one pill', () => {
    mount([effort, model, mode])
    expect(screen.getAllByRole('button').map((button) => button.getAttribute('aria-label'))).toEqual(['Mode: Plan mode', 'Model: Fable 5.2, Effort: High'])
    expect(screen.getByRole('button', { name: 'Model: Fable 5.2, Effort: High' }).textContent).toBe('Fable 5.2/High')
  })

  it('sets the effort from the model picker, which stays open', async () => {
    const settle = Promise.withResolvers<void>()
    const onSet = mount([model, effort], vi.fn(() => settle.promise))
    await userEvent.click(screen.getByRole('button', { name: /^Model/ }))
    expect(screen.getByRole('radio', { name: 'High' }).getAttribute('data-state')).toBe('checked')
    await userEvent.click(screen.getByRole('radio', { name: 'Low' }))
    expect(onSet).toHaveBeenCalledExactlyOnceWith(effort, 'low')
    await userEvent.click(screen.getByRole('option', { name: /^Opus 5.5/ }))
    expect(onSet).toHaveBeenCalledOnce()
    settle.resolve()
    await waitFor(() => expect(screen.getByRole('button', { name: /^Model/ }).getAttribute('aria-busy')).toBeNull())
    expect(screen.getByLabelText('Search Model')).toBeDefined()
  })

  it('sends the last effort picked while another is on its way', async () => {
    const first = Promise.withResolvers<void>()
    const onSet = mount([model, effort], vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(undefined))
    await userEvent.click(screen.getByRole('button', { name: /^Model/ }))
    await userEvent.click(screen.getByRole('radio', { name: 'Low' }))
    await userEvent.click(screen.getByRole('radio', { name: 'High' }))
    expect(onSet).toHaveBeenCalledOnce()
    first.resolve()
    await waitFor(() => expect(onSet).toHaveBeenCalledTimes(2))
    expect(onSet).toHaveBeenLastCalledWith(effort, 'high')
  })

  it('moves between effort levels with the arrow keys and sets one on Space', async () => {
    const onSet = mount([model, effort])
    await userEvent.click(screen.getByRole('button', { name: /^Model/ }))
    screen.getByRole('radio', { name: 'High' }).focus()
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Low' }))
    expect(onSet).not.toHaveBeenCalled()
    await userEvent.keyboard(' ')
    expect(onSet).toHaveBeenCalledExactlyOnceWith(effort, 'low')
  })

  it('sets the model and closes', async () => {
    const onSet = mount([model, effort])
    await userEvent.click(screen.getByRole('button', { name: /^Model/ }))
    await userEvent.click(screen.getByRole('option', { name: /^Opus 5.5/ }))
    expect(onSet).toHaveBeenCalledExactlyOnceWith(model, 'opus')
    await waitFor(() => expect(screen.queryByLabelText('Search Model')).toBeNull())
  })

  it('keeps the effort on its own pill when the agent offers no model option', () => {
    mount([effort])
    expect(screen.getByRole('button', { name: 'Effort: High' }).textContent).toBe('High')
  })
})
