package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"log"

	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
	"github.com/collinjanssen/thingstodo/internal/sse"
)

const (
	defaultPullLimit = 500
	maxPullLimit     = 1000
)

// SyncHandler handles sync pull and push endpoints.
type SyncHandler struct {
	changeLog       *repository.ChangeLogRepository
	tasks           *repository.TaskRepository
	projects        *repository.ProjectRepository
	areas           *repository.AreaRepository
	tags            *repository.TagRepository
	checklist       *repository.ChecklistRepository
	headings        *repository.HeadingRepository
	attachments     *repository.AttachmentRepository
	attachmentsPath string
	schedules       *repository.ScheduleRepository
	reminders       *repository.ReminderRepository
	repeatRules     *repository.RepeatRuleRepository
	scheduler       *scheduler.Scheduler
	loc             *time.Location
	broker          *sse.Broker
}

// NewSyncHandler creates a new SyncHandler.
func NewSyncHandler(
	changeLog *repository.ChangeLogRepository,
	tasks *repository.TaskRepository,
	projects *repository.ProjectRepository,
	areas *repository.AreaRepository,
	tags *repository.TagRepository,
	checklist *repository.ChecklistRepository,
	headings *repository.HeadingRepository,
	attachments *repository.AttachmentRepository,
	schedules *repository.ScheduleRepository,
	reminders *repository.ReminderRepository,
	repeatRules *repository.RepeatRuleRepository,
	sched *scheduler.Scheduler,
	loc *time.Location,
	// attachmentsPath is the files root the attachment delete path uses to
	// remove a stored file once its last row is gone.
	attachmentsPath string,
	// broker receives single-entity change events for the writes applied
	// here, so connected SSE clients refresh. May be nil in tests.
	broker *sse.Broker,
) *SyncHandler {
	if loc == nil {
		loc = time.UTC
	}
	return &SyncHandler{
		changeLog:       changeLog,
		tasks:           tasks,
		projects:        projects,
		areas:           areas,
		tags:            tags,
		checklist:       checklist,
		headings:        headings,
		attachments:     attachments,
		attachmentsPath: attachmentsPath,
		schedules:       schedules,
		reminders:       reminders,
		repeatRules:     repeatRules,
		scheduler:       sched,
		loc:             loc,
		broker:          broker,
	}
}

// broadcastTaskChange emits a single-entity change event for a task written
// through sync push. Without a broker it is a no-op.
func (h *SyncHandler) broadcastTaskChange(eventType, taskID string) {
	if h.broker == nil {
		return
	}
	h.broker.BroadcastChange(eventType, "task", taskID, nil)
}

// PullResponse is returned by GET /api/sync/pull.
type PullResponse struct {
	Changes []repository.ChangeLogEntry `json:"changes"`
	Cursor  int64                       `json:"cursor"`
	HasMore bool                        `json:"has_more"`
}

// Pull returns changes since a given sequence number.
// GET /api/sync/pull?since={seq}&limit={n}
func (h *SyncHandler) Pull(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil || since < 0 {
			writeError(w, http.StatusBadRequest, "invalid 'since' parameter", "BAD_REQUEST")
			return
		}
	}

	limit := defaultPullLimit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 1 {
			writeError(w, http.StatusBadRequest, "invalid 'limit' parameter", "BAD_REQUEST")
			return
		}
		limit = l
	}
	if limit > maxPullLimit {
		limit = maxPullLimit
	}

	// Detect expired cursor: if the client has a cursor but the oldest entry in the
	// change log is newer than since+1, the history has been purged and the client
	// must fall back to a full sync. oldest = since+1 is still contiguous — the
	// client holds every surviving entry's predecessor.
	if since > 0 {
		oldest, err := h.changeLog.GetOldestSeq()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return
		}
		if oldest > 0 && oldest > since+1 {
			writeError(w, http.StatusGone, "cursor expired: change log has been purged, perform a full sync", "CURSOR_EXPIRED")
			return
		}
	}

	// Fetch limit+1 to detect has_more
	entries, err := h.changeLog.GetChangesSince(since, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}

	var cursor int64
	if len(entries) > 0 {
		cursor = entries[len(entries)-1].Seq
	} else {
		cursor = since
	}

	writeJSON(w, http.StatusOK, PullResponse{
		Changes: entries,
		Cursor:  cursor,
		HasMore: hasMore,
	})
}

// FullSyncResponse is returned by GET /api/sync/full.
type FullSyncResponse struct {
	Tasks       interface{} `json:"tasks"`
	Projects    interface{} `json:"projects"`
	Areas       interface{} `json:"areas"`
	Tags        interface{} `json:"tags"`
	Headings    interface{} `json:"headings"`
	Checklist   interface{} `json:"checklist"`
	Attachments interface{} `json:"attachments"`
	Schedules   interface{} `json:"schedules"`
	Reminders   interface{} `json:"reminders"`
	RepeatRules interface{} `json:"repeat_rules"`
	Cursor      int64       `json:"cursor"`
}

