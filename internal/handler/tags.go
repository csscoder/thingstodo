package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/go-chi/chi/v5"
)

type TagHandler struct {
	repo   *repository.TagRepository
	broker *sse.Broker
}

func NewTagHandler(repo *repository.TagRepository, broker *sse.Broker) *TagHandler {
	return &TagHandler{repo: repo, broker: broker}
}

func (h *TagHandler) List(w http.ResponseWriter, r *http.Request) {
	tags, err := h.repo.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tags": tags})
}

func (h *TagHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input model.CreateTagInput
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
	if input.ParentTagID != nil && !refExists(w, h.repo.Conn(), "tags", "tag", "parent_tag_id", *input.ParentTagID) {
		return
	}
	input.Title = strings.ToLower(input.Title)
	if input.Title == "siyuan" || input.Title == "obsidian" {
		writeError(w, http.StatusForbidden, fmt.Sprintf("%q is a reserved tag name", input.Title), "RESERVED")
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
	tag, err := h.repo.Create(input)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateTagName) {
			writeError(w, http.StatusConflict, "There is already a tag with that name", "DUPLICATE_NAME")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastChange("tag_updated", "tag", tag.ID, map[string]any{"tag": tag})
	writeJSON(w, http.StatusCreated, tag)
}

func (h *TagHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var input model.UpdateTagInput
	raw, err := decodeJSONWithRaw(r, &input)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	input.Raw = raw
	if input.Title != nil {
		if *input.Title == "" {
			writeFieldError(w, http.StatusBadRequest, "title cannot be empty", "VALIDATION", "title")
			return
		}
		lower := strings.ToLower(*input.Title)
		input.Title = &lower
		if lower == "siyuan" || lower == "obsidian" {
			writeError(w, http.StatusForbidden, fmt.Sprintf("%q is a reserved tag name", lower), "RESERVED")
			return
		}
	}
	if _, ok := input.Raw["parent_tag_id"]; ok && input.ParentTagID != nil &&
		!refExists(w, h.repo.Conn(), "tags", "tag", "parent_tag_id", *input.ParentTagID) {
		return
	}

	tag, err := h.repo.Update(id, input)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateTagName) {
			writeError(w, http.StatusConflict, "There is already a tag with that name", "DUPLICATE_NAME")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if tag == nil {
		writeError(w, http.StatusNotFound, "tag not found", "NOT_FOUND")
		return
	}
	h.broker.BroadcastChange("tag_updated", "tag", tag.ID, map[string]any{"tag": tag})
	writeJSON(w, http.StatusOK, tag)
}

func (h *TagHandler) Reorder(w http.ResponseWriter, r *http.Request) {
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
		"type": "reorder", "entity": "tag", "ids": ids,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *TagHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("bulk_change", map[string]interface{}{
		"type": "delete", "entity": "tag", "ids": []string{id},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *TagHandler) GetTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tasks, err := h.repo.GetTasksByTag(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": tasks})
}
