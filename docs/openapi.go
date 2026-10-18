// Package docs embeds the OpenAPI contract that documents the /api surface.
// The embed directive only reaches files in the package directory, so the
// document lives at docs/openapi.yaml and is served at GET /api/openapi.yaml.
package docs

import _ "embed"

// OpenAPI is the raw OpenAPI 3.1 document from openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
