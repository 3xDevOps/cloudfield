import { createRootStore } from '@/store'

const stored = () => JSON.parse(localStorage.getItem('aether.ui')!)

describe('aether.ui persistence', () => {
  it('writes once after loading, then only when a persisted preference changes, and then at once', () => {
    const store = createRootStore()
    const setItem = vi.spyOn(Storage.prototype, 'setItem')

    for (let i = 0; i < 50; i++) store.setState({ hydrated: i % 2 === 0 })
    expect(setItem).toHaveBeenCalledTimes(1)

    store.getState().setMineOnly(true)
    expect(setItem).toHaveBeenCalledTimes(2)
    expect(stored().state.mineOnly).toBe(true)
  })

  it('retries a preference write the browser refused', () => {
    const store = createRootStore()
    vi.spyOn(Storage.prototype, 'setItem').mockImplementationOnce(() => {
      throw new DOMException('full', 'QuotaExceededError')
    })
    expect(() => store.getState().setMineOnly(true)).toThrow('full')

    store.setState({ hydrated: true })
    expect(stored().state.mineOnly).toBe(true)
  })
})
