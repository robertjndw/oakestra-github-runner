package controller

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
	"github.com/robertjndw/oakestra-github-runner/internal/oakestra"
	"github.com/robertjndw/oakestra-github-runner/internal/oakestra/oakestratest"
	"github.com/robertjndw/oakestra-github-runner/internal/store"
)

// The unit tests in this package run against fakes of the store and of Oakestra. This test
// runs one job through the real SQLite store and the real Oakestra client talking to the fake
// system manager, so a mismatch between the packages (status strings, error values, what the
// SLA carries) shows up here. Only GitHub stays faked.
func TestJobLifecycleAgainstRealStoreAndOakestraClient(t *testing.T) {
	ctx := context.Background()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sm := oakestratest.NewTB(t, oakestratest.Options{Users: map[string]string{"ghrunner": "secret"}})
	oak := oakestra.New(oakestra.Config{
		BaseURL: sm.URL,
		Credentials: func(context.Context) (core.OakestraCredentials, string, error) {
			return core.OakestraCredentials{Username: "ghrunner", Password: "secret"}, "env", nil
		},
	})

	conn := &core.Connection{Name: "acme", ConfigURL: "https://github.com/acme", AuthKind: core.AuthPAT, Status: core.ConnectionReady, Secret: "pat"}
	if err := st.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	pool := &core.Pool{ConnectionID: conn.ID, Name: "oakestra-2vcpu-ubuntu-2404", VCPUs: 2, MemoryMB: 4096, MaxRunners: 2, Cluster: "edge", Enabled: true}
	if err := st.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}

	gh := newFakeGitHub()
	m := New(Config{Store: st, Oakestra: oak, Clients: gh, DefaultImage: "ghcr.io/actions/actions-runner:latest"})

	client, _ := gh.NewClient(ctx, *conn)
	ssID, err := ensureScaleSet(ctx, client, *pool)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := client.OpenSession(ctx, ssID, "test")
	b := m.book(pool.ID)
	b.mu.Lock()
	b.pool, b.active, b.client, b.scaleSetID = *pool, true, client, ssID
	b.mu.Unlock()
	s := &scaler{m: m, b: b, session: sess}

	// One queued job: one runner service with the JIT config and the pool's size and pin.
	mustScale(t, s, stats(1, 1))
	runners, err := st.ListRunners(ctx, pool.ID)
	if err != nil || len(runners) != 1 {
		t.Fatalf("runners = %v, err = %v", runners, err)
	}
	r := runners[0]
	svc, ok := sm.Service(r.ServiceID)
	if !ok {
		t.Fatalf("service %s not created on Oakestra", r.ServiceID)
	}
	if svc.SLA.Name != r.Name || svc.SLA.Code != "ghcr.io/actions/actions-runner:latest" || !svc.SLA.OneShot ||
		svc.SLA.VCPUs != 2 || svc.SLA.Memory != 4096 ||
		!slices.Contains(svc.SLA.Environment, "ACTIONS_RUNNER_INPUT_JITCONFIG=jit-"+r.Name) ||
		len(svc.SLA.Constraints) != 1 || svc.SLA.Constraints[0].Cluster != "edge" {
		t.Fatalf("SLA = %+v", svc.SLA)
	}
	if len(svc.Instances) != 1 {
		t.Fatalf("instance not deployed: %+v", svc.Instances)
	}

	// Oakestra reports it running: the runner turns idle and records where it landed.
	if err := sm.SetInstancePlacement(r.ServiceID, "cluster-1", "worker-7"); err != nil {
		t.Fatal(err)
	}
	if err := sm.SetInstanceStatus(r.ServiceID, oakestratest.StatusRunning); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	got, err := st.GetRunner(ctx, r.Name)
	if err != nil || got.State != core.RunnerIdle || got.ClusterID != "cluster-1" || got.WorkerID != "worker-7" || got.RunningAt == nil {
		t.Fatalf("after RUNNING: %+v, err = %v", got, err)
	}

	// The job starts and completes. Completed arrives twice, as after a lost ack.
	base := jobBase(500)
	base.QueueTime = time.Now().Add(-time.Minute)
	mustScale(t, s, &scaleset.RunnerScaleSetMessage{MessageID: 2, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 1},
		JobStartedMessages: []*scaleset.JobStarted{{RunnerName: r.Name, JobMessageBase: base}}})
	done := &scaleset.RunnerScaleSetMessage{MessageID: 3, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 0},
		JobCompletedMessages: []*scaleset.JobCompleted{{RunnerName: r.Name, Result: "succeeded", JobMessageBase: base}}}
	mustScale(t, s, done)
	mustScale(t, s, done)

	if _, ok := sm.Service(r.ServiceID); ok {
		t.Error("the runner service should be deleted on Oakestra")
	}
	if live, _ := st.ListRunners(ctx, pool.ID); len(live) != 0 {
		t.Errorf("live runners after completion: %+v", live)
	}
	jobs, next, err := st.ListJobs(ctx, core.JobFilter{PoolID: pool.ID})
	if err != nil || len(jobs) != 1 || next != "" {
		t.Fatalf("jobs = %+v, next = %q, err = %v", jobs, next, err)
	}
	j := jobs[0]
	if j.Result != "succeeded" || j.RunnerName != r.Name || j.Repository != "acme/widgets" || j.ClusterID != "cluster-1" ||
		j.WorkerID != "worker-7" || j.QueuedAt == nil || j.StartedAt == nil || j.FinishedAt == nil {
		t.Fatalf("job = %+v", j)
	}

	// A runner that crashes before taking a job must be deleted, or Oakestra redeploys it
	// forever. The queue still wants a runner, so a replacement appears.
	mustScale(t, s, stats(4, 1))
	runners, _ = st.ListRunners(ctx, pool.ID)
	if len(runners) != 1 {
		t.Fatalf("runners = %+v", runners)
	}
	crashed := runners[0]
	if err := sm.SetInstanceStatus(crashed.ServiceID, oakestratest.StatusDead); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	if _, ok := sm.Service(crashed.ServiceID); ok {
		t.Error("the dead runner service should be deleted on Oakestra")
	}
	if gh.registered(crashed.Name) {
		t.Error("the dead runner's GitHub registration should be removed")
	}
	m.reconcile(ctx)
	runners, _ = st.ListRunners(ctx, pool.ID)
	if len(runners) != 1 || runners[0].Name == crashed.Name {
		t.Fatalf("expected one replacement runner, got %+v", runners)
	}

	// A service nobody recorded (crash between Oakestra create and the store write) is an
	// orphan and goes away on startup.
	orphan, err := oak.CreateRunner(ctx, core.RunnerSpec{Name: "rorphan000001", Image: "img", VCPUs: 1, MemoryMB: 512, JITConfig: "x"})
	if err != nil {
		t.Fatal(err)
	}
	m.cleanOrphans(ctx)
	if _, ok := sm.Service(orphan); ok {
		t.Error("the orphan service should be deleted")
	}
	if _, ok := sm.Service(runners[0].ServiceID); !ok {
		t.Error("the recorded runner must survive orphan cleanup")
	}
}
