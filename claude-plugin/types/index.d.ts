export type StatusLive = { files: number; added: number; removed: number; shared?: unknown }

export type StatusChainProgress = { done: number; total: number; phase?: string }

export type StatusRow = {
  name: string
  round: number
  status: string
  tone: string
  reason: string
  last_ts: string
  live?: StatusLive | null
  chain?: string
  activity?: string
  chain_progress?: StatusChainProgress | null
}

export type StatusDoc = {
  mastermind: { id: string; name: string }
  now: string
  rows: StatusRow[]
  push_live?: boolean
}

declare module 'claude-code' {
  interface PluginState {
    relevo: { status: StatusDoc | null }
  }
}
