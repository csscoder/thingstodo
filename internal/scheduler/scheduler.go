package scheduler

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/push"
	"github.com/collinjanssen/thingstodo/internal/recurrence"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron          *cron.Cron
	now           func() time.Time
	taskRepo      *repository.TaskRepository
	ruleRepo      *repository.RepeatRuleRepository
	checklistRepo *repository.ChecklistRepository
	attachRepo    *repository.AttachmentRepository
	scheduleRepo  *repository.ScheduleRepository
	reminderRepo  *repository.ReminderRepository
	settingsRepo  *repository.UserSettingsRepository
	userRepo      *repository.UserRepository
	changeLogRepo *repository.ChangeLogRepository
	pushSender    push.Notifier
	broker        *sse.Broker
	db            *sql.DB
	engine        *recurrence.Engine
	loc           *time.Location

	// testHookAfterInstance runs inside the finishing transaction after the
	// next repeat instance is created. Tests use it to force a rollback.
	testHookAfterInstance func() error
}

func New(db *sql.DB, taskRepo *repository.TaskRepository, ruleRepo *repository.RepeatRuleRepository, checklistRepo *repository.ChecklistRepository, attachRepo *repository.AttachmentRepository, scheduleRepo *repository.ScheduleRepository, reminderRepo *repository.ReminderRepository, settingsRepo *repository.UserSettingsRepository, userRepo *repository.UserRepository, changeLogRepo *repository.ChangeLogRepository, pushSender push.Notifier, broker *sse.Broker, loc *time.Location) *Scheduler {
	return &Scheduler{
		cron:          cron.New(),
		now:           time.Now,
		taskRepo:      taskRepo,
		ruleRepo:      ruleRepo,
		checklistRepo: checklistRepo,
		attachRepo:    attachRepo,
		scheduleRepo:  scheduleRepo,
		reminderRepo:  reminderRepo,
		settingsRepo:  settingsRepo,
		userRepo:      userRepo,
		changeLogRepo: changeLogRepo,
		pushSender:    pushSender,
		broker:        broker,
		db:            db,
		engine:        recurrence.NewEngine(),
		loc:           loc,
	}
}

func (s *Scheduler) Start() {
	if _, err := s.cron.AddFunc("@every 1h", s.processFixedRules); err != nil {
		log.Printf("scheduler: failed to add recurrence cron: %v", err)
	}
	if _, err := s.cron.AddFunc("@every 1m", s.processReminders); err != nil {
		log.Printf("scheduler: failed to add reminder cron: %v", err)
	}
	if _, err := s.cron.AddFunc("@daily", s.purgeChangeLog); err != nil {
		log.Printf("scheduler: failed to add change log purge cron: %v", err)
	}
	s.cron.Start()
	log.Println("scheduler started")
}

