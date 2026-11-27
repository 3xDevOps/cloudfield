import { act, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { type ComponentProps } from 'react'
import { AddLogin } from '@/components/agents/add-login'
import { LoginList, useAgentLogins } from '@/components/agents/agent-logins'
import type { Api } from '@/lib/api'
import type { AgentLogin, Member } from '@/lib/types'
import { useStore } from '@/store'
import { alice, bob, fakeApi } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { atViewport } from '@/test/viewport'
import { stillWaiting } from '@/store/optimistic'
import type * as xtermHostModule from '@/components/xterm-host'

// Passes every terminal through, keeping the link handler a sign-in gives it.
const xterm = vi.hoisted(() => ({ onLink: undefined as ((uri: string) => boolean) | undefined }))
vi.mock('@/components/xterm-host', async (importOriginal) => {
  const actual = await importOriginal<typeof xtermHostModule>()
  return {
    ...actual,
    useXterm: (options: Parameters<typeof actual.useXterm>[0]) => {
      xterm.onLink = options?.onLink
      return actual.useXterm(options)
    },
  }
})

const logins: AgentLogin[] = [
  { id: 'default', name: 'default', kind: 'subscription', plan: 'max', email: 'alice@example.test', organization_name: 'Alice personal', automatic: true, shared: true, position: 0 },
  { id: 'lgn-work0000001', name: 'work', kind: 'subscription', plan: 'pro', email: 'alice@work.example.test', organization_name: 'Example Co', automatic: true, shared: false, position: 1 },
  { id: 'lgn-console0001', name: 'console', kind: 'console', email: 'alice@example.test', organization_name: 'Alice API', per_use: true, automatic: false, shared: true, position: 2 },
]

function show(over: Parameters<typeof fakeApi>[0] = {}, sharedWith: Member[] | null = [bob]) {
  const client = fakeApi(over)
  const onChanged = vi.fn(async () => {})
  render(<LoginList logins={logins} sharedWith={sharedWith} client={client} onChanged={onChanged} />)
  return { client, onChanged }
}

const row = (name: string) => screen.getByRole('listitem', { name: `Login ${name}` })

describe('login list', () => {
  it('lists the logins in order with what each one is and whom shares reach', async () => {
    const { client } = show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => ({
        account_member_id: alice.id,
        login: params.login,
        providers: params.login ? [] : [{ provider: 'claude' as const, status: 'ok' as const, windows: [{ id: 'five_hour', label: '5-hour', used_percent: 42 }], checked_at: '2026-11-26T10:00:00Z' }],
      })),
    })
    const items = within(screen.getByRole('list', { name: 'Claude Code logins in priority order' })).getAllByRole('listitem')
    expect(items.map((item) => item.getAttribute('aria-label'))).toEqual(['Login default', 'Login work', 'Login console'])

    expect(items.map((item) => item.textContent?.charAt(0))).toEqual(['1', '2', '3'])
    expect(within(row('default')).getByText('Subscription · max · tried first').dataset.slot).toBeUndefined()
    expect(row('work').textContent).not.toContain('tried first')
    expect(row('default').textContent).toContain('alice@example.test · Alice personal')
    expect(row('default').textContent).toContain(`Sessions of ${bob.display_name} can run here too.`)
    expect(await within(row('default')).findByRole('progressbar', { name: 'Login default 5-hour usage' })).toBeDefined()
    expect(row('work').textContent).not.toContain('Sessions of')
    expect(row('work').textContent).not.toContain('your sessions')
    expect(within(row('work')).queryByText('Billed per use')).toBeNull()
    expect(within(row('console')).getByTitle('Billed per use to Alice API').textContent).toContain('Billed per use')
    expect(row('console').textContent).toContain('No share reaches it while Automatic is off.')
    expect(row('console').textContent).not.toContain('Automatic sessions can move here')
    expect(screen.getByText(`Your account is shared with ${bob.display_name}: logins on for shares serve their sessions too.`, { exact: false })).toBeDefined()
    expect(client.accountUsage).toHaveBeenCalledWith({})
    expect(client.accountUsage).toHaveBeenCalledWith({ login: 'lgn-work0000001' })

    expect(within(row('default')).queryByRole('button', { name: 'Rename default' })).toBeNull()
    expect(within(row('default')).queryByRole('button', { name: 'Remove default' })).toBeNull()
    expect(row('default').textContent).toContain('Your default login, the one cloudfield agent login claude signs in. It cannot be renamed or removed.')
    expect(within(row('work')).getByRole('button', { name: 'Rename work' })).toBeDefined()
    expect(within(row('default')).getByRole('button', { name: 'Move default up' })).toHaveProperty('disabled', true)
    expect(within(row('default')).getByRole('button', { name: 'Move default up' }).className).not.toContain('shadow-button')
    expect(within(row('default')).getByRole('button', { name: 'Move default down' }).className).toContain('shadow-button')
    expect(within(row('console')).getByRole('button', { name: 'Move console down' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('region', { name: 'Claude Code logins' }).textContent).toContain('Sessions start on the first automatic login in your order with usage left, and move to the first other one with usage left when it runs out.')
  })

  it('says when the account is shared with nobody', () => {
    show({}, [])
    expect(row('default').textContent).not.toContain('Sessions of')
    expect(screen.getByText('Your account is not shared with anyone.', { exact: false })).toBeDefined()
  })

  it('sends the whole order, Automatic and a rename, then reads the list again', async () => {
    const user = userEvent.setup()
    const { client, onChanged } = show({
      agentLoginOrder: vi.fn(async () => ({})),
      agentLoginAutomatic: vi.fn(async () => ({})),
      agentLoginRename: vi.fn(async () => ({})),
    })
    await user.click(within(row('work')).getByRole('button', { name: 'Move work down' }))
    expect(client.agentLoginOrder).toHaveBeenCalledWith('claude', ['default', 'lgn-console0001', 'lgn-work0000001'])
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))

    await user.click(within(row('console')).getByRole('switch', { name: 'Automatic' }))
    expect(client.agentLoginAutomatic).toHaveBeenCalledWith('claude', 'lgn-console0001', true)
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(2))

    await user.click(screen.getByRole('button', { name: 'Rename work' }))
    const field = screen.getByRole('textbox', { name: 'New name for work' })
    await user.clear(field)
    await user.type(field, 'office')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(client.agentLoginRename).toHaveBeenCalledWith('claude', 'lgn-work0000001', 'office')
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(3))
  })

  it('shows a refused rename and a refused order verbatim', async () => {
    const user = userEvent.setup()
    show({
      agentLoginRename: vi.fn(async () => { throw new Error('you already have a login named "console"') }),
      agentLoginOrder: vi.fn(async () => { throw new Error('login_ids must name each of your logins once, default included') }),
    })
    await user.click(screen.getByRole('button', { name: 'Rename work' }))
    const field = screen.getByRole('textbox', { name: 'New name for work' })
    await user.clear(field)
    await user.type(field, 'console')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect((await screen.findByRole('alert')).textContent).toBe('you already have a login named "console"')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    await user.click(within(row('work')).getByRole('button', { name: 'Move work up' }))
    expect((await screen.findByRole('alert')).textContent).toBe('login_ids must name each of your logins once, default included')
  })

  it('keeps the arrows off until the list is read again after a move, and reads it again after a refused one', async () => {
    const user = userEvent.setup()
    let reread = () => {}
    const onChanged = vi.fn(() => new Promise<void>((resolve) => { reread = resolve }))
    const client = fakeApi({
      agentLoginOrder: vi.fn<Api['agentLoginOrder']>().mockResolvedValueOnce({}).mockRejectedValueOnce(new Error('login_ids must name each of your logins once, default included')),
    })
    render(<LoginList logins={logins} sharedWith={[]} client={client} onChanged={onChanged} />)
    await user.click(within(row('default')).getByRole('button', { name: 'Move default down' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect(within(row('work')).getByRole('button', { name: 'Move work down' })).toHaveProperty('disabled', true)
    await act(async () => reread())
    expect(within(row('work')).getByRole('button', { name: 'Move work down' })).toHaveProperty('disabled', false)

    await user.click(within(row('work')).getByRole('button', { name: 'Move work down' }))
    expect((await screen.findByRole('alert')).textContent).toBe('login_ids must name each of your logins once, default included')
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it('says which login is at its limit and when it serves again, in days past 24 hours', async () => {
    const hour = 3_600_000
    const at = (hours: number) => new Date(Date.now() + hours * hour).toISOString()
    const windows: Record<string, { id: string; label: string; used_percent: number; resets_at: string }[]> = {
      default: [{ id: 'five_hour', label: '5 hours', used_percent: 94, resets_at: at(3) }, { id: 'seven_day', label: '7 days', used_percent: 58, resets_at: at(90) }],
      'lgn-work0000001': [{ id: 'five_hour', label: '5 hours', used_percent: 95, resets_at: at(1) }, { id: 'seven_day', label: '7 days', used_percent: 100, resets_at: at(40) }],
    }
    show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => ({
        account_member_id: alice.id,
        login: params.login,
        providers: [{ provider: 'claude' as const, status: 'ok' as const, windows: windows[params.login ?? 'default'] ?? [], checked_at: at(0) }],
      })),
    })
    expect(await within(row('work')).findByText('At its limit (7 days) · resets in 1d 16h')).toBeDefined()
    expect(await within(row('default')).findByText('58% used · resets in 3d 18h')).toBeDefined()
    expect(row('default').textContent).not.toContain('At its limit')
    expect(row('console').textContent).not.toContain('At its limit')
  })

  it('turns Use for shares through agent.login.share, and shows its refusal verbatim and reads the list again', async () => {
    const user = userEvent.setup()
    const refusal = 'you have no login "lgn-work0000001"'
    const share = vi.fn<Api['agentLoginShare']>().mockResolvedValueOnce({}).mockRejectedValueOnce(new Error(refusal))
    const { client, onChanged } = show({ agentLoginShare: share })
    expect(within(row('work')).getByRole('switch', { name: 'Use for shares' }).getAttribute('aria-checked')).toBe('false')
    await user.click(within(row('work')).getByRole('switch', { name: 'Use for shares' }))
    expect(client.agentLoginShare).toHaveBeenCalledWith('claude', 'lgn-work0000001', true)
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    await user.click(within(row('default')).getByRole('switch', { name: 'Use for shares' }))
    expect(client.agentLoginShare).toHaveBeenLastCalledWith('claude', 'default', false)
    expect((await screen.findByRole('alert')).textContent).toBe(refusal)
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it("says a Console login has no usage windows, and shows any other usage error verbatim", async () => {
    show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => {
        if (params.login === 'lgn-work0000001') throw new Error('account.usage: claude: credential file unavailable')
        return {
          account_member_id: alice.id,
          login: params.login,
          providers: params.login === 'lgn-console0001'
            ? [{ provider: 'claude' as const, status: 'unsupported' as const, windows: [], checked_at: '2026-11-26T10:00:00Z', error: 'an Anthropic Console sign-in is billed by the API and has no usage windows' }]
            : [],
        }
      }),
    })
    expect(await within(row('console')).findByText('No usage windows')).toBeDefined()
    expect(row('console').textContent).not.toContain('Usage not read')
    expect(await within(row('work')).findByText('account.usage: claude: credential file unavailable')).toBeDefined()
    expect(row('work').textContent).toContain('Usage not read: ')
  })

  it('says sessions can move onto a login billed per use and whom that charges, and whose sessions it serves', () => {
    const client = fakeApi()
    const { unmount } = render(<LoginList logins={logins.map((login) => ({ ...login, automatic: true }))} sharedWith={[]} client={client} onChanged={async () => {}} />)
    expect(row('console').textContent).toContain('Sessions can move here when the automatic logins above it run out, charged per use to Alice API.')
    expect(row('work').textContent).not.toContain('Sessions can move here')
    unmount()

    const carol = { ...bob, id: 'member-carol', display_name: 'Carol' }
    const shared = logins.map((login) => ({ ...login, automatic: true }))
    const { unmount: again } = render(<LoginList logins={shared} sharedWith={[bob, carol]} client={client} onChanged={async () => {}} />)
    expect(row('console').textContent).toContain(`Sessions of ${bob.display_name} and Carol can run here too, charged per use to Alice API.`)
    expect(row('default').textContent).toContain(`Sessions of ${bob.display_name} and Carol can run here too.`)
    expect(row('default').textContent).not.toContain('charged')
    again()

    render(<LoginList logins={shared.map((login) => ({ ...login, organization_name: undefined }))} sharedWith={[bob]} client={client} onChanged={async () => {}} />)
    expect(row('console').textContent).toContain(`Sessions of ${bob.display_name} can run here too, charged per use to login console.`)
    expect(within(row('console')).getByTitle('Billed per use to login console')).toBeDefined()
  })

  it('puts each line under the switch it explains below 1024px, and beside the switches from there', () => {
    const client = fakeApi()
    const shared = logins.map((login) => ({ ...login, automatic: true }))
    const describedBy = (name: string) => within(row('console')).getByRole('switch', { name }).getAttribute('aria-describedby')
    const wide = render(<LoginList logins={shared} sharedWith={[bob]} client={client} onChanged={async () => {}} />)
    expect(describedBy('Automatic')).toBeNull()
    expect(describedBy('Use for shares')).toBeNull()
    expect(row('console').textContent).toContain('Sessions can move here when the automatic logins above it run out, charged per use to Alice API.')
    wide.unmount()

    atViewport(1023)
    render(<LoginList logins={shared} sharedWith={[bob]} client={client} onChanged={async () => {}} />)
    expect(document.getElementById(describedBy('Automatic')!)!.textContent).toBe('Sessions can move here when the automatic logins above it run out, charged per use to Alice API.')
    expect(document.getElementById(describedBy('Use for shares')!)!.textContent).toBe(`Sessions of ${bob.display_name} can run here too, charged per use to Alice API.`)
  })

  it('confirms a removal, says Anthropic keeps the sign-in, and shows a refusal verbatim with ways to act on it', async () => {
    const user = userEvent.setup()
    const refusal = "login \"work\" is in use by your sessions run-7kq2m9x4ab (end them) and 1 of your teammates' sessions, which hold it until they end and their containers go (revoking the share you gave a teammate, with cloudfield account revoke <member-id>, moves their running sessions off it)"
    const navigate = vi.fn()
    const original = useStore.getState().navigate
    useStore.setState({ navigate })
    onTestFinished(() => { useStore.setState({ navigate: original }) })
    const { client, onChanged } = show({ agentLoginRemove: vi.fn(async () => { throw new Error(refusal) }) })
    await user.click(screen.getByRole('button', { name: 'Remove work' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove the login “work”?' })
    expect(dialog.textContent).toContain('Signed in as alice@work.example.test · Example Co.')
    expect(dialog.textContent).toContain('It does not sign the login out at Anthropic')
    expect(within(dialog).getByRole('button', { name: 'Remove' }).className).toContain('bg-state-failed')
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
    expect(client.agentLoginRemove).toHaveBeenCalledWith('claude', 'lgn-work0000001')
    expect(await within(dialog).findByText(refusal)).toBeDefined()
    expect(dialog.textContent).toContain("Use for shares is off, so no teammate's session starts or moves onto this login now; one already on it holds it until it ends.")
    expect(within(dialog).queryByRole('button', { name: 'Remove' })).toBeNull()
    expect(onChanged).toHaveBeenCalledTimes(1)
    await user.click(within(dialog).getByRole('button', { name: 'Open run-7kq2m9x4ab' }))
    expect(navigate).toHaveBeenCalledWith('run', { runId: 'run-7kq2m9x4ab' })
    expect(screen.queryByRole('alertdialog')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Remove work' }))
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }))
    await user.click(await screen.findByRole('button', { name: 'Revoke a share on Members' }))
    expect(navigate).toHaveBeenLastCalledWith('members')
  })

  it('claims nothing about shares while whom the account is shared with is unread, and shows why it is', () => {
    const { unmount } = render(<LoginList logins={logins} sharedWith={null} client={fakeApi()} onChanged={async () => {}} />)
    const section = screen.getByRole('region', { name: 'Claude Code logins' })
    expect(section.textContent).not.toContain('not shared')
    expect(section.textContent).not.toContain('Sessions of')
    unmount()

    render(<LoginList logins={logins} sharedWith={null} sharedError="account.list: server unreachable: dial tcp: connection refused" client={fakeApi()} onChanged={async () => {}} />)
    expect(screen.getByRole('region', { name: 'Claude Code logins' }).textContent).toContain('Whom your account is shared with was not read: account.list: server unreachable: dial tcp: connection refused')
    expect(screen.getByRole('region', { name: 'Claude Code logins' }).textContent).not.toContain('not shared with anyone')
  })

  it('says a share cannot use a default login kept as an API key or a Console profile', () => {
    for (const kind of ['api_key', 'console'] as const) {
      const { unmount } = render(<LoginList logins={[{ ...logins[0]!, kind, plan: undefined }, logins[1]!]} sharedWith={[bob]} client={fakeApi()} onChanged={async () => {}} />)
      expect(row('default').textContent).toContain('A share cannot use this kind of login.')
      expect(row('default').textContent).not.toContain('Sessions of')
      unmount()
    }
  })

  it('marks windows kept from a failed refresh as the last read, with the error', async () => {
    show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => ({
        account_member_id: alice.id,
        login: params.login,
        providers: params.login ? [] : [{ provider: 'claude' as const, status: 'stale' as const, windows: [{ id: 'five_hour', label: '5-hour', used_percent: 42 }], checked_at: '2026-11-26T10:00:00Z', error: 'claude usage: 429 Too Many Requests' }],
      })),
    })
    expect(await within(row('default')).findByText('claude usage: 429 Too Many Requests')).toBeDefined()
    expect(row('default').textContent).toContain('Last read, not current: claude usage: 429 Too Many Requests')
    expect(within(row('default')).getByRole('progressbar', { name: 'Login default 5-hour usage' })).toBeDefined()
  })

  it('reads the list again after a refused rename and after a call the server may have run without answering', async () => {
    const user = userEvent.setup()
    const { onChanged } = show({
      agentLoginRename: vi.fn(async () => { throw new Error('you already have a login named "console"') }),
      agentLoginShare: vi.fn(async () => { throw new Error('agent.login.share: server did not answer agent.login.share within 1m0s') }),
    })
    await user.click(screen.getByRole('button', { name: 'Rename work' }))
    await user.clear(screen.getByRole('textbox', { name: 'New name for work' }))
    await user.type(screen.getByRole('textbox', { name: 'New name for work' }), 'console')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(within(row('work')).getByRole('switch', { name: 'Use for shares' }))
    expect((await screen.findByRole('alert')).textContent).toBe('agent.login.share: server did not answer agent.login.share within 1m0s')
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it('shows which control is pending and locks only it, saying when the server is slow', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    onTestFinished(() => { vi.useRealTimers() })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    let answer = () => {}
    const { client } = show({
      agentLoginShare: vi.fn(() => new Promise<unknown>((resolve) => { answer = () => resolve({}) })),
      agentLoginOrder: vi.fn(() => new Promise<unknown>(() => {})),
    })
    await user.click(within(row('work')).getByRole('switch', { name: 'Use for shares' }))
    expect(within(row('work')).getByRole('status').textContent).toBe('Saving…')
    expect(within(row('work')).getByRole('switch', { name: 'Use for shares' })).toHaveProperty('disabled', true)
    expect(within(row('work')).getByRole('switch', { name: 'Automatic' })).toHaveProperty('disabled', false)
    expect(within(row('default')).getByRole('switch', { name: 'Use for shares' })).toHaveProperty('disabled', false)
    expect(within(row('work')).getByRole('button', { name: 'Move work up' })).toHaveProperty('disabled', false)
    expect(within(row('console')).getByRole('button', { name: 'Remove console' })).toHaveProperty('disabled', false)
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(within(row('work')).getByRole('status').textContent).toBe(stillWaiting)
    await act(async () => answer())
    expect(within(row('work')).queryByRole('status')).toBeNull()
    expect(within(row('work')).getByRole('switch', { name: 'Use for shares' })).toHaveProperty('disabled', false)

    await user.click(within(row('console')).getByRole('button', { name: 'Move console up' }))
    expect(client.agentLoginOrder).toHaveBeenCalledWith('claude', ['default', 'lgn-console0001', 'lgn-work0000001'])
    expect(within(row('console')).getByRole('status').textContent).toBe('Moving…')
    expect(within(row('default')).getByRole('button', { name: 'Move default down' })).toHaveProperty('disabled', true)
    expect(within(row('default')).getByRole('switch', { name: 'Automatic' })).toHaveProperty('disabled', false)
    expect(within(row('work')).getByRole('button', { name: 'Rename work' })).toHaveProperty('disabled', false)
  })
})

