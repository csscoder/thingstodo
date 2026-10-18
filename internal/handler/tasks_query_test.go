package handler_test

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// setupQueryEnv mounts the two query endpoints under test — GET/POST
// /api/tasks and GET /api/search — over a fresh test database. Fixtures go
// through the repositories so triggers, schedules and FTS stay realistic.
func strPtr(s string) *string { return &s }

func setupQueryEnv(t *testing.T) (*testutil.TestClient, *repository.TaskRepository) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	taskRepo := repository.NewTaskRepository(db, nil)
	reminderRepo := repository.NewReminderRepository(db, nil)
	settingsRepo := repository.NewUserSettingsRepository(db)
	searchRepo := repository.NewSearchRepository(db)
	taskHandler := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)
	searchHandler := handler.NewSearchHandler(searchRepo)

	r := chi.NewRouter()
	r.Get("/api/tasks", taskHandler.List)
	r.Post("/api/tasks", taskHandler.Create)
	r.Get("/api/search", searchHandler.Search)
	client := testutil.NewTestClient(t, r)
	return client, taskRepo
}

func queryTaskIDs(t *testing.T, resp *testutil.TestResponse) []string {
	t.Helper()
	var body struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	resp.JSON(t, &body)
	ids := make([]string, 0, len(body.Tasks))
	for _, task := range body.Tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func assertIDsUnordered(t *testing.T, resp *testutil.TestResponse, expected ...string) {
	t.Helper()
	got := queryTaskIDs(t, resp)
	sort.Strings(got)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected ids %v, got %v\nbody: %s", want, got, string(resp.Body))
	}
}

func assertIDsOrdered(t *testing.T, resp *testutil.TestResponse, expected ...string) {
	t.Helper()
	got := queryTaskIDs(t, resp)
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("expected ids %v, got %v\nbody: %s", expected, got, string(resp.Body))
	}
}

// nextCursorOf returns the response next_cursor, "" when it is null or
// absent (the last page).
func nextCursorOf(t *testing.T, resp *testutil.TestResponse) string {
	t.Helper()
	var m map[string]interface{}
	resp.JSON(t, &m)
	if m["next_cursor"] == nil {
		return ""
	}
	s, _ := m["next_cursor"].(string)
	return s
}

func mustCreateTask(t *testing.T, repo *repository.TaskRepository, input model.CreateTaskInput) {
	t.Helper()
	if _, err := repo.Create(input); err != nil {
		t.Fatalf("create task %q: %v", input.Title, err)
	}
}

