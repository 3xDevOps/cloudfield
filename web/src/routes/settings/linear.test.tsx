import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { LinearSection } from '@/routes/settings/linear'
import { useStore } from '@/store'
import { fakeApi } from '@/test/fixtures'

describe('Settings Linear section', () => {
  beforeEach(() => useStore.setState({ linear: null, capabilities: { gateway: 'test', methods: ['*'], ws: [] } }))

  it('saves a key through linear.connect and shows who it connected', async () => {
    const linearConnect = vi.fn(async () => ({ connected: true, name: 'Ada Lovelace', organization: 'Acme' }))
    render(<LinearSection client={fakeApi({ linearConnect })} />)

    const input = await screen.findByPlaceholderText('lin_api_…')
    expect(input.getAttribute('type')).toBe('password')
    fireEvent.change(input, { target: { value: ' lin_api_abc ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await screen.findByText('Connected as Ada Lovelace (Acme)')
    expect(linearConnect).toHaveBeenCalledWith('lin_api_abc')
    expect(useStore.getState().linear?.connected).toBe(true)
  })

  it('shows Linear\'s refusal verbatim and stays disconnected', async () => {
    const linearConnect = vi.fn(async () => { throw new Error('Authentication required, not authenticated') })
    render(<LinearSection client={fakeApi({ linearConnect })} />)

    fireEvent.change(await screen.findByPlaceholderText('lin_api_…'), { target: { value: 'lin_api_bad' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect((await screen.findByRole('alert')).textContent).toBe('Authentication required, not authenticated')
    expect(useStore.getState().linear).toEqual({ connected: false })
  })

  it('opens the Linear page from the connected row', async () => {
    render(<LinearSection client={fakeApi({ linearStatus: vi.fn(async () => ({ connected: true, name: 'Ada Lovelace' })) })} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Open Linear' }))

    expect(useStore.getState().route).toMatchObject({ name: 'linear' })
  })

  it('disconnects a saved key', async () => {
    const client = fakeApi({ linearStatus: vi.fn(async () => ({ connected: true, name: 'Ada Lovelace', organization: 'Acme' })) })
    render(<LinearSection client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }))

    await waitFor(() => expect(screen.getByPlaceholderText('lin_api_…')).toBeTruthy())
    expect(client.linearDisconnect).toHaveBeenCalled()
    expect(useStore.getState().linear).toEqual({ connected: false })
  })

  it('keeps Disconnect and shows Linear\'s error when the saved key is refused', async () => {
    const client = fakeApi({ linearStatus: vi.fn(async () => { throw new Error('Authentication required, not authenticated') }) })
    render(<LinearSection client={client} />)

    expect((await screen.findByRole('alert')).textContent).toBe('Authentication required, not authenticated')
    expect(screen.queryByPlaceholderText('lin_api_…')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Disconnect' }))

    await waitFor(() => expect(screen.getByPlaceholderText('lin_api_…')).toBeTruthy())
    expect(client.linearDisconnect).toHaveBeenCalled()
  })

  it('shows no key form while the status is loading', () => {
    render(<LinearSection client={fakeApi({ linearStatus: vi.fn(() => new Promise<never>(() => {})) })} />)

    expect(screen.queryByPlaceholderText('lin_api_…')).toBeNull()
  })

  it('renders nothing against a server without linear.status', () => {
    useStore.setState({ capabilities: { gateway: 'test', methods: ['run.list'], ws: [] } })
    const client = fakeApi()
    const { container } = render(<LinearSection client={client} />)

    expect(container.textContent).toBe('')
    expect(client.linearStatus).not.toHaveBeenCalled()
  })
})
