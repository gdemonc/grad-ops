import type { FileTarget } from './types'

export type ObservationMeta = {
  round?: number
  attempt?: number
}

/** Extract the round/attempt fields emitted by investigation.observed events. */
export function observationMeta(payload: unknown): ObservationMeta {
  if (typeof payload !== 'object' || payload === null) return {}
  const value = payload as Record<string, unknown>
  const meta: ObservationMeta = {}
  if (typeof value.round === 'number' && Number.isInteger(value.round) && value.round > 0) {
    meta.round = value.round
  }
  if (typeof value.attempt === 'number' && Number.isInteger(value.attempt) && value.attempt > 0) {
    meta.attempt = value.attempt
  }
  return meta
}

/** A preview of file-append content. The caller must show the full value too. */
export function fileTargetPreview(target: FileTarget, limit = 200): { text: string; truncated: boolean } {
  const data = target.data ?? ''
  if (data.length <= limit) return { text: data, truncated: false }
  return { text: data.slice(0, limit), truncated: true }
}

export function fileIdentity(device: number, inode: number): string {
  return `${device}:${inode}`
}
