package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/go-chi/chi/v5"
)

type AttachmentHandler struct {
	repo            *repository.AttachmentRepository
	broker          *sse.Broker
	attachmentsPath string
	maxUploadSize   int64
}

func NewAttachmentHandler(repo *repository.AttachmentRepository, broker *sse.Broker, attachmentsPath string, maxUploadSize int64) *AttachmentHandler {
	return &AttachmentHandler{
		repo:            repo,
		broker:          broker,
		attachmentsPath: attachmentsPath,
		maxUploadSize:   maxUploadSize,
	}
}

func (h *AttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "id")
	items, err := h.repo.ListByTask(taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"attachments": items})
}

// normalizeLinkInput validates attachment creation over JSON/sync. Only link
// attachments may be created that way; files are uploaded via multipart. It
// defaults an absent type to "link", trims the URL, and rejects URLs whose
// scheme could execute scripts or address local files.
func normalizeLinkInput(in *model.CreateAttachmentInput) error {
	if in.Type == "" {
		in.Type = "link"
	}
	if in.Type != "link" {
		return errors.New("only link attachments can be created via JSON")
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(in.URL)
	if err != nil {
		return errors.New("invalid url")
	}
	switch strings.ToLower(u.Scheme) {
	case "javascript", "data", "vbscript", "file":
		return errors.New("url scheme not allowed")
	}
	return nil
}

func (h *AttachmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "id")

	contentType := r.Header.Get("Content-Type")
	if len(contentType) >= 19 && contentType[:19] == "multipart/form-data" {
		h.createFile(w, r, taskID)
		return
	}

	// JSON link attachment. This route is exempt from the router-level
	// BodyLimit (multipart uploads may exceed MaxJSONBody), so cap the JSON
	// branch here itself.
	r.Body = http.MaxBytesReader(w, r.Body, mw.MaxJSONBody)
	var input model.CreateAttachmentInput
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := normalizeLinkInput(&input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "VALIDATION")
		return
	}
	att, err := h.repo.Create(taskID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": taskID})
	writeJSON(w, http.StatusCreated, att)
}

func (h *AttachmentHandler) createFile(w http.ResponseWriter, r *http.Request, taskID string) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadSize)
	if err := r.ParseMultipartForm(h.maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large", "FILE_TOO_LARGE")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field required", "BAD_REQUEST")
		return
	}
	defer file.Close()

	// Generate unique filename
	fileID := model.NewID()
	ext := filepath.Ext(header.Filename)
	storedName := fileID + ext
	storedPath := filepath.Join(h.attachmentsPath, storedName)

	if err := os.MkdirAll(h.attachmentsPath, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create attachments dir", "INTERNAL")
		return
	}

	dst, err := os.Create(storedPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot save file", "INTERNAL")
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot write file", "INTERNAL")
		return
	}

	input := model.CreateAttachmentInput{
		Type:     "file",
		Title:    header.Filename,
		URL:      storedName,
		MimeType: header.Header.Get("Content-Type"),
		FileSize: written,
	}

	att, err := h.repo.Create(taskID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	h.broker.BroadcastJSON("task_updated", map[string]interface{}{"id": taskID})
	writeJSON(w, http.StatusCreated, att)
}

func (h *AttachmentHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var input model.UpdateAttachmentInput
	if err := decodeJSON(r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	att, err := h.repo.Update(id, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if att == nil {
		writeError(w, http.StatusNotFound, "attachment not found", "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, att)
}

func (h *AttachmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Get attachment to find file path before deleting
	att, err := h.repo.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}

	if att != nil && att.Type == "file" {
		// Delete all attachment rows sharing this file, then remove the file from disk
		if err := h.repo.DeleteByURL(att.URL); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return
		}
		// os.Root confines removal to the attachments dir; a removal error
		// (e.g. a legacy row pointing outside it) is ignored, as before.
		if root, err := os.OpenRoot(h.attachmentsPath); err == nil {
			_ = root.Remove(att.URL)
			root.Close()
		}
	} else {
		if err := h.repo.Delete(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AttachmentHandler) Download(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	att, err := h.repo.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return
	}
	if att == nil {
		writeError(w, http.StatusNotFound, "attachment not found", "NOT_FOUND")
		return
	}
	if att.Type != "file" {
		writeError(w, http.StatusBadRequest, "not a file attachment", "BAD_REQUEST")
		return
	}

	// os.Root confines access to the attachments dir, blocking traversal via
	// stored URLs like "../secret.db".
	root, err := os.OpenRoot(h.attachmentsPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found on disk", "NOT_FOUND")
		return
	}
	defer root.Close()

	f, err := root.Open(att.URL)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found on disk", "NOT_FOUND")
		return
	}
	defer f.Close()

	if att.MimeType != "" {
		w.Header().Set("Content-Type", att.MimeType)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, att.Title))
	_, _ = io.Copy(w, f)
}
