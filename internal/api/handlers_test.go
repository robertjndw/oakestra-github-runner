package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

func seedConn(h *harness, kind core.AuthKind, url string) core.Connection {
	c := core.Connection{Name: "gh", ConfigURL: url, AuthKind: kind, Status: core.ConnectionReady, Secret: "s3cret"}
	if kind == core.AuthApp {
		c.AppID = 42
		c.Status = core.ConnectionPendingInstallation
		c.Secret = "PRIVATE-KEY"
	}
	_ = h.store.CreateConnection(context.Background(), &c)
	return c
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	c1 := seedConn(h, core.AuthPAT, "https://github.com/acme")
	seedConn(h, core.AuthApp, "https://github.com/acme")
	_ = h.store.CreatePool(context.Background(), &core.Pool{ConnectionID: c1.ID, Name: "a", Enabled: true})
	_ = h.store.CreatePool(context.Background(), &core.Pool{ConnectionID: c1.ID, Name: "b", Enabled: false})
	h.store.runners = []core.Runner{
		{Name: "r1", State: core.RunnerPending},
		{Name: "r2", State: core.RunnerIdle},
		{Name: "r3", State: core.RunnerIdle},
		{Name: "r4", State: core.RunnerBusy},
		{Name: "r5", State: core.RunnerRemoved},
	}
	st := decode[core.Status](t, h.do("GET", "/api/status", nil))
	if st.Version != "1.2.3" || st.RunnerImage != "runner:test" || !st.Oakestra.OK {
		t.Fatalf("header fields: %+v", st)
	}
	if st.Connections.Total != 2 || st.Connections.Ready != 1 || st.Pools.Total != 2 || st.Pools.Enabled != 1 ||
		st.Runners.Pending != 1 || st.Runners.Idle != 2 || st.Runners.Busy != 1 {
		t.Fatalf("counts: %+v", st)
	}
}

func TestOakestraCredentials(t *testing.T) {
	t.Run("stores and reloads", func(t *testing.T) {
		h := newHarness(t)
		rec := h.do("PUT", "/api/oakestra-credentials", map[string]string{"username": "svc", "password": "pw", "organization": "acme"})
		wantStatus(t, rec, 200)
		if got := decode[core.OakestraStatus](t, rec); !got.OK {
			t.Fatalf("status: %+v", got)
		}
		if h.store.creds == nil || h.store.creds.Username != "svc" || h.store.creds.Password != "pw" || h.store.creds.Organization != "acme" {
			t.Fatalf("stored: %+v", h.store.creds)
		}
		if h.oak.credentialsSeen != 1 || h.ctl.count() != 1 {
			t.Fatalf("notifications: oak=%d ctl=%d", h.oak.credentialsSeen, h.ctl.count())
		}
		if strings.Contains(rec.Body.String(), "pw") {
			t.Fatal("password echoed")
		}
	})
	t.Run("env credentials conflict", func(t *testing.T) {
		h := newHarness(t, func(d *Deps) { d.EnvCredentials = true })
		wantError(t, h.do("PUT", "/api/oakestra-credentials", map[string]string{"username": "a", "password": "b"}), 409, "conflict")
		if h.store.creds != nil {
			t.Fatal("stored despite env credentials")
		}
	})
	t.Run("validation", func(t *testing.T) {
		h := newHarness(t)
		e := wantError(t, h.do("PUT", "/api/oakestra-credentials", map[string]string{"organization": "x"}), 400, "validation_failed")
		if !strings.Contains(e.Message, "username") || !strings.Contains(e.Message, "password") {
			t.Fatalf("message: %s", e.Message)
		}
	})
}

