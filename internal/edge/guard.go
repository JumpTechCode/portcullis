// Package edge is the client-facing side of the gateway.
//
// It serves the Streamable HTTP MCP endpoint and guards it: every request is
// first validated for a permitted Origin (a DNS-rebinding defense, design §8)
// and then authenticated by a static API key resolved to a client identity
// (ADR-0003). The request guard is ordinary net/http middleware so it composes
// in front of the MCP handler; on success it carries the authenticated
// [domain.Identity] forward in the request context, and on failure it rejects at
// the door without invoking the wrapped handler.
package edge

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// ClientKey binds a configured client identity to its resolved API-key value.
// The composition root resolves each client's api_key_env to a value at startup
// and hands the pairs here; this package never reads environment itself.
type ClientKey struct {
	// ID is the client identity (for example "claude-desktop").
	ID string
	// Key is the secret API-key value that authenticates that identity.
	Key string
}

// clientDigest binds a client identity to the SHA-256 digest of its API key.
// Authentication compares these fixed-width digests rather than the raw keys, so
// the comparison cost is the same for every client and the response time cannot
// vary with a key's length (#24).
type clientDigest struct {
	id     string
	digest [sha256.Size]byte
}

// Guard is the edge request guard: Origin/DNS-rebinding validation followed by
// constant-time API-key authentication. It is safe for concurrent use, and its
// allowed origins and client keys are hot-reloadable: Reload swaps them
// atomically behind a read-write mutex so a SIGHUP config reload never drops a
// request in flight (design §5).
type Guard struct {
	mu             sync.RWMutex
	allowedOrigins []string
	digests        []clientDigest
}

// NewGuard builds a request guard from the permitted Origins and the resolved
// client keys. An Origin entry may end in "*" to match any Origin sharing the
// prefix before it (for example "vscode-webview://*"); other entries match
// exactly. The inputs are copied so later mutation by the caller cannot change
// the guard's decisions.
func NewGuard(allowedOrigins []string, clients []ClientKey) *Guard {
	g := &Guard{}
	g.set(allowedOrigins, clients)
	return g
}

// Reload atomically replaces the guard's allowed origins and client keys for a
// SIGHUP/file-watch config reload (design §5). Requests already in flight finish
// against whichever snapshot they read; subsequent requests see the new one. It
// is safe to call concurrently with request handling.
func (g *Guard) Reload(allowedOrigins []string, clients []ClientKey) {
	g.set(allowedOrigins, clients)
}

// set copies the origins and reduces each client key to its SHA-256 digest under
// the write lock, so a later mutation of the caller's slices cannot change the
// guard's decisions and the raw key bytes are not retained beyond construction.
func (g *Guard) set(allowedOrigins []string, clients []ClientKey) {
	origins := make([]string, len(allowedOrigins))
	copy(origins, allowedOrigins)
	digests := make([]clientDigest, len(clients))
	for i, c := range clients {
		digests[i] = clientDigest{id: c.ID, digest: sha256.Sum256([]byte(c.Key))}
	}

	g.mu.Lock()
	g.allowedOrigins = origins
	g.digests = digests
	g.mu.Unlock()
}

// snapshot returns the current origins and client digests under the read lock.
// The returned slices are the guard's own backing arrays, which set never
// mutates in place (it always assigns fresh slices), so reading them after the
// lock is released is safe against a concurrent Reload.
func (g *Guard) snapshot() (origins []string, digests []clientDigest) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.allowedOrigins, g.digests
}

// Wrap returns a handler that applies the guard before delegating to next. A
// request with a disallowed Origin is rejected with 403; a request without a
// valid Bearer API key is rejected with 401 (and a WWW-Authenticate challenge).
// Only a request that passes both checks reaches next, with the authenticated
// identity injected into its context.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Origin first: reject a cross-origin (DNS-rebinding) attempt at the door,
		// before doing any credential work.
		if !g.originAllowed(r.Header.Get("Origin")) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		key, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w)
			return
		}
		identity, ok := g.authenticate(key)
		if !ok {
			unauthorized(w)
			return
		}

		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
	})
}

// originAllowed reports whether origin is permitted. An empty Origin is allowed:
// non-browser clients omit it, and the DNS-rebinding vector it defends against
// is browser-only (the localhost bind is the primary defense). A present Origin
// must match an allowlist entry exactly, or match the prefix of a "*"-suffixed
// entry.
func (g *Guard) originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	allowedOrigins, _ := g.snapshot()
	for _, allowed := range allowedOrigins {
		if prefix, wild := strings.CutSuffix(allowed, "*"); wild {
			if strings.HasPrefix(origin, prefix) {
				return true
			}
		} else if origin == allowed {
			return true
		}
	}
	return false
}

// authenticate resolves a presented API key to its client identity using a
// constant-time comparison against every configured key. It deliberately does
// not stop at the first match: comparing all entries keeps the work independent
// of which client matched (and of whether any did), so the response time does
// not leak how far a guessed key got.
//
// The comparison is over SHA-256 digests, not the raw keys. Digests are always
// 32 bytes, so every comparison is equal-width; this removes the residual signal
// that subtle.ConstantTimeCompare returns early when two slices differ in length,
// which otherwise let the comparison cost vary with the presented key's length
// relative to the configured keys' (#24). A non-match returns the zero identity
// and false.
func (g *Guard) authenticate(presented string) (domain.Identity, bool) {
	_, digests := g.snapshot()
	presentedDigest := sha256.Sum256([]byte(presented))
	var id string
	found := 0
	for _, c := range digests {
		if subtle.ConstantTimeCompare(presentedDigest[:], c.digest[:]) == 1 {
			id = c.id
			found = 1
		}
	}
	if found == 0 {
		return domain.Identity{}, false
	}
	return domain.Identity{ID: id}, true
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header
// value. The scheme is matched case-insensitively (RFC 7235 §2.1). It returns
// false for a missing header, a non-Bearer scheme, or an empty token.
func bearerToken(authorization string) (string, bool) {
	const scheme = "bearer "
	if len(authorization) <= len(scheme) || !strings.EqualFold(authorization[:len(scheme)], scheme) {
		return "", false
	}
	token := strings.TrimSpace(authorization[len(scheme):])
	if token == "" {
		return "", false
	}
	return token, true
}

// unauthorized writes a 401 with a Bearer challenge (RFC 6750 §3).
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
