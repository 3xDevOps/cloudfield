import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ConfigOption } from '@/lib/session-types'
import { OptionPills } from '@/routes/run/composer-menus'

const mode: ConfigOption = { id: 'mode', name: 'Mode', category: 'mode', type: 'select', currentValue: 'plan', options: [{ value: 'plan', name: 'Plan mode' }, { value: 'default', name: 'Default' }] }
const model: ConfigOption = { id: 'model', name: 'Model', category: 'model', type: 'select', currentValue: 'fable', options: [{ value: 'fable', name: 'Fable 5.2' }, { value: 'opus', name: 'Opus 5.5' }] }
const effort: ConfigOption = {
  id: 'effort', name: 'Effort', category: 'thought_level', type: 'select', currentValue: 'high',
  options: [{ value: 'low', name: 'Low', description: 'Answers fast' }, { value: 'medium', name: 'Medium' }, { value: 'high', name: 'High', description: 'Thinks longer' }],
}

function mount(options: ConfigOption[], onSet = vi.fn(async () => true)) {
  render(<OptionPills options={options} disabled={false} onSet={onSet} />)
  return onSet
}

const openPicker = () => userEvent.click(screen.getByRole('button', { name: /^Model/ }))
const level = (name: string) => screen.getByRole('radio', { name })
const description = (name: string) => document.getElementById(level(name).getAttribute('aria-describedby')!)!

describe('OptionPills', () => {
  it('names the model and the effort on one pill', () => {
    mount([effort, model, mode])
    expect(screen.getAllByRole('button').map((button) => button.getAttribute('aria-label'))).toEqual(['Mode: Plan mode', 'Model: Fable 5.2, Effort: High'])
    expect(screen.getByRole('button', { name: 'Model: Fable 5.2, Effort: High' }).textContent).toBe('Fable 5.2/High')
  })

  it('sets the effort from the model picker, which stays open', async () => {
    const settle = Promise.withResolvers<boolean>()
    const onSet = mount([model, effort], vi.fn(() => settle.promise))
    await openPicker()
    expect(level('High').getAttribute('data-state')).toBe('checked')
    expect(description('High').textContent).toBe('Thinks longer')
    expect(level('Medium').getAttribute('aria-describedby')).toBeNull()
    await userEvent.click(level('Low'))
    expect(onSet).toHaveBeenCalledExactlyOnceWith(effort, 'low')
    expect(level('Low').getAttribute('aria-busy')).toBe('true')
    settle.resolve(true)
    await waitFor(() => expect(level('Low').getAttribute('aria-busy')).toBeNull())
    expect(screen.getByLabelText('Search Model')).toBeDefined()
  })

  it('sends the last pick made while another is on its way', async () => {
    const first = Promise.withResolvers<boolean>()
    const onSet = mount([model, effort], vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(true))
    await openPicker()
    await userEvent.click(level('Low'))
    await userEvent.click(level('Medium'))
    await userEvent.click(level('High'))
    expect(onSet).toHaveBeenCalledOnce()
    first.resolve(true)
    await waitFor(() => expect(onSet).toHaveBeenCalledTimes(2))
    expect(onSet).toHaveBeenLastCalledWith(effort, 'high')
  })

  it('drops the waiting pick when the agent refuses the one before it', async () => {
    const first = Promise.withResolvers<boolean>()
    const onSet = mount([model, effort], vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(true))
    await openPicker()
    await userEvent.click(level('Low'))
    await userEvent.click(level('Medium'))
    first.resolve(false)
    await waitFor(() => expect(level('Low').getAttribute('aria-busy')).toBeNull())
    expect(onSet).toHaveBeenCalledOnce()
  })

  it('closes on a model picked while an effort is on its way, and sends it after', async () => {
    const first = Promise.withResolvers<boolean>()
    const onSet = mount([model, effort], vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(true))
    await openPicker()
    await userEvent.click(level('Low'))
    await userEvent.click(screen.getByRole('option', { name: /^Opus 5.5/ }))
    await waitFor(() => expect(screen.queryByLabelText('Search Model')).toBeNull())
    expect(onSet).toHaveBeenCalledOnce()
    first.resolve(true)
    await waitFor(() => expect(onSet).toHaveBeenLastCalledWith(model, 'opus'))
  })

  it('moves between effort levels by key without choosing, and chooses on Space or Enter', async () => {
    const onSet = mount([model, effort])
    await openPicker()
    level('High').focus()
    // Radix chooses on arrow focus only while the key is still down.
    await userEvent.keyboard('{ArrowLeft>}')
    await waitFor(() => expect(document.activeElement).toBe(level('Medium')))
    await userEvent.keyboard('{/ArrowLeft}{Home}{Home}')
    expect(document.activeElement).toBe(level('Low'))
    expect([description('Low').className, description('High').className].map((name) => name.includes('invisible'))).toEqual([false, true])
    expect(onSet).not.toHaveBeenCalled()
    await userEvent.keyboard(' ')
    expect(onSet).toHaveBeenCalledExactlyOnceWith(effort, 'low')
    level('Medium').focus()
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(onSet).toHaveBeenLastCalledWith(effort, 'medium'))
  })

  it('sets the model and closes', async () => {
    const onSet = mount([model, effort])
    await openPicker()
    await userEvent.click(screen.getByRole('option', { name: /^Opus 5.5/ }))
    expect(onSet).toHaveBeenCalledExactlyOnceWith(model, 'opus')
    await waitFor(() => expect(screen.queryByLabelText('Search Model')).toBeNull())
  })

  it('keeps the effort on its own pill when the agent offers no model option', () => {
    mount([effort])
    expect(screen.getByRole('button', { name: 'Effort: High' }).textContent).toBe('High')
  })
})
