package edge_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/edge"
)

// testClients is a small, fixed client set used across the guard tests.
func testClients() []edge.ClientKey {
	return []edge.ClientKey{
		{ID: "claude-desktop", Key: "key-claude-desktop"},
		{ID: "ci-bot", Key: "key-ci-bot"},
	}
}

// identityRecorder is a next handler that records the identity the guard placed
// in the request context and reports 200, so a test can assert what reached the
// inner handler.
func identityRecorder(t *testing.T, got *domain.Identity, reached *bool) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		id, ok := edge.IdentityFromContext(r.Context())
		if !ok {
			t.Error("next handler reached without an identity in context")
		}
		*got = id
		w.WriteHeader(http.StatusOK)
	})
}

func bearer(key string) string { return "Bearer " + key }

func TestGuardAcceptsValidKeyAndInjectsIdentity(t *testing.T) {
	var got domain.Identity
	var reached bool
	h := edge.NewGuard([]string{"http://localhost"}, testClients()).
		Wrap(identityRecorder(t, &got, &reached))

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
	req.Header.Set("Origin", "http://localhost")
	req.Header.Set("Authorization", bearer("key-ci-bot"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !reached {
		t.Fatal("next handler was not reached for a valid request")
	}
	if got.ID != "ci-bot" {
		t.Errorf("identity = %q, want %q", got.ID, "ci-bot")
	}
}

func TestGuardRejectsDisallowedOrigin(t *testing.T) {
	var reached bool
	noNext := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	h := edge.NewGuard([]string{"http://localhost", "vscode-webview://*"}, testClients()).Wrap(noNext)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Authorization", bearer("key-ci-bot")) // valid key must not save a bad origin
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for disallowed origin", rec.Code)
	}
	if reached {
		t.Error("next handler was reached despite a disallowed origin")
	}
}

func TestGuardOriginMatching(t *testing.T) {
	allowed := []string{"http://localhost", "vscode-webview://*"}
	cases := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},                                // non-browser client omits Origin
		{"http://localhost", http.StatusOK},                // exact
		{"vscode-webview://abcdef123", http.StatusOK},      // scheme wildcard
		{"http://localhost:9999", http.StatusForbidden},    // port differs → not exact
		{"https://localhost", http.StatusForbidden},        // scheme differs
		{"https://evil.example.com", http.StatusForbidden}, // unrelated
	}
	for _, tc := range cases {
		var reached bool
		h := edge.NewGuard(allowed, testClients()).
			Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
		req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		req.Header.Set("Authorization", bearer("key-ci-bot"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("origin %q: status = %d, want %d", tc.origin, rec.Code, tc.want)
		}
		if tc.want == http.StatusForbidden && reached {
			t.Errorf("origin %q: next reached despite expected rejection", tc.origin)
		}
	}
}

func TestGuardRejectsMissingKey(t *testing.T) {
	var reached bool
	h := edge.NewGuard([]string{"http://localhost"}, testClients()).
		Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
	req.Header.Set("Origin", "http://localhost")
	// no Authorization header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for missing key", rec.Code)
	}
	if reached {
		t.Error("next handler reached despite missing key")
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("401 response missing WWW-Authenticate header")
	}
}

