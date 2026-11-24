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

test('a streaming code block is highlighted once, when another block follows', () => {
  const open = 'Here:\n\n```ts\nconst a = 1\n'
  const view = render(<Markdown text={open} live />)
  view.rerender(<Markdown text={`${open}const b = 2\n`} live />)
  const closed = `${open}const b = 2\nconst c = 3\n\`\`\`\n`
  view.rerender(<Markdown text={closed} live />)
  const pre = view.container.querySelector('pre')!
  const code = pre.querySelector('code')!
  expect(code.textContent).toBe('const a = 1\nconst b = 2\nconst c = 3')
  expect(code.children).toHaveLength(0)
  expect(highlightCode).not.toHaveBeenCalled()

  view.rerender(<Markdown text={`${closed}\nDone.`} live />)
  expect(highlightCode).toHaveBeenCalledTimes(1)
  expect(view.container.querySelector('pre')).toBe(pre)
  expect(pre.querySelector('.tok-keyword')?.textContent).toBe('const')

  view.rerender(<Markdown text={`${closed}\nDone. More.`} live />)
  view.rerender(<Markdown text={`${closed}\nDone. More.`} />)
  expect(highlightCode).toHaveBeenCalledTimes(1)
})

test('inline code in the last paragraph keeps its element when a new block arrives', () => {
  const text = 'Run `make` first.'
  const view = render(<Markdown text={text} live />)
  const code = view.container.querySelector('code')!
  view.rerender(<Markdown text={`${text}\n\nThen wait.`} live />)
  expect(view.container.querySelector('code')).toBe(code)
})

test('an open code block is highlighted when the reply ends or is not live', () => {
  const open = '```ts\nconst a = 1\n'
  const view = render(<Markdown text={open} live />)
  const pre = view.container.querySelector('pre')!
  expect(highlightCode).not.toHaveBeenCalled()
  view.rerender(<Markdown text={open} />)
  expect(highlightCode).toHaveBeenCalledTimes(1)
  expect(view.container.querySelector('pre')).toBe(pre)
  cleanup()
  render(<Markdown text={open} />)
  expect(highlightCode).toHaveBeenCalledTimes(2)
})
