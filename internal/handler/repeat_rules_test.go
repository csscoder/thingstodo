package handler_test

import (
	"net/http"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/recurrence"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func setupRepeatRouter(t *testing.T) *testutil.TestClient {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLog := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLog)
	scheduleRepo := repository.NewScheduleRepository(db, changeLog)
	reminderRepo := repository.NewReminderRepository(db, changeLog)
	settingsRepo := repository.NewUserSettingsRepository(db)
	repeatRepo := repository.NewRepeatRuleRepository(db, changeLog)

	taskH := handler.NewTaskHandler(taskRepo, scheduleRepo, reminderRepo, settingsRepo, broker, nil)
	repeatH := handler.NewRepeatRuleHandler(repeatRepo, taskRepo, recurrence.NewEngine(), broker)

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Put("/api/tasks/{id}/repeat", repeatH.Upsert)

	return testutil.NewTestClient(t, r)
}

func TestRepeatUpsert_Validates(t *testing.T) {
	client := setupRepeatRouter(t)

	resp := client.Post("/api/tasks", map[string]string{"title": "Repeat test task"})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]interface{}
	resp.JSON(t, &created)
	taskID := created["id"].(string)

	cases := []struct {
		name    string
		pattern map[string]interface{}
		field   string
	}{
		{"unknown type", map[string]interface{}{"type": "fortnightly", "every": 1, "mode": "fixed"}, "type"},
		{"every 0", map[string]interface{}{"type": "daily", "every": 0, "mode": "fixed"}, "every"},
		{"unknown mode", map[string]interface{}{"type": "daily", "every": 1, "mode": "whenever"}, "mode"},
		{"weekly on xyz", map[string]interface{}{"type": "weekly", "every": 1, "mode": "fixed", "on": []string{"xyz"}}, "on"},
		{"monthly_dom day 32", map[string]interface{}{"type": "monthly_dom", "every": 1, "mode": "fixed", "day": 32}, "day"},
		{"month 13", map[string]interface{}{"type": "yearly_date", "every": 1, "mode": "fixed", "month": 13, "day": 15}, "month"},
	}
	for _, tc := range cases {
		resp := client.Put("/api/tasks/"+taskID+"/repeat", map[string]interface{}{"pattern": tc.pattern})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected status 400, got %d (body: %s)", tc.name, resp.StatusCode, string(resp.Body))
			continue
		}
		testutil.AssertJSONField(t, resp, "code", "VALIDATION")
		testutil.AssertJSONField(t, resp, "field", tc.field)
	}
}
