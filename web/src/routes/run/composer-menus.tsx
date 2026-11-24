import { Fragment, useEffect, useId, useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import { Check, ChevronDown, FileText, Folder } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { Menu, MenuContent, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuTrigger } from '@/components/ui/menu'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { RadioPills, RadioPillsItem } from '@/components/ui/radio-pills'
import { Shimmer } from '@/components/ui/shimmer'
import { TextSwap } from '@/components/ui/text-swap'
import { api } from '@/lib/api'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import type { ConfigOption, ConfigValue, SessionCommand } from '@/lib/session-types'
import { cn, focusRingInset, surface } from '@/lib/utils'
import { useStore } from '@/store'
import { filesKey } from '@/store/files'

export interface Suggestion {
  id: string
  label: string
  detail?: string
  insert: string
  folder?: boolean
}

export function triggerAt(text: string, caret: number): { kind: '/' | '@'; query: string; start: number } | null {
  const before = text.slice(0, caret)
  const slash = /^\/(\S*)$/.exec(before)
  if (slash) return { kind: '/', query: slash[1]!, start: 0 }
  const at = /(^|\s)@(\S*)$/.exec(before)
  if (at) return { kind: '@', query: at[2]!, start: before.length - at[2]!.length - 1 }
  return null
}

export function commandSuggestions(commands: SessionCommand[], query: string): Suggestion[] {
  return commands
    .filter((command) => command.name.toLowerCase().startsWith(query.toLowerCase()))
    .map((command) => ({ id: command.name, label: `/${command.name}`, detail: command.description, insert: `/${command.name} ` }))
}

export function useFileSuggestions(workspaceID: string, runID: string, query: string | null): Suggestion[] {
  const slash = query === null ? -1 : query.lastIndexOf('/')
  const dir = query === null ? null : query.slice(0, slash + 1)
  const prefix = query === null ? '' : query.slice(slash + 1).toLowerCase()
  const key = dir === null ? null : filesKey(workspaceID, runID, dir.replace(/\/$/, ''))
  const tree = useStore((s) => (key ? s.trees[key] : undefined))
  useEffect(() => {
    if (!key || dir === null || tree) return
    const path = dir.replace(/\/$/, '')
    useStore.getState().setTree(key, { entries: [], loading: true })
    api.filesTree({ workspace_id: workspaceID, run_id: runID, path }).then(
      (result) => useStore.getState().setTree(key, { entries: result.entries }),
      (err: unknown) => useStore.getState().setTree(key, { entries: [], error: String(err) }),
    )
  }, [key, dir, tree, workspaceID, runID])
  if (!tree || dir === null) return []
  return tree.entries
    .filter((entry) => entry.name.toLowerCase().startsWith(prefix))
    .slice(0, 50)
    .map((entry) => ({
      id: `${dir}${entry.name}`,
      label: `${dir}${entry.name}${entry.kind === 'dir' ? '/' : ''}`,
      insert: entry.kind === 'dir' ? `@${dir}${entry.name}/` : `@${dir}${entry.name} `,
      folder: entry.kind === 'dir',
    }))
}

export function SuggestionList({ id, items, active, onPick }: {
  id: string
  items: Suggestion[]
  active: number
  onPick: (item: Suggestion) => void
}) {
  if (items.length === 0) return null
  return (
    <ul id={id} role="listbox" aria-label="Suggestions" className={cn(surface, 'absolute inset-x-0 bottom-full z-10 mb-1 max-h-56 overflow-y-auto p-1')}>
      {items.map((item, index) => (
        <li
          key={item.id}
          id={`${id}-${index}`}
          role="option"
          aria-selected={index === active}
          onMouseDown={(event) => {
            event.preventDefault()
            onPick(item)
          }}
          className={cn(
            focusRingInset,
            'flex h-7 min-w-0 cursor-pointer items-center gap-2 rounded-control px-2 text-ui-sm text-text coarse:h-11',
            index === active && 'bg-hover-chrome',
          )}
        >
          {item.insert.startsWith('@') && (item.folder ? <Folder aria-hidden className="size-3.5 shrink-0 text-muted" /> : <FileText aria-hidden className="size-3.5 shrink-0 text-muted" />)}
          <span className="shrink-0 font-code">{item.label}</span>
          {item.detail && <span className="min-w-0 truncate text-muted">{item.detail}</span>}
        </li>
      ))}
    </ul>
  )
}

const footerCategories = ['mode', 'model', 'thought_level']

function choices(option: ConfigOption): ConfigValue[] {
  return (option.options ?? []).flatMap((entry) => ('group' in entry ? entry.options : [entry]))
}

export function currentChoice(option: ConfigOption): ConfigValue | undefined {
  return choices(option).find((choice) => choice.value === option.currentValue)
}

const named = (option: ConfigOption) => `${option.name}: ${currentChoice(option)?.name ?? 'not set'}`

type OptionPillProps = {
  option: ConfigOption
  /** The effort option a model pill also names and sets. */
  effort?: ConfigOption
  disabled: boolean
  /** Resolves to whether the agent took the value. */
  onSet: (option: ConfigOption, value: string) => Promise<boolean>
}

function EffortRow({ option, sending, onChoose }: { option: ConfigOption; sending?: string; onChoose: (value: string) => void }) {
  const label = useId()
  const [pointed, setPointed] = useState<string>()
  // A navigation key brings a click with it, in the same task, that must not choose.
  const byKey = useRef(false)
  const markKey = () => {
    byKey.current = true
    queueMicrotask(() => {
      byKey.current = false
    })
  }
  const levels = choices(option)
  const shown = pointed ?? option.currentValue
  return (
    <div className="flex shrink-0 flex-col gap-1.5 border-t border-seam px-2 pt-2">
      <span id={label} className="text-ui-sm font-medium text-muted">{option.name}</span>
      {/* A touch screen cannot fit the levels on one row, so it gets even rows instead of one left over. */}
      <RadioPills
        aria-labelledby={label}
        value={String(option.currentValue ?? '')}
        onBlur={() => setPointed(undefined)}
        onPointerLeave={() => setPointed(undefined)}
        style={{ '--columns': Math.ceil(levels.length / Math.ceil(levels.length / 4)) } as CSSProperties}
        className="w-full flex-wrap coarse:grid coarse:grid-cols-[repeat(var(--columns),minmax(0,1fr))]"
      >
        {levels.map((choice, index) => (
          <RadioPillsItem
            key={choice.value}
            value={choice.value}
            aria-busy={choice.value === sending || undefined}
            aria-describedby={choice.description ? `${label}-${index}` : undefined}
            className="h-6 px-2 text-ui-sm coarse:justify-center coarse:px-1"
            onPointerEnter={() => setPointed(choice.value)}
            onFocus={() => {
              setPointed(choice.value)
              markKey()
            }}
            onClick={() => {
              if (!byKey.current) onChoose(choice.value)
            }}
            onKeyDown={(event) => {
              if (event.key === 'Enter') onChoose(choice.value)
              else if (event.key === 'Home' || event.key === 'End') markKey()
            }}
          >
            {choice.value === sending ? <Shimmer text={choice.name} /> : choice.name}
          </RadioPillsItem>
        ))}
      </RadioPills>
      {/* Every description shares one cell: each level keeps its own for a screen reader, and the cell never changes height. */}
      {levels.some((choice) => choice.description) && (
        <div className="grid text-ui-sm text-muted">
          {levels.map((choice, index) => (
            <p key={choice.value} id={`${label}-${index}`} className={cn('[grid-area:1/1]', choice.value !== shown && 'invisible')}>{choice.description}</p>
          ))}
        </div>
      )}
    </div>
  )
}

function OptionPill({ option, effort, disabled, onSet }: OptionPillProps) {
  const [open, setOpen] = useState(false)
  const [sending, setSending] = useState<[ConfigOption, string]>()
  const queued = useRef<[ConfigOption, string]>(undefined)
  const inFlight = useRef(false)
  const latestSet = useRef(onSet)
  latestSet.current = onSet
  const coarse = useMediaQuery(coarsePointer)
  const busy = sending !== undefined
  useEffect(() => () => {
    queued.current = undefined
  }, [])
  // The picker's own request disables it too, and an effort change keeps it open.
  useEffect(() => {
    if (disabled && !busy) setOpen(false)
  }, [disabled, busy])
  const current = currentChoice(option)
  const unavailable = disabled || busy
  const changeOpen = (next: boolean) => {
    if (next && (unavailable || inFlight.current)) return
    setOpen(next)
  }
  const send = (target: ConfigOption, value: string) => {
    inFlight.current = true
    setSending([target, value])
    void latestSet.current(target, value).then((taken) => {
      inFlight.current = false
      setSending(undefined)
      const next = queued.current
      queued.current = undefined
      // After a refusal the next request would clear its error from the composer.
      if (next && taken) send(...next)
    })
  }
  // A pick made while another is on its way waits for it, so the last one wins.
  const select = (target: ConfigOption, value: string) => {
    if (target !== effort) setOpen(false)
    if (inFlight.current) queued.current = [target, value]
    else if (!disabled) send(target, value)
  }
  // Keep the trigger focusable so Radix can return focus on close, not after the request.
  const trigger = (
    <Button variant="ghost" size="sm" className="aria-expanded:bg-hover-chrome aria-expanded:text-text" aria-disabled={(unavailable && !(open && busy)) || undefined} aria-busy={busy || undefined} aria-label={effort ? `${named(option)}, ${named(effort)}` : named(option)}>
      <TextSwap className={effort ? 'max-w-[min(12rem,24cqw)]' : 'max-w-32'} layerClassName="min-w-0 truncate">{current?.name ?? option.name}</TextSwap>
      {effort && (
        <>
          <span aria-hidden className="text-icon-faint">/</span>
          <TextSwap>{currentChoice(effort)?.name ?? effort.name}</TextSwap>
        </>
      )}
      <ChevronDown />
    </Button>
  )

  if (option.category === 'model') {
    const item = (choice: ConfigValue, group?: { group: string; name: string }) => (
      <CommandItem
        key={`value:${choice.value}`}
        value={JSON.stringify(choice.value)}
        keywords={[choice.value, choice.name, choice.description ?? '', group?.name ?? '', group?.group ?? '']}
        onSelect={() => select(option, choice.value)}
      >
        <span className="flex size-4 shrink-0 items-center">
          {choice.value === option.currentValue && <Check aria-hidden />}
        </span>
        <span className="flex min-w-0 flex-1 flex-col whitespace-normal py-1 [overflow-wrap:anywhere]">
          <span>{choice.name}</span>
          <span className="text-ui-sm text-muted">{choice.value}</span>
          {choice.description && <span className="text-ui-sm text-muted">{choice.description}</span>}
          {choice.value === option.currentValue && <span className="sr-only">Current model</span>}
        </span>
      </CommandItem>
    )
    return (
      <Popover open={open && (busy || !disabled)} onOpenChange={changeOpen}>
        <PopoverTrigger asChild>{trigger}</PopoverTrigger>
        <PopoverContent
          side="top"
          aria-label={option.name}
          className="flex flex-col overflow-hidden"
          onOpenAutoFocus={(event) => {
            const content = event.target as HTMLElement
            // A focused field raises a touch screen's keyboard over the list.
            const first = coarse ? content : content.querySelector<HTMLInputElement>('[cmdk-input]')
            if (first) {
              first.focus({ preventScroll: true })
              event.preventDefault()
            }
            content.querySelector('[cmdk-item][data-selected=true]')?.scrollIntoView({ block: 'nearest' })
          }}
        >
          <Command label={option.name} defaultValue={current ? JSON.stringify(current.value) : undefined} className="[&_[cmdk-input-wrapper]]:shrink-0">
            <CommandInput placeholder={`Search ${option.name.toLowerCase()}…`} aria-label={`Search ${option.name}`} />
            <CommandList className="min-h-0">
              <CommandEmpty>No models found. Try another search.</CommandEmpty>
              {(option.options ?? []).map((entry) => 'group' in entry ? (
                <CommandGroup
                  key={`group:${entry.group}`}
                  heading={entry.name}
                  className="[&_[cmdk-group-heading]]:whitespace-normal [&_[cmdk-group-heading]]:[overflow-wrap:anywhere]"
                >
                  {entry.options.map((choice) => item(choice, entry))}
                </CommandGroup>
              ) : item(entry))}
            </CommandList>
          </Command>
          {effort && (
            <EffortRow
              option={effort}
              sending={sending?.[0].id === effort.id ? sending[1] : undefined}
              onChoose={(value) => {
                if (inFlight.current || value !== effort.currentValue) select(effort, value)
              }}
            />
          )}
        </PopoverContent>
      </Popover>
    )
  }

  const item = (choice: ConfigValue) => (
    <MenuRadioItem
      key={choice.value}
      value={choice.value}
      description={choice.description ?? null}
      disabled={unavailable}
    >
      {choice.name}
    </MenuRadioItem>
  )
  return (
    <Menu open={open && !unavailable} onOpenChange={changeOpen}>
      <MenuTrigger asChild>{trigger}</MenuTrigger>
      <MenuContent align="start" side="top" className="w-[min(360px,calc(100vw-16px))]">
        <MenuLabel className="whitespace-normal [overflow-wrap:anywhere]">{option.name}</MenuLabel>
        <MenuRadioGroup value={String(option.currentValue ?? '')} onValueChange={(value) => select(option, value)}>
          {(option.options ?? []).map((entry) => 'group' in entry ? (
            <Fragment key={entry.group}>
              <MenuLabel className="whitespace-normal [overflow-wrap:anywhere]">{entry.name}</MenuLabel>
              {entry.options.map(item)}
            </Fragment>
          ) : item(entry))}
        </MenuRadioGroup>
      </MenuContent>
    </Menu>
  )
}

export function OptionPills({ options, disabled, onSet }: {
  options: ConfigOption[]
  disabled: boolean
  onSet: (option: ConfigOption, value: string) => Promise<boolean>
}) {
  const shown = footerCategories.flatMap((category) => options.filter((option) => option.category === category && option.type === 'select'))
  const model = shown.find((option) => option.category === 'model')
  const effort = model && shown.find((option) => option.category === 'thought_level')
  return (
    <>
      {shown.filter((option) => option !== effort).map((option) => (
        <OptionPill key={option.id} option={option} effort={option === model ? effort : undefined} disabled={disabled} onSet={onSet} />
      ))}
    </>
  )
}
