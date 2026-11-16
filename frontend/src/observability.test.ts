import { describe, expect, it } from 'vitest'
import { fileIdentity, fileTargetPreview, observationMeta } from './observability'
import type { FileTarget } from './types'

const target: FileTarget = {
  root: '/var/log',
  root_device: 10,
  root_inode: 20,
  relative: 'app.log',
  operation: 'append',
  data: '0123456789',
  device: 30,
  inode: 40,
}

describe('observationMeta', () => {
  it('reads valid round and attempt values', () => {
    expect(observationMeta({ round: 2, attempt: 1 })).toEqual({ round: 2, attempt: 1 })
  })

  it('ignores missing or malformed values for old events', () => {
    expect(observationMeta(null)).toEqual({})
    expect(observationMeta({})).toEqual({})
    expect(observationMeta({ round: 0, attempt: '1' })).toEqual({})
  })
})

describe('fileTargetPreview', () => {
  it('marks a truncated preview and keeps the full value available to the caller', () => {
    expect(fileTargetPreview(target, 4)).toEqual({ text: '0123', truncated: true })
    expect(target.data).toBe('0123456789')
  })

  it('does not mark a complete value as truncated', () => {
    expect(fileTargetPreview(target, 100)).toEqual({ text: '0123456789', truncated: false })
  })
})

describe('fileIdentity', () => {
  it('renders the root and target identity as dev:inode', () => {
    expect(fileIdentity(target.root_device, target.root_inode)).toBe('10:20')
  })
})
