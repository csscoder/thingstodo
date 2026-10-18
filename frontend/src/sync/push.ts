import { ApiError, api } from '../api/client'
import { localDb } from '../db/index'
import type { SyncEntity, SyncErrorCode, SyncQueueEntry } from '../db/schema'

interface ClientChange {
  entity: SyncQueueEntry['entity']
  entity_id: string
  action: SyncQueueEntry['action']
  fields?: string[]
  data: Record<string, unknown>
  client_updated_at: string
}

interface PushResult {
  entity?: SyncQueueEntry['entity']
  entity_id: string
  status: string // 'applied' | 'conflict_resolved' | 'error'
  seq?: number
  error?: string
  code?: SyncErrorCode
}

interface PushResponse {
  results: PushResult[]
}

export interface PushFailure {
  entity: SyncQueueEntry['entity'] | 'unknown'
  entityId: string
  error: string
  count: number
}

export interface PushSummary {
  processedCount: number
  failures: PushFailure[]
  stuckCount: number
}

/** Error codes where retrying the same change can never succeed. */
export function isPermanent(code: SyncErrorCode | undefined): boolean {
  return (
    code === 'VALIDATION' ||
    code === 'NOT_FOUND' ||
    code === 'CONFLICT' ||
    code === 'TOO_LARGE'
  )
}

/** Consecutive INTERNAL failures after which an entry becomes visible as stuck. */
export const INTERNAL_STUCK_THRESHOLD = 5

/** Maximum number of changes sent in one push request. */
export const PUSH_BATCH_SIZE = 100

function isSuccess(result: PushResult): boolean {
  return result.status === 'applied' || result.status === 'conflict_resolved'
}

/**
 * Permanently stuck entries are never resent; entries stuck via INTERNAL
 * failures keep auto-retrying until they succeed or fail permanently.
 */
function isPermanentlyStuck(entry: SyncQueueEntry): boolean {
  return entry.state === 'stuck' && isPermanent(entry.errorCode)
}

/** A stuck entry of any code except INTERNAL blocks later entries of its entity. */
function blocksLaterEntries(entry: SyncQueueEntry): boolean {
  return entry.state === 'stuck' && entry.errorCode !== 'INTERNAL'
}

async function countStuckEntries(): Promise<number> {
  return localDb.syncQueue.filter((entry) => entry.state === 'stuck').count()
}

function queueEntryKey(entity: SyncEntity, entityId: string): string {
  return `${entity}+${entityId}`
}

/** Entities whose creates live under a parent task. */
const TASK_CHILD_ENTITIES: ReadonlySet<SyncEntity> = new Set([
  'schedule',
  'reminder',
  'checklistItem',
  'attachment',
  'repeatRule',
])

interface ParentRef {
  entity: 'task' | 'project'
  entityId: string
}

/** The parent whose create must be pushed before this child entry. */
function parentRefOf(entry: SyncQueueEntry): ParentRef | null {
  const data = entry.data ?? {}
  if (entry.entity === 'heading') {
    const projectId = data.project_id
    return typeof projectId === 'string' ? { entity: 'project', entityId: projectId } : null
  }
  if (TASK_CHILD_ENTITIES.has(entry.entity)) {
    const taskId = data.task_id
    return typeof taskId === 'string' ? { entity: 'task', entityId: taskId } : null
  }
  return null
}

/**
 * The child may be pushed when some queued create of its parent is healthy
 * and has been selected earlier in this selection (lower id, already picked).
 * A stuck create (permanently or via INTERNAL) holds all of its children.
 */
function parentCreateSelected(
  parent: ParentRef,
  child: SyncQueueEntry,
  createsByEntity: Map<string, SyncQueueEntry[]>,
  selectedIds: Set<number>,
): boolean {
  const creates = createsByEntity.get(queueEntryKey(parent.entity, parent.entityId))
  // No queued create: the parent already exists on the server.
  if (!creates || creates.length === 0) return true

  return creates.some(
    (create) =>
      create.state !== 'stuck' &&
      create.id !== undefined &&
      child.id !== undefined &&
      create.id < child.id &&
      selectedIds.has(create.id),
  )
}

