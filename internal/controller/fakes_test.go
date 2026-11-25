package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/google/uuid"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// fakeStore keeps only what the controller touches.
type fakeStore struct {
	mu        sync.Mutex
	conns     map[string]core.Connection
	pools     map[string]core.Pool
	runners   map[string]core.Runner
	jobs      map[string]core.Job
	prunes    int
	upsertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{conns: map[string]core.Connection{}, pools: map[string]core.Pool{}, runners: map[string]core.Runner{}, jobs: map[string]core.Job{}}
}

func (s *fakeStore) putConn(c core.Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[c.ID] = c
}

func (s *fakeStore) putPool(p core.Pool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.pools[p.ID]; ok {
		p.ScaleSetID, p.LastError = old.ScaleSetID, old.LastError
	}
	s.pools[p.ID] = p
}

func (s *fakeStore) pool(id string) core.Pool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pools[id]
}

func (s *fakeStore) runner(name string) core.Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runners[name]
}

func (s *fakeStore) job(poolID string, id int64) (core.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[fmt.Sprintf("%s/%d", poolID, id)]
	return j, ok
}

func (s *fakeStore) live(poolID string) []core.Runner {
	rs, _ := s.ListRunners(context.Background(), poolID)
	return rs
}

func (s *fakeStore) ListConnections(context.Context) ([]core.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []core.Connection{}
	for _, c := range s.conns {
		out = append(out, c)
	}
	return out, nil
}

func (s *fakeStore) GetConnection(_ context.Context, id string) (core.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if !ok {
		return c, core.ErrNotFound
	}
	return c, nil
}
func (s *fakeStore) CreateConnection(context.Context, *core.Connection) error { return nil }
func (s *fakeStore) UpdateConnection(context.Context, *core.Connection) error { return nil }
func (s *fakeStore) DeleteConnection(context.Context, string) error           { return nil }

func (s *fakeStore) ListPools(context.Context) ([]core.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []core.Pool{}
	for _, p := range s.pools {
		out = append(out, p)
	}
	return out, nil
}

func (s *fakeStore) GetPool(_ context.Context, id string) (core.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[id]
	if !ok {
		return p, core.ErrNotFound
	}
	return p, nil
}
func (s *fakeStore) CreatePool(context.Context, *core.Pool) error { return nil }
func (s *fakeStore) UpdatePool(context.Context, *core.Pool) error { return nil }

func (s *fakeStore) SetPoolRuntime(_ context.Context, id string, ssid int, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pools[id]
	if !ok {
		return core.ErrNotFound
	}
	p.ScaleSetID, p.LastError = ssid, lastErr
	s.pools[id] = p
	return nil
}

func (s *fakeStore) DeletePool(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pools, id)
	return nil
}

