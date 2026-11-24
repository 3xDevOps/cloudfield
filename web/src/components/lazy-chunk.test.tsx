import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { lazyChunk, preloadChunks } from '@/components/lazy-chunk'

describe('lazyChunk', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('shows the placeholder until the code arrives', async () => {
    const Loaded = lazyChunk(async () => ({ name }: { name: string }) => <p>{name} loaded</p>, 'Loading')
    render(<Loaded name="editor" />)

    expect(screen.getByText('Loading')).toBeDefined()
    expect(await screen.findByText('editor loaded')).toBeDefined()
  })

  it('fetches the code once, ahead of the first render', async () => {
    const load = vi.fn(async () => () => <p>charts loaded</p>)
    const Loaded = lazyChunk(load, 'Loading')
    preloadChunks()
    preloadChunks()
    await Promise.resolve()
    render(<Loaded />)

    expect(await screen.findByText('charts loaded')).toBeDefined()
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('keeps a failed early fetch quiet, then shows the real error with a Reload button', async () => {
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const unhandled = vi.fn()
    process.on('unhandledRejection', unhandled)
    const Gone = lazyChunk<object>(() => Promise.reject(new Error('Failed to load chunk /_next/static/chunks/gone.js')), 'Loading')

    preloadChunks()
    await new Promise((resolve) => setTimeout(resolve))
    process.off('unhandledRejection', unhandled)
    expect(unhandled).not.toHaveBeenCalled()
    expect(reload).not.toHaveBeenCalled()

    render(<Gone />)
    expect((await screen.findByRole('alert')).textContent).toContain('Failed to load chunk /_next/static/chunks/gone.js')
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })
})
