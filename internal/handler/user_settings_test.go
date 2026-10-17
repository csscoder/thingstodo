package handler_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/handler"
	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func withTestUser(userID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), mw.UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func setupSettingsRouter(t *testing.T) *testutil.TestClient {
	t.Helper()
	db := testutil.SetupTestDB(t)
	settingsRepo := repository.NewUserSettingsRepository(db)
	settingsH := handler.NewUserSettingsHandler(settingsRepo)

	r := chi.NewRouter()
	r.Use(withTestUser("user1"))
	r.Get("/api/user/settings", settingsH.Get)
	r.Patch("/api/user/settings", settingsH.Update)

	return testutil.NewTestClient(t, r)
}

func TestSettingsFirstPatchSaved(t *testing.T) {
	client := setupSettingsRouter(t)

	// No GET first: the settings row does not exist yet, so the first
	// PATCH must upsert it instead of silently updating zero rows.
	resp := client.Patch("/api/user/settings", map[string]interface{}{"review_after_days": 3})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "review_after_days", float64(3))

	getResp := client.Get("/api/user/settings")
	testutil.AssertStatus(t, getResp, http.StatusOK)
	testutil.AssertJSONField(t, getResp, "review_after_days", float64(3))
}

func TestSettingsPatch_Validates(t *testing.T) {
	client := setupSettingsRouter(t)

	cases := []struct {
		name  string
		body  map[string]interface{}
		field string
	}{
		{"sort_areas", map[string]interface{}{"sort_areas": "random"}, "sort_areas"},
		{"sort_tags", map[string]interface{}{"sort_tags": "random"}, "sort_tags"},
		{"evening_starts_at 7pm", map[string]interface{}{"evening_starts_at": "7pm"}, "evening_starts_at"},
		{"time_format x", map[string]interface{}{"time_format": "x"}, "time_format"},
		{"font_size 13", map[string]interface{}{"font_size": 13}, "font_size"},
		{"default_reminder_type sms", map[string]interface{}{"default_reminder_type": "sms"}, "default_reminder_type"},
		{"default_reminder_value 100", map[string]interface{}{"default_reminder_value": 100}, "default_reminder_value"},
		{"notification_provider carrier_pigeon", map[string]interface{}{"notification_provider": "carrier_pigeon"}, "notification_provider"},
	}
	for _, tc := range cases {
		resp := client.Patch("/api/user/settings", tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected status 400, got %d (body: %s)", tc.name, resp.StatusCode, string(resp.Body))
			continue
		}
		testutil.AssertJSONField(t, resp, "code", "VALIDATION")
		testutil.AssertJSONField(t, resp, "field", tc.field)
	}

	// Documented values still pass. GET first so the settings row exists
	// (PATCH updates only; the row is created by GetOrCreate on GET).
	getResp := client.Get("/api/user/settings")
	testutil.AssertStatus(t, getResp, http.StatusOK)

	resp := client.Patch("/api/user/settings", map[string]interface{}{
		"time_format": "24h", "font_size": 14, "evening_starts_at": "19:30",
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "time_format", "24h")
}
