package handler

import (
	"errors"
	"net/http"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/go-chi/chi/v5"
)

type AreaHandler struct {
	repo   *repository.AreaRepository
	broker *sse.Broker
}

func NewAreaHandler(repo *repository.AreaRepository, broker *sse.Broker) *AreaHandler {
	return &AreaHandler{repo: repo, broker: broker}
}

func (h *AreaHandler) List(w http.ResponseWriter, r *http.Request) {
	areas, err := h.repo.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"areas": areas})
}

func (h *AreaHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	area, err := h.repo.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if area == nil {
		writeError(w, http.StatusNotFound, "area not found", "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, area)
}

func (h *AreaHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input model.CreateAreaInput
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
	area, err := h.repo.Create(input)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateAreaName) {
			writeError(w, http.StatusConflict, "There is already an area with that name", "DUPLICATE_NAME")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("area_updated", map[string]interface{}{"id": area.ID, "area": area})
	writeJSON(w, http.StatusCreated, area)
}

func (h *AreaHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var input model.UpdateAreaInput
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input.Title != nil && *input.Title == "" {
		writeFieldError(w, http.StatusBadRequest, "title cannot be empty", "VALIDATION", "title")
		return
	}
	area, err := h.repo.Update(id, input)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateAreaName) {
			writeError(w, http.StatusConflict, "There is already an area with that name", "DUPLICATE_NAME")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if area == nil {
		writeError(w, http.StatusNotFound, "area not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastJSON("area_updated", map[string]interface{}{"id": area.ID, "area": area})
	writeJSON(w, http.StatusOK, area)
}

func (h *AreaHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []model.SimpleReorderItem `json:"items"`
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
		"type": "reorder", "entity": "area", "ids": ids,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *AreaHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteWithTasks(id); err != nil {
		if errors.Is(err, repository.ErrAreaHasProjects) {
			writeError(w, http.StatusConflict, "area still has projects", "HAS_PROJECTS")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("bulk_change", map[string]interface{}{
		"type": "delete", "entity": "area", "ids": []string{id},
	})
	w.WriteHeader(http.StatusNoContent)
}
