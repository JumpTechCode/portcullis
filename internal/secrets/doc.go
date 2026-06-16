// Package secrets injects gateway-held downstream credentials.
//
// It supports connection-level injection (stdio environment variables or HTTP
// headers) and optional argument-level injection (filling a tool's credential
// parameter and stripping it from the client-facing schema). Injected values
// are registered with the outbound redactor so they cannot leak back to
// clients. Clients never hold downstream credentials.
package secrets