func (s *Scheduler) purgeChangeLog() {
	purged, err := s.changeLogRepo.PurgeOlderThan(90)
	if err != nil {
		log.Printf("change log purge error: %v", err)
		return
	}
	if purged > 0 {
		log.Printf("purged %d old change log entries", purged)
	}
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// SetClock overrides the scheduler's clock (default time.Now). "Today" for
// schedule cleanup and recurrence base dates is resolved as
// s.now().In(s.loc).
func (s *Scheduler) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// FinishTask is the one finishing path for a task. In a single transaction it
// guards the status change (only an open task can be finished), cleans up
// schedule entries, and, when a repeat rule exists, creates the next instance
// and moves the rule to it. A repeated or concurrent finish can therefore
// never spawn a second instance.
//
// It returns (nil, false, nil) when the task does not exist, and
// (task, false, nil) when the task was already finished — callers map the
// former to 404 and treat the latter as an idempotent success.
func (s *Scheduler) FinishTask(taskID, status string) (task *model.TaskDetail, changed bool, err error) {
	// Read the reminder-copy setting before opening the transaction:
	// UserSettingsRepository.GetOrCreate may write on its own connection and
	// contend with the transaction below.
	copyReminders := s.reminderCopyEnabled()

	txErr := repository.InTx(s.db, func(tx *sql.Tx) error {
		txTaskRepo := s.taskRepo.WithTx(tx)
		c, err := txTaskRepo.SetDoneStatus(taskID, status)
		if err != nil {
			return err
		}
		changed = c

		existing, err := txTaskRepo.GetByID(taskID)
		if err != nil {
			return err
		}
		if existing == nil {
			return nil // unknown task: (nil, false, nil)
		}
		task = existing
		if !changed {
			return nil // already finished: idempotent success, spawn nothing
		}

		today := s.now().In(s.loc).Format("2006-01-02")
		if err := s.scheduleRepo.WithTx(tx).CleanupOnTaskDone(taskID, today); err != nil {
			return fmt.Errorf("cleanup schedules for task %s: %w", taskID, err)
		}

		rule, err := s.ruleRepo.WithTx(tx).GetByTask(taskID)
		if err != nil {
			return fmt.Errorf("load repeat rule for task %s: %w", taskID, err)
		}
		if rule != nil {
			if err := s.createNextInstance(tx, taskID, rule.Pattern, today, copyReminders); err != nil {
				return err
			}
		}

		// Re-read so the response reflects everything the transaction did.
		task, err = txTaskRepo.GetByID(taskID)
		return err
	})
	if txErr != nil {
		return nil, false, txErr
	}
	return task, changed, nil
}

func (s *Scheduler) processFixedRules() {
	rules, err := s.ruleRepo.ListAll()
	if err != nil {
		log.Printf("scheduler: list rules: %v", err)
		return
	}

	today := s.now().In(s.loc).Format("2006-01-02")
	copyReminders := s.reminderCopyEnabled()

	for _, rule := range rules {
		if rule.Pattern.Mode != model.RecurrenceModeFixed {
			continue
		}
		task, err := s.taskRepo.GetByID(rule.TaskID)
		if err != nil || task == nil {
			continue
		}
		// Only process if the task is completed
		if task.Status != "completed" {
			continue
		}

		nextDate, err := s.nextInstanceDate(task, rule.Pattern, today)
		if err != nil {
			log.Printf("scheduler: next date for task %s: %v", rule.TaskID, err)
			continue
		}
		if nextDate == "" || nextDate > today {
			continue
		}

		err = repository.InTx(s.db, func(tx *sql.Tx) error {
			return s.createNextInstance(tx, rule.TaskID, rule.Pattern, today, copyReminders)
		})
		if err != nil {
			log.Printf("scheduler: create next instance for task %s: %v", rule.TaskID, err)
		}
	}
}

// nextInstanceDate computes the when_date for the next instance:
//   - after_completion: the next occurrence after the local completion date;
//   - fixed: the next occurrence after the task's when_date, rolled forward
//     while it lands before the local today, so a fixed schedule never
//     spawns a past instance. Without a usable when_date, today is the base.
func (s *Scheduler) nextInstanceDate(original *model.TaskDetail, pattern model.RecurrencePattern, today string) (string, error) {
	if pattern.Mode != model.RecurrenceModeFixed {
		next, err := s.engine.Next(today, pattern)
		if err != nil {
			return "", fmt.Errorf("next occurrence from %s: %w", today, err)
		}
		return next, nil
	}

	from := today
	if original.WhenDate != nil && *original.WhenDate != "" && *original.WhenDate != "someday" {
		from = *original.WhenDate
	}
	next, err := s.engine.Next(from, pattern)
	if err != nil {
		return "", fmt.Errorf("next occurrence from %s: %w", from, err)
	}
	for next < today {
		step, err := s.engine.Next(next, pattern)
		if err != nil {
			return "", fmt.Errorf("roll forward from %s: %w", next, err)
		}
		if step <= next {
			return "", fmt.Errorf("recurrence pattern %s does not advance from %s", pattern.Type, next)
		}
		next = step
	}
	return next, nil
}

// createNextInstance creates the next repeat instance of the task and moves
// the repeat rule to it, using tx-bound repositories so every write joins the
// caller's transaction. Every error is returned — the caller rolls back.
func (s *Scheduler) createNextInstance(tx *sql.Tx, originalTaskID string, pattern model.RecurrencePattern, today string, copyReminders bool) error {
	txTaskRepo := s.taskRepo.WithTx(tx)
	original, err := txTaskRepo.GetByID(originalTaskID)
	if err != nil {
		return fmt.Errorf("load original task %s: %w", originalTaskID, err)
	}
	if original == nil {
		return nil
	}

	nextDate, err := s.nextInstanceDate(original, pattern, today)
	if err != nil {
		return err
	}

	// Collect tag IDs from original
	tagIDs := make([]string, len(original.Tags))
	for i, t := range original.Tags {
		tagIDs[i] = t.ID
	}

	input := model.CreateTaskInput{
		Title:     original.Title,
		Notes:     original.Notes,
		WhenDate:  &nextDate,
		Deadline:  nil,
		ProjectID: original.ProjectID,
		AreaID:    original.AreaID,
		HeadingID: original.HeadingID,
		TagIDs:    tagIDs,
	}

	newTask, err := txTaskRepo.Create(input)
	if err != nil {
		return fmt.Errorf("create task instance: %w", err)
	}

	// Note: first schedule entry is created by taskRepo.Create via syncFirstScheduleDate

	// Copy checklist items (unchecked)
	txChecklistRepo := s.checklistRepo.WithTx(tx)
	for _, item := range original.Checklist {
		if _, err := txChecklistRepo.Create(newTask.ID, model.CreateChecklistInput{
			Title: item.Title,
		}); err != nil {
			return fmt.Errorf("copy checklist item for task %s: %w", newTask.ID, err)
		}
	}

	// Copy attachments (links and files — files share the same stored file on disk)
	txAttachRepo := s.attachRepo.WithTx(tx)
	for _, att := range original.Attachments {
		if _, err := txAttachRepo.Create(newTask.ID, model.CreateAttachmentInput{
			Type:     att.Type,
			Title:    att.Title,
			URL:      att.URL,
			MimeType: att.MimeType,
			FileSize: att.FileSize,
		}); err != nil {
			return fmt.Errorf("copy attachment for task %s: %w", newTask.ID, err)
		}
	}

	// Copy reminders if setting is enabled
	txReminderRepo := s.reminderRepo.WithTx(tx)
	if err := s.copyRemindersToNewInstance(txReminderRepo, copyReminders, originalTaskID, newTask.ID); err != nil {
		return fmt.Errorf("copy reminders for task %s: %w", newTask.ID, err)
	}

	// Move repeat rule to new task (pass through the pattern)
	txRuleRepo := s.ruleRepo.WithTx(tx)
	if err := txRuleRepo.DeleteByTask(originalTaskID); err != nil {
		return fmt.Errorf("delete rule for task %s: %w", originalTaskID, err)
	}
	if _, err := txRuleRepo.Upsert(newTask.ID, model.CreateRepeatRuleInput{
		Pattern: &pattern,
	}); err != nil {
		return fmt.Errorf("upsert rule for task %s: %w", newTask.ID, err)
	}

	if s.testHookAfterInstance != nil {
		if err := s.testHookAfterInstance(); err != nil {
			return fmt.Errorf("test hook after instance: %w", err)
		}
	}

	log.Printf("scheduler: created repeat instance %s from %s (next: %s)", newTask.ID, originalTaskID, nextDate)
	return nil
}

func (s *Scheduler) processReminders() {
	now := time.Now().In(s.loc)
	windowStart := now.Add(-1 * time.Minute)
	windowEnd := now.Add(1 * time.Minute)

	// Get morning time from first user's settings (single-user app)
	morningTime := "09:00"
	if user, err := s.userRepo.GetFirst(); err == nil && user != nil {
		if settings, err := s.settingsRepo.GetOrCreate(user.ID); err == nil {
			_ = settings
		}
	}

	// Process relative reminders (at_start, on_day, minutes_before, hours_before, days_before)
	pending, err := s.reminderRepo.GetPendingRelative()
	if err != nil {
		log.Printf("scheduler: get pending relative reminders: %v", err)
	}
	for _, p := range pending {
		fireAt := computeFireAt(p, morningTime, s.loc)
		if fireAt.IsZero() {
			continue
		}
		if fireAt.Before(windowStart) || !fireAt.Before(windowEnd) {
			continue
		}
		s.fireReminder(p, fireAt)
	}

	// Process exact reminders
	exact, err := s.reminderRepo.GetPendingExact()
	if err != nil {
		log.Printf("scheduler: get pending exact reminders: %v", err)
	}
	for _, p := range exact {
		if p.Reminder.ExactAt == nil {
			continue
		}
		var fireAt time.Time
		for _, layout := range []string{
			"2006-01-02T15:04:05",
			"2006-01-02T15:04",
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
		} {
			fireAt, err = time.ParseInLocation(layout, *p.Reminder.ExactAt, s.loc)
			if err == nil {
				break
			}
		}
		if err != nil {
			log.Printf("scheduler: exact reminder %s: failed to parse exact_at=%q: %v", p.Reminder.ID, *p.Reminder.ExactAt, err)
			continue
		}
		if fireAt.Before(windowStart) || !fireAt.Before(windowEnd) {
			continue
		}
		s.fireReminder(p, fireAt)
	}
}

func computeFireAt(p model.PendingReminder, morningTime string, loc *time.Location) time.Time {
	morningHour, morningMin := 9, 0
	if parts := splitTime(morningTime); parts != nil {
		morningHour, morningMin = parts[0], parts[1]
	}

	switch p.Reminder.Type {
	case model.ReminderAtStart:
		return parseAnchor(p.WhenDate, p.StartTime, morningHour, morningMin, loc)

	case model.ReminderOnDay:
		if p.WhenDate == "" {
			return time.Time{}
		}
		d, err := time.ParseInLocation("2006-01-02", p.WhenDate, loc)
		if err != nil {
			return time.Time{}
		}
		return d.Add(time.Duration(morningHour)*time.Hour + time.Duration(morningMin)*time.Minute)

	case model.ReminderMinutesBefore:
		anchor := parseAnchor(p.WhenDate, p.StartTime, morningHour, morningMin, loc)
		if anchor.IsZero() {
			return time.Time{}
		}
		return anchor.Add(-time.Duration(p.Reminder.Value) * time.Minute)

	case model.ReminderHoursBefore:
		anchor := parseAnchor(p.WhenDate, p.StartTime, morningHour, morningMin, loc)
		if anchor.IsZero() {
			return time.Time{}
		}
		return anchor.Add(-time.Duration(p.Reminder.Value) * time.Hour)

	case model.ReminderDaysBefore:
		if p.WhenDate == "" {
			return time.Time{}
		}
		d, err := time.ParseInLocation("2006-01-02", p.WhenDate, loc)
		if err != nil {
			return time.Time{}
		}
		d = d.AddDate(0, 0, -p.Reminder.Value)
		return d.Add(time.Duration(morningHour)*time.Hour + time.Duration(morningMin)*time.Minute)
	}
	return time.Time{}
}

// parseAnchor returns the anchor time for time-relative reminders.
// If start_time exists, use when_date + start_time. Otherwise fall back to when_date + morning.
func parseAnchor(whenDate string, startTime *string, morningH, morningM int, loc *time.Location) time.Time {
	if whenDate == "" || whenDate == "someday" {
		return time.Time{}
	}
	d, err := time.ParseInLocation("2006-01-02", whenDate, loc)
	if err != nil {
		return time.Time{}
	}
	if startTime != nil && *startTime != "" {
		parts := splitTime(*startTime)
		if parts != nil {
			return d.Add(time.Duration(parts[0])*time.Hour + time.Duration(parts[1])*time.Minute)
		}
	}
	return d.Add(time.Duration(morningH)*time.Hour + time.Duration(morningM)*time.Minute)
}

// splitTime parses "HH:MM" into [hour, minute].
func splitTime(s string) []int {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return nil
	}
	return []int{h, m}
}

