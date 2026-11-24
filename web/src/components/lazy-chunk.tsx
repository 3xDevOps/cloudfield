import { Component, lazy, Suspense, type ComponentType, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { message } from '@/lib/format'

const starts: (() => Promise<unknown>)[] = []

/** Fetches every lazy chunk now, so that none is fetched when its view opens. */
export function preloadChunks() {
  for (const start of starts) start().catch(() => {})
}

class ChunkError extends Component<{ children: ReactNode }, { error?: unknown }> {
  state: { error?: unknown } = {}

  static getDerivedStateFromError(error: unknown) {
    return { error }
  }

  render() {
    if (this.state.error === undefined) return this.props.children
    return (
      <Callout
        tone="failed"
        role="alert"
        className="m-2 whitespace-pre-wrap"
        actions={<Button size="sm" variant="secondary" onClick={() => window.location.reload()}>Reload</Button>}
      >
        {message(this.state.error)}
      </Callout>
    )
  }
}

/** A component whose code is kept out of the first-page script. */
export function lazyChunk<P extends object>(load: () => Promise<ComponentType<P>>, fallback: ReactNode) {
  let loading: Promise<{ default: ComponentType<P> }> | undefined
  const start = () => (loading ??= load().then((component) => ({ default: component })))
  starts.push(start)
  const Loaded = lazy(start)
  return function LazyChunk(props: P) {
    return (
      <ChunkError>
        <Suspense fallback={fallback}>
          <Loaded {...props} />
        </Suspense>
      </ChunkError>
    )
  }
}