/**
 * Pick the entries that may be pushed now, preserving queue order (input is
 * id ascending):
 * - permanently stuck entries are never resent;
 * - a later entry of an entity with an earlier stuck (non-INTERNAL) entry
 *   waits, so a stuck change is never overtaken by a later change of the
 *   same entity;
 * - a child waits until its parent's queued create is selected before it.
 */
export function selectPushable(entries: SyncQueueEntry[]): SyncQueueEntry[] {
  const createsByEntity = new Map<string, SyncQueueEntry[]>()
  for (const entry of entries) {
    if (entry.action !== 'create') continue
    const key = queueEntryKey(entry.entity, entry.entityId)
    const list = createsByEntity.get(key)
    if (list) list.push(entry)
    else createsByEntity.set(key, [entry])
  }

  const selected: SyncQueueEntry[] = []
  const selectedIds = new Set<number>()
  const blockedEntities = new Set<string>()

  for (const entry of entries) {
    const key = queueEntryKey(entry.entity, entry.entityId)

    if (blocksLaterEntries(entry)) blockedEntities.add(key)
    if (isPermanentlyStuck(entry)) continue
    if (blockedEntities.has(key)) continue

    const parent = parentRefOf(entry)
    if (parent && !parentCreateSelected(parent, entry, createsByEntity, selectedIds)) continue

    selected.push(entry)
    if (entry.id !== undefined) selectedIds.add(entry.id)
  }

  return selected
}

function summarizeFailures(results: PushResult[]): PushFailure[] {
  const grouped = new Map<string, PushFailure>()

  for (const result of results) {
    if (result.status !== 'error') continue

    const entity = result.entity ?? 'unknown'
    const entityId = result.entity_id
    const error = result.error ?? 'Unknown sync error'
    const key = `${entity}:${entityId}:${error}`
    const existing = grouped.get(key)

    if (existing) {
      existing.count += 1
      continue
    }

    grouped.set(key, {
      entity,
      entityId,
      error,
      count: 1,
    })
  }

  return [...grouped.values()]
}

export function formatPushFailures(failures: PushFailure[]): string | null {
  if (failures.length === 0) return null

  const failedChanges = failures.reduce((sum, failure) => sum + failure.count, 0)
  const lines = failures.slice(0, 3).map((failure) => {
    const repeated = failure.count > 1 ? ` (${failure.count}x)` : ''
    return `${failure.entity} ${failure.entityId}${repeated}: ${failure.error}`
  })

  if (failures.length > 3) {
    lines.push(`+${failures.length - 3} more`)
  }

  return `${failedChanges} sync change${failedChanges === 1 ? '' : 's'} failed.\n${lines.join('\n')}`
}

/**
 * Delete the skipped snapshot of every entity drained by this push: a
 * successful change removed the entity's last remaining queue entry.
 */
async function clearDrainedSnapshots(
  allEntries: SyncQueueEntry[],
  removedIds: number[],
): Promise<void> {
  const removed = new Set(removedIds)
  const checked = new Set<string>()
  const drained: Array<[SyncEntity, string]> = []

  for (const entry of allEntries) {
    if (entry.id === undefined || !removed.has(entry.id)) continue

    const key = queueEntryKey(entry.entity, entry.entityId)
    if (checked.has(key)) continue
    checked.add(key)

    const stillQueued = allEntries.some(
      (other) =>
        other.id !== undefined &&
        !removed.has(other.id) &&
        other.entity === entry.entity &&
        other.entityId === entry.entityId,
    )
    if (!stillQueued) {
      drained.push([entry.entity, entry.entityId])
    }
  }

  if (drained.length > 0) {
    await localDb.skippedSnapshots.bulkDelete(drained)
  }
}

