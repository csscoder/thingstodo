package repository

import "fmt"

// openNotDeleted is the base state every open-task list shares.
const openNotDeleted = "t.status = 'open' AND t.deleted_at IS NULL"

// overdueCond matches the Overdue section of the Today and Upcoming views:
// open, not trashed, deadline before today. Both views compute it relative
// to today, never to Upcoming's from.
func overdueCond(today string) (string, []any) {
	return openNotDeleted + " AND t.deadline < ?", []any{today}
}

// earlierCond matches the Earlier section of the Today and Upcoming views:
// open, not trashed, when_date before today and not someday, regardless of
// schedule rows (E3). Tasks with an overdue deadline may appear here and in
// Overdue at once.
func earlierCond(today string) (string, []any) {
	return openNotDeleted + " AND t.when_date < ? AND t.when_date != 'someday'", []any{today}
}

// todaySectionsCond matches the Today and This Evening sections before the
// start_time split: open, not trashed, and either due today (deadline =
// today) or holding an uncompleted schedule entry for today. A task with a
// when_date and no schedule rows counts as one uncompleted schedule on that
// when_date (E3), so a rowless task dated today matches. A dated task whose
// schedule rows all exist and are completed matches nothing — the single
// documented exception where an open task appears in no list.
func todaySectionsCond(today string) (string, []any) {
	return openNotDeleted + ` AND (
		t.deadline = ?
		OR EXISTS(SELECT 1 FROM task_schedules ts
			WHERE ts.task_id = t.id AND ts.when_date = ? AND ts.completed = 0)
		OR (t.when_date = ?
			AND NOT EXISTS(SELECT 1 FROM task_schedules ts WHERE ts.task_id = t.id))
	)`, []any{today, today, today}
}

// upcomingDatesCond matches the date groups of the Upcoming view with its
// default from = tomorrow: open, not trashed, with an uncompleted schedule
// entry after today, or the synthetic entry of a rowless task dated after
// today; someday never counts. upcomingDatesJoinCond is the JOIN-shaped twin
// the Upcoming builder uses; for from = tomorrow the two are equivalent.
func upcomingDatesCond(today string) (string, []any) {
	return openNotDeleted + ` AND (
		EXISTS(SELECT 1 FROM task_schedules ts
			WHERE ts.task_id = t.id AND ts.when_date > ? AND ts.when_date != 'someday' AND ts.completed = 0)
		OR (t.when_date > ? AND t.when_date != 'someday'
			AND NOT EXISTS(SELECT 1 FROM task_schedules ts WHERE ts.task_id = t.id))
	)`, []any{today, today}
}

// upcomingDatesJoinCond is the WHERE clause of the Upcoming Dates query,
// which LEFT JOINs task_schedules ts so a task appears once per future date.
// It expresses the same membership as upcomingDatesCond with the join's
// lower bound from in place of "after today": a joined uncompleted entry, or
// the synthetic entry of a rowless task dated on or after from.
func upcomingDatesJoinCond(from string) (string, []any) {
	return `t.status = 'open' AND t.deleted_at IS NULL AND (
		ts.id IS NOT NULL
		OR (t.when_date >= ? AND t.when_date != 'someday'
			AND NOT EXISTS(SELECT 1 FROM task_schedules ts2 WHERE ts2.task_id = t.id))
	)`, []any{from}
}

// listCondition returns cond, a SQL boolean expression over the tasks alias
// t, meaning "this task appears in view list". today must already be
// resolved by the caller in the server timezone (r.today()); listCondition
// never reads the clock itself. eveningStartsAt carries the user's evening
// threshold for interface parity with the Today view; whole-view membership
// does not depend on it, because a task in This Evening is in the today view
// at any threshold. An unknown list returns an error.
//
// View rules (spec E3):
//   - inbox:     open, not trashed, no project, no area, no when_date. A
//     deadline-only loose task is Inbox, never Anytime, and still lands in
//     Today (deadline today) or Overdue (deadline passed).
//   - today:     the union of the four open sections — Today/This Evening
//     (todaySectionsCond), Overdue and Earlier.
//   - upcoming:  the union of the date groups from tomorrow
//     (upcomingDatesCond), Overdue and Earlier, both relative to today.
//   - anytime:   open, not trashed, no when_date, and owned by a project or
//     an area; loose tasks are Inbox only.
//   - someday:   open, not trashed, when_date = 'someday'.
//   - logbook:   completed, canceled or wont_do, not trashed.
//   - trash:     soft-deleted, any status.
//
// The view builders, Counts and the list= filter (C1) all share these
// predicates, so counts always equal the distinct ids of their view.
func listCondition(list, today, eveningStartsAt string) (cond string, args []any, err error) {
	switch list {
	case "inbox":
		return openNotDeleted +
			" AND t.project_id IS NULL AND t.area_id IS NULL AND t.when_date IS NULL", nil, nil
	case "today":
		sections, sectionArgs := todaySectionsCond(today)
		overdue, overdueArgs := overdueCond(today)
		earlier, earlierArgs := earlierCond(today)
		return "(" + sections + " OR " + overdue + " OR " + earlier + ")",
			append(append(sectionArgs, overdueArgs...), earlierArgs...), nil
	case "upcoming":
		dates, dateArgs := upcomingDatesCond(today)
		overdue, overdueArgs := overdueCond(today)
		earlier, earlierArgs := earlierCond(today)
		return "(" + dates + " OR " + overdue + " OR " + earlier + ")",
			append(append(dateArgs, overdueArgs...), earlierArgs...), nil
	case "anytime":
		return openNotDeleted +
			" AND t.when_date IS NULL AND (t.project_id IS NOT NULL OR t.area_id IS NOT NULL)", nil, nil
	case "someday":
		return openNotDeleted + " AND t.when_date = 'someday'", nil, nil
	case "logbook":
		return "t.status IN ('completed', 'canceled', 'wont_do') AND t.deleted_at IS NULL", nil, nil
	case "trash":
		return "t.deleted_at IS NOT NULL", nil, nil
	default:
		return "", nil, fmt.Errorf("unknown list %q", list)
	}
}
