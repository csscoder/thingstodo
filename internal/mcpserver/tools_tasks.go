package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerTaskTools(srv *mcp.Server, d *dispatcher) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_tasks",
		Description: "Full-text search over tasks with optional filters. " + descDateFmt + " Comma-separated tag ids in tag_ids match any listed tag. Results are paginated with limit (default 50); use query_tasks for cursor-based paging and the full filter set.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchTasksIn) (*mcp.CallToolResult, any, error) {
		q := url.Values{}
		q.Set("search", in.Search)
		if in.Status != nil {
			q.Set("status", *in.Status)
		}
		if in.ProjectID != nil {
			q.Set("project_id", *in.ProjectID)
		}
		if in.AreaID != nil {
			q.Set("area_id", *in.AreaID)
		}
		if in.HeadingID != nil {
			q.Set("heading_id", *in.HeadingID)
		}
		if in.TagIDs != nil {
			q.Set("tag_ids", strings.Join(in.TagIDs, ","))
		}
		if in.WhenDate != nil {
			q.Set("when_date", *in.WhenDate)
		}
		if in.WhenBefore != nil {
			q.Set("when_before", *in.WhenBefore)
		}
		if in.WhenAfter != nil {
			q.Set("when_after", *in.WhenAfter)
		}
		if in.HasDeadline != nil {
			q.Set("has_deadline", strconv.FormatBool(*in.HasDeadline))
		}
		limit := 50
		if in.Limit != nil {
			limit = *in.Limit
		}
		q.Set("limit", strconv.Itoa(limit))
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/tasks?"+q.Encode(), nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_task",
		Description: "Get one task in full detail: all fields plus checklist, attachments, schedules and reminders.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodGet, "/api/tasks/", in.ID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a new task. " + descDateFmt + " " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createTaskIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tasks", in)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_task",
		Description: "Update a task (partial update). " + descDateFmt + " " + descNull + " when_date, deadline, project_id, area_id and heading_id accept null to clear them (clearing when_date also removes the task's schedule entries).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in *updateTaskIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPatch, "/api/tasks/", in.ID, "", in.body())
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	action := func(name, path, description string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			body, err := d.callID(ctx, req, http.MethodPatch, "/api/tasks/", in.ID, path, nil)
			if err != nil {
				return nil, nil, err
			}
			return toolJSON(body), nil, nil
		})
	}
	action("complete_task", "/complete", "Mark a task as completed. For a repeating task this also creates exactly one next instance; completing an already-completed task returns it unchanged and spawns nothing.")
	action("cancel_task", "/cancel", "Cancel a task.")
	action("wontdo_task", "/wontdo", "Mark a task as won't do.")
	action("reopen_task", "/reopen", "Reopen a completed or canceled task.")

	remove := func(name, path, description string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			_, err := d.callID(ctx, req, http.MethodDelete, "/api/tasks/", in.ID, path, nil)
			if err != nil {
				return nil, nil, err
			}
			return toolDeleted(), nil, nil
		})
	}
	remove("delete_task", "", "Soft-delete a task (move it to trash; restorable).")
	remove("purge_task", "/purge", "Permanently delete a task. Irreversible.")

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "restore_task",
		Description: "Restore a soft-deleted task from the trash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPatch, "/api/tasks/", in.ID, "/restore", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "query_tasks",
		Description: "Query tasks with the full filter, sort and pagination set (the same engine the REST API exposes). All filters combine with AND. " + descDateFmt + " status is a comma list of open, completed, canceled, wont_do. list selects a view's membership: inbox, today, upcoming, anytime, someday, logbook, trash. tag_mode any matches tasks with at least one listed tag, all requires every listed tag. sort is manual (default), when_date, deadline, created_at, updated_at or title, prefixed with - for descending. limit defaults to 50 (max 500) and is always applied; pass the returned next_cursor to fetch the next page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in queryTasksIn) (*mcp.CallToolResult, any, error) {
		q := url.Values{}
		if in.Status != nil {
			q.Set("status", *in.Status)
		}
		if in.List != nil {
			q.Set("list", *in.List)
		}
		if in.ProjectID != nil {
			q.Set("project_id", *in.ProjectID)
		}
		if in.AreaID != nil {
			q.Set("area_id", *in.AreaID)
		}
		if in.HeadingID != nil {
			q.Set("heading_id", *in.HeadingID)
		}
		if in.TagIDs != nil {
			q.Set("tag_ids", strings.Join(in.TagIDs, ","))
		}
		if in.TagMode != nil {
			q.Set("tag_mode", *in.TagMode)
		}
		if in.WhenDate != nil {
			q.Set("when_date", *in.WhenDate)
		}
		if in.WhenBefore != nil {
			q.Set("when_before", *in.WhenBefore)
		}
		if in.WhenAfter != nil {
			q.Set("when_after", *in.WhenAfter)
		}
		if in.DeadlineBefore != nil {
			q.Set("deadline_before", *in.DeadlineBefore)
		}
		if in.DeadlineAfter != nil {
			q.Set("deadline_after", *in.DeadlineAfter)
		}
		if in.HasDeadline != nil {
			q.Set("has_deadline", strconv.FormatBool(*in.HasDeadline))
		}
		if in.HasWhen != nil {
			q.Set("has_when", strconv.FormatBool(*in.HasWhen))
		}
		if in.HighPriority != nil {
			q.Set("high_priority", strconv.FormatBool(*in.HighPriority))
		}
		if in.UpdatedSince != nil {
			q.Set("updated_since", *in.UpdatedSince)
		}
		if in.CreatedSince != nil {
			q.Set("created_since", *in.CreatedSince)
		}
		if in.Search != nil {
			q.Set("search", *in.Search)
		}
		if in.Sort != nil {
			q.Set("sort", *in.Sort)
		}
		limit := 50
		if in.Limit != nil {
			limit = *in.Limit
		}
		q.Set("limit", strconv.Itoa(limit))
		if in.Cursor != nil {
			q.Set("cursor", *in.Cursor)
		}
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/tasks?"+q.Encode(), nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})
}

