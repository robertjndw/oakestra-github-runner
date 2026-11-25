package controller

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// Oakestra instance statuses the controller acts on.
const (
	statusRunning   = "RUNNING"
	statusCompleted = "COMPLETED"
	statusDead      = "DEAD"
	statusFailed    = "FAILED"
)

// reconcile checks every live runner against Oakestra, removes finished, crashed, stuck and
// surplus runners, and creates missing ones.
func (m *Manager) reconcile(ctx context.Context) {
	if !m.orphansChecked.Load() {
		m.cleanOrphans(ctx)
	}
	pools, err := m.cfg.Store.ListPools(ctx)
	if err != nil {
		m.log.Warn("reconcile: listing pools failed", "error", err)
		return
	}
	runners, err := m.cfg.Store.ListRunners(ctx, "")
	if err != nil {
		m.log.Warn("reconcile: listing runners failed", "error", err)
		return
	}
	exists := make(map[string]bool, len(pools))
	for _, p := range pools {
		exists[p.ID] = true
	}
	seen := map[string]bool{}
	for _, r := range runners {
		if seen[r.PoolID] {
			continue
		}
		seen[r.PoolID] = true
		if !exists[r.PoolID] {
			m.removeUnowned(ctx, r.PoolID)
			continue
		}
		// A pool without a book has not been loaded by apply yet; the next pass has it.
		if b := m.lookupBook(r.PoolID); b != nil {
			m.reconcilePool(ctx, b)
		}
	}
	for _, b := range m.allBooks() {
		if seen[b.id] {
			continue
		}
		b.mu.Lock()
		m.scaleUpLocked(ctx, b)
		b.mu.Unlock()
	}
}