func TestTaskQuery_Filters(t *testing.T) {
	client, repo := setupQueryEnv(t)
	db := repo.Conn()

	areaRepo := repository.NewAreaRepository(db, nil)
	if _, err := areaRepo.Create(model.CreateAreaInput{ID: "areaFix01", Title: "Fixtures Area"}); err != nil {
		t.Fatal(err)
	}
	projectRepo := repository.NewProjectRepository(db, nil)
	if _, err := projectRepo.Create(model.CreateProjectInput{ID: "projFix01", Title: "Fixtures Project", AreaID: strPtr("areaFix01")}); err != nil {
		t.Fatal(err)
	}
	headingRepo := repository.NewHeadingRepository(db, nil)
	if _, err := headingRepo.Create("projFix01", model.CreateHeadingInput{ID: "headFix01", Title: "Fixtures Heading"}); err != nil {
		t.Fatal(err)
	}
	tagRepo := repository.NewTagRepository(db, nil)
	for _, tag := range []model.CreateTagInput{
		{ID: "tagWork01", Title: "work"},
		{ID: "tagUrgent01", Title: "urgent"},
	} {
		if _, err := tagRepo.Create(tag); err != nil {
			t.Fatal(err)
		}
	}

	// Dates are computed relative to the repository clock so list=today
	// membership (overdue deadline, when_date today) and the when_before /
	// when_after bounds are stable regardless of the wall clock.
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixOpen01", Title: "Inbox task"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixDone01", Title: "Done task"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixCanc01", Title: "Canceled task"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixHigh01", Title: "High task", HighPriority: true, TagIDs: []string{"tagWork01", "tagUrgent01"}})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixWhen01", Title: "Someday task", WhenDate: strPtr("someday"), TagIDs: []string{"tagWork01"}})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixDead01", Title: "Deadline task", Deadline: strPtr("2026-10-20"), ProjectID: strPtr("projFix01"), HeadingID: strPtr("headFix01")})
	// Static past deadline: always overdue, always inside deadline_before=2026-10-16.
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixOver01", Title: "Overdue task", Deadline: strPtr("2026-10-15")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixToday01", Title: "Today task", WhenDate: strPtr(today)})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "fixTrash01", Title: "Trashed task"})

	if _, err := repo.SetDoneStatus("fixDone01", "completed"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetDoneStatus("fixCanc01", "canceled"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete("fixTrash01"); err != nil {
		t.Fatal(err)
	}

	// Pin created_at/updated_at so created_since and updated_since are
	// independent of the wall clock.
	setTimes := func(id, created, updated string) {
		t.Helper()
		if _, err := db.Exec("UPDATE tasks SET created_at = ?, updated_at = ? WHERE id = ?", created, updated, id); err != nil {
			t.Fatal(err)
		}
	}
	setTimes("fixOpen01", "2026-10-10 08:00:00", "2026-10-10 08:00:00")
	for _, id := range []string{"fixDone01", "fixCanc01", "fixHigh01", "fixWhen01", "fixDead01", "fixOver01", "fixToday01", "fixTrash01"} {
		setTimes(id, "2026-10-12 09:00:00", "2026-10-12 09:00:00")
	}
	setTimes("fixDone01", "2026-10-12 09:00:00", "2026-10-16 20:00:00")

	allLive := []string{"fixOpen01", "fixDone01", "fixCanc01", "fixHigh01", "fixWhen01", "fixDead01", "fixOver01", "fixToday01"}

	cases := []struct {
		name     string
		query    string
		expected []string
	}{
		{"status list", "status=open,completed", []string{"fixOpen01", "fixDone01", "fixHigh01", "fixWhen01", "fixDead01", "fixOver01", "fixToday01"}},
		{"project_id", "project_id=projFix01", []string{"fixDead01"}},
		{"area_id", "area_id=areaFix01", []string{"fixDead01"}},
		{"heading_id", "heading_id=headFix01", []string{"fixDead01"}},
		{"tag_ids any", "tag_ids=tagWork01", []string{"fixHigh01", "fixWhen01"}},
		{"tag_ids all", "tag_ids=tagWork01,tagUrgent01&tag_mode=all", []string{"fixHigh01"}},
		{"when_date", "when_date=someday", []string{"fixWhen01"}},
		{"when_before", "when_before=" + tomorrow, []string{"fixToday01"}},
		{"when_after", "when_after=" + yesterday, []string{"fixToday01"}},
		{"deadline_before inclusive", "deadline_before=2026-10-16", []string{"fixOver01"}},
		{"deadline_after inclusive", "deadline_after=2026-10-16", []string{"fixDead01"}},
		{"has_deadline", "has_deadline=true", []string{"fixDead01", "fixOver01"}},
		{"has_when false", "has_when=false", []string{"fixOpen01", "fixDone01", "fixCanc01", "fixHigh01", "fixDead01", "fixOver01"}},
		{"high_priority", "high_priority=true", []string{"fixHigh01"}},
		{"updated_since", "updated_since=2026-10-16+00:00:00", []string{"fixDone01"}},
		{"created_since", "created_since=2026-10-11+00:00:00", []string{"fixDone01", "fixCanc01", "fixHigh01", "fixWhen01", "fixDead01", "fixOver01", "fixToday01"}},
		{"list today", "list=today", []string{"fixToday01", "fixOver01"}},
		{"list trash", "list=trash", []string{"fixTrash01"}},
		{"search", "search=high", []string{"fixHigh01"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := client.Get("/api/tasks?" + tc.query)
			testutil.AssertStatus(t, resp, http.StatusOK)
			assertIDsUnordered(t, resp, tc.expected...)
		})
	}

	// Sanity: without any param, every live task of every status returns.
	t.Run("no filters returns all live", func(t *testing.T) {
		resp := client.Get("/api/tasks")
		testutil.AssertStatus(t, resp, http.StatusOK)
		assertIDsUnordered(t, resp, allLive...)
	})
}

func TestTaskQuery_Sort(t *testing.T) {
	client, repo := setupQueryEnv(t)
	db := repo.Conn()
	if _, err := repository.NewAreaRepository(db, nil).Create(model.CreateAreaInput{ID: "areaSort01", Title: "Sort Area"}); err != nil {
		t.Fatal(err)
	}
	projectRepo := repository.NewProjectRepository(db, nil)
	if _, err := projectRepo.Create(model.CreateProjectInput{ID: "projSort01", Title: "Sort Project", AreaID: strPtr("areaSort01")}); err != nil {
		t.Fatal(err)
	}

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortDl01", Title: "deadline late", Deadline: strPtr("2026-10-20")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortDl02", Title: "deadline mid", Deadline: strPtr("2026-10-18")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortDl03", Title: "deadline early", Deadline: strPtr("2026-10-15")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortDl04", Title: "deadline none"})

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortTi01", Title: "query a"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortTi02", Title: "query c"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortTi03", Title: "query b"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortTi04", Title: "dup x"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortTi05", Title: "dup x"})

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortPm01", Title: "project manual 1", ProjectID: strPtr("projSort01")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortPm02", Title: "project manual 2", ProjectID: strPtr("projSort01")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortPm03", Title: "project manual 3", ProjectID: strPtr("projSort01")})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "sortPm04", Title: "loose task"})

	// Explicit project order: 3, 1, 2.
	setProjectOrder := func(id string, order float64) {
		t.Helper()
		if _, err := db.Exec("UPDATE tasks SET sort_order_project = ? WHERE id = ?", order, id); err != nil {
			t.Fatal(err)
		}
	}
	setProjectOrder("sortPm01", 20)
	setProjectOrder("sortPm02", 30)
	setProjectOrder("sortPm03", 10)

	t.Run("-deadline desc nulls last", func(t *testing.T) {
		resp := client.Get("/api/tasks?sort=-deadline")
		testutil.AssertStatus(t, resp, http.StatusOK)
		// Deadlines descending first, then every NULL deadline row tie-broken
		// by id ascending.
		assertIDsOrdered(t, resp,
			"sortDl01", "sortDl02", "sortDl03",
			"sortDl04", "sortPm01", "sortPm02", "sortPm03", "sortPm04",
			"sortTi01", "sortTi02", "sortTi03", "sortTi04", "sortTi05")
	})

	t.Run("title asc tie by id", func(t *testing.T) {
		resp := client.Get("/api/tasks?sort=title")
		testutil.AssertStatus(t, resp, http.StatusOK)
		assertIDsOrdered(t, resp,
			"sortDl03", "sortDl01", "sortDl02", "sortDl04",
			"sortTi04", "sortTi05", "sortPm04", "sortPm01", "sortPm02", "sortPm03",
			"sortTi01", "sortTi03", "sortTi02")
	})

	t.Run("manual with project_id uses sort_order_project", func(t *testing.T) {
		resp := client.Get("/api/tasks?sort=manual&project_id=projSort01")
		testutil.AssertStatus(t, resp, http.StatusOK)
		assertIDsOrdered(t, resp, "sortPm03", "sortPm01", "sortPm02")
	})
}

