import { fireEvent, render, screen } from '@testing-library/react'
import type { EdgeStatusNotice, EdgeStatus, GatewayCapabilities, LinkStatus, ServerEdge } from '@/lib/types'
import { forgetHandedLink } from '@/lib/url-state'
import { OnboardingRoute } from '@/routes/onboarding'
import { EdgeSignIn, JoinLink } from '@/routes/onboarding/edge-link'
import { capability } from '@/lib/capability'
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

function seed() {
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
    edge: null,
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
  if (retiring) await vi.waitFor(() => expect(noticeText()).not.toBeNull())
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
  it('shows nothing without a notice, and asks for notices apart from the sign-in', async () => {
    seed()
    const client = await open()
    expect(noticeText()).toBeNull()
    expect(vi.mocked(client.localEdgeStatus).mock.calls.map(([notices]) => !!notices)).toEqual([false, true])
  })

  it('takes the new address\'s sign-in from the same answer as the notice', async () => {
    seed()
    const client = await open({ state: 'retiring', replacement: next }, [next])
    expect(noticeText()).toContain('You are already signed in at https://new.example.test.')
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [{ edge, account, retiring: { state: 'retiring', replacement: next } }] })
    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(noticeText()).toContain('Confirm that address with whoever runs the edge'))
  })

  it('keeps the latest answer when a slower earlier one arrives after it', async () => {
    seed()
    const client = await open()
    let first: (s: EdgeStatus) => void = () => {}
    vi.mocked(client.localEdgeStatus)
      .mockImplementationOnce(() => new Promise((resolve) => (first = resolve)))
      .mockResolvedValueOnce({ edges: [{ edge, account, retiring: { state: 'retired', replacement: next } }] })
    window.dispatchEvent(new Event('focus'))
    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(noticeText()).toContain('says it is retired'))
    first({ edges: [{ edge, account, retiring: { state: 'retiring', replacement: 'https://other.example.test' } }] })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(noticeText()).toContain('says it is retired')
  })

  it('drops the server list it showed once the address says it is retired', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({ edges: [{ edge, account }] })),
      localEdgeServers: vi.fn(async () => ({
        edge,
        servers: [{ id: serverEdge.server_id, name: 'work box', role: 'owner', online: true, access_policy: 'account' as const, kind: 'self-hosted' as const }],
      })),
    })
    render(<OnboardingRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('radio', { name: 'Cloudfield relay' }))
    expect(await screen.findByRole('button', { name: 'Link work box' })).toBeDefined()
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [{ edge, account, retiring: { state: 'retired', replacement: next } }] })
    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(noticeText()).toContain('says it is retired'))
    expect(screen.queryByRole('button', { name: 'Link work box' })).toBeNull()
    expect(screen.queryByRole('form', { name: 'Add a server' })).toBeNull()
  })

  it('asks for notices only once the relay panel is shown', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({ edges: [{ edge, account }] })),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<OnboardingRoute params={{}} client={client} />)
    const relay = await screen.findByRole('radio', { name: 'Cloudfield relay' })
    await new Promise((resolve) => setTimeout(resolve, 100))
    window.dispatchEvent(new Event('focus'))
    await new Promise((resolve) => setTimeout(resolve, 50))
    const asked = () => vi.mocked(client.localEdgeStatus).mock.calls.filter(([notices]) => notices).length
    expect(asked()).toBe(0)
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [{ edge, account, retiring: { state: 'retired', replacement: next } }] })
    fireEvent.click(relay)
    await vi.waitFor(() => expect(noticeText()).toContain('says it is retired'))
    expect(asked()).toBe(1)
  })

  it('offers the new sign-in beside a retired edge, choosing nothing for the person', async () => {
    seed()
    const client = await open({ state: 'retired', replacement: next })
    expect(screen.queryAllByRole('radio', { name: / as / })).toHaveLength(0)
    vi.mocked(client.localEdgeStatus).mockResolvedValue({
      edges: [{ edge, account, retiring: { state: 'retired', replacement: next } }, { edge: next, account }],
    })
    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(noticeText()).toContain('You are already signed in at https://new.example.test.'))
    const radios = screen.getAllByRole('radio', { name: / as / })
    expect(radios.map((r) => [document.querySelector(`label[for="${r.id}"]`)?.textContent, r.getAttribute('aria-checked')])).toEqual([
      ['old.example.test as octocat (GitHub) · says it is retired', 'true'],
      ['new.example.test as octocat (GitHub)', 'false'],
    ])
  })

  it('keeps a retired mark across a switch to another way and back, when the refetch fails', async () => {
    seed()
    const client = fakeApi({
      localLinkStatus: vi.fn(async () => unlinked),
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({ edges: [{ edge, account }] })),
      localEdgeServers: vi.fn(async () => ({
        edge,
        servers: [{ id: serverEdge.server_id, name: 'work box', role: 'owner', online: true, access_policy: 'account' as const, kind: 'self-hosted' as const }],
      })),
    })
    render(<OnboardingRoute params={{}} client={client} />)
    fireEvent.click(await screen.findByRole('radio', { name: 'Cloudfield relay' }))
    expect(await screen.findByRole('button', { name: 'Link work box' })).toBeDefined()
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [{ edge, account, retiring: { state: 'retired', replacement: next } }] })
    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(noticeText()).toContain('says it is retired'))
    vi.mocked(client.localEdgeStatus).mockRejectedValue(new Error('edge.status: the gateway is restarting'))
    fireEvent.click(screen.getByRole('radio', { name: /^Tailscale/ }))
    fireEvent.click(screen.getByRole('radio', { name: 'Cloudfield relay' }))
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(screen.queryByRole('button', { name: 'Link work box' })).toBeNull()
    expect(noticeText()).toContain('says it is retired')
  })

  it('goes back to signing in when the chosen edge was signed out elsewhere, and on once signed in again', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    seed()
    const client = await open()
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [] })
    window.dispatchEvent(new Event('focus'))
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with GitHub' }))
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    const login = { edge: next, signin_origin: next, user_code: 'WDJB-MJHT', verification_uri: `${next}/device` }
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [{ edge: next, account }], login: { ...login, state: 'signed_in', account } })
    await vi.waitFor(() => expect(screen.getByText((_, el) => el?.tagName === 'P' && /^Signed in to new\.example\.test as/.test(el.textContent ?? ''))).toBeDefined(), { timeout: 5000 })
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(screen.queryByRole('button', { name: 'Sign in with GitHub' })).toBeNull()
  })

  it('waits for the edge it signed in to among the rows', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const login = { edge, signin_origin: edge, user_code: 'WDJB-MJHT', verification_uri: `${edge}/device` }
    let answers = 0
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => {
        answers++
        if (answers === 1) return { edges: [], login: { ...login, state: 'pending' } }
        if (answers === 2) return { edges: [{ edge: next, account }], login: { ...login, state: 'signed_in', account } }
        return { edges: [{ edge: next, account }, { edge, account }], login: { ...login, state: 'signed_in', account } }
      }),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    await vi.waitFor(() => expect(answers).toBe(2), { timeout: 5000 })
    expect(screen.getByText('WDJB-MJHT')).toBeDefined()
    await vi.waitFor(() => expect(screen.getByText((_, el) => el?.tagName === 'P' && /^Signed in to old\.example\.test as/.test(el.textContent ?? ''))).toBeDefined(), { timeout: 5000 })
  })

  it('takes a sign-in at another spelling of the new address for none', async () => {
    seed()
    await open({ state: 'retiring', replacement: next }, [next + ':443'])
    expect(noticeText()).toContain('Confirm that address with whoever runs the edge before you sign in there.')
  })

  it('asks for notices again when the window gets focus', async () => {
    seed()
    const client = await open()
    vi.mocked(client.localEdgeStatus).mockResolvedValue({ edges: [{ edge, account, retiring: { state: 'retired', replacement: next } }] })
    window.dispatchEvent(new Event('focus'))
    await vi.waitFor(() => expect(noticeText()).toContain('old.example.test says it is retired'))
    expect(client.localEdgeStatus).toHaveBeenLastCalledWith(true)
  })

  it('shows the notice as the old address says it, as text only', async () => {
    seed()
    const client = await open({ state: 'retiring', replacement: next, date: '2027-03-01' })
    expect(noticeText()).toBe(
      'old.example.test says it is retiring on 2027-03-01 and that its new address is https://new.example.test. / Confirm that address with whoever runs the edge before you sign in there. Then run cloudfield login --edge <new address>.',
    )
    expect(noticeCallout()!.querySelector('input, button')).toBeNull()
    expect(screen.queryByRole('textbox', { name: /sign in/i })).toBeNull()
    expect(client.localEdgeLogin).not.toHaveBeenCalled()
  })

  it('says when this computer is already signed in at the new address', async () => {
    seed()
    await open({ state: 'retiring', replacement: next }, [next])
    expect(noticeText()).toBe(
      'old.example.test says it is retiring and that its new address is https://new.example.test. / You are already signed in at https://new.example.test.',
    )
  })

  it('says a retired address is retired, before its refusal, with no dead ends', async () => {
    seed()
    await open({ state: 'retired', replacement: next }, [], new Error('edge.servers: list servers: old.example.test refused: retired (HTTP 410)'))
    expect(noticeText()).toBe(
      'old.example.test says it is retired and that its new address is https://new.example.test. / Confirm that address with whoever runs the edge before you sign in there. Then run cloudfield login --edge <new address>.',
    )
    const refusal = await screen.findByText(/refused: retired \(HTTP 410\)/)
    expect(refusal.compareDocumentPosition(noticeCallout()!) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy()
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
          ? { edges: [{ edge, account, retiring: { state: 'retiring', replacement: next } }], login: { ...login, state: 'signed_in', account } }
          : { edges: [], login: { ...login, state: 'pending' } },
      ),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    done = true
    await vi.waitFor(() => expect(noticeText()).not.toBeNull(), { timeout: 5000 })
    expect(vi.mocked(client.localEdgeStatus).mock.calls.filter(([notices]) => notices).length).toBe(1)
  })

  it('finishes a sign-in while the notices cannot be read', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const login = { edge, signin_origin: edge, user_code: 'WDJB-MJHT', verification_uri: `${edge}/device` }
    let done = false
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (notices?: boolean): Promise<EdgeStatus> => {
        if (notices) throw new Error('edge.status: the gateway is restarting')
        return done ? { edges: [{ edge, account }], login: { ...login, state: 'signed_in', account } } : { edges: [], login: { ...login, state: 'pending' } }
      }),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    done = true
    await vi.waitFor(() => expect(screen.getByText((_, el) => el?.tagName === 'P' && /^Signed in to old\.example\.test as/.test(el.textContent ?? ''))).toBeDefined(), { timeout: 5000 })
  })

  it('asks for no notices on a join link', async () => {
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({ edges: [{ edge, account }] })),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    const link = `${edge}/join/${serverEdge.server_id}/inv-7k2m9q4xbd#abcdefghijklmnop`
    render(<JoinLink client={client} caps={capability(localCaps)} link={link} onLinked={vi.fn()} />)
    await vi.waitFor(() => expect(client.localEdgeStatus).toHaveBeenCalled())
    window.dispatchEvent(new Event('focus'))
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(vi.mocked(client.localEdgeStatus).mock.calls.every(([notices]) => !notices)).toBe(true)
  })

  it('keeps waiting while a finished sign-in has no entry yet', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const login = { edge, signin_origin: edge, user_code: 'WDJB-MJHT', verification_uri: `${edge}/device` }
    let answers = 0
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => {
        answers++
        if (answers === 1) return { edges: [], login: { ...login, state: 'pending' } }
        if (answers === 2) return { edges: [], login: { ...login, state: 'signed_in', account } }
        return { edges: [{ edge, account }], login: { ...login, state: 'signed_in', account } }
      }),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    await vi.waitFor(() => expect(answers).toBe(2), { timeout: 5000 })
    expect(screen.getByText('WDJB-MJHT')).toBeDefined()
    await vi.waitFor(() => expect(screen.getByText((_, el) => el?.tagName === 'P' && /^Signed in to old\.example\.test as/.test(el.textContent ?? ''))).toBeDefined(), { timeout: 5000 })
  })

  it('finds the gateway\'s sign-in for a claim that spells its edge differently, on entering', async () => {
    const claim = `aether://claim?edge=${encodeURIComponent('HTTPS://Old.example.test/')}&server=${serverEdge.server_id}#abcdefghijklmnop`
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({ edges: [{ edge, account }] })),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} claim={claim} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    await vi.waitFor(() => expect(screen.getByText((_, el) => el?.tagName === 'P' && /^Signed in to old\.example\.test as/.test(el.textContent ?? ''))).toBeDefined())
    expect(screen.queryByRole('button', { name: 'Sign in with GitHub' })).toBeNull()
  })

  it('keeps an explicit port in a claim\'s edge', async () => {
    const claim = `aether://claim?edge=${encodeURIComponent(edge + ':8443')}&server=${serverEdge.server_id}#abcdefghijklmnop`
    const client = fakeApi({ localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({ edges: [{ edge, account }] })) })
    render(<EdgeSignIn client={client} claim={claim} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    expect(await screen.findByRole('button', { name: 'Sign in with GitHub' })).toBeDefined()
  })

  it('finishes a sign-in for a claim that spells its edge differently from the gateway', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const claim = `aether://claim?edge=${encodeURIComponent('https://Old.example.test/')}&server=${serverEdge.server_id}#abcdefghijklmnop`
    const login = { edge, signin_origin: edge, user_code: 'WDJB-MJHT', verification_uri: `${edge}/device` }
    let done = false
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> =>
        done
          ? { edges: [{ edge, account, retiring: { state: 'retiring', replacement: next } }], login: { ...login, state: 'signed_in', account } }
          : { edges: [] },
      ),
      localEdgeLogin: vi.fn(async () => ({ ...login, state: 'pending' as const })),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} claim={claim} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with GitHub' }))
    expect(await screen.findByText('WDJB-MJHT')).toBeDefined()
    done = true
    await vi.waitFor(() => expect(noticeText()).not.toBeNull(), { timeout: 5000 })
    expect(screen.getByText((_, el) => el?.tagName === 'P' && /^Signed in to old\.example\.test as/.test(el.textContent ?? ''))).toBeDefined()
  })

  it('finds a sign-in at the new address while a handed claim names the old one', async () => {
    const claim = `aether://claim?edge=${encodeURIComponent(edge)}&server=${serverEdge.server_id}#abcdefghijklmnop`
    const client = fakeApi({
      localEdgeStatus: vi.fn(async (): Promise<EdgeStatus> => ({
        edges: [{ edge, account, retiring: { state: 'retiring', replacement: next } }, { edge: next, account }],
      })),
      localEdgeServers: vi.fn(async () => ({ edge, servers: [] })),
    })
    render(<EdgeSignIn client={client} claim={claim} onHand={vi.fn()} onLinked={vi.fn()} onSignedOut={vi.fn()} visible />)
    await vi.waitFor(() => expect(noticeText()).toContain('You are already signed in at https://new.example.test.'))
    expect(screen.queryByRole('radio')).toBeNull()
  })
})
