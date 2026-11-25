import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Input } from '@/components/ui/input'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LinearStatus } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { loadLinear } from '@/store/linear'

const link = 'text-accent underline-offset-2 hover:underline'

export function LinearSection({ client }: { client: Api }) {
  const served = useCapability().hasMethod('linear.status')
  const status = useStore((s) => s.linear)
  const writeLinear = useStore((s) => s.writeLinear)
  const navigate = useStore((s) => s.navigate)
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!served) return
    void loadLinear(useStore, client)
  }, [client, served])

  const run = async (call: () => Promise<LinearStatus | null>) => {
    setBusy(true)
    setError(null)
    try {
      writeLinear(await call())
      setKey('')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  if (!served) return null

  return (
    <SettingsSection title="Linear" id="linear" scope="Your account">
      {status === null ? (
        <SettingRow label="Personal API key" help="Loading…" />
      ) : status.connected ? (
        <SettingRow
          label={status.error ? 'Personal API key saved' : `Connected as ${status.name}${status.organization ? ` (${status.organization})` : ''}`}
          help={status.error ? 'A key is saved, but the status read failed. The error is below; Disconnect removes the key.' : 'Start session on the Linear page launches with the issue linked.'}
          control={
            <span className="flex items-center gap-2">
              {!status.error && <Button size="sm" onClick={() => navigate('linear')}>Open Linear</Button>}
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => void run(async () => { await client.linearDisconnect(); return { connected: false } })}>Disconnect</Button>
            </span>
          }
        >
          {(error ?? status.error) && <Callout tone="failed" role="alert">{error ?? status.error}</Callout>}
        </SettingRow>
      ) : (
        <SettingRow
          label="Personal API key"
          labelFor="linear-api-key"
          help={<>Create one in Linear under Settings → Account → Security &amp; access; <a className={link} href="https://linear.app/docs/api-and-webhooks" target="_blank" rel="noreferrer">Linear&apos;s API guide</a> shows where. Save checks the key with Linear first. The server keeps it for your account only.</>}
        >
          <form
            className="flex min-w-0 flex-wrap items-center gap-2"
            onSubmit={(event) => { event.preventDefault(); void run(() => client.linearConnect(key.trim())) }}
          >
            <Input
              id="linear-api-key"
              type="password"
              autoComplete="off"
              placeholder="lin_api_…"
              value={key}
              onChange={(event) => setKey(event.target.value)}
              className="min-w-0 flex-[1_1_16rem] font-mono"
            />
            <Button type="submit" size="sm" disabled={busy || key.trim() === ''}>{busy ? 'Saving…' : 'Save'}</Button>
          </form>
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
        </SettingRow>
      )}
      <SettingRow
        label="Pull request links"
        help={<>Linear links a pull request to its issue through <a className={link} href="https://linear.app/docs/github" target="_blank" rel="noreferrer">Linear&apos;s GitHub integration</a>.</>}
      />
    </SettingsSection>
  )
}
