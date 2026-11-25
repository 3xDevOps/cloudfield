import ClientRuntime from './client-runtime'

export default function Page() {
  return (
    <>
      <ClientRuntime />
      {/* Outside the client-only boundary, so React hydrates this node and its running fade instead of replacing it. */}
      <div aria-hidden="true" className="grid h-full place-items-center [[data-app]~&]:hidden">
        <div
          className="h-36 max-h-[24vh] w-52 animate-[fade-in_200ms_var(--ease-enter)_300ms_backwards] bg-text/15 motion-reduce:animate-[fade-in_200ms_step-end_300ms_backwards]"
          style={{ mask: 'url(/cloudfield-mark.svg) center / contain no-repeat' }}
        />
      </div>
    </>
  )
}
