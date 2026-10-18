package mcpserver

import (
	"context"
	"net/http"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerMiscTools(srv *mcp.Server, d *dispatcher) {
	// --- Views ---

	view := func(name, description, path string, withFrom bool) {
		if !withFrom {
			mcp.AddTool(srv, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
				body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, path, nil)
				if err != nil {
					return nil, nil, err
				}
				return toolJSON(body), nil, nil
			})
			return
		}
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in fromDateIn) (*mcp.CallToolResult, any, error) {
			requestPath := path
			if in.From != nil {
				requestPath += "?from=" + *in.From
			}
			body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, requestPath, nil)
			if err != nil {
				return nil, nil, err
			}
			return toolJSON(body), nil, nil
		})
	}
	view("get_today", "Today's tasks grouped by section (overdue, today, this evening), including deadline-only tasks due today.", "/api/views/today", false)
	view("get_upcoming", "Upcoming scheduled tasks grouped by date; a task with several schedule entries appears on each date. Optionally start from a given date (YYYY-MM-DD, defaults to today).", "/api/views/upcoming", true)
	view("get_inbox", "Inbox tasks (no project, area or schedule) plus the review section of stale tasks.", "/api/views/inbox", false)
	view("get_anytime", "Tasks without a scheduled date, grouped by area and project.", "/api/views/anytime", false)
	view("get_someday", "Someday tasks (scheduled with the literal date 'someday').", "/api/views/someday", false)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_logbook",
		Description: "Completed, canceled and won't-do tasks, newest first. " + descRFC3339,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, any, error) {
		q := ""
		if in.Limit != nil || in.Offset != nil {
			q = "?"
			if in.Limit != nil {
				q += "limit=" + strconv.Itoa(*in.Limit)
			}
			if in.Offset != nil {
				if q != "?" {
					q += "&"
				}
				q += "offset=" + strconv.Itoa(*in.Offset)
			}
		}
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/views/logbook"+q, nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_counts",
		Description: "Sidebar counts per smart list (inbox, today, upcoming, anytime, someday, logbook, trash, review), each counting a task once even when it appears on several rows.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/views/counts", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	// --- User settings ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_settings",
		Description: "Get the signed-in user's settings (review cadence, evening threshold, time format, notification delivery, and more).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/user/settings", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_settings",
		Description: "Update settings (partial update; send only the fields to change). evening_starts_at and default_time_gap are HH:MM / minute counts as documented per field; time_format is 12h or 24h; font_size is 12-20; notification_provider is webpush, ntfy or none. " + descTimeFmt,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateSettingsIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPatch, "/api/user/settings", in)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	// --- Bulk ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bulk_action",
		Description: "Apply one action to up to 100 tasks at once. Actions and their params: complete/cancel/wontdo/delete (no params); set_when {when_date: YYYY-MM-DD or 'someday', empty string clears}; set_deadline {deadline: YYYY-MM-DD, empty string clears}; set_priority {high_priority: boolean}; toggle_priority (no params); move_project {project_id: id, empty string removes}; add_tags/remove_tags/toggle_tags {tag_ids: [ids]}; mark_reviewed (no params). Status actions run the same guarded finishing path as the single-task endpoints, so a repeating task spawns exactly one next instance.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in bulkActionIn) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"task_ids": in.TaskIDs, "action": in.Action}
		if in.Params != nil {
			body["params"] = in.Params
		}
		out, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tasks/bulk", body)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(out), nil, nil
	})
}

type fromDateIn struct {
	From *string `json:"from,omitempty" jsonschema:"start date, YYYY-MM-DD (defaults to today)"`
}

type pageIn struct {
	Limit  *int `json:"limit,omitempty" jsonschema:"page size, 1-500 (default 50)"`
	Offset *int `json:"offset,omitempty" jsonschema:"pagination offset"`
}

type updateSettingsIn struct {
	PlayCompleteSound        *bool   `json:"play_complete_sound,omitempty" jsonschema:"play a sound when a task completes"`
	ShowCountMain            *bool   `json:"show_count_main,omitempty" jsonschema:"show task counts on smart lists"`
	ShowCountProjects        *bool   `json:"show_count_projects,omitempty" jsonschema:"show task counts on projects"`
	ShowCountTags            *bool   `json:"show_count_tags,omitempty" jsonschema:"show task counts on tags"`
	ReviewAfterDays          *int    `json:"review_after_days,omitempty" jsonschema:"days after which an untouched task surfaces in Review"`
	SortAreas                *string `json:"sort_areas,omitempty" jsonschema:"area order: manual, a-z or z-a"`
	SortTags                 *string `json:"sort_tags,omitempty" jsonschema:"tag order: manual, a-z or z-a"`
	EveningStartsAt          *string `json:"evening_starts_at,omitempty" jsonschema:"HH:MM when This Evening starts in Today"`
	DefaultTimeGap           *int    `json:"default_time_gap,omitempty" jsonschema:"default duration in minutes applied to new end times"`
	ShowTimeBadge            *bool   `json:"show_time_badge,omitempty" jsonschema:"show the scheduled-time badge in lists"`
	TimeFormat               *string `json:"time_format,omitempty" jsonschema:"12h or 24h"`
	FontSize                 *int    `json:"font_size,omitempty" jsonschema:"base font size, 12-20"`
	DefaultReminderType      *string `json:"default_reminder_type,omitempty" jsonschema:"default for new tasks: at_start, on_day, minutes_before, hours_before, days_before"`
	DefaultReminderValue     *int    `json:"default_reminder_value,omitempty" jsonschema:"default reminder offset, 0-99"`
	CopyRemindersToRecurring *bool   `json:"copy_reminders_to_recurring,omitempty" jsonschema:"copy reminders to new recurring instances"`
	NotificationProvider     *string `json:"notification_provider,omitempty" jsonschema:"webpush, ntfy or none"`
	NtfyServerURL            *string `json:"ntfy_server_url,omitempty" jsonschema:"ntfy server base URL"`
	NtfyTopic                *string `json:"ntfy_topic,omitempty" jsonschema:"ntfy topic name"`
	NtfyAccessToken          *string `json:"ntfy_access_token,omitempty" jsonschema:"ntfy access token, if the server requires auth"`
	BaseURL                  *string `json:"base_url,omitempty" jsonschema:"public app URL used in notification links"`
	PrivacyMode              *bool   `json:"privacy_mode,omitempty" jsonschema:"blur sensitive text in the UI"`
	ReviewIncludeRecurring   *bool   `json:"review_include_recurring,omitempty" jsonschema:"include recurring tasks in Review"`
}

type bulkActionIn struct {
	TaskIDs []string       `json:"task_ids" jsonschema:"task IDs (1-100)"`
	Action  string         `json:"action" jsonschema:"complete, cancel, wontdo, delete, set_when, set_deadline, set_priority, toggle_priority, move_project, add_tags, remove_tags, toggle_tags or mark_reviewed"`
	Params  map[string]any `json:"params,omitempty" jsonschema:"action-specific parameters, e.g. {\"high_priority\": true} for set_priority"`
}