// removeUnowned deletes the services of runners whose pool no longer exists. Their scale set
// is gone or going, so there is no registration worth protecting.
func (m *Manager) removeUnowned(ctx context.Context, poolID string) {
	b := m.lookupBook(poolID)
	if b == nil {
		b = newBook(poolID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tearingDown {
		return
	}
	runners, err := m.cfg.Store.ListRunners(ctx, poolID)
	if err != nil {
		return
	}
	for i := range runners {
		r := &runners[i]
		if err := m.cfg.Oakestra.DeleteRunner(ctx, r.ServiceID); err != nil {
			m.log.Debug("deleting runner of deleted pool failed", "runner", r.Name, "error", err)
			continue
		}
		if err := m.markRemovedLocked(ctx, b, r); err != nil {
			m.log.Warn("recording runner removal failed", "runner", r.Name, "error", err)
		}
	}
}

func (m *Manager) reconcilePool(ctx context.Context, b *poolBook) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tearingDown {
		return
	}
	runners, err := m.cfg.Store.ListRunners(ctx, b.id)
	if err != nil {
		m.log.Warn("reconcile: listing runners failed", "pool_id", b.id, "error", err)
		return
	}
	now := m.now()
	live := make([]core.Runner, 0, len(runners))
	for i := range runners {
		r := runners[i]
		if b.doomed[r.Name] {
			if err := m.cfg.Oakestra.DeleteRunner(ctx, r.ServiceID); err != nil {
				m.log.Debug("deleting runner failed, will retry", "runner", r.Name, "error", err)
				continue
			}
			if err := m.markRemovedLocked(ctx, b, &r); err != nil {
				m.log.Warn("recording runner removal failed", "runner", r.Name, "error", err)
			}
			continue
		}

		inst, err := m.cfg.Oakestra.GetRunner(ctx, r.ServiceID)
		if errors.Is(err, core.ErrNotFound) {
			m.retireLocked(ctx, b, &r, "service is gone", false, r.State == core.RunnerBusy)
			continue
		}
		if errors.Is(err, core.ErrNoCredentials) {
			// Nothing on Oakestra can be checked or changed; creation would fail the same way.
			return
		}
		if err != nil {
			m.log.Debug("reading runner status failed", "runner", r.Name, "error", err)
			live = append(live, r)
			continue
		}

		changed := false
		if inst.Status != r.OakestraStatus || inst.ClusterID != r.ClusterID || inst.WorkerID != r.WorkerID {
			r.OakestraStatus, r.ClusterID, r.WorkerID = inst.Status, inst.ClusterID, inst.WorkerID
			changed = true
		}
		if inst.Status == statusRunning {
			if r.RunningAt == nil {
				t := now
				r.RunningAt = &t
				changed = true
			}
			if r.State == core.RunnerPending {
				r.State = core.RunnerIdle
				changed = true
			}
		}

		switch {
		case inst.Status == statusCompleted || inst.Status == statusDead || inst.Status == statusFailed:
			// Oakestra redeploys DEAD and FAILED instances forever, so a finished or crashed
			// runner would come back unless its service is deleted. A runner that exits
			// normally has run its one job even if JobStarted has not been processed yet.
			ranJob := r.State == core.RunnerBusy || inst.Status == statusCompleted
			m.retireLocked(ctx, b, &r, "instance is "+inst.Status, true, ranJob)
			continue
		case r.State == core.RunnerPending && now.Sub(r.CreatedAt) > m.cfg.RunnerStartupTimeout:
			m.retireLocked(ctx, b, &r, "did not start within "+m.cfg.RunnerStartupTimeout.String()+" (status "+inst.Status+")", true, false)
			continue
		}
		if changed {
			if err := m.cfg.Store.UpdateRunner(ctx, &r); err != nil {
				m.log.Warn("recording runner status failed", "runner", r.Name, "error", err)
			}
		}
		live = append(live, r)
	}

	m.scaleDownLocked(ctx, b, live, now)
	m.scaleUpLocked(ctx, b)
}

// retireLocked removes a runner that is finished or broken. A runner that never took a job
// still has its registration on GitHub, which would linger as an offline runner in the scale
// set, so that is removed too (best effort: the runner cannot come back either way). ranJob
// marks a runner whose job may still be counted by the cached statistics.
func (m *Manager) retireLocked(ctx context.Context, b *poolBook, r *core.Runner, reason string, deleteService, ranJob bool) {
	if r.JobRequestID == 0 {
		if client := m.clientLocked(ctx, b); client != nil {
			if _, err := removeRegistration(ctx, client, r.Name); err != nil {
				m.log.Debug("removing runner registration failed", "runner", r.Name, "error", err)
			}
		}
	}
	if deleteService {
		if err := m.cfg.Oakestra.DeleteRunner(ctx, r.ServiceID); err != nil {
			m.log.Warn("deleting runner service failed, will retry", "runner", r.Name, "reason", reason, "error", err)
			return
		}
	}
	if err := m.markRemovedLocked(ctx, b, r); err != nil {
		m.log.Warn("recording runner removal failed", "runner", r.Name, "error", err)
		return
	}
	if ranJob && b.haveStats {
		b.retiredJobs[r.Name] = m.now()
	}
	m.log.Info("runner removed", "pool", b.pool.Name, "runner", r.Name, "reason", reason)
}

// scaleDownLocked removes surplus runners: idle ones past the idle timeout while the pool has
// more runners than its target, and for a stopped pool every runner that is not busy.
func (m *Manager) scaleDownLocked(ctx context.Context, b *poolBook, live []core.Runner, now time.Time) {
	client := m.clientLocked(ctx, b)
	target, idleFor, drainPending := 0, m.cfg.IdleRunnerTimeout, false
	if b.active {
		if !b.haveStats || client == nil {
			return
		}
		target = b.target()
	} else {
		idleFor, drainPending = 0, true
	}
	surplus := len(live) + b.retiredJobCount(now) - target
	if surplus <= 0 {
		return
	}
	var cands []core.Runner
	for _, r := range live {
		switch {
		case r.State == core.RunnerIdle && r.RunningAt != nil && now.Sub(*r.RunningAt) >= idleFor:
			cands = append(cands, r)
		case drainPending && r.State == core.RunnerPending:
			cands = append(cands, r)
		}
	}
	since := func(r core.Runner) time.Time {
		if r.RunningAt != nil {
			return *r.RunningAt
		}
		return r.CreatedAt
	}
	sort.Slice(cands, func(i, j int) bool { return since(cands[i]).Before(since(cands[j])) })
	if client == nil {
		m.drainWithoutGitHubLocked(ctx, b, cands, now)
		return
	}
	for i := range cands {
		if surplus == 0 {
			return
		}
		if m.safeRemoveLocked(ctx, b, client, &cands[i]) {
			surplus--
		}
	}
}

// drainWithoutGitHubLocked handles a stopped pool for which no GitHub client can be built
// (connection deleted, waiting for an App installation, credentials rejected). Its runners
// cannot be removed safely through GitHub, but leaving them up leaks them forever.
//
// With the listener stopped there is no message session, so nobody acquires jobs for the
// scale set and GitHub has nothing to assign to these runners. The exception is a job that
// was acquired just before the listener stopped and is assigned afterwards; that happens
// within seconds, so after the idle timeout an idle runner is very unlikely to be about to
// get one. The residual risk is killing such a job, and the GitHub registration stays behind
// as an offline runner until GitHub cleans it up.
func (m *Manager) drainWithoutGitHubLocked(ctx context.Context, b *poolBook, cands []core.Runner, now time.Time) {
	if now.Sub(b.inactiveSince) < m.cfg.IdleRunnerTimeout {
		return
	}
	for i := range cands {
		r := &cands[i]
		if err := m.cfg.Oakestra.DeleteRunner(ctx, r.ServiceID); err != nil {
			m.log.Warn("deleting runner of stopped pool failed", "runner", r.Name, "error", err)
			continue
		}
		if err := m.markRemovedLocked(ctx, b, r); err != nil {
			m.log.Warn("recording runner removal failed", "runner", r.Name, "error", err)
			continue
		}
		m.log.Info("runner of stopped pool removed without GitHub access", "pool", b.pool.Name, "runner", r.Name)
	}
}

// safeRemoveLocked removes an idle runner without racing GitHub's job assignment. The
// registration goes first: once it is gone GitHub cannot hand the runner a job, and if GitHub
// already did, it refuses with JobStillRunning and the runner stays. Deleting the service first
// could kill a job that was assigned a moment earlier.
func (m *Manager) safeRemoveLocked(ctx context.Context, b *poolBook, client ScaleSetClient, r *core.Runner) bool {
	busy, err := removeRegistration(ctx, client, r.Name)
	if busy {
		m.log.Info("runner picked up a job while scaling down, keeping it", "runner", r.Name)
		return false
	}
	if err != nil {
		m.log.Warn("removing idle runner registration failed", "runner", r.Name, "error", err)
		return false
	}
	if err := m.cfg.Oakestra.DeleteRunner(ctx, r.ServiceID); err != nil {
		m.log.Warn("deleting idle runner failed, will retry", "runner", r.Name, "error", err)
		b.doomed[r.Name] = true
		return true
	}
	if err := m.markRemovedLocked(ctx, b, r); err != nil {
		m.log.Warn("recording runner removal failed", "runner", r.Name, "error", err)
	}
	m.log.Info("idle runner removed", "pool", b.pool.Name, "runner", r.Name)
	return true
}

// cleanOrphans deletes runner services the store does not know as live, left behind by a crash
// or a timeout between creating a service and recording it, and retires store runners whose
// service is gone. Reconcile calls it until it succeeds once, Run also periodically.
func (m *Manager) cleanOrphans(ctx context.Context) {
	m.createMu.Lock()
	services, err := m.cfg.Oakestra.ListRunners(ctx)
	if err != nil {
		m.createMu.Unlock()
		if errors.Is(err, core.ErrNoCredentials) {
			m.log.Debug("orphan scan postponed, Oakestra credentials not configured")
		} else {
			m.log.Warn("orphan scan failed, will retry", "error", err)
		}
		return
	}
	runners, err := m.cfg.Store.ListRunners(ctx, "")
	if err != nil {
		m.createMu.Unlock()
		m.log.Warn("orphan scan failed, will retry", "error", err)
		return
	}
	known := make(map[string]bool, len(runners))
	for _, r := range runners {
		known[r.ServiceID] = true
	}
	present := make(map[string]bool, len(services))
	failed := false
	for _, s := range services {
		present[s.ServiceID] = true
		if known[s.ServiceID] {
			continue
		}
		if err := m.cfg.Oakestra.DeleteRunner(ctx, s.ServiceID); err != nil {
			m.log.Warn("deleting orphaned runner service failed", "service_id", s.ServiceID, "name", s.Name, "error", err)
			failed = true
			continue
		}
		m.log.Info("deleted orphaned runner service", "service_id", s.ServiceID, "name", s.Name)
	}
	m.createMu.Unlock()

	// Book locks are taken only after createMu is released; Scale holds a book lock while it
	// takes createMu shared.
	for i := range runners {
		r := &runners[i]
		if present[r.ServiceID] {
			continue
		}
		// At startup apply has not loaded the pools yet. A throwaway book keeps the scan from
		// creating empty books that apply would mistake for already loaded ones.
		b := m.lookupBook(r.PoolID)
		if b == nil {
			b = newBook(r.PoolID)
		}
		b.mu.Lock()
		// The runner may have been handled since the snapshot.
		if cur, err := m.cfg.Store.GetRunner(ctx, r.Name); err == nil && cur.State != core.RunnerRemoved && cur.ServiceID == r.ServiceID {
			m.retireLocked(ctx, b, &cur, "service is missing", false, false)
		}
		b.mu.Unlock()
	}
	if !failed {
		m.orphansChecked.Store(true)
	}
}
