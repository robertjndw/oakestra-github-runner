package api

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestTrailingSlashIsNotRedirected(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/api/pools/", "/api/status/", "/api//status", "/healthz/"} {
		rec := h.do("GET", p, nil)
		if rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("GET %s redirects to %s", p, loc)
		}
	}
	e := wantError(t, h.do("POST", "/api/pools/", map[string]any{}), 404, "not_found")
	if e.Message == "" {
		t.Fatal("empty message")
	}
}

func TestMethodNotAllowedIsJSON(t *testing.T) {
	h := newHarness(t)
	cases := []struct{ method, path, allow string }{
		{"DELETE", "/api/status", "GET, HEAD"},
		{"POST", "/api/pools/p1", "DELETE, GET, HEAD, PUT"},
		{"GET", "/api/oakestra-credentials", "PUT"},
		{"PUT", "/api/pools", "GET, HEAD, POST"},
	}
	for _, tc := range cases {
		rec := h.do(tc.method, tc.path, nil)
		wantError(t, rec, 405, "method_not_allowed")
		if got := rec.Header().Get("Allow"); got != tc.allow {
			t.Errorf("%s %s: Allow %q, want %q", tc.method, tc.path, got, tc.allow)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: content type %q", tc.method, tc.path, ct)
		}
	}
}

func TestUnmatchedAPIRequestsStillNeedAToken(t *testing.T) {
	h := authedHarness(t, newTestKeys(t))
	wantError(t, h.do("GET", "/api/nope", nil), 401, "unauthorized")
	wantError(t, h.do("DELETE", "/api/status", nil), 401, "unauthorized")
}

func TestUnmatchedAPIRequestsWithToken(t *testing.T) {
	k := newTestKeys(t)
	h := authedHarness(t, k)
	admin := bearer(k.token(t, nil))
	wantError(t, h.do("GET", "/api/nope", nil, "Authorization", admin), 404, "not_found")
	wantError(t, h.do("DELETE", "/api/status", nil, "Authorization", admin), 405, "method_not_allowed")
}

func TestNonAPIMethodNotAllowed(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/healthz", nil)
	wantStatus(t, rec, 405)
	if rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
	}
	wantStatus(t, h.do("PUT", "/remoteEntry.js", nil), 405)
}

func TestHeadRequests(t *testing.T) {
	h := newHarness(t)
	rec := h.do("HEAD", "/remoteEntry.js", nil)
	wantStatus(t, rec, 200)
	if rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("body %q, cache %q", rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	wantStatus(t, h.do("HEAD", "/missing.js", nil), 404)
	wantStatus(t, h.do("HEAD", "/healthz", nil), 200)
	wantStatus(t, h.do("HEAD", "/api/status", nil), 200)
}

func TestHeadOnAPIDoesNotNeedAdmin(t *testing.T) {
	k := newTestKeys(t)
	h := authedHarness(t, k)
	user := bearer(k.token(t, func(c jwt.MapClaims) { c["roles"] = []string{"User"} }))
	wantStatus(t, h.do("HEAD", "/api/status", nil, "Authorization", user), 200)
}

// gin prints a banner and warnings to stdout/stderr in debug mode, which would break the JSON
// log stream.
func TestNothingWrittenToStdoutOrStderr(t *testing.T) {
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	restore := func() { os.Stdout, os.Stderr = oldOut, oldErr }
	defer restore()

	t.Setenv("GIN_MODE", "debug")
	h := newHarness(t)
	h.deps.Store = nil
	handler := New(h.deps)
	doRequest(t, handler, "GET", "/api/status", nil)
	doRequest(t, handler, "GET", "/nope", nil)

	restore()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if len(out) != 0 {
		t.Fatalf("unexpected output: %q", strings.TrimSpace(string(out)))
	}
}