func (s *fakeStore) ListRunners(_ context.Context, poolID string) ([]core.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []core.Runner{}
	for _, r := range s.runners {
		if r.State != core.RunnerRemoved && (poolID == "" || r.PoolID == poolID) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *fakeStore) GetRunner(_ context.Context, name string) (core.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runners[name]
	if !ok {
		return r, core.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) CreateRunner(_ context.Context, r *core.Runner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runners[r.Name]; ok {
		return core.ErrConflict
	}
	s.runners[r.Name] = *r
	return nil
}

func (s *fakeStore) UpdateRunner(_ context.Context, r *core.Runner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runners[r.Name]; !ok {
		return core.ErrNotFound
	}
	s.runners[r.Name] = *r
	return nil
}

func (s *fakeStore) UpsertJob(_ context.Context, j *core.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	key := fmt.Sprintf("%s/%d", j.PoolID, j.RequestID)
	cur := s.jobs[key]
	merge := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	cur.PoolID, cur.RequestID = j.PoolID, j.RequestID
	merge(&cur.RunnerName, j.RunnerName)
	merge(&cur.Repository, j.Repository)
	merge(&cur.WorkflowRef, j.WorkflowRef)
	merge(&cur.JobName, j.JobName)
	merge(&cur.EventName, j.EventName)
	merge(&cur.Result, j.Result)
	merge(&cur.ClusterID, j.ClusterID)
	merge(&cur.WorkerID, j.WorkerID)
	if j.WorkflowRunID != 0 {
		cur.WorkflowRunID = j.WorkflowRunID
	}
	if j.QueuedAt != nil {
		cur.QueuedAt = j.QueuedAt
	}
	if j.StartedAt != nil {
		cur.StartedAt = j.StartedAt
	}
	if j.FinishedAt != nil {
		cur.FinishedAt = j.FinishedAt
	}
	s.jobs[key] = cur
	return nil
}

func (s *fakeStore) ListJobs(context.Context, core.JobFilter) ([]core.Job, string, error) {
	return nil, "", nil
}
func (s *fakeStore) OakestraCredentials(context.Context) (core.OakestraCredentials, error) {
	return core.OakestraCredentials{}, core.ErrNotFound
}
func (s *fakeStore) SetOakestraCredentials(context.Context, core.OakestraCredentials) error {
	return nil
}
func (s *fakeStore) SaveManifestState(context.Context, string, core.Connection) error { return nil }
func (s *fakeStore) TakeManifestState(context.Context, string) (core.Connection, error) {
	return core.Connection{}, core.ErrNotFound
}
func (s *fakeStore) Prune(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prunes++
	return nil
}
func (s *fakeStore) Close() error { return nil }

// fakeOakestra holds services by id.
type fakeOakestra struct {
	mu        sync.Mutex
	next      int
	services  map[string]core.ServiceInstance
	specs     []core.RunnerSpec
	deleted   []string
	createErr error
	deleteErr error
	getErr    error
	// createHook runs inside CreateRunner before the service is made; an error aborts.
	createHook func(ctx context.Context) error
	// deleteGate, when set, makes DeleteRunner wait until it is closed.
	deleteGate chan struct{}
}

func newFakeOakestra() *fakeOakestra {
	return &fakeOakestra{services: map[string]core.ServiceInstance{}}
}

func (o *fakeOakestra) CreateRunner(ctx context.Context, spec core.RunnerSpec) (string, error) {
	o.mu.Lock()
	hook := o.createHook
	o.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return "", err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.createErr != nil {
		return "", o.createErr
	}
	o.next++
	id := fmt.Sprintf("svc%d", o.next)
	o.services[id] = core.ServiceInstance{ServiceID: id, Name: spec.Name}
	o.specs = append(o.specs, spec)
	return id, nil
}

func (o *fakeOakestra) GetRunner(_ context.Context, id string) (core.ServiceInstance, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.getErr != nil {
		return core.ServiceInstance{}, o.getErr
	}
	s, ok := o.services[id]
	if !ok {
		return s, core.ErrNotFound
	}
	return s, nil
}

func (o *fakeOakestra) ListRunners(context.Context) ([]core.ServiceInstance, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.getErr != nil {
		return nil, o.getErr
	}
	out := []core.ServiceInstance{}
	for _, s := range o.services {
		out = append(out, s)
	}
	return out, nil
}

func (o *fakeOakestra) DeleteRunner(_ context.Context, id string) error {
	o.mu.Lock()
	gate := o.deleteGate
	o.mu.Unlock()
	if gate != nil {
		<-gate
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.deleteErr != nil {
		return o.deleteErr
	}
	delete(o.services, id)
	o.deleted = append(o.deleted, id)
	return nil
}

func (o *fakeOakestra) setStatus(id, status string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.services[id]
	s.Status, s.ClusterID, s.WorkerID = status, "c1", "w1"
	o.services[id] = s
}

func (o *fakeOakestra) setErrs(create, del, get error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.createErr, o.deleteErr, o.getErr = create, del, get
}

func (o *fakeOakestra) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.services)
}

func (o *fakeOakestra) has(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.services[id]
	return ok
}

func (o *fakeOakestra) ListClusters(context.Context) ([]core.Cluster, error) { return nil, nil }
func (o *fakeOakestra) Status(context.Context) core.OakestraStatus           { return core.OakestraStatus{} }
func (o *fakeOakestra) CredentialsChanged()                                  {}

// fakeGitHub simulates the scale set service for any number of connections.
type fakeGitHub struct {
	mu        sync.Mutex
	nextID    int
	scaleSets map[string]int // name -> id
	deletedSS []int
	regs      map[string]int // runner name -> registration id
	busy      map[string]bool
	removed   []string
	jitCalls  int
	sessions  map[int]*fakeSession // scale set id -> latest session
	failConn  map[string]error     // connection id -> NewClient error
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{scaleSets: map[string]int{}, regs: map[string]int{}, busy: map[string]bool{}, sessions: map[int]*fakeSession{}, failConn: map[string]error{}}
}

func (g *fakeGitHub) NewClient(_ context.Context, conn core.Connection) (ScaleSetClient, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.failConn[conn.ID]; err != nil {
		return nil, err
	}
	return &fakeClient{g: g}, nil
}

func (g *fakeGitHub) setFail(connID string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failConn[connID] = err
}

func (g *fakeGitHub) session(ssid int) *fakeSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[ssid]
}

func (g *fakeGitHub) ssID(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.scaleSets[name]
}

func (g *fakeGitHub) registered(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.regs[name]
	return ok
}