func (s *Scheduler) fireReminder(p model.PendingReminder, fireAt time.Time) {
	fireAtStr := fireAt.Format("2006-01-02T15:04:05")

	// Check dedup log
	if sent, _ := s.reminderRepo.HasBeenSent(p.Reminder.ID, p.ScheduleID, fireAtStr); sent {
		return
	}
	if err := s.reminderRepo.MarkSent(p.Reminder.ID, p.ScheduleID, fireAtStr); err != nil {
		return
	}

	log.Printf("scheduler: firing reminder %s for task %s (%s)", p.Reminder.ID, p.TaskID, p.TaskTitle)

	// Describe the reminder for notification text
	body := describeReminder(p.Reminder)

	// Web Push
	if s.pushSender != nil && s.pushSender.Enabled() {
		if user, err := s.userRepo.GetFirst(); err == nil && user != nil {
			if err := s.pushSender.Send(user.ID, push.Payload{
				Title: p.TaskTitle,
				Body:  body,
				URL:   fmt.Sprintf("/tasks/%s", p.TaskID),
				Tag:   p.Reminder.ID,
			}); err != nil {
				log.Printf("scheduler: push send error: %v", err)
			}
		}
	}

	// SSE broadcast for in-app toast
	s.broker.BroadcastJSON("reminder_fired", map[string]interface{}{
		"task_id":        p.TaskID,
		"task_title":     p.TaskTitle,
		"reminder_type":  string(p.Reminder.Type),
		"reminder_value": p.Reminder.Value,
	})
}

