import { localDb } from '../db/index'
import type { SyncQueueEntry } from '../db/schema'
import { QUEUE_ENTITY_TABLE } from './pull'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function tableFor(entity: SyncQueueEntry['entity']): any | undefined {
  const tableName = QUEUE_ENTITY_TABLE[entity]
  if (!tableName) return undefined
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (localDb as any)[tableName]
}

/** Pick only the fields listed in `fields`; undefined fields = all of data's own keys. */
function pick(data: Record<string, unknown>, fields?: string[]): Record<string, unknown> {
  if (!fields) return { ...data }
  const picked: Record<string, unknown> = {}
  for (const field of fields) {
    if (field in data) picked[field] = data[field]
  }
  return picked
}

/**
 * Re-apply a queued local change to the local database, mirroring what
 * db/mutations.ts does for a live edit: creates and updates leave the row
 * `pending`, task deletes are soft (deleted_at), everything else is a row
 * delete. Missing rows are skipped on update/delete.
 */
export async function applyQueueEntryLocally(entry: SyncQueueEntry): Promise<void> {
  const table = tableFor(entry.entity)
  if (!table) return

  if (entry.action === 'create') {
    await table.put({ ...entry.data, id: entry.entityId, _syncStatus: 'pending' })
    return
  }

  if (entry.action === 'update') {
    const existing = await table.get(entry.entityId)
    if (!existing) return
    await table.update(entry.entityId, {
      ...pick(entry.data, entry.fields),
      _syncStatus: 'pending',
      _localUpdatedAt: new Date().toISOString(),
    })
    return
  }

  // delete: tasks soft-delete (deleted_at), all other entities hard-delete
  const existing = await table.get(entry.entityId)
  if (!existing) return
  if (entry.entity === 'task') {
    await table.update(entry.entityId, {
      deleted_at: (entry.data.deleted_at as string | undefined) ?? new Date().toISOString(),
      _syncStatus: 'pending',
      _localUpdatedAt: new Date().toISOString(),
    })
  } else {
    await table.delete(entry.entityId)
  }
}
