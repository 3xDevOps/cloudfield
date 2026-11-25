import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError, codeUnavailable, type Api } from '@/lib/api'
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

  it('opens the Linear page from View issues', async () => {
    render(<LinearSection client={fakeApi({ linearStatus: vi.fn(async () => ({ connected: true, name: 'Ada Lovelace' })) })} />)

    fireEvent.click(await screen.findByRole('button', { name: 'View issues' }))

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

  it('offers Replace key beside Disconnect, in the danger style, when Linear refuses the saved key', async () => {
    const linearConnect = vi.fn(async () => ({ connected: true, name: 'Ada Lovelace' }))
    const client = fakeApi({ linearConnect, linearStatus: vi.fn(async () => { throw new ApiError(500, 'linear: Linear refused the API key: Authentication required, not authenticated', -32603) }) })
    render(<LinearSection client={client} />)

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe('linear: Linear refused the API key: Authentication required, not authenticated')
    expect(alert.getAttribute('data-tone')).toBe('failed')
    expect(screen.getByText('Linear refused your saved key')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
    fireEvent.change(screen.getByPlaceholderText('lin_api_…'), { target: { value: 'lin_api_new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Replace key' }))

    await screen.findByText('Connected as Ada Lovelace')
    expect(linearConnect).toHaveBeenCalledWith('lin_api_new')
  })

  it('disconnects a key Linear refuses', async () => {
    const client = fakeApi({ linearStatus: vi.fn(async () => { throw new ApiError(500, 'linear: Linear refused the API key: Authentication required', -32603) }) })
    render(<LinearSection client={client} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }))

    await waitFor(() => expect(client.linearDisconnect).toHaveBeenCalled())
  })

  it('shows the real error with Retry, and no Disconnect, when the service is not configured', async () => {
    const linearStatus = vi.fn<Api['linearStatus']>()
      .mockRejectedValueOnce(new ApiError(500, 'linear service not configured', codeUnavailable))
      .mockResolvedValue({ connected: false })
    render(<LinearSection client={fakeApi({ linearStatus })} />)

    expect((await screen.findByRole('alert')).textContent).toBe('linear service not configured')
    expect(screen.queryByRole('button', { name: 'Disconnect' })).toBeNull()
    expect(useStore.getState().linear?.connected).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    await screen.findByPlaceholderText('lin_api_…')
  })

  it('keeps Disconnect and Replace key, and offers Retry, when Linear cannot be reached with a key saved', async () => {
    const error = 'linear: Post "https://api.linear.app/graphql": context deadline exceeded'
    const linearStatus = vi.fn<Api['linearStatus']>()
      .mockRejectedValueOnce(new ApiError(500, error, -32603))
      .mockResolvedValue({ connected: true, name: 'Ada Lovelace' })
    render(<LinearSection client={fakeApi({ linearStatus })} />)

    expect((await screen.findByRole('alert')).textContent).toBe(error)
    expect(screen.getByText('Could not reach Linear')).toBeTruthy()
    expect(screen.queryByText('Linear refused your saved key')).toBeNull()
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Replace key' })).toBeTruthy()
    expect(useStore.getState().linear).toMatchObject({ connected: true, refused: false })
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    await screen.findByText('Connected as Ada Lovelace')
  })

  it('assumes no key, and says the key is unchanged, when a gateway answers 401 without the server\'s Linear text', async () => {
    const client = fakeApi({ linearStatus: vi.fn(async () => { throw new ApiError(401, 'linear.status: 401 Unauthorized') }) })
    render(<LinearSection client={client} />)

    expect((await screen.findByRole('alert')).textContent).toBe('linear.status: 401 Unauthorized')
    expect(screen.getByText('Cloudfield could not check the connection. Your key is unchanged.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Disconnect' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Replace key' })).toBeNull()
    expect(useStore.getState().linear?.connected).toBe(false)
  })

  it('keeps gold for Save: View issues and Retry are secondary', async () => {
    render(<LinearSection client={fakeApi({ linearStatus: vi.fn(async () => ({ connected: true, name: 'Ada Lovelace' })) })} />)

    expect((await screen.findByRole('button', { name: 'View issues' })).className).not.toContain('bg-action')
  })

  it('drops a Connect that finishes after the identity changed', async () => {
    useStore.setState({ identityKey: 'alice' })
    let finish: (s: { connected: boolean }) => void = () => {}
    const linearConnect = vi.fn(() => new Promise<{ connected: boolean }>((resolve) => { finish = resolve }))
    render(<LinearSection client={fakeApi({ linearConnect, linearStatus: vi.fn(async () => ({ connected: false })) })} />)

    fireEvent.change(await screen.findByPlaceholderText('lin_api_…'), { target: { value: 'lin_api_abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    act(() => useStore.setState({ identityKey: 'bob', linear: null }))
    await act(async () => finish({ connected: true }))

    expect(useStore.getState().linear?.connected).not.toBe(true)
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
