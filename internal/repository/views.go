package repository

import (
	"database/sql"
	"strconv"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

type ViewRepository struct {
	db  *sql.DB
	loc *time.Location
	now func() time.Time
}

// NewViewRepository binds the view queries to the server timezone loc. A nil
// loc defensively defaults to time.UTC. The clock defaults to time.Now and
// can be overridden with SetClock.
func NewViewRepository(db *sql.DB, loc *time.Location) *ViewRepository {
	if loc == nil {
		loc = time.UTC
	}
	return &ViewRepository{db: db, loc: loc, now: time.Now}
}

// SetClock overrides the view clock (default time.Now). "Today" and every
// date boundary is resolved as r.now().In(r.loc).
func (r *ViewRepository) SetClock(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

// today returns the current calendar date in the server timezone.
func (r *ViewRepository) today() string {
	return r.now().In(r.loc).Format("2006-01-02")
}

// reviewCutoff returns the local date that review staleness is measured
// against: local today minus days.
func (r *ViewRepository) reviewCutoff(days int) string {
	return r.now().In(r.loc).AddDate(0, 0, -days).Format("2006-01-02")
}

// localDayUTCBounds returns the UTC instants of local midnight starting the
// calendar day day (inclusive) and of the next local midnight (exclusive),
// both formatted "2006-01-02 15:04:05" so stored UTC timestamp strings can be
// compared lexically. DST-safe: the end bound is constructed as the next
// calendar midnight in loc, not as start + 24h, so 23- and 25-hour days come
// out right. An unparseable day yields empty strings.
func localDayUTCBounds(day string, loc *time.Location) (startUTC, endUTC string) {
	if loc == nil {
		loc = time.UTC
	}
	start, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return "", ""
	}
	y, m, d := start.Date()
	end := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	return start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05")
}

// utcToLocalDay converts a stored UTC timestamp to its calendar date in loc.
// Timestamps are written by SQLite datetime('now') as "2006-01-02 15:04:05";
// RFC 3339 forms (sync-pushed values) are accepted too. Unparseable input
// falls back to its date prefix so grouping never drops a task.
func utcToLocalDay(ts string, loc *time.Location) string {
	if len(ts) < 10 {
		return ts
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.In(loc).Format("2006-01-02")
		}
	}
	return ts[:10]
}

func (r *ViewRepository) Inbox(reviewAfterDays *int, includeRecurring bool) (*model.InboxView, error) {
	inboxCond, inboxArgs, err := listCondition("inbox", r.today(), "")
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + inboxCond + `
		ORDER BY t.sort_order_today ASC`, inboxArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	inboxTasks := scanTaskListItems(r.db, rows)
	populateActionableScheduleFlags(r.db, inboxTasks, r.today())

	// Collect inbox task IDs to exclude from review
	inboxIDs := make(map[string]bool, len(inboxTasks))
	for _, t := range inboxTasks {
		inboxIDs[t.ID] = true
	}

	var reviewTasks []model.TaskListItem
	if reviewAfterDays != nil && *reviewAfterDays > 0 {
		reviewRows, err := r.db.Query(`
			SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
				t.deadline, t.project_id, t.area_id, t.heading_id,
				t.sort_order_today, t.sort_order_project, t.sort_order_heading,
				t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
				COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
				COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
				CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
				(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
				(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
				(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
				(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
			FROM tasks t
			WHERE t.status = 'open' AND t.deleted_at IS NULL
				AND date(t.updated_at) < ?
			ORDER BY t.updated_at ASC`, r.reviewCutoff(*reviewAfterDays))
		if err != nil {
			return nil, err
		}
		defer func() { _ = reviewRows.Close() }()
		allReview := scanTaskListItems(r.db, reviewRows)
		populateActionableScheduleFlags(r.db, allReview, r.today())

		// Exclude tasks already in inbox, and optionally exclude recurring tasks
		for _, t := range allReview {
			if inboxIDs[t.ID] {
				continue
			}
			if !includeRecurring && t.HasRepeatRule {
				continue
			}
			reviewTasks = append(reviewTasks, t)
		}
	}
	if reviewTasks == nil {
		reviewTasks = []model.TaskListItem{}
	}

	return &model.InboxView{Tasks: inboxTasks, Review: reviewTasks}, nil
}

