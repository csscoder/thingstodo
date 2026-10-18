package mcpserver

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerDetailTools(srv *mcp.Server, d *dispatcher) {
	// --- Checklist ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_checklist_item",
		Description: "Add a checklist item to a task. " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in addChecklistItemIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"title": in.Title}
		if in.ID != "" {
			body["id"] = in.ID
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tasks/"+in.TaskID+"/checklist", body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_checklist_item",
		Description: "Update a checklist item's title or completion state.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateChecklistItemIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if in.Title != nil {
			body["title"] = *in.Title
		}
		if in.Completed != nil {
			body["completed"] = *in.Completed
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPatch, "/api/checklist/"+in.ItemID, body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_checklist_item",
		Description: "Delete a checklist item.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in itemIDIn) (*mcp.CallToolResult, any, error) {
		_, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodDelete, "/api/checklist/"+in.ItemID, nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Attachments (links) ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_link",
		Description: "Attach a link (URL) to a task. Only http(s) and similar safe schemes are accepted. " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in addLinkIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"url": in.URL}
		if in.ID != "" {
			body["id"] = in.ID
		}
		if in.Title != nil {
			body["title"] = *in.Title
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tasks/"+in.TaskID+"/attachments", body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_attachment",
		Description: "Delete an attachment (link or file) from a task.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in attachmentIDIn) (*mcp.CallToolResult, any, error) {
		_, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodDelete, "/api/attachments/"+in.AttachmentID, nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Schedules ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_schedule",
		Description: "Add a scheduled date entry to a task (a task can carry up to 12). " + descDateFmt + " " + descTimeFmt + " " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in addScheduleIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"when_date": in.WhenDate}
		if in.ID != "" {
			body["id"] = in.ID
		}
		if in.StartTime != nil {
			body["start_time"] = *in.StartTime
		}
		if in.EndTime != nil {
			body["end_time"] = *in.EndTime
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tasks/"+in.TaskID+"/schedules", body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_schedule",
		Description: "Update a schedule entry. " + descDateFmt + " " + descTimeFmt,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateScheduleIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if in.WhenDate != nil {
			body["when_date"] = *in.WhenDate
		}
		if in.StartTime != nil {
			body["start_time"] = *in.StartTime
		}
		if in.EndTime != nil {
			body["end_time"] = *in.EndTime
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPatch, "/api/schedules/"+in.ScheduleID, body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_schedule",
		Description: "Delete a schedule entry.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in scheduleIDIn) (*mcp.CallToolResult, any, error) {
		_, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodDelete, "/api/schedules/"+in.ScheduleID, nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Repeat rules ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_repeat_rule",
		Description: "Get the repeat rule of a task, or a null repeat_rule when the task does not repeat.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskIDIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/tasks/"+in.TaskID+"/repeat", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "set_repeat_rule",
		Description: "Create or replace the repeat rule of a task. Completing a repeating task spawns exactly one next instance. Pattern: type is daily, daily_weekday, daily_weekend, weekly, monthly_dom, monthly_dow, monthly_workday, yearly_date or yearly_dow; every is the interval (at least 1); mode is fixed (next instance computed from the scheduled date) or after_completion (from the completion date). Weekly patterns list weekdays in on (mon..sun). Monthly/dom uses day, monthly/dow uses ordinal plus weekday, monthly_workday uses workday_position first|last, yearly patterns set month (and day for yearly_date). A task without a scheduled date gets its first occurrence assigned. " + descDateFmt,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in setRepeatRuleIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPut, "/api/tasks/"+in.TaskID+"/repeat", map[string]any{"pattern": in.Pattern})
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_repeat_rule",
		Description: "Delete a task's repeat rule; the task itself is untouched.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskIDIn) (*mcp.CallToolResult, any, error) {
		_, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodDelete, "/api/tasks/"+in.TaskID+"/repeat", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Reminders ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_reminders",
		Description: "List the reminders of a task.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskIDIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/tasks/"+in.TaskID+"/reminders", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_reminder",
		Description: "Add a reminder to a task. type is at_start (fires when a timed schedule starts), on_day (value hours, 0-99, at the start of the scheduled day), minutes_before, hours_before or days_before (value 0-99 relative to the schedule start), or exact (value 0; exact_at required). " + descRFC3339 + " " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in addReminderIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"type": in.Type, "value": in.Value}
		if in.ID != "" {
			body["id"] = in.ID
		}
		if in.ExactAt != nil {
			body["exact_at"] = *in.ExactAt
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tasks/"+in.TaskID+"/reminders", body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_reminder",
		Description: "Delete a reminder.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in reminderIDIn) (*mcp.CallToolResult, any, error) {
		_, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodDelete, "/api/reminders/"+in.ReminderID, nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})
}

