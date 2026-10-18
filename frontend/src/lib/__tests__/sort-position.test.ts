import { describe, expect, test } from 'vitest'
import { calculatePosition } from '../sort-position'
import type { Task } from '../../api/types'
import type { SortField } from '../../api/types'

function tasksWithOrders(orders: number[], sortField: SortField): Task[] {
  return orders.map((order, i) =>
    ({
      id: `task-${i}`,
      sort_order_today: sortField === 'sort_order_today' ? order : 0,
      sort_order_project: sortField === 'sort_order_project' ? order : 0,
      sort_order_heading: sortField === 'sort_order_heading' ? order : 0,
    }) as unknown as Task,
  )
}

describe('calculatePosition', () => {
  test('empty list starts at 1024', () => {
    expect(calculatePosition([], 0, 'sort_order_today')).toBe(1024)
  })

  test('before the first task halves the first order', () => {
    const tasks = tasksWithOrders([2048, 4096], 'sort_order_today')
    expect(calculatePosition(tasks, 0, 'sort_order_today')).toBe(1024)
  })

  test('after the last task appends 1024', () => {
    const tasks = tasksWithOrders([100, 200], 'sort_order_today')
    expect(calculatePosition(tasks, 2, 'sort_order_today')).toBe(1224)
    // An index past the end behaves like the end
    expect(calculatePosition(tasks, 99, 'sort_order_today')).toBe(1224)
  })

  test('between two neighbours returns their midpoint', () => {
    const tasks = tasksWithOrders([100, 200, 300], 'sort_order_today')
    expect(calculatePosition(tasks, 1, 'sort_order_today')).toBe(150)
    expect(calculatePosition(tasks, 2, 'sort_order_today')).toBe(250)
  })

  test('equal neighbours keep the shared order (no movement)', () => {
    const tasks = tasksWithOrders([500, 500], 'sort_order_today')
    expect(calculatePosition(tasks, 1, 'sort_order_today')).toBe(500)
  })

  test('reads the order of the requested sort field', () => {
    const orders = [100, 200, 300]
    const today = tasksWithOrders(orders, 'sort_order_today')
    const project = tasksWithOrders(orders, 'sort_order_project')
    const heading = tasksWithOrders(orders, 'sort_order_heading')

    expect(calculatePosition(today, 1, 'sort_order_project')).toBe(0) // project orders are all 0
    expect(calculatePosition(project, 1, 'sort_order_project')).toBe(150)
    expect(calculatePosition(heading, 0, 'sort_order_heading')).toBe(50)
  })
})
