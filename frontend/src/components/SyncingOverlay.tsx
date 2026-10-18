interface SyncingOverlayProps {
  show: boolean
  error?: string | null
  onRetry?: () => void
}

export function SyncingOverlay({ show, error, onRetry }: SyncingOverlayProps) {
  if (!show) return null

  return (
    <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-white dark:bg-neutral-900">
      {error ? (
        <>
          <p className="text-sm text-neutral-500 dark:text-neutral-400">
            Couldn't load your data: {error}
          </p>
          {onRetry && (
            <button
              type="button"
              onClick={onRetry}
              className="mt-4 rounded-lg bg-red-500 px-4 py-2 text-sm font-medium text-white active:bg-red-600"
            >
              Retry
            </button>
          )}
        </>
      ) : (
        <>
          <div className="h-8 w-8 animate-spin rounded-full border-4 border-red-500 border-t-transparent" />
          <p className="mt-4 text-sm text-neutral-500 dark:text-neutral-400">Syncing your tasks…</p>
        </>
      )}
    </div>
  )
}
