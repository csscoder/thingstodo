import { useCallback, useEffect, useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { localDb } from '../db/index'
import type { SyncEntity, SyncQueueEntry } from '../db/schema'
import {
  discardEntry,
  entriesRemovedByDiscard,
  listStuckEntries,
  retryEntry,
} from '../sync/queueActions'
import { useSyncStore } from '../sync/status'
import { ConfirmDialog } from './ConfirmDialog'

const ENTITY_LABELS: Record<SyncEntity, string> = {
  task: 'Task',
  project: 'Project',
  area: 'Area',
  tag: 'Tag',
  checklistItem: 'Checklist item',
  attachment: 'Attachment',
  schedule: 'Schedule',
  reminder: 'Reminder',
  repeatRule: 'Repeat rule',
  heading: 'Heading',
}

/** Human-readable name of a queue entry: the task row title when available,
 * else the payload's title, else the entity id. */
async function displayTitle(entry: SyncQueueEntry): Promise<string> {
  if (entry.entity === 'task') {
    const task = await localDb.tasks.get(entry.entityId)
    if (task?.title) return task.title
  }
  const title = entry.data?.title
  if (typeof title === 'string' && title.length > 0) return title
  return entry.entityId
}

interface PendingDiscard {
  entryId: number
  lines: string[]
  count: number
}

interface StuckChangesDialogProps {
  open: boolean
  onClose: () => void
}

export function StuckChangesDialog({ open, onClose }: StuckChangesDialogProps) {
  const setStuckCount = useSyncStore((s) => s.setStuckCount)
  const [entries, setEntries] = useState<SyncQueueEntry[]>([])
  const [titles, setTitles] = useState<Record<number, string>>({})
  const [pendingDiscard, setPendingDiscard] = useState<PendingDiscard | null>(null)
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(async () => {
    const stuck = await listStuckEntries()
    setEntries(stuck)
    const nextTitles: Record<number, string> = {}
    for (const entry of stuck) {
      if (entry.id !== undefined) nextTitles[entry.id] = await displayTitle(entry)
    }
    setTitles(nextTitles)
    setStuckCount(stuck.length)
  }, [setStuckCount])

  useEffect(() => {
    if (open) void refresh()
  }, [open, refresh])

  async function handleRetry(entryId: number) {
    setBusy(true)
    try {
      await retryEntry(entryId)
      await refresh()
    } finally {
      setBusy(false)
    }
  }

  async function handleDiscardClick(entryId: number) {
    const affected = await entriesRemovedByDiscard(entryId)
    const lines: string[] = []
    for (const entry of affected) {
      const title = entry.id !== undefined ? titles[entry.id] : undefined
      lines.push(`${entry.action} ${ENTITY_LABELS[entry.entity]} ${title ?? (await displayTitle(entry))}`)
    }
    setPendingDiscard({ entryId, lines, count: affected.length })
  }

  async function handleConfirmDiscard() {
    if (!pendingDiscard) return
    const { entryId } = pendingDiscard
    setPendingDiscard(null)
    setBusy(true)
    try {
      await discardEntry(entryId)
      await refresh()
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  return (
    <Dialog.Root open onOpenChange={(next) => { if (!next) onClose() }}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/40" />
        <Dialog.Content
          aria-describedby={undefined}
          className="fixed left-1/2 top-1/2 mx-4 flex max-h-[80vh] w-full max-w-lg -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl bg-white p-4 shadow-xl md:mx-0 md:p-6 dark:bg-neutral-800"
        >
          <div className="mb-4 flex items-center justify-between">
            <Dialog.Title className="text-lg font-semibold text-neutral-900 dark:text-neutral-100">
              Stuck changes
            </Dialog.Title>
            <Dialog.Close
              className="rounded-lg p-1 text-neutral-400 hover:bg-neutral-100 dark:hover:bg-neutral-700"
              aria-label="Close"
            >
              <X size={18} />
            </Dialog.Close>
          </div>

          {entries.length === 0 ? (
            <p className="py-6 text-center text-sm text-neutral-500 dark:text-neutral-400">
              No stuck changes.
            </p>
          ) : (
            <div className="max-h-[50vh] space-y-2 overflow-y-auto">
              {entries.map((entry) => {
                if (entry.id === undefined) return null
                const id = entry.id
                return (
                  <div
                    key={id}
                    className="rounded-lg border border-neutral-200 p-3 dark:border-neutral-700"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="min-w-0">
                        <span className="text-xs font-medium uppercase tracking-wide text-neutral-400 dark:text-neutral-500">
                          {ENTITY_LABELS[entry.entity]}
                        </span>
                        <p className="truncate text-sm font-medium text-neutral-900 dark:text-neutral-100">
                          {titles[id] ?? entry.entityId}
                        </p>
                      </div>
                      <div className="flex shrink-0 gap-2">
                        <button
                          onClick={() => void handleRetry(id)}
                          disabled={busy}
                          className="rounded-md px-2.5 py-1 text-xs font-medium text-neutral-700 hover:bg-neutral-100 disabled:opacity-50 dark:text-neutral-300 dark:hover:bg-neutral-700"
                        >
                          Retry
                        </button>
                        <button
                          onClick={() => void handleDiscardClick(id)}
                          disabled={busy}
                          className="rounded-md px-2.5 py-1 text-xs font-medium text-red-600 hover:bg-red-50 disabled:opacity-50 dark:text-red-400 dark:hover:bg-red-950/40"
                        >
                          Discard
                        </button>
                      </div>
                    </div>
                    {entry.errorMessage ? (
                      <p
                        className="mt-1 truncate text-xs text-red-500 dark:text-red-400"
                        title={entry.errorMessage}
                      >
                        {entry.errorMessage}
                      </p>
                    ) : null}
                  </div>
                )
              })}
            </div>
          )}

          <ConfirmDialog
            open={pendingDiscard !== null}
            title="Discard stuck changes?"
            description={`These local changes will be removed and cannot be undone:\n${pendingDiscard?.lines.join('\n') ?? ''}`}
            confirmLabel={`Discard ${pendingDiscard?.count ?? 0} change(s)`}
            destructive
            onConfirm={() => void handleConfirmDiscard()}
            onCancel={() => setPendingDiscard(null)}
          />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
