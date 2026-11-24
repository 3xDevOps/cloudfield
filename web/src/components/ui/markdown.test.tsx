import { cleanup, render } from '@testing-library/react'
import { highlightCode } from '@lezer/highlight'
import { afterEach, expect, test, vi } from 'vitest'
import { Markdown } from './markdown'

vi.mock('@lezer/highlight', async (original) => {
  const actual = await original<typeof import('@lezer/highlight')>()
  return { ...actual, highlightCode: vi.fn(actual.highlightCode) }
})

afterEach(() => {
  cleanup()
  vi.mocked(highlightCode).mockClear()
})

test('a streaming code block is highlighted once, when its fence closes', () => {
  const open = 'Here:\n\n```ts\nconst a = 1\n'
  const view = render(<Markdown text={open} live />)
  view.rerender(<Markdown text={`${open}const b = 2\n`} live />)
  view.rerender(<Markdown text={`${open}const b = 2\nconst c = 3\n`} live />)
  const code = view.container.querySelector('[data-lang="ts"] code')!
  expect(code.textContent).toBe('const a = 1\nconst b = 2\nconst c = 3')
  expect(code.children).toHaveLength(0)
  expect(highlightCode).not.toHaveBeenCalled()

  const closed = `${open}const b = 2\nconst c = 3\n\`\`\`\n`
  view.rerender(<Markdown text={closed} live />)
  expect(highlightCode).toHaveBeenCalledTimes(1)
  expect(view.container.querySelector('[data-lang="ts"] code .tok-keyword')?.textContent).toBe('const')

  view.rerender(<Markdown text={`${closed}\nDone.`} live />)
  view.rerender(<Markdown text={`${closed}\nDone.`} />)
  expect(highlightCode).toHaveBeenCalledTimes(1)
})

test('an open code block is highlighted when the reply ends or is not live', () => {
  const open = '```ts\nconst a = 1\n'
  const view = render(<Markdown text={open} live />)
  expect(highlightCode).not.toHaveBeenCalled()
  view.rerender(<Markdown text={open} />)
  expect(highlightCode).toHaveBeenCalledTimes(1)
  cleanup()
  render(<Markdown text={open} />)
  expect(highlightCode).toHaveBeenCalledTimes(2)
})