// idIn is the single required id parameter shared by simple task tools.
type idIn struct {
	ID string `json:"id" jsonschema:"task ID"`
}

type searchTasksIn struct {
	Search      string   `json:"search" jsonschema:"search query; multiple words are all required (AND)"`
	Status      *string  `json:"status,omitempty" jsonschema:"filter by status: open, completed, canceled or wont_do (comma list)"`
	ProjectID   *string  `json:"project_id,omitempty" jsonschema:"filter by project ID"`
	AreaID      *string  `json:"area_id,omitempty" jsonschema:"filter by area ID"`
	HeadingID   *string  `json:"heading_id,omitempty" jsonschema:"filter by heading ID"`
	TagIDs      []string `json:"tag_ids,omitempty" jsonschema:"filter by tag IDs (any match)"`
	WhenDate    *string  `json:"when_date,omitempty" jsonschema:"filter by exact when date (YYYY-MM-DD or someday)"`
	WhenBefore  *string  `json:"when_before,omitempty" jsonschema:"scheduled before this date (YYYY-MM-DD)"`
	WhenAfter   *string  `json:"when_after,omitempty" jsonschema:"scheduled after this date (YYYY-MM-DD)"`
	HasDeadline *bool    `json:"has_deadline,omitempty" jsonschema:"only tasks that have (true) or lack (false) a deadline"`
	Limit       *int     `json:"limit,omitempty" jsonschema:"page size, 1-500, default 50"`
}

