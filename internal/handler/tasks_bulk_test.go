package handler_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// setupBulkRouter mounts the bulk endpoint together with the task,
// schedule, container and sync endpoints its tests need, with real
// change-log repositories so /api/sync/pull observes bulk side effects.
func setupBulkRouter(t *testing.T) (*testutil.TestClient, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLog := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLog)
	projectRepo := repository.NewProjectRepository(db, changeLog)
	areaRepo := repository.NewAreaRepository(db, changeLog)
	headingRepo := repository.NewHeadingRepository(db, changeLog)
	scheduleRepo := repository.NewScheduleRepository(db, changeLog)
	reminderRepo := repository.NewReminderRepository(db, changeLog)
	settingsRepo := repository.NewUserSettingsRepository(db)

	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)
	projectH := handler.NewProjectHandler(projectRepo, broker)
	areaH := handler.NewAreaHandler(areaRepo, broker)
	headingH := handler.NewHeadingHandler(headingRepo, broker)
	scheduleH := handler.NewScheduleHandler(scheduleRepo, taskRepo, broker)
	syncH := handler.NewSyncHandler(changeLog, taskRepo, projectRepo, areaRepo, nil, nil, headingRepo, nil, scheduleRepo, reminderRepo, nil, nil, time.UTC, t.TempDir(), broker)

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Get("/api/tasks/{id}", taskH.Get)
	r.Post("/api/tasks/bulk", taskH.BulkAction)
	r.Get("/api/tasks/{id}/schedules", scheduleH.List)
	r.Post("/api/tasks/{id}/schedules", scheduleH.Create)
	r.Post("/api/areas", areaH.Create)
	r.Post("/api/projects", projectH.Create)
	r.Post("/api/projects/{id}/headings", headingH.Create)
	r.Get("/api/sync/pull", syncH.Pull)

	client := testutil.NewTestClient(t, r)
	return client, db
}

func bulkCreateTask(t *testing.T, client *testutil.TestClient, title string) string {
	t.Helper()
	resp := client.Post("/api/tasks", map[string]string{"title": title})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	var m map[string]interface{}
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m["id"].(string)
}

func TestBulkAction_Complete(t *testing.T) {
	client, _ := setupTaskRouter(t)

	id1 := bulkCreateTask(t, client, "Task 1")
	id2 := bulkCreateTask(t, client, "Task 2")

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id1, id2},
		"action":   "complete",
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "affected", float64(2))

	resp1 := client.Get("/api/tasks/" + id1)
	testutil.AssertStatus(t, resp1, http.StatusOK)
	testutil.AssertJSONField(t, resp1, "status", "completed")

	resp2 := client.Get("/api/tasks/" + id2)
	testutil.AssertStatus(t, resp2, http.StatusOK)
	testutil.AssertJSONField(t, resp2, "status", "completed")
}

func TestBulkAction_Delete(t *testing.T) {
	client, _ := setupTaskRouter(t)

	id1 := bulkCreateTask(t, client, "Task A")
	id2 := bulkCreateTask(t, client, "Task B")

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id1, id2},
		"action":   "delete",
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "affected", float64(2))
}

func TestBulkAction_InvalidAction(t *testing.T) {
	client, _ := setupTaskRouter(t)

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{"abc"},
		"action":   "explode",
	})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestBulkAction_EmptyTaskIDs(t *testing.T) {
	client, _ := setupTaskRouter(t)

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{},
		"action":   "complete",
	})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestBulkAction_SetPriority(t *testing.T) {
	client, _ := setupTaskRouter(t)

	id1 := bulkCreateTask(t, client, "Priority Task")

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id1},
		"action":   "set_priority",
		"params":   map[string]interface{}{"priority": 1},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)

	resp1 := client.Get("/api/tasks/" + id1)
	testutil.AssertStatus(t, resp1, http.StatusOK)
	testutil.AssertJSONField(t, resp1, "high_priority", true)
}

// TestBulkComplete_AffectedCountsChangedOnly pins the bulk finishing
// semantics: only tasks that actually transitioned count as affected; an
// already-completed task and an unknown id do not.
func TestBulkComplete_AffectedCountsChangedOnly(t *testing.T) {
	client, _ := setupTaskRouter(t)

	openID := bulkCreateTask(t, client, "Bulk open")
	doneID := bulkCreateTask(t, client, "Bulk done")
	resp := client.Patch("/api/tasks/"+doneID+"/complete", nil)
	testutil.AssertStatus(t, resp, http.StatusOK)

	resp = client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{openID, doneID, "zzzz9999"},
		"action":   "complete",
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "affected", float64(1))

	resp1 := client.Get("/api/tasks/" + openID)
	testutil.AssertJSONField(t, resp1, "status", "completed")
}