func (r *ViewRepository) Today(eveningStartsAt string) (*model.TodayView, error) {
	today := r.today()

	// Today tasks: LEFT JOIN task_schedules so multi-schedule entries for
	// today each appear. Membership comes from todaySectionsCond; the day
	// section keeps entries with no start time or one before eveningStartsAt.
	sectionsCond, sectionsArgs := todaySectionsCond(today)
	todayRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			ts.start_time,
			ts.end_time,
			ts.id AS schedule_entry_id
		FROM tasks t
		LEFT JOIN task_schedules ts ON ts.task_id = t.id AND ts.when_date = ?
		WHERE ` + sectionsCond + `
			AND (ts.start_time IS NULL OR ts.start_time < ?)
		ORDER BY t.sort_order_today ASC, ts.start_time ASC`,
		append([]any{today}, append(sectionsArgs, eveningStartsAt)...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = todayRows.Close() }()
	todayTasks := scanTodayTaskListItems(r.db, todayRows)
	populateActionableScheduleFlags(r.db, todayTasks, r.today())

	// Evening tasks: same membership, but the joined entry's start_time is
	// at or after eveningStartsAt.
	eveningRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			ts.start_time,
			ts.end_time,
			ts.id AS schedule_entry_id
		FROM tasks t
		LEFT JOIN task_schedules ts ON ts.task_id = t.id AND ts.when_date = ?
		WHERE ` + sectionsCond + `
			AND ts.start_time IS NOT NULL AND ts.start_time >= ?
		ORDER BY t.sort_order_today ASC, ts.start_time ASC`,
		append([]any{today}, append(sectionsArgs, eveningStartsAt)...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = eveningRows.Close() }()
	eveningTasks := scanTodayTaskListItems(r.db, eveningRows)
	populateActionableScheduleFlags(r.db, eveningTasks, r.today())

	// Overdue
	overdueCondSQL, overdueArgs := overdueCond(today)
	overdueRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + overdueCondSQL + `
		ORDER BY t.deadline ASC`, overdueArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = overdueRows.Close() }()
	overdueTasks := scanTaskListItems(r.db, overdueRows)
	populateActionableScheduleFlags(r.db, overdueTasks, r.today())

	// Earlier: open tasks with when_date before today and not someday,
	// regardless of schedule rows (E3). A task with an overdue deadline may
	// appear here and in Overdue at once.
	earlierCondSQL, earlierArgs := earlierCond(today)
	earlierRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id AND when_date < ? AND when_date != 'someday' AND completed = 0 ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id AND when_date < ? AND when_date != 'someday' AND completed = 0 ORDER BY sort_order ASC LIMIT 1),
			(SELECT id FROM task_schedules WHERE task_id = t.id AND when_date < ? AND when_date != 'someday' AND completed = 0 ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + earlierCondSQL + `
		ORDER BY t.when_date ASC, t.sort_order_today ASC`,
		append([]any{today, today, today}, earlierArgs...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = earlierRows.Close() }()
	earlierTasks := scanTodayTaskListItems(r.db, earlierRows)
	populatePastScheduleCounts(r.db, earlierTasks, today)
	populateActionableScheduleFlags(r.db, earlierTasks, r.today())

	// Completed today: the stored UTC timestamps must fall inside the local
	// calendar day, i.e. between the UTC instants of this local midnight
	// (inclusive) and the next one (exclusive).
	completedStart, completedEnd := localDayUTCBounds(today, r.loc)
	completedRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE t.status IN ('completed', 'canceled', 'wont_do')
			AND COALESCE(t.completed_at, t.canceled_at, t.updated_at) >= ?
			AND COALESCE(t.completed_at, t.canceled_at, t.updated_at) < ?
			AND t.deleted_at IS NULL
		ORDER BY COALESCE(t.completed_at, t.canceled_at, t.updated_at) DESC`, completedStart, completedEnd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = completedRows.Close() }()
	completedTasks := scanTaskListItems(r.db, completedRows)

	return &model.TodayView{
		Sections: []model.TodaySection{
			{Title: "Today", Groups: groupByProject(r.db, todayTasks)},
			{Title: "This Evening", Groups: groupByProject(r.db, eveningTasks)},
		},
		Overdue:   overdueTasks,
		Earlier:   earlierTasks,
		Completed: completedTasks,
	}, nil
}

func (r *ViewRepository) Upcoming(from string) (*model.UpcomingView, error) {
	today := r.today()
	if from == "" {
		// E3: Upcoming starts tomorrow unless from overrides it.
		from = r.now().In(r.loc).AddDate(0, 0, 1).Format("2006-01-02")
	}
	// LEFT JOIN task_schedules so a task with multiple schedule dates appears
	// once per date, and a task with a when_date but no schedule rows appears
	// on its when_date (the synthetic schedule of E3).
	datesCond, datesArgs := upcomingDatesJoinCond(from)
	rows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			ts.start_time,
			ts.end_time,
			ts.id AS schedule_entry_id,
			COALESCE(ts.when_date, t.when_date) AS schedule_date
		FROM tasks t
		LEFT JOIN task_schedules ts ON ts.task_id = t.id AND ts.when_date >= ? AND ts.when_date != 'someday' AND ts.completed = 0
		WHERE ` + datesCond + `
		ORDER BY COALESCE(ts.when_date, t.when_date) ASC, ts.start_time ASC, t.sort_order_today ASC`,
		append([]any{from}, datesArgs...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	tasks := scanUpcomingTaskListItems(r.db, rows)

	// Group by schedule_date (which may differ from t.when_date for multi-date tasks)
	dateMap := make(map[string][]model.TaskListItem)
	var dateOrder []string
	for _, item := range tasks {
		d := item.date
		if _, ok := dateMap[d]; !ok {
			dateOrder = append(dateOrder, d)
		}
		dateMap[d] = append(dateMap[d], item.task)
	}

	var dates []model.DateGroup
	for _, d := range dateOrder {
		group := dateMap[d]
		populateActionableScheduleFlags(r.db, group, r.today())
		dates = append(dates, model.DateGroup{Date: d, Tasks: group})
	}
	if dates == nil {
		dates = []model.DateGroup{}
	}

	// Overdue: tasks with deadline before today (E3: relative to today,
	// never to from)
	overdueCondSQL, overdueArgs := overdueCond(today)
	overdueRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + overdueCondSQL + `
		ORDER BY t.deadline ASC`, overdueArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = overdueRows.Close() }()
	overdueTasks := scanTaskListItems(r.db, overdueRows)
	populateActionableScheduleFlags(r.db, overdueTasks, r.today())

	// Earlier: open tasks with when_date before today and not someday,
	// regardless of schedule rows (E3: relative to today, never to from)
	earlierCondSQL, earlierArgs := earlierCond(today)
	earlierRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id AND when_date < ? AND when_date != 'someday' AND completed = 0 ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id AND when_date < ? AND when_date != 'someday' AND completed = 0 ORDER BY sort_order ASC LIMIT 1),
			(SELECT id FROM task_schedules WHERE task_id = t.id AND when_date < ? AND when_date != 'someday' AND completed = 0 ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + earlierCondSQL + `
		ORDER BY t.when_date ASC, t.sort_order_today ASC`,
		append([]any{today, today, today}, earlierArgs...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = earlierRows.Close() }()
	earlierTasks := scanTodayTaskListItems(r.db, earlierRows)
	populatePastScheduleCounts(r.db, earlierTasks, today)
	populateActionableScheduleFlags(r.db, earlierTasks, r.today())

	return &model.UpcomingView{Overdue: overdueTasks, Dates: dates, Earlier: earlierTasks}, nil
}

