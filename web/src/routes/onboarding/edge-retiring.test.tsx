import { fireEvent, render, screen } from '@testing-library/react'
import type { EdgeStatusNotice, EdgeStatus, GatewayCapabilities, LinkStatus, ServerEdge } from '@/lib/types'
import { forgetHandedLink } from '@/lib/url-state'
import { OnboardingRoute } from '@/routes/onboarding'
import { EdgeSignIn } from '@/routes/onboarding/edge-link'
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
function noticeCallout(): Element | undefined {
  return [...document.querySelectorAll('[data-slot="callout"]')].find((c) => / says it is /.test(c.textContent ?? ''))
}

function noticeText(): string | null {
  const callout = noticeCallout()
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
  })

  it('shows the notice as the old address says it, as text only', async () => {
    seed()
    const client = await open({ state: 'retiring', replacement: next, date: '2027-03-01', known: true })
    expect(noticeText()).toBe(
      'old.example.test says it is retiring on 2027-03-01 and that its new address is https://new.example.test. / Confirm that address with whoever runs the edge before you sign in there.',
    )
    expect(noticeCallout()!.querySelector('input, button')).toBeNull()
    expect(screen.queryByRole('textbox', { name: /sign in/i })).toBeNull()
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
  })

  it('does not take the linked server\'s notice as a reason to fill anything in', async () => {
    seed({ ...serverEdge, notice: { state: 'retiring', replacement: next } })
    await open({ state: 'retiring', replacement: next, known: false })
    expect(screen.getByText('Confirm that address with whoever runs the edge before you sign in there.')).toBeDefined()
    expect(screen.queryByDisplayValue(next)).toBeNull()
  })

  it('says when this computer is already signed in at the new address', async () => {
    seed()
    await open({ state: 'retiring', replacement: next, known: true }, [next])
    expect(noticeText()).toBe(
      'old.example.test says it is retiring and that its new address is https://new.example.test. / You are already signed in at https://new.example.test.',
    )
  })

  it('says a retired address is retired, after its refusal, with no dead ends', async () => {
    seed()
    await open({ state: 'retired', replacement: next, known: false }, [], new Error('edge.servers: list servers: old.example.test refused: retired (HTTP 410)'))
    expect(noticeText()).toBe(
      'old.example.test says it is retired and that its new address is https://new.example.test. / Confirm that address with whoever runs the edge before you sign in there.',
    )
    const refusal = await screen.findByText(/refused: retired \(HTTP 410\)/)
    expect(refusal.compareDocumentPosition(noticeCallout()!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(screen.queryByRole('form', { name: 'Add a server' })).toBeNull()
    expect(screen.queryByText('Other ways to link')).toBeNull()
  })

  it('shows the notice of an edge signed in to just now, without a remount', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const login = { edge, signin_origin: edge, user_code: 'WDJB-MJHT', verification_uri: `${edge}/device` }
    let done = false
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> =>
        done
          ? { edges: [{ edge, account, retiring: { state: 'retiring', replacement: next, known: false } }], login: { ...login, state: 'signed_in', account } }
          : { edges: [], login: { ...login, state: 'pending' } },
      ),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} />)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    done = true
    await vi.waitFor(() => expect(noticeText()).not.toBeNull(), { timeout: 5000 })
  })

  it('finds a sign-in at the new address while a handed claim names the old one', async () => {
    const claim = `aether://claim?edge=${encodeURIComponent(edge)}&server=${serverEdge.server_id}#abcdefghijklmnop`
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({
        edges: [{ edge, account, retiring: { state: 'retiring', replacement: next, known: true } }, { edge: next, account }],
      })),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} claim={claim} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} />)
    await vi.waitFor(() => expect(noticeText()).toContain('You are already signed in at https://new.example.test.'))
    expect(screen.queryByRole('radio')).toBeNull()
  })
})
