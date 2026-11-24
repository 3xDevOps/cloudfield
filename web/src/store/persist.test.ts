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

  it('writes a preference set back after another tab changed it and this one read it again', async () => {
    const store = createRootStore()
    store.getState().setMineOnly(true)
    const theirs = stored()
    localStorage.setItem('aether.ui', JSON.stringify({ ...theirs, state: { ...theirs.state, mineOnly: false } }))
    await store.persist.rehydrate()
    expect(store.getState().mineOnly).toBe(false)

    store.getState().setMineOnly(true)
    expect(stored().state.mineOnly).toBe(true)
  })
})
