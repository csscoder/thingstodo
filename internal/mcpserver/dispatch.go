package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientIDRE mirrors the REST boundary's client-id rule (handler.isClientID):
// 8-21 characters of letters, digits, underscore or hyphen. Ids are the only
// caller-supplied values interpolated into dispatch paths, and an id failing
// this rule may contain "/" — which would add route segments and reach a
// different endpoint (delete_task with id "x/purge" would dispatch
// DELETE /api/tasks/x/purge and purge task "x" for good).
var clientIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,21}$`)

// idErrorEnvelope matches the API's error body so a rejected id surfaces
// with the same shape whether the API or the dispatcher rejected it.
type idErrorEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

// errMalformedID is the error every id-bearing dispatch returns for an id
// that fails the client-id rule; it never builds a request.
func errMalformedID() error {
	body, _ := json.Marshal(idErrorEnvelope{
		Error: "id must be 8-21 characters of A-Za-z0-9_-",
		Code:  "VALIDATION",
		Field: "id",
	})
	return fmt.Errorf("%s", body)
}

// callID is call for an endpoint addressed by a caller-supplied entity id:
// it builds the path prefix+id+suffix and validates the id first, so a
// malformed id (e.g. one containing "/") fails as a VALIDATION error
// before any request is built and can never misroute the dispatch.
func (d *dispatcher) callID(ctx context.Context, req *mcp.CallToolRequest, method, prefix, id, suffix string, body any) (json.RawMessage, error) {
	if !clientIDRE.MatchString(id) {
		return nil, errMalformedID()
	}
	return call(ctx, d.api, credentials(req, d.proxyHeader), method, prefix+id+suffix, body)
}

// dispatchTimeout bounds one in-process API dispatch from a tool call.
const dispatchTimeout = 60 * time.Second

// dispatcher carries what every tool needs to run an API call: the REST
// router to dispatch to and the header that carries the authenticated user
// in proxy auth mode.
type dispatcher struct {
	api         http.Handler
	proxyHeader string
}

// credentials returns the headers that authenticate an internal API
// request, copied from the HTTP request that carried the tool call (the
// go-sdk exposes it as RequestExtra.Header; nil-safe when absent). Only
// credentials are copied: Authorization, Cookie and the configured auth
// proxy header. Origin is deliberately never forwarded — the outer /mcp
// request already passed the cross-origin checks, and the internal
// request must not re-trigger them.
func credentials(req *mcp.CallToolRequest, proxyHeader string) http.Header {
	out := http.Header{}
	var hdr http.Header
	if req != nil && req.Extra != nil {
		hdr = req.Extra.Header
	}
	if len(hdr) == 0 {
		return out
	}
	names := []string{"Authorization", "Cookie"}
	if proxyHeader != "" {
		names = append(names, proxyHeader)
	}
	for _, name := range names {
		canonical := http.CanonicalHeaderKey(name)
		if values := hdr.Values(canonical); len(values) > 0 {
			out[canonical] = values
		}
	}
	return out
}

// call serves an in-process request against the REST router: same method,
// path (including query string) and JSON body an HTTP client would send,
// authenticated with the caller's credentials (see credentials). The
// request is served via httptest.NewRecorder. It returns the response body
// for 2xx statuses; any other status is an error whose text is the API
// error envelope, so the MCP tool result surfaces both the API error code
// (e.g. VALIDATION) and its message.
//
// The request runs on a fresh context, not the tool call's context. The
// tool call context derives from the outer /mcp request and carries the
// router's in-flight per-request routing state; reusing it for the inner
// request would corrupt that state and misroute the dispatch. Credentials
// travel as headers, so nothing of value is lost by cutting the lineage;
// the bounded timeout keeps a runaway dispatch from outliving the call.
func call(_ context.Context, api http.Handler, hdr http.Header, method, path string, body any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range hdr {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	if rec.Code < 200 || rec.Code > 299 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(rec.Body.String()))
	}
	response := rec.Body.Bytes()
	if len(response) == 0 {
		return nil, nil
	}
	return json.RawMessage(response), nil
}

// toolJSON renders a successful tool result from the API response body.
func toolJSON(body json.RawMessage) *mcp.CallToolResult {
	if len(body) == 0 {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}
}

// toolDeleted is the result for endpoints that answer 204 No Content.
func toolDeleted() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Deleted successfully"}}}
}

// Description fragments shared by many tools. Every create tool states the
// retry rule: repeating the call with the same client-chosen id after a
// lost response returns the existing entity instead of duplicating it.
const (
	descDateFmt  = "Dates are YYYY-MM-DD calendar dates; when_date also accepts the literal 'someday'."
	descTimeFmt  = "Times are HH:MM (00:00-23:59); end_time must be after start_time when both are set."
	descRFC3339  = "Exact timestamps are RFC 3339 with an offset (2026-10-20T09:00:00+03:00 or Z)."
	descNull     = "Pass JSON null to clear a field; omit the field to leave it unchanged."
	descIDFormat = "8-21 characters of A-Za-z0-9_-"
	descRetry    = "Safe to retry: supply a client-chosen id (" + descIDFormat + "); repeating the call with the same id after a lost response returns the existing entity without creating a duplicate."
)
