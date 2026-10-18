package repository_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

// TestViewContractFixtures runs every shared fixture in
// ../../testdata/views against the real view repositories. The fixtures
// are the one source of view truth shared with the frontend test suite;
// expectations encode the spec rules (view_predicates.go, spec E3/Q2),
// and rows compare in order.
func TestViewContractFixtures(t *testing.T) {
	files, err := filepath.Glob("../../testdata/views/*.json")
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no view contract fixtures found in ../../testdata/views")
	}
	sort.Strings(files)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		var fx vcFixture
		if err := json.Unmarshal(data, &fx); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		t.Run(fx.Name, func(t *testing.T) {
			db := testutil.SetupTestDB(t)
			vcSeedDB(t, db, fx.Seed)

			loc, err := time.LoadLocation(fx.Timezone)
			if err != nil {
				t.Fatalf("load timezone %s: %v", fx.Timezone, err)
			}
			now, err := time.Parse(time.RFC3339, fx.Now)
			if err != nil {
				t.Fatalf("parse now %s: %v", fx.Now, err)
			}
			clock := func() time.Time { return now }
			repo := repository.NewViewRepository(db, loc)
			repo.SetClock(clock)

			keys := make([]string, 0, len(fx.Expect))
			for k := range fx.Expect {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				switch {
				case key == "inbox":
					view, err := repo.Inbox(nil, false)
					if err != nil {
						t.Fatalf("inbox: %v", err)
					}
					vcCompareRows(t, key, vcWantRows(t, fx, key), vcTaskRows(view.Tasks))
				case key == "anytime", key == "someday":
					var view *model.AnytimeView
					if key == "anytime" {
						view, err = repo.Anytime()
					} else {
						view, err = repo.Someday()
					}
					if err != nil {
						t.Fatalf("%s: %v", key, err)
					}
					vcCompareRows(t, key, vcWantRows(t, fx, key), vcTaskRows(vcFlattenAnytime(view)))
				case key == "logbook":
					view, err := repo.Logbook(1000, 0)
					if err != nil {
						t.Fatalf("logbook: %v", err)
					}
					vcCompareRows(t, key, vcWantRows(t, fx, key), vcGroupRows(view.Groups))
				case key == "trash":
					view, err := repo.Trash(1000, 0)
					if err != nil {
						t.Fatalf("trash: %v", err)
					}
					vcCompareRows(t, key, vcWantRows(t, fx, key), vcGroupRows(view.Groups))
				case key == "today":
					view, err := repo.Today(fx.EveningStartsAt)
					if err != nil {
						t.Fatalf("today: %v", err)
					}
					var got []vcRow
					for _, g := range view.Sections[0].Groups {
						got = append(got, vcSectionRows("today", g.Tasks)...)
					}
					for _, g := range view.Sections[1].Groups {
						got = append(got, vcSectionRows("evening", g.Tasks)...)
					}
					got = append(got, vcSectionRows("overdue", view.Overdue)...)
					got = append(got, vcSectionRows("earlier", view.Earlier)...)
					vcCompareRows(t, key, vcWantRows(t, fx, key), got)
				case key == "upcoming":
					// Default from: the date groups start tomorrow, like
					// GET /api/views/upcoming without a from override.
					view, err := repo.Upcoming("")
					if err != nil {
						t.Fatalf("upcoming: %v", err)
					}
					got := vcSectionRows("overdue", view.Overdue)
					for _, dg := range view.Dates {
						got = append(got, vcTaskRows(dg.Tasks)...)
						for i := len(got) - len(dg.Tasks); i < len(got); i++ {
							got[i].Date = dg.Date
						}
					}
					got = append(got, vcSectionRows("earlier", view.Earlier)...)
					vcCompareRows(t, key, vcWantRows(t, fx, key), got)
				case key == "counts":
					got, err := repo.Counts(nil, false)
					if err != nil {
						t.Fatalf("counts: %v", err)
					}
					var want vcCounts
					vcDecode(t, fx, key, &want)
					if got.Inbox != want.Inbox || got.Today != want.Today || got.Overdue != want.Overdue ||
						got.Anytime != want.Anytime || got.Someday != want.Someday ||
						got.Logbook != want.Logbook || got.Trash != want.Trash {
						t.Errorf("counts: want %+v, got %+v", want, *got)
					}
				case strings.HasPrefix(key, "project:"):
					p, err := repository.NewProjectRepository(db, nil).WithLocation(loc).GetByID(strings.TrimPrefix(key, "project:"))
					if err != nil {
						t.Fatalf("%s: %v", key, err)
					}
					if p == nil {
						t.Fatalf("%s: project not found", key)
					}
					var got []vcRow
					for _, h := range p.Headings {
						got = append(got, vcTaskRows(h.Tasks)...)
					}
					got = append(got, vcTaskRows(p.TasksWithoutHeading)...)
					got = append(got, vcTaskRows(p.CompletedTasks)...)
					vcCompareRows(t, key, vcWantRows(t, fx, key), got)
				case strings.HasPrefix(key, "area:"):
					areaRepo := repository.NewAreaRepository(db, nil).WithLocation(loc)
					areaRepo.SetClock(clock)
					a, err := areaRepo.GetByID(strings.TrimPrefix(key, "area:"))
					if err != nil {
						t.Fatalf("%s: %v", key, err)
					}
					if a == nil {
						t.Fatalf("%s: area not found", key)
					}
					got := append(vcTaskRows(a.Tasks), vcTaskRows(a.CompletedTasks)...)
					vcCompareRows(t, key, vcWantRows(t, fx, key), got)
				case strings.HasPrefix(key, "tag:"):
					tasks, err := repository.NewTagRepository(db, nil).WithLocation(loc).GetTasksByTag(strings.TrimPrefix(key, "tag:"))
					if err != nil {
						t.Fatalf("%s: %v", key, err)
					}
					vcCompareRows(t, key, vcWantRows(t, fx, key), vcTaskRows(tasks))
				default:
					t.Errorf("unknown expect key %q", key)
				}
			}
		})
	}
}