func (r *ViewRepository) Anytime() (*model.AnytimeView, error) {
	return r.buildAnytimeView(false)
}

func (r *ViewRepository) Someday() (*model.AnytimeView, error) {
	return r.buildAnytimeView(true)
}

func (r *ViewRepository) buildAnytimeView(somedayOnly bool) (*model.AnytimeView, error) {
	// Anytime: open tasks with no when_date (not scheduled for a specific day, not someday).
	// Someday: open tasks explicitly marked when_date = 'someday'.

	// Get all areas
	areaRows, err := r.db.Query("SELECT id, title FROM areas ORDER BY sort_order")
	if err != nil {
		return nil, err
	}
	defer func() { _ = areaRows.Close() }()

	var view model.AnytimeView
	for areaRows.Next() {
		var areaRef model.Ref
		_ = areaRows.Scan(&areaRef.ID, &areaRef.Title)

		aa := model.AnytimeArea{Area: areaRef}

		// Projects in area
		projRows, err := r.db.Query(
			"SELECT id, title FROM projects WHERE area_id = ? AND status = 'open' ORDER BY sort_order", areaRef.ID)
		if err != nil {
			continue
		}
		for projRows.Next() {
			var projRef model.Ref
			_ = projRows.Scan(&projRef.ID, &projRef.Title)
			tasks := r.getAnytimeTasks(&projRef.ID, &areaRef.ID, true, somedayOnly)
			if len(tasks) == 0 {
				continue
			}
			aa.Projects = append(aa.Projects, model.AnytimeProject{Project: projRef, Tasks: tasks})
		}
		_ = projRows.Close()
		if aa.Projects == nil {
			aa.Projects = []model.AnytimeProject{}
		}

		// Standalone tasks in area
		aa.StandaloneTasks = r.getAnytimeTasks(nil, &areaRef.ID, false, somedayOnly)

		if len(aa.Projects) == 0 && len(aa.StandaloneTasks) == 0 {
			continue
		}
		view.Areas = append(view.Areas, aa)
	}
	if view.Areas == nil {
		view.Areas = []model.AnytimeArea{}
	}

	// No-area projects
	noAreaProjRows, err := r.db.Query(
		"SELECT id, title FROM projects WHERE area_id IS NULL AND status = 'open' ORDER BY sort_order")
	if err == nil {
		for noAreaProjRows.Next() {
			var projRef model.Ref
			_ = noAreaProjRows.Scan(&projRef.ID, &projRef.Title)
			tasks := r.getAnytimeTasks(&projRef.ID, nil, true, somedayOnly)
			if len(tasks) == 0 {
				continue
			}
			view.NoArea.Projects = append(view.NoArea.Projects, model.AnytimeProject{Project: projRef, Tasks: tasks})
		}
		_ = noAreaProjRows.Close()
	}
	if view.NoArea.Projects == nil {
		view.NoArea.Projects = []model.AnytimeProject{}
	}

	// Standalone tasks with no area, no project
	view.NoArea.StandaloneTasks = r.getAnytimeStandaloneNoArea(somedayOnly)

	return &view, nil
}