func TestTaskQuery_CursorPages(t *testing.T) {
	client, repo := setupQueryEnv(t)

	for i := 1; i <= 7; i++ {
		mustCreateTask(t, repo, model.CreateTaskInput{
			ID:    fmt.Sprintf("curPage%02d", i),
			Title: fmt.Sprintf("cursor task %d", i),
		})
	}

	var collected []string
	cursors := map[string]bool{}
	query := "/api/tasks?limit=3"
	pages := 0
	for {
		resp := client.Get(query)
		testutil.AssertStatus(t, resp, http.StatusOK)
		ids := queryTaskIDs(t, resp)
		pages++
		switch pages {
		case 1, 2:
			if len(ids) != 3 {
				t.Fatalf("page %d: expected 3 tasks, got %d", pages, len(ids))
			}
		case 3:
			if len(ids) != 1 {
				t.Fatalf("page 3: expected 1 task, got %d", len(ids))
			}
		default:
			t.Fatalf("expected 3 pages, got a %dth", pages)
		}
		collected = append(collected, ids...)
		cursor := nextCursorOf(t, resp)
		if cursor == "" {
			break
		}
		if cursors[cursor] {
			t.Fatalf("cursor repeated: %s", cursor)
		}
		cursors[cursor] = true
		query = "/api/tasks?limit=3&cursor=" + url.QueryEscape(cursor)
	}

	if len(collected) != 7 {
		t.Fatalf("expected 7 tasks over all pages, got %d", len(collected))
	}
	seen := map[string]bool{}
	for _, id := range collected {
		if seen[id] {
			t.Fatalf("duplicate id across pages: %s", id)
		}
		seen[id] = true
	}
	// No gaps: the paged ids equal the unpaged listing.
	full := client.Get("/api/tasks")
	for _, id := range queryTaskIDs(t, full) {
		if !seen[id] {
			t.Fatalf("task %s missing from pages", id)
		}
	}
}

