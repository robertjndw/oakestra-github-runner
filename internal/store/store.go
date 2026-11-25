// Package store implements core.Store on SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"modernc.org/sqlite"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

var _ core.Store = (*Store)(nil)

const (
	defaultRetention = 30 * 24 * time.Hour
	manifestTTL      = time.Hour
	defaultJobLimit  = 50
	maxJobLimit      = 200
	keyCheckLabel    = "meta:key_check"
	keyCheckPlain    = "oakestra-github-runner"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is a core.Store backed by a SQLite file.
type Store struct {
	db        *sql.DB
	seal      *sealer
	retention time.Duration
	now       func() time.Time
}

// Option customises Open.
type Option func(*Store)

// WithRetention sets how long removed runners and finished jobs are kept by Prune.
func WithRetention(d time.Duration) Option {
	return func(s *Store) { s.retention = d }
}

// WithClock replaces time.Now, for tests that need to age rows.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// Open opens (creating if needed) the database at path, applies pending migrations and checks
// that key is the one the data was encrypted with.
func Open(path string, key []byte, opts ...Option) (*Store, error) {
	sl, err := newSealer(key)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// SQLite has a single writer. One connection serialises all access inside the process, which
	// avoids SQLITE_BUSY between our own goroutines (a deferred transaction that upgrades to a
	// write can fail immediately with it, regardless of the busy timeout). The cost is that
	// readers wait for writers, which is fine for this workload. Never run a query on s.db while
	// a transaction is open: it would wait for a second connection that never comes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, seal: sl, retention: defaultRetention, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	ctx := context.Background()
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.checkKey(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current > len(names) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, len(names))
	}
	for i := current; i < len(names); i++ {
		body, err := migrationFS.ReadFile("migrations/" + names[i])
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", names[i], err)
		}
	}
	if current != len(names) {
		// PRAGMA does not take bind parameters.
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(len(names))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// checkKey stores an encrypted marker on first use and verifies it afterwards, so a wrong key is
// reported at startup instead of as scattered per-record decrypt errors.
func (s *Store) checkKey(ctx context.Context) error {
	var blob []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'key_check'").Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		sealed, err := s.seal.seal(keyCheckPlain, keyCheckLabel)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('key_check', ?)", sealed)
		return err
	}
	if err != nil {
		return err
	}
	if _, err := s.seal.open(blob, keyCheckLabel); err != nil {
		return fmt.Errorf("encryption key does not match the database: %w", err)
	}
	return nil
}

// Time helpers. Times are stored as UTC unix nanoseconds so they round-trip exactly.

func (s *Store) clock() time.Time { return s.now().UTC().Round(0) }

func toNS(t time.Time) int64 { return t.UnixNano() }

func fromNS(n int64) time.Time { return time.Unix(0, n).UTC() }

func nullNS(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return toNS(*t)
}

func ptrTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := fromNS(n.Int64)
	return &t
}

func newID(id *string) {
	if *id == "" {
		*id = uuid.NewString()
	}
}

const (
	sqliteConstraint = 19 // SQLITE_CONSTRAINT, primary result code
)

// mapErr turns constraint violations into core.ErrConflict and leaves everything else alone.
func mapErr(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqliteConstraint {
		return fmt.Errorf("%w: %s", core.ErrConflict, se.Error())
	}
	return err
}

type rowScanner interface{ Scan(dest ...any) error }

