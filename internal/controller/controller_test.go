package controller

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

type env struct {
	m     *Manager
	store *fakeStore
	oak   *fakeOakestra
	gh    *fakeGitHub
	clock *fakeClock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{store: newFakeStore(), oak: newFakeOakestra(), gh: newFakeGitHub(), clock: &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	e.m = New(Config{
		Store:             e.store,
		Oakestra:          e.oak,
		Clients:           e.gh,
		DefaultImage:      "ghcr.io/example/runner:latest",
		ReconcileInterval: 10 * time.Millisecond,
		PruneInterval:     10 * time.Millisecond,
		RetryMin:          5 * time.Millisecond,
		RetryMax:          40 * time.Millisecond,
		Now:               e.clock.Now,
	})
	return e
}

func testPool(id string, max int) core.Pool {
	return core.Pool{ID: id, ConnectionID: "c1", Name: "oak-" + id, VCPUs: 4, MemoryMB: 8192, MaxRunners: max, Cluster: "cl", Node: "n1", Enabled: true}
}

// activePool wires a pool's book as a running worker would, without the listener goroutine,
// so Scale and reconcile can be driven step by step.
func (e *env) activePool(t *testing.T, p core.Pool) (*poolBook, *scaler, *fakeSession) {
	t.Helper()
	e.store.putPool(p)
	client, _ := e.gh.NewClient(context.Background(), core.Connection{ID: p.ConnectionID})
	id, err := ensureScaleSet(context.Background(), client, p)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := client.OpenSession(context.Background(), id, "test")
	b := e.m.book(p.ID)
	b.mu.Lock()
	b.pool, b.active, b.client, b.scaleSetID = p, true, client, id
	b.mu.Unlock()
	return b, &scaler{m: e.m, b: b, session: sess}, sess.(*fakeSession)
}

func stats(id, assigned int) *scaleset.RunnerScaleSetMessage {
	return &scaleset.RunnerScaleSetMessage{MessageID: id, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: assigned}}
}