/** A server keeping the logins in the order the calls leave them, read through the page's hook. */
function ServedLogins({ client }: { client: Parameters<typeof useAgentLogins>[0] & ComponentProps<typeof LoginList>['client'] }) {
  const { logins: list, reload } = useAgentLogins(client, true)
  return list ? <LoginList logins={list} sharedWith={[]} client={client} onChanged={reload} /> : null
}

describe('login list against a server', () => {
  it('builds each move on the order the server answered for the one before', async () => {
    const user = userEvent.setup()
    let served = [...logins]
    const client = fakeApi({
      agentLoginList: vi.fn(async () => served.map((login, position) => ({ ...login, position }))),
      agentLoginOrder: vi.fn(async (_agent: string, ids: string[]) => {
        served = ids.map((id) => served.find((login) => login.id === id)!)
        return {}
      }),
    })
    render(<ServedLogins client={client} />)
    await user.click(await within(await screen.findByRole('listitem', { name: 'Login console' })).findByRole('button', { name: 'Move console up' }))
    await waitFor(() => expect(within(row('console')).getByRole('button', { name: 'Move console up' })).toHaveProperty('disabled', false))
    await user.click(within(row('console')).getByRole('button', { name: 'Move console up' }))
    expect(client.agentLoginOrder).toHaveBeenLastCalledWith('claude', ['lgn-console0001', 'default', 'lgn-work0000001'])
    await waitFor(() => expect(within(screen.getByRole('list', { name: 'Claude Code logins in priority order' })).getAllByRole('listitem').map((item) => item.getAttribute('aria-label'))).toEqual(['Login console', 'Login default', 'Login work']))
  })
})

