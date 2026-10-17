package handler_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/handler"
	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/push"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func setupTaskRouter(t *testing.T) (*testutil.TestClient, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	taskRepo := repository.NewTaskRepository(db, nil)
	ruleRepo := repository.NewRepeatRuleRepository(db, nil)
	checklistRepo := repository.NewChecklistRepository(db, nil)
	attachRepo := repository.NewAttachmentRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	reminderRepo := repository.NewReminderRepository(db, nil)
	settingsRepo := repository.NewUserSettingsRepository(db)
	userRepo := repository.NewUserRepository(db)
	pushSubRepo := repository.NewPushSubscriptionRepository(db)
	pushSender := push.NewSender(pushSubRepo, "", "", "")
	sched := scheduler.New(db, taskRepo, ruleRepo, checklistRepo, attachRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, nil, pushSender, broker, time.UTC)
	taskHandler := handler.NewTaskHandler(taskRepo, scheduleRepo, reminderRepo, settingsRepo, broker, sched)

	r := chi.NewRouter()
	r.Route("/api/tasks", func(r chi.Router) {
		r.Get("/", taskHandler.List)
		r.Post("/", taskHandler.Create)
		r.Patch("/reorder", taskHandler.Reorder)
		r.Post("/bulk", taskHandler.BulkAction)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", taskHandler.Get)
			r.Patch("/", taskHandler.Update)
			r.Delete("/", taskHandler.Delete)
			r.Patch("/complete", taskHandler.Complete)
			r.Patch("/cancel", taskHandler.Cancel)
			r.Patch("/wontdo", taskHandler.WontDo)
			r.Patch("/reopen", taskHandler.Reopen)
		})
	})

	client := testutil.NewTestClient(t, r)
	return client, db
}

func TestTaskHandlerCreate(t *testing.T) {
	client, _ := setupTaskRouter(t)

	resp := client.Post("/api/tasks", map[string]string{"title": "Buy groceries"})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	testutil.AssertJSONField(t, resp, "title", "Buy groceries")
	testutil.AssertJSONField(t, resp, "status", "open")
}

func TestTaskHandlerCreateMissingTitle(t *testing.T) {
	client, _ := setupTaskRouter(t)

	resp := client.Post("/api/tasks", map[string]string{})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
}

func TestTaskHandlerCreateInvalidJSON(t *testing.T) {
	client, _ := setupTaskRouter(t)

	// Send raw request with invalid body
	resp := client.Post("/api/tasks", "not json")
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestTaskHandlerGet(t *testing.T) {
	client, _ := setupTaskRouter(t)

	// Create a task first
	createResp := client.Post("/api/tasks", map[string]string{"title": "Test task"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Get("/api/tasks/" + id)
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "title", "Test task")
}

func TestTaskHandlerGetNotFound(t *testing.T) {
	client, _ := setupTaskRouter(t)

	resp := client.Get("/api/tasks/nonexistent")
	testutil.AssertStatus(t, resp, http.StatusNotFound)
	testutil.AssertJSONField(t, resp, "code", "NOT_FOUND")
}

func TestTaskHandlerUpdate(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "Original"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Patch("/api/tasks/"+id, map[string]string{"title": "Updated"})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "title", "Updated")
}

func TestTaskHandlerDelete(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "To delete"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Delete("/api/tasks/" + id)
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	// Soft-deleted task is still retrievable by direct GET (for trash view)
	getResp := client.Get("/api/tasks/" + id)
	testutil.AssertStatus(t, getResp, http.StatusOK)

	// But it should not appear in list
	listResp := client.Get("/api/tasks")
	var listBody struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	listResp.JSON(t, &listBody)
	for _, task := range listBody.Tasks {
		if task["id"] == id {
			t.Error("soft-deleted task should not appear in task list")
		}
	}
}

func TestTaskHandlerComplete(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "To complete"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Patch("/api/tasks/"+id+"/complete", nil)
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "status", "completed")
}

func TestTaskHandlerCancel(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "To cancel"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Patch("/api/tasks/"+id+"/cancel", nil)
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "status", "canceled")
}

func TestTaskHandlerWontDo(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "Won't do"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Patch("/api/tasks/"+id+"/wontdo", nil)
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "status", "wont_do")
}