func mustScale(t *testing.T, s *scaler, msg *scaleset.RunnerScaleSetMessage) {
	t.Helper()
	if err := s.Scale(context.Background(), msg); err != nil {
		t.Fatalf("Scale: %v", err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var runnerNameRE = regexp.MustCompile(`^r[0-9a-f]{12}$`)

func TestScaleUpCappedByMaxRunners(t *testing.T) {
	e := newEnv(t)
	p := testPool("p1", 3)
	p.Image = "custom:1"
	_, s, _ := e.activePool(t, p)

	mustScale(t, s, &scaleset.RunnerScaleSetMessage{MessageID: listener.InitialMessageID, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 5}})

	live := e.store.live("p1")
	if len(live) != 3 {
		t.Fatalf("live runners = %d, want 3", len(live))
	}
	for _, r := range live {
		if !runnerNameRE.MatchString(r.Name) || r.State != core.RunnerPending || r.ServiceID == "" {
			t.Errorf("runner %+v", r)
		}
	}
	for _, spec := range e.oak.specs {
		if spec.Image != "custom:1" || spec.VCPUs != 4 || spec.MemoryMB != 8192 || spec.Cluster != "cl" || spec.Node != "n1" || spec.JITConfig != "jit-"+spec.Name {
			t.Errorf("spec %+v", spec)
		}
	}

	// Raising the cap lets the cached target through on the next pass, no message needed.
	b := e.m.book("p1")
	b.mu.Lock()
	b.pool.MaxRunners = 10
	b.mu.Unlock()
	e.m.reconcile(context.Background())
	if n := len(e.store.live("p1")); n != 5 {
		t.Fatalf("live runners after max change = %d, want 5", n)
	}
}

func TestDefaultImageAndRedeliveryNoDuplicates(t *testing.T) {
	e := newEnv(t)
	_, s, sess := e.activePool(t, testPool("p1", 10))
	msg := stats(1, 2)
	msg.JobAvailableMessages = []*scaleset.JobAvailable{{JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 11}}, {JobMessageBase: scaleset.JobMessageBase{RunnerRequestID: 12}}}
	mustScale(t, s, msg)
	mustScale(t, s, msg)
	mustScale(t, s, nil)
	if n := len(e.store.live("p1")); n != 2 {
		t.Fatalf("live runners = %d, want 2", n)
	}
	if e.gh.jitCalls != 2 {
		t.Errorf("JIT configs generated = %d, want 2", e.gh.jitCalls)
	}
	if sess.acquired[11] != 2 || sess.acquired[12] != 2 {
		t.Errorf("acquired = %v", sess.acquired)
	}
	if e.oak.specs[0].Image != "ghcr.io/example/runner:latest" {
		t.Errorf("image = %s", e.oak.specs[0].Image)
	}
}

func jobBase(id int64) scaleset.JobMessageBase {
	return scaleset.JobMessageBase{RunnerRequestID: id, OwnerName: "acme", RepositoryName: "widgets", JobDisplayName: "build", JobWorkflowRef: "acme/widgets/.github/workflows/ci.yml@refs/heads/main", WorkflowRunID: 99, EventName: "push"}
}

func TestJobStartedAndCompleted(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setStatus(r.ServiceID, "RUNNING")
	e.m.reconcile(context.Background())
	if got := e.store.runner(r.Name); got.State != core.RunnerIdle || got.RunningAt == nil || got.ClusterID != "c1" {
		t.Fatalf("after RUNNING: %+v", got)
	}

	started := &scaleset.RunnerScaleSetMessage{MessageID: 2, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 1},
		JobStartedMessages: []*scaleset.JobStarted{{RunnerName: r.Name, JobMessageBase: jobBase(500)}}}
	mustScale(t, s, started)
	if got := e.store.runner(r.Name); got.State != core.RunnerBusy || got.JobRequestID != 500 {
		t.Fatalf("after JobStarted: %+v", got)
	}
	j, ok := e.store.job("p1", 500)
	if !ok || j.Repository != "acme/widgets" || j.RunnerName != r.Name || j.StartedAt == nil || j.ClusterID != "c1" || j.JobName != "build" {
		t.Fatalf("job after start: %+v", j)
	}

	completed := &scaleset.RunnerScaleSetMessage{MessageID: 3, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 0},
		JobCompletedMessages: []*scaleset.JobCompleted{{RunnerName: r.Name, Result: "succeeded", JobMessageBase: jobBase(500)}}}
	mustScale(t, s, completed)
	mustScale(t, s, completed) // redelivery
	if got := e.store.runner(r.Name); got.State != core.RunnerRemoved || got.RemovedAt == nil {
		t.Fatalf("after JobCompleted: %+v", got)
	}
	if e.oak.has(r.ServiceID) {
		t.Error("service should be deleted")
	}
	j, _ = e.store.job("p1", 500)
	if j.Result != "succeeded" || j.FinishedAt == nil || j.WorkerID != "w1" {
		t.Fatalf("job after completion: %+v", j)
	}
	if len(e.store.live("p1")) != 0 {
		t.Error("no runner should be created for a target of 0")
	}

	// A job cancelled before any runner took it has no runner name.
	mustScale(t, s, &scaleset.RunnerScaleSetMessage{MessageID: 4, JobCompletedMessages: []*scaleset.JobCompleted{{Result: "canceled", JobMessageBase: jobBase(501)}}})
	if j, _ := e.store.job("p1", 501); j.Result != "canceled" {
		t.Errorf("cancelled job: %+v", j)
	}
}

func TestStoreFailureRedelivers(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	e.store.upsertErr = errBoom
	msg := &scaleset.RunnerScaleSetMessage{MessageID: 2, JobCompletedMessages: []*scaleset.JobCompleted{{RunnerName: "rnope", Result: "failed", JobMessageBase: jobBase(1)}}}
	if err := s.Scale(context.Background(), msg); err == nil {
		t.Fatal("a failed job write must fail the message so it is redelivered")
	}
	e.store.upsertErr = nil
	mustScale(t, s, msg)
}

func TestTerminalStatusesAreCleanedUp(t *testing.T) {
	for _, status := range []string{"COMPLETED", "DEAD", "FAILED", "gone"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			_, s, _ := e.activePool(t, testPool("p1", 5))
			mustScale(t, s, stats(1, 1))
			r := e.store.live("p1")[0]
			if status == "gone" {
				e.oak.DeleteRunner(context.Background(), r.ServiceID)
			} else {
				e.oak.setStatus(r.ServiceID, status)
			}
			// Drop the target so reconcile does not replace the runner.
			mustScale(t, s, stats(2, 0))
			e.m.reconcile(context.Background())
			if got := e.store.runner(r.Name); got.State != core.RunnerRemoved {
				t.Fatalf("runner %+v", got)
			}
			if e.oak.has(r.ServiceID) {
				t.Error("service still exists")
			}
			if e.gh.registered(r.Name) {
				t.Error("registration of a runner that never took a job should be removed")
			}
		})
	}
}

