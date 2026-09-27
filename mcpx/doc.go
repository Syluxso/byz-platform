// Package mcpx will provide Mount: wrap a service-supplied /mcp http.Handler
// with iam.FromRequest and serve the RFC 9728 protected-resource document.
// The MCP SDK stays in the service. See BYZ-48.
package mcpx
