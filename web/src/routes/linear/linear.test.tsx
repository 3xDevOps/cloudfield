import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { LinearIssue } from '@/lib/types'
import { LinearRoute } from '@/routes/linear'
import { linearTask, linearTone } from '@/routes/linear/issues'
import { useStore } from '@/store'
import { fakeApi } from '@/test/fixtures'

const issue: LinearIssue = {
  identifier: 'ENG-123',
  title: 'Fix checkout total rounding',
  url: 'https://linear.app/acme/issue/ENG-123/fix-checkout',
  state: { name: 'In Progress', type: 'started' },
  team: { key: 'ENG', name: 'Engineering' },
  assignee: { name: 'Ada Lovelace' },
  updated_at: '2026-11-25T10:00:00Z',
}

function renderPage(over: Partial<Api> = {}) {
  const client = fakeApi({ linearIssues: vi.fn(async () => [issue]), linearTeams: vi.fn(async () => [{ key: 'ENG', name: 'Engineering' }]), ...over })
  render(<LinearRoute params={{}} client={client} />)
  return client
}

describe('linear page', () => {
  beforeEach(() => useStore.setState({ runs: {}, linear: null }))

  it('lists the issues with a Start session button that opens the launch with the issue linked', async () => {
    renderPage()
    await screen.findByText(issue.title)
    expect(screen.getByRole('link', { name: /Open in Linear/ }).getAttribute('href')).toBe(issue.url)

    fireEvent.click(screen.getByRole('button', { name: 'Start session' }))

    const state = useStore.getState()
    expect(state.paletteDialog).toBe('launch')
    expect(state.paletteLaunchTask).toBe('ENG-123 Fix checkout total rounding')
    expect(state.paletteLaunchLinear).toEqual({ identifier: 'ENG-123', url: issue.url })
  })

  it('offers Open session instead of Start session for an issue with a live linked session', async () => {
    useStore.setState({ runs: { 'run-1': { id: 'run-1', status: 'running', linear_issue: 'ENG-123' } as never } })
    renderPage()
    await screen.findByText(issue.title)

    fireEvent.click(screen.getByRole('button', { name: 'Open session' }))

    expect(screen.queryByRole('button', { name: 'Start session' })).toBeNull()
    expect(useStore.getState().route).toMatchObject({ name: 'run', params: { runId: 'run-1' } })
  })

  it('sends the typed words and the chosen team to linear.issues', async () => {
    const linearIssues = vi.fn(async () => [issue])
    renderPage({ linearIssues })
    await screen.findByText(issue.title)

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: ' rounding ' } })

    await waitFor(() => expect(linearIssues).toHaveBeenLastCalledWith({ query: 'rounding' }))
  })

  it('sends the chosen team to linear.issues', async () => {
    const linearIssues = vi.fn(async () => [issue])
    renderPage({ linearIssues })
    await screen.findByText(issue.title)

    fireEvent.click(screen.getByRole('combobox', { name: 'Team' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Engineering' }))

    await waitFor(() => expect(linearIssues).toHaveBeenLastCalledWith({ team: 'ENG' }))
  })

  it('shows the teams error instead of silently offering only All teams', async () => {
    renderPage({ linearTeams: vi.fn(async () => { throw new Error('Rate limited') }) })

    expect((await screen.findByRole('alert')).textContent).toContain('Rate limited')
  })

  it('keeps the search box on an issues error and retries', async () => {
    const linearIssues = vi.fn<Api['linearIssues']>()
      .mockRejectedValueOnce(new Error('Rate limited'))
      .mockResolvedValue([issue])
    renderPage({ linearIssues })

    expect((await screen.findByRole('alert')).textContent).toContain('Rate limited')
    expect(screen.getByRole('searchbox')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    await screen.findByText(issue.title)
  })

  it('shows one card when teams and issues both fail', async () => {
    const error = new Error('linear: not connected')
    renderPage({ linearTeams: vi.fn(async () => { throw error }), linearIssues: vi.fn(async () => { throw error }) })

    await screen.findByText('Could not load issues')

    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(screen.queryByText('Could not load teams')).toBeNull()
  })

  it('offers Settings, not Retry or filters, to a member with no key', async () => {
    useStore.setState({ linear: { connected: false } })
    renderPage({ linearIssues: vi.fn(async () => { throw new Error('linear: not connected') }) })

    await screen.findByText('Linear is not connected')

    expect(screen.getByRole('alert').textContent).toContain('linear: not connected')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(screen.queryByRole('searchbox')).toBeNull()
    expect(screen.getByRole('button', { name: 'Open Settings → Linear' })).toBeTruthy()
  })

  it('leads a refused key to Settings, with Retry second and no filters', async () => {
    useStore.setState({ linear: { connected: true, error: 'Authentication required' } })
    renderPage({ linearIssues: vi.fn(async () => { throw new Error('Authentication required') }) })

    await screen.findByText('Linear refused your API key')

    expect(screen.queryByRole('searchbox')).toBeNull()
    const [first, second] = within(screen.getByRole('alert')).getAllByRole('button')
    expect(first!.textContent).toBe('Open Settings → Linear')
    expect(first!.className).toContain('bg-action')
    expect(second!.textContent).toBe('Retry')
  })

  it('starts over for another identity', async () => {
    useStore.setState({ identityKey: 'alice' })
    const client = renderPage()
    await screen.findByText(issue.title)
    expect(client.linearIssues).toHaveBeenCalledTimes(1)

    act(() => useStore.setState({ identityKey: 'bob' }))

    await waitFor(() => expect(client.linearIssues).toHaveBeenCalledTimes(2))
  })

  it('shows Linear\'s own error with a button to Settings', async () => {
    const error = 'Linear is not connected: save a personal API key in Settings → Connections → Linear'
    renderPage({ linearIssues: vi.fn(async () => { throw new Error(error) }) })

    expect((await screen.findByRole('alert')).textContent).toContain(error)
    fireEvent.click(screen.getByRole('button', { name: 'Open Settings → Linear' }))
    expect(useStore.getState().route).toMatchObject({ name: 'settings', params: { section: 'linear' } })
  })
})

describe('linear issue helpers', () => {
  it('words the task as the identifier and title, with no URL the server could link again', () => {
    expect(linearTask(issue)).toBe('ENG-123 Fix checkout total rounding')
  })

  it('colors a status by its type, with review as its own tone', () => {
    expect(linearTone({ name: 'Todo', type: 'unstarted' })).toBe('neutral')
    expect(linearTone({ name: 'In Progress', type: 'started' })).toBe('working')
    expect(linearTone({ name: 'In Review', type: 'started' })).toBe('review')
    expect(linearTone({ name: 'Done', type: 'completed' })).toBe('done')
    expect(linearTone({ name: 'Canceled', type: 'canceled' })).toBe('closed')
  })
})