function toClientChange(entry: SyncQueueEntry): ClientChange {
  return {
    entity: entry.entity,
    entity_id: entry.entityId,
    action: entry.action,
    fields: entry.fields,
    data: entry.data,
    client_updated_at: entry.clientUpdatedAt,
  }
}

export async function pushChanges(deviceId: string): Promise<PushSummary> {
  const allEntries = await localDb.syncQueue.orderBy('id').toArray()
  const entries = selectPushable(allEntries)

  if (entries.length === 0) {
    return {
      processedCount: 0,
      failures: [],
      stuckCount: await countStuckEntries(),
    }
  }

  let processedCount = 0
  const failures: PushFailure[] = []
  const removedIds: number[] = []

  // The server returns one result per sent change, in request order:
  // result i belongs to sent entry i.
  const applyResults = async (batch: SyncQueueEntry[], response: PushResponse): Promise<void> => {
    const idsToRemove: number[] = []
    const entryUpdates: Array<{ id: number; update: Partial<SyncQueueEntry> }> = []

    for (const [index, entry] of batch.entries()) {
      const result: PushResult | undefined = response.results[index]
      if (entry.id === undefined || result === undefined) continue

      if (isSuccess(result)) {
        idsToRemove.push(entry.id)
        continue
      }

      if (result.status !== 'error') continue

      if (isPermanent(result.code)) {
        // Retrying the same change can never succeed: keep it, but make the
        // problem visible instead of silently retrying forever.
        entryUpdates.push({
          id: entry.id,
          update: {
            state: 'stuck',
            errorCode: result.code,
            errorMessage: result.error ?? 'Unknown sync error',
          },
        })
        continue
      }

      // INTERNAL or missing code: transient, keep auto-retrying but count it.
      const internalFailures = (entry.internalFailures ?? 0) + 1
      const update: Partial<SyncQueueEntry> = {
        internalFailures,
        state: internalFailures >= INTERNAL_STUCK_THRESHOLD ? 'stuck' : 'pending',
        errorMessage: result.error ?? 'Unknown sync error',
      }
      if (result.code) update.errorCode = result.code
      entryUpdates.push({ id: entry.id, update })
    }

    if (idsToRemove.length > 0) {
      await localDb.syncQueue.bulkDelete(idsToRemove)
    }
    for (const { id, update } of entryUpdates) {
      await localDb.syncQueue.update(id, update)
    }

    processedCount += idsToRemove.length
    removedIds.push(...idsToRemove)
    failures.push(...summarizeFailures(response.results))
  }

  // Send one batch per request. On 413, retry the same entries in halves;
  // a single entry that still overflows is permanently stuck. Any other
  // failure stops the whole push: remaining entries stay pending.
  const sendBatch = async (batch: SyncQueueEntry[]): Promise<void> => {
    let response: PushResponse
    try {
      response = await api.post<PushResponse>('/sync/push', {
        device_id: deviceId,
        changes: batch.map(toClientChange),
      })
    } catch (error) {
      if (error instanceof ApiError && error.status === 413) {
        if (batch.length === 1) {
          const entry = batch[0]
          if (entry.id !== undefined) {
            await localDb.syncQueue.update(entry.id, {
              state: 'stuck',
              errorCode: 'TOO_LARGE',
              errorMessage: 'change is larger than the 10 MiB request limit',
            })
          }
          return
        }
        const half = Math.max(1, Math.floor(batch.length / 2))
        for (let start = 0; start < batch.length; start += half) {
          await sendBatch(batch.slice(start, start + half))
        }
        return
      }
      throw error
    }

    await applyResults(batch, response)
  }

  try {
    for (let start = 0; start < entries.length; start += PUSH_BATCH_SIZE) {
      await sendBatch(entries.slice(start, start + PUSH_BATCH_SIZE))
    }
  } finally {
    await clearDrainedSnapshots(allEntries, removedIds)
  }

  return {
    processedCount,
    failures,
    stuckCount: await countStuckEntries(),
  }
}
