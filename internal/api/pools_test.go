package api

import (
	"strings"
	"testing"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

func validPool(connID string) map[string]any {
	return map[string]any{
		"connection_id": connID, "name": "oakestra-4vcpu", "vcpus": 4, "memory_mb": 8192, "max_runners": 5,
	}
}

func with(m map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestPoolValidation(t *testing.T) {
	h := newHarness(t)
	c := seedConn(h, core.AuthPAT, "https://github.com/acme")
	base := validPool(c.ID)

	cases := []struct {
		name string
		body map[string]any
		want string // substring of the error message, empty means success
	}{
		{"valid", base, ""},
		{"valid pinned arch", with(base, "arch", "arm64", "cluster", "edge"), ""},
		{"valid node", with(base, "cluster", "edge", "node", "w1"), ""},
		{"name empty", with(base, "name", ""), "name is required"},
		{"name uppercase", with(base, "name", "Big"), "name must match"},
		{"name leading dash", with(base, "name", "-x"), "name must match"},
		{"name space", with(base, "name", "a b"), "name must match"},
		{"name too long", with(base, "name", strings.Repeat("a", 64)), "name must match"},
		{"name max length", with(base, "name", strings.Repeat("a", 63)), ""},
		{"vcpus zero", with(base, "vcpus", 0), "vcpus"},
		{"vcpus too many", with(base, "vcpus", 257), "vcpus"},
		{"vcpus max", with(base, "vcpus", 256), ""},
		{"memory low", with(base, "memory_mb", 255), "memory_mb"},
		{"memory min", with(base, "memory_mb", 256), ""},
		{"memory high", with(base, "memory_mb", 1048577), "memory_mb"},
		{"max runners zero", with(base, "max_runners", 0), "max_runners"},
		{"max runners high", with(base, "max_runners", 101), "max_runners"},
		{"max runners max", with(base, "max_runners", 100), ""},
		{"bad arch", with(base, "arch", "riscv", "cluster", "edge"), "arch must be"},
		{"arch without cluster", with(base, "arch", "amd64"), "arch requires cluster"},
		{"node without cluster", with(base, "node", "w1"), "node requires cluster"},
		{"missing connection id", with(base, "connection_id", ""), "connection_id is required"},
		{"unknown connection", with(base, "connection_id", "nope"), "connection_id does not exist"},
		{"image with space", with(base, "image", "a b"), "image"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := with(tc.body, "name", tc.body["name"])
			// Distinct names keep the store's uniqueness rule out of the table.
			if tc.want == "" && body["name"] != "" {
				body["name"] = fmt1(tc.body["name"].(string), i)
			}
			rec := h.do("POST", "/api/pools", body)
			if tc.want == "" {
				wantStatus(t, rec, 201)
				return
			}
			e := wantError(t, rec, 400, "validation_failed")
			if !strings.Contains(e.Message, tc.want) {
				t.Fatalf("message %q lacks %q", e.Message, tc.want)
			}
		})
	}
}

func fmt1(name string, i int) string {
	suffix := "-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	if len(name)+len(suffix) > 63 {
		name = name[:63-len(suffix)]
	}
	return name + suffix
}

func TestPoolValidationListsAllProblems(t *testing.T) {
	h := newHarness(t)
	e := wantError(t, h.do("POST", "/api/pools", map[string]any{"name": "BAD", "vcpus": 0, "arch": "x", "node": "n"}), 400, "validation_failed")
	for _, want := range []string{"name must match", "vcpus", "memory_mb", "max_runners", "arch must be", "node requires cluster", "connection_id"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message %q lacks %q", e.Message, want)
		}
	}
}

func TestPoolCRUD(t *testing.T) {
	h := newHarness(t)
	c := seedConn(h, core.AuthPAT, "https://github.com/acme")

	rec := h.do("POST", "/api/pools", with(validPool(c.ID),
		"id", "evil", "scale_set_id", 99, "last_error", "x", "created_at", "2020-01-01T00:00:00Z", "cluster", "edge", "runner_group", "ci"))
	wantStatus(t, rec, 201)
	p := decode[core.Pool](t, rec)
	if p.ID == "" || p.ID == "evil" || p.ScaleSetID != 0 || p.LastError != "" || !p.Enabled || p.Cluster != "edge" || p.RunnerGroup != "ci" {
		t.Fatalf("created: %+v", p)
	}
	if h.ctl.count() != 1 {
		t.Fatalf("reloads after create = %d", h.ctl.count())
	}

	// Runtime fields are owned by the controller and survive an update.
	stored := h.store.pools[p.ID]
	stored.ScaleSetID, stored.LastError = 7, "boom"
	h.store.pools[p.ID] = stored

	rec = h.do("PUT", "/api/pools/"+p.ID, with(validPool(c.ID), "vcpus", 8, "enabled", false, "scale_set_id", 1234, "id", "other"))
	wantStatus(t, rec, 200)
	u := decode[core.Pool](t, rec)
	if u.ID != p.ID || u.VCPUs != 8 || u.Enabled || u.ScaleSetID != 7 || u.LastError != "boom" || !u.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("updated: %+v", u)
	}
	if h.ctl.count() != 2 {
		t.Fatalf("reloads after update = %d", h.ctl.count())
	}

	// Omitting enabled on update keeps the stored value.
	wantStatus(t, h.do("PUT", "/api/pools/"+p.ID, validPool(c.ID)), 200)
	if h.store.pools[p.ID].Enabled {
		t.Fatal("enabled flipped by omission")
	}

	got := decode[core.Pool](t, h.do("GET", "/api/pools/"+p.ID, nil))
	if got.ID != p.ID {
		t.Fatalf("get: %+v", got)
	}
	if list := decode[[]core.Pool](t, h.do("GET", "/api/pools", nil)); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}

	wantError(t, h.do("PUT", "/api/pools/missing", validPool(c.ID)), 404, "not_found")
	wantError(t, h.do("GET", "/api/pools/missing", nil), 404, "not_found")
	wantError(t, h.do("PUT", "/api/pools/"+p.ID, with(validPool(c.ID), "vcpus", 0)), 400, "validation_failed")

	before := h.ctl.count()
	wantStatus(t, h.do("DELETE", "/api/pools/"+p.ID, nil), 204)
	if h.ctl.count() != before+1 {
		t.Fatal("no reload after delete")
	}
	wantError(t, h.do("DELETE", "/api/pools/"+p.ID, nil), 404, "not_found")
}

func TestPoolNameConflict(t *testing.T) {
	h := newHarness(t)
	c := seedConn(h, core.AuthPAT, "https://github.com/acme")
	wantStatus(t, h.do("POST", "/api/pools", validPool(c.ID)), 201)
	before := h.ctl.count()
	e := wantError(t, h.do("POST", "/api/pools", validPool(c.ID)), 409, "conflict")
	if !strings.Contains(e.Message, "already in use") {
		t.Fatalf("message: %s", e.Message)
	}
	if h.ctl.count() != before {
		t.Fatal("reload after failed write")
	}

	other := decode[core.Pool](t, h.do("POST", "/api/pools", with(validPool(c.ID), "name", "second")))
	wantError(t, h.do("PUT", "/api/pools/"+other.ID, validPool(c.ID)), 409, "conflict")
}