func TestGuardRejectsBadKey(t *testing.T) {
	var reached bool
	h := edge.NewGuard([]string{"http://localhost"}, testClients()).
		Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
	req.Header.Set("Origin", "http://localhost")
	req.Header.Set("Authorization", bearer("not-a-real-key"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for bad key", rec.Code)
	}
	if reached {
		t.Error("next handler reached despite a bad key")
	}
}

// TestGuardAuthenticatesVariableLengthKeys pins the behavior that constant-time
// authentication must preserve regardless of how keys are compared: keys of very
// different lengths each resolve to their own identity, and a wrong key — even
// one that shares a real key's length — is rejected. The digest-based comparison
// (fixed-width, length-independent) must satisfy exactly this.
func TestGuardAuthenticatesVariableLengthKeys(t *testing.T) {
	clients := []edge.ClientKey{
		{ID: "short", Key: "x"},
		{ID: "long", Key: "a-considerably-longer-api-key-value-0123456789"},
	}

	serve := func(key string) (domain.Identity, int) {
		var got domain.Identity
		var reached bool
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", http.NoBody)
		req.Header.Set("Origin", "http://localhost")
		req.Header.Set("Authorization", bearer(key))
		rec := httptest.NewRecorder()
		edge.NewGuard([]string{"http://localhost"}, clients).
			Wrap(identityRecorder(t, &got, &reached)).ServeHTTP(rec, req)
		return got, rec.Code
	}

	if id, code := serve("x"); code != http.StatusOK || id.ID != "short" {
		t.Errorf("short key: identity=%q status=%d, want \"short\"/200", id.ID, code)
	}
	if id, code := serve("a-considerably-longer-api-key-value-0123456789"); code != http.StatusOK || id.ID != "long" {
		t.Errorf("long key: identity=%q status=%d, want \"long\"/200", id.ID, code)
	}
	// Wrong key the same length as the "short" client's key must still fail.
	if _, code := serve("y"); code != http.StatusUnauthorized {
		t.Errorf("wrong same-length key: status=%d, want 401", code)
	}
}

func TestGuardRejectsMalformedAuthScheme(t *testing.T) {
	for _, authz := range []string{
		"key-ci-bot",       // no scheme
		"Basic key-ci-bot", // wrong scheme
		"Bearer",           // scheme without token
		"Bearer ",          // scheme, no token
		"Bearer    ",       // scheme, whitespace-only token
	} {
		var reached bool
		h := edge.NewGuard([]string{"http://localhost"}, testClients()).
			Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
		req.Header.Set("Origin", "http://localhost")
		req.Header.Set("Authorization", authz)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("authorization %q: status = %d, want 401", authz, rec.Code)
		}
		if reached {
			t.Errorf("authorization %q: next reached despite malformed scheme", authz)
		}
	}
}

func TestGuardAcceptsCaseInsensitiveBearerScheme(t *testing.T) {
	var reached bool
	h := edge.NewGuard([]string{"http://localhost"}, testClients()).
		Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp", http.NoBody)
	req.Header.Set("Origin", "http://localhost")
	req.Header.Set("Authorization", "bearer key-ci-bot") // lowercase scheme is valid per RFC 7235
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for lowercase bearer scheme", rec.Code)
	}
	if !reached {
		t.Error("next not reached for lowercase bearer scheme")
	}
}

func TestIdentityFromContextAbsent(t *testing.T) {
	if _, ok := edge.IdentityFromContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody).Context()); ok {
		t.Error("IdentityFromContext reported an identity on a bare context")
	}
}

func TestGuardReloadSwapsClientsAndOrigins(t *testing.T) {
	guard := edge.NewGuard([]string{"http://old"}, []edge.ClientKey{{ID: "a", Key: "k1"}})

	serve := func(key, origin string) int {
		var reached bool
		var got domain.Identity
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", http.NoBody)
		req.Header.Set("Origin", origin)
		req.Header.Set("Authorization", bearer(key))
		rec := httptest.NewRecorder()
		guard.Wrap(identityRecorder(t, &got, &reached)).ServeHTTP(rec, req)
		return rec.Code
	}

	if serve("k1", "http://old") != http.StatusOK {
		t.Fatal("precondition: original key + origin should pass")
	}

	guard.Reload([]string{"http://new"}, []edge.ClientKey{{ID: "b", Key: "k2"}})

	if code := serve("k2", "http://new"); code != http.StatusOK {
		t.Errorf("after reload, new key + origin: status = %d, want 200", code)
	}
	if code := serve("k1", "http://new"); code != http.StatusUnauthorized {
		t.Errorf("after reload, the removed key should be rejected: status = %d, want 401", code)
	}
	if code := serve("k2", "http://old"); code != http.StatusForbidden {
		t.Errorf("after reload, the removed origin should be rejected: status = %d, want 403", code)
	}
}

func TestGuardReloadIsConcurrencySafe(t *testing.T) {
	guard := edge.NewGuard([]string{"http://localhost"}, []edge.ClientKey{{ID: "a", Key: "k1"}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := guard.Wrap(next)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			guard.Reload([]string{"http://localhost"}, []edge.ClientKey{{ID: "a", Key: "k1"}})
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", http.NoBody)
		req.Header.Set("Origin", "http://localhost")
		req.Header.Set("Authorization", bearer("k1"))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	<-done
}
