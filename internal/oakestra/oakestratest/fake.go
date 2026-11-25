// Package oakestratest is an in-process fake of the Oakestra system manager for tests and manual
// development. It speaks the subset of the REST API the oakestra package uses, including the
// quirks of the real implementation (see the handlers), and has knobs to steer instance state,
// expire tokens and inject failures.
package oakestratest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Instance statuses the real system manager uses.
const (
	StatusRequested = "REQUESTED"
	StatusRunning   = "RUNNING"
	StatusCompleted = "COMPLETED"
	StatusFailed    = "FAILED"
	StatusDead      = "DEAD"
)

// Options configures a Server. The zero value works.
type Options struct {
	// Users maps user names to passwords. Empty accepts any non-empty credentials.
	Users map[string]string
	// AccessTTL and RefreshTTL default to 10 minutes and 30 days, like the real system manager.
	AccessTTL, RefreshTTL time.Duration
	// Now is the clock for token expiry; time.Now when nil. Tests that move a fake clock must
	// give the client the same one.
	Now func() time.Time
}

// Microservice is the microservice part of a created service SLA, as the fake received it.
type Microservice struct {
	Name           string       `json:"microservice_name"`
	Namespace      string       `json:"microservice_namespace"`
	Virtualization string       `json:"virtualization"`
	Code           string       `json:"code"`
	Cmd            []string     `json:"cmd"`
	Environment    []string     `json:"environment"`
	OneShot        bool         `json:"one_shot"`
	VCPUs          int          `json:"vcpus"`
	Memory         int          `json:"memory"`
	Constraints    []Constraint `json:"constraints"`
}

// Constraint is a placement constraint of an SLA.
type Constraint struct {
	Type    string `json:"type"`
	Cluster string `json:"cluster,omitempty"`
	Node    string `json:"node,omitempty"`
}

// Instance is one entry of a service's instance_list.
type Instance struct {
	InstanceNumber int    `json:"instance_number"`
	ClusterID      string `json:"cluster_id"`
	Status         string `json:"status"`
	StatusDetail   string `json:"status_detail"`
	WorkerID       string `json:"worker_id"`
}

// Service is a created service with the SLA it was created from.
type Service struct {
	ID            string
	ApplicationID string
	Owner         string
	// CustomerID is the customerID of the SLA the service was created with.
	CustomerID string
	// SLA is the microservice exactly as sent, so tests can assert on the SLA.
	SLA         Microservice
	Instances   []Instance
	nextInstNum int
}

// Application is an application of one user.
type Application struct {
	ID        string
	Name      string
	Namespace string
	Owner     string
	Services  []string
}

// ClusterSpec describes a cluster for GET /api/clusters/.
type ClusterSpec struct {
	// ID defaults to a generated id.
	ID       string
	Name     string
	Location string
	// ActiveNodes, VCPUs and MemoryMB are the cluster aggregates.
	ActiveNodes int
	VCPUs       int
	MemoryMB    int
	// Architectures are reported verbatim as keys of aggregation_per_architecture, so tests can
	// use the spellings workers report, such as "x86_64" or "aarch64".
	Architectures []string
}

// Failure makes matching requests answer with Status.
type Failure struct {
	// Method matches the request method; empty matches any.
	Method string
	// Path is a prefix of the request path; empty matches any.
	Path string
	// PathContains additionally requires the path to contain this text, for example
	// "/instance" to hit only instance calls.
	PathContains string
	Status       int
	Message      string
	// Times limits how many requests fail; 0 means until ClearFailures.
	Times int
}

type tokenInfo struct {
	user    string
	refresh bool
	exp     time.Time
}

// Server is the fake system manager. It is safe for concurrent use.
type Server struct {
	// URL is the base URL without a trailing slash and without /api.
	URL string

	opts Options
	ts   *httptest.Server
	mux  *http.ServeMux

	mu         sync.Mutex
	seq        int
	secret     []byte
	tokens     map[string]tokenInfo
	apps       map[string]*Application
	appOrder   []string
	services   map[string]*Service
	svcOrder   []string
	clusters   []ClusterSpec
	failures   []*Failure
	requests   []string
	logins     int
	refreshes  int
	omitJobID  bool
	noInstance bool
	failCreate string
	emptyLogin bool
}