func TestTaskQuery_NoLimitReturnsAll(t *testing.T) {
	client, repo := setupQueryEnv(t)
	db := repo.Conn()

	// Insert directly for speed: the FTS triggers keep the index warm and
	// List does not depend on how the rows were written.
	for i := 0; i < 600; i++ {
		id := fmt.Sprintf("bulk%05d", i)
		if _, err := db.Exec(
			"INSERT INTO tasks (id, title, sort_order_today, sort_order_project, sort_order_heading) VALUES (?, ?, ?, ?, ?)",
			id, "bulk task "+id, float64(i), float64(i), float64(i),
		); err != nil {
			t.Fatalf("insert bulk task %d: %v", i, err)
		}
	}

	resp := client.Get("/api/tasks")
	testutil.AssertStatus(t, resp, http.StatusOK)
	if got := len(queryTaskIDs(t, resp)); got != 600 {
		t.Fatalf("expected all 600 tasks without limit, got %d", got)
	}
	if cursor := nextCursorOf(t, resp); cursor != "" {
		t.Fatalf("expected null next_cursor without limit, got %q", cursor)
	}
}

func TestTaskQuery_InvalidParams(t *testing.T) {
	client, _ := setupQueryEnv(t)

	cases := []struct {
		name  string
		query string
		field string
	}{
		{"limit zero", "limit=0", "limit"},
		{"limit too large", "limit=501", "limit"},
		{"limit not a number", "limit=abc", "limit"},
		{"unknown sort", "sort=nope", "sort"},
		{"unknown list", "list=nope", "list"},
		{"unknown tag_mode", "tag_ids=tagWork01&tag_mode=x", "tag_mode"},
		{"invalid has_when", "has_when=maybe", "has_when"},
		{"invalid high_priority", "high_priority=maybe", "high_priority"},
		{"invalid status", "status=archive", "status"},
		{"invalid when_before", "when_before=not-a-date", "when_before"},
		{"invalid deadline_before", "deadline_before=2026-13-01", "deadline_before"},
		{"invalid updated_since", "updated_since=yesterday", "updated_since"},
		{"unknown param", "foo=1", "foo"},
		{"garbage cursor", "cursor=%21%21%21", "cursor"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := client.Get("/api/tasks?" + tc.query)
			testutil.AssertStatus(t, resp, http.StatusBadRequest)
			testutil.AssertJSONField(t, resp, "code", "VALIDATION")
			testutil.AssertJSONField(t, resp, "field", tc.field)
		})
	}
}

