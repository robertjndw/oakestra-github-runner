// Package controller runs one scale set listener per pool and keeps Oakestra runner services in
// line with what GitHub asks for.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// Config configures a Manager. Zero durations take the defaults below.
type Config struct {
	Store    core.Store
	Oakestra core.Oakestra
	Clients  ClientFactory
	// DefaultImage is used for pools without their own image.
	DefaultImage string
	Logger       *slog.Logger

	ReconcileInterval    time.Duration // default 5s
	RunnerStartupTimeout time.Duration // default 5m
	IdleRunnerTimeout    time.Duration // default 10m
	PruneInterval        time.Duration // default 1h
	OrphanScanInterval   time.Duration // default 10m
	// RetryMin and RetryMax bound the exponential backoff for failing listeners and failing
	// runner creation. Defaults 1s and 2m.
	RetryMin time.Duration
	RetryMax time.Duration
	// SessionOwner is the owner name of the scale set message sessions. Default
	// "oakestra-github-runner".
	SessionOwner string
	// Now is the clock used for runner timeouts. Default time.Now.
	Now func() time.Time
}

func (c *Config) defaults() {
	if c.ReconcileInterval <= 0 {
		c.ReconcileInterval = 5 * time.Second
	}
	if c.RunnerStartupTimeout <= 0 {
		c.RunnerStartupTimeout = 5 * time.Minute
	}
	if c.IdleRunnerTimeout <= 0 {
		c.IdleRunnerTimeout = 10 * time.Minute
	}
	if c.PruneInterval <= 0 {
		c.PruneInterval = time.Hour
	}
	if c.OrphanScanInterval <= 0 {
		c.OrphanScanInterval = 10 * time.Minute
	}
	if c.RetryMin <= 0 {
		c.RetryMin = time.Second
	}
	if c.RetryMax < c.RetryMin {
		c.RetryMax = max(2*time.Minute, c.RetryMin)
	}
	if c.SessionOwner == "" {
		c.SessionOwner = "oakestra-github-runner"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
}

// Timeouts that keep shutdown prompt. The process waits for Run before closing the store, so
// everything Run waits for on the way out has to finish in a few seconds.
const (
	// createTimeout bounds a runner creation, which is deliberately not cancelled by
	// shutdown: cancelling after Oakestra created the service but before we read its id
	// would leave a service nobody knows about.
	createTimeout       = 15 * time.Second
	sessionCloseTimeout = 5 * time.Second
	// teardownShutdownGrace is how long shutdown lets running teardowns continue.
	teardownShutdownGrace = 5 * time.Second
	teardownTimeout       = 2 * time.Minute
	// retiredJobGrace is how long a runner that finished a job keeps its slot when Oakestra
	// reported the exit before GitHub sent JobCompleted. The message normally follows within
	// seconds; the limit only matters if it never comes.
	retiredJobGrace = 2 * time.Minute
)

// Manager implements core.PoolController.
type Manager struct {
	cfg    Config
	log    *slog.Logger
	reload chan struct{}

	mu sync.Mutex
	// books outlive listener restarts and hold everything known about a pool from the last
	// load, so a pool can be cleaned up after its row changed or disappeared.
	books map[string]*poolBook
	// workers is only touched by the Run goroutine.
	workers map[string]*worker

	// createMu makes the orphan scan see Oakestra and the store consistently: a runner exists
	// in Oakestra a moment before it is recorded in the store, and the scan must not mistake it
	// for an orphan. Creation takes it shared, the scan exclusively. Never acquire a book lock
	// while holding it exclusively.
	createMu       sync.RWMutex
	orphansChecked atomic.Bool

	// Teardowns run on bgCtx rather than Run's context so that a reload-triggered cleanup is
	// not cut short by an unrelated shutdown signal, until shutdown gives up on them.
	bg       sync.WaitGroup
	bgCtx    context.Context
	bgCancel context.CancelFunc
}

var _ core.PoolController = (*Manager)(nil)

// poolBook is the controller's state for one pool. mu serializes everything that reads and
// writes the pool's runners (Scale, reconcile, the bookkeeping part of teardown), so the
// read-modify-write cycles on store rows cannot interleave.
type poolBook struct {
	id string
	mu sync.Mutex

	pool core.Pool
	// conn is the pool's connection as of the last load. It survives the connection row so
	// cleanup still has credentials.
	conn core.Connection

	// The scale set this pool owns on GitHub and the identity it was created under. It is
	// seeded from the persisted Pool.ScaleSetID, so cleanup works for pools whose listener
	// never ran in this process.
	scaleSetID int
	ssConn     core.Connection
	ssName     string
	ssGroup    string

	active        bool // a listener is supposed to run
	inactiveSince time.Time
	listener      *listener.Listener

	// client talks to the connection in clientKey. Active pools get it from their worker,
	// inactive ones build it on demand for draining and cleanup.
	client    ScaleSetClient
	clientKey string

	// assigned is the last TotalAssignedJobs seen by the current listener. Nil messages carry
	// no statistics, so the reconcile loop converges on this cached value.
	assigned  int
	haveStats bool

	// doomed runners must go but deleting their service failed. They no longer count towards
	// the target and are retried by reconcile.
	doomed map[string]bool
	// retiredJobs are runners reconcile removed after they ran a job whose JobCompleted has not
	// arrived yet. The cached statistics still count that job, so the runner keeps its slot
	// until the message comes; otherwise every such job would get a replacement runner that
	// then idles until the idle timeout.
	retiredJobs map[string]time.Time

	createFailures int
	createRetryAt  time.Time

	tearingDown  bool
	teardownDone chan struct{}

	listenerErr string
	scaleErr    string
	published   bool
	pubErr      string
	pubSSID     int
}

func newBook(id string) *poolBook {
	return &poolBook{id: id, doomed: map[string]bool{}, retiredJobs: map[string]time.Time{}}
}

func (b *poolBook) target() int {
	if !b.haveStats {
		return 0
	}
	return max(0, min(b.pool.MaxRunners, b.assigned))
}

// resetStats forgets the statistics of a listener that is gone. A new listener's initial
// message brings fresh ones; scaling from old ones could start runners for jobs long finished.
func (b *poolBook) resetStats() {
	b.haveStats, b.assigned = false, 0
}

func (b *poolBook) identityDiffers(p core.Pool, conn core.Connection, haveConn bool) bool {
	if b.ssName != p.Name || b.ssGroup != normalizeGroup(p.RunnerGroup) || b.ssConn.ID != p.ConnectionID {
		return true
	}
	return haveConn && b.ssConn.ConfigURL != conn.ConfigURL
}

type worker struct {
	pool   core.Pool
	conn   core.Connection
	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Manager. Call Run to start it.
func New(cfg Config) *Manager {
	cfg.defaults()
	bgCtx, bgCancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:      cfg,
		log:      cfg.Logger,
		reload:   make(chan struct{}, 1),
		books:    map[string]*poolBook{},
		workers:  map[string]*worker{},
		bgCtx:    bgCtx,
		bgCancel: bgCancel,
	}
}

// Reload asks Run to re-read connections and pools. Bursts collapse into one reload.
func (m *Manager) Reload() {
	select {
	case m.reload <- struct{}{}:
	default:
	}
}

func (m *Manager) now() time.Time { return m.cfg.Now() }

func (m *Manager) orphansDone() bool { return m.orphansChecked.Load() }

// book returns the pool's book, creating an empty one if needed.
func (m *Manager) book(id string) *poolBook {
	b, _ := m.bookNew(id)
	return b
}

func (m *Manager) bookNew(id string) (*poolBook, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.books[id]
	if !ok {
		b = newBook(id)
		m.books[id] = b
	}
	return b, !ok
}

func (m *Manager) lookupBook(id string) *poolBook {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.books[id]
}

func (m *Manager) allBooks() []*poolBook {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*poolBook, 0, len(m.books))
	for _, b := range m.books {
		out = append(out, b)
	}
	return out
}

