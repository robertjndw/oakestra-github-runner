package api

import "testing"

func TestStaticFiles(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		path   string
		status int
		cache  string
	}{
		{"/mf-manifest.json", 200, "no-cache"},
		{"/remoteEntry.js", 200, "no-cache"},
		{"/assets/app-abc.js", 200, "public, max-age=31536000, immutable"},
		{"/assets/sub/x.css", 200, "public, max-age=31536000, immutable"},
		{"/index.html", 200, ""},
		{"/static/other.txt", 200, ""},
		{"/", 404, ""},
		{"/secret-dir", 404, ""},
		{"/assets/", 404, ""},
		{"/missing.js", 404, ""},
		{"/some/spa/route", 404, ""},
		{"/../assets/app-abc.js", 404, ""},
		{"//assets/app-abc.js", 404, ""},
		{"/assets/./app-abc.js", 404, ""},
		{"/assets/%2e%2e/index.html", 404, ""},
	}
	for _, tc := range cases {
		rec := h.do("GET", tc.path, nil)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, rec.Code, tc.status)
			continue
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cache {
			t.Errorf("%s: Cache-Control %q, want %q", tc.path, got, tc.cache)
		}
	}
	wantStatus(t, h.do("POST", "/mf-manifest.json", nil), 405)
}

func TestStaticWithoutUI(t *testing.T) {
	h := newHarness(t, func(d *Deps) { d.UI = nil })
	wantStatus(t, h.do("GET", "/remoteEntry.js", nil), 404)
	wantStatus(t, h.do("GET", "/healthz", nil), 200)
}
