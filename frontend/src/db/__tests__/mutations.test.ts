import { beforeEach, describe, expect, test, vi } from 'vitest'
import { localDb } from '../index'
import {
  createTask,
  updateTask,
  completeTask,
  deleteTask,
  createProject,
  createArea,
  createSchedule,
  deleteProject,
  deleteArea,
  updateProject,
  createChecklistItem,
  updateChecklistItem,
  deleteChecklistItem,
  createReminder,
  deleteReminder,
} from '../mutations'

// Mock syncNow so tests don't try to call the API
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

describe('mutations', () => {
  beforeEach(async () => {
    await clearDb()
  })

  test('createTask writes to IndexedDB and queues sync', async () => {
    const id = await createTask({ title: 'Test task' })

    const task = await localDb.tasks.get(id)
    expect(task?.title).toBe('Test task')
    expect(task?._syncStatus).toBe('pending')

    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(1)
    expect(queue[0].entity).toBe('task')
    expect(queue[0].action).toBe('create')
    expect(queue[0].entityId).toBe(id)
  })

  test('updateTask merges fields and queues sync', async () => {
    const id = await createTask({ title: 'Original' })
    await updateTask(id, { title: 'Updated' })

    const task = await localDb.tasks.get(id)
    expect(task?.title).toBe('Updated')

    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(2) // create + update
    expect(queue[1].fields).toContain('title')
    expect(queue[1].action).toBe('update')
    expect(queue[1].entityId).toBe(id)
  })

  test('completeTask sets status and completed_at', async () => {
    const id = await createTask({ title: 'Todo' })
    await completeTask(id)

    const task = await localDb.tasks.get(id)
    expect(task?.status).toBe('completed')
    expect(task?.completed_at).toBeTruthy()
  })

  test('deleteTask soft-deletes with deleted_at', async () => {
    const id = await createTask({ title: 'To delete' })
    await deleteTask(id)

    const task = await localDb.tasks.get(id)
    // Task still in DB (soft delete)
    expect(task).toBeDefined()
    expect(task?.deleted_at).toBeTruthy()
  })

  test('updateTask on non-existent id is a no-op', async () => {
    await updateTask('nonexistent', { title: 'Ghost' })
    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(0)
  })

  test('createTask sets correct defaults', async () => {
    const id = await createTask({})
    const task = await localDb.tasks.get(id)
    expect(task?.title).toBe('')
    expect(task?.status).toBe('open')
    expect(task?.high_priority).toBe(false)
    expect(task?._syncStatus).toBe('pending')
  })

  test('syncFirstScheduleDate moves the first entry of a multi-entry task', async () => {
    const id = await createTask({ title: 'Multi', when_date: '2026-01-10' })
    await createSchedule({ task_id: id, when_date: '2026-02-20', sort_order: 1 })

    await updateTask(id, { when_date: '2026-01-05' })

    const schedules = await localDb.schedules.where('task_id').equals(id).sortBy('sort_order')
    expect(schedules).toHaveLength(2)
    expect(schedules[0].when_date).toBe('2026-01-05')
    expect(schedules[1].when_date).toBe('2026-02-20')
  })

  test('deleting a project locally moves its tasks to Trash and clears project_id and area_id', async () => {
    const areaId = await createArea({ title: 'Area' })
    const projectId = await createProject({ title: 'Project' })
    const liveTask = await createTask({
      title: 'Live task',
      project_id: projectId,
      area_id: areaId,
    })
    const trashedTask = await createTask({
      title: 'Already trashed',
      project_id: projectId,
      deleted_at: '2026-01-01T00:00:00.000Z',
    })
    const outsideTask = await createTask({ title: 'Outside', area_id: areaId })

    await deleteProject(projectId)

    // Live task moved to Trash with containers cleared (as projects.go DeleteToTrash)
    const live = await localDb.tasks.get(liveTask)
    expect(live?.deleted_at).toBeTruthy()
    expect(live?.project_id).toBeNull()
    expect(live?.area_id).toBeNull()
    // Already-trashed task keeps its trash date, containers cleared
    const trashed = await localDb.tasks.get(trashedTask)
    expect(trashed?.deleted_at).toBe('2026-01-01T00:00:00.000Z')
    expect(trashed?.project_id).toBeNull()
    // Tasks outside the project are untouched
    const outside = await localDb.tasks.get(outsideTask)
    expect(outside?.deleted_at).toBeFalsy()
    expect(outside?.area_id).toBe(areaId)
    // Project row is gone and the delete is queued for sync
    expect(await localDb.projects.get(projectId)).toBeUndefined()
    const queue = await localDb.syncQueue.toArray()
    expect(queue.some((e) => e.entity === 'project' && e.action === 'delete' && e.entityId === projectId)).toBe(true)
  })

  test('deleting an area locally moves its loose tasks to Trash', async () => {
    const areaId = await createArea({ title: 'Area' })
    const projectId = await createProject({ title: 'Project' })
    const looseTask = await createTask({ title: 'Loose', area_id: areaId })
    const projectTask = await createTask({ title: 'In project', area_id: areaId, project_id: projectId })

    await deleteArea(areaId)

    // Loose task moved to Trash with the area cleared (as areas.go DeleteToTrash)
    const loose = await localDb.tasks.get(looseTask)
    expect(loose?.deleted_at).toBeTruthy()
    expect(loose?.area_id).toBeNull()
    // Project tasks are not the area's concern
    const inProject = await localDb.tasks.get(projectTask)
    expect(inProject?.deleted_at).toBeFalsy()
    expect(inProject?.area_id).toBe(areaId)
    expect(await localDb.areas.get(areaId)).toBeUndefined()
  })

  test('createTask with when_date creates the first schedule entry', async () => {
    const id = await createTask({ title: 'Dated', when_date: '2026-01-10' })

    const schedules = await localDb.schedules.where('task_id').equals(id).toArray()
    expect(schedules).toHaveLength(1)
    expect(schedules[0].when_date).toBe('2026-01-10')
    expect(schedules[0]._syncStatus).toBe('pending')
  })

  test('clearing when_date deletes every schedule entry of the task', async () => {
    const id = await createTask({ title: 'Multi', when_date: '2026-01-10' })
    await createSchedule({ task_id: id, when_date: '2026-02-20', sort_order: 1 })
    expect(await localDb.schedules.where('task_id').equals(id).count()).toBe(2)

    await updateTask(id, { when_date: null })

    expect(await localDb.schedules.where('task_id').equals(id).count()).toBe(0)
    const task = await localDb.tasks.get(id)
    expect(task?.when_date).toBeNull()
  })

  test('setting the same when_date leaves the first schedule entry untouched', async () => {
    const id = await createTask({ title: 'Same date', when_date: '2026-01-10' })
    const before = (await localDb.schedules.where('task_id').equals(id).toArray())[0]

    await updateTask(id, { when_date: '2026-01-10' })

    const after = (await localDb.schedules.where('task_id').equals(id).toArray())[0]
    expect(after._localUpdatedAt).toBe(before._localUpdatedAt)
  })

  test('project status change queues a status-only update and leaves member tasks untouched', async () => {
    const projectId = await createProject({ title: 'Project' })
    const taskId = await createTask({ title: 'Member', project_id: projectId })

    await updateProject(projectId, { status: 'completed' })

    // Project row updated
    const project = await localDb.projects.get(projectId)
    expect(project?.status).toBe('completed')
    // Member tasks are not cascaded (server Complete does not touch tasks)
    const task = await localDb.tasks.get(taskId)
    expect(task?.status).toBe('open')
    expect(task?.deleted_at).toBeFalsy()
    // The queued change carries only the status field
    const queue = await localDb.syncQueue.toArray()
    const statusEntry = queue.find(
      (e) => e.entity === 'project' && e.entityId === projectId && e.action === 'update',
    )
    expect(statusEntry?.fields).toEqual(['status'])
    expect(statusEntry?.data).toEqual({ status: 'completed' })
  })

  test('checklist item changes refresh checklist_count and checklist_done on the task', async () => {
    const id = await createTask({ title: 'With checklist' })
    const first = await createChecklistItem({ task_id: id, title: 'One' })
    const second = await createChecklistItem({ task_id: id, title: 'Two' })

    let task = await localDb.tasks.get(id)
    expect(task?.checklist_count).toBe(2)
    expect(task?.checklist_done).toBe(0)

    await updateChecklistItem(first, { completed: true })
    task = await localDb.tasks.get(id)
    expect(task?.checklist_done).toBe(1)

    await deleteChecklistItem(second)
    task = await localDb.tasks.get(id)
    expect(task?.checklist_count).toBe(1)
    expect(task?.checklist_done).toBe(1)
  })

  test('reminder create and delete refresh has_reminders and first_reminder_* flags', async () => {
    const id = await createTask({ title: 'With reminder' })

    await createReminder({ task_id: id, type: 'minutes_before', value: 30 })
    let task = await localDb.tasks.get(id)
    expect(task?.has_reminders).toBe(true)
    expect(task?.first_reminder_type).toBe('minutes_before')
    expect(task?.first_reminder_value).toBe(30)

    const reminders = await localDb.reminders.where('task_id').equals(id).toArray()
    await deleteReminder(reminders[0].id)
    task = await localDb.tasks.get(id)
    expect(task?.has_reminders).toBe(false)
    expect(task?.first_reminder_type).toBeNull()
    expect(task?.first_reminder_value).toBeNull()
    expect(task?.first_reminder_exact_at).toBeNull()
  })
})