type taskIDIn struct {
	TaskID string `json:"task_id" jsonschema:"task ID"`
}

type itemIDIn struct {
	ItemID string `json:"item_id" jsonschema:"checklist item ID"`
}

type attachmentIDIn struct {
	AttachmentID string `json:"attachment_id" jsonschema:"attachment ID"`
}

type scheduleIDIn struct {
	ScheduleID string `json:"schedule_id" jsonschema:"schedule entry ID"`
}

type reminderIDIn struct {
	ReminderID string `json:"reminder_id" jsonschema:"reminder ID"`
}

type addChecklistItemIn struct {
	TaskID string `json:"task_id" jsonschema:"task to add the item to"`
	ID     string `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	Title  string `json:"title" jsonschema:"checklist item title (required, not empty)"`
}

type updateChecklistItemIn struct {
	ItemID    string  `json:"item_id" jsonschema:"checklist item ID"`
	Title     *string `json:"title,omitempty" jsonschema:"new title"`
	Completed *bool   `json:"completed,omitempty" jsonschema:"completion state"`
}

type addLinkIn struct {
	TaskID string  `json:"task_id" jsonschema:"task to attach the link to"`
	ID     string  `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	URL    string  `json:"url" jsonschema:"link URL (http/https and similar safe schemes only)"`
	Title  *string `json:"title,omitempty" jsonschema:"link title"`
}

type addScheduleIn struct {
	TaskID    string  `json:"task_id" jsonschema:"task to schedule"`
	ID        string  `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	WhenDate  string  `json:"when_date" jsonschema:"scheduled date, YYYY-MM-DD or 'someday'"`
	StartTime *string `json:"start_time,omitempty" jsonschema:"start time, HH:MM"`
	EndTime   *string `json:"end_time,omitempty" jsonschema:"end time, HH:MM (after start_time)"`
}

type updateScheduleIn struct {
	ScheduleID string  `json:"schedule_id" jsonschema:"schedule entry ID"`
	WhenDate   *string `json:"when_date,omitempty" jsonschema:"new date, YYYY-MM-DD or 'someday'"`
	StartTime  *string `json:"start_time,omitempty" jsonschema:"new start time, HH:MM"`
	EndTime    *string `json:"end_time,omitempty" jsonschema:"new end time, HH:MM"`
}

type setRepeatRuleIn struct {
	TaskID  string          `json:"task_id" jsonschema:"task the rule belongs to"`
	Pattern repeatPatternIn `json:"pattern" jsonschema:"recurrence pattern"`
}

type repeatPatternIn struct {
	Type            string   `json:"type" jsonschema:"daily, daily_weekday, daily_weekend, weekly, monthly_dom, monthly_dow, monthly_workday, yearly_date or yearly_dow"`
	Every           int      `json:"every" jsonschema:"interval, at least 1"`
	Mode            string   `json:"mode" jsonschema:"fixed or after_completion"`
	On              []string `json:"on,omitempty" jsonschema:"weekly: weekdays mon..sun the rule fires on"`
	Day             *int     `json:"day,omitempty" jsonschema:"monthly_dom / yearly_date: day of month"`
	Ordinal         string   `json:"ordinal,omitempty" jsonschema:"monthly_dow / yearly_dow: first, second, third, fourth or last"`
	Weekday         string   `json:"weekday,omitempty" jsonschema:"monthly_dow / yearly_dow: weekday name"`
	WorkdayPosition string   `json:"workday_position,omitempty" jsonschema:"monthly_workday: first or last"`
	Month           *int     `json:"month,omitempty" jsonschema:"yearly_date / yearly_dow: month 1-12"`
}

type addReminderIn struct {
	TaskID  string  `json:"task_id" jsonschema:"task to add the reminder to"`
	ID      string  `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	Type    string  `json:"type" jsonschema:"at_start, on_day, minutes_before, hours_before, days_before or exact"`
	Value   int     `json:"value" jsonschema:"0-99; unit depends on type (minutes/hours/days); 0 for at_start, on_day and exact"`
	ExactAt *string `json:"exact_at,omitempty" jsonschema:"exact: RFC 3339 timestamp with offset"`
}