func (r *ViewRepository) getAnytimeTasks(projectID, areaID *string, byProject, somedayOnly bool) []model.TaskListItem {
	list := "anytime"
	if somedayOnly {
		list = "someday"
	}
	cond, args, err := listCondition(list, r.today(), "")
	if err != nil {
		return []model.TaskListItem{}
	}

	query := `
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t WHERE ` + cond

	if byProject && projectID != nil {
		query += " AND t.project_id = ?"
		args = append(args, *projectID)
	} else if !byProject {
		query += " AND t.project_id IS NULL"
		if areaID != nil {
			query += " AND t.area_id = ?"
			args = append(args, *areaID)
		}
	}

	query += " ORDER BY t.sort_order_today ASC"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return []model.TaskListItem{}
	}
	defer func() { _ = rows.Close() }()
	tasks := scanTaskListItems(r.db, rows)
	populateActionableScheduleFlags(r.db, tasks, r.today())
	return tasks
}

// getAnytimeStandaloneNoArea lists the view's no-area standalone tasks.
// For anytime the shared predicate and the standalone context contradict
// each other — a loose task belongs to the Inbox (E3) — so the result is
// always empty; someday keeps loose tasks.
func (r *ViewRepository) getAnytimeStandaloneNoArea(somedayOnly bool) []model.TaskListItem {
	list := "anytime"
	if somedayOnly {
		list = "someday"
	}
	cond, args, err := listCondition(list, r.today(), "")
	if err != nil {
		return []model.TaskListItem{}
	}
	query := `
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + cond + ` AND t.project_id IS NULL AND t.area_id IS NULL
		ORDER BY t.sort_order_today ASC`

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return []model.TaskListItem{}
	}
	defer func() { _ = rows.Close() }()
	tasks := scanTaskListItems(r.db, rows)
	populateActionableScheduleFlags(r.db, tasks, r.today())
	return tasks
}

func (r *ViewRepository) Logbook(limit, offset int) (*model.LogbookView, error) {
	if limit <= 0 {
		limit = 50
	}

	logbookCond, logbookArgs, err := listCondition("logbook", r.today(), "")
	if err != nil {
		return nil, err
	}

	var total int
	_ = r.db.QueryRow("SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE " + logbookCond,
		logbookArgs...).Scan(&total)

	rows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + logbookCond + `
		ORDER BY COALESCE(t.completed_at, t.canceled_at, t.updated_at) DESC
		LIMIT ? OFFSET ?`, append(append([]any{}, logbookArgs...), limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	tasks := scanTaskListItems(r.db, rows)

	// Group by completion date in the server timezone
	dateMap := make(map[string][]model.TaskListItem)
	var dateOrder []string
	for _, t := range tasks {
		d := ""
		if t.CompletedAt != nil {
			d = utcToLocalDay(*t.CompletedAt, r.loc)
		} else if t.CanceledAt != nil {
			d = utcToLocalDay(*t.CanceledAt, r.loc)
		} else {
			d = utcToLocalDay(t.UpdatedAt, r.loc)
		}
		if _, ok := dateMap[d]; !ok {
			dateOrder = append(dateOrder, d)
		}
		dateMap[d] = append(dateMap[d], t)
	}

	var groups []model.DateGroup
	for _, d := range dateOrder {
		groups = append(groups, model.DateGroup{Date: d, Tasks: dateMap[d]})
	}
	if groups == nil {
		groups = []model.DateGroup{}
	}

	return &model.LogbookView{Groups: groups, Total: total}, nil
}

func (r *ViewRepository) Trash(limit, offset int) (*model.LogbookView, error) {
	if limit <= 0 {
		limit = 50
	}

	trashCond, trashArgs, err := listCondition("trash", r.today(), "")
	if err != nil {
		return nil, err
	}

	var total int
	_ = r.db.QueryRow("SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE "+trashCond,
		trashArgs...).Scan(&total)

	rows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE ` + trashCond + `
		ORDER BY t.deleted_at DESC
		LIMIT ? OFFSET ?`, append(append([]any{}, trashArgs...), limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	tasks := scanTaskListItems(r.db, rows)

	// Group by deletion date in the server timezone
	dateMap := make(map[string][]model.TaskListItem)
	var dateOrder []string
	for _, t := range tasks {
		d := utcToLocalDay(t.UpdatedAt, r.loc)
		if t.DeletedAt != nil {
			d = utcToLocalDay(*t.DeletedAt, r.loc)
		}
		if _, ok := dateMap[d]; !ok {
			dateOrder = append(dateOrder, d)
		}
		dateMap[d] = append(dateMap[d], t)
	}

	var groups []model.DateGroup
	for _, d := range dateOrder {
		groups = append(groups, model.DateGroup{Date: d, Tasks: dateMap[d]})
	}
	if groups == nil {
		groups = []model.DateGroup{}
	}

	return &model.LogbookView{Groups: groups, Total: total}, nil
}

type upcomingItem struct {
	task model.TaskListItem
	date string // the schedule_date for grouping
}

func scanUpcomingTaskListItems(db *sql.DB, rows *sql.Rows) []upcomingItem {
	var items []upcomingItem
	for rows.Next() {
		var t model.TaskListItem
		var whenEvening, highPriority, hasNotes, hasLinks, hasFiles, hasRepeat, hasReminders int
		var scheduleDate string
		_ = rows.Scan(
			&t.ID, &t.Title, &t.Notes, &t.Status, &t.WhenDate, &whenEvening, &highPriority,
			&t.Deadline, &t.ProjectID, &t.AreaID, &t.HeadingID,
			&t.SortOrderToday, &t.SortOrderProject, &t.SortOrderHeading,
			&t.CompletedAt, &t.CanceledAt, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt,
			&t.ChecklistCount, &t.ChecklistDone,
			&hasNotes, &hasLinks, &hasFiles, &hasRepeat, &hasReminders,
			&t.FirstReminderType, &t.FirstReminderValue, &t.FirstReminderExactAt,
			&t.FirstScheduleTime, &t.FirstScheduleEndTime,
			&t.ScheduleEntryID,
			&scheduleDate,
		)
		_ = whenEvening // column retained in DB but no longer exposed
		t.HighPriority = highPriority == 1
		t.HasNotes = hasNotes == 1
		t.HasLinks = hasLinks == 1
		t.HasFiles = hasFiles == 1
		t.HasRepeatRule = hasRepeat == 1
		t.HasReminders = hasReminders == 1
		// Get tags
		tagRows, _ := db.Query(
			"SELECT t.id, t.title, t.color FROM tags t JOIN task_tags tt ON t.id = tt.tag_id WHERE tt.task_id = ?", t.ID)
		if tagRows != nil {
			var tags []model.TagRef
			for tagRows.Next() {
				var tag model.TagRef
				_ = tagRows.Scan(&tag.ID, &tag.Title, &tag.Color)
				tags = append(tags, tag)
			}
			_ = tagRows.Close()
			if tags == nil {
				tags = []model.TagRef{}
			}
			t.Tags = tags
		} else {
			t.Tags = []model.TagRef{}
		}
		// Resolve project/area names
		if t.ProjectID != nil {
			var name string
			if err := db.QueryRow("SELECT title FROM projects WHERE id = ?", *t.ProjectID).Scan(&name); err == nil {
				t.ProjectName = &name
			}
		}
		if t.AreaID != nil {
			var name string
			if err := db.QueryRow("SELECT title FROM areas WHERE id = ?", *t.AreaID).Scan(&name); err == nil {
				t.AreaName = &name
			}
		}
		items = append(items, upcomingItem{task: t, date: scheduleDate})
	}
	return items
}

// scanTodayTaskListItems scans rows that include ts.start_time, ts.end_time,
// and ts.id (schedule_entry_id) from a LEFT JOIN on task_schedules.
// Unlike scanUpcomingTaskListItems, there is no extra schedule_date grouping column.
func scanTodayTaskListItems(db *sql.DB, rows *sql.Rows) []model.TaskListItem {
	var tasks []model.TaskListItem
	for rows.Next() {
		var t model.TaskListItem
		var whenEvening, highPriority, hasNotes, hasLinks, hasFiles, hasRepeat, hasReminders int
		_ = rows.Scan(
			&t.ID, &t.Title, &t.Notes, &t.Status, &t.WhenDate, &whenEvening, &highPriority,
			&t.Deadline, &t.ProjectID, &t.AreaID, &t.HeadingID,
			&t.SortOrderToday, &t.SortOrderProject, &t.SortOrderHeading,
			&t.CompletedAt, &t.CanceledAt, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt,
			&t.ChecklistCount, &t.ChecklistDone,
			&hasNotes, &hasLinks, &hasFiles, &hasRepeat, &hasReminders,
			&t.FirstReminderType, &t.FirstReminderValue, &t.FirstReminderExactAt,
			&t.FirstScheduleTime, &t.FirstScheduleEndTime,
			&t.ScheduleEntryID,
		)
		_ = whenEvening // column retained in DB but no longer exposed
		t.HighPriority = highPriority == 1
		t.HasNotes = hasNotes == 1
		t.HasLinks = hasLinks == 1
		t.HasFiles = hasFiles == 1
		t.HasRepeatRule = hasRepeat == 1
		t.HasReminders = hasReminders == 1
		// Get tags
		tagRows, _ := db.Query(
			"SELECT t.id, t.title, t.color FROM tags t JOIN task_tags tt ON t.id = tt.tag_id WHERE tt.task_id = ?", t.ID)
		if tagRows != nil {
			var tags []model.TagRef
			for tagRows.Next() {
				var tag model.TagRef
				_ = tagRows.Scan(&tag.ID, &tag.Title, &tag.Color)
				tags = append(tags, tag)
			}
			_ = tagRows.Close()
			if tags == nil {
				tags = []model.TagRef{}
			}
			t.Tags = tags
		} else {
			t.Tags = []model.TagRef{}
		}
		// Resolve project/area names
		if t.ProjectID != nil {
			var name string
			if err := db.QueryRow("SELECT title FROM projects WHERE id = ?", *t.ProjectID).Scan(&name); err == nil {
				t.ProjectName = &name
			}
		}
		if t.AreaID != nil {
			var name string
			if err := db.QueryRow("SELECT title FROM areas WHERE id = ?", *t.AreaID).Scan(&name); err == nil {
				t.AreaName = &name
			}
		}
		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []model.TaskListItem{}
	}
	return tasks
}

func groupByProject(db *sql.DB, tasks []model.TaskListItem) []model.TaskGroup {
	if len(tasks) == 0 {
		return []model.TaskGroup{}
	}

	projectMap := make(map[string][]model.TaskListItem)
	var projectOrder []string

	for _, t := range tasks {
		key := ""
		if t.ProjectID != nil {
			key = *t.ProjectID
		}
		if _, ok := projectMap[key]; !ok {
			projectOrder = append(projectOrder, key)
		}
		projectMap[key] = append(projectMap[key], t)
	}

	var groups []model.TaskGroup
	for _, key := range projectOrder {
		g := model.TaskGroup{Tasks: projectMap[key]}
		if key != "" {
			g.Project = getRef(db, "projects", key)
		}
		groups = append(groups, g)
	}
	return groups
}

// Counts returns one COUNT(DISTINCT t.id) per view, each built from the
// same listCondition predicate as the view itself (C2), so a count always
// equals the distinct task ids of its view — including today, which spans
// the Today, This Evening, Overdue and Earlier sections.
func (r *ViewRepository) Counts(reviewAfterDays *int, includeRecurring bool) (*model.ViewCounts, error) {
	today := r.today()
	var c model.ViewCounts

	inboxCond, inboxArgs, err := listCondition("inbox", today, "")
	if err != nil {
		return nil, err
	}
	todayCond, todayArgs, err := listCondition("today", today, "")
	if err != nil {
		return nil, err
	}
	overdueCondSQL, overdueArgs := overdueCond(today)
	anytimeCond, anytimeArgs, err := listCondition("anytime", today, "")
	if err != nil {
		return nil, err
	}
	somedayCond, somedayArgs, err := listCondition("someday", today, "")
	if err != nil {
		return nil, err
	}
	logbookCond, logbookArgs, err := listCondition("logbook", today, "")
	if err != nil {
		return nil, err
	}
	trashCond, trashArgs, err := listCondition("trash", today, "")
	if err != nil {
		return nil, err
	}

	args := make([]any, 0, 7)
	args = append(args, inboxArgs...)
	args = append(args, todayArgs...)
	args = append(args, overdueArgs...)
	args = append(args, anytimeArgs...)
	args = append(args, somedayArgs...)
	args = append(args, logbookArgs...)
	args = append(args, trashArgs...)

	err = r.db.QueryRow(`
		SELECT
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+inboxCond+`),
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+todayCond+`),
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+overdueCondSQL+`),
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+anytimeCond+`),
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+somedayCond+`),
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+logbookCond+`),
			(SELECT COUNT(DISTINCT t.id) FROM tasks t WHERE `+trashCond+`)
	`, args...).Scan(&c.Inbox, &c.Today, &c.Overdue, &c.Anytime, &c.Someday, &c.Logbook, &c.Trash)
	if err != nil {
		return nil, err
	}

	// Review count: all open non-deleted tasks stale by X days, excluding inbox tasks
	if reviewAfterDays != nil && *reviewAfterDays > 0 {
		recurringClause := ""
		if !includeRecurring {
			recurringClause = " AND NOT EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = tasks.id)"
		}
		_ = r.db.QueryRow(`
			SELECT COUNT(*) FROM tasks
			WHERE status = 'open' AND deleted_at IS NULL
				AND date(updated_at) < ?
				AND NOT (project_id IS NULL AND area_id IS NULL AND when_date IS NULL)
		`+recurringClause, r.reviewCutoff(*reviewAfterDays)).Scan(&c.Review)
	}

	return &c, nil
}

