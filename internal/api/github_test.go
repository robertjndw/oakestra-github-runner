package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

func manifestReq(redirect string) map[string]string {
	return map[string]string{"name": "ci", "config_url": "https://github.com/acme", "redirect_url": redirect}
}

func TestManifestFlowEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.gh.creds = core.AppCredentials{AppID: 42, Slug: "ci-app", ClientID: "Iv1.x", HTMLURL: "https://github.com/apps/ci-app", PrivateKey: "PRIVATE-KEY"}
	h.gh.installs = []core.Installation{
		{ID: 1, Account: "other", TargetType: "Organization"},
		{ID: 2, Account: "ACME", TargetType: "Organization"},
	}

	rec := h.do("POST", "/api/github/app-manifest", manifestReq("https://dash.example/addons/runner"))
	wantStatus(t, rec, 200)
	m := decode[core.AppManifest](t, rec)
	if m.State == "" || len(m.State) < 32 || m.State != h.gh.manifestState || m.Manifest["redirect_url"] != "https://dash.example/addons/runner" {
		t.Fatalf("manifest: %+v", m)
	}
	pending, ok := h.store.manifests[m.State]
	if !ok || pending.Name != "ci" || pending.ConfigURL != "https://github.com/acme" || pending.AuthKind != core.AuthApp {
		t.Fatalf("pending: %+v", pending)
	}

	// Wrong code leaves no connection, and the state is consumed.
	rec = h.do("POST", "/api/github/app-manifest/complete", map[string]string{"state": m.State, "code": "bad"})
	wantError(t, rec, 502, "upstream_error")
	wantError(t, h.do("POST", "/api/github/app-manifest/complete", map[string]string{"state": m.State, "code": "good-code"}), 400, "validation_failed")

	m = decode[core.AppManifest](t, h.do("POST", "/api/github/app-manifest", manifestReq("http://localhost:3000/x")))
	rec = h.do("POST", "/api/github/app-manifest/complete", map[string]string{"state": m.State, "code": "good-code"})
	wantStatus(t, rec, 201)
	conn := decode[core.Connection](t, rec)
	if conn.Status != core.ConnectionPendingInstallation || conn.AppID != 42 || conn.AppSlug != "ci-app" ||
		conn.AppClientID != "Iv1.x" || conn.AuthKind != core.AuthApp || conn.Name != "ci" {
		t.Fatalf("connection: %+v", conn)
	}
	if strings.Contains(rec.Body.String(), "PRIVATE-KEY") {
		t.Fatal("private key in response")
	}
	if h.store.conns[conn.ID].Secret != "PRIVATE-KEY" {
		t.Fatal("private key not stored")
	}
	if h.ctl.count() != 1 {
		t.Fatalf("reloads = %d", h.ctl.count())
	}

	rec = h.do("GET", "/api/connections/"+conn.ID+"/installations", nil)
	wantStatus(t, rec, 200)
	if got := decode[[]core.Installation](t, rec); len(got) != 2 {
		t.Fatalf("installations: %+v", got)
	}
	if h.gh.installsSeen.appID != 42 || h.gh.installsSeen.key != "PRIVATE-KEY" {
		t.Fatalf("stored credentials not used: %+v", h.gh.installsSeen)
	}

	// Installation 1 exists but belongs to another account.
	e := wantError(t, h.do("PUT", "/api/connections/"+conn.ID+"/installation", map[string]int64{"installation_id": 1}), 400, "validation_failed")
	if !strings.Contains(e.Message, "other") {
		t.Fatalf("message: %s", e.Message)
	}
	wantError(t, h.do("PUT", "/api/connections/"+conn.ID+"/installation", map[string]int64{"installation_id": 99}), 400, "validation_failed")
	wantError(t, h.do("PUT", "/api/connections/"+conn.ID+"/installation", map[string]int64{}), 400, "validation_failed")
	if h.store.conns[conn.ID].Status != core.ConnectionPendingInstallation {
		t.Fatal("status changed by rejected requests")
	}

	before := h.ctl.count()
	rec = h.do("PUT", "/api/connections/"+conn.ID+"/installation", map[string]int64{"installation_id": 2})
	wantStatus(t, rec, 200)
	done := decode[core.Connection](t, rec)
	if done.Status != core.ConnectionReady || done.InstallationID != 2 {
		t.Fatalf("connection: %+v", done)
	}
	if h.store.conns[conn.ID].Secret != "PRIVATE-KEY" {
		t.Fatal("secret lost on update")
	}
	if h.ctl.count() != before+1 {
		t.Fatal("no reload after installation")
	}
}

func TestManifestValidation(t *testing.T) {
	h := newHarness(t)
	for name, redirect := range map[string]string{"empty": "", "relative": "/page", "ftp": "ftp://x/y", "no host": "https://"} {
		t.Run(name, func(t *testing.T) {
			e := wantError(t, h.do("POST", "/api/github/app-manifest", manifestReq(redirect)), 400, "validation_failed")
			if !strings.Contains(e.Message, "redirect_url") {
				t.Fatalf("message: %s", e.Message)
			}
		})
	}
	bad := manifestReq("https://x/y")
	bad["config_url"] = "http://github.com/acme"
	wantError(t, h.do("POST", "/api/github/app-manifest", bad), 400, "validation_failed")
	if len(h.store.manifests) != 0 {
		t.Fatal("state saved for invalid input")
	}

	h.gh.manifestErr = errors.New("github down")
	wantError(t, h.do("POST", "/api/github/app-manifest", manifestReq("https://x/y")), 502, "upstream_error")
	if len(h.store.manifests) != 0 {
		t.Fatal("state saved although the manifest failed")
	}
}

func TestManifestCompleteErrors(t *testing.T) {
	h := newHarness(t)
	wantError(t, h.do("POST", "/api/github/app-manifest/complete", map[string]string{"state": "unknown", "code": "c"}), 400, "validation_failed")
	wantError(t, h.do("POST", "/api/github/app-manifest/complete", map[string]string{"state": "s"}), 400, "validation_failed")
}

func TestInstallationsErrors(t *testing.T) {
	h := newHarness(t)
	pat := seedConn(h, core.AuthPAT, "https://github.com/acme")
	app := seedConn(h, core.AuthApp, "https://github.com/acme")

	wantError(t, h.do("GET", "/api/connections/nope/installations", nil), 404, "not_found")
	wantError(t, h.do("GET", "/api/connections/"+pat.ID+"/installations", nil), 400, "validation_failed")
	wantError(t, h.do("PUT", "/api/connections/"+pat.ID+"/installation", map[string]int64{"installation_id": 1}), 400, "validation_failed")

	rec := h.do("GET", "/api/connections/"+app.ID+"/installations", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("body: %q", rec.Body.String())
	}

	h.gh.installsErr = errors.New("github down")
	wantError(t, h.do("GET", "/api/connections/"+app.ID+"/installations", nil), 502, "upstream_error")
	wantError(t, h.do("PUT", "/api/connections/"+app.ID+"/installation", map[string]int64{"installation_id": 1}), 502, "upstream_error")
}
