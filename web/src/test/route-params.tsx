import { RunParamsContext } from '@/routes/run/agent-context'
import { useStore } from '@/store'

/** Stands in for the session frame around a view rendered alone: the route's params. */
export function RouteParams({ children }: { children: React.ReactNode }) {
  return <RunParamsContext value={useStore((s) => s.route.params)}>{children}</RunParamsContext>
}