// --- fixture decoding and seeding ---

type vcFixture struct {
	Name            string                     `json:"name"`
	Now             string                     `json:"now"`
	Timezone        string                     `json:"timezone"`
	EveningStartsAt string                     `json:"evening_starts_at"`
	Seed            vcSeed                     `json:"seed"`
	Expect          map[string]json.RawMessage `json:"expect"`
}

type vcSeed struct {
	Areas     []vcArea     `json:"areas"`
	Projects  []vcProject  `json:"projects"`
	Headings  []vcHeading  `json:"headings"`
	Tags      []vcTag      `json:"tags"`
	Tasks     []vcTask     `json:"tasks"`
	Schedules []vcSchedule `json:"schedules"`
}

type vcArea struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	SortOrder *float64 `json:"sort_order"`
}

type vcProject struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	AreaID    *string  `json:"area_id"`
	Status    string   `json:"status"`
	SortOrder *float64 `json:"sort_order"`
}

type vcHeading struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	ProjectID string   `json:"project_id"`
	SortOrder *float64 `json:"sort_order"`
}

type vcTag struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	ParentTagID *string  `json:"parent_tag_id"`
	SortOrder   *float64 `json:"sort_order"`
}

type vcTask struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Status           string   `json:"status"`
	WhenDate         *string  `json:"when_date"`
	Deadline         *string  `json:"deadline"`
	ProjectID        *string  `json:"project_id"`
	AreaID           *string  `json:"area_id"`
	HeadingID        *string  `json:"heading_id"`
	SortOrderToday   *float64 `json:"sort_order_today"`
	SortOrderProject *float64 `json:"sort_order_project"`
	CompletedAt      *string  `json:"completed_at"`
	CanceledAt       *string  `json:"canceled_at"`
	DeletedAt        *string  `json:"deleted_at"`
	TagIDs           []string `json:"tag_ids"`
}

type vcSchedule struct {
	ID        string   `json:"id"`
	TaskID    string   `json:"task_id"`
	WhenDate  string   `json:"when_date"`
	StartTime *string  `json:"start_time"`
	EndTime   *string  `json:"end_time"`
	Completed bool     `json:"completed"`
	SortOrder *float64 `json:"sort_order"`
}

// vcSeed inserts the fixture rows with plain SQL; fields the fixture
// omits take the DB defaults (status 'open', sort_order 0, NULL refs).
func vcSeedDB(t *testing.T, db *sql.DB, s vcSeed) {
	t.Helper()
	for _, a := range s.Areas {
		vcExec(t, db, "INSERT INTO areas (id, title, sort_order) VALUES (?, ?, ?)",
			a.ID, a.Title, vcSort(a.SortOrder))
	}
	for _, p := range s.Projects {
		status := p.Status
		if status == "" {
			status = "open"
		}
		vcExec(t, db, "INSERT INTO projects (id, title, area_id, status, sort_order) VALUES (?, ?, ?, ?, ?)",
			p.ID, p.Title, p.AreaID, status, vcSort(p.SortOrder))
	}
	for _, h := range s.Headings {
		vcExec(t, db, "INSERT INTO headings (id, title, project_id, sort_order) VALUES (?, ?, ?, ?)",
			h.ID, h.Title, h.ProjectID, vcSort(h.SortOrder))
	}
	for _, g := range s.Tags {
		vcExec(t, db, "INSERT INTO tags (id, title, parent_tag_id, sort_order) VALUES (?, ?, ?, ?)",
			g.ID, g.Title, g.ParentTagID, vcSort(g.SortOrder))
	}
	for _, task := range s.Tasks {
		status := task.Status
		if status == "" {
			status = "open"
		}
		vcExec(t, db, `INSERT INTO tasks (id, title, status, when_date, deadline, project_id, area_id, heading_id,
			sort_order_today, sort_order_project, completed_at, canceled_at, deleted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			task.ID, task.Title, status, task.WhenDate, task.Deadline, task.ProjectID, task.AreaID,
			task.HeadingID, vcSort(task.SortOrderToday), vcSort(task.SortOrderProject),
			task.CompletedAt, task.CanceledAt, task.DeletedAt)
	}
	for _, task := range s.Tasks {
		for _, tagID := range task.TagIDs {
			vcExec(t, db, "INSERT INTO task_tags (task_id, tag_id) VALUES (?, ?)", task.ID, tagID)
		}
	}
	for _, sc := range s.Schedules {
		completed := 0
		if sc.Completed {
			completed = 1
		}
		vcExec(t, db, `INSERT INTO task_schedules (id, task_id, when_date, start_time, end_time, completed, sort_order)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sc.ID, sc.TaskID, sc.WhenDate, sc.StartTime, sc.EndTime, completed, vcSort(sc.SortOrder))
	}
}

