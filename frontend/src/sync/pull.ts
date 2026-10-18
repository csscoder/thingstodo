import { api } from '../api/client'
import { localDb } from '../db/index'
import type { SyncEntity } from '../db/schema'
import { applyQueueEntryLocally } from './replay'

const ENTITY_TABLE_MAP: Record<string, string> = {
  task: 'tasks',
  project: 'projects',
  area: 'areas',
  tag: 'tags',
  checklist_item: 'checklistItems',
  attachment: 'attachments',
  schedule: 'schedules',
  reminder: 'reminders',
  repeat_rule: 'repeatRules',
  heading: 'headings',
}

// Map from server entity name to SyncEntity (queue entity name)
const SERVER_TO_QUEUE_ENTITY: Record<string, SyncEntity> = {
  task: 'task',
  project: 'project',
  area: 'area',
  tag: 'tag',
  checklist_item: 'checklistItem',
  attachment: 'attachment',
  schedule: 'schedule',
  reminder: 'reminder',
  repeat_rule: 'repeatRule',
  heading: 'heading',
}

// Map from SyncEntity (queue entity name) to its Dexie table name,
// derived from the server-name maps above.
export const QUEUE_ENTITY_TABLE: Record<SyncEntity, string> = Object.fromEntries(
  Object.entries(SERVER_TO_QUEUE_ENTITY).map(([serverEntity, queueEntity]) => [
    queueEntity,
    ENTITY_TABLE_MAP[serverEntity],
  ]),
) as Record<SyncEntity, string>

interface ChangeLogEntry {
  seq: number
  entity: string
  entity_id: string
  action: 'create' | 'update' | 'delete'
  snapshot: string // JSON string of entity
  fields: string | null
  created_at: string
}

interface PullResponse {
  changes: ChangeLogEntry[]
  cursor: number
  has_more: boolean
}

interface FullSyncResponse {
  tasks: Record<string, unknown>[]
  projects: Record<string, unknown>[]
  areas: Record<string, unknown>[]
  tags: Record<string, unknown>[]
  checklist?: Record<string, unknown>[]
  attachments?: Record<string, unknown>[]
  schedules?: Record<string, unknown>[]
  reminders?: Record<string, unknown>[]
  repeat_rules?: Record<string, unknown>[]
  headings?: Record<string, unknown>[]
  cursor: number
}

async function getCursor(): Promise<number> {
  const meta = await localDb.syncMeta.get('cursor')
  return meta ? (meta.value as number) : 0
}

async function saveCursor(cursor: number): Promise<void> {
  await localDb.syncMeta.put({ key: 'cursor', value: cursor })
}

async function getPendingEntityIds(): Promise<Map<SyncEntity, Set<string>>> {
  const pending = await localDb.syncQueue.toArray()
  const map = new Map<SyncEntity, Set<string>>()
  for (const entry of pending) {
    if (!map.has(entry.entity)) {
      map.set(entry.entity, new Set())
    }
    map.get(entry.entity)!.add(entry.entityId)
  }
  return map
}