type createTaskIn struct {
	ID           string   `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	Title        string   `json:"title" jsonschema:"task title (required, not empty)"`
	Notes        *string  `json:"notes,omitempty" jsonschema:"task notes"`
	WhenDate     *string  `json:"when_date,omitempty" jsonschema:"scheduled date, YYYY-MM-DD or 'someday'"`
	HighPriority *bool    `json:"high_priority,omitempty" jsonschema:"mark as high priority"`
	Deadline     *string  `json:"deadline,omitempty" jsonschema:"deadline date, YYYY-MM-DD"`
	ProjectID    *string  `json:"project_id,omitempty" jsonschema:"assign to project (project ID)"`
	AreaID       *string  `json:"area_id,omitempty" jsonschema:"assign to area (area ID)"`
	HeadingID    *string  `json:"heading_id,omitempty" jsonschema:"assign to heading within the project (heading ID)"`
	TagIDs       []string `json:"tag_ids,omitempty" jsonschema:"tag IDs to assign"`
}

// updateTaskIn types update_task so that JSON null and an absent field
// differ: the struct drives the tool's input schema, while the present map
// (captured by UnmarshalJSON) records which fields the caller actually
// sent, including explicit nulls.
type updateTaskIn struct {
	ID           string   `json:"id" jsonschema:"task ID"`
	Title        *string  `json:"title,omitempty" jsonschema:"new title (not empty)"`
	Notes        *string  `json:"notes,omitempty" jsonschema:"new notes"`
	WhenDate     *string  `json:"when_date,omitempty" jsonschema:"new scheduled date (YYYY-MM-DD or 'someday'); null clears it"`
	HighPriority *bool    `json:"high_priority,omitempty" jsonschema:"priority flag"`
	Deadline     *string  `json:"deadline,omitempty" jsonschema:"new deadline (YYYY-MM-DD); null clears it"`
	ProjectID    *string  `json:"project_id,omitempty" jsonschema:"move to project; null removes the project"`
	AreaID       *string  `json:"area_id,omitempty" jsonschema:"move to area; null removes the area"`
	HeadingID    *string  `json:"heading_id,omitempty" jsonschema:"move to heading; null removes the heading"`
	TagIDs       []string `json:"tag_ids,omitempty" jsonschema:"replace tag assignments"`
	present      map[string]json.RawMessage
}

// UnmarshalJSON captures which fields were sent so explicit null can be
// distinguished from an omitted field.
func (in *updateTaskIn) UnmarshalJSON(data []byte) error {
	type alias updateTaskIn
	if err := json.Unmarshal(data, (*alias)(in)); err != nil {
		return err
	}
	return json.Unmarshal(data, &in.present)
}

// body builds the PATCH body, forwarding explicit nulls for the clearable
// fields and omitting everything the caller did not send.
func (in *updateTaskIn) body() map[string]any {
	body := map[string]any{}
	setString := func(name string, value *string) {
		if value != nil {
			body[name] = *value
		} else if _, ok := in.present[name]; ok {
			body[name] = nil
		}
	}
	setBool := func(name string, value *bool) {
		if value != nil {
			body[name] = *value
		} else if _, ok := in.present[name]; ok {
			body[name] = nil
		}
	}
	if in.Title != nil {
		body["title"] = *in.Title
	}
	setString("notes", in.Notes)
	setString("when_date", in.WhenDate)
	setBool("high_priority", in.HighPriority)
	setString("deadline", in.Deadline)
	setString("project_id", in.ProjectID)
	setString("area_id", in.AreaID)
	setString("heading_id", in.HeadingID)
	if in.TagIDs != nil {
		body["tag_ids"] = in.TagIDs
	} else if _, ok := in.present["tag_ids"]; ok {
		body["tag_ids"] = nil
	}
	return body
}

type queryTasksIn struct {
	Status         *string  `json:"status,omitempty" jsonschema:"comma list of open, completed, canceled, wont_do"`
	List           *string  `json:"list,omitempty" jsonschema:"view membership: inbox, today, upcoming, anytime, someday, logbook or trash"`
	ProjectID      *string  `json:"project_id,omitempty" jsonschema:"filter by project ID"`
	AreaID         *string  `json:"area_id,omitempty" jsonschema:"filter by area ID"`
	HeadingID      *string  `json:"heading_id,omitempty" jsonschema:"filter by heading ID"`
	TagIDs         []string `json:"tag_ids,omitempty" jsonschema:"filter by tag IDs"`
	TagMode        *string  `json:"tag_mode,omitempty" jsonschema:"tag matching: any (default) or all"`
	WhenDate       *string  `json:"when_date,omitempty" jsonschema:"exact when date (YYYY-MM-DD or someday)"`
	WhenBefore     *string  `json:"when_before,omitempty" jsonschema:"scheduled before this date (YYYY-MM-DD)"`
	WhenAfter      *string  `json:"when_after,omitempty" jsonschema:"scheduled after this date (YYYY-MM-DD)"`
	DeadlineBefore *string  `json:"deadline_before,omitempty" jsonschema:"deadline on or before this date (YYYY-MM-DD, inclusive)"`
	DeadlineAfter  *string  `json:"deadline_after,omitempty" jsonschema:"deadline on or after this date (YYYY-MM-DD, inclusive)"`
	HasDeadline    *bool    `json:"has_deadline,omitempty" jsonschema:"only tasks with (true) or without (false) a deadline"`
	HasWhen        *bool    `json:"has_when,omitempty" jsonschema:"only tasks with (true) or without (false) a scheduled date"`
	HighPriority   *bool    `json:"high_priority,omitempty" jsonschema:"only high-priority (true) or normal (false) tasks"`
	UpdatedSince   *string  `json:"updated_since,omitempty" jsonschema:"UTC timestamp lower bound (RFC 3339)"`
	CreatedSince   *string  `json:"created_since,omitempty" jsonschema:"UTC timestamp lower bound (RFC 3339)"`
	Search         *string  `json:"search,omitempty" jsonschema:"full-text search terms (all required)"`
	Sort           *string  `json:"sort,omitempty" jsonschema:"manual (default), when_date, deadline, created_at, updated_at or title; prefix - for descending"`
	Limit          *int     `json:"limit,omitempty" jsonschema:"page size 1-500; default 50, always applied"`
	Cursor         *string  `json:"cursor,omitempty" jsonschema:"opaque cursor from the previous page's next_cursor"`
}