// New starts a fake. Call Close when done.
func New(opts Options) *Server {
	if opts.AccessTTL <= 0 {
		opts.AccessTTL = 10 * time.Minute
	}
	if opts.RefreshTTL <= 0 {
		opts.RefreshTTL = 30 * 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Server{
		opts:     opts,
		secret:   []byte("oakestratest-secret"),
		tokens:   map[string]tokenInfo{},
		apps:     map[string]*Application{},
		services: map[string]*Service{},
	}
	s.routes()
	s.ts = httptest.NewServer(s)
	s.URL = s.ts.URL
	return s
}

// NewTB starts a fake that is closed when the test ends.
func NewTB(tb testing.TB, opts Options) *Server {
	tb.Helper()
	s := New(opts)
	tb.Cleanup(s.Close)
	return s
}

// Close shuts the fake down.
func (s *Server) Close() { s.ts.Close() }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if fl := s.record(r); fl != nil {
		writeJSON(w, fl.Status, map[string]string{"message": fl.Message})
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) record(r *http.Request) *Failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	for i, fl := range s.failures {
		if (fl.Method != "" && fl.Method != r.Method) || !strings.HasPrefix(r.URL.Path, fl.Path) || !strings.Contains(r.URL.Path, fl.PathContains) {
			continue
		}
		out := *fl
		if fl.Times > 0 {
			fl.Times--
			if fl.Times == 0 {
				s.failures = slices.Delete(s.failures, i, i+1)
			}
		}
		return &out
	}
	return nil
}

func (s *Server) newIDLocked() string {
	s.seq++
	return fmt.Sprintf("%024x", s.seq)
}

// --- test knobs: failures and request log ---

// Fail adds a failure rule. Rules are checked in the order added.
func (s *Server) Fail(f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, &f)
}

// FailNext makes the next n requests of any kind answer with status.
func (s *Server) FailNext(n, status int) {
	s.Fail(Failure{Status: status, Message: "injected failure", Times: n})
}

// ClearFailures removes every failure rule.
func (s *Server) ClearFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = nil
}

// Requests returns "METHOD /path" for every request received, failed ones included.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// CountRequests counts requests whose "METHOD /path" equals the argument.
func (s *Server) CountRequests(methodAndPath string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r == methodAndPath {
			n++
		}
	}
	return n
}

// Logins and Refreshes count successful logins and token refreshes.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

func (s *Server) Refreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

// --- test knobs: tokens ---

// ExpireAccessTokens makes the server reject every access token issued so far with 401 while
// the exp claim inside still looks valid to the client. Refresh tokens keep working.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.tokens {
		if !t.refresh {
			t.exp = time.Unix(0, 0)
			s.tokens[k] = t
		}
	}
}

// InvalidateTokens forgets every token, refresh tokens included, like a system manager restart
// with a new secret. Only a new login helps.
func (s *Server) InvalidateTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
}

// InvalidateRefreshTokens forgets only the refresh tokens.
func (s *Server) InvalidateRefreshTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.tokens {
		if t.refresh {
			delete(s.tokens, k)
		}
	}
}

// EmptyLogin makes login answer 200 with {}, as some error paths of the real system manager do.
func (s *Server) EmptyLogin(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emptyLogin = on
}

// --- test knobs: clusters ---

// SetClusters replaces the clusters GET /api/clusters/ reports.
func (s *Server) SetClusters(c ...ClusterSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters = slices.Clone(c)
}

// --- test knobs: services and instances ---

// OmitJobID makes service creation leave job_id out of the response.
func (s *Server) OmitJobID(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.omitJobID = on
}

// SkipInstanceCreation makes the deploy call answer ok without creating an instance, which is
// what a service looks like until the scheduler has picked it up.
func (s *Server) SkipInstanceCreation(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noInstance = on
}

// FailServiceCreation makes service creation answer 200 with the service in failed_services and
// the given message. An empty message turns it off.
func (s *Server) FailServiceCreation(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCreate = message
}

