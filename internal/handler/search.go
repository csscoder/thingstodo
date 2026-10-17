package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/collinjanssen/thingstodo/internal/repository"
)

type SearchHandler struct {
	repo *repository.SearchRepository
}

func NewSearchHandler(repo *repository.SearchRepository) *SearchHandler {
	return &SearchHandler{repo: repo}
}

// Search serves GET /api/search (D4): prefix FTS over quoted tokens, a
// page of at most limit results ordered by rank then id, and next_cursor
// when more rows exist. The response keeps its results key.
func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required", "VALIDATION")
		return
	}
	limit := 20
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 500 {
			writeFieldError(w, http.StatusBadRequest, "limit must be between 1 and 500", "VALIDATION", "limit")
			return
		}
		limit = n
	}

	results, nextCursor, err := h.repo.Search(query, limit, q.Get("cursor"))
	if err != nil {
		if errors.Is(err, repository.ErrInvalidCursor) {
			writeFieldError(w, http.StatusBadRequest, "invalid cursor", "VALIDATION", "cursor")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	resp := map[string]interface{}{"results": results}
	if nextCursor != "" {
		resp["next_cursor"] = nextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}
