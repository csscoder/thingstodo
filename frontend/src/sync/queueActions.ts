import type { Table } from 'dexie'
import { localDb } from '../db/index'
import type { SyncEntity, SyncQueueEntry } from '../db/schema'
import { QUEUE_ENTITY_TABLE, restoreFromSkippedSnapshot } from './pull'
import { syncNow } from './engine'

/** Entities whose creates live under a parent task (via data.task_id). */
const TASK_CHILD_ENTITIES: ReadonlySet<SyncEntity> = new Set([
  'schedule',
  'reminder',
  'checklistItem',
  'attachment',
  'repeatRule',
])

/**
 * True when `child` names `parent` as its parent: schedule/reminder/
 * checklist/attachment/repeatRule entries belong to a task create via
 * data.task_id, headings to a project create via data.project_id.
 */
function namesParent(child: SyncQueueEntry, parent: SyncQueueEntry): boolean {
  const data = child.data ?? {}
  if (parent.entity === 'task' && TASK_CHILD_ENTITIES.has(child.entity)) {
    return data.task_id === parent.entityId
  }
  if (parent.entity === 'project' && child.entity === 'heading') {
    return data.project_id === parent.entityId
  }
  return false
}

/** All stuck queue entries, id-ascending (queue order). */
export async function listStuckEntries(): Promise<SyncQueueEntry[]> {
  return localDb.syncQueue.orderBy('id').filter((entry) => entry.state === 'stuck').toArray()
}

/**
 * The entries a discard of `entryId` removes from the queue:
 * - the entry itself;
 * - every later entry of the same entity (they were held behind the stuck
 *   one and were made on top of it);
 * - when a create is involved, every queued entry — at any position — whose
 *   data.task_id / data.project_id names that created entity, transitively
 *   (children cannot push without their parent).
 */
export async function entriesRemovedByDiscard(entryId: number): Promise<SyncQueueEntry[]> {
  const all = await localDb.syncQueue.orderBy('id').toArray()
  const target = all.find((entry) => entry.id === entryId)
  if (!target) return []

  const removedIds = new Set<number>([entryId])

  for (const entry of all) {
    if (
      entry.id !== undefined &&
      entry.id > entryId &&
      entry.entity === target.entity &&
      entry.entityId === target.entityId
    ) {
      removedIds.add(entry.id)
    }
  }

  let grew = true
  while (grew) {
    grew = false
    for (const entry of all) {
      if (entry.id === undefined || removedIds.has(entry.id)) continue
      const parentRemoved = all.some(
        (parent) =>
          parent.action === 'create' &&
          parent.id !== undefined &&
          removedIds.has(parent.id) &&
          namesParent(entry, parent),
      )
      if (parentRemoved) {
        removedIds.add(entry.id)
        grew = true
      }
    }
  }

  return all.filter((entry) => entry.id !== undefined && removedIds.has(entry.id))
}

/** Delete the skipped snapshot of every entity fully drained by a discard. */
async function clearDrainedSnapshots(entries: SyncQueueEntry[]): Promise<void> {
  const seen = new Set<string>()
  for (const entry of entries) {
    const key = `${entry.entity}+${entry.entityId}`
    if (seen.has(key)) continue
    seen.add(key)

    const stillQueued = await localDb.syncQueue
      .filter((other) => other.entity === entry.entity && other.entityId === entry.entityId)
      .count()
    if (stillQueued === 0) {
      await localDb.skippedSnapshots.delete([entry.entity, entry.entityId])
    }
  }
}

/**
 * Discard a stuck change: remove exactly the entries
 * {@link entriesRemovedByDiscard} lists. If the set contains the entity's
 * create, the entity never reached the server — delete the local rows of
 * the entity and its discarded children plus their skipped snapshots.
 * Otherwise restore the entity from its skipped snapshot (the server
 * version). All local writes run in one transaction, so a failure leaves
 * no partial discard behind; the sync afterwards runs outside it.
 */
export async function discardEntry(entryId: number): Promise<void> {
  const removed = await entriesRemovedByDiscard(entryId)
  const target = removed.find((entry) => entry.id === entryId)
  if (!target) return

  const ids = removed
    .map((entry) => entry.id)
    .filter((id): id is number => id !== undefined)

  const discardsCreate = removed.some(
    (entry) =>
      entry.entity === target.entity &&
      entry.entityId === target.entityId &&
      entry.action === 'create',
  )

  // Every table the discard may touch, so the whole thing commits or
  // rolls back as one unit.
  const entityTables: Table[] = [
    ...new Set(removed.map((entry) => QUEUE_ENTITY_TABLE[entry.entity])),
  ]
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    .map((name) => (localDb as any)[name] as Table | undefined)
    .filter((table): table is Table => table !== undefined)

  await localDb.transaction(
    'rw',
    [localDb.syncQueue, localDb.skippedSnapshots, ...entityTables],
    async () => {
      await localDb.syncQueue.bulkDelete(ids)

      if (discardsCreate) {
        for (const entry of removed) {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          const table = (localDb as any)[QUEUE_ENTITY_TABLE[entry.entity]]
          if (table) await table.delete(entry.entityId)
        }
        await clearDrainedSnapshots(removed)
      } else {
        // Joins the ambient transaction: it only touches the entity's
        // table and skippedSnapshots, both declared in the scope above.
        await restoreFromSkippedSnapshot(target.entity, target.entityId)
      }
    },
  )

  await syncNow()
}

/** Clear a stuck entry's failure markers and let the next sync retry it. */
export async function retryEntry(entryId: number): Promise<void> {
  // Dexie's update deletes keys whose value is set to undefined.
  await localDb.syncQueue.update(entryId, {
    state: undefined,
    errorCode: undefined,
    errorMessage: undefined,
    internalFailures: undefined,
  })
  await syncNow()
}