func TestReplacementAfterCrash(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setStatus(r.ServiceID, "FAILED")
	e.m.reconcile(context.Background())
	live := e.store.live("p1")
	if len(live) != 1 || live[0].Name == r.Name {
		t.Fatalf("want one fresh runner, got %+v", live)
	}
}

func TestStartupTimeout(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setStatus(r.ServiceID, "NO_WORKER_CAPACITY")

	e.clock.Advance(4 * time.Minute)
	mustScale(t, s, stats(2, 0))
	e.m.reconcile(context.Background())
	if got := e.store.runner(r.Name); got.State != core.RunnerPending || got.OakestraStatus != "NO_WORKER_CAPACITY" {
		t.Fatalf("before timeout: %+v", got)
	}
	e.clock.Advance(2 * time.Minute)
	e.m.reconcile(context.Background())
	if got := e.store.runner(r.Name); got.State != core.RunnerRemoved {
		t.Fatalf("after timeout: %+v", got)
	}
	if e.oak.has(r.ServiceID) || e.gh.registered(r.Name) {
		t.Error("service and registration should be gone")
	}
}

func TestIdleScaleDown(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 3))
	live := e.store.live("p1")
	for _, r := range live {
		e.oak.setStatus(r.ServiceID, "RUNNING")
	}
	e.m.reconcile(context.Background())

	// Queued jobs were cancelled; GitHub now wants one runner.
	mustScale(t, s, stats(2, 1))
	e.m.reconcile(context.Background())
	if n := len(e.store.live("p1")); n != 3 {
		t.Fatalf("idle runners must survive until the idle timeout, live = %d", n)
	}

	// One of the idle runners got a job in the meantime: GitHub refuses to remove it.
	busy := live[0].Name
	e.gh.setBusy(busy)
	e.clock.Advance(11 * time.Minute)
	e.m.reconcile(context.Background())

	got := e.store.live("p1")
	if len(got) != 1 || got[0].Name != busy {
		t.Fatalf("want only the busy runner left, got %+v", got)
	}
	if !e.gh.registered(busy) {
		t.Error("busy runner registration must stay")
	}
	for _, r := range live[1:] {
		if e.oak.has(r.ServiceID) || e.gh.registered(r.Name) {
			t.Errorf("runner %s should be gone from Oakestra and GitHub", r.Name)
		}
	}
}

func TestScaleDownKeepsServiceWhenRegistrationRemovalFails(t *testing.T) {
	e := newEnv(t)
	b, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setStatus(r.ServiceID, "RUNNING")
	e.m.reconcile(context.Background())
	mustScale(t, s, stats(2, 0))
	e.clock.Advance(11 * time.Minute)

	b.mu.Lock()
	b.client = &failingClient{ScaleSetClient: b.client}
	b.mu.Unlock()
	e.m.reconcile(context.Background())
	if !e.oak.has(r.ServiceID) {
		t.Fatal("the service must not be deleted while the registration could not be removed")
	}
}

type failingClient struct{ ScaleSetClient }

func (failingClient) GetRunnerByName(context.Context, string) (*scaleset.RunnerReference, error) {
	return nil, errBoom
}