func TestTaskHandlerReopen(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "Reopen me"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	client.Patch("/api/tasks/"+id+"/complete", nil)
	resp := client.Patch("/api/tasks/"+id+"/reopen", nil)
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "status", "open")
}

func TestTaskHandlerList(t *testing.T) {
	client, _ := setupTaskRouter(t)

	client.Post("/api/tasks", map[string]string{"title": "Task 1"})
	client.Post("/api/tasks", map[string]string{"title": "Task 2"})

	resp := client.Get("/api/tasks")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var body map[string]interface{}
	resp.JSON(t, &body)
	tasks := body["tasks"].([]interface{})
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestTaskHandlerListFilterByStatus(t *testing.T) {
	client, _ := setupTaskRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "Task 1"})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	client.Post("/api/tasks", map[string]string{"title": "Task 2"})
	client.Patch("/api/tasks/"+created["id"].(string)+"/complete", nil)

	resp := client.Get("/api/tasks?status=open")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var body map[string]interface{}
	resp.JSON(t, &body)
	tasks := body["tasks"].([]interface{})
	if len(tasks) != 1 {
		t.Errorf("expected 1 open task, got %d", len(tasks))
	}
}

func TestTaskHandlerListEmpty(t *testing.T) {
	client, _ := setupTaskRouter(t)

	resp := client.Get("/api/tasks")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var body map[string]interface{}
	resp.JSON(t, &body)
	tasks := body["tasks"].([]interface{})
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestTaskHandlerReorder(t *testing.T) {
	client, _ := setupTaskRouter(t)

	r1 := client.Post("/api/tasks", map[string]string{"title": "Task 1"})
	r2 := client.Post("/api/tasks", map[string]string{"title": "Task 2"})

	var t1, t2 map[string]interface{}
	r1.JSON(t, &t1)
	r2.JSON(t, &t2)

	resp := client.Patch("/api/tasks/reorder", map[string]interface{}{
		"items": []map[string]interface{}{
			{"id": t1["id"], "sort_field": "sort_order_today", "sort_order": 200},
			{"id": t2["id"], "sort_field": "sort_order_today", "sort_order": 100},
		},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
}

// setupTaskRouterWithUser is setupTaskRouter plus a user injected into the
// request context, real change-log repositories, and a user_settings row, for
// tests that exercise the default reminder and change-log behavior.
func setupTaskRouterWithUser(t *testing.T) (*testutil.TestClient, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	changeLog := repository.NewChangeLogRepository(db)
	broker := sse.NewBroker()
	taskRepo := repository.NewTaskRepository(db, changeLog)
	ruleRepo := repository.NewRepeatRuleRepository(db, changeLog)
	checklistRepo := repository.NewChecklistRepository(db, changeLog)
	attachRepo := repository.NewAttachmentRepository(db, changeLog)
	scheduleRepo := repository.NewScheduleRepository(db, changeLog)
	reminderRepo := repository.NewReminderRepository(db, changeLog)
	settingsRepo := repository.NewUserSettingsRepository(db)
	userRepo := repository.NewUserRepository(db)
	pushSubRepo := repository.NewPushSubscriptionRepository(db)
	pushSender := push.NewSender(pushSubRepo, "", "", "")
	sched := scheduler.New(db, taskRepo, ruleRepo, checklistRepo, attachRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, nil, pushSender, broker, time.UTC)
	taskHandler := handler.NewTaskHandler(taskRepo, scheduleRepo, reminderRepo, settingsRepo, broker, sched)

	if _, err := db.Exec("INSERT INTO users (id, username, password_hash) VALUES ('u1', 'alice', 'x')"); err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	if _, err := db.Exec("INSERT INTO user_settings (user_id) VALUES ('u1')"); err != nil {
		t.Fatalf("insert test user settings: %v", err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), mw.UserIDKey, "u1")
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/tasks", func(r chi.Router) {
		r.Get("/", taskHandler.List)
		r.Post("/", taskHandler.Create)
		r.Patch("/reorder", taskHandler.Reorder)
		r.Post("/bulk", taskHandler.BulkAction)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", taskHandler.Get)
			r.Patch("/", taskHandler.Update)
			r.Delete("/", taskHandler.Delete)
			r.Patch("/complete", taskHandler.Complete)
			r.Patch("/cancel", taskHandler.Cancel)
			r.Patch("/wontdo", taskHandler.WontDo)
			r.Patch("/reopen", taskHandler.Reopen)
		})
	})

	client := testutil.NewTestClient(t, r)
	return client, db
}