// TestBulkSetPriority_HighPriorityParam pins the set_priority params: the
// boolean high_priority (which wins when both are sent) and the legacy
// integer priority where 1 means high.
func TestBulkSetPriority_HighPriorityParam(t *testing.T) {
	client, _ := setupBulkRouter(t)

	setPriority := func(id string, params map[string]interface{}) bool {
		t.Helper()
		resp := client.Post("/api/tasks/bulk", map[string]interface{}{
			"task_ids": []string{id},
			"action":   "set_priority",
			"params":   params,
		})
		testutil.AssertStatus(t, resp, http.StatusOK)
		getResp := client.Get("/api/tasks/" + id)
		testutil.AssertStatus(t, getResp, http.StatusOK)
		var task map[string]interface{}
		getResp.JSON(t, &task)
		return task["high_priority"] == true
	}

	id1 := bulkCreateTask(t, client, "High via high_priority")
	if !setPriority(id1, map[string]interface{}{"high_priority": true}) {
		t.Errorf("high_priority:true did not set the flag")
	}
	id2 := bulkCreateTask(t, client, "Low via high_priority")
	if setPriority(id2, map[string]interface{}{"high_priority": false}) {
		t.Errorf("high_priority:false did not clear the flag")
	}

	id3 := bulkCreateTask(t, client, "High via legacy priority")
	if !setPriority(id3, map[string]interface{}{"priority": 1}) {
		t.Errorf("legacy priority:1 did not set the flag")
	}
	id4 := bulkCreateTask(t, client, "Low via legacy priority")
	if setPriority(id4, map[string]interface{}{"priority": 0}) {
		t.Errorf("legacy priority:0 did not clear the flag")
	}

	id5 := bulkCreateTask(t, client, "Both params bool wins high")
	if !setPriority(id5, map[string]interface{}{"high_priority": true, "priority": 0}) {
		t.Errorf("high_priority must win when both params are sent")
	}
	id6 := bulkCreateTask(t, client, "Both params bool wins low")
	if setPriority(id6, map[string]interface{}{"high_priority": false, "priority": 1}) {
		t.Errorf("high_priority:false must win over legacy priority:1")
	}
}

// TestBulkAffected_IgnoresUnknownIDs pins the non-status affected count:
// one row per task that actually changed; unknown ids contribute zero.
func TestBulkAffected_IgnoresUnknownIDs(t *testing.T) {
	client, _ := setupBulkRouter(t)

	id := bulkCreateTask(t, client, "Real task")

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id, "zzzz9999"},
		"action":   "set_deadline",
		"params":   map[string]interface{}{"deadline": "2026-12-24"},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "affected", float64(1))

	getResp := client.Get("/api/tasks/" + id)
	testutil.AssertJSONField(t, getResp, "deadline", "2026-12-24")
}

// TestBulkSetWhen_EmptyClears pins the clear semantics of bulk set_when:
// when_date "" nulls tasks.when_date and leaves no schedule rows (in
// particular no phantom row with an empty date).
func TestBulkSetWhen_EmptyClears(t *testing.T) {
	client, db := setupBulkRouter(t)

	createResp := client.Post("/api/tasks", map[string]string{"title": "Clears when", "when_date": "2026-11-01"})
	testutil.AssertStatus(t, createResp, http.StatusCreated)
	var created map[string]interface{}
	createResp.JSON(t, &created)
	id := created["id"].(string)

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id},
		"action":   "set_when",
		"params":   map[string]interface{}{"when_date": ""},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)

	getResp := client.Get("/api/tasks/" + id)
	testutil.AssertJSONField(t, getResp, "when_date", nil)

	var scheduleCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM task_schedules WHERE task_id = ?", id).Scan(&scheduleCount); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if scheduleCount != 0 {
		t.Errorf("expected no schedule rows after clearing when_date, got %d", scheduleCount)
	}
}

// TestBulkSetWhen_DateMovesFirstSchedule pins the schedule sync of bulk
// set_when: every task's first schedule entry follows the new date, and
// the change log carries a schedule entry per task for sync clients.
func TestBulkSetWhen_DateMovesFirstSchedule(t *testing.T) {
	client, _ := setupBulkRouter(t)

	createWithWhen := func(title string) string {
		t.Helper()
		resp := client.Post("/api/tasks", map[string]string{"title": title, "when_date": "2026-11-01"})
		testutil.AssertStatus(t, resp, http.StatusCreated)
		var created map[string]interface{}
		resp.JSON(t, &created)
		return created["id"].(string)
	}
	id1 := createWithWhen("Moves A")
	id2 := createWithWhen("Moves B")

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id1, id2},
		"action":   "set_when",
		"params":   map[string]interface{}{"when_date": "2026-11-05"},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "affected", float64(2))

	firstScheduleDate := func(id string) string {
		t.Helper()
		listResp := client.Get("/api/tasks/" + id + "/schedules")
		testutil.AssertStatus(t, listResp, http.StatusOK)
		var list struct {
			Items []struct {
				WhenDate string `json:"when_date"`
			} `json:"items"`
		}
		listResp.JSON(t, &list)
		if len(list.Items) == 0 {
			t.Fatalf("task %s has no schedule entries", id)
		}
		return list.Items[0].WhenDate
	}
	if got := firstScheduleDate(id1); got != "2026-11-05" {
		t.Errorf("task %s first schedule: expected 2026-11-05, got %s", id1, got)
	}
	if got := firstScheduleDate(id2); got != "2026-11-05" {
		t.Errorf("task %s first schedule: expected 2026-11-05, got %s", id2, got)
	}

	pullResp := client.Get("/api/sync/pull?since=0")
	testutil.AssertStatus(t, pullResp, http.StatusOK)
	var pull struct {
		Changes []struct {
			Entity   string `json:"entity"`
			Snapshot string `json:"snapshot"`
		} `json:"changes"`
	}
	pullResp.JSON(t, &pull)
	seen := map[string]bool{}
	for _, c := range pull.Changes {
		if c.Entity != "schedule" {
			continue
		}
		var s struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal([]byte(c.Snapshot), &s); err != nil {
			t.Fatalf("unmarshal schedule snapshot %q: %v", c.Snapshot, err)
		}
		seen[s.TaskID] = true
	}
	if !seen[id1] || !seen[id2] {
		t.Errorf("sync/pull missing schedule entries: id1=%v id2=%v", seen[id1], seen[id2])
	}
}

