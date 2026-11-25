package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/actions/scaleset"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// scaler is the listener.Scaler of one pool.
//
// Error policy: Scale returns an error only when redelivering the message can fix it, that is
// when acquiring jobs or writing job and runner bookkeeping to the store failed. Everything
// about creating or deleting runners (GitHub JIT configs, Oakestra being down or unconfigured,
// no capacity) is recorded on the pool and retried by reconcile, which converges on the cached
// target. Failing the message for those would stall the queue behind a problem that a retry
// of the same message cannot solve, and JobCompleted messages behind it would not be recorded.
type scaler struct {
	m       *Manager
	b       *poolBook
	session Session
}

func (s *scaler) Scale(ctx context.Context, msg *scaleset.RunnerScaleSetMessage) error {
	if msg == nil {
		return nil
	}
	m, b := s.m, s.b

	if len(msg.JobAvailableMessages) > 0 {
		ids := make([]int64, 0, len(msg.JobAvailableMessages))
		for _, j := range msg.JobAvailableMessages {
			ids = append(ids, j.RunnerRequestID)
		}
		// Acquiring an already acquired job is a no-op, so a redelivered message is safe.
		if _, err := s.session.AcquireJobs(ctx, ids); err != nil {
			return fmt.Errorf("acquiring jobs: %w", err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, j := range msg.JobStartedMessages {
		if err := m.jobStartedLocked(ctx, b, j); err != nil {
			return err
		}
	}
	for _, j := range msg.JobCompletedMessages {
		if err := m.jobCompletedLocked(ctx, b, j); err != nil {
			return err
		}
	}
	if msg.Statistics != nil {
		b.assigned = msg.Statistics.TotalAssignedJobs
		b.haveStats = true
	}
	m.scaleUpLocked(ctx, b)
	return nil
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func jobFromMessage(poolID string, base *scaleset.JobMessageBase) core.Job {
	repo := base.RepositoryName
	if base.OwnerName != "" {
		repo = base.OwnerName + "/" + base.RepositoryName
	}
	return core.Job{
		PoolID:        poolID,
		RequestID:     base.RunnerRequestID,
		Repository:    repo,
		WorkflowRef:   base.JobWorkflowRef,
		JobName:       base.JobDisplayName,
		WorkflowRunID: base.WorkflowRunID,
		EventName:     base.EventName,
		QueuedAt:      timePtr(base.QueueTime),
	}
}

// lookupRunner returns the runner or ok=false when it is unknown. Only store failures are
// errors.
func (m *Manager) lookupRunner(ctx context.Context, name string) (core.Runner, bool, error) {
	if name == "" {
		return core.Runner{}, false, nil
	}
	r, err := m.cfg.Store.GetRunner(ctx, name)
	if errors.Is(err, core.ErrNotFound) {
		return core.Runner{}, false, nil
	}
	if err != nil {
		return core.Runner{}, false, fmt.Errorf("looking up runner %s: %w", name, err)
	}
	return r, true, nil
}

func (m *Manager) jobStartedLocked(ctx context.Context, b *poolBook, j *scaleset.JobStarted) error {
	r, found, err := m.lookupRunner(ctx, j.RunnerName)
	if err != nil {
		return err
	}
	job := jobFromMessage(b.id, &j.JobMessageBase)
	job.RunnerName = j.RunnerName
	job.StartedAt = timePtr(j.RunnerAssignTime)
	if job.StartedAt == nil {
		now := m.now()
		job.StartedAt = &now
	}
	if found {
		job.ClusterID, job.WorkerID = r.ClusterID, r.WorkerID
	}
	if err := m.cfg.Store.UpsertJob(ctx, &job); err != nil {
		return fmt.Errorf("recording started job %d: %w", j.RunnerRequestID, err)
	}
	m.log.Info("job started", "pool", b.pool.Name, "runner", j.RunnerName, "request_id", j.RunnerRequestID, "repository", job.Repository, "job", job.JobName)
	if !found || r.State == core.RunnerRemoved || r.PoolID != b.id {
		return nil
	}
	if r.State == core.RunnerBusy && r.JobRequestID == j.RunnerRequestID {
		return nil
	}
	r.State = core.RunnerBusy
	r.JobRequestID = j.RunnerRequestID
	if r.RunningAt == nil {
		now := m.now()
		r.RunningAt = &now
	}
	if err := m.cfg.Store.UpdateRunner(ctx, &r); err != nil {
		return fmt.Errorf("marking runner %s busy: %w", r.Name, err)
	}
	return nil
}

func (m *Manager) jobCompletedLocked(ctx context.Context, b *poolBook, j *scaleset.JobCompleted) error {
	r, found, err := m.lookupRunner(ctx, j.RunnerName)
	if err != nil {
		return err
	}
	job := jobFromMessage(b.id, &j.JobMessageBase)
	job.RunnerName = j.RunnerName
	// GitHub does not document the casing of result strings; lower case keeps the history filter
	// matching whatever it sends.
	job.Result = strings.ToLower(j.Result)
	job.StartedAt = timePtr(j.RunnerAssignTime)
	job.FinishedAt = timePtr(j.FinishTime)
	if job.FinishedAt == nil {
		now := m.now()
		job.FinishedAt = &now
	}
	if found {
		job.ClusterID, job.WorkerID = r.ClusterID, r.WorkerID
	}
	if err := m.cfg.Store.UpsertJob(ctx, &job); err != nil {
		return fmt.Errorf("recording completed job %d: %w", j.RunnerRequestID, err)
	}
	m.log.Info("job completed", "pool", b.pool.Name, "runner", j.RunnerName, "request_id", j.RunnerRequestID, "result", j.Result)
	delete(b.retiredJobs, j.RunnerName)
	if !found || r.State == core.RunnerRemoved || r.PoolID != b.id {
		return nil
	}
	if r.JobRequestID == 0 {
		r.JobRequestID = j.RunnerRequestID
	}
	if err := m.cfg.Oakestra.DeleteRunner(ctx, r.ServiceID); err != nil {
		m.log.Warn("deleting finished runner failed, will retry", "runner", r.Name, "error", err)
		b.doomed[r.Name] = true
		return nil
	}
	if err := m.markRemovedLocked(ctx, b, &r); err != nil {
		// The service is gone already; redelivery deletes it again, which is a no-op, and
		// retries this write.
		return err
	}
	return nil
}

func (m *Manager) markRemovedLocked(ctx context.Context, b *poolBook, r *core.Runner) error {
	now := m.now()
	r.State = core.RunnerRemoved
	r.RemovedAt = &now
	if err := m.cfg.Store.UpdateRunner(ctx, r); err != nil {
		return fmt.Errorf("marking runner %s removed: %w", r.Name, err)
	}
	delete(b.doomed, r.Name)
	return nil
}

// liveCount counts the runners that serve the pool's target, including finished runners whose
// job the cached statistics still count.
func liveCount(b *poolBook, runners []core.Runner, now time.Time) int {
	n := 0
	for _, r := range runners {
		if r.State != core.RunnerRemoved && !b.doomed[r.Name] {
			n++
		}
	}
	return n + b.retiredJobCount(now)
}

func (b *poolBook) retiredJobCount(now time.Time) int {
	for name, at := range b.retiredJobs {
		if now.Sub(at) > retiredJobGrace {
			delete(b.retiredJobs, name)
		}
	}
	return len(b.retiredJobs)
}

func (m *Manager) retryDelay(failures int) time.Duration {
	d := m.cfg.RetryMin
	for i := 1; i < failures && d < m.cfg.RetryMax; i++ {
		d *= 2
	}
	return min(d, m.cfg.RetryMax)
}

// scaleUpLocked creates runners until the pool has as many as its target. Failures are
// recorded on the pool and back off; they are never returned (see scaler).
func (m *Manager) scaleUpLocked(ctx context.Context, b *poolBook) {
	if !b.active || !b.haveStats || b.client == nil || b.scaleSetID == 0 {
		return
	}
	now := m.now()
	if now.Before(b.createRetryAt) {
		return
	}
	runners, err := m.cfg.Store.ListRunners(ctx, b.id)
	if err != nil {
		m.log.Warn("listing runners failed", "pool", b.pool.Name, "error", err)
		return
	}
	need := b.target() - liveCount(b, runners, now)
	for range need {
		if ctx.Err() != nil {
			return
		}
		if err := m.createRunnerLocked(ctx, b); err != nil {
			if ctx.Err() != nil {
				return
			}
			b.createFailures++
			delay := m.retryDelay(b.createFailures)
			b.createRetryAt = now.Add(delay)
			m.log.Warn("creating runner failed", "pool", b.pool.Name, "error", err, "retry_in", delay)
			m.setScaleErrLocked(ctx, b, err.Error())
			return
		}
	}
	b.createFailures = 0
	b.createRetryAt = time.Time{}
	if b.scaleErr != "" {
		m.setScaleErrLocked(ctx, b, "")
	}
}

// newRunnerName returns "r" plus 12 hex characters. It is both the Oakestra microservice name
// (^[a-zA-Z0-9]{1,32}$) and the GitHub runner name, which is how job messages find the service.
func newRunnerName() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "r" + hex.EncodeToString(buf[:]), nil
}

func (m *Manager) createRunnerLocked(ctx context.Context, b *poolBook) error {
	image := b.pool.Image
	if image == "" {
		image = m.cfg.DefaultImage
	}
	if image == "" {
		return errors.New("no runner image configured")
	}
	name, err := newRunnerName()
	if err != nil {
		return err
	}
	jit, err := b.client.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name}, b.scaleSetID)
	if err != nil {
		return fmt.Errorf("generating runner config on GitHub: %w", err)
	}
	var ghRunnerID int
	if jit.Runner != nil {
		ghRunnerID = jit.Runner.ID
	}

	// From here on the work is not cancelled with ctx: a cancellation after Oakestra created
	// the service but before we recorded it would leave a service nobody knows about.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	defer cancel()
	m.createMu.RLock()
	defer m.createMu.RUnlock()
	serviceID, err := m.cfg.Oakestra.CreateRunner(cctx, core.RunnerSpec{
		Name:      name,
		Image:     image,
		VCPUs:     b.pool.VCPUs,
		MemoryMB:  b.pool.MemoryMB,
		Cluster:   b.pool.Cluster,
		Node:      b.pool.Node,
		JITConfig: jit.EncodedJITConfig,
	})
	if err != nil {
		// The JIT config registered a runner on GitHub that will never come online.
		m.dropRegistration(cctx, b.client, name, ghRunnerID)
		return fmt.Errorf("deploying runner on Oakestra: %w", err)
	}
	r := &core.Runner{
		Name:      name,
		PoolID:    b.id,
		ServiceID: serviceID,
		State:     core.RunnerPending,
		CreatedAt: m.now(),
	}
	if err := m.cfg.Store.CreateRunner(cctx, r); err != nil {
		if derr := m.cfg.Oakestra.DeleteRunner(cctx, serviceID); derr != nil {
			m.log.Warn("deleting unrecorded runner service failed", "runner", name, "error", derr)
		}
		m.dropRegistration(cctx, b.client, name, ghRunnerID)
		return fmt.Errorf("recording runner: %w", err)
	}
	m.log.Info("runner created", "pool", b.pool.Name, "runner", name, "service_id", serviceID)
	return nil
}

func (m *Manager) dropRegistration(ctx context.Context, client ScaleSetClient, name string, id int) {
	var err error
	if id != 0 {
		_, err = removeRegistrationByID(ctx, client, id)
	} else {
		_, err = removeRegistration(ctx, client, name)
	}
	if err != nil {
		m.log.Debug("removing runner registration failed", "runner", name, "error", err)
	}
}

// removeRegistration deletes the runner's GitHub registration. busy is true when GitHub refused
// because the runner is running a job. A runner GitHub does not know is not an error.
func removeRegistration(ctx context.Context, client ScaleSetClient, name string) (busy bool, err error) {
	ref, err := client.GetRunnerByName(ctx, name)
	if err != nil {
		return false, fmt.Errorf("looking up runner %s on GitHub: %w", name, err)
	}
	if ref == nil {
		return false, nil
	}
	return removeRegistrationByID(ctx, client, ref.ID)
}

func removeRegistrationByID(ctx context.Context, client ScaleSetClient, id int) (busy bool, err error) {
	err = client.RemoveRunner(ctx, int64(id))
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, scaleset.JobStillRunningError):
		return true, nil
	case errors.Is(err, scaleset.RunnerNotFoundError), errors.Is(err, scaleset.NotFoundError):
		return false, nil
	default:
		return false, fmt.Errorf("removing runner %d on GitHub: %w", id, err)
	}
}