func describeReminder(r model.Reminder) string {
	switch r.Type {
	case model.ReminderAtStart:
		return "Starting now"
	case model.ReminderOnDay:
		return "Scheduled for today"
	case model.ReminderMinutesBefore:
		return fmt.Sprintf("Starting in %d minutes", r.Value)
	case model.ReminderHoursBefore:
		if r.Value == 1 {
			return "Starting in 1 hour"
		}
		return fmt.Sprintf("Starting in %d hours", r.Value)
	case model.ReminderDaysBefore:
		if r.Value == 1 {
			return "Scheduled for tomorrow"
		}
		return fmt.Sprintf("Scheduled in %d days", r.Value)
	case model.ReminderExact:
		return "Reminder"
	}
	return "Reminder"
}

// reminderCopyEnabled reads the user setting outside any transaction, so
// GetOrCreate never writes on a second connection while a transaction holds
// the write lock.
func (s *Scheduler) reminderCopyEnabled() bool {
	if user, err := s.userRepo.GetFirst(); err == nil && user != nil {
		if settings, err := s.settingsRepo.GetOrCreate(user.ID); err == nil && !settings.CopyRemindersToRecurring {
			return false
		}
	}
	return true
}

// copyRemindersToNewInstance copies relative reminders to the new instance
// when the user enabled it, using the tx-bound reminderRepo so the copies
// join the caller's transaction.
func (s *Scheduler) copyRemindersToNewInstance(reminderRepo *repository.ReminderRepository, copyEnabled bool, originalTaskID, newTaskID string) error {
	if !copyEnabled {
		return nil
	}

	reminders, err := reminderRepo.ListByTask(originalTaskID)
	if err != nil {
		return fmt.Errorf("list reminders of task %s: %w", originalTaskID, err)
	}

	for _, r := range reminders {
		if r.Type == model.ReminderExact {
			// Skip exact reminders — they are tied to specific dates
			continue
		}
		if _, err := reminderRepo.Create(newTaskID, model.CreateReminderInput{
			Type:  r.Type,
			Value: r.Value,
		}); err != nil {
			return fmt.Errorf("copy reminder for task %s: %w", newTaskID, err)
		}
	}
	return nil
}
