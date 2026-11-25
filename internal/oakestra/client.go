// Package oakestra implements core.Oakestra against the Oakestra system manager REST API.
//
// The backend has no browser user, so it logs in as a dedicated service user. All runner
// services live in one application owned by that user; the application is created lazily.
package oakestra

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

const (
	defaultAppName   = "ghrunner"
	defaultNamespace = "ci"

	slaVersion = "v2.0"
	// runnerCommand replaces the image's entrypoint and command together, hence the full path.
	runnerCommand = "/home/runner/run.sh"
	// Environment variable the runner reads its just-in-time config from.
	jitEnv = "ACTIONS_RUNNER_INPUT_JITCONFIG"
)

// nameRE is the pattern the system manager enforces for application, namespace and
// microservice names.
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9]{1,32}$`)

// Config configures a Client.
type Config struct {
	// BaseURL is the system manager, e.g. http://system_manager:10000, without /api.
	BaseURL string
	// HTTPClient is optional.
	HTTPClient *http.Client
	// Credentials returns the service user. It must return core.ErrNoCredentials when none are
	// configured. source is "env" or "stored" and is only passed through to Status. It is called
	// for every login, so credentials stored later take effect without a restart.
	Credentials func(ctx context.Context) (creds core.OakestraCredentials, source string, err error)
	// AppName and Namespace identify the application that holds the runner services.
	AppName   string // default "ghrunner"
	Namespace string // default "ci"
	Logger    *slog.Logger
}

// Client implements core.Oakestra. It is safe for concurrent use.
type Client struct {
	cfg  Config
	base string
	hc   *http.Client
	log  *slog.Logger
	now  func() time.Time

	mu       sync.Mutex
	tokens   tokenState
	inflight *authCall
	// gen counts CredentialsChanged calls, so results of work started before one are dropped.
	gen   uint64
	appID string

	// appCreate serializes application creation so concurrent first calls create it once.
	appCreate sync.Mutex
}

var _ core.Oakestra = (*Client)(nil)

// New returns a client. It does not contact Oakestra; the first call logs in.
func New(cfg Config) *Client {
	if cfg.AppName == "" {
		cfg.AppName = defaultAppName
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultNamespace
	}
	c := &Client{
		cfg:  cfg,
		base: strings.TrimRight(cfg.BaseURL, "/"),
		hc:   cfg.HTTPClient,
		log:  cfg.Logger,
		now:  time.Now,
	}
	if c.hc == nil {
		c.hc = &http.Client{}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c
}

// CredentialsChanged drops cached tokens and the cached application id.
func (c *Client) CredentialsChanged() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.tokens = tokenState{}
	c.inflight = nil
	c.appID = ""
}

// Status reports the login state, logging in when there is no valid token.
func (c *Client) Status(ctx context.Context) core.OakestraStatus {
	if c.cfg.Credentials == nil {
		return core.OakestraStatus{}
	}
	creds, source, err := c.cfg.Credentials(ctx)
	if errors.Is(err, core.ErrNoCredentials) {
		return core.OakestraStatus{}
	}
	if err != nil {
		return core.OakestraStatus{Error: err.Error()}
	}
	st := core.OakestraStatus{
		Configured:   true,
		Source:       source,
		Username:     creds.Username,
		Organization: creds.Organization,
	}
	if _, err := c.session(ctx, ""); err != nil {
		st.Error = err.Error()
		return st
	}
	st.OK = true
	return st
}

// CreateRunner creates the service for one runner and starts its instance.
func (c *Client) CreateRunner(ctx context.Context, spec core.RunnerSpec) (string, error) {
	if !nameRE.MatchString(spec.Name) {
		return "", fmt.Errorf("oakestra: runner name %q must match %s", spec.Name, nameRE)
	}
	if spec.Image == "" || spec.JITConfig == "" {
		return "", errors.New("oakestra: runner image and JIT config are required")
	}
	if spec.Node != "" && spec.Cluster == "" {
		return "", errors.New("oakestra: a node constraint requires a cluster")
	}

	id, err := c.createService(ctx, spec)
	if err != nil {
		return "", err
	}
	if err := c.call(ctx, "deploy instance", http.MethodPost, serviceURL(id)+"/instance", nil, nil); err != nil {
		// The service without an instance would never run and nothing else knows its id, so
		// remove it rather than leave an orphan. The caller's context may be what failed.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
		defer cancel()
		if derr := c.DeleteRunner(dctx, id); derr != nil {
			c.log.Warn("oakestra: could not remove service after failed deploy", "service", id, "err", derr)
		}
		return "", err
	}
	return id, nil
}

func (c *Client) createService(ctx context.Context, spec core.RunnerSpec) (string, error) {
	var id string
	err := c.withApp(ctx, func(sess session, appID string) error {
		body := c.serviceSLA(sess.org, appID, spec)
		var res createServiceResult
		if err := c.call(ctx, "create service", http.MethodPost, "/api/service/", body, &res); err != nil {
			return err
		}
		// The endpoint answers 200 even when the service failed.
		if len(res.FailedServices) > 0 {
			f := res.FailedServices[0]
			status := f.Status
			if status == 0 {
				status = http.StatusInternalServerError
			}
			return &APIError{Op: "create service " + f.ServiceName, Status: status, Message: f.Message}
		}
		id = string(res.JobID)
		// job_id is str(None) when the insert failed without being reported.
		if id == "" || id == "None" {
			found, err := c.findService(ctx, appID, spec.Name)
			if err != nil {
				return err
			}
			id = found
		}
		return nil
	})
	return id, err
}

// findService looks up a service by microservice name, newest first.
func (c *Client) findService(ctx context.Context, appID, name string) (string, error) {
	docs, err := c.listServices(ctx, appID)
	if err != nil {
		return "", err
	}
	for _, d := range slices.Backward(docs) {
		if d.MicroserviceName == name && d.ID != "" {
			return string(d.ID), nil
		}
	}
	return "", fmt.Errorf("oakestra: create service %s: no service id in the response and none found by name", name)
}

func (c *Client) listServices(ctx context.Context, appID string) ([]serviceDoc, error) {
	var docs []serviceDoc
	err := c.call(ctx, "list services", http.MethodGet, "/api/services/"+url.PathEscape(appID), nil, &docs)
	return docs, err
}

func serviceURL(id string) string { return "/api/service/" + url.PathEscape(id) }

// GetRunner returns core.ErrNotFound when the service is gone.
func (c *Client) GetRunner(ctx context.Context, serviceID string) (core.ServiceInstance, error) {
	var doc *serviceDoc
	err := c.call(ctx, "get service", http.MethodGet, serviceURL(serviceID), nil, &doc)
	if statusOf(err) == http.StatusNotFound {
		return core.ServiceInstance{}, fmt.Errorf("oakestra: service %s: %w", serviceID, core.ErrNotFound)
	}
	if err != nil {
		return core.ServiceInstance{}, err
	}
	// A null or empty document means the same as a 404.
	if doc == nil || doc.ID == "" {
		return core.ServiceInstance{}, fmt.Errorf("oakestra: service %s: %w", serviceID, core.ErrNotFound)
	}
	return doc.toInstance(), nil
}

func (d serviceDoc) toInstance() core.ServiceInstance {
	out := core.ServiceInstance{ServiceID: string(d.ID), Name: d.MicroserviceName}
	// A runner has one instance; should a rescheduling leave several, the newest one counts.
	if len(d.InstanceList) > 0 {
		in := d.InstanceList[0]
		for _, other := range d.InstanceList[1:] {
			if other.InstanceNumber >= in.InstanceNumber {
				in = other
			}
		}
		out.Status = in.Status
		out.ClusterID = string(in.ClusterID)
		out.WorkerID = string(in.WorkerID)
	}
	return out
}

// ListRunners lists every service in the runner application.
func (c *Client) ListRunners(ctx context.Context) ([]core.ServiceInstance, error) {
	var docs []serviceDoc
	err := c.withApp(ctx, func(_ session, appID string) error {
		var err error
		docs, err = c.listServices(ctx, appID)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]core.ServiceInstance, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.toInstance())
	}
	return out, nil
}

// DeleteRunner deletes the service and its instance. A missing service is success.
func (c *Client) DeleteRunner(ctx context.Context, serviceID string) error {
	err := c.call(ctx, "delete service", http.MethodDelete, serviceURL(serviceID), nil, nil)
	if err == nil {
		return nil
	}
	// The system manager answers 500 "Job not deleted" for unknown and not-owned services alike,
	// so a failed delete is only an error when the service is still there.
	if st := statusOf(err); st == http.StatusInternalServerError || st == http.StatusNotFound {
		if _, gerr := c.GetRunner(ctx, serviceID); errors.Is(gerr, core.ErrNotFound) {
			return nil
		}
	}
	return err
}

// ListClusters returns the clusters. The endpoint needs no authentication, so the placement
// picker keeps working while credentials are wrong.
func (c *Client) ListClusters(ctx context.Context) ([]core.Cluster, error) {
	var docs []clusterDoc
	if err := c.send(ctx, "list clusters", http.MethodGet, "/api/clusters/", "", nil, &docs); err != nil {
		return nil, err
	}
	out := make([]core.Cluster, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.toCluster())
	}
	return out, nil
}

func (d clusterDoc) toCluster() core.Cluster {
	cl := core.Cluster{
		ID:          string(d.ID),
		ActiveNodes: int(d.ActiveNodes),
		VCPUs:       int(d.VCPUs),
		MemoryMB:    int(d.Memory),
	}
	// The system manager fills cluster_name with a placeholder when the candidate has no name.
	cl.Name = d.ClusterName
	if cl.Name == "" || cl.Name == "Unknown Cluster" {
		cl.Name = d.CandidateName
	}
	cl.Location = d.Location
	if cl.Location == "" || cl.Location == "Unknown Location" {
		cl.Location = d.CandLocation
	}
	seen := map[string]bool{}
	for arch := range d.PerArch {
		if a := normalizeArch(arch); a != "" && !seen[a] {
			seen[a] = true
			cl.Architectures = append(cl.Architectures, a)
		}
	}
	slices.Sort(cl.Architectures)
	return cl
}

// normalizeArch maps the architecture spellings workers report to Docker style names.
func normalizeArch(a string) string {
	switch a = strings.ToLower(strings.TrimSpace(a)); a {
	case "x86_64", "x86-64", "x64", "amd64":
		return "amd64"
	case "aarch64", "arm64", "armv8", "armv8l":
		return "arm64"
	case "armv7l", "armv7", "arm":
		return "arm"
	case "i386", "i686", "x86":
		return "386"
	}
	return a
}

func (c *Client) serviceSLA(org, appID string, spec core.RunnerSpec) sla {
	ms := slaMicroservice{
		Name:           spec.Name,
		Namespace:      c.cfg.Namespace,
		Virtualization: "container",
		Code:           spec.Image,
		Cmd:            []string{runnerCommand},
		Environment:    []string{jitEnv + "=" + spec.JITConfig},
		// With one_shot, exit code 0 ends as COMPLETED instead of DEAD.
		OneShot: true,
		VCPUs:   spec.VCPUs,
		Memory:  spec.MemoryMB,
	}
	if spec.Cluster != "" {
		ms.Constraints = []slaConstraint{{Type: "direct", Cluster: spec.Cluster, Node: spec.Node}}
	}
	return sla{
		SLAVersion: slaVersion,
		CustomerID: org,
		Applications: []slaApplication{{
			ApplicationID:        appID,
			ApplicationName:      c.cfg.AppName,
			ApplicationNamespace: c.cfg.Namespace,
			Microservices:        []slaMicroservice{ms},
		}},
	}
}
