// This file implements the remote Streamable HTTP downstream session and its
// factory. Unlike the stdio path (stdio.go, unix-only), an HTTP downstream is a
// dialed network connection with no subprocess to supervise, so this file
// carries no build constraint. The transport-neutral protocol behavior is shared
// with stdio via the embedded sdkSession (session.go); this file adds only the
// HTTP transport wiring and connection-level header injection.

package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpSession is a DownstreamSession backed by a remote MCP server reached over
// the Streamable HTTP transport. It embeds the shared sdkSession for the
// protocol behavior and adds only its own idempotent teardown — there is no
// subprocess to reap, so Close just closes the SDK session.
type httpSession struct {
	sdkSession

	closeOnce onceCloser
}

// Compile-time assertion that httpSession satisfies DownstreamSession.
var _ DownstreamSession = (*httpSession)(nil)

// Close shuts the downstream session down. It is idempotent: the SDK session is
// closed once, and the recorded error is returned on every call.
func (s *httpSession) Close() error {
	return s.closeOnce.do(s.closeSession)
}

// HTTPConfig configures a remote Streamable HTTP downstream transport.
type HTTPConfig struct {
	// URL is the downstream's MCP endpoint (the Streamable HTTP endpoint).
	URL string
	// Headers are connection-level headers set on every request to the
	// downstream, including any resolved connection-level secrets such as an
	// Authorization bearer token. The composition root resolves each declared
	// secret's env var to a value and passes it here, so the client never holds
	// the credential (ADR-0012). The gateway never reads these back to a client.
	Headers map[string]string
	// HTTPClient optionally overrides the HTTP client backing the transport (for
	// example to set timeouts or a custom dialer). When nil a zero-value client is
	// used. Configured Headers are injected regardless of which client backs it.
	HTTPClient *http.Client
}

// HTTPFactory creates remote-HTTP DownstreamSessions and satisfies the pool's
// Factory interface, so a supervised pool of remote sessions is just
// NewPool(NewHTTPFactory(cfg), poolCfg) — the same composition as stdio.
type HTTPFactory struct {
	cfg HTTPConfig
}

// Compile-time assertion that HTTPFactory satisfies Factory.
var _ Factory = (*HTTPFactory)(nil)

// NewHTTPFactory returns a factory that dials the configured downstream endpoint
// for each new session.
func NewHTTPFactory(cfg HTTPConfig) *HTTPFactory {
	return &HTTPFactory{cfg: cfg}
}

// New dials the downstream endpoint, performs the MCP handshake, and returns a
// ready DownstreamSession. Connection-level headers (including injected secrets)
// are applied to every request via a wrapping RoundTripper, so the credential
// reaches the downstream without ever being supplied by the client. If the
// handshake fails the SDK tears the transport down before New returns the error.
func (f *HTTPFactory) New(ctx context.Context) (Session, error) {
	if f.cfg.URL == "" {
		return nil, errors.New("http factory: empty URL")
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:   f.cfg.URL,
		HTTPClient: httpClientWithHeaders(f.cfg.HTTPClient, f.cfg.Headers),
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "portcullis", Version: "dev"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect downstream %q: %w", f.cfg.URL, err)
	}
	return &httpSession{sdkSession: sdkSession{cs: cs}}, nil
}

// httpClientWithHeaders returns an HTTP client that injects the given headers on
// every request. When there are no headers the base client (or a zero-value one)
// is used unchanged. The base client is never mutated: the returned client is a
// shallow copy whose transport is wrapped, so a client passed in by the caller
// keeps its own transport.
func httpClientWithHeaders(base *http.Client, headers map[string]string) *http.Client {
	var c http.Client
	if base != nil {
		c = *base // shallow copy; we replace Transport on the copy only
	}
	if len(headers) == 0 {
		return &c
	}
	inner := c.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	// Copy the header map so later mutation of the caller's map cannot change
	// what this session sends.
	hdr := make(map[string]string, len(headers))
	for k, v := range headers {
		hdr[k] = v
	}
	c.Transport = &headerRoundTripper{base: inner, headers: hdr}
	return &c
}

// headerRoundTripper sets a fixed set of headers on every request before
// delegating to a base RoundTripper. It carries connection-level secrets (an
// Authorization header, for example) so the client never has to supply them.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

// RoundTrip injects the configured headers and delegates. The RoundTripper
// contract forbids modifying the supplied request, so it operates on a clone.
func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range h.headers {
		clone.Header.Set(k, v)
	}
	return h.base.RoundTrip(clone)
}