// helper for parsing int from query params
func ParseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// populateActionableScheduleFlags sets HasActionableSchedules on each task
// that has uncompleted schedule entries requiring confirmation — i.e. past
// entries (will be auto-completed) or future/someday entries (will be deleted).
// If the only uncompleted entries are for today, no flag is set because
// completing today's entry is the normal expected behavior.
func populateActionableScheduleFlags(db DBTX, tasks []model.TaskListItem, today string) {
	if len(tasks) == 0 {
		return
	}
	for i, t := range tasks {
		var count int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM task_schedules WHERE task_id = ? AND completed = 0 AND (when_date != ? OR when_date = 'someday')",
			t.ID, today,
		).Scan(&count)
		if err == nil && count > 0 {
			tasks[i].HasActionableSchedules = true
		}
	}
	populateAllSchedulesCompleted(db, tasks)
	populateFirstScheduleCompleted(db, tasks)
}

// populateAllSchedulesCompleted sets AllTodaySchedulesCompleted on each
// task that has schedule entries and ALL of them are completed.
func populateAllSchedulesCompleted(db DBTX, tasks []model.TaskListItem) {
	if len(tasks) == 0 {
		return
	}
	for i, t := range tasks {
		var total, completed int
		err := db.QueryRow(
			"SELECT COUNT(*), SUM(CASE WHEN completed = 1 THEN 1 ELSE 0 END) FROM task_schedules WHERE task_id = ?",
			t.ID,
		).Scan(&total, &completed)
		if err == nil && total > 0 && completed == total {
			tasks[i].AllTodaySchedulesCompleted = true
		}
	}
}