func TestTaskQuery_FillsNames(t *testing.T) {
	client, repo := setupQueryEnv(t)
	db := repo.Conn()

	if _, err := repository.NewAreaRepository(db, nil).Create(model.CreateAreaInput{ID: "areaName01", Title: "Website Area"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewProjectRepository(db, nil).Create(model.CreateProjectInput{ID: "projName01", Title: "Website Redesign", AreaID: strPtr("areaName01")}); err != nil {
		t.Fatal(err)
	}
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "nameTask01", Title: "Named task", ProjectID: strPtr("projName01")})

	resp := client.Get("/api/tasks?project_id=projName01")
	testutil.AssertStatus(t, resp, http.StatusOK)
	var body struct {
		Tasks []struct {
			ID          string  `json:"id"`
			ProjectName *string `json:"project_name"`
			AreaName    *string `json:"area_name"`
		} `json:"tasks"`
	}
	resp.JSON(t, &body)
	if len(body.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d\nbody: %s", len(body.Tasks), string(resp.Body))
	}
	task := body.Tasks[0]
	if task.ProjectName == nil || *task.ProjectName != "Website Redesign" {
		t.Errorf("expected project_name %q, got %v", "Website Redesign", task.ProjectName)
	}
	if task.AreaName == nil || *task.AreaName != "Website Area" {
		t.Errorf("expected area_name %q, got %v", "Website Area", task.AreaName)
	}
}

