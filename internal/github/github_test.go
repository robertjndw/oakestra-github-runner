package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

func TestParseConfigURL(t *testing.T) {
	cases := []struct {
		in      string
		want    ConfigURL
		api     string
		wantErr bool
	}{
		{in: "https://github.com/acme", want: ConfigURL{Scheme: "https", Host: "github.com", Owner: "acme"}, api: "https://api.github.com"},
		{in: "https://github.com/acme/", want: ConfigURL{Scheme: "https", Host: "github.com", Owner: "acme"}, api: "https://api.github.com"},
		{in: " https://GitHub.com/acme/widgets.git ", want: ConfigURL{Scheme: "https", Host: "github.com", Owner: "acme", Repo: "widgets"}, api: "https://api.github.com"},
		{in: "https://ghe.example.com/team/repo.x", want: ConfigURL{Scheme: "https", Host: "ghe.example.com", Owner: "team", Repo: "repo.x"}, api: "https://ghe.example.com/api/v3"},
		{in: "https://acme.ghe.com/team", want: ConfigURL{Scheme: "https", Host: "acme.ghe.com", Owner: "team"}, api: "https://api.acme.ghe.com"},
		{in: "", wantErr: true},
		{in: "github.com/acme", wantErr: true},
		{in: "ftp://github.com/acme", wantErr: true},
		{in: "https://github.com/", wantErr: true},
		{in: "https://github.com/a/b/c", wantErr: true},
		{in: "https://github.com/enterprises/big", wantErr: true},
		{in: "https://github.com/acme?x=1", wantErr: true},
		{in: "https://user:pw@github.com/acme", wantErr: true},
		{in: "https://github.com/-bad", wantErr: true},
		{in: "https://github.com/acme/bad name", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseConfigURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseConfigURL(%q) = %+v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseConfigURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseConfigURL(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.APIBaseURL() != tc.api {
			t.Errorf("APIBaseURL(%q) = %s, want %s", tc.in, got.APIBaseURL(), tc.api)
		}
	}
}

func newTestApps(t *testing.T, h http.Handler) *Apps {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := NewApps(srv.Client())
	a.APIBaseURL = srv.URL
	return a
}

func ownerHandler(types map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login := strings.TrimPrefix(r.URL.Path, "/users/")
		typ, ok := types[login]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprintf(w, `{"login":%q,"type":%q}`, login, typ)
	}
}

func TestManifest(t *testing.T) {
	a := newTestApps(t, ownerHandler(map[string]string{"acme": "Organization", "alice": "User"}))
	ctx := context.Background()
	redirect := "http://dashboard.local/addons/runner"

	t.Run("organization", func(t *testing.T) {
		m, err := a.Manifest(ctx, "oak-runner", "https://github.com/acme", redirect, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if m.PostURL != "https://github.com/organizations/acme/settings/apps/new?state=s1" {
			t.Errorf("PostURL = %s", m.PostURL)
		}
		perms := m.Manifest["default_permissions"].(map[string]string)
		if perms["organization_self_hosted_runners"] != "write" || perms["metadata"] != "read" || len(perms) != 2 {
			t.Errorf("permissions = %v", perms)
		}
		hook := m.Manifest["hook_attributes"].(map[string]any)
		if hook["active"] != false {
			t.Errorf("webhook must be inactive: %v", hook)
		}
		if m.Manifest["public"] != false || m.Manifest["redirect_url"] != redirect || m.Manifest["name"] != "oak-runner" {
			t.Errorf("manifest = %v", m.Manifest)
		}
		if m.State != "s1" {
			t.Errorf("state = %s", m.State)
		}
	})

	t.Run("user repository", func(t *testing.T) {
		m, err := a.Manifest(ctx, "x", "https://github.com/alice/repo", redirect, "a b")
		if err != nil {
			t.Fatal(err)
		}
		if m.PostURL != "https://github.com/settings/apps/new?state=a+b" {
			t.Errorf("PostURL = %s", m.PostURL)
		}
		perms := m.Manifest["default_permissions"].(map[string]string)
		if perms["administration"] != "write" || perms["metadata"] != "read" || len(perms) != 2 {
			t.Errorf("permissions = %v", perms)
		}
	})

	t.Run("org repository", func(t *testing.T) {
		m, err := a.Manifest(ctx, "x", "https://github.com/acme/repo", redirect, "s")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(m.PostURL, "https://github.com/organizations/acme/") {
			t.Errorf("PostURL = %s", m.PostURL)
		}
		if m.Manifest["default_permissions"].(map[string]string)["administration"] != "write" {
			t.Errorf("manifest = %v", m.Manifest)
		}
	})

	t.Run("user account at org level", func(t *testing.T) {
		if _, err := a.Manifest(ctx, "x", "https://github.com/alice", redirect, "s"); err == nil {
			t.Fatal("want error for a personal account config URL")
		}
	})
	t.Run("unknown owner", func(t *testing.T) {
		if _, err := a.Manifest(ctx, "x", "https://github.com/ghost", redirect, "s"); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad redirect", func(t *testing.T) {
		if _, err := a.Manifest(ctx, "x", "https://github.com/acme", "/relative", "s"); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestExchangeManifestCode(t *testing.T) {
	a := newTestApps(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		switch r.URL.Path {
		case "/app-manifests/good/conversions":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":42,"slug":"oak","client_id":"Iv1.abc","html_url":"https://github.com/apps/oak","pem":"PEM","webhook_secret":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	got, err := a.ExchangeManifestCode(context.Background(), "https://github.com/acme", "good")
	if err != nil {
		t.Fatal(err)
	}
	want := core.AppCredentials{AppID: 42, Slug: "oak", ClientID: "Iv1.abc", HTMLURL: "https://github.com/apps/oak", PrivateKey: "PEM"}
	if got != want {
		t.Errorf("got %+v", got)
	}
	if _, err := a.ExchangeManifestCode(context.Background(), "https://github.com/acme", "expired"); err == nil || !strings.Contains(err.Error(), "expires") {
		t.Errorf("err = %v", err)
	}
	if _, err := a.ExchangeManifestCode(context.Background(), "https://github.com/acme", "../x"); err == nil {
		t.Error("want error for a code with path characters")
	}
}

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return key, string(p)
}

func TestListInstallations(t *testing.T) {
	key, keyPEM := testKey(t)
	var srvURL string
	a := newTestApps(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		tok, err := jwt.ParseWithClaims(raw, &jwt.RegisteredClaims{}, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
			jwt.WithValidMethods([]string{"RS256"}))
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims := tok.Claims.(*jwt.RegisteredClaims)
		if claims.Issuer != "7" || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 10*time.Minute {
			t.Errorf("claims = %+v", claims)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/app/installations?per_page=100&page=%d>; rel="next", <%s/app/installations?page=3>; rel="last"`, srvURL, page+1, srvURL))
		}
		fmt.Fprintf(w, `[{"id":%d,"account":{"login":"org%d"},"target_type":"Organization","html_url":"h%d"}]`, page, page, page)
	}))
	srvURL = a.APIBaseURL

	got, err := a.ListInstallations(context.Background(), "https://github.com/acme", 7, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != (core.Installation{ID: 3, Account: "org3", TargetType: "Organization", HTMLURL: "h3"}) {
		t.Errorf("got %+v", got)
	}

	_, otherPEM := testKey(t)
	if _, err := a.ListInstallations(context.Background(), "https://github.com/acme", 7, otherPEM); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v", err)
	}
	if _, err := a.ListInstallations(context.Background(), "https://github.com/acme", 7, "not a key"); err == nil {
		t.Error("want error for a bad key")
	}
}

func TestValidatePAT(t *testing.T) {
	a := newTestApps(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		path := r.URL.Path
		switch {
		case auth == "Bearer bad":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
		case auth == "Bearer sso":
			w.Header().Set("X-GitHub-SSO", "required; url=https://github.com/orgs/acme/sso")
			w.WriteHeader(http.StatusForbidden)
		case strings.HasPrefix(path, "/orgs/ghost"), strings.HasPrefix(path, "/repos/acme/ghost"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(path, "/actions/runners"):
			switch auth {
			case "Bearer classic-read":
				w.Header().Set("X-OAuth-Scopes", "read:org, public_repo")
				w.WriteHeader(http.StatusForbidden)
			case "Bearer fine-read":
				w.WriteHeader(http.StatusForbidden)
			default:
				w.Header().Set("X-OAuth-Scopes", "admin:org, repo")
				fmt.Fprint(w, `{"total_count":0,"runners":[]}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	ctx := context.Background()
	cases := []struct {
		url, token, wantErr string
	}{
		{"https://github.com/acme", "good", ""},
		{"https://github.com/acme/repo", "good", ""},
		{"https://github.com/acme", "bad", "wrong, expired or revoked"},
		{"https://github.com/acme", "sso", "single sign-on"},
		{"https://github.com/ghost", "good", "does not exist or the token has no access"},
		{"https://github.com/acme/ghost", "good", "does not exist or the token has no access"},
		{"https://github.com/acme", "classic-read", `lacks the "admin:org" scope`},
		{"https://github.com/acme/repo", "classic-read", `lacks the "repo" scope`},
		{"https://github.com/acme", "fine-read", "Self-hosted runners"},
		{"https://github.com/acme/repo", "fine-read", "Administration"},
		{"https://github.com/acme", " ", "empty"},
		{"not a url", "good", "invalid"},
	}
	for _, tc := range cases {
		err := a.ValidatePAT(ctx, tc.url, tc.token)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s/%s: %v", tc.url, tc.token, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s/%s: err = %v, want %q", tc.url, tc.token, err, tc.wantErr)
		}
	}
}

func TestScaleSetClientFactory(t *testing.T) {
	_, keyPEM := testKey(t)
	f := NewScaleSetClientFactory("1.2.3", nil)
	app := core.Connection{Name: "c", ConfigURL: "https://github.com/acme", AuthKind: core.AuthApp, Status: core.ConnectionReady, AppID: 5, InstallationID: 9, Secret: keyPEM}
	c, err := f.NewClient(app)
	if err != nil {
		t.Fatal(err)
	}
	if info := c.SystemInfo(); info.System != SystemName || info.Version != "1.2.3" {
		t.Errorf("system info = %+v", info)
	}
	pat := core.Connection{Name: "p", ConfigURL: "https://github.com/acme/repo", AuthKind: core.AuthPAT, Status: core.ConnectionReady, Secret: "ghp_x"}
	if _, err := f.NewClient(pat); err != nil {
		t.Fatal(err)
	}
	pending := app
	pending.Status = core.ConnectionPendingInstallation
	if _, err := f.NewClient(pending); err == nil {
		t.Error("want error for a pending connection")
	}
	noInst := app
	noInst.InstallationID = 0
	if _, err := f.NewClient(noInst); err == nil {
		t.Error("want error without installation")
	}
}

func TestAppCallsUseConfigURLHost(t *testing.T) {
	_, keyPEM := testKey(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v3/app-manifests/c1/conversions":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1,"pem":"PEM"}`)
		case "/api/v3/app/installations":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"`+strings.Repeat("x", 5000)+`"}`)
		}
	}))
	defer srv.Close()
	a := NewApps(srv.Client())
	ghes := srv.URL + "/acme"
	if _, err := a.ExchangeManifestCode(context.Background(), ghes, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListInstallations(context.Background(), ghes, 1, keyPEM); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
	err := a.ValidatePAT(context.Background(), ghes, "tok")
	if err == nil || len(err.Error()) > 1000 {
		t.Fatalf("upstream message must be bounded, got %d bytes", len(fmt.Sprint(err)))
	}
}

func TestPaginationStaysOnHost(t *testing.T) {
	_, keyPEM := testKey(t)
	a := newTestApps(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://evil.example/app/installations?page=2>; rel="next"`)
		fmt.Fprint(w, `[]`)
	}))
	if _, err := a.ListInstallations(context.Background(), "https://github.com/acme", 1, keyPEM); err == nil {
		t.Fatal("want error for a pagination link to another host")
	}
}