// populateFirstScheduleCompleted sets FirstScheduleCompleted on each task
// whose displayed schedule entry is completed. Uses ScheduleEntryID if present
// (Today view), otherwise checks the first schedule by sort_order.
func populateFirstScheduleCompleted(db DBTX, tasks []model.TaskListItem) {
	if len(tasks) == 0 {
		return
	}
	for i, t := range tasks {
		if t.FirstScheduleTime == nil {
			continue
		}
		var completed int
		var err error
		if t.ScheduleEntryID != nil {
			err = db.QueryRow(
				"SELECT completed FROM task_schedules WHERE id = ?",
				*t.ScheduleEntryID,
			).Scan(&completed)
		} else {
			err = db.QueryRow(
				"SELECT completed FROM task_schedules WHERE task_id = ? ORDER BY sort_order ASC LIMIT 1",
				t.ID,
			).Scan(&completed)
		}
		if err == nil && completed == 1 {
			tasks[i].FirstScheduleCompleted = true
		}
	}
}

// populatePastScheduleCounts sets PastScheduleCount on each task that has
// schedule entries with when_date before the given date.
func populatePastScheduleCounts(db *sql.DB, tasks []model.TaskListItem, before string) {
	if len(tasks) == 0 {
		return
	}
	for i, t := range tasks {
		var count int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM task_schedules WHERE task_id = ? AND when_date < ? AND when_date != 'someday' AND completed = 0`,
			t.ID, before,
		).Scan(&count)
		if err == nil {
			tasks[i].PastScheduleCount = count
		}
	}
}
