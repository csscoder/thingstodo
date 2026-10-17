package database_test

import (
	"os"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func TestSchemaTablesExist(t *testing.T) {
	db := testutil.SetupTestDB(t)

	expectedTables := []string{
		"areas",
		"projects",
		"headings",
		"tasks",
		"checklist_items",
		"attachments",
		"tags",
		"task_tags",
		"project_tags",
		"repeat_rules",
		"users",
		"_migrations",
	}

	for _, table := range expectedTables {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestSchemaFTSExists(t *testing.T) {
	db := testutil.SetupTestDB(t)

	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='tasks_fts'",
	).Scan(&name)
	if err != nil {
		t.Errorf("FTS table tasks_fts not found: %v", err)
	}
}

func TestSchemaIndexesExist(t *testing.T) {
	db := testutil.SetupTestDB(t)

	expectedIndexes := []string{
		"idx_tasks_status",
		"idx_tasks_when_date",
		"idx_tasks_deadline",
		"idx_tasks_project_id",
		"idx_tasks_area_id",
		"idx_tasks_heading_id",
		"idx_projects_area_id",
		"idx_projects_status",
		"idx_checklist_items_task_id",
		"idx_attachments_task_id",
		"idx_headings_project_id",
	}

	for _, idx := range expectedIndexes {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
		).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

func TestSchemaForeignKeyConstraints(t *testing.T) {
	db := testutil.SetupTestDB(t)

	// Inserting a task with a non-existent project_id should fail.
	_, err := db.Exec(`INSERT INTO tasks (id, title, project_id) VALUES ('t1', 'Test', 'nonexistent')`)
	if err == nil {
		t.Error("expected foreign key constraint violation for tasks.project_id")
	}

	// Inserting a heading with a non-existent project_id should fail.
	_, err = db.Exec(`INSERT INTO headings (id, title, project_id) VALUES ('h1', 'Test', 'nonexistent')`)
	if err == nil {
		t.Error("expected foreign key constraint violation for headings.project_id")
	}
}

func TestSchemaStatusCheckConstraints(t *testing.T) {
	db := testutil.SetupTestDB(t)

	// Invalid task status should be rejected.
	_, err := db.Exec(`INSERT INTO tasks (id, title, status) VALUES ('t1', 'Test', 'invalid')`)
	if err == nil {
		t.Error("expected check constraint violation for invalid task status")
	}

	// Invalid project status should be rejected.
	_, _ = db.Exec(`INSERT INTO areas (id, title) VALUES ('a_chk', 'Check Area')`)
	_, err = db.Exec(`INSERT INTO projects (id, title, area_id, status) VALUES ('p1', 'Test', 'a_chk', 'invalid')`)
	if err == nil {
		t.Error("expected check constraint violation for invalid project status")
	}
}

func TestSchemaCascadeDelete(t *testing.T) {
	db := testutil.SetupTestDB(t)

	// Create an area, then a project and a heading within it.
	_, err := db.Exec(`INSERT INTO areas (id, title) VALUES ('a1', 'Area 1')`)
	if err != nil {
		t.Fatalf("failed to insert area: %v", err)
	}
	_, err = db.Exec(`INSERT INTO projects (id, title, area_id) VALUES ('p1', 'Project 1', 'a1')`)
	if err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	_, err = db.Exec(`INSERT INTO headings (id, title, project_id) VALUES ('h1', 'Heading 1', 'p1')`)
	if err != nil {
		t.Fatalf("failed to insert heading: %v", err)
	}

	// Delete the project -- heading should cascade delete.
	_, err = db.Exec(`DELETE FROM projects WHERE id = 'p1'`)
	if err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM headings WHERE id = 'h1'`).Scan(&count)
	if count != 0 {
		t.Error("expected heading to be cascade-deleted with project")
	}
}

func TestSchemaTaskCascadeDelete(t *testing.T) {
	db := testutil.SetupTestDB(t)

	// Create a task with checklist items and attachments.
	_, err := db.Exec(`INSERT INTO tasks (id, title) VALUES ('t1', 'Task 1')`)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}
	_, err = db.Exec(`INSERT INTO checklist_items (id, task_id, title) VALUES ('c1', 't1', 'Item 1')`)
	if err != nil {
		t.Fatalf("failed to insert checklist item: %v", err)
	}
	_, err = db.Exec(`INSERT INTO attachments (id, task_id, type, url) VALUES ('a1', 't1', 'link', 'https://example.com')`)
	if err != nil {
		t.Fatalf("failed to insert attachment: %v", err)
	}
	_, err = db.Exec(`INSERT INTO tags (id, title) VALUES ('tag1', 'urgent')`)
	if err != nil {
		t.Fatalf("failed to insert tag: %v", err)
	}
	_, err = db.Exec(`INSERT INTO task_tags (task_id, tag_id) VALUES ('t1', 'tag1')`)
	if err != nil {
		t.Fatalf("failed to insert task_tag: %v", err)
	}

	// Delete the task -- children should cascade delete.
	_, err = db.Exec(`DELETE FROM tasks WHERE id = 't1'`)
	if err != nil {
		t.Fatalf("failed to delete task: %v", err)
	}

	tables := []struct {
		table string
		query string
	}{
		{"checklist_items", "SELECT COUNT(*) FROM checklist_items WHERE task_id = 't1'"},
		{"attachments", "SELECT COUNT(*) FROM attachments WHERE task_id = 't1'"},
		{"task_tags", "SELECT COUNT(*) FROM task_tags WHERE task_id = 't1'"},
	}

	for _, tc := range tables {
		var count int
		_ = db.QueryRow(tc.query).Scan(&count)
		if count != 0 {
			t.Errorf("expected %s to be cascade-deleted with task, got count=%d", tc.table, count)
		}
	}
}

// TestMigration029_ZeroPads pins E3/D1: the migration zero-pads single-digit
// hour times (9:00 -> 09:00), leaves HH:MM and unparseable values alone, and
// re-running it is a no-op.
func TestMigration029_ZeroPads(t *testing.T) {
	db := testutil.SetupTestDB(t)

	// The migration ran when the test database was opened.
	var applied int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM _migrations WHERE name = '029_zero_pad_schedule_times.sql'",
	).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("expected migration 029 to be recorded, got applied=%d err=%v", applied, err)
	}

	rows := []struct{ id, start, end string }{
		{"s_pad", "9:00", "9:30"},
		{"s_ok", "09:30", "10:15"},
		{"s_junk", "junk", "also junk"},
	}
	for _, row := range rows {
		if _, err := db.Exec("INSERT INTO tasks (id, title) VALUES (?, ?)", row.id, row.id); err != nil {
			t.Fatalf("insert task %s: %v", row.id, err)
		}
		if _, err := db.Exec(
			"INSERT INTO task_schedules (id, task_id, when_date, start_time, end_time) VALUES (?, ?, '2026-10-17', ?, ?)",
			row.id, row.id, row.start, row.end,
		); err != nil {
			t.Fatalf("insert schedule %s: %v", row.id, err)
		}
	}

	content, err := os.ReadFile("migrations/029_zero_pad_schedule_times.sql")
	if err != nil {
		t.Fatalf("read migration 029: %v", err)
	}
	// Apply twice: the GLOB filter makes the second pass a no-op.
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(string(content)); err != nil {
			t.Fatalf("apply migration 029 (pass %d): %v", i+1, err)
		}
	}

	want := map[string][2]string{
		"s_pad":  {"09:00", "09:30"},
		"s_ok":   {"09:30", "10:15"},
		"s_junk": {"junk", "also junk"},
	}
	for id, expected := range want {
		var start, end string
		if err := db.QueryRow(
			"SELECT start_time, end_time FROM task_schedules WHERE id = ?", id,
		).Scan(&start, &end); err != nil {
			t.Fatalf("read schedule %s: %v", id, err)
		}
		if start != expected[0] || end != expected[1] {
			t.Errorf("%s: got %q/%q, want %q/%q", id, start, end, expected[0], expected[1])
		}
	}
}
