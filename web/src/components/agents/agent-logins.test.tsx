import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AddLogin } from '@/components/agents/add-login'
import { LoginList } from '@/components/agents/agent-logins'
import type { Api } from '@/lib/api'
import type { AgentLogin, Member } from '@/lib/types'
import { alice, bob, fakeApi } from '@/test/fixtures'

const logins: AgentLogin[] = [
  { id: 'default', name: 'default', kind: 'subscription', plan: 'max', email: 'alice@example.test', organization_name: 'Alice personal', automatic: true, shared: true, position: 0 },
  { id: 'lgn-work0000001', name: 'work', kind: 'subscription', plan: 'pro', email: 'alice@work.example.test', organization_name: 'Example Co', automatic: true, shared: false, position: 1 },
  { id: 'lgn-console0001', name: 'console', kind: 'console', email: 'alice@example.test', organization_name: 'Alice API', per_use: true, automatic: false, shared: true, position: 2 },
]

function show(over: Parameters<typeof fakeApi>[0] = {}, sharedWith: Member[] = [bob]) {
  const client = fakeApi(over)
  const onChanged = vi.fn()
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

    expect(row('default').textContent).toContain('Subscription · max')
    expect(row('default').textContent).toContain('alice@example.test · Alice personal')
    expect(row('default').textContent).toContain(`Shares reach it: ${bob.display_name}`)
    expect(await within(row('default')).findByRole('progressbar', { name: 'Login default 5-hour usage' })).toBeDefined()
    expect(row('work').textContent).toContain('Off for shares: your sessions only')
    expect(within(row('work')).queryByText('Billed per use')).toBeNull()
    expect(within(row('console')).getByText('Billed per use')).toBeDefined()
    expect(row('console').textContent).toContain('No share reaches it while Automatic is off')
    expect(screen.getByText(`Your account is shared with ${bob.display_name}: logins on for shares serve their sessions too.`, { exact: false })).toBeDefined()
    expect(client.accountUsage).toHaveBeenCalledWith({})
    expect(client.accountUsage).toHaveBeenCalledWith({ login: 'lgn-work0000001' })

    expect(within(row('default')).getByRole('button', { name: 'Rename default' })).toHaveProperty('disabled', true)
    expect(within(row('default')).queryByRole('button', { name: 'Remove default' })).toBeNull()
    expect(within(row('default')).getByRole('button', { name: 'Move default up' })).toHaveProperty('disabled', true)
    expect(within(row('console')).getByRole('button', { name: 'Move console down' })).toHaveProperty('disabled', true)
  })

  it('says when the account is shared with nobody', () => {
    show({}, [])
    expect(row('default').textContent).toContain('No share reaches it: your account is not shared')
    expect(screen.getByText('Your account is not shared with anyone.', { exact: false })).toBeDefined()
  })

  it('sends the whole order, Automatic and a rename, then reads the list again', async () => {
    const user = userEvent.setup()
    const { client, onChanged } = show({
      agentLoginOrder: vi.fn(async () => ({})),
      agentLoginAutomatic: vi.fn(async () => ({})),
      agentLoginRename: vi.fn(async () => ({})),
    })
    await user.click(screen.getByRole('button', { name: 'Move work down' }))
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

    await user.click(screen.getByRole('button', { name: 'Move work up' }))
    expect((await screen.findByRole('alert')).textContent).toBe('login_ids must name each of your logins once, default included')
  })

  it('turns Use for shares through agent.login.share, and shows its refusal verbatim', async () => {
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
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("shows a login's usage error verbatim when it has no windows", async () => {
    show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => ({
        account_member_id: alice.id,
        login: params.login,
        providers: params.login === 'lgn-console0001'
          ? [{ provider: 'claude' as const, status: 'unsupported' as const, windows: [], checked_at: '2026-11-26T10:00:00Z', error: 'claude: a Console login reports no usage windows' }]
          : [],
      })),
    })
    expect(await within(row('console')).findByText('claude: a Console login reports no usage windows')).toBeDefined()
    expect(row('console').textContent).toContain('Usage not read: ')
  })

  it('confirms a removal, says Anthropic keeps the sign-in, and shows a refusal verbatim', async () => {
    const user = userEvent.setup()
    const refusal = 'login "work" is in use by your sessions run-7kq2m9x4ab (end them)'
    const { client, onChanged } = show({ agentLoginRemove: vi.fn(async () => { throw new Error(refusal) }) })
    await user.click(screen.getByRole('button', { name: 'Remove work' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove login work?' })
    expect(dialog.textContent).toContain('It does not sign the login out at Anthropic')
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
    expect(client.agentLoginRemove).toHaveBeenCalledWith('claude', 'lgn-work0000001')
    expect(await within(dialog).findByText(refusal)).toBeDefined()
    expect(onChanged).not.toHaveBeenCalled()
  })
})

describe('add login', () => {
  it('shows a refused name verbatim and starts no sign-in', async () => {
    const user = userEvent.setup()
    const refusal = 'login name "Work": use lowercase letters, digits and \'-\', starting with a letter or digit'
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error(refusal) }) })
    render(<AddLogin client={client} onClose={() => {}} />)
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect(client.agentLoginAdd).toHaveBeenCalledWith('claude', 'Work')
    expect((await screen.findByRole('alert')).textContent).toBe(`Not started${refusal}`)
    expect(client.agentLoginSocket).not.toHaveBeenCalled()
    expect(client.agentLoginResult).not.toHaveBeenCalled()
  })

  it('opens the sign-in on the new login and shows what was added', async () => {
    const user = userEvent.setup()
    const client = fakeApi({
      agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000001' })),
      agentLoginResult: vi.fn(async () => ({ id: 'lgn-new00000001', name: 'work', kind: 'subscription' as const, plan: 'pro', email: 'alice@work.example.test', automatic: true, shared: true, position: 1 })),
    })
    render(<AddLogin client={client} onClose={() => {}} />)
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-new00000001'))
    expect(client.agentLoginResult).toHaveBeenCalledWith('claude', 'lgn-new00000001')
    expect(await screen.findByText('Added login work')).toBeDefined()
    expect(screen.getByText('Subscription · pro · alice@work.example.test')).toBeDefined()
  })

  it('shows the server refusing the sign-in verbatim and starts again', async () => {
    const user = userEvent.setup()
    const refusal = 'this sign-in is the account and organisation your login "default" is signed in to, so it was not added; sign in to another account to add a login'
    const client = fakeApi({
      agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000002' })),
      agentLoginResult: vi.fn(async () => { throw new Error(refusal) }),
    })
    render(<AddLogin client={client} onClose={() => {}} />)
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'twin')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Login twin was not added')
    expect(within(alert).getByText(refusal)).toBeDefined()
    await user.click(within(alert).getByRole('button', { name: 'Start again' }))
    expect(screen.getByRole('textbox', { name: 'Name' })).toBeDefined()
  })
})
