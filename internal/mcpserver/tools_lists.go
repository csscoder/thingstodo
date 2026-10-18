package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerListTools(srv *mcp.Server, d *dispatcher) {
	// --- Projects ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_projects",
		Description: "List all projects with their task counts and the area each belongs to.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/projects", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_project",
		Description: "Get one project in detail: its headings and tasks.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in projectIDIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodGet, "/api/projects/", in.ProjectID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_project",
		Description: "Create a new project inside an area (a project always belongs to an area). " + descDateFmt + " " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createProjectIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/projects", in)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_project",
		Description: "Update a project (partial update). " + descDateFmt,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateProjectIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPatch, "/api/projects/", in.ID, "", in.body())
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_project",
		Description: "Delete a project. Its tasks move to the project's area; blocked while headings remain.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in projectIDIn) (*mcp.CallToolResult, any, error) {
		_, err := d.callID(ctx, req, http.MethodDelete, "/api/projects/", in.ProjectID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Areas ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_areas",
		Description: "List all areas, each with its nested projects and their task counts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/areas", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_area",
		Description: "Get one area in detail: its projects and their tasks.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in areaIDIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodGet, "/api/areas/", in.AreaID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_area",
		Description: "Create a new area. " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createAreaIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/areas", in)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_area",
		Description: "Rename an area.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateAreaIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPatch, "/api/areas/", in.ID, "", map[string]any{"title": in.Title})
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_area",
		Description: "Delete an area. Blocked while it still contains projects.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in areaIDIn) (*mcp.CallToolResult, any, error) {
		_, err := d.callID(ctx, req, http.MethodDelete, "/api/areas/", in.AreaID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Tags ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_tags",
		Description: "List all tags with their colors, parents and task counts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodGet, "/api/tags", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_tag",
		Description: "Create a new tag, optionally nested under a parent tag. " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createTagIn) (*mcp.CallToolResult, any, error) {
		body, err := call(ctx, d.api, credentials(req, d.proxyHeader), http.MethodPost, "/api/tags", in)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_tag",
		Description: "Update a tag's title, color or parent.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateTagIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPatch, "/api/tags/", in.ID, "", in.body())
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_tag",
		Description: "Delete a tag; it is removed from every task that carries it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in tagIDIn) (*mcp.CallToolResult, any, error) {
		_, err := d.callID(ctx, req, http.MethodDelete, "/api/tags/", in.TagID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})

	// --- Headings ---

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_headings",
		Description: "List the headings (sections) of a project, in display order.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in projectIDIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodGet, "/api/projects/", in.ProjectID, "/headings", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_heading",
		Description: "Create a heading (section) inside a project. " + descRetry,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createHeadingIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPost, "/api/projects/", in.ProjectID, "/headings", map[string]any{"id": in.ID, "title": in.Title})
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_heading",
		Description: "Rename a heading.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateHeadingIn) (*mcp.CallToolResult, any, error) {
		body, err := d.callID(ctx, req, http.MethodPatch, "/api/headings/", in.ID, "", map[string]any{"title": in.Title})
		if err != nil {
			return nil, nil, err
		}
		return toolJSON(body), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_heading",
		Description: "Delete a heading; its tasks move to the project without a heading.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in headingIDIn) (*mcp.CallToolResult, any, error) {
		_, err := d.callID(ctx, req, http.MethodDelete, "/api/headings/", in.HeadingID, "", nil)
		if err != nil {
			return nil, nil, err
		}
		return toolDeleted(), nil, nil
	})
}

type projectIDIn struct {
	ProjectID string `json:"project_id" jsonschema:"project ID"`
}

type areaIDIn struct {
	AreaID string `json:"area_id" jsonschema:"area ID"`
}

type tagIDIn struct {
	TagID string `json:"tag_id" jsonschema:"tag ID"`
}

type headingIDIn struct {
	HeadingID string `json:"heading_id" jsonschema:"heading ID"`
}

