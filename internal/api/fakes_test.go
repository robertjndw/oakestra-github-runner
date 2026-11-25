package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

type fakeStore struct {
	mu          sync.Mutex
	seq         int
	conns       map[string]core.Connection
	pools       map[string]core.Pool
	runners     []core.Runner
	jobs        []core.Job
	lastFilter  core.JobFilter
	creds       *core.OakestraCredentials
	manifests   map[string]core.Connection
	poolNameErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		conns:     map[string]core.Connection{},
		pools:     map[string]core.Pool{},
		manifests: map[string]core.Connection{},
	}
}

func (f *fakeStore) nextID(prefix string) string {
	f.seq++
	return prefix + strconv.Itoa(f.seq)
}

func (f *fakeStore) ListConnections(context.Context) ([]core.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Connection
	for _, c := range f.conns {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeStore) GetConnection(_ context.Context, id string) (core.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.conns[id]
	if !ok {
		return core.Connection{}, core.ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) CreateConnection(_ context.Context, c *core.Connection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.ID = f.nextID("c")
	c.CreatedAt = time.Now()
	f.conns[c.ID] = *c
	return nil
}

func (f *fakeStore) UpdateConnection(_ context.Context, c *core.Connection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.conns[c.ID]; !ok {
		return core.ErrNotFound
	}
	f.conns[c.ID] = *c
	return nil
}

func (f *fakeStore) DeleteConnection(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.conns[id]; !ok {
		return core.ErrNotFound
	}
	for _, p := range f.pools {
		if p.ConnectionID == id {
			return fmt.Errorf("pools still use this connection: %w", core.ErrConflict)
		}
	}
	delete(f.conns, id)
	return nil
}

func (f *fakeStore) ListPools(context.Context) ([]core.Pool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Pool
	for _, p := range f.pools {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) GetPool(_ context.Context, id string) (core.Pool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pools[id]
	if !ok {
		return core.Pool{}, core.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) nameTaken(p *core.Pool) bool {
	for _, o := range f.pools {
		if o.ID != p.ID && o.ConnectionID == p.ConnectionID && o.Name == p.Name {
			return true
		}
	}
	return false
}

func (f *fakeStore) CreatePool(_ context.Context, p *core.Pool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nameTaken(p) {
		return fmt.Errorf("pool name already in use: %w", core.ErrConflict)
	}
	p.ID = f.nextID("p")
	p.CreatedAt = time.Now()
	p.UpdatedAt = p.CreatedAt
	f.pools[p.ID] = *p
	return nil
}

func (f *fakeStore) UpdatePool(_ context.Context, p *core.Pool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pools[p.ID]; !ok {
		return core.ErrNotFound
	}
	if f.nameTaken(p) {
		return fmt.Errorf("pool name already in use: %w", core.ErrConflict)
	}
	f.pools[p.ID] = *p
	return nil
}

func (f *fakeStore) SetPoolRuntime(context.Context, string, int, string) error { return nil }

func (f *fakeStore) DeletePool(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pools[id]; !ok {
		return core.ErrNotFound
	}
	delete(f.pools, id)
	return nil
}

func (f *fakeStore) ListRunners(_ context.Context, poolID string) ([]core.Runner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Runner
	for _, r := range f.runners {
		if poolID == "" || r.PoolID == poolID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetRunner(context.Context, string) (core.Runner, error) {
	return core.Runner{}, core.ErrNotFound
}
func (f *fakeStore) CreateRunner(context.Context, *core.Runner) error { return nil }
func (f *fakeStore) UpdateRunner(context.Context, *core.Runner) error { return nil }
func (f *fakeStore) UpsertJob(context.Context, *core.Job) error       { return nil }

func (f *fakeStore) ListJobs(_ context.Context, fl core.JobFilter) ([]core.Job, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastFilter = fl
	var next string
	if len(f.jobs) > 0 {
		next = "cursor-2"
	}
	return f.jobs, next, nil
}

func (f *fakeStore) OakestraCredentials(context.Context) (core.OakestraCredentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.creds == nil {
		return core.OakestraCredentials{}, core.ErrNotFound
	}
	return *f.creds, nil
}

func (f *fakeStore) SetOakestraCredentials(_ context.Context, c core.OakestraCredentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds = &c
	return nil
}

func (f *fakeStore) SaveManifestState(_ context.Context, state string, c core.Connection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests[state] = c
	return nil
}

func (f *fakeStore) TakeManifestState(_ context.Context, state string) (core.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.manifests[state]
	if !ok {
		return core.Connection{}, core.ErrNotFound
	}
	delete(f.manifests, state)
	return c, nil
}

func (f *fakeStore) Prune(context.Context) error { return nil }
func (f *fakeStore) Close() error                { return nil }

type fakeOakestra struct {
	status          core.OakestraStatus
	clusters        []core.Cluster
	clustersErr     error
	credentialsSeen int
}

func (o *fakeOakestra) CreateRunner(context.Context, core.RunnerSpec) (string, error) {
	return "", nil
}
func (o *fakeOakestra) GetRunner(context.Context, string) (core.ServiceInstance, error) {
	return core.ServiceInstance{}, core.ErrNotFound
}
func (o *fakeOakestra) ListRunners(context.Context) ([]core.ServiceInstance, error) { return nil, nil }
func (o *fakeOakestra) DeleteRunner(context.Context, string) error                  { return nil }
func (o *fakeOakestra) ListClusters(context.Context) ([]core.Cluster, error) {
	return o.clusters, o.clustersErr
}
func (o *fakeOakestra) Status(context.Context) core.OakestraStatus { return o.status }
func (o *fakeOakestra) CredentialsChanged()                        { o.credentialsSeen++ }

type fakeGitHub struct {
	patErr        error
	patSeen       [2]string
	manifestState string
	manifestErr   error
	creds         core.AppCredentials
	exchangeErr   error
	installs      []core.Installation
	installsErr   error
	installsSeen  struct {
		appID int64
		key   string
	}
}

func (g *fakeGitHub) Manifest(_ context.Context, name, configURL, redirectURL, state string) (core.AppManifest, error) {
	if g.manifestErr != nil {
		return core.AppManifest{}, g.manifestErr
	}
	g.manifestState = state
	return core.AppManifest{
		PostURL:  "https://github.com/settings/apps/new?state=" + state,
		Manifest: map[string]any{"name": name, "url": configURL, "redirect_url": redirectURL},
	}, nil
}

func (g *fakeGitHub) ExchangeManifestCode(_ context.Context, _, code string) (core.AppCredentials, error) {
	if g.exchangeErr != nil {
		return core.AppCredentials{}, g.exchangeErr
	}
	if code != "good-code" {
		return core.AppCredentials{}, fmt.Errorf("bad code")
	}
	return g.creds, nil
}

func (g *fakeGitHub) ListInstallations(_ context.Context, _ string, appID int64, key string) ([]core.Installation, error) {
	g.installsSeen.appID, g.installsSeen.key = appID, key
	return g.installs, g.installsErr
}

func (g *fakeGitHub) ValidatePAT(_ context.Context, configURL, token string) error {
	g.patSeen = [2]string{configURL, token}
	return g.patErr
}

type fakeController struct {
	mu      sync.Mutex
	reloads int
}

func (c *fakeController) Reload() {
	c.mu.Lock()
	c.reloads++
	c.mu.Unlock()
}

func (c *fakeController) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reloads
}

type harness struct {
	t     *testing.T
	h     http.Handler
	store *fakeStore
	oak   *fakeOakestra
	gh    *fakeGitHub
	ctl   *fakeController
	deps  Deps
}

// newHarness builds the API with auth off, so handler tests do not deal with tokens.
func newHarness(t *testing.T, mutate ...func(*Deps)) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		store: newFakeStore(),
		oak:   &fakeOakestra{status: core.OakestraStatus{Configured: true, OK: true, Source: "stored"}},
		gh:    &fakeGitHub{},
		ctl:   &fakeController{},
	}
	h.deps = Deps{
		Store:       h.store,
		Oakestra:    h.oak,
		GitHub:      h.gh,
		Controller:  h.ctl,
		Version:     "1.2.3",
		RunnerImage: "runner:test",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		UI: fstest.MapFS{
			"mf-manifest.json":    {Data: []byte(`{}`)},
			"remoteEntry.js":      {Data: []byte(`//`)},
			"assets/app-abc.js":   {Data: []byte(`//`)},
			"assets/sub/x.css":    {Data: []byte(`a{}`)},
			"index.html":          {Data: []byte(`<html></html>`)},
			"static/other.txt":    {Data: []byte(`x`)},
			"secret-dir/file.txt": {Data: []byte(`x`)},
		},
	}
	for _, m := range mutate {
		m(&h.deps)
	}
	h.h = New(h.deps)
	return h
}

func (h *harness) do(method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	return doRequest(h.t, h.h, method, path, body, headers...)
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, status, rec.Body.String())
	}
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) apiError {
	t.Helper()
	wantStatus(t, rec, status)
	e := decode[apiError](t, rec)
	if e.Code != code {
		t.Fatalf("error code = %q, want %q (%s)", e.Code, code, e.Message)
	}
	return e
}

type testKeys struct {
	priv *rsa.PrivateKey
	pem  string
}

func newTestKeys(t *testing.T) testKeys {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return testKeys{priv: priv, pem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}

func (k testKeys) token(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":          "alice",
		"type":         "access",
		"roles":        []string{"Admin"},
		"organization": "acme",
		"exp":          time.Now().Add(10 * time.Minute).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(k.priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