func vcSort(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func vcExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("seed %q: %v", query, err)
	}
}

// --- row mapping and comparison ---

// vcRow is the fixture row grammar: task id plus schedule entry, and
// either a view section or a group date.
type vcRow struct {
	Task    string  `json:"task"`
	Section string  `json:"section"`
	Entry   *string `json:"entry"`
	Date    string  `json:"date"`
}

type vcCounts struct {
	Inbox   int `json:"inbox"`
	Today   int `json:"today"`
	Overdue int `json:"overdue"`
	Anytime int `json:"anytime"`
	Someday int `json:"someday"`
	Logbook int `json:"logbook"`
	Trash   int `json:"trash"`
}

func vcWantRows(t *testing.T, fx vcFixture, key string) []vcRow {
	t.Helper()
	var rows []vcRow
	vcDecode(t, fx, key, &rows)
	return rows
}

func vcDecode(t *testing.T, fx vcFixture, key string, v any) {
	t.Helper()
	if err := json.Unmarshal(fx.Expect[key], v); err != nil {
		t.Fatalf("decode expect %q: %v", key, err)
	}
}

// vcTaskRows maps flat task lists (no section, no group date).
func vcTaskRows(tasks []model.TaskListItem) []vcRow {
	rows := make([]vcRow, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, vcRow{Task: task.ID, Entry: task.ScheduleEntryID})
	}
	return rows
}

// vcSectionRows maps tasks shown under a named view section.
func vcSectionRows(section string, tasks []model.TaskListItem) []vcRow {
	rows := vcTaskRows(tasks)
	for i := range rows {
		rows[i].Section = section
	}
	return rows
}

// vcGroupRows maps date-grouped listings (logbook, trash).
func vcGroupRows(groups []model.DateGroup) []vcRow {
	var rows []vcRow
	for _, g := range groups {
		for _, task := range g.Tasks {
			rows = append(rows, vcRow{Task: task.ID, Entry: task.ScheduleEntryID, Date: g.Date})
		}
	}
	return rows
}

// vcFlattenAnytime walks the Anytime/Someday view in its display order:
// each area's projects then standalone tasks, then the no-area projects
// and no-area standalone tasks.
func vcFlattenAnytime(v *model.AnytimeView) []model.TaskListItem {
	var tasks []model.TaskListItem
	for _, a := range v.Areas {
		for _, p := range a.Projects {
			tasks = append(tasks, p.Tasks...)
		}
		tasks = append(tasks, a.StandaloneTasks...)
	}
	for _, p := range v.NoArea.Projects {
		tasks = append(tasks, p.Tasks...)
	}
	return append(tasks, v.NoArea.StandaloneTasks...)
}

func vcCompareRows(t *testing.T, key string, want, got []vcRow) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: want %d rows (%s), got %d rows (%s)",
			key, len(want), vcRowsJSON(want), len(got), vcRowsJSON(got))
		return
	}
	for i := range want {
		w, g := want[i], got[i]
		switch {
		case w.Task != g.Task:
			t.Errorf("%s row %d: want task %s, got %s", key, i, w.Task, g.Task)
		case w.Section != g.Section:
			t.Errorf("%s row %d (%s): want section %q, got %q", key, i, w.Task, w.Section, g.Section)
		case w.Date != g.Date:
			t.Errorf("%s row %d (%s): want date %q, got %q", key, i, w.Task, w.Date, g.Date)
		case !vcSameEntry(w.Entry, g.Entry):
			t.Errorf("%s row %d (%s): want entry %s, got %s", key, i, w.Task, vcEntryJSON(w.Entry), vcEntryJSON(g.Entry))
		}
	}
}

func vcSameEntry(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func vcEntryJSON(e *string) string {
	if e == nil {
		return "null"
	}
	return *e
}

func vcRowsJSON(rows []vcRow) string {
	if rows == nil {
		return "[]"
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "?"
	}
	return string(b)
}