// TestBulkSetWhen_FailureRollsBack pins the atomicity of bulk set_when:
// when moving one task's first schedule would create a duplicate
// timeless date, the whole action is rejected and no task's when_date or
// schedule rows change.
func TestBulkSetWhen_FailureRollsBack(t *testing.T) {
	client, db := setupBulkRouter(t)

	createWithWhen := func(title string) string {
		t.Helper()
		resp := client.Post("/api/tasks", map[string]string{"title": title, "when_date": "2026-11-01"})
		testutil.AssertStatus(t, resp, http.StatusCreated)
		var created map[string]interface{}
		resp.JSON(t, &created)
		return created["id"].(string)
	}
	id1 := createWithWhen("Rollback A")
	id2 := createWithWhen("Rollback B")

	// id1 already owns a timeless schedule on the target date, so moving
	// its first entry there would be a duplicate.
	extra := client.Post("/api/tasks/"+id1+"/schedules", map[string]interface{}{"when_date": "2026-11-05"})
	testutil.AssertStatus(t, extra, http.StatusCreated)

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{id1, id2},
		"action":   "set_when",
		"params":   map[string]interface{}{"when_date": "2026-11-05"},
	})
	if resp.StatusCode < 300 {
		t.Errorf("expected non-2xx from conflicting bulk set_when, got %d (body: %s)", resp.StatusCode, string(resp.Body))
	}
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")

	for _, id := range []string{id1, id2} {
		getResp := client.Get("/api/tasks/" + id)
		testutil.AssertJSONField(t, getResp, "when_date", "2026-11-01")
	}

	var dates []string
	rows, err := db.Query("SELECT when_date FROM task_schedules WHERE task_id = ? ORDER BY when_date", id1)
	if err != nil {
		t.Fatalf("query schedules: %v", err)
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan schedule date: %v", err)
		}
		dates = append(dates, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schedules: %v", err)
	}
	want := []string{"2026-11-01", "2026-11-05"}
	if len(dates) != len(want) {
		t.Fatalf("task %s schedule dates: expected %v, got %v", id1, want, dates)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("task %s schedule dates: expected %v, got %v", id1, want, dates)
			break
		}
	}

	var count2 int
	if err := db.QueryRow("SELECT COUNT(*) FROM task_schedules WHERE task_id = ?", id2).Scan(&count2); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if count2 != 1 {
		t.Errorf("task %s: expected 1 schedule row after rollback, got %d", id2, count2)
	}
}

// TestBulkMoveProject_SetsAreaClearsHeading pins the D5 container
// invariant on bulk move_project: the moved task's area becomes the new
// project's area and a heading left behind by the move is cleared.
func TestBulkMoveProject_SetsAreaClearsHeading(t *testing.T) {
	client, _ := setupBulkRouter(t)

	post := func(path string, body map[string]interface{}) map[string]interface{} {
		t.Helper()
		resp := client.Post(path, body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s: %d %s", path, resp.StatusCode, string(resp.Body))
		}
		var m map[string]interface{}
		resp.JSON(t, &m)
		return m
	}

	area1 := post("/api/areas", map[string]interface{}{"title": "Area One"})
	project1 := post("/api/projects", map[string]interface{}{"title": "P One", "area_id": area1["id"]})
	heading1 := post("/api/projects/"+project1["id"].(string)+"/headings", map[string]interface{}{"title": "H One"})
	area2 := post("/api/areas", map[string]interface{}{"title": "Area Two"})
	project2 := post("/api/projects", map[string]interface{}{"title": "P Two", "area_id": area2["id"]})

	task := post("/api/tasks", map[string]interface{}{
		"title": "Moved", "project_id": project1["id"], "heading_id": heading1["id"],
	})

	resp := client.Post("/api/tasks/bulk", map[string]interface{}{
		"task_ids": []string{task["id"].(string)},
		"action":   "move_project",
		"params":   map[string]interface{}{"project_id": project2["id"]},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "affected", float64(1))

	getResp := client.Get("/api/tasks/" + task["id"].(string))
	testutil.AssertJSONField(t, getResp, "project_id", project2["id"])
	testutil.AssertJSONField(t, getResp, "area_id", area2["id"])
	testutil.AssertJSONField(t, getResp, "heading_id", nil)
}
