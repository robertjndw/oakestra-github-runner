package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

func (g *fakeGitHub) addScaleSet(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextID++
	g.scaleSets[name] = g.nextID
	return g.nextID
}

func (g *fakeGitHub) scaleSetDeleted(id int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range g.deletedSS {
		if d == id {
			return true
		}
	}
	return false
}

// idleRunner records a running idle runner that has been idle for an hour.
func (e *env) idleRunner(t *testing.T, poolID, name string) string {
	t.Helper()
	svc, _ := e.oak.CreateRunner(context.Background(), core.RunnerSpec{Name: name})
	e.oak.setStatus(svc, "RUNNING")
	old := e.clock.Now().Add(-time.Hour)
	if err := e.store.CreateRunner(context.Background(), &core.Runner{Name: name, PoolID: poolID, ServiceID: svc, State: core.RunnerIdle, CreatedAt: old, RunningAt: &old}); err != nil {
		t.Fatal(err)
	}
	return svc
}

// After a restart, a disabled pool's book has no client from a listener. Draining must build
// one from the pool's connection.
func TestDisabledPoolIdleRunnerDrainedAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(core.Connection{ID: "c1", Status: core.ConnectionReady, AuthKind: core.AuthPAT, Secret: "x"})
	p := testPool("p1", 3)
	p.Enabled = false
	e.store.putPool(p)
	svc := e.idleRunner(t, "p1", "rabc")

	ctx := context.Background()
	e.m.cleanOrphans(ctx)
	if err := e.m.apply(ctx); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		e.clock.Advance(time.Hour)
		e.m.reconcile(ctx)
	}
	if e.oak.has(svc) {
		t.Errorf("idle runner of disabled pool still running after restart; state=%s", e.store.runner("rabc").State)
	}
}

// Without any usable GitHub client the runner is still deleted, but only after the grace
// period.
func TestStoppedPoolWithoutClientDrainsAfterGrace(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(core.Connection{ID: "c1", Status: core.ConnectionPendingInstallation, AuthKind: core.AuthApp})
	e.store.putPool(testPool("p1", 3))
	svc := e.idleRunner(t, "p1", "rabc")

	ctx := context.Background()
	if err := e.m.apply(ctx); err != nil {
		t.Fatal(err)
	}
	e.m.reconcile(ctx)
	if !e.oak.has(svc) {
		t.Fatal("runner must survive the grace period")
	}
	e.clock.Advance(11 * time.Minute)
	e.m.reconcile(ctx)
	if e.oak.has(svc) || e.store.runner("rabc").State != core.RunnerRemoved {
		t.Fatal("runner should be removed after the grace period")
	}
}

// Renaming a pool while it is disabled must remove the old scale set.
func TestRenameWhileDisabledDeletesOldScaleSet(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(core.Connection{ID: "c1", Status: core.ConnectionReady, AuthKind: core.AuthPAT, Secret: "x"})
	p := testPool("p1", 3)
	e.store.putPool(p)
	stop := e.startRun(t)
	defer stop()
	eventually(t, "scale set", func() bool { return e.gh.ssID("oak-p1") != 0 })
	oldID := e.gh.ssID("oak-p1")

	p.Enabled = false
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "pool stopped", func() bool { return e.gh.session(oldID).isClosedNow() })
	p.Name = "oak-renamed"
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "old scale set deleted while disabled", func() bool { return e.gh.scaleSetDeleted(oldID) })
	p.Enabled = true
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "new scale set", func() bool { return e.gh.ssID("oak-renamed") != 0 })
	if e.gh.ssID("oak-p1") != 0 {
		t.Errorf("old scale set oak-p1 still exists after rename")
	}
}

// Deleting a pool whose listener never ran in this process must still delete its scale set,
// found through the persisted id.
func TestDeleteDisabledPoolAfterRestartDeletesScaleSet(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	ssid := e.gh.addScaleSet("oak-p1")
	p := testPool("p1", 3)
	p.Enabled = false
	e.store.putPool(p)
	e.store.SetPoolRuntime(context.Background(), "p1", ssid, "")
	svc := e.idleRunner(t, "p1", "rabc")

	ctx := context.Background()
	if err := e.m.apply(ctx); err != nil {
		t.Fatal(err)
	}
	e.store.DeletePool(ctx, "p1")
	if err := e.m.apply(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "scale set deleted", func() bool { return e.gh.scaleSetDeleted(ssid) })
	eventually(t, "runner removed", func() bool { return !e.oak.has(svc) && e.store.runner("rabc").State == core.RunnerRemoved })
	eventually(t, "book forgotten", func() bool { return e.m.lookupBook("p1") == nil })
}

// A scale set with the pool's name that this addon did not record is refused and survives
// the pool's deletion.
func TestForeignScaleSetIsNotAdoptedOrDeleted(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	foreign := e.gh.addScaleSet("oak-p1")
	e.store.putPool(testPool("p1", 3))
	stop := e.startRun(t)
	defer stop()

	eventually(t, "refusal recorded", func() bool { return strings.Contains(e.store.pool("p1").LastError, "already exists") })
	if e.gh.session(foreign) != nil {
		t.Fatal("must not listen on a foreign scale set")
	}
	e.store.DeletePool(context.Background(), "p1")
	e.m.Reload()
	eventually(t, "book forgotten", func() bool { return e.m.lookupBook("p1") == nil })
	if e.gh.scaleSetDeleted(foreign) || e.gh.ssID("oak-p1") != foreign {
		t.Fatal("a foreign scale set must never be deleted")
	}
}