func TestOrphanCleanupOnStart(t *testing.T) {
	e := newEnv(t)
	e.store.putPool(testPool("p1", 5))
	orphan, _ := e.oak.CreateRunner(context.Background(), core.RunnerSpec{Name: "rorphan"})
	known, _ := e.oak.CreateRunner(context.Background(), core.RunnerSpec{Name: "rknown"})
	e.store.CreateRunner(context.Background(), &core.Runner{Name: "rknown", PoolID: "p1", ServiceID: known, State: core.RunnerIdle})
	e.store.CreateRunner(context.Background(), &core.Runner{Name: "rlost", PoolID: "p1", ServiceID: "svc-missing", State: core.RunnerBusy, JobRequestID: 3})

	e.oak.setErrs(nil, nil, core.ErrNoCredentials)
	e.m.cleanOrphans(context.Background())
	if e.m.orphansDone() || !e.oak.has(orphan) {
		t.Fatal("scan must wait for Oakestra credentials")
	}
	e.oak.setErrs(nil, nil, nil)
	e.m.reconcile(context.Background())

	if !e.m.orphansDone() {
		t.Fatal("scan should have completed")
	}
	if e.oak.has(orphan) {
		t.Error("orphan service should be deleted")
	}
	if !e.oak.has(known) {
		t.Error("known service must stay")
	}
	if got := e.store.runner("rlost"); got.State != core.RunnerRemoved {
		t.Errorf("runner without service: %+v", got)
	}
}

func TestOakestraFailureDoesNotBlockAcks(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	e.oak.setErrs(core.ErrNoCredentials, nil, nil)

	msg := stats(1, 2)
	msg.JobCompletedMessages = []*scaleset.JobCompleted{{Result: "succeeded", JobMessageBase: jobBase(7)}}
	mustScale(t, s, msg)
	if _, ok := e.store.job("p1", 7); !ok {
		t.Fatal("job bookkeeping must happen even when runners cannot be created")
	}
	if n := len(e.store.live("p1")); n != 0 {
		t.Fatalf("live = %d", n)
	}
	if p := e.store.pool("p1"); !strings.Contains(p.LastError, "credentials") {
		t.Fatalf("LastError = %q", p.LastError)
	}
	e.gh.mu.Lock()
	regs := len(e.gh.regs)
	e.gh.mu.Unlock()
	if regs != 0 {
		t.Errorf("registrations of undeployed runners must be removed, have %d", regs)
	}

	// Backoff: an immediate reconcile does not hammer GitHub with JIT configs.
	calls := e.gh.jitCalls
	e.m.reconcile(context.Background())
	if e.gh.jitCalls != calls {
		t.Error("creation should back off")
	}

	e.oak.setErrs(nil, nil, nil)
	e.clock.Advance(time.Second)
	e.m.reconcile(context.Background())
	if n := len(e.store.live("p1")); n != 2 {
		t.Fatalf("live after recovery = %d, want 2", n)
	}
	if p := e.store.pool("p1"); p.LastError != "" {
		t.Errorf("LastError not cleared: %q", p.LastError)
	}
}

func TestCompletedRunnerDeleteFailureIsRetried(t *testing.T) {
	e := newEnv(t)
	_, s, _ := e.activePool(t, testPool("p1", 5))
	mustScale(t, s, stats(1, 1))
	r := e.store.live("p1")[0]
	e.oak.setErrs(nil, errBoom, nil)
	msg := &scaleset.RunnerScaleSetMessage{MessageID: 2, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 1},
		JobCompletedMessages: []*scaleset.JobCompleted{{RunnerName: r.Name, Result: "succeeded", JobMessageBase: jobBase(9)}}}
	mustScale(t, s, msg)
	// The finished runner no longer counts, so a new job gets a fresh runner.
	if n := len(e.store.live("p1")); n != 2 {
		t.Fatalf("live = %d, want the doomed runner plus a replacement", n)
	}
	e.oak.setErrs(nil, nil, nil)
	e.m.reconcile(context.Background())
	if got := e.store.runner(r.Name); got.State != core.RunnerRemoved || e.oak.has(r.ServiceID) {
		t.Fatalf("doomed runner not cleaned up: %+v", got)
	}
}

// --- tests through Run, with the real listener driving Scale ---

func (e *env) startRun(t *testing.T) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.m.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not stop")
		}
	}
}

func (e *env) waitSession(t *testing.T, name string) *fakeSession {
	t.Helper()
	var s *fakeSession
	eventually(t, "session for "+name, func() bool {
		id := e.gh.ssID(name)
		if id == 0 {
			return false
		}
		s = e.gh.session(id)
		return s != nil
	})
	return s
}

func readyConn(id string) core.Connection {
	return core.Connection{ID: id, Name: id, ConfigURL: "https://github.com/" + id, AuthKind: core.AuthPAT, Status: core.ConnectionReady, Secret: "t"}
}

