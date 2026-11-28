// A server with no edge cannot mint a connector code; Settings, reached from
// the palette, links to the edge setup guide before offering connector setup.

import { expect, test } from './fixtures'

test('Settings shows why this server cannot connect ChatGPT or Claude', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await page.goto(alice.url)
  await expect(page.getByRole('navigation', { name: 'Cloudfield' })).toBeVisible()
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByRole('option', { name: 'Connect ChatGPT or Claude' }).click()
  const section = page.locator('#connectors')
  await expect(section).toBeInViewport()
  await expect(section.getByText('No connectors yet')).toBeVisible()
  const unavailable = section.locator('[data-slot=callout]').filter({ hasText: 'This server has no edge' })
  await expect(unavailable.getByRole('link', { name: 'Edge guide' })).toBeVisible()
  await expect(section.getByRole('button', { name: 'Get a code' })).toHaveCount(0)
  await expect(section.getByRole('link', { name: 'ChatGPT Plugins' })).toHaveCount(0)
})

// Under edge-access account with a GitHub account linked, connector.list
// says signing in replaces the code: Settings shows the universal URL at
// once, mints no code, and offers to move a connector added at the
// server's own URL. The edge is stubbed; the rest is the real server.
test('Settings shows the universal URL without a code and offers to move an older connector', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const universal = 'https://edge.example/mcp'
  const own = 'https://edge.example/mcp/srv'
  const minted: string[] = []
  await page.route('**/api/v1/connector.list', (route) => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({
      url: universal,
      universal_url: universal,
      sign_in: true,
      connectors: [{ id: 'con-7k2m9q4xbd', member_id: 'mem_1', client_id: 'https://chatgpt.com/oauth/client.json', client_name: 'ChatGPT', redirect_host: 'chatgpt.com', resource: own, scopes: ['read', 'write'], created_at: '2026-10-18T10:00:00Z' }],
    }),
  }))
  await page.route('**/api/v1/connector.code', (route) => {
    minted.push(route.request().url())
    return route.fallback()
  })
  await page.goto(`${alice.url}&page=settings`)
  await page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name: 'Connections' }).click()
  const section = page.locator('#connectors')
  await expect(section.getByRole('button', { name: `Copy ${universal}` })).toBeVisible()
  await expect(section.getByText('Server URL', { exact: true })).toBeVisible()
  await expect(section.getByText('Cloudfield', { exact: true }).first()).toBeVisible()
  await expect(section.getByRole('link', { name: 'ChatGPT Plugins' })).toHaveAttribute('href', 'https://chatgpt.com/plugins')
  await expect(section.getByText('Create as a plugin', { exact: true })).toBeVisible()
  await expect(section.getByText('Work', { exact: true })).toBeVisible()
  await expect(section.getByText(/No code is needed/)).toBeVisible()
  await expect(section.getByRole('button', { name: 'Get a code' })).toHaveCount(0)
  await expect(section.getByText(own)).toBeVisible()
  await section.getByRole('button', { name: 'Move ChatGPT (chatgpt.com) to the universal URL' }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog).toContainText(`Add Cloudfield again in ChatGPT with ${universal} and sign in. Once the new connection calls Cloudfield, this connector is revoked`)
  await dialog.getByRole('button', { name: 'Move' }).click()
  await expect(section.getByText('Waiting for ChatGPT to connect…')).toBeVisible()
  expect(minted).toEqual([])
})