describe('login list reads', () => {
  it('keeps the newest read when an older one answers last', async () => {
    const answers: ((list: AgentLogin[]) => void)[] = []
    const client = fakeApi({ agentLoginList: vi.fn(() => new Promise<AgentLogin[]>((resolve) => { answers.push(resolve) })) })
    const { result } = renderHook(() => useAgentLogins(client, true))
    let newest = Promise.resolve()
    act(() => { newest = result.current.reload() })
    await act(async () => {
      answers[1]!(logins)
      await newest
    })
    await act(async () => answers[0]!([logins[0]!]))
    expect(result.current.logins).toEqual(logins)
  })

  it("shows nothing of the member the gateway was bound to before, and drops that member's late answer", async () => {
    useStore.setState({ identityKey: 'member:alice' })
    onTestFinished(() => { useStore.setState({ identityKey: null }) })
    const answers: ((list: AgentLogin[]) => void)[] = []
    const client = fakeApi({ agentLoginList: vi.fn(() => new Promise<AgentLogin[]>((resolve) => { answers.push(resolve) })) })
    const { result } = renderHook(() => useAgentLogins(client, true))
    await act(async () => answers[0]!(logins))
    expect(result.current.logins).toEqual(logins)

    const late = act(() => { result.current.reload() })
    await late
    act(() => useStore.setState({ identityKey: 'member:bob' }))
    expect(result.current.logins).toBeNull()
    await act(async () => answers[1]!(logins))
    expect(result.current.logins).toBeNull()
    const bobs = [{ id: 'default', name: 'default', automatic: true, shared: true, position: 0 }]
    await act(async () => answers[2]!(bobs))
    expect(result.current.logins).toEqual(bobs)
  })

  it('links only the sessions a refusal lists, never a login named like one', async () => {
    const user = userEvent.setup()
    const named = { ...logins[1]!, name: 'run-abcdefghij' }
    const client = fakeApi({ agentLoginRemove: vi.fn(async () => { throw new Error('login "run-abcdefghij" is in use by your sessions run-7kq2m9x4ab, run-8kq2m9x4ab (end them)') }) })
    render(<LoginList logins={[logins[0]!, named]} sharedWith={[]} client={client} onChanged={async () => {}} />)
    await user.click(screen.getByRole('button', { name: 'Remove run-abcdefghij' }))
    const dialog = screen.getByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
    expect(await within(dialog).findByRole('button', { name: 'Open run-7kq2m9x4ab' })).toBeDefined()
    expect(within(dialog).getByRole('button', { name: 'Open run-8kq2m9x4ab' })).toBeDefined()
    expect(within(dialog).queryByRole('button', { name: 'Open run-abcdefghij' })).toBeNull()
  })
})