// Full returns all current entities along with the latest change_log cursor.
// GET /api/sync/full
func (h *SyncHandler) Full(w http.ResponseWriter, r *http.Request) {
	// The cursor is read before the snapshot tables on purpose: a write
	// that commits after this point is both in the snapshot and re-sent by
	// a later pull from this cursor, and pull re-applies full snapshots so
	// the overlap is harmless. A cursor taken after the reads could skip a
	// write that landed between the two. This ordering is deliberate but,
	// without a hook between the reads, not observable by a test.
	cursor, err := h.changeLog.GetLatestSeq()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get cursor: "+err.Error(), "INTERNAL")
		return
	}

	// IncludeDeleted: trashed tasks must reach clients on first load (the
	// web Trash view seeds from this snapshot). Purged tasks are
	// hard-deleted rows and never appear.
	tasks, _, err := h.tasks.List(model.TaskFilters{IncludeDeleted: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tasks: "+err.Error(), "INTERNAL")
		return
	}

	projects, err := h.projects.List(nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load projects: "+err.Error(), "INTERNAL")
		return
	}

	areas, err := h.areas.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load areas: "+err.Error(), "INTERNAL")
		return
	}

	tags, err := h.tags.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tags: "+err.Error(), "INTERNAL")
		return
	}

	headings, err := h.headings.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load headings: "+err.Error(), "INTERNAL")
		return
	}

	checklist, err := h.checklist.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load checklist: "+err.Error(), "INTERNAL")
		return
	}

	attachments, err := h.attachments.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load attachments: "+err.Error(), "INTERNAL")
		return
	}

	schedules, err := h.schedules.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load schedules: "+err.Error(), "INTERNAL")
		return
	}

	reminders, err := h.reminders.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load reminders: "+err.Error(), "INTERNAL")
		return
	}

	repeatRules, err := h.repeatRules.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load repeat rules: "+err.Error(), "INTERNAL")
		return
	}

	writeJSON(w, http.StatusOK, FullSyncResponse{
		Tasks:       tasks,
		Projects:    projects,
		Areas:       areas,
		Tags:        tags,
		Headings:    headings,
		Checklist:   checklist,
		Attachments: attachments,
		Schedules:   schedules,
		Reminders:   reminders,
		RepeatRules: repeatRules,
		Cursor:      cursor,
	})
}

// --- Push types ---

// SyncPushRequest is the body for POST /api/sync/push.
type SyncPushRequest struct {
	DeviceID string       `json:"device_id"`
	Changes  []SyncChange `json:"changes"`
}

// SyncChange represents a single change from the client.
type SyncChange struct {
	Entity          string                 `json:"entity"`
	EntityID        string                 `json:"entity_id"`
	Action          string                 `json:"action"` // create, update, delete
	Data            map[string]interface{} `json:"data"`
	Fields          []string               `json:"fields"`
	ClientUpdatedAt string                 `json:"client_updated_at"`
}

// SyncPushResult is the result for a single change in the push response.
type SyncPushResult struct {
	Entity   string `json:"entity"`
	EntityID string `json:"entity_id"`
	Status   string `json:"status"` // applied, conflict_resolved, error
	Seq      int64  `json:"seq"`
	Error    string `json:"error,omitempty"`
	// Code classifies an error result for machine handling by client
	// sync queues: VALIDATION, NOT_FOUND and CONFLICT are permanent
	// (retrying the same change cannot succeed); INTERNAL is transient.
	Code string `json:"code,omitempty"`
}

// SyncPushResponse is returned by POST /api/sync/push.
type SyncPushResponse struct {
	Results []SyncPushResult `json:"results"`
}

// Push applies changes from a client device.
// POST /api/sync/push
func (h *SyncHandler) Push(w http.ResponseWriter, r *http.Request) {
	_ = r.Context().Value(mw.UserIDKey) // verify auth context exists

	var req SyncPushRequest
	if err := decodeJSON(r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}

	if req.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "device_id is required", "VALIDATION")
		return
	}

	results := make([]SyncPushResult, 0, len(req.Changes))
	for _, change := range req.Changes {
		result := h.applyChange(change)
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, SyncPushResponse{Results: results})
}

func (h *SyncHandler) applyChange(change SyncChange) SyncPushResult {
	switch change.Entity {
	case "task":
		return h.applyTaskChange(change)
	case "project":
		return h.applyProjectChange(change)
	case "area":
		return h.applyAreaChange(change)
	case "tag":
		return h.applyTagChange(change)
	case "schedule":
		return h.applyScheduleChange(change)
	case "checklistItem":
		return h.applyChecklistChange(change)
	case "attachment":
		return h.applyAttachmentChange(change)
	case "reminder":
		return h.applyReminderChange(change)
	case "heading":
		return h.applyHeadingChange(change)
	default:
		return SyncPushResult{
			Entity:   change.Entity,
			EntityID: change.EntityID,
			Status:   "error",
			Code:     "VALIDATION",
			Error:    "unsupported entity type: " + change.Entity,
		}
	}
}

// --- Task change application ---

