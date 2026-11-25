package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func bearer(tok string) string { return "Bearer " + tok }

func authedHarness(t *testing.T, k testKeys) *harness {
	t.Helper()
	v, err := NewJWTVerifier(JWTOptions{PublicKeyPEM: k.pem})
	if err != nil {
		t.Fatal(err)
	}
	return newHarness(t, func(d *Deps) { d.Verifier = v })
}

func TestAuthMissingToken(t *testing.T) {
	h := authedHarness(t, newTestKeys(t))
	for _, hdr := range [][]string{nil, {"Authorization", "Basic abc"}, {"Authorization", "Bearer "}} {
		rec := h.do("GET", "/api/status", nil, hdr...)
		wantError(t, rec, 401, "unauthorized")
	}
}

func TestAuthTokenRejections(t *testing.T) {
	k := newTestKeys(t)
	other := newTestKeys(t)
	h := authedHarness(t, k)
	cases := map[string]string{
		"bad signature": other.token(t, nil),
		"expired":       k.token(t, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"refresh token": k.token(t, func(c jwt.MapClaims) { c["type"] = "refresh" }),
		"missing type":  k.token(t, func(c jwt.MapClaims) { delete(c, "type") }),
		"no expiry":     k.token(t, func(c jwt.MapClaims) { delete(c, "exp") }),
		"no subject":    k.token(t, func(c jwt.MapClaims) { delete(c, "sub") }),
		"garbage":       "not.a.jwt",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			wantError(t, h.do("GET", "/api/status", nil, "Authorization", bearer(tok)), 401, "unauthorized")
		})
	}

	t.Run("hs256 signed with the public key", func(t *testing.T) {
		claims := jwt.MapClaims{"sub": "a", "type": "access", "exp": time.Now().Add(time.Hour).Unix()}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(k.pem))
		if err != nil {
			t.Fatal(err)
		}
		wantError(t, h.do("GET", "/api/status", nil, "Authorization", bearer(tok)), 401, "unauthorized")
	})
	t.Run("alg none", func(t *testing.T) {
		claims := jwt.MapClaims{"sub": "a", "type": "access", "exp": time.Now().Add(time.Hour).Unix()}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatal(err)
		}
		wantError(t, h.do("GET", "/api/status", nil, "Authorization", bearer(tok)), 401, "unauthorized")
	})
}

func TestAuthRoles(t *testing.T) {
	k := newTestKeys(t)
	h := authedHarness(t, k)
	user := k.token(t, func(c jwt.MapClaims) { c["roles"] = []string{"Application_Provider"} })
	admin := k.token(t, nil)

	wantStatus(t, h.do("GET", "/api/status", nil, "Authorization", bearer(user)), 200)
	wantError(t, h.do("POST", "/api/pools", map[string]any{}, "Authorization", bearer(user)), 403, "forbidden")
	wantError(t, h.do("DELETE", "/api/pools/x", nil, "Authorization", bearer(user)), 403, "forbidden")
	wantError(t, h.do("PUT", "/api/oakestra-credentials", map[string]any{}, "Authorization", bearer(user)), 403, "forbidden")
	// The admin passes auth and reaches the handler, which rejects the empty body.
	wantError(t, h.do("POST", "/api/pools", map[string]any{}, "Authorization", bearer(admin)), 400, "validation_failed")
}

func TestAuthHealthzAndUIAreOpen(t *testing.T) {
	h := authedHarness(t, newTestKeys(t))
	wantStatus(t, h.do("GET", "/healthz", nil), 200)
	wantStatus(t, h.do("GET", "/mf-manifest.json", nil), 200)
}

func TestAuth503UntilKeyAvailable(t *testing.T) {
	k := newTestKeys(t)
	v, err := NewJWTVerifier(JWTOptions{GeneratorURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(d *Deps) { d.Verifier = v })
	wantError(t, h.do("GET", "/api/status", nil, "Authorization", bearer(k.token(t, nil))), 503, "unavailable")

	// Without a token the answer is still 401: the caller learns nothing about key state.
	wantError(t, h.do("GET", "/api/status", nil), 401, "unauthorized")

	v.SetKey(&k.priv.PublicKey)
	wantStatus(t, h.do("GET", "/api/status", nil, "Authorization", bearer(k.token(t, nil))), 200)
}

type fakeGenerator struct {
	srv   *httptest.Server
	key   atomic.Pointer[testKeys]
	calls atomic.Int32
}

func newFakeGenerator(t *testing.T, k testKeys) *fakeGenerator {
	t.Helper()
	g := &fakeGenerator{}
	g.key.Store(&k)
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" {
			http.NotFound(w, r)
			return
		}
		g.calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"public_key": g.key.Load().pem})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGeneratorKeyRotationRefetch(t *testing.T) {
	first, second := newTestKeys(t), newTestKeys(t)
	gen := newFakeGenerator(t, first)
	v, err := NewJWTVerifier(JWTOptions{
		GeneratorURL:       gen.srv.URL,
		MinRefetchInterval: 50 * time.Millisecond,
		Logger:             slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.Start(ctx)
	waitFor(t, "initial key", func() bool { return v.key.Load() != nil })

	if _, err := v.Verify(ctx, first.token(t, nil)); err != nil {
		t.Fatalf("token of the first key: %v", err)
	}

	gen.key.Store(&second)
	before := gen.calls.Load()
	if _, err := v.Verify(ctx, second.token(t, nil)); err != nil {
		t.Fatalf("token of the rotated key should verify after a refetch: %v", err)
	}
	if gen.calls.Load() != before+1 {
		t.Fatalf("expected exactly one refetch, got %d", gen.calls.Load()-before)
	}

	// A forged token must not turn into a refetch storm.
	forged := newTestKeys(t).token(t, nil)
	time.Sleep(60 * time.Millisecond)
	before = gen.calls.Load()
	for range 5 {
		if _, err := v.Verify(ctx, forged); err == nil {
			t.Fatal("forged token accepted")
		}
	}
	if got := gen.calls.Load() - before; got != 1 {
		t.Fatalf("refetches for forged tokens = %d, want 1 (rate limited)", got)
	}
}

func TestStaticAndURLKeysAreNotRefetched(t *testing.T) {
	first, second := newTestKeys(t), newTestKeys(t)
	gen := newFakeGenerator(t, first)
	v, err := NewJWTVerifier(JWTOptions{PublicKeyURL: gen.srv.URL + "/key", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.Start(ctx)
	waitFor(t, "key from url", func() bool { return v.key.Load() != nil })

	gen.key.Store(&second)
	if _, err := v.Verify(ctx, second.token(t, nil)); err == nil {
		t.Fatal("rotated key accepted although URL keys are loaded once")
	}
	if n := gen.calls.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
}

func TestJWTPublicKeyEscapedNewlines(t *testing.T) {
	k := newTestKeys(t)
	flat := ""
	for _, r := range k.pem {
		if r == '\n' {
			flat += `\n`
		} else {
			flat += string(r)
		}
	}
	v, err := NewJWTVerifier(JWTOptions{PublicKeyPEM: flat})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), k.token(t, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJWTVerifier(JWTOptions{PublicKeyPEM: "garbage"}); err == nil {
		t.Fatal("garbage key accepted")
	}
}

func TestAuthOff(t *testing.T) {
	h := newHarness(t)
	wantStatus(t, h.do("GET", "/api/status", nil), 200)
	wantError(t, h.do("POST", "/api/pools", map[string]any{}), 400, "validation_failed")
}