// SetInstanceStatus sets the status of the service's instance.
func (s *Server) SetInstanceStatus(serviceID, status string) error {
	return s.updateInstance(serviceID, func(in *Instance) { in.Status = status })
}

// SetInstancePlacement sets the cluster and worker of the service's instance.
func (s *Server) SetInstancePlacement(serviceID, clusterID, workerID string) error {
	return s.updateInstance(serviceID, func(in *Instance) {
		in.ClusterID = clusterID
		in.WorkerID = workerID
	})
}

func (s *Server) updateInstance(serviceID string, fn func(*Instance)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, ok := s.services[serviceID]
	if !ok {
		return fmt.Errorf("oakestratest: service %q not found", serviceID)
	}
	if len(svc.Instances) == 0 {
		return fmt.Errorf("oakestratest: service %q has no instance", serviceID)
	}
	fn(&svc.Instances[len(svc.Instances)-1])
	return nil
}

// --- inspection ---

// Services returns every service in creation order.
func (s *Server) Services() []Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Service, 0, len(s.svcOrder))
	for _, id := range s.svcOrder {
		out = append(out, cloneService(s.services[id]))
	}
	return out
}

// Service returns one service by id.
func (s *Server) Service(id string) (Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, ok := s.services[id]
	if !ok {
		return Service{}, false
	}
	return cloneService(svc), true
}

// Applications returns every application in creation order.
func (s *Server) Applications() []Application {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Application, 0, len(s.appOrder))
	for _, id := range s.appOrder {
		a := *s.apps[id]
		a.Services = slices.Clone(a.Services)
		out = append(out, a)
	}
	return out
}

// DeleteApplication removes an application and its services behind the client's back.
func (s *Server) DeleteApplication(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.apps[id]
	if !ok {
		return
	}
	for _, sid := range a.Services {
		delete(s.services, sid)
		s.svcOrder = slices.DeleteFunc(s.svcOrder, func(x string) bool { return x == sid })
	}
	delete(s.apps, id)
	s.appOrder = slices.DeleteFunc(s.appOrder, func(x string) bool { return x == id })
}

// DeleteServiceDirect removes a service without going through the API.
func (s *Server) DeleteServiceDirect(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteServiceLocked(id)
}

func cloneService(svc *Service) Service {
	c := *svc
	c.Instances = slices.Clone(svc.Instances)
	c.SLA.Cmd = slices.Clone(svc.SLA.Cmd)
	c.SLA.Environment = slices.Clone(svc.SLA.Environment)
	c.SLA.Constraints = slices.Clone(svc.SLA.Constraints)
	return c
}

// --- tokens ---

func (s *Server) newTokenLocked(user, org string, refresh bool) string {
	ttl := s.opts.AccessTTL
	if refresh {
		ttl = s.opts.RefreshTTL
	}
	exp := s.opts.Now().Add(ttl)
	s.seq++
	claims := jwt.MapClaims{
		"sub":          user,
		"user":         user,
		"organization": org,
		"exp":          exp.Unix(),
		"jti":          fmt.Sprint(s.seq),
		"type":         map[bool]string{false: "access", true: "refresh"}[refresh],
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		panic(err)
	}
	s.tokens[tok] = tokenInfo{user: user, refresh: refresh, exp: exp}
	return tok
}

// orgID derives a stable organization id from the organization name.
func orgID(name string) string {
	if name == "" {
		name = "root"
	}
	return fmt.Sprintf("%024x", []byte(name))[:24]
}

// userID derives a stable user document id; the fake has no user collection.
func userID(user string) string {
	return fmt.Sprintf("%024x", []byte(user))[:24]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func message(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"message": fmt.Sprintf(format, args...)})
}

// claimOrg reads the organization claim of a token this server issued.
func claimOrg(tok string) string {
	var mc jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(tok, &mc); err != nil {
		return ""
	}
	org, _ := mc["organization"].(string)
	return org
}

// OrganizationID returns the organization id the fake puts into tokens for a login with the
// given organization name ("" means root).
func OrganizationID(name string) string { return orgID(name) }
