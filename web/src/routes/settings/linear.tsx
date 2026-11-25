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

function KeyForm({ label, busy, error, onSave }: { label: string; busy: boolean; error: string | null; onSave: (key: string) => void }) {
  const [key, setKey] = useState('')
  return (
    <>
      <form
        className="flex min-w-0 items-center gap-2"
        onSubmit={(event) => { event.preventDefault(); onSave(key.trim()) }}
      >
        <Input
          id="linear-api-key"
          type="password"
          autoComplete="off"
          placeholder="lin_api_…"
          value={key}
          onChange={(event) => setKey(event.target.value)}
          className="min-w-0 flex-[1_1_9rem] font-mono"
        />
        <Button type="submit" size="sm" disabled={busy || key.trim() === ''}>{busy ? 'Saving…' : label}</Button>
      </form>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
    </>
  )
}

export function LinearSection({ client }: { client: Api }) {
  const identityKey = useStore((s) => s.identityKey)
  return <LinearBody key={identityKey} client={client} />
}

function LinearBody({ client }: { client: Api }) {
  const served = useCapability().hasMethod('linear.status')
  const status = useStore((s) => s.linear)
  const writeLinear = useStore((s) => s.writeLinear)
  const navigate = useStore((s) => s.navigate)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [viewer] = useState(() => useStore.getState().identityKey)

  useEffect(() => {
    if (!served) return
    void loadLinear(useStore, client)
  }, [client, served])

  const run = async (call: () => Promise<LinearStatus | null>) => {
    setBusy(true)
    setError(null)
    try {
      const next = await call()
      if (useStore.getState().identityKey === viewer) writeLinear(next)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  const connect = (key: string) => void run(() => client.linearConnect(key))
  const disconnect = () => void run(async () => { await client.linearDisconnect(); return { connected: false } })

  if (!served) return null

  const keyHelp = <>Create one in Linear under Settings → Account → Security &amp; access; <a className={link} href="https://linear.app/docs/api-and-webhooks" target="_blank" rel="noreferrer">Linear&apos;s API guide</a> shows where. Save checks the key with Linear first. The server keeps it for your account only.</>

  return (
    <SettingsSection title="Linear" id="linear" scope="Your account">
      {status === null ? (
        <SettingRow label="Personal API key" help="Loading…" />
      ) : status.connected && status.error ? (
        <SettingRow
          label={status.refused ? 'Linear refused your saved key' : 'Could not reach Linear'}
          labelFor="linear-api-key"
          help={status.refused ? 'Paste a new key to replace it, or Disconnect to remove it.' : 'Your key is saved. Retry, paste a new key to replace it, or Disconnect to remove it.'}
          control={
            <span className="flex items-center gap-2">
              {!status.refused && <Button size="sm" variant="secondary" disabled={busy} onClick={() => void loadLinear(useStore, client)}>Retry</Button>}
              <Button size="sm" variant="secondary" disabled={busy} onClick={disconnect}>Disconnect</Button>
            </span>
          }
        >
          <KeyForm label="Replace key" busy={busy} error={error ?? status.error} onSave={connect} />
        </SettingRow>
      ) : status.connected ? (
        <SettingRow
          label={`Connected as ${status.name}${status.organization ? ` (${status.organization})` : ''}`}
          help={<>The <strong>Start session</strong> button on an issue links the new session to it.</>}
          control={
            <span className="flex items-center gap-2">
              <Button size="sm" variant="secondary" onClick={() => navigate('linear')}>View issues</Button>
              <Button size="sm" variant="secondary" disabled={busy} onClick={disconnect}>Disconnect</Button>
            </span>
          }
        >
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
        </SettingRow>
      ) : status.error ? (
        <SettingRow
          label="Could not read your Linear connection"
          help="Cloudfield could not check the connection. Your key is unchanged."
          control={<Button size="sm" variant="secondary" onClick={() => void loadLinear(useStore, client)}>Retry</Button>}
        >
          <Callout tone="failed" role="alert">{status.error}</Callout>
        </SettingRow>
      ) : (
        <SettingRow label="Personal API key" labelFor="linear-api-key" help={keyHelp}>
          <KeyForm label="Save" busy={busy} error={error} onSave={connect} />
        </SettingRow>
      )}
      <SettingRow
        label="Pull request links"
        help={<>Linear links a pull request to its issue through <a className={link} href="https://linear.app/docs/github" target="_blank" rel="noreferrer">Linear&apos;s GitHub integration</a>.</>}
      />
    </SettingsSection>
  )
}
