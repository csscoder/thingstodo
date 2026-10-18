package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New returns the MCP server (spec B) as an http.Handler for /mcp. Every
// tool call is dispatched in-process to api — the same chi router REST
// clients use — carrying the caller's credentials, so MCP inherits REST
// validation, side effects and errors with no duplicated logic. The
// handler is Stateless per MCP revision 2026-07-28: no session persists
// between requests and one *mcp.Server instance is shared by every
// request, which the Streamable HTTP handler explicitly supports.
func New(api http.Handler, proxyHeader string) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "thingstodo", Version: "1.0"}, nil)
	d := &dispatcher{api: api, proxyHeader: proxyHeader}

	registerTaskTools(srv, d)
	registerListTools(srv, d)
	registerDetailTools(srv, d)
	registerMiscTools(srv, d)

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
}