func assertCount(t *testing.T, db *sql.DB, want int, query string) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if got != want {
		t.Errorf("query %q: expected %d rows, got %d", query, want, got)
	}
}

func TestTaskCreate_BadTagWritesNothing(t *testing.T) {
	client, db := setupTaskRouterWithUser(t)

	resp := client.Post("/api/tasks", map[string]interface{}{"title": "x", "tag_ids": []string{"nope0000"}})
	if resp.StatusCode < 300 {
		t.Errorf("expected non-2xx status, got %d\nbody: %s", resp.StatusCode, resp.Body)
	}
	assertCount(t, db, 0, "SELECT COUNT(*) FROM tasks")
	assertCount(t, db, 0, "SELECT COUNT(*) FROM change_log WHERE entity = 'task'")
}

func TestTaskCreate_DuplicateTimelessScheduleRollsBack(t *testing.T) {
	client, db := setupTaskRouterWithUser(t)

	date := "2026-02-01"
	other := "2026-02-05"
	createResp := client.Post("/api/tasks", map[string]interface{}{"title": "dup", "when_date": date})
	testutil.AssertStatus(t, createResp, http.StatusCreated)
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	// A second timeless schedule entry on another date.
	if _, err := db.Exec(
		"INSERT INTO task_schedules (id, task_id, when_date, sort_order) VALUES ('schedB', ?, ?, 2048)", id, other); err != nil {
		t.Fatalf("insert schedule entry: %v", err)
	}

	// Moving the task onto the other entry's date must fail...
	resp := client.Patch("/api/tasks/"+id, map[string]interface{}{"when_date": other})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")

	// ...and leave tasks.when_date unchanged (audit B18: the row update used
	// to survive the failed schedule sync).
	var when sql.NullString
	if err := db.QueryRow("SELECT when_date FROM tasks WHERE id = ?", id).Scan(&when); err != nil {
		t.Fatalf("query task: %v", err)
	}
	if !when.Valid || when.String != date {
		t.Errorf("tasks.when_date must stay %q after the failed update, got %v", date, when)
	}
}

func TestTaskCreate_DefaultReminderFailureRollsBack(t *testing.T) {
	client, db := setupTaskRouterWithUser(t)

	// Force the reminder insert to fail: the reminders.type CHECK rejects
	// this value.
	if _, err := db.Exec("UPDATE user_settings SET default_reminder_type = 'bogus' WHERE user_id = 'u1'"); err != nil {
		t.Fatalf("set default reminder type: %v", err)
	}

	resp := client.Post("/api/tasks", map[string]string{"title": "remind me"})
	testutil.AssertStatus(t, resp, http.StatusInternalServerError)
	testutil.AssertJSONField(t, resp, "code", "INTERNAL")

	assertCount(t, db, 0, "SELECT COUNT(*) FROM tasks")
	assertCount(t, db, 0, "SELECT COUNT(*) FROM task_schedules")
	assertCount(t, db, 0, "SELECT COUNT(*) FROM change_log WHERE entity = 'task'")
	assertCount(t, db, 0, "SELECT COUNT(*) FROM change_log WHERE entity = 'reminder'")
}

func TestTaskCreate_WithDefaultReminderSucceeds(t *testing.T) {
	client, db := setupTaskRouterWithUser(t)

	if _, err := db.Exec("UPDATE user_settings SET default_reminder_type = 'at_start' WHERE user_id = 'u1'"); err != nil {
		t.Fatalf("set default reminder type: %v", err)
	}

	resp := client.Post("/api/tasks", map[string]string{"title": "remind me"})
	testutil.AssertStatus(t, resp, http.StatusCreated)

	var created struct {
		ID        string `json:"id"`
		Reminders []struct {
			Type string `json:"type"`
		} `json:"reminders"`
	}
	resp.JSON(t, &created)
	if len(created.Reminders) != 1 {
		t.Fatalf("expected 1 reminder on the created task, got %d\nbody: %s", len(created.Reminders), resp.Body)
	}
	if created.Reminders[0].Type != "at_start" {
		t.Errorf("expected reminder type %q, got %q", "at_start", created.Reminders[0].Type)
	}
	assertCount(t, db, 1, "SELECT COUNT(*) FROM reminders")
}
