'use client'

import dynamic from 'next/dynamic'

// App owns the existing live store, xterm host, Electron bridge, and gateway
// connection. Keep it behind a client-only dynamic boundary so static export
// does not evaluate browser-only modules during server prerendering.
const App = dynamic(() => import('@/App').then(({ App: DashboardApp }) => DashboardApp), {
  ssr: false,
  // Prerendered into index.html. The delay keeps it off screen on a fast load
  // and ahead of the desktop launch splash; reduced motion gets a cut, not a fade.
  loading: () => (
    <div className="grid h-full place-items-center">
      <div
        aria-hidden="true"
        className="h-36 max-h-[24vh] w-52 animate-[fade-in_200ms_var(--ease-enter)_300ms_backwards] bg-text/15 motion-reduce:animate-[fade-in_200ms_step-end_300ms_backwards]"
        style={{ mask: 'url(/cloudfield-mark.svg) center / contain no-repeat' }}
      />
    </div>
  ),
})

export default function ClientRuntime() {
  return <App />
}