type createProjectIn struct {
	ID       string   `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	Title    string   `json:"title" jsonschema:"project title (required, not empty)"`
	AreaID   string   `json:"area_id" jsonschema:"area the project belongs to (required)"`
	Notes    *string  `json:"notes,omitempty" jsonschema:"project notes"`
	WhenDate *string  `json:"when_date,omitempty" jsonschema:"scheduled date, YYYY-MM-DD or 'someday'"`
	Deadline *string  `json:"deadline,omitempty" jsonschema:"deadline date, YYYY-MM-DD"`
	TagIDs   []string `json:"tag_ids,omitempty" jsonschema:"tag IDs to assign"`
}

// updateProjectIn forwards null-clearable fields with explicit presence,
// mirroring update_task.
type updateProjectIn struct {
	ID       string   `json:"id" jsonschema:"project ID"`
	Title    *string  `json:"title,omitempty" jsonschema:"new title"`
	Notes    *string  `json:"notes,omitempty" jsonschema:"new notes"`
	AreaID   *string  `json:"area_id,omitempty" jsonschema:"move to this area"`
	WhenDate *string  `json:"when_date,omitempty" jsonschema:"new scheduled date; null clears it"`
	Deadline *string  `json:"deadline,omitempty" jsonschema:"new deadline; null clears it"`
	TagIDs   []string `json:"tag_ids,omitempty" jsonschema:"replace tag assignments"`
	present  map[string]json.RawMessage
}

func (in *updateProjectIn) UnmarshalJSON(data []byte) error {
	type alias updateProjectIn
	if err := json.Unmarshal(data, (*alias)(in)); err != nil {
		return err
	}
	return json.Unmarshal(data, &in.present)
}

func (in *updateProjectIn) body() map[string]any {
	body := map[string]any{}
	setString := func(name string, value *string) {
		if value != nil {
			body[name] = *value
		} else if _, ok := in.present[name]; ok {
			body[name] = nil
		}
	}
	if in.Title != nil {
		body["title"] = *in.Title
	}
	if in.Notes != nil {
		body["notes"] = *in.Notes
	}
	setString("area_id", in.AreaID)
	setString("when_date", in.WhenDate)
	setString("deadline", in.Deadline)
	if in.TagIDs != nil {
		body["tag_ids"] = in.TagIDs
	}
	return body
}

type createAreaIn struct {
	ID    string `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	Title string `json:"title" jsonschema:"area title (required, not empty)"`
}

type updateAreaIn struct {
	ID    string `json:"id" jsonschema:"area ID"`
	Title string `json:"title" jsonschema:"new title (required, not empty)"`
}

type createTagIn struct {
	ID          string  `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	Title       string  `json:"title" jsonschema:"tag title (required, not empty)"`
	ParentTagID *string `json:"parent_tag_id,omitempty" jsonschema:"parent tag ID for nesting"`
}

type updateTagIn struct {
	ID          string  `json:"id" jsonschema:"tag ID"`
	Title       *string `json:"title,omitempty" jsonschema:"new title"`
	Color       *string `json:"color,omitempty" jsonschema:"new color"`
	ParentTagID *string `json:"parent_tag_id,omitempty" jsonschema:"new parent tag"`
}

func (in updateTagIn) body() map[string]any {
	body := map[string]any{}
	if in.Title != nil {
		body["title"] = *in.Title
	}
	if in.Color != nil {
		body["color"] = *in.Color
	}
	if in.ParentTagID != nil {
		body["parent_tag_id"] = *in.ParentTagID
	}
	return body
}

type createHeadingIn struct {
	ID        string `json:"id,omitempty" jsonschema:"optional client-chosen id (8-21 characters of A-Za-z0-9_-) for idempotent retries"`
	ProjectID string `json:"project_id" jsonschema:"project to create the heading in"`
	Title     string `json:"title" jsonschema:"heading title (required, not empty)"`
}

type updateHeadingIn struct {
	ID    string `json:"id" jsonschema:"heading ID"`
	Title string `json:"title" jsonschema:"new title (required, not empty)"`
}
