import { localDb } from '../db/index'
import { stopSyncEngine } from './engine'
import { closeSSE } from '../hooks/useSSE'

const TIMEZONE_KEY = 'thingstodo_timezone'

/**
 * Wipe all local data on logout: stop the sync engine and SSE first, then
 * drop the whole database (including any unsynced sync-queue entries) and
 * device-local settings. The database is recreated empty so the next
 * login works without a page reload.
 */
export async function clearLocalData(): Promise<void> {
  stopSyncEngine()
  closeSSE()
  await localDb.delete()
  await localDb.open()
  localStorage.removeItem(TIMEZONE_KEY)
}
