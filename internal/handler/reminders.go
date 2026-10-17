package handler

import (
	"net/http"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/go-chi/chi/v5"
)

type ReminderHandler struct {
	repo   *repository.ReminderRepository
	broker *sse.Broker
	loc    *time.Location
}

// NewReminderHandler builds a ReminderHandler. loc is the location naive
// exact_at timestamps are read in (cfg.Location); nil falls back to UTC.
func NewReminderHandler(repo *repository.ReminderRepository, broker *sse.Broker, loc *time.Location) *ReminderHandler {
	if loc == nil {
		loc = time.UTC
	}
	return &ReminderHandler{repo: repo, broker: broker, loc: loc}
}

func (h *ReminderHandler) List(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "id")
	items, err := h.repo.ListByTask(taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *ReminderHandler) Create(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "id")
	var input model.CreateReminderInput
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input.ID != "" && !isClientID(input.ID) {
		writeFieldError(w, http.StatusBadRequest, "id must be 8-21 characters of A-Za-z0-9_-", "VALIDATION", "id")
		return
	}
	if !refExists(w, h.repo.Conn(), "tasks", "task", "task_id", taskID) {
		return
	}
	if field, msg, ok := validateReminderCreate(&input, h.loc); !ok {
		writeFieldError(w, http.StatusBadRequest, msg, "VALIDATION", field)
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
	item, err := h.repo.Create(taskID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastChange("task_updated", "task", taskID, nil)
	writeJSON(w, http.StatusCreated, item)
}

func (h *ReminderHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	taskID, err := h.repo.GetTaskIDForReminder(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if err := h.repo.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if taskID != "" {
		h.broker.BroadcastChange("task_updated", "task", taskID, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
