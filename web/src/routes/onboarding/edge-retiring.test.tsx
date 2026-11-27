import { fireEvent, render, screen } from '@testing-library/react'
import type { EdgeStatusNotice, EdgeStatus, GatewayCapabilities, LinkStatus, ServerEdge } from '@/lib/types'
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

async function open(retiring?: EdgeStatusNotice, alsoAt: string[] = [], serversError?: Error) {
  const status: EdgeStatus = { edges: [{ edge, account, retiring }, ...alsoAt.map((e) => ({ edge: e, account }))] }
  const client = fakeApi({
    localLinkStatus: vi.fn(async () => unlinked),
    localEdgeStatus: vi.fn(async () => status),
    localEdgeServers: vi.fn(async () => {
      if (serversError) throw serversError
      return { edge, servers: [] }
    }),
    localEdgeLogin: vi.fn(async () => ({
      state: 'pending' as const, edge: next, signin_origin: next, user_code: 'WDJB-MJHT', verification_uri: `${next}/device`,
    })),
  })
  render(<OnboardingRoute params={{}} client={client} />)
  fireEvent.click(await screen.findByRole('radio', { name: 'Cloudfield relay' }))
  if (alsoAt.length === 0) await vi.waitFor(() => expect(client.localEdgeServers).toHaveBeenCalled())
  else await vi.waitFor(() => expect(noticeText()).not.toBeNull())
  return client
}

/** The notice's title and its first line, as a person reads them. */
function noticeText(): string | null {
  const callout = document.querySelector('[data-slot="callout"]')
  if (!callout) return null
  const [title, said] = callout.querySelectorAll('p')
  return `${title.textContent} / ${said.textContent}`
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
    expect(noticeText()).toBe('old.example.test is retiring / It says its new address is https://new.example.test.')
    expect(screen.getByText('Confirm that address with whoever runs the edge before you sign in there.')).toBeDefined()
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe('')
    expect(screen.getByRole('button', { name: 'Sign in' }).hasAttribute('disabled')).toBe(true)
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
  })

  it('fills in a replacement this computer knows, and signs in only when asked', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const client = await open({ state: 'retiring', replacement: next, date: '2027-03-01', known: true })
    expect(noticeText()).toBe('old.example.test is retiring on 2027-03-01 / It says its new address is https://new.example.test.')
    expect(screen.getByText('Sign in at the new address, then link your server again there.')).toBeDefined()
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe(next)
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(client.localEdgeLogin).toHaveBeenCalledWith(next)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
  })

  it('offers no sign-in at a replacement this computer is already signed in at', async () => {
    seed()
    await open({ state: 'retiring', replacement: next, known: true }, [next])
    expect(screen.getByText((_, el) => el?.tagName === 'P' && el.textContent === 'You are signed in at new.example.test too: choose it above and link your server again through it.')).toBeDefined()
    expect(screen.queryByLabelText('Sign in at')).toBeNull()
  })

  it('says a retired address is retired', async () => {
    seed()
    await open({ state: 'retired', replacement: next, known: false }, [], new Error('edge.servers: list servers: old.example.test refused: retired (HTTP 410)'))
    expect(noticeText()).toBe('old.example.test is retired / It says its new address is https://new.example.test.')
    expect(await screen.findByText(/refused: retired \(HTTP 410\)/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('fills in the replacement the linked server verified', async () => {
    seed({ ...serverEdge, notice: { state: 'retiring', replacement: next } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe(next)
  })

  it('does not fill in a replacement from a notice the server trusted on first use', async () => {
    seed({ ...serverEdge, notice: { state: 'retiring', replacement: next, first_use: true } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe('')
  })

  it('does not fill in a replacement the server names differently', async () => {
    seed({ ...serverEdge, notice: { state: 'retiring', replacement: 'https://other.example.test' } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect((screen.getByLabelText('Sign in at') as HTMLInputElement).value).toBe('')
  })
})