func TestClusters(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/api/clusters", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list body = %q", rec.Body.String())
	}

	h.oak.clusters = []core.Cluster{{ID: "c1", Name: "edge", ActiveNodes: 2}}
	got := decode[[]core.Cluster](t, h.do("GET", "/api/clusters", nil))
	if len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("clusters: %+v", got)
	}

	h.oak.clustersErr = fmt.Errorf("wrapped: %w", core.ErrNoCredentials)
	e := wantError(t, h.do("GET", "/api/clusters", nil), 409, "conflict")
	if !strings.Contains(e.Message, "service user") {
		t.Fatalf("message: %s", e.Message)
	}

	h.oak.clustersErr = errors.New("boom")
	wantError(t, h.do("GET", "/api/clusters", nil), 502, "upstream_error")
}

func TestCreatePATConnection(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/connections", map[string]string{"name": " org ", "config_url": "https://github.com/acme/", "token": "ghp_x"})
	wantStatus(t, rec, 201)
	c := decode[core.Connection](t, rec)
	if c.ID == "" || c.Name != "org" || c.ConfigURL != "https://github.com/acme" || c.AuthKind != core.AuthPAT || c.Status != core.ConnectionReady {
		t.Fatalf("connection: %+v", c)
	}
	if strings.Contains(rec.Body.String(), "ghp_x") {
		t.Fatal("token in response")
	}
	if h.gh.patSeen != [2]string{"https://github.com/acme", "ghp_x"} {
		t.Fatalf("ValidatePAT args: %v", h.gh.patSeen)
	}
	if h.store.conns[c.ID].Secret != "ghp_x" {
		t.Fatal("token not stored")
	}
	if h.ctl.count() != 1 {
		t.Fatalf("reloads = %d", h.ctl.count())
	}
}

func TestCreatePATConnectionErrors(t *testing.T) {
	bad := []struct {
		name string
		body map[string]string
	}{
		{"no name", map[string]string{"config_url": "https://github.com/acme", "token": "t"}},
		{"no token", map[string]string{"name": "n", "config_url": "https://github.com/acme"}},
		{"http", map[string]string{"name": "n", "config_url": "http://github.com/acme", "token": "t"}},
		{"no owner", map[string]string{"name": "n", "config_url": "https://github.com", "token": "t"}},
		{"too deep", map[string]string{"name": "n", "config_url": "https://github.com/a/b/c", "token": "t"}},
		{"query", map[string]string{"name": "n", "config_url": "https://github.com/a?x=1", "token": "t"}},
		{"fragment", map[string]string{"name": "n", "config_url": "https://github.com/a#x", "token": "t"}},
		{"userinfo", map[string]string{"name": "n", "config_url": "https://u:p@github.com/a", "token": "t"}},
		{"double slash", map[string]string{"name": "n", "config_url": "https://github.com/a//b", "token": "t"}},
		{"garbage", map[string]string{"name": "n", "config_url": "nope", "token": "t"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			wantError(t, h.do("POST", "/api/connections", tc.body), 400, "validation_failed")
			if len(h.store.conns) != 0 || h.ctl.count() != 0 {
				t.Fatal("state changed on invalid input")
			}
		})
	}

	t.Run("repo url is fine", func(t *testing.T) {
		h := newHarness(t)
		wantStatus(t, h.do("POST", "/api/connections", map[string]string{"name": "n", "config_url": "https://github.com/a/b", "token": "t"}), 201)
	})
	t.Run("github rejects token", func(t *testing.T) {
		h := newHarness(t)
		h.gh.patErr = errors.New("token cannot see https://github.com/acme")
		e := wantError(t, h.do("POST", "/api/connections", map[string]string{"name": "n", "config_url": "https://github.com/acme", "token": "t"}), 400, "validation_failed")
		if !strings.Contains(e.Message, "token cannot see") {
			t.Fatalf("message: %s", e.Message)
		}
		if len(h.store.conns) != 0 {
			t.Fatal("connection stored")
		}
	})
}

func TestStrictDecoding(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"unknown field": `{"name":"n","config_url":"https://github.com/a","token":"t","extra":1}`,
		"not json":      `nope`,
		"trailing data": `{"name":"n","config_url":"https://github.com/a","token":"t"} {}`,
		"wrong type":    `{"name":1}`,
		"empty":         ``,
		"too large":     `{"name":"` + strings.Repeat("a", maxBodyBytes+10) + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			wantError(t, h.do("POST", "/api/connections", body), 400, "validation_failed")
		})
	}
	wantError(t, h.do("POST", "/api/pools", `{"bogus":true}`), 400, "validation_failed")
	if len(h.store.conns) != 0 {
		t.Fatal("something was stored")
	}
}

