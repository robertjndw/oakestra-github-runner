package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

var ctx = context.Background()

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, keySize) }

func openAt(t *testing.T, path string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(path, testKey(1), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func open(t *testing.T, opts ...Option) *Store {
	return openAt(t, filepath.Join(t.TempDir(), "db.sqlite"), opts...)
}

func mustConn(t *testing.T, s *Store, name string) core.Connection {
	t.Helper()
	c := core.Connection{Name: name, ConfigURL: "https://github.com/acme", AuthKind: core.AuthPAT,
		Status: core.ConnectionReady, Secret: "ghp_secret_" + name}
	if err := s.CreateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func mustPool(t *testing.T, s *Store, connID, name string) core.Pool {
	t.Helper()
	p := core.Pool{ConnectionID: connID, Name: name, VCPUs: 2, MemoryMB: 4096, MaxRunners: 3, Enabled: true}
	if err := s.CreatePool(ctx, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConnectionCRUD(t *testing.T) {
	s := open(t)
	c := core.Connection{Name: "org", ConfigURL: "https://github.com/acme", AuthKind: core.AuthApp,
		Status: core.ConnectionPendingInstallation, AppID: 42, AppSlug: "slug", AppClientID: "cid",
		AppHTMLURL: "https://x", Secret: "-----BEGIN PEM-----"}
	if err := s.CreateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.CreatedAt.IsZero() {
		t.Fatalf("id/created not filled: %+v", c)
	}
	got, err := s.GetConnection(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, c)
	}

	c.Status, c.InstallationID, c.Secret = core.ConnectionReady, 99, "new-secret"
	if err := s.UpdateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetConnection(ctx, c.ID)
	if got.Status != core.ConnectionReady || got.InstallationID != 99 || got.Secret != "new-secret" {
		t.Fatalf("update not applied: %+v", got)
	}
	list, err := s.ListConnections(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if err := s.DeleteConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnection(ctx, c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := s.DeleteConnection(ctx, c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := s.UpdateConnection(ctx, &c); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	list, _ = s.ListConnections(ctx)
	if list == nil || len(list) != 0 {
		t.Fatalf("empty list must be non-nil empty, got %#v", list)
	}
}

func TestPoolCRUDAndConflicts(t *testing.T) {
	s := open(t)
	c1, c2 := mustConn(t, s, "a"), mustConn(t, s, "b")
	p := mustPool(t, s, c1.ID, "small")
	if p.ID == "" || p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Fatalf("not filled: %+v", p)
	}

	dup := core.Pool{ConnectionID: c1.ID, Name: "small", VCPUs: 1, MemoryMB: 1, MaxRunners: 1}
	if err := s.CreatePool(ctx, &dup); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	// Same name under another connection is fine.
	mustPool(t, s, c2.ID, "small")

	other := mustPool(t, s, c1.ID, "large")
	other.Name = "small"
	if err := s.UpdatePool(ctx, &other); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict on rename, got %v", err)
	}

	orphan := core.Pool{ConnectionID: "missing", Name: "x"}
	if err := s.CreatePool(ctx, &orphan); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict for unknown connection, got %v", err)
	}

	if err := s.DeleteConnection(ctx, c1.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict while pools exist, got %v", err)
	}

	// UpdatePool must not clobber runtime fields.
	if err := s.SetPoolRuntime(ctx, p.ID, 77, "boom"); err != nil {
		t.Fatal(err)
	}
	p.VCPUs, p.Enabled, p.ScaleSetID, p.LastError = 8, false, 0, ""
	if err := s.UpdatePool(ctx, &p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPool(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VCPUs != 8 || got.Enabled || got.ScaleSetID != 77 || got.LastError != "boom" {
		t.Fatalf("unexpected pool %+v", got)
	}
	if !got.UpdatedAt.Equal(p.UpdatedAt) || got.UpdatedAt.Before(got.CreatedAt) {
		t.Fatalf("updated_at wrong: %+v", got)
	}

	if err := s.SetPoolRuntime(ctx, "nope", 1, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := s.UpdatePool(ctx, &core.Pool{ID: "nope"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if _, err := s.GetPool(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}

	pools, _ := s.ListPools(ctx)
	if len(pools) != 3 {
		t.Fatalf("want 3 pools, got %d", len(pools))
	}
	for _, id := range []string{p.ID, other.ID} {
		if err := s.DeletePool(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeletePool(ctx, p.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := s.DeleteConnection(ctx, c1.ID); err != nil {
		t.Fatalf("delete after pools gone: %v", err)
	}
}

func TestRunnerCRUD(t *testing.T) {
	s := open(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("x", 3600))
	r := core.Runner{Name: "r1", PoolID: "p", ServiceID: "svc", State: core.RunnerPending, CreatedAt: created}
	if err := s.CreateRunner(ctx, &r); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRunner(ctx, &core.Runner{Name: "r1", State: core.RunnerPending}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	got, err := s.GetRunner(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(created) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at: %v", got.CreatedAt)
	}
	if got.RunningAt != nil || got.RemovedAt != nil {
		t.Fatalf("nil pointers expected: %+v", got)
	}

	run := time.Now().UTC().Add(time.Second)
	got.State, got.RunningAt, got.JobRequestID, got.ClusterID = core.RunnerBusy, &run, 12, "cl"
	if err := s.UpdateRunner(ctx, &got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetRunner(ctx, "r1")
	if again.State != core.RunnerBusy || again.RunningAt == nil || !again.RunningAt.Equal(run) || again.JobRequestID != 12 {
		t.Fatalf("update: %+v", again)
	}
	if err := s.UpdateRunner(ctx, &core.Runner{Name: "zz"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if _, err := s.GetRunner(ctx, "zz"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestListRunnersHidesRemoved(t *testing.T) {
	s := open(t)
	for _, r := range []core.Runner{
		{Name: "a", PoolID: "p1", State: core.RunnerIdle},
		{Name: "b", PoolID: "p2", State: core.RunnerBusy},
		{Name: "c", PoolID: "p1", State: core.RunnerRemoved},
	} {
		r := r
		if err := s.CreateRunner(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.ListRunners(ctx, "")
	if len(all) != 2 || all[0].Name != "a" || all[1].Name != "b" {
		t.Fatalf("all: %+v", all)
	}
	p1, _ := s.ListRunners(ctx, "p1")
	if len(p1) != 1 || p1[0].Name != "a" {
		t.Fatalf("p1: %+v", p1)
	}
	if got, err := s.GetRunner(ctx, "c"); err != nil || got.State != core.RunnerRemoved {
		t.Fatalf("removed runner must stay readable: %v %+v", err, got)
	}
}

func ts(sec int) *time.Time {
	t := time.Date(2026, 3, 1, 12, 0, sec, 987654321, time.UTC)
	return &t
}

func TestUpsertJobMergeBothOrders(t *testing.T) {
	started := core.Job{PoolID: "p", RequestID: 7, RunnerName: "r1", Repository: "acme/x", JobName: "build",
		WorkflowRunID: 5, EventName: "push", QueuedAt: ts(1), StartedAt: ts(2), ClusterID: "c", WorkerID: "w"}
	completed := core.Job{PoolID: "p", RequestID: 7, Result: "succeeded", FinishedAt: ts(9), RunnerName: "r1"}

	for name, order := range map[string][]core.Job{
		"started first":   {started, completed},
		"completed first": {completed, started},
		"repeated":        {started, completed, started, completed},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			for _, j := range order {
				j := j
				if err := s.UpsertJob(ctx, &j); err != nil {
					t.Fatal(err)
				}
			}
			jobs, next, err := s.ListJobs(ctx, core.JobFilter{})
			if err != nil || next != "" || len(jobs) != 1 {
				t.Fatalf("jobs=%v next=%q err=%v", jobs, next, err)
			}
			j := jobs[0]
			if j.Result != "succeeded" || j.RunnerName != "r1" || j.Repository != "acme/x" || j.JobName != "build" ||
				j.WorkflowRunID != 5 || j.EventName != "push" || j.ClusterID != "c" || j.WorkerID != "w" {
				t.Fatalf("merge lost fields: %+v", j)
			}
			if j.QueuedAt == nil || !j.QueuedAt.Equal(*ts(1)) || j.StartedAt == nil || !j.StartedAt.Equal(*ts(2)) ||
				j.FinishedAt == nil || !j.FinishedAt.Equal(*ts(9)) {
				t.Fatalf("times: %+v", j)
			}
		})
	}
}

func TestUpsertJobNilTimesAndSeparateKeys(t *testing.T) {
	s := open(t)
	a := core.Job{PoolID: "p", RequestID: 1, JobName: "a"}
	b := core.Job{PoolID: "q", RequestID: 1, JobName: "b"}
	for _, j := range []*core.Job{&a, &b} {
		if err := s.UpsertJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	jobs, _, _ := s.ListJobs(ctx, core.JobFilter{})
	if len(jobs) != 2 {
		t.Fatalf("same request id in different pools must be distinct: %+v", jobs)
	}
	for _, j := range jobs {
		if j.QueuedAt != nil || j.StartedAt != nil || j.FinishedAt != nil {
			t.Fatalf("times must stay nil: %+v", j)
		}
	}
}

func TestListJobsPaging(t *testing.T) {
	s := open(t)
	const n = 23
	for i := 1; i <= n; i++ {
		res := "failed"
		if i%2 == 0 {
			res = "succeeded"
		}
		pool := "p1"
		if i%3 == 0 {
			pool = "p2"
		}
		// No StartedAt on purpose: ordering must not depend on it.
		if err := s.UpsertJob(ctx, &core.Job{PoolID: pool, RequestID: int64(i), Result: res, JobName: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}

	var seen []int64
	before := ""
	pages := 0
	for {
		jobs, next, err := s.ListJobs(ctx, core.JobFilter{Limit: 5, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, j := range jobs {
			seen = append(seen, j.RequestID)
		}
		if next == "" {
			break
		}
		if len(jobs) != 5 {
			t.Fatalf("full page expected when next set, got %d", len(jobs))
		}
		before = next
	}
	if pages != 5 || len(seen) != n {
		t.Fatalf("pages=%d seen=%d", pages, len(seen))
	}
	for i, id := range seen {
		if id != int64(n-i) {
			t.Fatalf("order wrong at %d: %v", i, seen)
		}
	}

	// A page that ends exactly at the last row has no next cursor.
	jobs, next, _ := s.ListJobs(ctx, core.JobFilter{Limit: n})
	if len(jobs) != n || next != "" {
		t.Fatalf("exact page: %d %q", len(jobs), next)
	}

	jobs, _, _ = s.ListJobs(ctx, core.JobFilter{PoolID: "p2"})
	if len(jobs) != 7 {
		t.Fatalf("pool filter: %d", len(jobs))
	}
	jobs, _, _ = s.ListJobs(ctx, core.JobFilter{Result: "succeeded", PoolID: "p1"})
	for _, j := range jobs {
		if j.Result != "succeeded" || j.PoolID != "p1" {
			t.Fatalf("filter: %+v", j)
		}
	}
	if _, _, err := s.ListJobs(ctx, core.JobFilter{Before: "!!!"}); err == nil {
		t.Fatal("bad cursor must error")
	}
}

func TestListJobsLimits(t *testing.T) {
	s := open(t)
	for i := 1; i <= 260; i++ {
		if err := s.UpsertJob(ctx, &core.Job{PoolID: "p", RequestID: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, next, _ := s.ListJobs(ctx, core.JobFilter{})
	if len(jobs) != 50 || next == "" {
		t.Fatalf("default limit: %d %q", len(jobs), next)
	}
	jobs, next, _ = s.ListJobs(ctx, core.JobFilter{Limit: 1000})
	if len(jobs) != 200 || next == "" {
		t.Fatalf("max limit: %d %q", len(jobs), next)
	}
}

func TestOakestraCredentials(t *testing.T) {
	s := open(t)
	if _, err := s.OakestraCredentials(ctx); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	want := core.OakestraCredentials{Username: "svc", Password: "pw1", Organization: "org"}
	if err := s.SetOakestraCredentials(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.Password = "pw2"
	if err := s.SetOakestraCredentials(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.OakestraCredentials(ctx)
	if err != nil || got != want {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestManifestState(t *testing.T) {
	now := time.Now()
	s := open(t, WithClock(func() time.Time { return now }))
	pending := core.Connection{ID: "pre", Name: "n", ConfigURL: "https://github.com/acme", AuthKind: core.AuthApp,
		Status: core.ConnectionPendingInstallation, Secret: "pem"}
	if err := s.SaveManifestState(ctx, "st1", pending); err != nil {
		t.Fatal(err)
	}
	got, err := s.TakeManifestState(ctx, "st1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "pem" || got.Name != "n" || got.ID != "pre" {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.TakeManifestState(ctx, "st1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second take must fail, got %v", err)
	}

	if err := s.SaveManifestState(ctx, "old", pending); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Second)
	if _, err := s.TakeManifestState(ctx, "old"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expired state must be not found, got %v", err)
	}
}

func TestPrune(t *testing.T) {
	now := time.Now()
	s := open(t, WithClock(func() time.Time { return now }), WithRetention(24*time.Hour))
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)

	for _, r := range []core.Runner{
		{Name: "old-removed", State: core.RunnerRemoved, RemovedAt: &old},
		{Name: "recent-removed", State: core.RunnerRemoved, RemovedAt: &recent},
		{Name: "old-idle", State: core.RunnerIdle, CreatedAt: old},
	} {
		r := r
		if err := s.CreateRunner(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	for _, j := range []core.Job{
		{PoolID: "p", RequestID: 1, FinishedAt: &old},
		{PoolID: "p", RequestID: 2, FinishedAt: &recent},
		{PoolID: "p", RequestID: 3, StartedAt: &old}, // still running, must stay
	} {
		j := j
		if err := s.UpsertJob(ctx, &j); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveManifestState(ctx, "stale", core.Connection{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetRunner(ctx, "old-removed"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old removed runner should be pruned: %v", err)
	}
	for _, n := range []string{"recent-removed", "old-idle"} {
		if _, err := s.GetRunner(ctx, n); err != nil {
			t.Fatalf("%s must survive: %v", n, err)
		}
	}
	jobs, _, _ := s.ListJobs(ctx, core.JobFilter{})
	ids := map[int64]bool{}
	for _, j := range jobs {
		ids[j.RequestID] = true
	}
	if ids[1] || !ids[2] || !ids[3] {
		t.Fatalf("jobs after prune: %v", ids)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM manifest_states").Scan(&n); err != nil || n != 0 {
		t.Fatalf("manifest states left: %d %v", n, err)
	}
}

func TestPruneDefaultRetention(t *testing.T) {
	now := time.Now()
	s := open(t, WithClock(func() time.Time { return now }))
	old := now.Add(-29 * 24 * time.Hour)
	older := now.Add(-31 * 24 * time.Hour)
	_ = s.UpsertJob(ctx, &core.Job{PoolID: "p", RequestID: 1, FinishedAt: &old})
	_ = s.UpsertJob(ctx, &core.Job{PoolID: "p", RequestID: 2, FinishedAt: &older})
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, _, _ := s.ListJobs(ctx, core.JobFilter{})
	if len(jobs) != 1 || jobs[0].RequestID != 1 {
		t.Fatalf("jobs: %+v", jobs)
	}
}

func TestReopenPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path, testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	c := mustConn(t, s, "persist")
	p := mustPool(t, s, c.ID, "pool")
	_ = s.UpsertJob(ctx, &core.Job{PoolID: p.ID, RequestID: 1, Result: "ok"})
	_ = s.SetOakestraCredentials(ctx, core.OakestraCredentials{Username: "u", Password: "pw"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openAt(t, path)
	got, err := s2.GetConnection(ctx, c.ID)
	if err != nil || got != c {
		t.Fatalf("connection: %+v %v", got, err)
	}
	if gp, err := s2.GetPool(ctx, p.ID); err != nil || gp != p {
		t.Fatalf("pool: %+v %v", gp, err)
	}
	if jobs, _, _ := s2.ListJobs(ctx, core.JobFilter{}); len(jobs) != 1 {
		t.Fatalf("jobs: %v", jobs)
	}
	if cr, err := s2.OakestraCredentials(ctx); err != nil || cr.Password != "pw" {
		t.Fatalf("creds: %+v %v", cr, err)
	}
	var v int
	if err := s2.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("user_version %d %v", v, err)
	}
}

func TestEncryptionAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path, testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "SUPER-SECRET-PAT-VALUE-12345"
	const pw = "OAKESTRA-PASSWORD-67890"
	const pending = "PENDING-PEM-PRIVATE-KEY"
	c := core.Connection{Name: "n", ConfigURL: "u", AuthKind: core.AuthPAT, Status: core.ConnectionReady, Secret: secret}
	if err := s.CreateConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	_ = s.SetOakestraCredentials(ctx, core.OakestraCredentials{Username: "u", Password: pw})
	_ = s.SaveManifestState(ctx, "st", core.Connection{Secret: pending})

	// Checkpoint so everything is in the main file, then scan the file and the WAL.
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, plain := range []string{secret, pw, pending} {
			if bytes.Contains(b, []byte(plain)) {
				t.Fatalf("%s contains plaintext %q", f, plain)
			}
		}
	}
}

func TestEncryptionNonceIsRandom(t *testing.T) {
	s := open(t)
	a, _ := s.seal.seal("same", "l")
	b, _ := s.seal.seal("same", "l")
	if bytes.Equal(a, b) {
		t.Fatal("ciphertexts must differ")
	}
}

func TestWrongKeyFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path, testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	mustConn(t, s, "x")
	s.Close()
	if _, err := Open(path, testKey(2)); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want key mismatch error, got %v", err)
	}
	if _, err := Open(path, []byte("short")); err == nil {
		t.Fatal("short key must be rejected")
	}
}

func TestCorruptSecretNamesRecord(t *testing.T) {
	s := open(t)
	c := mustConn(t, s, "x")
	if _, err := s.db.Exec("UPDATE connections SET secret = ? WHERE id = ?", []byte("garbage-garbage-garbage-garbage-garbage"), c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetConnection(ctx, c.ID)
	if err == nil || !strings.Contains(err.Error(), c.ID) {
		t.Fatalf("want error naming %s, got %v", c.ID, err)
	}
	if _, err := s.ListConnections(ctx); err == nil || !strings.Contains(err.Error(), c.ID) {
		t.Fatalf("list: want error naming %s, got %v", c.ID, err)
	}

	_ = s.SetOakestraCredentials(ctx, core.OakestraCredentials{Username: "u", Password: "p"})
	_, _ = s.db.Exec("UPDATE oakestra_credentials SET password = ?", []byte("garbage-garbage-garbage-garbage-garbage"))
	if _, err := s.OakestraCredentials(ctx); err == nil || !strings.Contains(err.Error(), "oakestra") {
		t.Fatalf("creds: %v", err)
	}
}

func TestSwappedCiphertextRejected(t *testing.T) {
	s := open(t)
	a, b := mustConn(t, s, "a"), mustConn(t, s, "b")
	if _, err := s.db.Exec("UPDATE connections SET secret = (SELECT secret FROM connections WHERE id = ?) WHERE id = ?", a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnection(ctx, b.ID); err == nil {
		t.Fatal("ciphertext moved to another row must not decrypt")
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	raw := testKey(7)

	t.Run("env base64", func(t *testing.T) {
		k, err := LoadOrCreateKey(t.TempDir(), base64.StdEncoding.EncodeToString(raw))
		if err != nil || !bytes.Equal(k, raw) {
			t.Fatalf("%v %v", k, err)
		}
	})
	t.Run("env hex", func(t *testing.T) {
		dir := t.TempDir()
		k, err := LoadOrCreateKey(dir, hex.EncodeToString(raw))
		if err != nil || !bytes.Equal(k, raw) {
			t.Fatalf("%v %v", k, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "secret.key")); err == nil {
			t.Fatal("env key must not create a file")
		}
	})
	t.Run("env wrong size", func(t *testing.T) {
		if _, err := LoadOrCreateKey(t.TempDir(), base64.StdEncoding.EncodeToString(raw[:16])); err == nil {
			t.Fatal("want error")
		}
		if _, err := LoadOrCreateKey(t.TempDir(), "not a key"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("create then reuse", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested")
		k1, err := LoadOrCreateKey(dir, "")
		if err != nil || len(k1) != keySize {
			t.Fatalf("%v %v", k1, err)
		}
		fi, err := os.Stat(filepath.Join(dir, "secret.key"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode: %v %v", fi, err)
		}
		k2, err := LoadOrCreateKey(dir, "")
		if err != nil || !bytes.Equal(k1, k2) {
			t.Fatalf("key changed: %v", err)
		}
	})
	t.Run("wrong size file kept", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret.key")
		if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateKey(dir, ""); err == nil {
			t.Fatal("want error")
		}
		if b, _ := os.ReadFile(path); string(b) != "short" {
			t.Fatal("bad key file must not be replaced")
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores file modes")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "secret.key")
		if err := os.WriteFile(path, raw, 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateKey(dir, ""); err == nil {
			t.Fatal("want error")
		}
		if b, _ := os.ReadFile(path); b != nil {
			t.Fatal("unexpected readable")
		}
		_ = os.Chmod(path, 0o600)
		if b, _ := os.ReadFile(path); !bytes.Equal(b, raw) {
			t.Fatal("file must be untouched")
		}
	})
}

func TestConcurrentWriters(t *testing.T) {
	s := open(t)
	c := mustConn(t, s, "c")
	const workers, per = 16, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*per*3)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				id := int64(w*per + i + 1)
				if err := s.UpsertJob(ctx, &core.Job{PoolID: "p", RequestID: id, StartedAt: ts(1)}); err != nil {
					errs <- err
				}
				if err := s.UpsertJob(ctx, &core.Job{PoolID: "p", RequestID: id, Result: "ok", FinishedAt: ts(2)}); err != nil {
					errs <- err
				}
				p := core.Pool{ConnectionID: c.ID, Name: fmt.Sprintf("p-%d-%d", w, i), VCPUs: 1, MemoryMB: 1, MaxRunners: 1}
				if err := s.CreatePool(ctx, &p); err != nil {
					errs <- err
				}
				if _, _, err := s.ListJobs(ctx, core.JobFilter{Limit: 10}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	jobs, _, err := s.ListJobs(ctx, core.JobFilter{Limit: 200})
	if err != nil || len(jobs) != 200 {
		t.Fatalf("jobs %d %v", len(jobs), err)
	}
	for _, j := range jobs {
		if j.Result != "ok" || j.StartedAt == nil {
			t.Fatalf("lost merge: %+v", j)
		}
	}
	if pools, _ := s.ListPools(ctx); len(pools) != workers*per {
		t.Fatalf("pools: %d", len(pools))
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path, testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(path, testKey(1)); err == nil {
		t.Fatal("want error for newer schema")
	}
}
