package oakestra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
	"github.com/robertjndw/oakestra-github-runner/internal/oakestra/oakestratest"
)

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

type env struct {
	srv   *oakestratest.Server
	c     *Client
	clock *fakeClock
	creds *credsBox
}

type credsBox struct {
	mu     sync.Mutex
	creds  core.OakestraCredentials
	source string
	err    error
}

func (b *credsBox) get(context.Context) (core.OakestraCredentials, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.creds, b.source, b.err
}

func (b *credsBox) set(c core.OakestraCredentials, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.creds, b.err = c, err
}

func newEnv(t *testing.T, opts oakestratest.Options) *env {
	t.Helper()
	clock := &fakeClock{t: time.Now()}
	opts.Now = clock.Now
	srv := oakestratest.NewTB(t, opts)
	box := &credsBox{creds: core.OakestraCredentials{Username: "runner", Password: "pw"}, source: "env"}
	c := New(Config{BaseURL: srv.URL + "/", Credentials: box.get})
	c.now = clock.Now
	return &env{srv: srv, c: c, clock: clock, creds: box}
}

func spec(name string) core.RunnerSpec {
	return core.RunnerSpec{Name: name, Image: "ghcr.io/actions/actions-runner:latest", VCPUs: 4, MemoryMB: 8192, JITConfig: "secret-jit"}
}