func (h *SyncHandler) applyTaskChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "task", EntityID: change.EntityID}

	switch change.Action {
	case "create":
		// Check if task already exists (idempotency — client may re-push)
		existing, _ := h.tasks.GetByID(change.EntityID)
		if existing != nil {
			result.Status = "applied"
			return result
		}

		input := model.CreateTaskInput{
			ID:    change.EntityID, // use client-provided ID
			Title: stringFromData(change.Data, "title"),
			Notes: stringFromData(change.Data, "notes"),
		}
		if v, ok := change.Data["when_date"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "when_date")
			}
			input.WhenDate = s
		}
		if v, ok := change.Data["high_priority"]; ok {
			if b, ok := v.(bool); ok {
				input.HighPriority = b
			}
		}
		if v, ok := change.Data["deadline"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "deadline")
			}
			input.Deadline = s
		}
		if v, ok := change.Data["project_id"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "project_id")
			}
			input.ProjectID = s
		}
		if v, ok := change.Data["area_id"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "area_id")
			}
			input.AreaID = s
		}
		if v, ok := change.Data["heading_id"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "heading_id")
			}
			input.HeadingID = s
		}
		if v, ok := change.Data["tag_ids"]; ok && v != nil {
			if arr, ok := v.([]interface{}); ok {
				for _, item := range arr {
					if s, ok := item.(string); ok {
						input.TagIDs = append(input.TagIDs, s)
					}
				}
			}
		}

		if field, msg, ok := h.validateTaskCreate(input); !ok {
			return syncFieldError(result, field, msg)
		}

		task, err := h.tasks.Create(input)
		if err != nil {
			return syncFail(result, err)
		}
		_ = task
		h.broadcastTaskChange("task_created", change.EntityID)
		result.Status = "applied"

	case "update":
		existing, err := h.tasks.GetByID(change.EntityID)
		if err != nil || existing == nil {
			return syncFieldError(result, "", "task not found")
		}

		// LWW: compare client_updated_at vs server updated_at
		status := "applied"
		clientTime, err := time.Parse(time.RFC3339, change.ClientUpdatedAt)
		if err != nil {
			// Try alternate format (SQLite datetime)
			clientTime, err = time.Parse("2006-01-02 15:04:05", change.ClientUpdatedAt)
			if err != nil {
				return syncFieldError(result, "", "invalid client_updated_at format")
			}
		}

		serverTime, err := time.Parse("2006-01-02 15:04:05", existing.UpdatedAt)
		if err != nil {
			serverTime, _ = time.Parse(time.RFC3339, existing.UpdatedAt)
		}

		// If server is strictly newer, it's a conflict resolution
		if serverTime.After(clientTime) {
			status = "conflict_resolved"
		}

		// Build update input only for specified fields
		input := model.UpdateTaskInput{
			Raw: make(map[string]json.RawMessage),
		}
		for _, field := range change.Fields {
			val := change.Data[field]
			rawVal, _ := json.Marshal(val)
			input.Raw[field] = rawVal

			switch field {
			case "title":
				s := stringFromData(change.Data, "title")
				input.Title = &s
			case "notes":
				s := stringFromData(change.Data, "notes")
				input.Notes = &s
			case "when_date":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "when_date")
					}
					input.WhenDate = s
				}
			case "high_priority":
				if b, ok := val.(bool); ok {
					input.HighPriority = &b
				}
			case "deadline":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "deadline")
					}
					input.Deadline = s
				}
			case "project_id":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "project_id")
					}
					input.ProjectID = s
				}
			case "area_id":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "area_id")
					}
					input.AreaID = s
				}
			case "heading_id":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "heading_id")
					}
					input.HeadingID = s
				}
			case "tag_ids":
				if val != nil {
					if arr, ok := val.([]interface{}); ok {
						tagIDs := make([]string, 0, len(arr))
						for _, item := range arr {
							if s, ok := item.(string); ok {
								tagIDs = append(tagIDs, s)
							}
						}
						input.TagIDs = tagIDs
					}
				} else {
					// Explicit null → clear all tags
					input.TagIDs = []string{}
				}
			case "status":
				if s, ok := val.(string); ok {
					switch s {
					case "completed", "canceled", "wont_do":
						// One finishing path: the guarded, transactional
						// FinishTask (idempotent on re-push, spawns once).
						if h.scheduler == nil {
							result.Status = "error"
							result.Code = "INTERNAL"
							result.Error = "scheduler not available"
							return result
						}
						if _, _, cErr := h.scheduler.FinishTask(change.EntityID, s); cErr != nil {
							return syncFail(result, cErr)
						}
						h.broadcastTaskChange("task_updated", change.EntityID)
						result.Status = status
						return result
					case "open":
						if _, cErr := h.tasks.Reopen(change.EntityID); cErr != nil {
							return syncFail(result, cErr)
						}
						h.broadcastTaskChange("task_updated", change.EntityID)
						result.Status = status
						return result
					}
				}
			case "deleted_at":
				if val != nil {
					// Soft-delete
					if cErr := h.tasks.Delete(change.EntityID); cErr != nil {
						return syncFail(result, cErr)
					}
					h.broadcastTaskChange("task_deleted", change.EntityID)
					result.Status = status
					return result
				}
				// Restore (deleted_at = null)
				if _, cErr := h.tasks.Restore(change.EntityID); cErr != nil {
					return syncFail(result, cErr)
				}
				h.broadcastTaskChange("task_updated", change.EntityID)
				result.Status = status
				return result
			}
		}

		if len(input.Raw) > 0 {
			if field, msg, ok := h.validateTaskUpdate(input, existing); !ok {
				return syncFieldError(result, field, msg)
			}
			_, err = h.tasks.Update(change.EntityID, input)
			if err != nil {
				return syncFail(result, err)
			}
			h.broadcastTaskChange("task_updated", change.EntityID)
		}
		result.Status = status

	case "delete":
		err := h.tasks.Delete(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		h.broadcastTaskChange("task_deleted", change.EntityID)
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}

	return result
}

