import { beforeEach, describe, expect, test } from 'vitest'
import { localDb } from '../index'
import type { SkippedSnapshot } from '../schema'

describe('db schema', () => {
  beforeEach(async () => {
    await Promise.all(localDb.tables.map((t) => t.clear()))
  })

  test('stores and replaces a skipped snapshot per entity', async () => {
    const first: SkippedSnapshot = {
      entity: 'task',
      entityId: 't1',
      action: 'update',
      snapshot: { title: 'Old title' },
      seq: 3,
    }
    await localDb.skippedSnapshots.put(first)

    const second: SkippedSnapshot = {
      entity: 'task',
      entityId: 't1',
      action: 'update',
      snapshot: { title: 'Newer title' },
      seq: 5,
    }
    await localDb.skippedSnapshots.put(second)

    const stored = await localDb.skippedSnapshots.get(['task', 't1'])
    expect(stored?.seq).toBe(5)
    expect(stored?.snapshot).toEqual({ title: 'Newer title' })
    expect(await localDb.skippedSnapshots.count()).toBe(1)
  })
})
