package api

import _ "embed"

//go:embed openapi.yaml
var openapiSpec []byte

// OpenAPISpec returns the embedded OpenAPI document.
func OpenAPISpec() []byte { return openapiSpec }

// Routes returns "METHOD /pattern" for every API route.
func Routes() []string {
	out := make([]string, len(routes))
	for i, rt := range routes {
		out[i] = rt.method + " " + rt.pattern
	}
	return out
}
