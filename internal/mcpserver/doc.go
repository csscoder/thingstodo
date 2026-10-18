// Package mcpserver serves the ThingsToDo MCP tools (spec B) from inside
// the Go binary over Streamable HTTP, dispatching every tool call
// in-process to the REST router so MCP and REST share one code path.
package mcpserver
