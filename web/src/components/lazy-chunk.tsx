import { Component, lazy, Suspense, type ComponentType, type ReactNode } from 'react'
import { Callout } from '@/components/ui/callout'
import { message } from '@/lib/format'

const reloadedAt = 'cloudfield.chunk-reloaded-at'
const reloadEveryMs = 60_000

class ChunkError extends Component<{ children: ReactNode }, { error?: unknown }> {
  state: { error?: unknown } = {}

  static getDerivedStateFromError(error: unknown) {
    return { error }
  }

  render() {
    if (this.state.error === undefined) return this.props.children
    return <Callout tone="failed" role="alert" className="m-2 whitespace-pre-wrap">{message(this.state.error)}</Callout>
  }
}

/**
 * A component whose code is fetched when it first renders. A server update
 * replaces the build, so a tab opened before it asks for a file that is gone:
 * the page reloads once onto the new build, and a failure right after a
 * reload shows the error instead.
 */
export function lazyChunk<P extends object>(load: () => Promise<ComponentType<P>>, fallback: ReactNode) {
  const Loaded = lazy(() =>
    load().then(
      (component) => ({ default: component }),
      (err: unknown) => {
        if (Date.now() - Number(window.sessionStorage.getItem(reloadedAt)) > reloadEveryMs) {
          window.sessionStorage.setItem(reloadedAt, String(Date.now()))
          window.location.reload()
        }
        throw err
      },
    ),
  )
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