func searchResultIDs(t *testing.T, resp *testutil.TestResponse) []string {
	t.Helper()
	var body struct {
		Results []struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"results"`
	}
	resp.JSON(t, &body)
	ids := make([]string, 0, len(body.Results))
	for _, result := range body.Results {
		ids = append(ids, result.Task.ID)
	}
	return ids
}

func TestSearch_LiteralPunctuation(t *testing.T) {
	client, repo := setupQueryEnv(t)

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "litCpp01", Title: "c++ notes"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "litMail01", Title: "email me"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "litMilk01", Title: "milk-2 chocolate"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "litMilk02", Title: "buy milk"})

	// expectedPrefix: /api/search matches token prefixes; expectedLiteral:
	// the search param matches whole tokens only.
	cases := []struct {
		name            string
		q               string
		expectedPrefix  []string
		expectedLiteral []string
	}{
		// "c++" is the token c: as a prefix it also reaches chocolate.
		{"plus signs", "c++", []string{"litCpp01", "litMilk01"}, []string{"litCpp01"}},
		{"trailing colon", "email:", []string{"litMail01"}, []string{"litMail01"}},
		{"hyphenated token", "milk-2", []string{"litMilk01"}, []string{"litMilk01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/api/search", func(t *testing.T) {
			resp := client.Get("/api/search?" + url.Values{"q": {tc.q}}.Encode())
			testutil.AssertStatus(t, resp, http.StatusOK)
			got := searchResultIDs(t, resp)
			sort.Strings(got)
			want := append([]string(nil), tc.expectedPrefix...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("expected ids %v, got %v\nbody: %s", want, got, string(resp.Body))
			}
		})
		t.Run(tc.name+"/api/tasks search", func(t *testing.T) {
			resp := client.Get("/api/tasks?" + url.Values{"search": {tc.q}}.Encode())
			testutil.AssertStatus(t, resp, http.StatusOK)
			got := queryTaskIDs(t, resp)
			sort.Strings(got)
			want := append([]string(nil), tc.expectedLiteral...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("expected ids %v, got %v\nbody: %s", want, got, string(resp.Body))
			}
		})
	}
}

func TestSearch_Prefix(t *testing.T) {
	client, repo := setupQueryEnv(t)

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "prefMilk01", Title: "buy milk"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "prefMilk02", Title: "milk-2 chocolate"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "prefOther", Title: "buy eggs"})

	resp := client.Get("/api/search?" + url.Values{"q": {"mil"}}.Encode())
	testutil.AssertStatus(t, resp, http.StatusOK)
	got := searchResultIDs(t, resp)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"prefMilk01", "prefMilk02"}) {
		t.Errorf("expected prefix match on both milk tasks, got %v\nbody: %s", got, string(resp.Body))
	}

	// The search param on /api/tasks does NOT prefix-match.
	resp = client.Get("/api/tasks?" + url.Values{"search": {"mil"}}.Encode())
	testutil.AssertStatus(t, resp, http.StatusOK)
	if got := queryTaskIDs(t, resp); len(got) != 0 {
		t.Errorf("expected no full-token match for %q, got %v", "mil", got)
	}
}

func TestSearch_CyrillicPrefix(t *testing.T) {
	client, repo := setupQueryEnv(t)

	mustCreateTask(t, repo, model.CreateTaskInput{ID: "cyrShop01", Title: "Покупки на неделю"})
	mustCreateTask(t, repo, model.CreateTaskInput{ID: "cyrOther01", Title: "Звонок врачу"})

	resp := client.Get("/api/search?" + url.Values{"q": {"покуп"}}.Encode())
	testutil.AssertStatus(t, resp, http.StatusOK)
	if got := searchResultIDs(t, resp); !reflect.DeepEqual(got, []string{"cyrShop01"}) {
		t.Errorf("expected Cyrillic prefix match on cyrShop01, got %v\nbody: %s", got, string(resp.Body))
	}

	// The search param uses the full word, folded case-insensitively.
	resp = client.Get("/api/tasks?" + url.Values{"search": {"покупки"}}.Encode())
	testutil.AssertStatus(t, resp, http.StatusOK)
	if got := queryTaskIDs(t, resp); !reflect.DeepEqual(got, []string{"cyrShop01"}) {
		t.Errorf("expected full-word Cyrillic match on cyrShop01, got %v\nbody: %s", got, string(resp.Body))
	}
}

// TestTaskQuery_WhitespaceSearchIs400 pins the D4 boundary rule: a search
// param with no searchable tokens can never reach FTS5 (an empty MATCH
// string is a syntax error and would 500).
func TestTaskQuery_WhitespaceSearchIs400(t *testing.T) {
	client, _ := setupQueryEnv(t)

	for _, query := range []string{"search=%20", "search=%09"} {
		resp := client.Get("/api/tasks?" + query)
		testutil.AssertStatus(t, resp, http.StatusBadRequest)
		testutil.AssertJSONField(t, resp, "code", "VALIDATION")
		testutil.AssertJSONField(t, resp, "field", "search")
	}
}

// TestSearch_WhitespaceQueryIs400 is the /api/search twin: q with only
// whitespace is rejected with 400 naming q, not a 500.
func TestSearch_WhitespaceQueryIs400(t *testing.T) {
	client, _ := setupQueryEnv(t)

	resp := client.Get("/api/search?q=%20")
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
	testutil.AssertJSONField(t, resp, "field", "q")
}

func TestSearch_PagesByRankStable(t *testing.T) {
	client, repo := setupQueryEnv(t)

	// Identical titles give identical ranks, so paging exercises the id
	// tie-breaker rather than distinct rank values.
	for i := 1; i <= 5; i++ {
		mustCreateTask(t, repo, model.CreateTaskInput{
			ID:    fmt.Sprintf("rankTie%02d", i),
			Title: "alpha beta",
		})
	}

	var collected []string
	seen := map[string]bool{}
	query := "/api/search?" + url.Values{"q": {"alpha"}, "limit": {"2"}}.Encode()
	pages := 0
	for {
		resp := client.Get(query)
		testutil.AssertStatus(t, resp, http.StatusOK)
		// The response keeps its results key.
		var probe map[string]interface{}
		resp.JSON(t, &probe)
		if _, ok := probe["results"].([]interface{}); !ok {
			t.Fatalf("response missing results array\nbody: %s", string(resp.Body))
		}
		ids := searchResultIDs(t, resp)
		pages++
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("duplicate id across pages: %s", id)
			}
			seen[id] = true
			collected = append(collected, id)
		}
		cursor := nextCursorOf(t, resp)
		if cursor == "" {
			break
		}
		query = "/api/search?" + url.Values{"q": {"alpha"}, "limit": {"2"}, "cursor": {cursor}}.Encode()
	}

	if len(collected) != 5 {
		t.Fatalf("expected 5 results over all pages, got %d", len(collected))
	}
	if pages != 3 {
		t.Fatalf("expected pages 2,2,1, got %d pages", pages)
	}
}
