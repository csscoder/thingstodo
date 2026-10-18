package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/frontend"
	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/mcpserver"
	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/push"
	"github.com/collinjanssen/thingstodo/internal/recurrence"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/go-chi/chi/v5"
)

func New(db *sql.DB, cfg config.Config, broker *sse.Broker, sched *scheduler.Scheduler) http.Handler {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	r := chi.NewRouter()
	r.Use(mw.Recover)
	r.Use(mw.Logger)
	r.Use(mw.BodyLimit(mw.MaxJSONBody))

	// Repositories
	changeLogRepo := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLogRepo).WithLocation(cfg.Location)
	projectRepo := repository.NewProjectRepository(db, changeLogRepo)
	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	tagRepo := repository.NewTagRepository(db, changeLogRepo)
	headingRepo := repository.NewHeadingRepository(db, changeLogRepo)
	checklistRepo := repository.NewChecklistRepository(db, changeLogRepo)
	attachmentRepo := repository.NewAttachmentRepository(db, changeLogRepo)
	repeatRuleRepo := repository.NewRepeatRuleRepository(db, changeLogRepo)
	searchRepo := repository.NewSearchRepository(db)
	viewRepo := repository.NewViewRepository(db, cfg.Location)
	userRepo := repository.NewUserRepository(db)
	settingsRepo := repository.NewUserSettingsRepository(db)
	savedFilterRepo := repository.NewSavedFilterRepository(db)
	scheduleRepo := repository.NewScheduleRepository(db, changeLogRepo).WithTasks(taskRepo)
	reminderRepo := repository.NewReminderRepository(db, changeLogRepo)
	tokenRepo := repository.NewAPITokenRepository(db)

	// Single-entity change events carry the latest change-log seq, read at
	// broadcast time, so clients can gauge the feed position.
	broker.SetSeqSource(changeLogRepo.GetLatestSeq)
	pushSubRepo := repository.NewPushSubscriptionRepository(db)

	// Handlers
	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, sched)
	projectH := handler.NewProjectHandler(projectRepo, broker)
	areaH := handler.NewAreaHandler(areaRepo, broker)
	tagH := handler.NewTagHandler(tagRepo, broker)
	headingH := handler.NewHeadingHandler(headingRepo, broker)
	checklistH := handler.NewChecklistHandler(checklistRepo, broker)
	attachmentH := handler.NewAttachmentHandler(attachmentRepo, broker, cfg.AttachmentsPath, cfg.MaxUploadSize)
	repeatRuleH := handler.NewRepeatRuleHandler(repeatRuleRepo, taskRepo, recurrence.NewEngine(), broker)
	searchH := handler.NewSearchHandler(searchRepo)
	viewH := handler.NewViewHandler(viewRepo, settingsRepo)
	authH := handler.NewAuthHandler(userRepo, cfg)
	settingsH := handler.NewUserSettingsHandler(settingsRepo)
	savedFilterH := handler.NewSavedFilterHandler(savedFilterRepo, broker)
	scheduleH := handler.NewScheduleHandler(scheduleRepo, taskRepo, broker)
	reminderH := handler.NewReminderHandler(reminderRepo, broker, cfg.Location)
	tokenH := handler.NewAPITokenHandler(tokenRepo)
	pushSender := push.NewSender(pushSubRepo, cfg.VAPIDPrivateKey, cfg.VAPIDPublicKey, cfg.VAPIDContact)
	ntfySender := push.NewNtfySender(settingsRepo, userRepo)
	notifier := push.NewDispatcher(pushSender, ntfySender, settingsRepo, userRepo)
	pushSubH := handler.NewPushSubscriptionHandler(pushSubRepo, cfg, notifier)
	syncH := handler.NewSyncHandler(changeLogRepo, taskRepo, projectRepo, areaRepo, tagRepo, checklistRepo, headingRepo, attachmentRepo, scheduleRepo, reminderRepo, repeatRuleRepo, sched, cfg.Location, cfg.AttachmentsPath, broker)
	eventH := handler.NewEventHandler(broker)

	var oidcH *handler.OIDCHandler
	if cfg.AuthMode == "oidc" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var err error
		oidcH, err = handler.NewOIDCHandler(ctx, userRepo, cfg)
		if err != nil {
			log.Fatalf("OIDC initialization failed: %v", err)
		}
	}

	// Auth middleware shared by the /api and /mcp route groups: the same
	// tokens and sessions authenticate MCP clients (spec B) and REST
	// clients alike.
	authMW := mw.Auth(cfg, func() (string, error) {
		u, err := userRepo.GetFirst()
		if err != nil {
			return "", err
		}
		if u == nil {
			return "", nil
		}
		return u.ID, nil
	}, func(id string) (bool, error) {
		u, err := userRepo.GetByID(id)
		return u != nil, err
	}, func(token string) (string, bool) {
		id, ok, err := tokenRepo.Lookup(token)
		if err != nil {
			log.Printf("ERROR api token lookup: %v", err)
			return "", false
		}
		return id, ok
	}, tokenRepo.TouchLastUsed)

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// API routes
	r.Route("/api", func(r chi.Router) {
		// CORS (spec A2), mounted before the auth group so preflight
		// OPTIONS requests are answered with 204 without a token. Off
		// in none mode, where any wildcard policy would expose the
		// unauthenticated API to every website.
		r.Use(mw.CORS(cfg.AuthMode != "none"))

		// Unknown /api routes and wrong methods return the JSON error
		// envelope instead of chi's plain-text defaults.
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			mw.WriteError(w, http.StatusNotFound, "not found", "NOT_FOUND", "")
		})
		r.Use(methodNotAllowedJSON)

		// Auth endpoints (no middleware)
		r.Post("/auth/login", authH.Login)
		r.Delete("/auth/logout", authH.Logout)
		r.Get("/auth/config", authH.AuthConfig)
		if oidcH != nil {
			r.Get("/auth/oidc/login", oidcH.Login)
			r.Get("/auth/oidc/callback", oidcH.Callback)
		}

		// Protected routes
		r.Group(func(r chi.Router) {
			r.Use(authMW)

			// SSE events
			r.Get("/events", eventH.Stream)

			// Auth
			r.Get("/auth/me", authH.Me)

			// API tokens
			r.Get("/tokens", tokenH.List)
			r.Post("/tokens", tokenH.Create)
			r.Delete("/tokens/{id}", tokenH.Revoke)

			// Tasks
			r.Get("/tasks", taskH.List)
			r.Post("/tasks", taskH.Create)
			r.Get("/tasks/{id}", taskH.Get)
			r.Patch("/tasks/{id}", taskH.Update)
			r.Delete("/tasks/{id}", taskH.Delete)
			r.Delete("/tasks/{id}/purge", taskH.Purge)
			r.Patch("/tasks/{id}/complete", taskH.Complete)
			r.Patch("/tasks/{id}/cancel", taskH.Cancel)
			r.Patch("/tasks/{id}/wontdo", taskH.WontDo)
			r.Patch("/tasks/{id}/reopen", taskH.Reopen)
			r.Patch("/tasks/{id}/restore", taskH.Restore)
			r.Patch("/tasks/{id}/review", taskH.Review)
			r.Patch("/tasks/{id}/move", taskH.Move)
			r.Patch("/tasks/reorder", taskH.Reorder)
			r.Post("/tasks/bulk", taskH.BulkAction)

			// Checklist
			r.Get("/tasks/{id}/checklist", checklistH.List)
			r.Post("/tasks/{id}/checklist", checklistH.Create)
			r.Patch("/checklist/{id}", checklistH.Update)
			r.Delete("/checklist/{id}", checklistH.Delete)

			// Attachments
			r.Get("/tasks/{id}/attachments", attachmentH.List)
			r.Post("/tasks/{id}/attachments", attachmentH.Create)
			r.Patch("/attachments/{id}", attachmentH.Update)
			r.Delete("/attachments/{id}", attachmentH.Delete)
			r.Get("/attachments/{id}/file", attachmentH.Download)

			// Repeat rules
			r.Get("/tasks/{id}/repeat", repeatRuleH.Get)
			r.Put("/tasks/{id}/repeat", repeatRuleH.Upsert)
			r.Delete("/tasks/{id}/repeat", repeatRuleH.Delete)

			// Schedules
			r.Get("/tasks/{id}/schedules", scheduleH.List)
			r.Post("/tasks/{id}/schedules", scheduleH.Create)
			r.Patch("/tasks/{id}/schedules/reorder", scheduleH.Reorder)
			r.Patch("/schedules/{id}", scheduleH.Update)
			r.Delete("/schedules/{id}", scheduleH.Delete)

			// Reminders
			r.Get("/tasks/{id}/reminders", reminderH.List)
			r.Post("/tasks/{id}/reminders", reminderH.Create)
			r.Delete("/reminders/{id}", reminderH.Delete)

			// Push subscriptions
			r.Post("/push/subscribe", pushSubH.Subscribe)
			r.Delete("/push/subscribe", pushSubH.Unsubscribe)
			r.Get("/push/vapid-key", pushSubH.VAPIDKey)
			r.Post("/push/test", pushSubH.TestNotification)

			// Projects
			r.Get("/projects", projectH.List)
			r.Post("/projects", projectH.Create)
			r.Get("/projects/{id}", projectH.Get)
			r.Patch("/projects/{id}", projectH.Update)
			r.Delete("/projects/{id}", projectH.Delete)
			r.Patch("/projects/{id}/complete", projectH.Complete)
			r.Patch("/projects/reorder", projectH.Reorder)

			// Headings
			r.Get("/projects/{id}/headings", headingH.List)
			r.Post("/projects/{id}/headings", headingH.Create)
			r.Patch("/headings/{id}", headingH.Update)
			r.Delete("/headings/{id}", headingH.Delete)
			r.Patch("/headings/reorder", headingH.Reorder)

			// Areas
			r.Get("/areas", areaH.List)
			r.Post("/areas", areaH.Create)
			r.Get("/areas/{id}", areaH.Get)
			r.Patch("/areas/{id}", areaH.Update)
			r.Delete("/areas/{id}", areaH.Delete)
			r.Patch("/areas/reorder", areaH.Reorder)

			// Tags
			r.Get("/tags", tagH.List)
			r.Post("/tags", tagH.Create)
			r.Patch("/tags/{id}", tagH.Update)
			r.Delete("/tags/{id}", tagH.Delete)
			r.Get("/tags/{id}/tasks", tagH.GetTasks)
			r.Patch("/tags/reorder", tagH.Reorder)

			// Views
			r.Get("/views/inbox", viewH.Inbox)
			r.Get("/views/today", viewH.Today)
			r.Get("/views/upcoming", viewH.Upcoming)
			r.Get("/views/anytime", viewH.Anytime)
			r.Get("/views/someday", viewH.Someday)
			r.Get("/views/logbook", viewH.Logbook)
			r.Get("/views/trash", viewH.Trash)
			r.Get("/views/counts", viewH.Counts)

			// User Settings
			r.Get("/user/settings", settingsH.Get)
			r.Patch("/user/settings", settingsH.Update)

			// Saved Filters
			r.Get("/saved-filters", savedFilterH.List)
			r.Post("/saved-filters", savedFilterH.Create)
			r.Delete("/saved-filters/{id}", savedFilterH.Delete)

			// Search
			r.Get("/search", searchH.Search)

			// Sync
			r.Get("/sync/pull", syncH.Pull)
			r.Post("/sync/push", syncH.Push)
			r.Get("/sync/full", syncH.Full)
		})
	})

	// /mcp serves the MCP server (spec B) over Streamable HTTP, stateless
	// per MCP revision 2026-07-28. Its CORS policy (A2) is the same as
	// /api's, so agent and widget clients face one cross-origin policy;
	// CORS answers preflights before auth, so cross-origin clients can
	// negotiate. Every tool call is dispatched in-process to this same
	// router, carrying the caller's credentials. Only POST (and GET,
	// which the stateless handler answers with 405) are routed: anything
	// else falls through to chi's 404 instead of reaching the SDK.
	root := r
	r.Route("/mcp", func(r chi.Router) {
		r.Use(mw.CORS(cfg.AuthMode != "none"))
		r.Group(func(r chi.Router) {
			r.Use(authMW)
			mcpHandler := mcpserver.New(root, cfg.AuthProxyHeader)
			r.Method(http.MethodPost, "/*", mcpHandler)
			r.Method(http.MethodGet, "/*", mcpHandler)
		})
		// Methods the MCP handler does not serve keep the JSON 404 the
		// Task 18 placeholder answered for every request (and which the
		// router tests pin): no CORS-off preflight handling, no fall
		// through to the SPA, no 405 with an Allow header.
		mcpNotFound := func(w http.ResponseWriter, _ *http.Request) {
			mw.WriteError(w, http.StatusNotFound, "not found", "NOT_FOUND", "")
		}
		r.NotFound(mcpNotFound)
		r.MethodNotAllowed(mcpNotFound)
	})

	// Serve embedded frontend static files with SPA fallback
	staticFS, err := fs.Sub(frontend.StaticFiles, "dist")
	if err != nil {
		panic("failed to create sub filesystem for frontend: " + err.Error())
	}
	indexHTML, _ := fs.ReadFile(staticFS, "index.html")

	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Try to open the requested file
		path := req.URL.Path[1:] // strip leading /
		if path == "" {
			path = "index.html"
		}

		// Set Cache-Control headers based on file type
		// Hashed assets are immutable; everything else must revalidate
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}

		if f, err := staticFS.Open(path); err == nil {
			_ = f.Close()
			http.FileServer(http.FS(staticFS)).ServeHTTP(w, req)
			return
		}
		// SPA fallback: serve index.html for all other routes
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(indexHTML)
	}))

	return r
}

// methodNotAllowedJSON keeps chi's default 405 responder — the only way to
// preserve the Allow header chi computes from the route table (chi v5 drops
// that header when a custom MethodNotAllowed handler is installed) — and
// swaps its empty body for the standard JSON error envelope. It applies to
// the /api sub-router only, leaving the SPA fallback untouched.
func methodNotAllowedJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&methodNotAllowedBody{ResponseWriter: w}, r)
	})
}

// methodNotAllowedBody replaces the body of chi's default 405 response with
// the JSON error envelope. Flush is forwarded so SSE streaming on
// GET /api/events keeps working through the wrapper.
type methodNotAllowedBody struct {
	http.ResponseWriter
	wrote bool
}

func (w *methodNotAllowedBody) WriteHeader(status int) {
	if status == http.StatusMethodNotAllowed && !w.wrote {
		w.wrote = true
		mw.WriteError(w.ResponseWriter, status, "method not allowed", "METHOD_NOT_ALLOWED", "")
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *methodNotAllowedBody) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