// connKey identifies a connection including its credentials, so a cached client is replaced
// when a secret or installation changes. It is a hash because it sits in memory next to
// things that get logged.
func connKey(c core.Connection) string {
	if c.ID == "" {
		return ""
	}
	h := sha256.New()
	for _, s := range []string{c.ID, string(c.AuthKind), c.ConfigURL, strconv.FormatInt(c.AppID, 10), c.AppClientID, strconv.FormatInt(c.InstallationID, 10), c.Secret} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// clientLocked returns a client for the connection the pool's scale set lives on, building one
// when needed. It returns nil when none can be built, for example while the connection waits
// for an App installation.
func (m *Manager) clientLocked(ctx context.Context, b *poolBook) ScaleSetClient {
	conn := b.ssConn
	if b.scaleSetID == 0 || conn.ID == "" {
		conn = b.conn
	}
	key := connKey(conn)
	if b.client != nil && b.clientKey == key {
		return b.client
	}
	if conn.ID == "" || conn.Status != core.ConnectionReady {
		return nil
	}
	c, err := m.cfg.Clients.NewClient(ctx, conn)
	if err != nil {
		m.log.Debug("creating GitHub client for cleanup failed", "pool", b.pool.Name, "error", err)
		return nil
	}
	b.client, b.clientKey = c, key
	return c
}

// Run starts the listeners and the reconcile loop and blocks until ctx is done. On return all
// listeners are stopped and their sessions closed. Runners are left running: their jobs are
// still in flight and the next start picks them up from the store.
func (m *Manager) Run(ctx context.Context) error {
	m.cleanOrphans(ctx)
	applyPending := false
	if err := m.apply(ctx); err != nil {
		m.log.Error("loading pools failed, retrying", "error", err)
		applyPending = true
	}

	reconcile := time.NewTicker(m.cfg.ReconcileInterval)
	defer reconcile.Stop()
	prune := time.NewTicker(m.cfg.PruneInterval)
	defer prune.Stop()
	orphans := time.NewTicker(m.cfg.OrphanScanInterval)
	defer orphans.Stop()

	for {
		select {
		case <-ctx.Done():
			m.shutdown()
			return nil
		case <-m.reload:
			if err := m.apply(ctx); err != nil {
				m.log.Error("reloading pools failed, retrying", "error", err)
				applyPending = true
			} else {
				applyPending = false
			}
		case <-reconcile.C:
			if applyPending {
				applyPending = m.apply(ctx) != nil
			}
			m.reconcile(ctx)
		case <-orphans.C:
			// Besides crashes, a service can be orphaned when creation times out after
			// Oakestra created it, so the scan is not only a startup task.
			m.cleanOrphans(ctx)
		case <-prune.C:
			if err := m.cfg.Store.Prune(ctx); err != nil {
				m.log.Warn("pruning history failed", "error", err)
			}
		}
	}
}

func (m *Manager) shutdown() {
	for _, w := range m.workers {
		w.cancel()
	}
	for id, w := range m.workers {
		<-w.done
		delete(m.workers, id)
	}
	done := make(chan struct{})
	go func() { m.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(teardownShutdownGrace):
		m.log.Warn("abandoning unfinished pool cleanup for shutdown")
		m.bgCancel()
		<-done
	}
	m.bgCancel()
}

type desiredPool struct {
	pool core.Pool
	conn core.Connection
}

// apply loads pools and connections and starts, stops, restarts and cleans up to match.
func (m *Manager) apply(ctx context.Context) error {
	conns, err := m.cfg.Store.ListConnections(ctx)
	if err != nil {
		return fmt.Errorf("listing connections: %w", err)
	}
	pools, err := m.cfg.Store.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	connByID := make(map[string]core.Connection, len(conns))
	for _, c := range conns {
		connByID[c.ID] = c
	}
	exists := make(map[string]bool, len(pools))
	now := m.now()

	for _, p := range pools {
		exists[p.ID] = true
		conn, haveConn := connByID[p.ConnectionID]
		d := desiredPool{pool: p, conn: conn}
		want := p.Enabled && haveConn && conn.Status == core.ConnectionReady

		b, isNew := m.bookNew(p.ID)
		b.mu.Lock()
		b.pool = p
		if haveConn {
			b.conn = conn
		}
		if isNew {
			b.scaleSetID = p.ScaleSetID
			b.ssConn, b.ssName, b.ssGroup = b.conn, p.Name, normalizeGroup(p.RunnerGroup)
			b.inactiveSince = now
		}
		if haveConn && b.ssConn.ID == conn.ID {
			// Same connection, possibly rotated credentials: cleanup must use the new ones.
			b.ssConn = conn
		}
		// An admin action is a good moment to retry creation right away, for example after
		// Oakestra credentials were fixed.
		b.createRetryAt = time.Time{}
		moved := b.scaleSetID != 0 && b.identityDiffers(p, conn, haveConn)
		b.mu.Unlock()

		w := m.workers[p.ID]
		switch {
		case moved:
			// The scale set is bound to the connection, name and runner group, so a new one
			// is needed. The old one and its runners go first, also when the pool is
			// disabled, or the old label would stay registered on GitHub.
			m.log.Info("pool identity changed, removing its old scale set", "pool", p.Name)
			if w != nil {
				m.stopWorker(p.ID, w)
			}
			m.teardown(b, false)
			if want {
				m.startWorker(ctx, d)
			}
		case w != nil && !want:
			m.log.Info("pool stopped, draining idle runners", "pool", p.Name)
			m.stopWorker(p.ID, w)
		case w != nil && credentialsChanged(w.conn, conn):
			m.log.Info("connection changed, restarting listener", "pool", p.Name)
			m.stopWorker(p.ID, w)
			m.startWorker(ctx, d)
		case w != nil:
			b.mu.Lock()
			if b.listener != nil {
				b.listener.SetMaxRunners(p.MaxRunners)
			}
			b.mu.Unlock()
			w.pool = p
		case want:
			m.startWorker(ctx, d)
		}
	}

	for _, b := range m.allBooks() {
		if exists[b.id] {
			continue
		}
		if w := m.workers[b.id]; w != nil {
			m.stopWorker(b.id, w)
		}
		b.mu.Lock()
		busy := b.tearingDown
		name := b.pool.Name
		b.mu.Unlock()
		if !busy {
			m.log.Info("pool deleted, removing its runners and scale set", "pool", name)
			m.teardown(b, true)
		}
	}
	return nil
}

func credentialsChanged(a, b core.Connection) bool {
	return a.AuthKind != b.AuthKind || a.Secret != b.Secret || a.AppID != b.AppID ||
		a.AppClientID != b.AppClientID || a.InstallationID != b.InstallationID
}

func normalizeGroup(g string) string {
	if g == "" {
		return scaleset.DefaultRunnerGroup
	}
	return g
}

func (m *Manager) startWorker(ctx context.Context, d desiredPool) {
	wctx, cancel := context.WithCancel(ctx)
	w := &worker{pool: d.pool, conn: d.conn, cancel: cancel, done: make(chan struct{})}
	m.workers[d.pool.ID] = w
	b := m.book(d.pool.ID)
	b.mu.Lock()
	b.pool = d.pool
	b.active = true
	b.resetStats()
	waitFor := b.teardownDone
	b.mu.Unlock()
	go m.runWorker(wctx, w, b, waitFor)
}

func (m *Manager) stopWorker(id string, w *worker) {
	w.cancel()
	<-w.done
	delete(m.workers, id)
	b := m.book(id)
	b.mu.Lock()
	b.active = false
	b.inactiveSince = m.now()
	b.listener = nil
	b.resetStats()
	b.mu.Unlock()
}

// teardown removes every runner of the pool, busy ones included, and deletes the scale set
// this addon created for it. The book is reset right away so a successor worker starts clean;
// the slow part runs in the background without holding the pool lock across network calls,
// and reconcile leaves the pool alone until it is done.
func (m *Manager) teardown(b *poolBook, forget bool) {
	b.mu.Lock()
	client := m.clientLocked(m.bgCtx, b)
	scaleSetID := b.scaleSetID
	b.scaleSetID = 0
	b.ssConn, b.ssName, b.ssGroup = b.conn, b.pool.Name, normalizeGroup(b.pool.RunnerGroup)
	b.resetStats()
	b.doomed = map[string]bool{}
	b.retiredJobs = map[string]time.Time{}
	b.listenerErr, b.scaleErr = "", ""
	b.tearingDown = true
	done := make(chan struct{})
	b.teardownDone = done
	b.mu.Unlock()

	m.bg.Add(1)
	go func() {
		defer m.bg.Done()
		defer close(done)
		tctx, cancel := context.WithTimeout(m.bgCtx, teardownTimeout)
		defer cancel()

		runners, err := m.cfg.Store.ListRunners(tctx, b.id)
		if err != nil {
			m.log.Warn("listing runners for teardown failed", "pool_id", b.id, "error", err)
		}
		for i := range runners {
			r := &runners[i]
			if client != nil {
				if _, err := removeRegistration(tctx, client, r.Name); err != nil {
					m.log.Debug("removing runner registration failed", "runner", r.Name, "error", err)
				}
			}
			if err := m.cfg.Oakestra.DeleteRunner(tctx, r.ServiceID); err != nil {
				// Reconcile deletes services of runners whose pool is gone; for a recreated
				// pool the startup timeout or terminal status catches it.
				m.log.Warn("deleting runner service failed", "runner", r.Name, "error", err)
				continue
			}
			b.mu.Lock()
			err := m.markRemovedLocked(tctx, b, r)
			b.mu.Unlock()
			if err != nil {
				m.log.Warn("recording runner removal failed", "runner", r.Name, "error", err)
			}
		}

		switch {
		case scaleSetID == 0:
		case client == nil:
			m.log.Warn("cannot delete scale set, no usable GitHub credentials", "pool_id", b.id, "scale_set_id", scaleSetID)
		default:
			if err := client.DeleteRunnerScaleSet(tctx, scaleSetID); err != nil && !errors.Is(err, scaleset.NotFoundError) {
				m.log.Warn("deleting scale set failed", "pool_id", b.id, "scale_set_id", scaleSetID, "error", err)
			} else {
				m.log.Info("scale set deleted", "pool_id", b.id, "scale_set_id", scaleSetID)
			}
		}

		b.mu.Lock()
		b.tearingDown = false
		if !forget {
			m.publishLocked(tctx, b)
		}
		active := b.active
		b.mu.Unlock()
		if forget && !active {
			m.mu.Lock()
			if m.books[b.id] == b {
				delete(m.books, b.id)
			}
			m.mu.Unlock()
		}
	}()
}

// runWorker keeps one pool's listener running, restarting it with backoff after failures.
func (m *Manager) runWorker(ctx context.Context, w *worker, b *poolBook, waitFor <-chan struct{}) {
	defer close(w.done)
	if waitFor != nil {
		select {
		case <-waitFor:
		case <-ctx.Done():
			return
		}
	}
	backoff := m.cfg.RetryMin
	for {
		started := time.Now()
		err := m.runListener(ctx, w, b)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("listener stopped unexpectedly")
		}
		m.log.Warn("pool listener failed", "pool", w.pool.Name, "error", err, "retry_in", backoff)
		b.mu.Lock()
		b.listenerErr = err.Error()
		b.resetStats()
		m.publishLocked(ctx, b)
		b.mu.Unlock()

		// A listener that ran for a while was healthy; start the backoff over.
		if time.Since(started) > m.cfg.RetryMax {
			backoff = m.cfg.RetryMin
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(2*backoff, m.cfg.RetryMax)
	}
}

func (m *Manager) runListener(ctx context.Context, w *worker, b *poolBook) error {
	client, err := m.cfg.Clients.NewClient(ctx, w.conn)
	if err != nil {
		return fmt.Errorf("creating GitHub client: %w", err)
	}
	b.mu.Lock()
	b.client, b.clientKey = client, connKey(w.conn)
	pool := b.pool
	pool.ScaleSetID = b.scaleSetID
	b.mu.Unlock()

	scaleSetID, err := ensureScaleSet(ctx, client, pool)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.scaleSetID = scaleSetID
	b.ssConn, b.ssName, b.ssGroup = w.conn, pool.Name, normalizeGroup(pool.RunnerGroup)
	// Persist the id before anything else: it is the only proof that this scale set is ours
	// after a restart.
	m.publishLocked(ctx, b)
	b.mu.Unlock()

	sess, err := client.OpenSession(ctx, scaleSetID, m.cfg.SessionOwner)
	if err != nil {
		return fmt.Errorf("opening message session: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCloseTimeout)
		defer cancel()
		if err := sess.Close(cctx); err != nil {
			m.log.Debug("closing message session failed", "pool", pool.Name, "error", err)
		}
	}()

	l, err := listener.New(sess, listener.Config{
		ScaleSetID: scaleSetID,
		MaxRunners: pool.MaxRunners,
		Logger:     m.log.With("pool", pool.Name),
	})
	if err != nil {
		return fmt.Errorf("creating listener: %w", err)
	}
	b.mu.Lock()
	b.listener = l
	// MaxRunners may have changed while the session was being opened.
	l.SetMaxRunners(b.pool.MaxRunners)
	b.listenerErr = ""
	m.publishLocked(ctx, b)
	b.mu.Unlock()
	m.log.Info("pool listener started", "pool", pool.Name, "scale_set_id", scaleSetID)

	defer func() {
		b.mu.Lock()
		if b.listener == l {
			b.listener = nil
		}
		b.mu.Unlock()
	}()
	return l.Run(ctx, &scaler{m: m, b: b, session: sess})
}

// ensureScaleSet returns the id of the pool's scale set, creating it when needed.
// pool.ScaleSetID is the id this addon recorded for the pool, or 0.
//
// A scale set found by name is adopted only when its id is the recorded one. Anything else
// with that name belongs to someone else (ARC, another installation, another pool on a
// different connection) and is refused, because teardown would later delete it. The scale set
// API has no field to mark ownership in: the only free-form data is labels, and an extra label
// would change which jobs match (and needs a feature flag on GHES).
func ensureScaleSet(ctx context.Context, client ScaleSetClient, pool core.Pool) (int, error) {
	group := normalizeGroup(pool.RunnerGroup)
	// The default group always has id 1; looking it up by name would be an extra call that
	// the reference autoscaler also skips.
	groupID := 1
	if group != scaleset.DefaultRunnerGroup {
		g, err := client.GetRunnerGroupByName(ctx, group)
		if err != nil {
			return 0, fmt.Errorf("looking up runner group %q: %w", group, err)
		}
		groupID = g.ID
	}
	existing, err := client.GetRunnerScaleSet(ctx, groupID, pool.Name)
	if err != nil {
		return 0, fmt.Errorf("looking up scale set %q: %w", pool.Name, err)
	}
	if existing != nil {
		if pool.ScaleSetID != 0 && existing.ID == pool.ScaleSetID {
			return existing.ID, nil
		}
		return 0, fmt.Errorf("a runner scale set named %q already exists in runner group %q and was not created by this addon for this pool; delete it on GitHub or give the pool another name", pool.Name, group)
	}
	created, err := client.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          pool.Name,
		RunnerGroupID: groupID,
		Labels:        []scaleset.Label{{Name: pool.Name}},
		// Runners are single-use containers from a pinned image; a self-update would only
		// download a new runner into a container that is thrown away after one job.
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	})
	if err != nil {
		return 0, fmt.Errorf("creating scale set %q: %w", pool.Name, err)
	}
	return created.ID, nil
}

// maxLastError bounds what we store as a pool's error. Scale set client errors include the
// upstream response body, and the dashboard shows this string.
const maxLastError = 500

// publishLocked writes the pool's scale set id and error to the store when they changed.
// Listener errors win over runner creation errors because they explain why nothing happens.
func (m *Manager) publishLocked(ctx context.Context, b *poolBook) {
	msg := b.listenerErr
	if msg == "" {
		msg = b.scaleErr
	}
	if r := []rune(msg); len(r) > maxLastError {
		msg = string(r[:maxLastError]) + "..."
	}
	if b.published && msg == b.pubErr && b.scaleSetID == b.pubSSID {
		return
	}
	err := m.cfg.Store.SetPoolRuntime(ctx, b.id, b.scaleSetID, msg)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		m.log.Warn("recording pool status failed", "pool_id", b.id, "error", err)
		return
	}
	b.published, b.pubErr, b.pubSSID = true, msg, b.scaleSetID
}

func (m *Manager) setScaleErrLocked(ctx context.Context, b *poolBook, msg string) {
	b.scaleErr = msg
	m.publishLocked(ctx, b)
}
