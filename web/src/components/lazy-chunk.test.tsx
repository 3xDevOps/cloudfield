import { cleanup, render, screen } from '@testing-library/react'
import { lazyChunk } from '@/components/lazy-chunk'

describe('lazyChunk', () => {
  const reload = vi.fn()
  beforeEach(() => {
    reload.mockClear()
    window.sessionStorage.clear()
    vi.stubGlobal('location', { ...window.location, reload })
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  const gone = () => lazyChunk<object>(() => Promise.reject(new Error('Failed to load chunk /_next/static/chunks/gone.js')), 'Loading')

  it('renders the component once its code arrives', async () => {
    const Loaded = lazyChunk(async () => ({ name }: { name: string }) => <p>{name} loaded</p>, 'Loading')
    render(<Loaded name="editor" />)

    expect(screen.getByText('Loading')).toBeDefined()
    expect(await screen.findByText('editor loaded')).toBeDefined()
    expect(reload).not.toHaveBeenCalled()
  })

  it('reloads the page once when the code is gone, then shows the real error', async () => {
    const First = gone()
    render(<First />)
    expect((await screen.findByRole('alert')).textContent).toBe('Failed to load chunk /_next/static/chunks/gone.js')
    expect(reload).toHaveBeenCalledTimes(1)
    cleanup()

    const AfterReload = gone()
    render(<AfterReload />)
    expect((await screen.findByRole('alert')).textContent).toBe('Failed to load chunk /_next/static/chunks/gone.js')
    expect(reload).toHaveBeenCalledTimes(1)
  })
})