// validateTaskCreate applies the same boundary rules as POST /api/tasks:
// id format, date formats, the deadline >= when_date rule, and existence of
// every referenced id. It returns the failing field and message for a
// per-change error.
func (h *SyncHandler) validateTaskCreate(input model.CreateTaskInput) (string, string, bool) {
	if field, msg, ok := validClientID(input.ID); !ok {
		return field, msg, false
	}
	if input.Title == "" {
		return "title", "title is required", false
	}
	if input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
		return "when_date", "must be YYYY-MM-DD or 'someday'", false
	}
	if input.Deadline != nil && !isDate(*input.Deadline) {
		return "deadline", "must be YYYY-MM-DD", false
	}
	if input.WhenDate != nil && input.Deadline != nil && *input.WhenDate != "someday" && *input.Deadline < *input.WhenDate {
		return "deadline", "cannot be before the when date", false
	}
	db := h.tasks.Conn()
	if input.ProjectID != nil {
		if field, msg, ok := refFound(db, "projects", "project_id", "project not found", *input.ProjectID); !ok {
			return field, msg, false
		}
	}
	if input.AreaID != nil {
		if field, msg, ok := refFound(db, "areas", "area_id", "area not found", *input.AreaID); !ok {
			return field, msg, false
		}
	}
	if input.HeadingID != nil {
		if input.ProjectID != nil {
			inProject, err := repository.HeadingInProject(db, *input.HeadingID, *input.ProjectID)
			if err != nil {
				log.Printf("sync heading check %s: %v", *input.HeadingID, err)
				return "", "internal error", false
			}
			if !inProject {
				return "heading_id", "heading is not in this project", false
			}
		} else if field, msg, ok := refFound(db, "headings", "heading_id", "heading not found", *input.HeadingID); !ok {
			return field, msg, false
		}
	}
	for _, tagID := range input.TagIDs {
		if field, msg, ok := refFound(db, "tags", "tag_ids", "tag not found", tagID); !ok {
			return field, msg, false
		}
	}
	return "", "", true
}

// validateTaskUpdate applies the same boundary rules as PATCH /api/tasks to
// the fields a sync update actually changes. existing is the stored task;
// it resolves the effective project for the heading membership rule.
func (h *SyncHandler) validateTaskUpdate(input model.UpdateTaskInput, existing *model.TaskDetail) (string, string, bool) {
	if input.Title != nil && *input.Title == "" {
		return "title", "cannot be empty", false
	}
	if _, ok := input.Raw["when_date"]; ok && input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
		return "when_date", "must be YYYY-MM-DD or 'someday'", false
	}
	if _, ok := input.Raw["deadline"]; ok && input.Deadline != nil && !isDate(*input.Deadline) {
		return "deadline", "must be YYYY-MM-DD", false
	}
	db := h.tasks.Conn()
	if _, ok := input.Raw["project_id"]; ok && input.ProjectID != nil {
		if field, msg, ok := refFound(db, "projects", "project_id", "project not found", *input.ProjectID); !ok {
			return field, msg, false
		}
	}
	if _, ok := input.Raw["area_id"]; ok && input.AreaID != nil {
		if field, msg, ok := refFound(db, "areas", "area_id", "area not found", *input.AreaID); !ok {
			return field, msg, false
		}
	}
	if _, ok := input.Raw["heading_id"]; ok && input.HeadingID != nil {
		if field, msg, ok := refFound(db, "headings", "heading_id", "heading not found", *input.HeadingID); !ok {
			return field, msg, false
		}
		// A carried heading must belong to the effective project — the one
		// this change sets, or the task's stored one when it leaves the
		// project untouched — mirroring the REST PATCH/Move boundary check.
		effective := existing.ProjectID
		if _, projectGiven := input.Raw["project_id"]; projectGiven {
			effective = input.ProjectID
		}
		if effective != nil {
			inProject, err := repository.HeadingInProject(db, *input.HeadingID, *effective)
			if err != nil {
				log.Printf("sync heading check %s: %v", *input.HeadingID, err)
				return "", "internal error", false
			}
			if !inProject {
				return "heading_id", "heading is not in this project", false
			}
		}
	}
	for _, tagID := range input.TagIDs {
		if field, msg, ok := refFound(db, "tags", "tag_ids", "tag not found", tagID); !ok {
			return field, msg, false
		}
	}
	return "", "", true
}

// --- Project change application ---

