package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/go-chi/chi/v5"
)

type TaskHandler struct {
	repo         *repository.TaskRepository
	reminderRepo *repository.ReminderRepository
	settingsRepo *repository.UserSettingsRepository
	broker       *sse.Broker
	scheduler    *scheduler.Scheduler
}

func NewTaskHandler(repo *repository.TaskRepository, reminderRepo *repository.ReminderRepository, settingsRepo *repository.UserSettingsRepository, broker *sse.Broker, sched *scheduler.Scheduler) *TaskHandler {
	return &TaskHandler{repo: repo, reminderRepo: reminderRepo, settingsRepo: settingsRepo, broker: broker, scheduler: sched}
}

func (h *TaskHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.TaskFilters{}

	if v := q.Get("status"); v != "" {
		f.Status = &v
	}
	if v := q.Get("project_id"); v != "" {
		f.ProjectID = &v
	}
	if v := q.Get("area_id"); v != "" {
		f.AreaID = &v
	}
	if v := q.Get("heading_id"); v != "" {
		f.HeadingID = &v
	}
	if v := q.Get("tag_ids"); v != "" {
		f.TagIDs = strings.Split(v, ",")
	}
	if v := q.Get("when_date"); v != "" {
		f.WhenDate = &v
	}
	if v := q.Get("when_before"); v != "" {
		f.WhenBefore = &v
	}
	if v := q.Get("when_after"); v != "" {
		f.WhenAfter = &v
	}
	if v := q.Get("has_deadline"); v != "" {
		b := v == "true"
		f.HasDeadline = &b
	}
	if v := q.Get("search"); v != "" {
		f.Search = &v
	}

	tasks, err := h.repo.List(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": tasks})
}

func (h *TaskHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.repo.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *TaskHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input model.CreateTaskInput
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input.ID != "" && !isClientID(input.ID) {
		writeFieldError(w, http.StatusBadRequest, "id must be 8-21 characters of A-Za-z0-9_-", "VALIDATION", "id")
		return
	}
	if input.Title == "" {
		writeFieldError(w, http.StatusBadRequest, "title is required", "VALIDATION", "title")
		return
	}
	if input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
		writeFieldError(w, http.StatusBadRequest, "when_date must be YYYY-MM-DD or 'someday'", "VALIDATION", "when_date")
		return
	}
	if input.Deadline != nil && !isDate(*input.Deadline) {
		writeFieldError(w, http.StatusBadRequest, "deadline must be YYYY-MM-DD", "VALIDATION", "deadline")
		return
	}
	if input.WhenDate != nil && input.Deadline != nil && *input.WhenDate != "someday" && *input.Deadline < *input.WhenDate {
		writeFieldError(w, http.StatusBadRequest, "deadline cannot be before the when date", "VALIDATION", "deadline")
		return
	}
	db := h.repo.Conn()
	if input.ProjectID != nil && !refExists(w, db, "projects", "project", "project_id", *input.ProjectID) {
		return
	}
	if input.AreaID != nil && !refExists(w, db, "areas", "area", "area_id", *input.AreaID) {
		return
	}
	if input.HeadingID != nil {
		if input.ProjectID != nil {
			inProject, err := repository.HeadingInProject(db, *input.HeadingID, *input.ProjectID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
				return
			}
			if !inProject {
				writeFieldError(w, http.StatusUnprocessableEntity, "heading is not in this project", "VALIDATION", "heading_id")
				return
			}
		} else if !refExists(w, db, "headings", "heading", "heading_id", *input.HeadingID) {
			return
		}
	}
	if !tagsExist(w, db, "tag_ids", input.TagIDs) {
		return
	}
	// Idempotent create: a client-supplied id that already exists replays
	// the stored entity with 200 — no write, no broadcast, no change log.
	if input.ID != "" {
		existing, err := h.repo.GetByID(input.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return
		}
		if existing != nil {
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}
	// Read the settings before opening the transaction: GetOrCreate may write
	// on its own connection, which would contend with the transaction below.
	userID, _ := r.Context().Value(mw.UserIDKey).(string)
	settings, settingsErr := h.settingsRepo.GetOrCreate(userID)
	var defaultReminder *model.CreateReminderInput
	if settingsErr == nil && settings != nil && settings.DefaultReminderType != nil {
		defaultReminder = &model.CreateReminderInput{
			Type:  model.ReminderType(*settings.DefaultReminderType),
			Value: settings.DefaultReminderValue,
		}
	}

	// The handler owns the transaction so the task and its default reminder
	// commit or roll back together. The WithTx-bound repositories reuse tx
	// through inTx; a reminder error rolls the task back and surfaces as 500.
	var task *model.TaskDetail
	err := repository.InTx(h.repo.Conn(), func(tx *sql.Tx) error {
		var err error
		task, err = h.repo.WithTx(tx).Create(input)
		if err != nil {
			return err
		}
		if defaultReminder != nil {
			if _, err := h.reminderRepo.WithTx(tx).Create(task.ID, *defaultReminder); err != nil {
				return fmt.Errorf("create default reminder: %w", err)
			}
			// Re-fetch to include the new reminder in the response
			task, err = h.repo.WithTx(tx).GetByID(task.ID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}

	h.broker.BroadcastJSON("task_created", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusCreated, task)
}

func (h *TaskHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var input model.UpdateTaskInput
	raw, err := decodeJSONWithRaw(r, &input)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	input.Raw = raw

	if input.Title != nil && *input.Title == "" {
		writeFieldError(w, http.StatusBadRequest, "title cannot be empty", "VALIDATION", "title")
		return
	}
	if _, ok := input.Raw["when_date"]; ok && input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
		writeFieldError(w, http.StatusBadRequest, "when_date must be YYYY-MM-DD or 'someday'", "VALIDATION", "when_date")
		return
	}
	if _, ok := input.Raw["deadline"]; ok && input.Deadline != nil && !isDate(*input.Deadline) {
		writeFieldError(w, http.StatusBadRequest, "deadline must be YYYY-MM-DD", "VALIDATION", "deadline")
		return
	}
	db := h.repo.Conn()
	if _, ok := input.Raw["project_id"]; ok && input.ProjectID != nil &&
		!refExists(w, db, "projects", "project", "project_id", *input.ProjectID) {
		return
	}
	if _, ok := input.Raw["area_id"]; ok && input.AreaID != nil &&
		!refExists(w, db, "areas", "area", "area_id", *input.AreaID) {
		return
	}
	if _, ok := input.Raw["heading_id"]; ok && input.HeadingID != nil &&
		!refExists(w, db, "headings", "heading", "heading_id", *input.HeadingID) {
		return
	}
	_, projectGiven := input.Raw["project_id"]
	if !h.checkHeadingMembership(w, db, id, input.HeadingID, projectGiven, input.ProjectID) {
		return
	}
	if input.TagIDs != nil && !tagsExist(w, db, "tag_ids", input.TagIDs) {
		return
	}

	// Validate deadline >= when_date: resolve effective values from input + existing task
	if _, hasWhen := input.Raw["when_date"]; hasWhen {
		if _, hasDeadline := input.Raw["deadline"]; hasDeadline {
			// Both changing — validate directly
			if input.WhenDate != nil && input.Deadline != nil && *input.WhenDate != "someday" && *input.Deadline < *input.WhenDate {
				writeFieldError(w, http.StatusBadRequest, "deadline cannot be before the when date", "VALIDATION", "deadline")
				return
			}
		}
	}
	if needsDateCrossCheck(input) {
		existing, err := h.repo.GetByID(id)
		if err != nil || existing == nil {
			writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
			return
		}
		whenDate := existing.WhenDate
		if _, ok := input.Raw["when_date"]; ok {
			whenDate = input.WhenDate
		}
		deadline := existing.Deadline
		if _, ok := input.Raw["deadline"]; ok {
			deadline = input.Deadline
		}
		if whenDate != nil && deadline != nil && *whenDate != "someday" && *deadline < *whenDate {
			writeFieldError(w, http.StatusBadRequest, "deadline cannot be before the when date", "VALIDATION", "deadline")
			return
		}
	}

	task, err := h.repo.Update(id, input)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate timeless date") {
			writeError(w, http.StatusBadRequest, "a schedule for this date already exists without a time", "VALIDATION")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusOK, task)
}

func (h *TaskHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("task_deleted", map[string]interface{}{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *TaskHandler) Purge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.PermanentDelete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("task_purged", map[string]interface{}{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *TaskHandler) Complete(w http.ResponseWriter, r *http.Request) {
	h.finishTask(w, chi.URLParam(r, "id"), "completed")
}

func (h *TaskHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	h.finishTask(w, chi.URLParam(r, "id"), "canceled")
}

func (h *TaskHandler) WontDo(w http.ResponseWriter, r *http.Request) {
	h.finishTask(w, chi.URLParam(r, "id"), "wont_do")
}

// finishTask routes every finishing action through the scheduler's guarded,
// transactional FinishTask. A repeated call returns 200 with the task and
// spawns nothing; an unknown task maps to 404 as before.
func (h *TaskHandler) finishTask(w http.ResponseWriter, id, status string) {
	if h.scheduler == nil {
		writeError(w, http.StatusInternalServerError, "scheduler not available", "INTERNAL")
		return
	}
	task, _, err := h.scheduler.FinishTask(id, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusOK, task)
}

func (h *TaskHandler) Reopen(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.repo.Reopen(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusOK, task)
}

func (h *TaskHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.repo.Restore(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusOK, task)
}

func (h *TaskHandler) Review(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.repo.MarkReviewed(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusOK, task)
}

func (h *TaskHandler) Move(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var input model.MoveTaskInput
	raw, err := decodeJSONWithRaw(r, &input)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	input.Raw = raw
	if input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
		writeFieldError(w, http.StatusBadRequest, "when_date must be YYYY-MM-DD or 'someday'", "VALIDATION", "when_date")
		return
	}
	db := h.repo.Conn()
	if input.ProjectID != nil && !refExists(w, db, "projects", "project", "project_id", *input.ProjectID) {
		return
	}
	if input.AreaID != nil && !refExists(w, db, "areas", "area", "area_id", *input.AreaID) {
		return
	}
	if input.HeadingID != nil && !refExists(w, db, "headings", "heading", "heading_id", *input.HeadingID) {
		return
	}
	_, projectGiven := input.Raw["project_id"]
	if !h.checkHeadingMembership(w, db, id, input.HeadingID, projectGiven, input.ProjectID) {
		return
	}
	task, err := h.repo.Move(id, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": task.ID, "task": task})
	writeJSON(w, http.StatusOK, task)
}

// checkHeadingMembership applies Create's heading rule to a partial change:
// when the request carries a heading, it must belong to the effective
// project — the project the request sets, or the task's stored one when the
// request leaves the project untouched. With no effective project the plain
// existence check (already run) is enough, matching Create's shape. It
// writes the error response and returns false when validation fails.
func (h *TaskHandler) checkHeadingMembership(w http.ResponseWriter, db repository.DBTX, taskID string, headingID *string, projectGiven bool, projectID *string) bool {
	if headingID == nil {
		return true
	}
	effective := projectID
	if !projectGiven {
		existing, err := h.repo.GetByID(taskID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return false
		}
		if existing == nil {
			return true // unknown task: the repository's 404 path handles it
		}
		effective = existing.ProjectID
	}
	if effective == nil {
		return true
	}
	inProject, err := repository.HeadingInProject(db, *headingID, *effective)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return false
	}
	if !inProject {
		writeFieldError(w, http.StatusUnprocessableEntity, "heading is not in this project", "VALIDATION", "heading_id")
		return false
	}
	return true
}

func (h *TaskHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []model.ReorderItem `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := h.repo.Reorder(body.Items); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	ids := make([]string, len(body.Items))
	for i, item := range body.Items {
		ids[i] = item.ID
	}
	h.broker.BroadcastJSON("bulk_change", map[string]interface{}{
		"type": "reorder", "entity": "task", "ids": ids,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *TaskHandler) BulkAction(w http.ResponseWriter, r *http.Request) {
	var input model.BulkActionInput
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if len(input.TaskIDs) == 0 {
		writeError(w, http.StatusBadRequest, "task_ids required", "BAD_REQUEST")
		return
	}
	if len(input.TaskIDs) > 100 {
		writeError(w, http.StatusBadRequest, "maximum 100 tasks per bulk action", "BAD_REQUEST")
		return
	}

	validActions := map[string]bool{
		"complete": true, "cancel": true, "wontdo": true, "delete": true,
		"set_when": true, "set_deadline": true, "set_priority": true, "toggle_priority": true,
		"move_project": true, "add_tags": true, "remove_tags": true, "toggle_tags": true,
		"mark_reviewed": true,
	}
	if !validActions[input.Action] {
		writeError(w, http.StatusBadRequest, "invalid action: "+input.Action, "BAD_REQUEST")
		return
	}

	// Date params of bulk actions are validated at the boundary like every
	// other date field. An empty string clears the date and stays allowed.
	for _, tc := range []struct {
		action string
		param  string
		valid  func(string) bool
		msg    string
	}{
		{"set_when", "when_date", isWhenDate, "when_date must be YYYY-MM-DD or 'someday'"},
		{"set_deadline", "deadline", isDate, "deadline must be YYYY-MM-DD"},
	} {
		if input.Action != tc.action {
			continue
		}
		v, present := input.Params[tc.param]
		if !present || v == nil {
			continue
		}
		s, isStr := v.(string)
		if !isStr || (s != "" && !tc.valid(s)) {
			writeFieldError(w, http.StatusBadRequest, tc.msg, "VALIDATION", tc.param)
			return
		}
	}

	// Status actions run the one guarded finishing path per task; every other
	// action is a set-oriented bulk write.
	if status, isStatus := bulkStatusActions[input.Action]; isStatus {
		affected, err := h.bulkFinish(input.TaskIDs, status)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return
		}
		h.broker.BroadcastJSON("bulk_change", map[string]interface{}{
			"type":   input.Action,
			"entity": "task",
			"ids":    input.TaskIDs,
		})
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":       true,
			"affected": affected,
		})
		return
	}

	affected, err := h.repo.BulkAction(input)
	if err != nil {
		// Same conflict shape as a single-task when_date update: the
		// bulk action was rejected and rolled back, nothing was applied.
		if strings.Contains(err.Error(), "duplicate timeless date") {
			writeError(w, http.StatusBadRequest, "a schedule for this date already exists without a time", "VALIDATION")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}

	h.broker.BroadcastJSON("bulk_change", map[string]interface{}{
		"type":   input.Action,
		"entity": "task",
		"ids":    input.TaskIDs,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       true,
		"affected": affected,
	})
}

// bulkStatusActions maps the bulk action names to task statuses.
var bulkStatusActions = map[string]string{
	"complete": "completed",
	"cancel":   "canceled",
	"wontdo":   "wont_do",
}

// bulkFinish runs the guarded finishing path per task id. Only tasks that
// actually transitioned count as affected; already-finished and unknown ids
// do not.
func (h *TaskHandler) bulkFinish(taskIDs []string, status string) (int, error) {
	if h.scheduler == nil {
		return 0, fmt.Errorf("scheduler not available")
	}
	affected := 0
	for _, id := range taskIDs {
		task, changed, err := h.scheduler.FinishTask(id, status)
		if err != nil {
			return affected, err
		}
		if task != nil && changed {
			affected++
		}
	}
	return affected, nil
}

// needsDateCrossCheck returns true when only one of when_date/deadline is
// changing, so we need to fetch the existing task to validate the pair.
func needsDateCrossCheck(input model.UpdateTaskInput) bool {
	_, hasWhen := input.Raw["when_date"]
	_, hasDeadline := input.Raw["deadline"]
	return (hasWhen && !hasDeadline) || (!hasWhen && hasDeadline)
}
