import { api } from '../api/client'
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

async function countStuckEntries(): Promise<number> {
  return localDb.syncQueue.filter((entry) => entry.state === 'stuck').count()
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

    const key = `${entry.entity}+${entry.entityId}`
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

export async function pushChanges(deviceId: string): Promise<PushSummary> {
  const allEntries = await localDb.syncQueue.orderBy('createdAt').toArray()
  const entries = allEntries.filter((entry) => !isPermanentlyStuck(entry))

  if (entries.length === 0) {
    return {
      processedCount: 0,
      failures: [],
      stuckCount: await countStuckEntries(),
    }
  }

  const changes: ClientChange[] = entries.map((entry: SyncQueueEntry) => ({
    entity: entry.entity,
    entity_id: entry.entityId,
    action: entry.action,
    fields: entry.fields,
    data: entry.data,
    client_updated_at: entry.clientUpdatedAt,
  }))

  const response = await api.post<PushResponse>('/sync/push', {
    device_id: deviceId,
    changes,
  })

  // The server returns one result per sent change, in request order:
  // result i belongs to sent entry i.
  const idsToRemove: number[] = []
  const entryUpdates: Array<{ id: number; update: Partial<SyncQueueEntry> }> = []

  for (const [index, entry] of entries.entries()) {
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
  await clearDrainedSnapshots(allEntries, idsToRemove)

  return {
    processedCount: idsToRemove.length,
    failures: summarizeFailures(response.results),
    stuckCount: await countStuckEntries(),
  }
}