function addLogin(props: Partial<ComponentProps<typeof AddLogin>> & Pick<ComponentProps<typeof AddLogin>, 'client'>) {
  return render(<AddLogin sharedWith={null} logins={null} onChanged={async () => {}} running={null} ended={null} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} {...props} />)
}

const signInSockets = () => StubSocket.opened.filter((socket) => socket.url.includes('agent_login='))
const accept = (socket: StubSocket) => act(() => {
  socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ ok: true, has_control: true, cols: 80, rows: 24, replay: 0 }) })
})
const inputs = (socket: StubSocket) => socket.frames().filter((frame) => (frame as { type?: string }).type === 'input')

describe('add login', () => {
  beforeEach(() => StubSocket.install())

  it('shows a refused name verbatim and starts no sign-in', async () => {
    const user = userEvent.setup()
    const refusal = 'login name "Work": use lowercase letters, digits and \'-\', starting with a letter or digit'
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error(refusal) }) })
    addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect(client.agentLoginAdd).toHaveBeenCalledWith('claude', 'Work')
    expect((await screen.findByRole('alert')).textContent).toBe(`Sign-in not started${refusal}`)
    expect(client.agentLoginSocket).not.toHaveBeenCalled()
  })

  it('opens the sign-in on the new login, waits on nothing itself, and shows the outcome the page passes', async () => {
    const user = userEvent.setup()
    const onRunning = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000001' })) })
    const { rerender } = addLogin({ client, onRunning })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-new00000001'))
    expect(onRunning).toHaveBeenCalledWith({ id: 'lgn-new00000001', name: 'work' })
    expect(client.agentLoginResult).not.toHaveBeenCalled()
    const login = { id: 'lgn-new00000001', name: 'work', kind: 'subscription' as const, plan: 'pro', email: 'alice@work.example.test', automatic: true, shared: true, position: 1 }
    rerender(<AddLogin client={client} sharedWith={null} logins={null} onChanged={async () => {}} running={null} ended={{ id: 'lgn-new00000001', outcome: { login } }} onRunning={onRunning} onOpened={() => {}} onClose={() => {}} />)
    expect(await screen.findByRole('heading', { name: 'Added work' })).toBeDefined()
    expect(screen.getAllByRole('status')[0]!.textContent).toBe('Subscription · pro · alice@work.example.test')
    expect(screen.queryByText('Billed per use')).toBeNull()
    expect(screen.getByRole('switch', { name: 'Automatic' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByText('Sessions can move here when the automatic logins above it run out.')).toBeDefined()
    expect(screen.getByRole('switch', { name: 'Use for shares' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByText('Sessions of teammates you share your account with can run here too.')).toBeDefined()
    expect(screen.getByRole('region', { name: 'Sign in to Claude Code' }).textContent).not.toContain('Sign in with the account for')
  })

  it('turns the added login\'s switches through the row\'s calls, shows what the server keeps, and a refusal verbatim', async () => {
    const user = userEvent.setup()
    const login = { id: 'lgn-new00000010', name: 'api', kind: 'console' as const, per_use: true, organization_name: 'Alice API', automatic: true, shared: true, position: 3 }
    let answer = () => {}
    const client = fakeApi({
      agentLoginAutomatic: vi.fn(() => new Promise<unknown>((resolve) => { answer = () => resolve({}) })),
      agentLoginShare: vi.fn(async () => { throw new Error('agent.login.share: store: not found') }),
    })
    const onChanged = vi.fn(async () => {})
    const { rerender } = addLogin({ client, sharedWith: [bob], onChanged, ended: { id: login.id, outcome: { login } }, running: { id: login.id, name: 'api' }, resume: true })
    expect(screen.getByText(`Sessions of ${bob.display_name} can run here too, charged per use to Alice API.`)).toBeDefined()
    expect(screen.getByTitle('Billed per use to Alice API')).toBeDefined()

    await user.click(screen.getByRole('switch', { name: 'Automatic' }))
    expect(client.agentLoginAutomatic).toHaveBeenCalledWith('claude', login.id, false)
    expect(screen.getByRole('switch', { name: 'Automatic' })).toHaveProperty('disabled', true)
    expect(screen.getByText('Saving…')).toBeDefined()
    await act(async () => answer())
    expect(onChanged).toHaveBeenCalledTimes(1)
    rerender(<AddLogin client={client} sharedWith={[bob]} logins={[{ ...login, automatic: false }]} onChanged={onChanged} running={null} ended={{ id: login.id, outcome: { login } }} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} />)
    expect(screen.getByRole('switch', { name: 'Automatic' }).getAttribute('aria-checked')).toBe('false')
    expect(screen.getByText('No share reaches it while Automatic is off.')).toBeDefined()

    await user.click(screen.getByRole('switch', { name: 'Use for shares' }))
    expect(client.agentLoginShare).toHaveBeenCalledWith('claude', login.id, false)
    expect((await screen.findByRole('alert')).textContent).toBe('agent.login.share: store: not found')
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it('says where in the order the added login landed once the list is read again, and any login billed per use above it', () => {
    const console = { id: 'lgn-console0001', name: 'console', kind: 'console' as const, per_use: true, organization_name: 'Alice API', automatic: true, shared: true, position: 1 }
    const spare = { id: 'lgn-spare000001', name: 'spare', kind: 'subscription' as const, automatic: true, shared: true, position: 1 }
    const api = { id: 'lgn-api00000001', name: 'api', kind: 'console' as const, per_use: true, automatic: true, shared: true, position: 2 }
    const base = { id: 'default', name: 'default', kind: 'subscription' as const, automatic: true, shared: true, position: 0 }
    const { unmount } = addLogin({ client: fakeApi(), logins: null, ended: { id: spare.id, outcome: { login: spare } }, running: { id: spare.id, name: 'spare' }, resume: true })
    expect(screen.queryByText(/^Added as number/)).toBeNull()
    unmount()

    const again = addLogin({ client: fakeApi(), logins: [base, spare, { ...console, position: 2 }], ended: { id: spare.id, outcome: { login: spare } }, running: { id: spare.id, name: 'spare' }, resume: true })
    expect(screen.getByText(/^Added as number/).textContent).toBe('Added as number 2 in your order.')
    again.unmount()

    addLogin({ client: fakeApi(), logins: [base, console, api], ended: { id: api.id, outcome: { login: api } }, running: { id: api.id, name: 'api' }, resume: true })
    expect(screen.getByText(/^Added as number/).textContent).toBe('Added as number 3 in your order, after console, billed per use.')
  })

  it('says before the sign-in what the new login is used for, and after it that a Console login is billed per use and charged for whom', async () => {
    const user = userEvent.setup()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000009' })) })
    const { unmount } = addLogin({ client })
    expect(screen.getByRole('form', { name: 'Add a Claude Code login' }).textContent).toContain('Automatic and Use for shares both start on: Cloudfield may start or move your sessions onto it, and teammates you share your account with can use it too. If you sign in to a Console account, its organisation is charged per use for those sessions, yours and theirs. You can turn either off on the next screen as soon as the login is added.')
    unmount()

    const { rerender } = addLogin({ client, sharedWith: [bob] })
    expect(screen.getByRole('form', { name: 'Add a Claude Code login' }).textContent).toContain(`and sessions of ${bob.display_name} can run on it too.`)
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'api')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    await screen.findByRole('heading', { name: 'Sign in to api' })
    const login = { id: 'lgn-new00000009', name: 'api', kind: 'console' as const, per_use: true, email: 'alice@example.test', organization_name: 'Alice API', automatic: true, shared: true, position: 3 }
    rerender(<AddLogin client={client} sharedWith={[bob]} logins={null} onChanged={async () => {}} running={null} ended={{ id: 'lgn-new00000009', outcome: { login } }} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} />)
    expect(await screen.findByRole('heading', { name: 'Added api' })).toBeDefined()
    expect(screen.getAllByRole('status')[0]!.textContent).toBe('Console · alice@example.test · Alice APIBilled per useBilled per use to Alice API')
    const described = (name: string) => document.getElementById(screen.getByRole('switch', { name }).getAttribute('aria-describedby')!)!.textContent
    expect(described('Automatic')).toBe('Sessions can move here when the automatic logins above it run out, charged per use to Alice API.')
    expect(described('Use for shares')).toBe(`Sessions of ${bob.display_name} can run here too, charged per use to Alice API.`)
  })

  it('shows the server refusing the sign-in verbatim and starts again', async () => {
    const user = userEvent.setup()
    const refusal = 'this sign-in is the account and organisation your login "default" is signed in to, so it was not added; sign in to another account to add a login'
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000002' })) })
    const { rerender } = addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'twin')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    await screen.findByRole('heading', { name: 'Sign in to twin' })
    rerender(<AddLogin client={client} sharedWith={null} logins={null} onChanged={async () => {}} running={null} ended={{ id: 'lgn-new00000002', outcome: { refused: refusal } }} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} />)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Login twin was not added')
    expect(within(alert).getByText(refusal)).toBeDefined()
    await user.click(within(alert).getByRole('button', { name: 'Start again' }))
    expect(screen.getByRole('textbox', { name: 'Name' })).toBeDefined()
  })

  it('returns to a sign-in still running when the server refuses another', async () => {
    const user = userEvent.setup()
    const refusal = 'store: conflict: the sign-in of login "work" (lgn-run00000001) is still running; finish it first'
    const onRunning = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error(refusal) }) })
    addLogin({ client, onRunning })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const running = await screen.findByRole('status')
    expect(running.textContent).toContain('Sign-in for work is still running')
    expect(within(running).getByText(refusal)).toBeDefined()
    expect(onRunning).toHaveBeenCalledWith({ id: 'lgn-run00000001', name: 'work' })
    expect(screen.queryByRole('button', { name: 'Start sign-in' })).toBeNull()
    expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull()
    const back = within(running).getByRole('button', { name: 'Return to the sign-in' })
    expect(back.className).toContain('bg-action')
    await user.click(back)
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-run00000001'))
    expect(screen.getByRole('heading', { name: 'Sign in to work' })).toBeDefined()
    expect(screen.getByRole('region', { name: 'Sign in to Claude Code' }).textContent).toContain('Sign in with the account for work.')
  })

  it('offers a sign-in the page knows is running before any refusal, and opens it at once to resume', async () => {
    const user = userEvent.setup()
    const client = fakeApi()
    const running = { id: 'lgn-run00000002', name: 'spare' }
    const { unmount } = addLogin({ client, running })
    expect(screen.getByRole('status').textContent).toContain('Sign-in for spare is still running')
    expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Return to the sign-in' }))
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-run00000002'))
    unmount()

    addLogin({ client, running, resume: true })
    expect(screen.getByRole('heading', { name: 'Sign in to spare' })).toBeDefined()
  })

  it('says how long a sign-in this page started has left, and only that one', async () => {
    const user = userEvent.setup()
    const onOpened = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000004' })) })
    const { unmount } = addLogin({ client, onOpened })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const panel = await screen.findByRole('region', { name: 'Sign in to Claude Code' })
    expect(panel.textContent).toContain('You can leave this page: the sign-in keeps running until 15 minutes after its start, and Add login offers Return to the sign-in.')
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    accept(signInSockets()[0]!)
    expect(onOpened).toHaveBeenCalledWith('lgn-new00000004')
    unmount()

    const opened = Date.now() - 5 * 60_000
    addLogin({ client, running: { id: 'lgn-new00000004', name: 'spare', opened }, resume: true, onOpened })
    expect(screen.getByRole('region', { name: 'Sign in to Claude Code' }).textContent).toContain('keeps running for 10 more minutes, and')
    await waitFor(() => expect(signInSockets()).toHaveLength(2))
    accept(signInSockets()[1]!)
    expect(onOpened).toHaveBeenCalledTimes(1)
  })

  it('ends the sign-in only through Cancel sign-in, which sends Ctrl-C, after the terminal is up if it is still starting', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000003' })) })
    addLogin({ client, onClose })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const panel = await screen.findByRole('region', { name: 'Sign in to Claude Code' })
    expect(within(panel).queryByRole('button', { name: 'Close' })).toBeNull()
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    const socket = signInSockets()[0]!

    await user.click(within(panel).getByRole('button', { name: 'Cancel sign-in' }))
    expect(onClose).not.toHaveBeenCalled()
    expect(within(panel).getByRole('status').textContent).toBe('Cancelling: the sign-in ends as soon as its terminal is up.')
    expect(within(panel).getByRole('button', { name: 'Cancel sign-in' })).toHaveProperty('disabled', true)
    accept(socket)
    expect(inputs(socket)).toEqual([{ type: 'input', data: '\x03' }])
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('sends Ctrl-C at once to a sign-in whose terminal is up', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    addLogin({ client: fakeApi(), running: { id: 'lgn-run00000005', name: 'spare' }, resume: true, onClose })
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    const socket = signInSockets()[0]!
    accept(socket)
    await user.click(screen.getByRole('button', { name: 'Cancel sign-in' }))
    expect(inputs(socket)).toEqual([{ type: 'input', data: '\x03' }])
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('keeps a sign-in whose socket dropped running and reattaches to it', async () => {
    addLogin({ client: fakeApi(), running: { id: 'lgn-run00000006', name: 'spare' }, resume: true })
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    accept(signInSockets()[0]!)
    act(() => signInSockets()[0]!.onclose?.({ code: 1006 }))
    const panel = screen.getByRole('region', { name: 'Sign in to Claude Code' })
    expect(within(panel).getByRole('status').textContent).toBe('Reconnecting to the sign-in…')
    expect(within(panel).getByRole('button', { name: 'Cancel sign-in' })).toBeDefined()
    expect(within(panel).queryByRole('button', { name: 'Done' })).toBeNull()
    await waitFor(() => expect(signInSockets()).toHaveLength(2), { timeout: 3000 })
    expect(signInSockets()[1]!.url).toBe(signInSockets()[0]!.url)
    accept(signInSockets()[1]!)
    expect(within(panel).getByRole('status').textContent).toBe('Starting the sign-in…')
  })

  it('stops offering a sign-in another tab refused for once that sign-in has ended', async () => {
    const user = userEvent.setup()
    const refusal = 'store: conflict: the sign-in of login "work" (lgn-run00000003) is still running; finish it first'
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error(refusal) }) })
    const { rerender } = addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect(await screen.findByRole('button', { name: 'Return to the sign-in' })).toBeDefined()
    const login = { id: 'lgn-run00000003', name: 'work', kind: 'subscription' as const, automatic: true, shared: true, position: 1 }
    rerender(<AddLogin client={client} sharedWith={null} logins={null} onChanged={async () => {}} running={null} ended={{ id: 'lgn-run00000003', outcome: { login } }} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} />)
    expect(screen.queryByRole('button', { name: 'Return to the sign-in' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Start sign-in' })).toHaveProperty('disabled', false)
  })

  it('shows one outcome and Start again for a sign-in the server ended, not also Try again', async () => {
    const refused = 'agent.login.result: the sign-in container did not start: image pull failed'
    const running = { id: 'lgn-run00000007', name: 'spare' }
    const client = fakeApi()
    const { rerender } = addLogin({ client, running, resume: true })
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    rerender(<AddLogin client={client} sharedWith={null} logins={null} onChanged={async () => {}} running={null} ended={{ id: running.id, outcome: { refused } }} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} />)
    act(() => {
      signInSockets()[0]!.onopen?.()
      signInSockets()[0]!.onmessage?.({ data: JSON.stringify({ ok: false, error: 'agent login lgn-run00000007 has ended' }) })
    })
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(screen.getByRole('alert').textContent).toContain(refused)
    expect(screen.getByRole('button', { name: 'Start again' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
  })

  it("opens only the vendor's sign-in pages from the terminal, and shows any other link as text", async () => {
    addLogin({ client: fakeApi(), running: { id: 'lgn-run00000008', name: 'spare' }, resume: true })
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    onTestFinished(() => open.mockRestore())
    expect(xterm.onLink!('https://claude.ai/oauth/authorize?code=true')).toBe(false)
    act(() => { expect(xterm.onLink!('https://claude.ai.example.test/oauth/authorize')).toBe(true) })
    expect(screen.getByText('https://claude.ai.example.test/oauth/authorize').closest('p')!.textContent).toBe('Not opened, as it is not a Claude sign-in page: https://claude.ai.example.test/oauth/authorize')
    act(() => { expect(xterm.onLink!('http://claude.ai/oauth/authorize')).toBe(true) })
    expect(open).not.toHaveBeenCalled()
  })

  it('remembers no sign-in started for a member the gateway is no longer bound to', async () => {
    const user = userEvent.setup()
    useStore.setState({ identityKey: 'member:alice' })
    onTestFinished(() => { useStore.setState({ identityKey: null }) })
    let answer = (_: { login_id: string }) => {}
    const onRunning = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(() => new Promise<{ login_id: string }>((resolve) => { answer = resolve })) })
    addLogin({ client, onRunning })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    act(() => useStore.setState({ identityKey: 'member:bob' }))
    await act(async () => answer({ login_id: 'lgn-alice000001' }))
    expect(onRunning).not.toHaveBeenCalled()
    expect(client.agentLoginSocket).not.toHaveBeenCalled()
  })

  it('offers no return for any other refusal', async () => {
    const user = userEvent.setup()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error('store: conflict: you already have a login named "work"') }) })
    addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Sign-in not startedstore: conflict: you already have a login named "work"')
    expect(screen.queryByRole('button', { name: 'Return to the sign-in' })).toBeNull()
  })
})