// A restart adopts the scale set whose id was recorded.
func TestRecordedScaleSetIsAdopted(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	ssid := e.gh.addScaleSet("oak-p1")
	e.store.putPool(testPool("p1", 3))
	e.store.SetPoolRuntime(context.Background(), "p1", ssid, "")
	stop := e.startRun(t)
	defer stop()
	eventually(t, "session on recorded scale set", func() bool { return e.gh.session(ssid) != nil })
	if p := e.store.pool("p1"); p.LastError != "" || p.ScaleSetID != ssid {
		t.Fatalf("pool = %+v", p)
	}
}

func TestCompletedBeforeJobCompletedDoesNotOverprovision(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setStatus(r.ServiceID, "RUNNING")
	e.m.reconcile(context.Background())
	started := stats(2, 1)
	started.JobStartedMessages = []*scaleset.JobStarted{{RunnerName: r.Name, JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 9}}}
	mustScale(t, s, started)
	// The job finishes: the container exits (COMPLETED) and reconcile sees it before
	// JobCompleted arrives.
	e.oak.setStatus(r.ServiceID, "COMPLETED")
	e.m.reconcile(context.Background())
	if n := len(e.store.live("p1")); n != 0 {
		t.Fatalf("%d replacement runner(s) created for a job that already finished", n)
	}

	// Once JobCompleted is in, the slot is free again for real demand.
	done := stats(3, 1)
	done.JobCompletedMessages = []*scaleset.JobCompleted{{RunnerName: r.Name, Result: "succeeded", JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 9}}}
	mustScale(t, s, done)
	if n := len(e.store.live("p1")); n != 1 {
		t.Fatalf("live = %d, want 1 for the new job", n)
	}
}

func TestRetiredJobSlotExpires(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setStatus(r.ServiceID, "COMPLETED")
	e.m.reconcile(context.Background())
	if n := len(e.store.live("p1")); n != 0 {
		t.Fatalf("live = %d", n)
	}
	e.clock.Advance(retiredJobGrace + time.Second)
	e.m.reconcile(context.Background())
	if n := len(e.store.live("p1")); n != 1 {
		t.Fatalf("live = %d, want 1 once the missing JobCompleted is given up on", n)
	}
}

// Re-enabling a pool must wait for fresh statistics instead of scaling from the old ones.
func TestReenableDoesNotScaleFromStaleStats(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	p := testPool("p1", 5)
	e.store.putPool(p)
	stop := e.startRun(t)
	defer stop()

	sess := e.waitSession(t, p.Name)
	sess.msgs <- stats(1, 3)
	eventually(t, "3 runners", func() bool { return len(e.store.live("p1")) == 3 })
	for _, r := range e.store.live("p1") {
		e.oak.setStatus(r.ServiceID, "RUNNING")
	}
	p.Enabled = false
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "drained", func() bool { return len(e.store.live("p1")) == 0 })

	// The listener cannot come up, so only stale statistics could cause runners.
	e.gh.setFail("c1", errBoom)
	p.Enabled = true
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "listener failing", func() bool { return strings.Contains(e.store.pool("p1").LastError, "boom") })
	time.Sleep(50 * time.Millisecond) // several reconcile ticks
	if n := len(e.store.live("p1")); n != 0 {
		t.Fatalf("%d runners created from stale statistics", n)
	}
}

// A teardown stuck on slow Oakestra calls must not block reconcile or reloads.
func TestTeardownDoesNotBlockReconcile(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	e.store.putPool(testPool("p1", 3))
	e.store.putPool(testPool("p2", 3))
	e.idleRunner(t, "p1", "rabc")
	ctx := context.Background()
	if err := e.m.apply(ctx); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	e.oak.mu.Lock()
	e.oak.deleteGate = gate
	e.oak.mu.Unlock()
	e.store.DeletePool(ctx, "p1")
	if err := e.m.apply(ctx); err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() {
		e.m.reconcile(ctx)
		_ = e.m.apply(ctx)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile blocked behind a teardown")
	}
	close(gate)
	eventually(t, "teardown done", func() bool { return e.store.runner("rabc").State == core.RunnerRemoved })
	e.m.shutdown()
}

// Shutdown in the middle of creating a runner must not orphan the service.
func TestCreationSurvivesCancellation(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	ctx, cancel := context.WithCancel(context.Background())
	e.oak.createHook = func(c context.Context) error {
		cancel()
		return c.Err()
	}
	if err := s.Scale(ctx, stats(1, 3)); err != nil {
		t.Fatal(err)
	}
	live := e.store.live("p1")
	if len(live) != 1 || !e.oak.has(live[0].ServiceID) || e.oak.count() != 1 {
		t.Fatalf("want exactly the one started creation recorded, live = %+v, services = %d", live, e.oak.count())
	}
}

func TestOrphanScanIsPeriodic(t *testing.T) {
	e := newEnv(t)
	e.m.cfg.OrphanScanInterval = 10 * time.Millisecond
	stop := e.startRun(t)
	defer stop()
	eventually(t, "startup scan", func() bool { return e.m.orphansDone() })
	orphan, _ := e.oak.CreateRunner(context.Background(), core.RunnerSpec{Name: "rlate"})
	eventually(t, "late orphan deleted", func() bool { return !e.oak.has(orphan) })
}

func TestLastErrorIsBounded(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	e.store.putPool(testPool("p1", 3))
	e.gh.setFail("c1", errLong)
	stop := e.startRun(t)
	defer stop()
	eventually(t, "error recorded", func() bool { return e.store.pool("p1").LastError != "" })
	if n := len([]rune(e.store.pool("p1").LastError)); n > maxLastError+3 {
		t.Fatalf("LastError has %d characters", n)
	}
}

var errLong = errorString(strings.Repeat("upstream says no ", 500))

type errorString string

func (e errorString) Error() string { return string(e) }
