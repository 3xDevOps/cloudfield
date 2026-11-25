import { render, screen } from '@testing-library/react'
import { RunLinearIssue } from '@/routes/run/linear-issue'
import { useStore } from '@/store'
import { fakeApi } from '@/test/fixtures'

const run = { linear_issue: 'ENG-123', linear_issue_url: 'https://linear.app/acme/issue/ENG-123' }

describe('run header Linear link', () => {
  beforeEach(() => useStore.setState({ linear: null }))

  it('links the identifier to Linear in a new tab without reading a status when no key is saved', () => {
    const client = fakeApi()
    useStore.setState({ linear: { connected: false } })
    render(<RunLinearIssue run={run} client={client} />)

    const link = screen.getByRole('link', { name: 'Linear issue ENG-123' })
    expect(link.getAttribute('href')).toBe(run.linear_issue_url)
    expect(link.getAttribute('target')).toBe('_blank')
    expect(client.linearIssue).not.toHaveBeenCalled()
  })

  it('shows the issue status when a key is saved', async () => {
    useStore.setState({ linear: { connected: true } })
    const client = fakeApi({ linearIssue: vi.fn(async () => ({ identifier: 'ENG-123', title: 't', url: run.linear_issue_url, state: { name: 'In Progress', type: 'started' } })) })
    render(<RunLinearIssue run={run} client={client} />)

    expect((await screen.findByRole('link', { name: 'Linear issue ENG-123, In Progress' })).textContent).toContain('In Progress')
    expect(client.linearIssue).toHaveBeenCalledWith('ENG-123')
  })

  it('reads no status in the compact header, which shows none', () => {
    useStore.setState({ linear: { connected: true } })
    const client = fakeApi()
    render(<RunLinearIssue run={run} client={client} compact />)

    expect(screen.getByRole('link', { name: 'Linear issue ENG-123' }).textContent).toBe('ENG-123')
    expect(client.linearIssue).not.toHaveBeenCalled()
  })

  it('shows the status in the details row as one link titled with the issue title', async () => {
    useStore.setState({ linear: { connected: true } })
    const client = fakeApi({ linearIssue: vi.fn(async () => ({ identifier: 'ENG-123', title: 'Fix rounding', url: run.linear_issue_url, state: { name: 'In Progress', type: 'started' } })) })
    render(<RunLinearIssue run={run} client={client} details />)

    const link = await screen.findByRole('link', { name: 'Linear issue ENG-123, In Progress' })
    expect(link.textContent).toBe('ENG-123In Progress')
    expect(link.getAttribute('title')).toBe('Fix rounding')
  })

  it('never reads an issue with an empty identifier', () => {
    useStore.setState({ linear: { connected: true } })
    const client = fakeApi()
    render(<RunLinearIssue run={{ linear_issue: '', linear_issue_url: run.linear_issue_url }} client={client} />)

    expect(client.linearIssue).not.toHaveBeenCalled()
  })

  it('shows no status and puts Linear\'s error in the tooltip when the lookup fails', async () => {
    useStore.setState({ linear: { connected: true } })
    const client = fakeApi({ linearIssue: vi.fn(async () => { throw new Error('Entity not found: Issue') }) })
    render(<RunLinearIssue run={run} client={client} />)

    const link = await screen.findByTitle('Linear status unavailable: Entity not found: Issue')
    expect(link.textContent).toBe('ENG-123')
  })

  it('still looks the issue up when linear.status failed, because a key is saved', async () => {
    useStore.setState({ linear: { connected: true, error: 'Authentication required' } })
    const client = fakeApi({ linearIssue: vi.fn(async () => { throw new Error('Authentication required') }) })
    render(<RunLinearIssue run={run} client={client} />)

    expect((await screen.findByTitle('Linear status unavailable: Authentication required')).textContent).toBe('ENG-123')
  })

  it('renders nothing for a session without an issue', () => {
    const { container } = render(<RunLinearIssue run={{}} client={fakeApi()} />)
    expect(container.textContent).toBe('')
  })
})
