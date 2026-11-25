// A right-click on Chat opens the session's More menu at the cursor, and
// Leave a comment, from there or from More, puts the cursor in the note field.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

const agentShim = 'sh /workspace/agent.sh'
const task = 'write the result file'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test('right-click on Chat opens the session menu at the cursor', async ({ page, aether }, info) => {
  await page.emulateMedia({ colorScheme: 'dark' })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.continue().click()
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  aether.installAgent(await memberID(alice), 'claude', agentShim)
  await wizard.agent.skip().click()
  await wizard.expectStep('First session')
  await wizard.firstRun.launch('claude', task)
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()

  await page.getByRole('tab', { name: 'Chat', exact: true }).click()
  const chat = page.getByRole('tabpanel', { name: 'Chat' })
  const box = (await chat.boundingBox())!
  const point = { x: box.x + box.width / 2, y: box.y + 80 }
  await page.mouse.click(point.x, point.y, { button: 'right' })

  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  const opened = (await menu.boundingBox())!
  expect(Math.abs(opened.x - point.x)).toBeLessThan(2)
  expect(Math.abs(opened.y - point.y)).toBeLessThan(2)
  await page.screenshot({ path: info.outputPath('session-menu.png') })

  await menu.getByRole('menuitem', { name: 'Leave a comment…' }).click()
  const note = page.getByRole('textbox', { name: 'Add a note' })
  await expect(note).toBeFocused()
  await page.screenshot({ path: info.outputPath('leave-comment.png') })

  await note.blur()
  await page.locator('header').filter({ hasText: task }).getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Leave a comment…' }).click()
  await expect(note).toBeFocused()
  // The closing menu would restore focus to More a moment later.
  await page.waitForTimeout(300)
  await expect(note).toBeFocused()
})