func (h *SyncHandler) applyProjectChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "project", EntityID: change.EntityID}

	switch change.Action {
	case "create":
		existing, err := h.projects.GetByID(change.EntityID)
		if err == nil && existing != nil {
			result.Status = "applied"
			return result
		}

		input := model.CreateProjectInput{
			ID:    change.EntityID,
			Title: stringFromData(change.Data, "title"),
			Notes: stringFromData(change.Data, "notes"),
		}
		if v, ok := change.Data["area_id"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "area_id")
			}
			input.AreaID = s
		}
		if v, ok := change.Data["when_date"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "when_date")
			}
			input.WhenDate = s
		}
		if v, ok := change.Data["deadline"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "deadline")
			}
			input.Deadline = s
		}

		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if input.Title == "" {
			return syncFieldError(result, "title", "title is required")
		}
		if input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
			return syncFieldError(result, "when_date", "must be YYYY-MM-DD or 'someday'")
		}
		if input.Deadline != nil && !isDate(*input.Deadline) {
			return syncFieldError(result, "deadline", "must be YYYY-MM-DD")
		}
		if input.AreaID != nil {
			if field, msg, ok := refFound(h.tasks.Conn(), "areas", "area_id", "area not found", *input.AreaID); !ok {
				return syncFieldError(result, field, msg)
			}
		}
		for _, tagID := range input.TagIDs {
			if field, msg, ok := refFound(h.tasks.Conn(), "tags", "tag_ids", "tag not found", tagID); !ok {
				return syncFieldError(result, field, msg)
			}
		}

		_, err = h.projects.Create(input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "update":
		existing, err := h.projects.GetByID(change.EntityID)
		if err != nil || existing == nil {
			return syncFieldError(result, "", "project not found")
		}

		status := "applied"
		clientTime, serverTime := parseTimes(change.ClientUpdatedAt, existing.UpdatedAt)
		if serverTime.After(clientTime) {
			status = "conflict_resolved"
		}

		input := model.UpdateProjectInput{
			Raw: make(map[string]json.RawMessage),
		}
		for _, field := range change.Fields {
			val := change.Data[field]
			rawVal, _ := json.Marshal(val)
			input.Raw[field] = rawVal

			switch field {
			case "title":
				s := stringFromData(change.Data, "title")
				input.Title = &s
			case "notes":
				s := stringFromData(change.Data, "notes")
				input.Notes = &s
			case "area_id":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "area_id")
					}
					input.AreaID = s
				}
			case "status":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "status")
					}
					input.Status = s
				}
			case "when_date":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "when_date")
					}
					input.WhenDate = s
				}
			case "deadline":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "deadline")
					}
					input.Deadline = s
				}
			}
		}

		if len(input.Raw) > 0 {
			if input.Title != nil && *input.Title == "" {
				return syncFieldError(result, "title", "cannot be empty")
			}
			if _, ok := input.Raw["when_date"]; ok && input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
				return syncFieldError(result, "when_date", "must be YYYY-MM-DD or 'someday'")
			}
			if _, ok := input.Raw["deadline"]; ok && input.Deadline != nil && !isDate(*input.Deadline) {
				return syncFieldError(result, "deadline", "must be YYYY-MM-DD")
			}
			if _, ok := input.Raw["status"]; ok && input.Status != nil && !isProjectStatus(*input.Status) {
				return syncFieldError(result, "status", "must be open, completed or canceled")
			}
			if _, ok := input.Raw["area_id"]; ok && input.AreaID != nil {
				if field, msg, ok := refFound(h.tasks.Conn(), "areas", "area_id", "area not found", *input.AreaID); !ok {
					return syncFieldError(result, field, msg)
				}
			}
			_, err = h.projects.Update(change.EntityID, input)
			if err != nil {
				return syncFail(result, err)
			}
		}
		result.Status = status

	case "delete":
		err := h.projects.DeleteToTrash(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}

	return result
}

// --- Area change application ---

func (h *SyncHandler) applyAreaChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "area", EntityID: change.EntityID}

	switch change.Action {
	case "create":
		existing, err := h.areas.GetByID(change.EntityID)
		if err == nil && existing != nil {
			result.Status = "applied"
			return result
		}

		input := model.CreateAreaInput{
			ID:    change.EntityID,
			Title: stringFromData(change.Data, "title"),
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if input.Title == "" {
			return syncFieldError(result, "title", "title is required")
		}
		_, err = h.areas.Create(input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "update":
		existing, err := h.areas.GetByID(change.EntityID)
		if err != nil || existing == nil {
			return syncFieldError(result, "", "area not found")
		}

		status := "applied"
		clientTime, serverTime := parseTimes(change.ClientUpdatedAt, existing.UpdatedAt)
		if serverTime.After(clientTime) {
			status = "conflict_resolved"
		}

		input := model.UpdateAreaInput{}
		for _, field := range change.Fields {
			val := change.Data[field]
			switch field {
			case "title":
				s := stringFromData(change.Data, "title")
				input.Title = &s
			case "sort_order":
				if f, ok := val.(float64); ok {
					input.SortOrder = &f
				}
			}
		}
		if input.Title != nil && *input.Title == "" {
			return syncFieldError(result, "title", "cannot be empty")
		}

		_, err = h.areas.Update(change.EntityID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = status

	case "delete":
		err := h.areas.DeleteToTrash(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}

	return result
}

// --- Tag change application ---

func (h *SyncHandler) applyTagChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "tag", EntityID: change.EntityID}

	switch change.Action {
	case "create":
		existing, err := h.tags.GetByID(change.EntityID)
		if err == nil && existing != nil {
			result.Status = "applied"
			return result
		}

		input := model.CreateTagInput{
			ID:    change.EntityID,
			Title: stringFromData(change.Data, "title"),
		}
		if v, ok := change.Data["parent_tag_id"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "parent_tag_id")
			}
			input.ParentTagID = s
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if input.Title == "" {
			return syncFieldError(result, "title", "title is required")
		}
		if input.ParentTagID != nil {
			if field, msg, ok := refFound(h.tasks.Conn(), "tags", "parent_tag_id", "tag not found", *input.ParentTagID); !ok {
				return syncFieldError(result, field, msg)
			}
		}
		_, err = h.tags.Create(input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "update":
		// Tags don't have an updated_at for LWW comparison in the repo,
		// so we apply directly (last write wins by default).
		input := model.UpdateTagInput{
			Raw: make(map[string]json.RawMessage),
		}
		for _, field := range change.Fields {
			val := change.Data[field]
			rawVal, _ := json.Marshal(val)
			input.Raw[field] = rawVal

			switch field {
			case "title":
				s := stringFromData(change.Data, "title")
				input.Title = &s
			case "color":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "color")
					}
					input.Color = s
				}
			case "parent_tag_id":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "parent_tag_id")
					}
					input.ParentTagID = s
				}
			case "sort_order":
				if f, ok := val.(float64); ok {
					input.SortOrder = &f
				}
			}
		}

		if input.Title != nil && *input.Title == "" {
			return syncFieldError(result, "title", "cannot be empty")
		}
		if _, ok := input.Raw["parent_tag_id"]; ok && input.ParentTagID != nil {
			if field, msg, ok := refFound(h.tasks.Conn(), "tags", "parent_tag_id", "tag not found", *input.ParentTagID); !ok {
				return syncFieldError(result, field, msg)
			}
		}
		_, err := h.tags.Update(change.EntityID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "delete":
		err := h.tags.Delete(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}

	return result
}

