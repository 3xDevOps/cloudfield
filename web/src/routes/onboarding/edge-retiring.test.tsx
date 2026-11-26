import { fireEvent, render, screen } from '@testing-library/react'
import type { EdgeNotice, EdgeStatus, GatewayCapabilities, LinkStatus, ServerEdge } from '@/lib/types'
import { forgetHandedLink } from '@/lib/url-state'
import { OnboardingRoute } from '@/routes/onboarding'
import { useStore } from '@/store'
import { fakeApi, serverInfo, workspace } from '@/test/fixtures'

const edge = 'https://old.example.test'
const next = 'https://new.example.test'

const localCaps: GatewayCapabilities = {
  gateway: 'local',
  methods: ['*'],
  ws: ['events', 'attach', 'terminal'],
  local: ['link.status', 'edge.login', 'edge.status', 'edge.servers', 'edge.hostkey', 'edge.link', 'edge.claim', 'edge.join'],
}

const unlinked: LinkStatus = { server_configured: false, linked: false, addr: '', user: '', repo: '' }
const account = { id: 'acct_aaaaaaaaaaaaaaaaaaaaaaaaaa', provider: 'github', subject: '583231', login: 'octocat' }

const serverEdge: ServerEdge = {
  edge_url: edge,
  server_id: 'abcdefghijklmnopqrstuvwxyz',
  access_policy: 'account',
  owner: null,
  connected: true,
  claimed: true,
}

function seed(server: ServerEdge | null = null) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    info: serverInfo,
    capabilities: localCaps,
    hydrated: true,
    hydrationError: null,
    route: { name: 'onboarding', params: {} },
    onboarded: false,
    onboardingStep: 'Connect',
    onboardingFurthest: 'Connect',
    onboardingWorkspace: '',
    edge: server,
  })
}

async function open(retiring?: EdgeNotice) {
  const status: EdgeStatus = { edges: [{ edge, account, retiring }] }
  const client = fakeApi({
    localLinkStatus: vi.fn(async () => unlinked),
    localEdgeStatus: vi.fn(async () => status),
    localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    localEdgeLogin: vi.fn(async () => ({
      state: 'pending' as const, edge: next, signin_origin: next, user_code: 'WDJB-MJHT', verification_uri: `${next}/device`,
    })),
  })
  render(<OnboardingRoute params={{}} client={client} />)
  fireEvent.click(await screen.findByRole('radio', { name: 'Cloudfield relay' }))
  await vi.waitFor(() => expect(client.localEdgeServers).toHaveBeenCalled())
  return client
}

function noticeText(): string | null {
  const notice = screen.queryAllByText((_, el) => el?.tagName === 'P' && / says it is /.test(el.textContent ?? ''))
  return notice[0]?.textContent ?? null
}

afterEach(() => {
  vi.restoreAllMocks()
  forgetHandedLink()
  window.history.replaceState(null, '', '/')
})

describe('a signed-in edge that says its address is retiring', () => {
  it('shows nothing without a notice', async () => {
    seed()
    await open()
    expect(noticeText()).toBeNull()
    expect(screen.queryByLabelText('Sign in at')).toBeNull()
  })

  it('shows an unknown replacement as the old address says it, and fills nothing in', async () => {
    seed()
    const client = await open({ state: 'retiring', replacement: next, known: false })
    expect(noticeText()).toBe('old.example.test says it is retiring and that its new address is https://new.example.test.')
    expect(screen.getByText('Confirm that address with whoever runs the edge before you sign in there.')).toBeDefined()
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe('')
    expect(screen.getByRole('button', { name: 'Sign in' }).hasAttribute('disabled')).toBe(true)
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
  })

  it('fills in a replacement this computer knows, and signs in only when asked', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const client = await open({ state: 'retiring', replacement: next, date: '2027-03-01', known: true })
    expect(noticeText()).toBe('old.example.test says it is retiring on 2027-03-01 and that its new address is https://new.example.test.')
    expect(screen.getByText('Sign in at the new address, then link your server again there.')).toBeDefined()
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe(next)
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(client.localEdgeLogin).toHaveBeenCalledWith(next)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
  })

  it('says a retired address is retired', async () => {
    seed()
    await open({ state: 'retired', replacement: next, known: false })
    expect(noticeText()).toBe('old.example.test says it is retired and that its new address is https://new.example.test.')
  })

  it('fills in the replacement the linked server verified', async () => {
    seed({ ...serverEdge, retiring: { state: 'retiring', origin: edge, replacement: next } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe(next)
  })

  it('does not fill in a replacement from a notice the server trusted on first use', async () => {
    seed({ ...serverEdge, retiring: { state: 'retiring', origin: edge, replacement: next, first_use: true } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe('')
  })

  it('does not fill in a replacement the server names differently', async () => {
    seed({ ...serverEdge, retiring: { state: 'retiring', origin: edge, replacement: 'https://other.example.test' } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe('')
  })
})