func affected(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// Connections

const connCols = "id, name, config_url, auth_kind, status, app_id, app_slug, app_client_id, app_html_url, installation_id, secret, created_at"

func (s *Store) scanConnection(r rowScanner) (core.Connection, error) {
	var c core.Connection
	var secret []byte
	var created int64
	err := r.Scan(&c.ID, &c.Name, &c.ConfigURL, &c.AuthKind, &c.Status, &c.AppID, &c.AppSlug,
		&c.AppClientID, &c.AppHTMLURL, &c.InstallationID, &secret, &created)
	if err != nil {
		return c, err
	}
	c.CreatedAt = fromNS(created)
	c.Secret, err = s.seal.open(secret, "connection:"+c.ID)
	if err != nil {
		return c, fmt.Errorf("decrypt secret of connection %s: %w", c.ID, err)
	}
	return c, nil
}

func (s *Store) ListConnections(ctx context.Context) ([]core.Connection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+connCols+" FROM connections ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Connection{}
	for rows.Next() {
		c, err := s.scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConnection(ctx context.Context, id string) (core.Connection, error) {
	c, err := s.scanConnection(s.db.QueryRowContext(ctx, "SELECT "+connCols+" FROM connections WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Connection{}, core.ErrNotFound
	}
	return c, err
}

func (s *Store) CreateConnection(ctx context.Context, c *core.Connection) error {
	newID(&c.ID)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.clock()
	}
	c.CreatedAt = c.CreatedAt.UTC()
	secret, err := s.seal.seal(c.Secret, "connection:"+c.ID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO connections ("+connCols+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
		c.ID, c.Name, c.ConfigURL, c.AuthKind, c.Status, c.AppID, c.AppSlug, c.AppClientID,
		c.AppHTMLURL, c.InstallationID, secret, toNS(c.CreatedAt))
	return mapErr(err)
}

func (s *Store) UpdateConnection(ctx context.Context, c *core.Connection) error {
	secret, err := s.seal.seal(c.Secret, "connection:"+c.ID)
	if err != nil {
		return err
	}
	return affected(s.db.ExecContext(ctx, `UPDATE connections SET name=?, config_url=?, auth_kind=?, status=?,
		app_id=?, app_slug=?, app_client_id=?, app_html_url=?, installation_id=?, secret=? WHERE id=?`,
		c.Name, c.ConfigURL, c.AuthKind, c.Status, c.AppID, c.AppSlug, c.AppClientID, c.AppHTMLURL,
		c.InstallationID, secret, c.ID))
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	return affected(s.db.ExecContext(ctx, "DELETE FROM connections WHERE id = ?", id))
}

// Pools

const poolCols = "id, connection_id, name, vcpus, memory_mb, arch, max_runners, cluster, node, runner_group, image, enabled, scale_set_id, last_error, created_at, updated_at"

func scanPool(r rowScanner) (core.Pool, error) {
	var p core.Pool
	var created, updated int64
	err := r.Scan(&p.ID, &p.ConnectionID, &p.Name, &p.VCPUs, &p.MemoryMB, &p.Arch, &p.MaxRunners,
		&p.Cluster, &p.Node, &p.RunnerGroup, &p.Image, &p.Enabled, &p.ScaleSetID, &p.LastError,
		&created, &updated)
	p.CreatedAt, p.UpdatedAt = fromNS(created), fromNS(updated)
	return p, err
}

func (s *Store) ListPools(ctx context.Context) ([]core.Pool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+poolCols+" FROM pools ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Pool{}
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPool(ctx context.Context, id string) (core.Pool, error) {
	p, err := scanPool(s.db.QueryRowContext(ctx, "SELECT "+poolCols+" FROM pools WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Pool{}, core.ErrNotFound
	}
	return p, err
}

// CreatePool returns ErrConflict for a duplicate name within the connection and also for an
// unknown connection id (foreign key violation).
func (s *Store) CreatePool(ctx context.Context, p *core.Pool) error {
	newID(&p.ID)
	now := s.clock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, "INSERT INTO pools ("+poolCols+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		p.ID, p.ConnectionID, p.Name, p.VCPUs, p.MemoryMB, p.Arch, p.MaxRunners, p.Cluster, p.Node,
		p.RunnerGroup, p.Image, p.Enabled, p.ScaleSetID, p.LastError, toNS(p.CreatedAt), toNS(p.UpdatedAt))
	return mapErr(err)
}

func (s *Store) UpdatePool(ctx context.Context, p *core.Pool) error {
	now := s.clock()
	err := affected(s.db.ExecContext(ctx, `UPDATE pools SET connection_id=?, name=?, vcpus=?, memory_mb=?, arch=?,
		max_runners=?, cluster=?, node=?, runner_group=?, image=?, enabled=?, updated_at=? WHERE id=?`,
		p.ConnectionID, p.Name, p.VCPUs, p.MemoryMB, p.Arch, p.MaxRunners, p.Cluster, p.Node,
		p.RunnerGroup, p.Image, p.Enabled, toNS(now), p.ID))
	if err == nil {
		p.UpdatedAt = now
	}
	return err
}

func (s *Store) SetPoolRuntime(ctx context.Context, id string, scaleSetID int, lastError string) error {
	return affected(s.db.ExecContext(ctx, "UPDATE pools SET scale_set_id=?, last_error=? WHERE id=?",
		scaleSetID, lastError, id))
}

func (s *Store) DeletePool(ctx context.Context, id string) error {
	return affected(s.db.ExecContext(ctx, "DELETE FROM pools WHERE id = ?", id))
}

// Runners

const runnerCols = "name, pool_id, service_id, state, oakestra_status, cluster_id, worker_id, job_request_id, created_at, running_at, removed_at"

func scanRunner(r rowScanner) (core.Runner, error) {
	var rn core.Runner
	var created int64
	var running, removed sql.NullInt64
	err := r.Scan(&rn.Name, &rn.PoolID, &rn.ServiceID, &rn.State, &rn.OakestraStatus, &rn.ClusterID,
		&rn.WorkerID, &rn.JobRequestID, &created, &running, &removed)
	rn.CreatedAt = fromNS(created)
	rn.RunningAt, rn.RemovedAt = ptrTime(running), ptrTime(removed)
	return rn, err
}

func (s *Store) ListRunners(ctx context.Context, poolID string) ([]core.Runner, error) {
	q := "SELECT " + runnerCols + " FROM runners WHERE state <> ?"
	args := []any{string(core.RunnerRemoved)}
	if poolID != "" {
		q += " AND pool_id = ?"
		args = append(args, poolID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Runner{}
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRunner also returns removed runners, which job history lookups rely on.
func (s *Store) GetRunner(ctx context.Context, name string) (core.Runner, error) {
	r, err := scanRunner(s.db.QueryRowContext(ctx, "SELECT "+runnerCols+" FROM runners WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Runner{}, core.ErrNotFound
	}
	return r, err
}

func (s *Store) CreateRunner(ctx context.Context, r *core.Runner) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.clock()
	}
	r.CreatedAt = r.CreatedAt.UTC()
	_, err := s.db.ExecContext(ctx, "INSERT INTO runners ("+runnerCols+") VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		r.Name, r.PoolID, r.ServiceID, r.State, r.OakestraStatus, r.ClusterID, r.WorkerID,
		r.JobRequestID, toNS(r.CreatedAt), nullNS(r.RunningAt), nullNS(r.RemovedAt))
	return mapErr(err)
}

func (s *Store) UpdateRunner(ctx context.Context, r *core.Runner) error {
	return affected(s.db.ExecContext(ctx, `UPDATE runners SET pool_id=?, service_id=?, state=?, oakestra_status=?,
		cluster_id=?, worker_id=?, job_request_id=?, running_at=?, removed_at=? WHERE name=?`,
		r.PoolID, r.ServiceID, r.State, r.OakestraStatus, r.ClusterID, r.WorkerID, r.JobRequestID,
		nullNS(r.RunningAt), nullNS(r.RemovedAt), r.Name))
}

// Jobs

const jobCols = "pool_id, request_id, runner_name, repository, workflow_ref, job_name, workflow_run_id, event_name, result, queued_at, started_at, finished_at, cluster_id, worker_id"

func scanJob(r rowScanner, extra ...any) (core.Job, error) {
	var j core.Job
	var queued, started, finished sql.NullInt64
	dest := append([]any{&j.PoolID, &j.RequestID, &j.RunnerName, &j.Repository, &j.WorkflowRef, &j.JobName,
		&j.WorkflowRunID, &j.EventName, &j.Result, &queued, &started, &finished, &j.ClusterID, &j.WorkerID}, extra...)
	err := r.Scan(dest...)
	j.QueuedAt, j.StartedAt, j.FinishedAt = ptrTime(queued), ptrTime(started), ptrTime(finished)
	return j, err
}

func mergeJob(dst *core.Job, src core.Job) {
	str := func(d *string, v string) {
		if v != "" {
			*d = v
		}
	}
	tm := func(d **time.Time, v *time.Time) {
		if v != nil && !v.IsZero() {
			t := v.UTC()
			*d = &t
		}
	}
	str(&dst.RunnerName, src.RunnerName)
	str(&dst.Repository, src.Repository)
	str(&dst.WorkflowRef, src.WorkflowRef)
	str(&dst.JobName, src.JobName)
	if src.WorkflowRunID != 0 {
		dst.WorkflowRunID = src.WorkflowRunID
	}
	str(&dst.EventName, src.EventName)
	str(&dst.Result, src.Result)
	tm(&dst.QueuedAt, src.QueuedAt)
	tm(&dst.StartedAt, src.StartedAt)
	tm(&dst.FinishedAt, src.FinishedAt)
	str(&dst.ClusterID, src.ClusterID)
	str(&dst.WorkerID, src.WorkerID)
}

// UpsertJob merges j into the stored row and writes the merged result back into j.
func (s *Store) UpsertJob(ctx context.Context, j *core.Job) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cur, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobCols+" FROM jobs WHERE pool_id = ? AND request_id = ?",
		j.PoolID, j.RequestID))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !exists {
		cur = core.Job{PoolID: j.PoolID, RequestID: j.RequestID}
	}
	mergeJob(&cur, *j)

	if exists {
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET runner_name=?, repository=?, workflow_ref=?, job_name=?,
			workflow_run_id=?, event_name=?, result=?, queued_at=?, started_at=?, finished_at=?, cluster_id=?,
			worker_id=? WHERE pool_id=? AND request_id=?`,
			cur.RunnerName, cur.Repository, cur.WorkflowRef, cur.JobName, cur.WorkflowRunID, cur.EventName,
			cur.Result, nullNS(cur.QueuedAt), nullNS(cur.StartedAt), nullNS(cur.FinishedAt), cur.ClusterID,
			cur.WorkerID, cur.PoolID, cur.RequestID)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO jobs ("+jobCols+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			cur.PoolID, cur.RequestID, cur.RunnerName, cur.Repository, cur.WorkflowRef, cur.JobName,
			cur.WorkflowRunID, cur.EventName, cur.Result, nullNS(cur.QueuedAt), nullNS(cur.StartedAt),
			nullNS(cur.FinishedAt), cur.ClusterID, cur.WorkerID)
	}
	if err != nil {
		return mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*j = cur
	return nil
}

func encodeCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(seq, 10)))
}

func decodeCursor(c string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err == nil {
		var seq int64
		if seq, err = strconv.ParseInt(string(b), 10, 64); err == nil && seq > 0 {
			return seq, nil
		}
	}
	return 0, fmt.Errorf("invalid jobs cursor %q", c)
}

func (s *Store) ListJobs(ctx context.Context, f core.JobFilter) ([]core.Job, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultJobLimit
	}
	if limit > maxJobLimit {
		limit = maxJobLimit
	}
	var where []string
	var args []any
	if f.PoolID != "" {
		where = append(where, "pool_id = ?")
		args = append(args, f.PoolID)
	}
	if f.Result != "" {
		where = append(where, "result = ?")
		args = append(args, f.Result)
	}
	if f.Before != "" {
		seq, err := decodeCursor(f.Before)
		if err != nil {
			return nil, "", err
		}
		where = append(where, "seq < ?")
		args = append(args, seq)
	}
	q := "SELECT " + jobCols + ", seq FROM jobs"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// One extra row tells us whether another page exists.
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []core.Job{}
	var lastSeq int64
	more := false
	for rows.Next() {
		var seq int64
		j, err := scanJob(rows, &seq)
		if err != nil {
			return nil, "", err
		}
		if len(out) == limit {
			more = true
			break
		}
		out = append(out, j)
		lastSeq = seq
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if more {
		next = encodeCursor(lastSeq)
	}
	return out, next, nil
}

// Oakestra credentials

func (s *Store) OakestraCredentials(ctx context.Context) (core.OakestraCredentials, error) {
	var c core.OakestraCredentials
	var pw []byte
	err := s.db.QueryRowContext(ctx, "SELECT username, password, organization FROM oakestra_credentials WHERE id = 1").
		Scan(&c.Username, &pw, &c.Organization)
	if errors.Is(err, sql.ErrNoRows) {
		return core.OakestraCredentials{}, core.ErrNotFound
	}
	if err != nil {
		return core.OakestraCredentials{}, err
	}
	c.Password, err = s.seal.open(pw, "oakestra_credentials")
	if err != nil {
		return core.OakestraCredentials{}, fmt.Errorf("decrypt oakestra password: %w", err)
	}
	return c, nil
}

func (s *Store) SetOakestraCredentials(ctx context.Context, c core.OakestraCredentials) error {
	pw, err := s.seal.seal(c.Password, "oakestra_credentials")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oakestra_credentials (id, username, password, organization)
		VALUES (1, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET username=excluded.username,
		password=excluded.password, organization=excluded.organization`, c.Username, pw, c.Organization)
	return err
}

// Manifest states

func (s *Store) SaveManifestState(ctx context.Context, state string, pending core.Connection) error {
	// Secret has json:"-", so it is stored separately and encrypted.
	body, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	secret, err := s.seal.seal(pending.Secret, "manifest:"+state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO manifest_states (state, connection, secret, created_at) VALUES (?,?,?,?)",
		state, string(body), secret, toNS(s.clock()))
	return mapErr(err)
}

func (s *Store) TakeManifestState(ctx context.Context, state string) (core.Connection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Connection{}, err
	}
	defer tx.Rollback()

	var body string
	var secret []byte
	var created int64
	err = tx.QueryRowContext(ctx, "SELECT connection, secret, created_at FROM manifest_states WHERE state = ?", state).
		Scan(&body, &secret, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Connection{}, core.ErrNotFound
	}
	if err != nil {
		return core.Connection{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM manifest_states WHERE state = ?", state); err != nil {
		return core.Connection{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Connection{}, err
	}
	if s.clock().Sub(fromNS(created)) > manifestTTL {
		return core.Connection{}, core.ErrNotFound
	}
	var c core.Connection
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return core.Connection{}, fmt.Errorf("decode manifest state: %w", err)
	}
	c.Secret, err = s.seal.open(secret, "manifest:"+state)
	if err != nil {
		return core.Connection{}, fmt.Errorf("decrypt secret of pending manifest state: %w", err)
	}
	return c, nil
}

func (s *Store) Prune(ctx context.Context) error {
	now := s.clock()
	cutoff := toNS(now.Add(-s.retention))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []struct {
		q    string
		args []any
	}{
		// removed_at can be missing if a caller forgot to set it; fall back to creation time so
		// such rows still age out.
		{"DELETE FROM runners WHERE state = ? AND COALESCE(removed_at, created_at) < ?", []any{string(core.RunnerRemoved), cutoff}},
		{"DELETE FROM jobs WHERE finished_at IS NOT NULL AND finished_at < ?", []any{cutoff}},
		{"DELETE FROM manifest_states WHERE created_at < ?", []any{toNS(now.Add(-manifestTTL))}},
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
