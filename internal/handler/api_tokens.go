package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/repository"
)

// APITokenHandler manages named API tokens. Tokens are shown with a short
// prefix only; the plaintext value is returned exactly once, at creation,
// and can never be recovered afterwards.
type APITokenHandler struct {
	repo *repository.APITokenRepository
}

func NewAPITokenHandler(repo *repository.APITokenRepository) *APITokenHandler {
	return &APITokenHandler{repo: repo}
}

// rejectTokenAuth answers the 403 every /api/tokens handler returns when
// the request itself authenticated with an API token: a token must not be
// able to mint or revoke credentials. It reports whether it wrote the
// response.
func rejectTokenAuth(w http.ResponseWriter, r *http.Request) bool {
	if mw.AuthMethod(r.Context()) == mw.AuthToken {
		writeError(w, http.StatusForbidden, "token authentication cannot manage tokens", "FORBIDDEN")
		return true
	}
	return false
}

// List handles GET /api/tokens
func (h *APITokenHandler) List(w http.ResponseWriter, r *http.Request) {
	if rejectTokenAuth(w, r) {
		return
	}
	tokens, err := h.repo.List()
	if err != nil {
		log.Printf("ERROR api_tokens.List: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

// Create handles POST /api/tokens. A body id is accepted for body-shape
// compatibility and ignored: tokens always get a server-generated id and
// token creates are not idempotent.
func (h *APITokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	if rejectTokenAuth(w, r) {
		return
	}
	var input struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		writeFieldError(w, http.StatusBadRequest, "name is required", "VALIDATION", "name")
		return
	}
	id, token, err := h.repo.Create(name)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateTokenName) {
			writeError(w, http.StatusConflict, "There is already a token with that name", "DUPLICATE_NAME")
			return
		}
		log.Printf("ERROR api_tokens.Create: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Prefix string `json:"prefix"`
		Token  string `json:"token"`
	}{ID: id, Name: name, Prefix: repository.TokenPrefix(token), Token: token})
}

// Revoke handles DELETE /api/tokens/{id}. Revoking an already-revoked
// token is a no-op (204).
func (h *APITokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if rejectTokenAuth(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.repo.Revoke(id); err != nil {
		if errors.Is(err, repository.ErrTokenNotFound) {
			writeError(w, http.StatusNotFound, "api token not found", "NOT_FOUND")
			return
		}
		log.Printf("ERROR api_tokens.Revoke id=%s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