// --- Schedule change application ---

func (h *SyncHandler) applyScheduleChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "schedule", EntityID: change.EntityID}
	taskID := stringFromData(change.Data, "task_id")

	switch change.Action {
	case "create":
		if taskID == "" {
			return syncFieldError(result, "", "task_id is required")
		}
		input := model.CreateTaskScheduleInput{
			ID:       change.EntityID,
			WhenDate: stringFromData(change.Data, "when_date"),
		}
		if v, ok := change.Data["start_time"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "start_time")
			}
			input.StartTime = s
		}
		if v, ok := change.Data["end_time"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "end_time")
			}
			input.EndTime = s
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if field, msg, ok := refFound(h.tasks.Conn(), "tasks", "task_id", "task not found", taskID); !ok {
			return syncFieldError(result, field, msg)
		}
		// Idempotency — the client may re-push a create whose response
		// was lost: same id under the same task is an applied no-op,
		// same id under another task is a conflict.
		existing, _ := h.schedules.GetByID(change.EntityID)
		if existing != nil {
			if existing.TaskID != taskID {
				result = syncFieldError(result, "id", "already exists for another task")
				result.Code = "CONFLICT"
				return result
			}
			result.Status = "applied"
			return result
		}
		if !isWhenDate(input.WhenDate) {
			return syncFieldError(result, "when_date", "must be YYYY-MM-DD or 'someday'")
		}
		if input.StartTime != nil && !isClock(*input.StartTime) {
			return syncFieldError(result, "start_time", "must be HH:MM in 24-hour form")
		}
		if input.EndTime != nil && !isClock(*input.EndTime) {
			return syncFieldError(result, "end_time", "must be HH:MM in 24-hour form")
		}
		if input.StartTime != nil && input.EndTime != nil && *input.EndTime <= *input.StartTime {
			return syncFieldError(result, "end_time", "must be after start_time")
		}
		_, err := h.schedules.Create(taskID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "update":
		input := model.UpdateTaskScheduleInput{
			Raw: make(map[string]json.RawMessage),
		}
		for _, field := range change.Fields {
			val := change.Data[field]
			rawVal, _ := json.Marshal(val)
			input.Raw[field] = rawVal
			switch field {
			case "when_date":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "when_date")
					}
					input.WhenDate = s
				}
			case "start_time":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "start_time")
					}
					input.StartTime = s
				}
			case "end_time":
				if val != nil {
					s, ok := optString(val)
					if !ok {
						return fieldTypeError(result, "end_time")
					}
					input.EndTime = s
				}
			case "completed":
				if b, ok := val.(bool); ok {
					input.Completed = &b
				}
			case "sort_order":
				if f, ok := val.(float64); ok {
					input.SortOrder = &f
				}
			}
		}
		if _, ok := input.Raw["when_date"]; ok && input.WhenDate != nil && !isWhenDate(*input.WhenDate) {
			return syncFieldError(result, "when_date", "must be YYYY-MM-DD or 'someday'")
		}
		if input.StartTime != nil && !isClock(*input.StartTime) {
			return syncFieldError(result, "start_time", "must be HH:MM in 24-hour form")
		}
		if input.EndTime != nil && !isClock(*input.EndTime) {
			return syncFieldError(result, "end_time", "must be HH:MM in 24-hour form")
		}
		if input.StartTime != nil && input.EndTime != nil && *input.EndTime <= *input.StartTime {
			return syncFieldError(result, "end_time", "must be after start_time")
		}
		_, err := h.schedules.Update(change.EntityID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "delete":
		err := h.schedules.Delete(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}
	return result
}

// --- Checklist change application ---