func (g *fakeGitHub) setBusy(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy[name] = true
}

type fakeClient struct{ g *fakeGitHub }

func (c *fakeClient) GetRunnerGroupByName(_ context.Context, name string) (*scaleset.RunnerGroup, error) {
	return &scaleset.RunnerGroup{ID: 7, Name: name}, nil
}

func (c *fakeClient) GetRunnerScaleSet(_ context.Context, groupID int, name string) (*scaleset.RunnerScaleSet, error) {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	if id, ok := c.g.scaleSets[name]; ok {
		return &scaleset.RunnerScaleSet{ID: id, Name: name}, nil
	}
	return nil, nil
}

func (c *fakeClient) CreateRunnerScaleSet(_ context.Context, s *scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error) {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	c.g.nextID++
	c.g.scaleSets[s.Name] = c.g.nextID
	out := *s
	out.ID = c.g.nextID
	return &out, nil
}

func (c *fakeClient) DeleteRunnerScaleSet(_ context.Context, id int) error {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	for name, sid := range c.g.scaleSets {
		if sid == id {
			delete(c.g.scaleSets, name)
		}
	}
	c.g.deletedSS = append(c.g.deletedSS, id)
	return nil
}

func (c *fakeClient) GenerateJitRunnerConfig(_ context.Context, s *scaleset.RunnerScaleSetJitRunnerSetting, ssid int) (*scaleset.RunnerScaleSetJitRunnerConfig, error) {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	c.g.jitCalls++
	c.g.nextID++
	c.g.regs[s.Name] = c.g.nextID
	return &scaleset.RunnerScaleSetJitRunnerConfig{
		Runner:           &scaleset.RunnerReference{ID: c.g.nextID, Name: s.Name, RunnerScaleSetID: ssid},
		EncodedJITConfig: "jit-" + s.Name,
	}, nil
}

func (c *fakeClient) GetRunnerByName(_ context.Context, name string) (*scaleset.RunnerReference, error) {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	id, ok := c.g.regs[name]
	if !ok {
		return nil, nil
	}
	return &scaleset.RunnerReference{ID: id, Name: name}, nil
}

func (c *fakeClient) RemoveRunner(_ context.Context, id int64) error {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	for name, rid := range c.g.regs {
		if int64(rid) != id {
			continue
		}
		if c.g.busy[name] {
			// Mirrors how the scaleset client surfaces a JobStillRunningException.
			return fmt.Errorf("%w: %w: runner is running a job", scaleset.BadRequestError, scaleset.JobStillRunningError)
		}
		delete(c.g.regs, name)
		c.g.removed = append(c.g.removed, name)
		return nil
	}
	return fmt.Errorf("%w: %w", scaleset.NotFoundError, scaleset.RunnerNotFoundError)
}

func (c *fakeClient) OpenSession(_ context.Context, ssid int, owner string) (Session, error) {
	s := &fakeSession{
		id:       uuid.New(),
		msgs:     make(chan *scaleset.RunnerScaleSetMessage, 16),
		closed:   make(chan struct{}),
		maxSeen:  -1,
		acquired: map[int64]int{},
	}
	c.g.mu.Lock()
	c.g.sessions[ssid] = s
	c.g.mu.Unlock()
	return s, nil
}

type fakeSession struct {
	id   uuid.UUID
	msgs chan *scaleset.RunnerScaleSetMessage

	mu       sync.Mutex
	acked    []int
	acquired map[int64]int
	maxSeen  int
	closed   chan struct{}
	isClosed bool
}

func (s *fakeSession) GetMessage(ctx context.Context, last, maxCapacity int) (*scaleset.RunnerScaleSetMessage, error) {
	s.mu.Lock()
	s.maxSeen = maxCapacity
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m := <-s.msgs:
		return m, nil
	case <-time.After(20 * time.Millisecond):
		// Simulates the long-poll timeout so the listener reports maxCapacity regularly.
		return nil, nil
	}
}

func (s *fakeSession) DeleteMessage(_ context.Context, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = append(s.acked, id)
	return nil
}

func (s *fakeSession) AcquireJobs(_ context.Context, ids []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.acquired[id]++
	}
	return ids, nil
}

func (s *fakeSession) Session() scaleset.RunnerScaleSetSession {
	return scaleset.RunnerScaleSetSession{SessionID: s.id, Statistics: &scaleset.RunnerScaleSetStatistic{}}
}

func (s *fakeSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isClosed {
		s.isClosed = true
		close(s.closed)
	}
	return nil
}

func (s *fakeSession) maxCapacity() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxSeen
}

func (s *fakeSession) ackedIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.acked...)
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var errBoom = errors.New("boom")