func TestReloadLifecycle(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	p := testPool("p1", 2)
	e.store.putPool(p)
	stop := e.startRun(t)
	defer stop()

	sess := e.waitSession(t, p.Name)
	eventually(t, "scale set id recorded", func() bool { return e.store.pool("p1").ScaleSetID != 0 })
	sess.msgs <- stats(1, 5)
	eventually(t, "2 runners", func() bool { return len(e.store.live("p1")) == 2 })
	eventually(t, "message acked", func() bool { return len(sess.ackedIDs()) == 1 })

	// MaxRunners applies live, without a new session.
	p.MaxRunners = 4
	e.store.putPool(p)
	e.m.Reload()
	e.m.Reload()
	eventually(t, "4 runners", func() bool { return len(e.store.live("p1")) == 4 })
	eventually(t, "listener capacity", func() bool { return sess.maxCapacity() == 4 })
	if e.gh.session(e.gh.ssID(p.Name)) != sess {
		t.Fatal("a max runners change must not restart the listener")
	}

	// Disable: the listener stops and runners that are not busy are drained.
	for _, r := range e.store.live("p1") {
		e.oak.setStatus(r.ServiceID, "RUNNING")
	}
	busy := e.store.live("p1")[0]
	e.gh.setBusy(busy.Name)
	p.Enabled = false
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "session closed", func() bool { return sess.isClosedNow() })
	eventually(t, "drained to the busy runner", func() bool {
		live := e.store.live("p1")
		return len(live) == 1 && live[0].Name == busy.Name
	})

	// Re-enable: a new session on the same scale set.
	ssid := e.gh.ssID(p.Name)
	p.Enabled = true
	e.store.putPool(p)
	e.m.Reload()
	eventually(t, "new session", func() bool { s := e.gh.session(ssid); return s != nil && s != sess })

	// Delete: runners and scale set go away.
	e.store.DeletePool(context.Background(), "p1")
	e.m.Reload()
	eventually(t, "scale set deleted", func() bool { return e.gh.ssID(p.Name) == 0 })
	eventually(t, "runners removed", func() bool { return len(e.store.live("p1")) == 0 && e.oak.count() == 0 })
}

func (s *fakeSession) isClosedNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isClosed
}

func TestRenameRecreatesScaleSet(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("c1"))
	p := testPool("p1", 2)
	e.store.putPool(p)
	stop := e.startRun(t)
	defer stop()

	sess := e.waitSession(t, p.Name)
	sess.msgs <- stats(1, 1)
	eventually(t, "runner", func() bool { return len(e.store.live("p1")) == 1 })
	old := e.store.live("p1")[0]

	p.Name = "oak-renamed"
	e.store.putPool(p)
	e.m.Reload()
	e.waitSession(t, "oak-renamed")
	eventually(t, "old scale set deleted", func() bool { return e.gh.ssID("oak-p1") == 0 })
	eventually(t, "old runner removed", func() bool { return e.store.runner(old.Name).State == core.RunnerRemoved })
}

func TestFailingListenerDoesNotAffectOthers(t *testing.T) {
	e := newEnv(t)
	e.store.putConn(readyConn("bad"))
	e.store.putConn(readyConn("good"))
	pb := testPool("pb", 2)
	pb.ConnectionID = "bad"
	pg := testPool("pg", 2)
	pg.ConnectionID = "good"
	e.store.putPool(pb)
	e.store.putPool(pg)
	e.gh.setFail("bad", errBoom)

	stop := e.startRun(t)
	defer stop()

	sess := e.waitSession(t, pg.Name)
	sess.msgs <- stats(1, 1)
	eventually(t, "good pool scales", func() bool { return len(e.store.live("pg")) == 1 })
	eventually(t, "bad pool error recorded", func() bool { return strings.Contains(e.store.pool("pb").LastError, "boom") })
	if e.store.pool("pg").LastError != "" {
		t.Errorf("good pool has error %q", e.store.pool("pg").LastError)
	}

	e.gh.setFail("bad", nil)
	e.waitSession(t, pb.Name)
	eventually(t, "error cleared after recovery", func() bool { return e.store.pool("pb").LastError == "" })
}

func TestPruneRuns(t *testing.T) {
	e := newEnv(t)
	stop := e.startRun(t)
	eventually(t, "prune", func() bool {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		return e.store.prunes > 0
	})
	stop()
}
