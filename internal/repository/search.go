package repository

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/collinjanssen/thingstodo/internal/model"
)

type SearchRepository struct {
	db *sql.DB
}

func NewSearchRepository(db *sql.DB) *SearchRepository {
	return &SearchRepository{db: db}
}

// ftsQuery converts raw user input into a safe FTS5 MATCH expression (D4):
// the input is split on whitespace and every token is quoted as an FTS5
// string, doubling any ", so operators and punctuation are literal
// ("c++", "email:", "milk-2") and can never form an FTS5 syntax error.
// Tokens are joined with spaces, FTS5's implicit AND. With prefix true
// each token keeps a trailing * so a prefix such as mil still matches
// milk; /api/search searches with prefix, the search param on /api/tasks
// without.
func ftsQuery(input string, prefix bool) string {
	tokens := strings.Fields(input)
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		q := `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
		if prefix {
			q += "*"
		}
		parts = append(parts, q)
	}
	return strings.Join(parts, " ")
}

// Search returns tasks matching query ordered by rank then id (D4), a page
// of at most limit results, and the cursor of the next page ("" when this
// is the last one). cursor resumes after the (rank, id) pair it encodes.
func (r *SearchRepository) Search(query string, limit int, cursor string) ([]model.SearchResult, string, error) {
	if limit <= 0 {
		limit = 20
	}

	var args []any
	args = append(args, ftsQuery(query, true))

	cursorCond := ""
	if cursor != "" {
		sortKey, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		rank, err := decodeFloatCursor(sortKey)
		if err != nil {
			return nil, "", err
		}
		cursorCond = " WHERE (rank > ? OR (rank = ? AND id > ?))"
		args = append(args, rank, rank, id)
	}
	args = append(args, limit+1)

	// The FTS rank column cannot be filtered in the MATCH query, so the
	// join is wrapped and the cursor keyset applies to the wrapped rank.
	rows, err := r.db.Query(`
		SELECT * FROM (
			SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
				t.deadline, t.project_id, t.area_id, t.heading_id,
				t.sort_order_today, t.sort_order_project, t.sort_order_heading,
				t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
				COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
				COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
				CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
				CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
				(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
				(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
				(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
				p.title AS project_name,
				a.title AS area_name,
				snippet(tasks_fts, 0, '<mark>', '</mark>', '...', 32) AS title_snippet,
				snippet(tasks_fts, 1, '<mark>', '</mark>', '...', 32) AS notes_snippet,
				rank
			FROM tasks_fts
			JOIN tasks t ON t.rowid = tasks_fts.rowid
			LEFT JOIN projects p ON p.id = t.project_id
			LEFT JOIN areas a ON a.id = t.area_id
			WHERE tasks_fts MATCH ? AND t.deleted_at IS NULL
		)`+cursorCond+`
		ORDER BY rank, id
		LIMIT ?`, args...)
	if err != nil {
		return nil, "", fmt.Errorf("search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []model.SearchResult
	for rows.Next() {
		var sr model.SearchResult
		var t model.TaskListItem
		var whenEvening, highPriority, hasNotes, hasLinks, hasFiles, hasRepeat, hasReminders int
		var projectName, areaName sql.NullString
		if err := rows.Scan(
			&t.ID, &t.Title, &t.Notes, &t.Status, &t.WhenDate, &whenEvening, &highPriority,
			&t.Deadline, &t.ProjectID, &t.AreaID, &t.HeadingID,
			&t.SortOrderToday, &t.SortOrderProject, &t.SortOrderHeading,
			&t.CompletedAt, &t.CanceledAt, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt,
			&t.ChecklistCount, &t.ChecklistDone,
			&hasNotes, &hasLinks, &hasFiles, &hasRepeat, &hasReminders,
			&t.FirstReminderType, &t.FirstReminderValue, &t.FirstReminderExactAt,
			&projectName, &areaName,
			&sr.TitleSnippet, &sr.NotesSnippet, &sr.Rank,
		); err != nil {
			return nil, "", fmt.Errorf("scan search result: %w", err)
		}
		_ = whenEvening // column retained in DB but no longer exposed
		t.HighPriority = highPriority == 1
		t.HasNotes = hasNotes == 1
		t.HasLinks = hasLinks == 1
		t.HasFiles = hasFiles == 1
		t.HasRepeatRule = hasRepeat == 1
		t.HasReminders = hasReminders == 1
		t.Tags = []model.TagRef{}
		t.ProjectName = nullStrPtr(projectName)
		t.AreaName = nullStrPtr(areaName)
		sr.Task = t
		results = append(results, sr)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("search: %w", err)
	}
	nextCursor := ""
	if len(results) > limit {
		results = results[:limit]
		last := results[limit-1]
		nextCursor = encodeCursor(last.Rank, last.Task.ID)
	}
	if results == nil {
		results = []model.SearchResult{}
	}
	return results, nextCursor, nil
}