func (h *SyncHandler) applyChecklistChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "checklistItem", EntityID: change.EntityID}
	taskID := stringFromData(change.Data, "task_id")

	switch change.Action {
	case "create":
		if taskID == "" {
			return syncFieldError(result, "", "task_id is required")
		}
		input := model.CreateChecklistInput{
			ID:    change.EntityID,
			Title: stringFromData(change.Data, "title"),
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if field, msg, ok := refFound(h.tasks.Conn(), "tasks", "task_id", "task not found", taskID); !ok {
			return syncFieldError(result, field, msg)
		}
		// Idempotency — the client may re-push a create whose response
		// was lost: same id under the same task is an applied no-op,
		// same id under another task is a conflict.
		existing, _ := h.checklist.GetByID(change.EntityID)
		if existing != nil {
			if existing.TaskID != taskID {
				result = syncFieldError(result, "id", "already exists for another task")
				result.Code = "CONFLICT"
				return result
			}
			result.Status = "applied"
			return result
		}
		if input.Title == "" {
			return syncFieldError(result, "title", "title is required")
		}
		_, err := h.checklist.Create(taskID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "update":
		input := model.UpdateChecklistInput{}
		for _, field := range change.Fields {
			val := change.Data[field]
			switch field {
			case "title":
				s := stringFromData(change.Data, "title")
				input.Title = &s
			case "completed":
				if b, ok := val.(bool); ok {
					input.Completed = &b
				}
			case "sort_order":
				if f, ok := val.(float64); ok {
					input.SortOrder = &f
				}
			}
		}
		if input.Title != nil && *input.Title == "" {
			return syncFieldError(result, "title", "cannot be empty")
		}
		_, err := h.checklist.Update(change.EntityID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "delete":
		err := h.checklist.Delete(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}
	return result
}

// --- Attachment change application ---

func (h *SyncHandler) applyAttachmentChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "attachment", EntityID: change.EntityID}
	taskID := stringFromData(change.Data, "task_id")

	switch change.Action {
	case "create":
		if taskID == "" {
			return syncFieldError(result, "", "task_id is required")
		}
		input := model.CreateAttachmentInput{
			ID:       change.EntityID,
			Type:     stringFromData(change.Data, "type"),
			Title:    stringFromData(change.Data, "title"),
			URL:      stringFromData(change.Data, "url"),
			MimeType: stringFromData(change.Data, "mime_type"),
		}
		if v, ok := change.Data["file_size"]; ok {
			if f, ok := v.(float64); ok {
				input.FileSize = int64(f)
			}
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if field, msg, ok := refFound(h.tasks.Conn(), "tasks", "task_id", "task not found", taskID); !ok {
			return syncFieldError(result, field, msg)
		}
		// Idempotency — the client may re-push a create whose response
		// was lost: same id under the same task is an applied no-op,
		// same id under another task is a conflict.
		existing, _ := h.attachments.GetByID(change.EntityID)
		if existing != nil {
			if existing.TaskID != taskID {
				result = syncFieldError(result, "id", "already exists for another task")
				result.Code = "CONFLICT"
				return result
			}
			result.Status = "applied"
			return result
		}
		if err := normalizeLinkInput(&input); err != nil {
			return syncFieldError(result, "", err.Error())
		}
		_, err := h.attachments.Create(taskID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "delete":
		// DeleteOne removes exactly this row and reports the stored file
		// only when no other row still references it.
		orphanedFile, err := h.attachments.DeleteOne(change.EntityID)
		if err != nil {
			if errors.Is(err, repository.ErrAttachmentNotFound) {
				// A re-pushed delete of an already-deleted attachment is an
				// idempotent success, like the other sync entity deletes.
				result.Status = "applied"
				return result
			}
			return syncFail(result, err)
		}
		if orphanedFile != "" {
			removeStoredFile(h.attachmentsPath, orphanedFile)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}
	return result
}

// --- Reminder change application ---

func (h *SyncHandler) applyReminderChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "reminder", EntityID: change.EntityID}
	taskID := stringFromData(change.Data, "task_id")

	switch change.Action {
	case "create":
		if taskID == "" {
			return syncFieldError(result, "", "task_id is required")
		}
		input := model.CreateReminderInput{
			ID:    change.EntityID,
			Type:  model.ReminderType(stringFromData(change.Data, "type")),
			Value: int(floatFromData(change.Data, "value")),
		}
		if v, ok := change.Data["exact_at"]; ok && v != nil {
			s, ok := optString(v)
			if !ok {
				return fieldTypeError(result, "exact_at")
			}
			input.ExactAt = s
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if field, msg, ok := refFound(h.tasks.Conn(), "tasks", "task_id", "task not found", taskID); !ok {
			return syncFieldError(result, field, msg)
		}
		// Idempotency — the client may re-push a create whose response
		// was lost: same id under the same task is an applied no-op,
		// same id under another task is a conflict.
		existing, _ := h.reminders.GetByID(change.EntityID)
		if existing != nil {
			if existing.TaskID != taskID {
				result = syncFieldError(result, "id", "already exists for another task")
				result.Code = "CONFLICT"
				return result
			}
			result.Status = "applied"
			return result
		}
		if field, msg, ok := validateReminderCreate(&input, h.loc); !ok {
			return syncFieldError(result, field, msg)
		}
		_, err := h.reminders.Create(taskID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "delete":
		err := h.reminders.Delete(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}
	return result
}

// --- Heading change application ---

func (h *SyncHandler) applyHeadingChange(change SyncChange) SyncPushResult {
	result := SyncPushResult{Entity: "heading", EntityID: change.EntityID}
	projectID := stringFromData(change.Data, "project_id")

	switch change.Action {
	case "create":
		if projectID == "" {
			return syncFieldError(result, "", "project_id is required")
		}
		input := model.CreateHeadingInput{
			ID:    change.EntityID,
			Title: stringFromData(change.Data, "title"),
		}
		if field, msg, ok := validClientID(input.ID); !ok {
			return syncFieldError(result, field, msg)
		}
		if input.Title == "" {
			return syncFieldError(result, "title", "title is required")
		}
		if field, msg, ok := refFound(h.tasks.Conn(), "projects", "project_id", "project not found", projectID); !ok {
			return syncFieldError(result, field, msg)
		}
		_, err := h.headings.Create(projectID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "update":
		input := model.UpdateHeadingInput{}
		for _, field := range change.Fields {
			val := change.Data[field]
			switch field {
			case "title":
				s := stringFromData(change.Data, "title")
				input.Title = &s
			case "sort_order":
				if f, ok := val.(float64); ok {
					input.SortOrder = &f
				}
			}
		}
		if input.Title != nil && *input.Title == "" {
			return syncFieldError(result, "title", "cannot be empty")
		}
		_, err := h.headings.Update(change.EntityID, input)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	case "delete":
		err := h.headings.Delete(change.EntityID)
		if err != nil {
			return syncFail(result, err)
		}
		result.Status = "applied"

	default:
		result = syncFieldError(result, "", "unsupported action: "+change.Action)
	}
	return result
}

// --- Helpers ---

// syncFail marks a sync change result as failed: the message is
// client-safe (syncErrorMessage) and carries a machine-readable code.
func syncFail(result SyncPushResult, err error) SyncPushResult {
	result.Status = "error"
	result.Error = syncErrorMessage(err)
	result.Code = syncErrorCode(err)
	return result
}

// syncErrorCode classifies an error for machine handling by client sync
// queues: VALIDATION, NOT_FOUND and CONFLICT are permanent (a retry of
// the same change cannot succeed), INTERNAL is transient. Sentinels are
// matched with errors.Is; the string checks cover repository errors that
// predate the sentinels.
func syncErrorCode(err error) string {
	if errors.Is(err, repository.ErrDuplicateProjectName) ||
		errors.Is(err, repository.ErrDuplicateAreaName) ||
		errors.Is(err, repository.ErrDuplicateTagName) ||
		errors.Is(err, repository.ErrHasProjects) {
		return "CONFLICT"
	}
	msg := err.Error()
	if strings.Contains(msg, "duplicate timeless date") {
		return "VALIDATION"
	}
	if errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, repository.ErrAttachmentNotFound) ||
		strings.Contains(msg, "not found") {
		return "NOT_FOUND"
	}
	return "INTERNAL"
}

// syncErrorMessage maps a sync change error to a client-safe message.
// User-facing duplicate-name sentinels keep their own text; the duplicate
// timeless date rule names its field; every other error is logged
// server-side and replaced with a generic message so internal details
// (SQLite errors, schema names) never reach clients.
func syncErrorMessage(err error) string {
	if errors.Is(err, repository.ErrDuplicateProjectName) ||
		errors.Is(err, repository.ErrDuplicateAreaName) ||
		errors.Is(err, repository.ErrDuplicateTagName) {
		return err.Error()
	}
	if errors.Is(err, repository.ErrHasProjects) {
		return "HAS_PROJECTS: area still has projects"
	}
	if strings.Contains(err.Error(), "duplicate timeless date") {
		return "when_date: duplicate timeless date"
	}
	log.Printf("sync change error: %v", err)
	return "internal error"
}

// optString converts a sync data value into an optional string field.
// nil stays accepted ((nil, true) — the web UI sends nulls to clear values);
// any other non-string type is rejected with (nil, false) so the caller can
// report a per-change error instead of panicking.
func optString(v interface{}) (*string, bool) {
	switch s := v.(type) {
	case nil:
		return nil, true
	case string:
		return &s, true
	default:
		return nil, false
	}
}

// fieldTypeError marks a sync change result as failed because a field had an
// unexpected JSON type, so the rest of the batch continues.
func fieldTypeError(result SyncPushResult, field string) SyncPushResult {
	result.Status = "error"
	result.Code = "VALIDATION"
	result.Error = "invalid type for field " + field
	return result
}

// syncFieldError marks a sync change result as failed boundary validation.
// The error is "<field>: <msg>" (the message alone when field is empty),
// and the rest of the batch continues. The code classifies the failure:
// NOT_FOUND when the message ends with "not found", otherwise VALIDATION.
func syncFieldError(result SyncPushResult, field, msg string) SyncPushResult {
	result.Status = "error"
	if field == "" {
		result.Error = msg
	} else {
		result.Error = field + ": " + msg
	}
	if strings.HasSuffix(msg, "not found") {
		result.Code = "NOT_FOUND"
	} else {
		result.Code = "VALIDATION"
	}
	return result
}

// validClientID is the sync twin of the REST id check.
func validClientID(id string) (string, string, bool) {
	if id != "" && !isClientID(id) {
		return "id", "must be 8-21 characters of A-Za-z0-9_-", false
	}
	return "", "", true
}

// refFound wraps repository.Exists for the sync per-change error style.
func refFound(db repository.DBTX, table, field, notFoundMsg, id string) (string, string, bool) {
	ok, err := repository.Exists(db, table, id)
	if err != nil {
		log.Printf("sync validation for %s %s: %v", table, id, err)
		return "", "internal error", false
	}
	if !ok {
		return field, notFoundMsg, false
	}
	return "", "", true
}

func stringFromData(data map[string]interface{}, key string) string {
	if v, ok := data[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func floatFromData(data map[string]interface{}, key string) float64 {
	if v, ok := data[key]; ok && v != nil {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

func parseTimes(clientUpdatedAt, serverUpdatedAt string) (time.Time, time.Time) {
	clientTime, err := time.Parse(time.RFC3339, clientUpdatedAt)
	if err != nil {
		clientTime, _ = time.Parse("2006-01-02 15:04:05", clientUpdatedAt)
	}
	serverTime, err := time.Parse("2006-01-02 15:04:05", serverUpdatedAt)
	if err != nil {
		serverTime, _ = time.Parse(time.RFC3339, serverUpdatedAt)
	}
	return clientTime, serverTime
}