func TestDeleteConnection(t *testing.T) {
	h := newHarness(t)
	c := seedConn(h, core.AuthPAT, "https://github.com/acme")
	_ = h.store.CreatePool(context.Background(), &core.Pool{ConnectionID: c.ID, Name: "p"})

	e := wantError(t, h.do("DELETE", "/api/connections/"+c.ID, nil), 409, "conflict")
	if !strings.Contains(e.Message, "pools still use") {
		t.Fatalf("message: %s", e.Message)
	}
	if h.ctl.count() != 0 {
		t.Fatal("reload on failed delete")
	}
	h.store.pools = map[string]core.Pool{}
	wantStatus(t, h.do("DELETE", "/api/connections/"+c.ID, nil), 204)
	if h.ctl.count() != 1 {
		t.Fatalf("reloads = %d", h.ctl.count())
	}
	wantError(t, h.do("DELETE", "/api/connections/"+c.ID, nil), 404, "not_found")
}

func TestListsAreNeverNull(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/api/connections", "/api/pools", "/api/runners", "/api/clusters"} {
		rec := h.do("GET", p, nil)
		wantStatus(t, rec, 200)
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("%s: %q", p, rec.Body.String())
		}
	}
	rec := h.do("GET", "/api/jobs", nil)
	wantStatus(t, rec, 200)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"items":[],"next":""}` {
		t.Errorf("jobs: %q", got)
	}
}

func TestRunnersAndJobs(t *testing.T) {
	h := newHarness(t)
	h.store.runners = []core.Runner{{Name: "r1", PoolID: "p1"}, {Name: "r2", PoolID: "p2"}}
	got := decode[[]core.Runner](t, h.do("GET", "/api/runners?pool_id=p2", nil))
	if len(got) != 1 || got[0].Name != "r2" {
		t.Fatalf("runners: %+v", got)
	}

	h.store.jobs = []core.Job{{PoolID: "p1", RequestID: 7, JobName: "build"}}
	rec := h.do("GET", "/api/jobs?pool_id=p1&result=failed&before=abc&limit=10", nil)
	wantStatus(t, rec, 200)
	resp := decode[struct {
		Items []core.Job `json:"items"`
		Next  string     `json:"next"`
	}](t, rec)
	if len(resp.Items) != 1 || resp.Next != "cursor-2" {
		t.Fatalf("jobs: %+v", resp)
	}
	if f := h.store.lastFilter; f != (core.JobFilter{PoolID: "p1", Result: "failed", Before: "abc", Limit: 10}) {
		t.Fatalf("filter: %+v", f)
	}

	h.do("GET", "/api/jobs", nil)
	if h.store.lastFilter.Limit != defaultJobLimit {
		t.Fatalf("default limit = %d", h.store.lastFilter.Limit)
	}
	h.do("GET", "/api/jobs?limit=100000", nil)
	if h.store.lastFilter.Limit != maxJobLimit {
		t.Fatalf("clamped limit = %d", h.store.lastFilter.Limit)
	}
	wantError(t, h.do("GET", "/api/jobs?limit=-1", nil), 400, "validation_failed")
	wantError(t, h.do("GET", "/api/jobs?limit=x", nil), 400, "validation_failed")
}

func TestUnknownAPIPath(t *testing.T) {
	h := newHarness(t)
	wantError(t, h.do("GET", "/api/nope", nil), 404, "not_found")
}

func TestPanicRecovery(t *testing.T) {
	h := newHarness(t)
	h.deps.Store = nil // nil interface call inside a handler panics
	rec := doRequest(t, New(h.deps), "GET", "/api/connections", nil)
	wantError(t, rec, 500, "internal_error")
}