async function applyChange(
  change: ChangeLogEntry,
  pendingEntityIds: Map<SyncEntity, Set<string>>,
): Promise<void> {
  const tableName = ENTITY_TABLE_MAP[change.entity]
  if (!tableName) return

  const queueEntity = SERVER_TO_QUEUE_ENTITY[change.entity]
  if (queueEntity) {
    const pendingIds = pendingEntityIds.get(queueEntity)
    if (pendingIds?.has(change.entity_id)) {
      // Skip — we have local pending changes for this entity. Keep the
      // server's latest snapshot so it can be restored later (Discard).
      await localDb.skippedSnapshots.put({
        entity: queueEntity,
        entityId: change.entity_id,
        action: change.action,
        snapshot: JSON.parse(change.snapshot) as Record<string, unknown>,
        seq: change.seq,
      })
      return
    }
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const table = (localDb as any)[tableName]
  if (!table) return

  const data = JSON.parse(change.snapshot)

  if (change.action === 'delete') {
    // If snapshot has deleted_at, soft-delete (keep in DB); otherwise hard-delete
    if (data.deleted_at) {
      await table.put({
        ...data,
        _syncStatus: 'synced',
        _localUpdatedAt: new Date().toISOString(),
        _serverSeq: change.seq,
      })
    } else {
      await table.delete(change.entity_id)
    }

    // When a repeat_rule is deleted, mark the parent task accordingly
    if (change.entity === 'repeat_rule' && data.task_id) {
      await localDb.tasks.update(data.task_id, { has_repeat_rule: false })
    }
  } else {
    // For schedule creates, remove any local-only schedules for the same task
    // to prevent duplicates (frontend creates a temporary local schedule for
    // immediate UI display; the server creates the canonical one).
    if (change.entity === 'schedule' && change.action === 'create' && data.task_id) {
      const localSchedules = await localDb.schedules
        .where('task_id')
        .equals(data.task_id)
        .toArray()
      for (const ls of localSchedules) {
        if (ls.id !== change.entity_id && !ls._serverSeq) {
          await localDb.schedules.delete(ls.id)
        }
      }
    }

    // create or update — upsert with sync metadata
    const record = {
      ...data,
      id: change.entity_id,
      _syncStatus: 'synced',
      _localUpdatedAt: new Date().toISOString(),
      _serverSeq: change.seq,
    }
    await table.put(record)

    // When a repeat_rule is created/updated, mark the parent task accordingly
    if (change.entity === 'repeat_rule' && data.task_id) {
      await localDb.tasks.update(data.task_id, { has_repeat_rule: true })
    }
  }
}

/**
 * Apply the stored skipped snapshot of an entity to its table and remove
 * the record. A delete-action snapshot without deleted_at hard-deletes the
 * row; any other snapshot is written back with synced metadata. No stored
 * record leaves the row untouched.
 */
export async function restoreFromSkippedSnapshot(
  entity: SyncEntity,
  entityId: string,
): Promise<void> {
  const record = await localDb.skippedSnapshots.get([entity, entityId])
  if (!record) return

  const tableName = QUEUE_ENTITY_TABLE[entity]
  if (tableName) {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const table = (localDb as any)[tableName]
    if (record.action === 'delete' && !record.snapshot.deleted_at) {
      await table.delete(entityId)
    } else {
      await table.put({
        ...record.snapshot,
        id: entityId,
        _syncStatus: 'synced',
        _localUpdatedAt: new Date().toISOString(),
        _serverSeq: record.seq,
      })
    }
  }

  await localDb.skippedSnapshots.delete([entity, entityId])
}

export async function pullChanges(): Promise<ChangeLogEntry[]> {
  const pendingEntityIds = await getPendingEntityIds()
  const allChanges: ChangeLogEntry[] = []

  let cursor = await getCursor()
  let hasMore = true

  while (hasMore) {
    const response = await api.get<PullResponse>(
      `/sync/pull?since=${cursor}&limit=500`,
    )

    for (const change of response.changes) {
      await applyChange(change, pendingEntityIds)
      allChanges.push(change)
    }

    cursor = response.cursor
    hasMore = response.has_more
  }

  await saveCursor(cursor)
  return allChanges
}

export async function fullSync(): Promise<void> {
  const response = await api.get<FullSyncResponse>('/sync/full')

  // Clear all entity tables (not syncQueue or syncMeta)
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  await (localDb as any).transaction(
    'rw',
    [
      localDb.tasks,
      localDb.projects,
      localDb.areas,
      localDb.tags,
      localDb.checklistItems,
      localDb.attachments,
      localDb.schedules,
      localDb.reminders,
      localDb.repeatRules,
      localDb.headings,
    ],
    async () => {
      await localDb.tasks.clear()
      await localDb.projects.clear()
      await localDb.areas.clear()
      await localDb.tags.clear()
      await localDb.checklistItems.clear()
      await localDb.attachments.clear()
      await localDb.schedules.clear()
      await localDb.reminders.clear()
      await localDb.repeatRules.clear()
      await localDb.headings.clear()

      const now = new Date().toISOString()
      // Stamp every row with the full-sync cursor so later pulled changes
      // can tell confirmed rows apart from local-only ones (e.g. the
      // temporary-row cleanup for schedule creates).
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const withMeta = (items: Record<string, unknown>[], seq: number): any[] =>
        items.map((item) => ({
          ...item,
          _syncStatus: 'synced' as const,
          _localUpdatedAt: now,
          _serverSeq: seq,
        }))

      if (response.tasks?.length) await localDb.tasks.bulkPut(withMeta(response.tasks, response.cursor) as Parameters<typeof localDb.tasks.bulkPut>[0])
      if (response.projects?.length) await localDb.projects.bulkPut(withMeta(response.projects, response.cursor) as Parameters<typeof localDb.projects.bulkPut>[0])
      if (response.areas?.length) await localDb.areas.bulkPut(withMeta(response.areas, response.cursor) as Parameters<typeof localDb.areas.bulkPut>[0])
      if (response.tags?.length) await localDb.tags.bulkPut(withMeta(response.tags, response.cursor) as Parameters<typeof localDb.tags.bulkPut>[0])
      if (response.checklist?.length) await localDb.checklistItems.bulkPut(withMeta(response.checklist, response.cursor) as Parameters<typeof localDb.checklistItems.bulkPut>[0])
      if (response.attachments?.length) await localDb.attachments.bulkPut(withMeta(response.attachments, response.cursor) as Parameters<typeof localDb.attachments.bulkPut>[0])
      if (response.schedules?.length) await localDb.schedules.bulkPut(withMeta(response.schedules, response.cursor) as Parameters<typeof localDb.schedules.bulkPut>[0])
      if (response.reminders?.length) await localDb.reminders.bulkPut(withMeta(response.reminders, response.cursor) as Parameters<typeof localDb.reminders.bulkPut>[0])
      if (response.repeat_rules?.length) await localDb.repeatRules.bulkPut(withMeta(response.repeat_rules, response.cursor) as Parameters<typeof localDb.repeatRules.bulkPut>[0])
      if (response.headings?.length) await localDb.headings.bulkPut(withMeta(response.headings, response.cursor) as Parameters<typeof localDb.headings.bulkPut>[0])
    },
  )

  // The clear+bulkPut transaction has committed. Re-apply every still-queued
  // local change over the server rows (Dexie transactions don't nest, so
  // this runs as sequential awaits after the transaction) so pending and
  // stuck edits survive the full sync.
  const queueEntries = await localDb.syncQueue.orderBy('id').toArray()

  // Capture each queued entity's full-sync row (before replay overlays it)
  // as its skipped snapshot, so Discard can restore the server version.
  // Entities the server doesn't have get a delete snapshot.
  const touchedEntities = new Map<SyncEntity, Set<string>>()
  for (const entry of queueEntries) {
    if (!touchedEntities.has(entry.entity)) {
      touchedEntities.set(entry.entity, new Set())
    }
    touchedEntities.get(entry.entity)!.add(entry.entityId)
  }
  for (const [entity, entityIds] of touchedEntities) {
    const tableName = QUEUE_ENTITY_TABLE[entity]
    if (!tableName) continue
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const table = (localDb as any)[tableName]
    for (const entityId of entityIds) {
      const row = await table.get(entityId)
      await localDb.skippedSnapshots.put({
        entity,
        entityId,
        action: row ? 'update' : 'delete',
        snapshot: (row ?? {}) as Record<string, unknown>,
        seq: response.cursor,
      })
    }
  }

  for (const entry of queueEntries) {
    await applyQueueEntryLocally(entry)
  }

  await saveCursor(response.cursor)
}
