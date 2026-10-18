import type {
  Task,
  Project,
  Area,
  Tag,
  ChecklistItem,
  Attachment,
  TaskSchedule,
  Reminder,
  RepeatRule,
  Heading,
} from '../api/types'

// Sync metadata added to every local entity
export interface SyncMeta {
  _syncStatus: 'synced' | 'pending' | 'conflict'
  _localUpdatedAt: string
  _serverSeq?: number
}

// Local entity types: server type + sync metadata
export type LocalTask = Task & SyncMeta
export type LocalProject = Project & SyncMeta
export type LocalArea = Area & SyncMeta
export type LocalTag = Tag & SyncMeta
export type LocalChecklistItem = ChecklistItem & SyncMeta & { task_id: string }
export type LocalAttachment = Attachment & SyncMeta & { task_id: string }
export type LocalSchedule = TaskSchedule & SyncMeta
export type LocalReminder = Reminder & SyncMeta & { task_id: string }
export type LocalRepeatRule = RepeatRule & SyncMeta & { task_id: string }
export type LocalHeading = Heading & SyncMeta

// Sync queue: records pending mutations to replay against the server
export type SyncAction = 'create' | 'update' | 'delete'
export type SyncEntity =
  | 'task'
  | 'project'
  | 'area'
  | 'tag'
  | 'checklistItem'
  | 'attachment'
  | 'schedule'
  | 'reminder'
  | 'repeatRule'
  | 'heading'

export type SyncErrorCode = 'VALIDATION' | 'NOT_FOUND' | 'CONFLICT' | 'INTERNAL' | 'TOO_LARGE'

export interface SyncQueueEntry {
  id?: number // auto-increment primary key
  entity: SyncEntity
  entityId: string
  action: SyncAction
  /** Specific fields changed (for partial updates) */
  fields?: string[]
  /** Full payload to send to the server */
  data: Record<string, unknown>
  clientUpdatedAt: string
  createdAt: string
  /** undefined === 'pending' */
  state?: 'pending' | 'stuck'
  errorCode?: SyncErrorCode
  errorMessage?: string
  /** consecutive INTERNAL results */
  internalFailures?: number
}

// Latest server snapshot skipped while a local change was pending (one per entity)
export interface SkippedSnapshot {
  entity: SyncEntity
  entityId: string
  action: SyncAction
  snapshot: Record<string, unknown>
  seq: number
}

// Key-value store for sync cursor and other metadata
export interface SyncMetaRecord {
  key: string
  value: unknown
}