func TestCreateRunner(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	ctx := t.Context()

	s := spec("runner1")
	s.Cluster, s.Node = "c1", "n1"
	id, err := e.c.CreateRunner(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := e.srv.Service(id)
	if !ok {
		t.Fatalf("service %s not in fake", id)
	}
	if svc.CustomerID != oakestratest.OrganizationID("") {
		t.Errorf("customerID = %q, want the organization claim", svc.CustomerID)
	}
	ms := svc.SLA
	if ms.Name != "runner1" || ms.Namespace != "ci" || ms.Virtualization != "container" || ms.Code != s.Image ||
		!ms.OneShot || ms.VCPUs != 4 || ms.Memory != 8192 {
		t.Errorf("unexpected SLA: %+v", ms)
	}
	if len(ms.Cmd) != 1 || ms.Cmd[0] != "/home/runner/run.sh" {
		t.Errorf("cmd = %v", ms.Cmd)
	}
	if len(ms.Environment) != 1 || ms.Environment[0] != "ACTIONS_RUNNER_INPUT_JITCONFIG=secret-jit" {
		t.Errorf("environment = %v", ms.Environment)
	}
	if len(ms.Constraints) != 1 || ms.Constraints[0] != (oakestratest.Constraint{Type: "direct", Cluster: "c1", Node: "n1"}) {
		t.Errorf("constraints = %+v", ms.Constraints)
	}
	apps := e.srv.Applications()
	if len(apps) != 1 || apps[0].Name != "ghrunner" || apps[0].Namespace != "ci" || svc.ApplicationID != apps[0].ID {
		t.Errorf("apps = %+v", apps)
	}

	// No constraint without a cluster.
	id2, err := e.c.CreateRunner(ctx, spec("runner2"))
	if err != nil {
		t.Fatal(err)
	}
	svc2, _ := e.srv.Service(id2)
	if len(svc2.SLA.Constraints) != 0 {
		t.Errorf("constraints = %+v, want none", svc2.SLA.Constraints)
	}
	if n := e.srv.CountRequests("POST /api/application/"); n != 1 {
		t.Errorf("application created %d times, want 1", n)
	}
}

func TestCreateRunnerValidation(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	for _, name := range []string{"", "has-dash", "with space", strings.Repeat("a", 33)} {
		if _, err := e.c.CreateRunner(t.Context(), spec(name)); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	s := spec("ok")
	s.Node = "n1"
	if _, err := e.c.CreateRunner(t.Context(), s); err == nil {
		t.Error("node without cluster accepted")
	}
	if n := len(e.srv.Requests()); n != 0 {
		t.Errorf("%d requests sent for invalid specs", n)
	}
}

func TestNoCredentials(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	e.creds.set(core.OakestraCredentials{}, core.ErrNoCredentials)
	if _, err := e.c.CreateRunner(t.Context(), spec("r")); !errors.Is(err, core.ErrNoCredentials) {
		t.Errorf("CreateRunner err = %v, want ErrNoCredentials", err)
	}
	if st := e.c.Status(t.Context()); st.Configured || st.OK || st.Error != "" {
		t.Errorf("status = %+v", st)
	}
}

func TestConcurrentFirstUse(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	const n = 20
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			ids[i], errs[i] = e.c.CreateRunner(t.Context(), spec(fmt.Sprintf("runner%d", i)))
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("runner %d: %v", i, err)
		}
	}
	if got := e.srv.Logins(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
	if got := e.srv.CountRequests("POST /api/application/"); got != 1 {
		t.Errorf("application creations = %d, want 1", got)
	}
	if got := len(e.srv.Services()); got != n {
		t.Errorf("services = %d, want %d", got, n)
	}
}

func TestRefreshBeforeExpiry(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	ctx := t.Context()
	if _, err := e.c.ListRunners(ctx); err != nil {
		t.Fatal(err)
	}
	// Inside the refresh margin of the 10 minute access token.
	e.clock.Advance(8*time.Minute + 30*time.Second)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := e.c.ListRunners(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if e.srv.Logins() != 1 || e.srv.Refreshes() != 1 {
		t.Errorf("logins=%d refreshes=%d, want 1 and 1", e.srv.Logins(), e.srv.Refreshes())
	}
}

func TestRelogin(t *testing.T) {
	t.Run("401 from an api call", func(t *testing.T) {
		e := newEnv(t, oakestratest.Options{})
		if _, err := e.c.ListRunners(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.srv.ExpireAccessTokens()
		if _, err := e.c.ListRunners(t.Context()); err != nil {
			t.Fatal(err)
		}
		if e.srv.Logins() != 2 {
			t.Errorf("logins = %d, want 2", e.srv.Logins())
		}
	})
	t.Run("refresh token rejected", func(t *testing.T) {
		e := newEnv(t, oakestratest.Options{})
		if _, err := e.c.ListRunners(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.srv.InvalidateTokens()
		e.clock.Advance(9 * time.Minute)
		if _, err := e.c.ListRunners(t.Context()); err != nil {
			t.Fatal(err)
		}
		if e.srv.Logins() != 2 {
			t.Errorf("logins = %d, want 2", e.srv.Logins())
		}
	})
	t.Run("wrong credentials", func(t *testing.T) {
		e := newEnv(t, oakestratest.Options{Users: map[string]string{"runner": "other"}})
		_, err := e.c.CreateRunner(t.Context(), spec("r"))
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 401 {
			t.Errorf("err = %v, want 401 APIError", err)
		}
	})
}

func TestExistingApplication(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	// A second client of the same user, as after a restart of the backend.
	other := New(Config{BaseURL: e.srv.URL, Credentials: e.creds.get})
	if _, err := other.CreateRunner(t.Context(), spec("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.CreateRunner(t.Context(), spec("second")); err != nil {
		t.Fatal(err)
	}
	apps := e.srv.Applications()
	if len(apps) != 1 || len(apps[0].Services) != 2 {
		t.Errorf("apps = %+v, want one app with both services", apps)
	}
}

func TestApplicationRecreated(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	if _, err := e.c.CreateRunner(t.Context(), spec("a")); err != nil {
		t.Fatal(err)
	}
	e.srv.DeleteApplication(e.srv.Applications()[0].ID)
	id, err := e.c.CreateRunner(t.Context(), spec("b"))
	if err != nil {
		t.Fatal(err)
	}
	if svc, ok := e.srv.Service(id); !ok || svc.SLA.Name != "b" {
		t.Errorf("service = %+v, %v", svc, ok)
	}
	if n := len(e.srv.Applications()); n != 1 {
		t.Errorf("apps = %d, want 1", n)
	}
}

func TestCreateServiceQuirks(t *testing.T) {
	t.Run("failed services", func(t *testing.T) {
		e := newEnv(t, oakestratest.Options{})
		e.srv.FailServiceCreation("net plugin down")
		_, err := e.c.CreateRunner(t.Context(), spec("r"))
		if err == nil || !strings.Contains(err.Error(), "net plugin down") {
			t.Errorf("err = %v", err)
		}
		if got := e.srv.CountRequests("POST /api/service/r/instance"); got != 0 {
			t.Error("instance deployed for failed service")
		}
	})
	t.Run("missing job id", func(t *testing.T) {
		e := newEnv(t, oakestratest.Options{})
		e.srv.OmitJobID(true)
		if _, err := e.c.CreateRunner(t.Context(), spec("older")); err != nil {
			t.Fatal(err)
		}
		id, err := e.c.CreateRunner(t.Context(), spec("newer"))
		if err != nil {
			t.Fatal(err)
		}
		svc, _ := e.srv.Service(id)
		if svc.SLA.Name != "newer" || len(svc.Instances) != 1 {
			t.Errorf("service %+v", svc)
		}
	})
	t.Run("deploy failure removes the service", func(t *testing.T) {
		e := newEnv(t, oakestratest.Options{})
		e.srv.Fail(oakestratest.Failure{Method: "POST", PathContains: "/instance", Status: 502, Message: "bad gateway"})
		if _, err := e.c.CreateRunner(t.Context(), spec("r")); err == nil {
			t.Fatal("expected error")
		}
		if n := len(e.srv.Services()); n != 0 {
			t.Errorf("%d services left behind", n)
		}
	})
}

func TestGetListDelete(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	ctx := t.Context()
	id, err := e.c.CreateRunner(ctx, spec("r1"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.c.GetRunner(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServiceID != id || got.Name != "r1" || got.Status != oakestratest.StatusRequested {
		t.Errorf("runner = %+v", got)
	}
	if err := e.srv.SetInstanceStatus(id, oakestratest.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.SetInstancePlacement(id, "cl1", "w1"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.c.GetRunner(ctx, id)
	if got.Status != "RUNNING" || got.ClusterID != "cl1" || got.WorkerID != "w1" {
		t.Errorf("runner = %+v", got)
	}

	list, err := e.c.ListRunners(ctx)
	if err != nil || len(list) != 1 || list[0].ServiceID != id || list[0].Status != "RUNNING" {
		t.Errorf("list = %+v, %v", list, err)
	}

	if _, err := e.c.GetRunner(ctx, "000000000000000000000999"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown service err = %v, want ErrNotFound", err)
	}
	if err := e.c.DeleteRunner(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.c.DeleteRunner(ctx, id); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if _, err := e.c.GetRunner(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted service err = %v", err)
	}
	if list, _ := e.c.ListRunners(ctx); len(list) != 0 {
		t.Errorf("list = %+v", list)
	}
}

func TestEmptyInstanceList(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	e.srv.SkipInstanceCreation(true)
	id, err := e.c.CreateRunner(t.Context(), spec("noinst"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.c.GetRunner(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "" || got.ClusterID != "" || got.Name != "noinst" {
		t.Errorf("runner = %+v, want empty status", got)
	}
}

func TestDeleteRealFailureIsReported(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	id, err := e.c.CreateRunner(t.Context(), spec("r"))
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Fail(oakestratest.Failure{Method: "DELETE", Path: "/api/service/", Status: 500, Message: "Job not deleted", Times: 1})
	if err := e.c.DeleteRunner(t.Context(), id); err == nil {
		t.Error("delete of an existing service that failed returned nil")
	}
}

func TestListClusters(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	e.srv.SetClusters(
		oakestratest.ClusterSpec{ID: "c1", Name: "edge1", Location: "Munich", ActiveNodes: 3, VCPUs: 24, MemoryMB: 65536, Architectures: []string{"x86_64", "aarch64"}},
		oakestratest.ClusterSpec{ID: "c2", Name: "edge2", Architectures: []string{"arm64", "amd64"}},
	)
	// No credentials needed.
	e.creds.set(core.OakestraCredentials{}, core.ErrNoCredentials)
	got, err := e.c.ListClusters(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []core.Cluster{
		{ID: "c1", Name: "edge1", Location: "Munich", ActiveNodes: 3, VCPUs: 24, MemoryMB: 65536, Architectures: []string{"amd64", "arm64"}},
		{ID: "c2", Name: "edge2", Architectures: []string{"amd64", "arm64"}},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("clusters = %+v\nwant %+v", got, want)
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t, oakestratest.Options{Users: map[string]string{"runner": "pw"}})
	ctx := t.Context()
	e.creds.mu.Lock()
	e.creds.creds.Organization = "acme"
	e.creds.mu.Unlock()

	st := e.c.Status(ctx)
	want := core.OakestraStatus{Configured: true, Source: "env", Username: "runner", Organization: "acme", OK: true}
	if st != want {
		t.Errorf("status = %+v, want %+v", st, want)
	}
	e.c.Status(ctx)
	if e.srv.Logins() != 1 {
		t.Errorf("logins = %d, a valid token must be reused", e.srv.Logins())
	}

	e.creds.set(core.OakestraCredentials{Username: "runner", Password: "wrong"}, nil)
	e.c.CredentialsChanged()
	st = e.c.Status(ctx)
	if !st.Configured || st.OK || st.Error == "" {
		t.Errorf("status = %+v", st)
	}

	e.creds.set(core.OakestraCredentials{Username: "runner", Password: "pw"}, nil)
	e.srv.EmptyLogin(true)
	if st = e.c.Status(ctx); st.OK || st.Error == "" {
		t.Errorf("empty login status = %+v", st)
	}
}

func TestCredentialsChanged(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	ctx := t.Context()
	if _, err := e.c.CreateRunner(ctx, spec("a")); err != nil {
		t.Fatal(err)
	}
	e.creds.set(core.OakestraCredentials{Username: "other", Password: "pw"}, nil)
	e.c.CredentialsChanged()
	if _, err := e.c.CreateRunner(ctx, spec("b")); err != nil {
		t.Fatal(err)
	}
	if e.srv.Logins() != 2 {
		t.Errorf("logins = %d, want 2", e.srv.Logins())
	}
	// Apps are private to their creator, so the new user got an app of its own.
	owners := map[string]bool{}
	for _, a := range e.srv.Applications() {
		owners[a.Owner] = true
	}
	if len(owners) != 2 {
		t.Errorf("owners = %v, want both users", owners)
	}
}

func TestUnavailable(t *testing.T) {
	e := newEnv(t, oakestratest.Options{})
	e.srv.Close()
	if _, err := e.c.ListClusters(t.Context()); err == nil {
		t.Error("expected error")
	}
}
