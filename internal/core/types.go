// Package core holds the domain types and the interfaces the packages of this addon use to talk
// to each other. It has no dependencies on the other internal packages, so store, oakestra,
// github, controller and api can all import it without cycles.
package core

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a write would break a reference or a uniqueness rule, for
	// example deleting a connection that pools still use.
	ErrConflict = errors.New("conflict")
	// ErrNoCredentials means the Oakestra service user has not been configured yet.
	ErrNoCredentials = errors.New("oakestra credentials are not configured")
)

type AuthKind string

const (
	AuthApp AuthKind = "app"
	AuthPAT AuthKind = "pat"
)

type ConnectionStatus string

const (
	// ConnectionPendingInstallation is a GitHub App that exists but has no installation chosen.
	ConnectionPendingInstallation ConnectionStatus = "pending_installation"
	ConnectionReady               ConnectionStatus = "ready"
)

// Connection is a GitHub target (organization or repository) plus the credential used to manage
// runner scale sets there.
type Connection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ConfigURL is https://github.com/<org> or https://github.com/<org>/<repo>.
	ConfigURL string           `json:"config_url"`
	AuthKind  AuthKind         `json:"auth_kind"`
	Status    ConnectionStatus `json:"status"`

	// GitHub App fields, empty for PAT connections.
	AppID          int64  `json:"app_id,omitempty"`
	AppSlug        string `json:"app_slug,omitempty"`
	AppClientID    string `json:"app_client_id,omitempty"`
	AppHTMLURL     string `json:"app_html_url,omitempty"`
	InstallationID int64  `json:"installation_id,omitempty"`

	// Secret is the PAT, or the App's private key in PEM. The store encrypts it at rest and it
	// never leaves the backend.
	Secret string `json:"-"`

	CreatedAt time.Time `json:"created_at"`
}

// Pool is one runner size. It maps 1:1 to a GitHub runner scale set, because a scale set has
// fixed labels and hands any matching job to any of its idle runners.
type Pool struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	// Name is the scale set name and therefore the runs-on label, e.g. oakestra-4vcpu-ubuntu-2404.
	Name       string `json:"name"`
	VCPUs      int    `json:"vcpus"`
	MemoryMB   int    `json:"memory_mb"`
	Arch       string `json:"arch,omitempty"` // "amd64", "arm64" or empty for any
	MaxRunners int    `json:"max_runners"`
	// Cluster and Node pin runners with an Oakestra "direct" constraint. Both are names (the
	// cluster name and the worker hostname), because that is what the scheduler matches, not ids.
	// Node requires Cluster.
	Cluster     string `json:"cluster,omitempty"`
	Node        string `json:"node,omitempty"`
	RunnerGroup string `json:"runner_group,omitempty"` // empty means GitHub's "default" group
	Image       string `json:"image,omitempty"`        // empty means the configured default image
	Enabled     bool   `json:"enabled"`

	// Set by the controller.
	ScaleSetID int    `json:"scale_set_id,omitempty"`
	LastError  string `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RunnerState string

const (
	// RunnerPending: the Oakestra service exists, the runner has not been seen running yet.
	RunnerPending RunnerState = "pending"
	// RunnerIdle: the container runs and waits for GitHub to assign a job.
	RunnerIdle RunnerState = "idle"
	// RunnerBusy: GitHub reported a job started on this runner.
	RunnerBusy RunnerState = "busy"
	// RunnerRemoved: the Oakestra service was deleted. Kept for job history lookups.
	RunnerRemoved RunnerState = "removed"
)

// Runner is one single-use runner container. Name is the Oakestra microservice name and the
// GitHub runner name, which is how scale set job messages are mapped back to a service.
type Runner struct {
	Name           string      `json:"name"`
	PoolID         string      `json:"pool_id"`
	ServiceID      string      `json:"service_id"`
	State          RunnerState `json:"state"`
	OakestraStatus string      `json:"oakestra_status,omitempty"`
	ClusterID      string      `json:"cluster_id,omitempty"`
	WorkerID       string      `json:"worker_id,omitempty"`
	// JobRequestID is the runner request id of the job this runner took, 0 while idle.
	JobRequestID int64      `json:"job_request_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	RunningAt    *time.Time `json:"running_at,omitempty"`
	RemovedAt    *time.Time `json:"removed_at,omitempty"`
}

// Job is one GitHub Actions job handled by a pool. Result is empty while the job runs, then
// GitHub's result string ("succeeded", "failed", "canceled", ...).
type Job struct {
	PoolID        string     `json:"pool_id"`
	RequestID     int64      `json:"request_id"`
	RunnerName    string     `json:"runner_name,omitempty"`
	Repository    string     `json:"repository"` // owner/name
	WorkflowRef   string     `json:"workflow_ref,omitempty"`
	JobName       string     `json:"job_name"`
	WorkflowRunID int64      `json:"workflow_run_id,omitempty"`
	EventName     string     `json:"event_name,omitempty"`
	Result        string     `json:"result,omitempty"`
	QueuedAt      *time.Time `json:"queued_at,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	// Where the job ran, copied from the runner so history survives runner cleanup.
	ClusterID string `json:"cluster_id,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

// JobFilter selects jobs for the history page, newest first.
type JobFilter struct {
	PoolID string
	Result string
	// Before returns jobs with a sort key lower than this cursor. Empty means from the newest.
	Before string
	Limit  int
}

// OakestraCredentials is the service user the backend logs in as.
type OakestraCredentials struct {
	Username     string `json:"username"`
	Password     string `json:"-"`
	Organization string `json:"organization,omitempty"`
}

// OakestraStatus describes the backend's login state for the status endpoint.
type OakestraStatus struct {
	Configured bool `json:"configured"`
	// Source is "env" when the credentials come from environment variables, "stored" when an
	// admin entered them in the UI.
	Source       string `json:"source,omitempty"`
	Username     string `json:"username,omitempty"`
	Organization string `json:"organization,omitempty"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// RunnerSpec is what the controller asks Oakestra to run for one job.
type RunnerSpec struct {
	Name     string // microservice name, ^[a-zA-Z0-9]{1,32}$
	Image    string
	VCPUs    int
	MemoryMB int
	Cluster  string
	Node     string
	// JITConfig is the base64 just-in-time runner config. It is a single-use secret.
	JITConfig string
}

// ServiceInstance is the part of an Oakestra service the controller cares about. A runner
// service has at most one instance.
type ServiceInstance struct {
	ServiceID string
	Name      string // microservice name
	// Status is the Oakestra instance status (RUNNING, COMPLETED, DEAD, FAILED, ...). It is
	// empty while the service has no instance yet.
	Status    string
	ClusterID string
	WorkerID  string
}

// Cluster is an Oakestra cluster as shown in the placement picker.
type Cluster struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Location    string `json:"location,omitempty"`
	ActiveNodes int    `json:"active_nodes"`
	VCPUs       int    `json:"vcpus"`
	MemoryMB    int    `json:"memory_mb"`
	// Architectures lists the worker architectures present in the cluster, e.g. ["amd64"].
	Architectures []string `json:"architectures,omitempty"`
}

// Installation is a GitHub App installation an admin can bind a connection to.
type Installation struct {
	ID         int64  `json:"id"`
	Account    string `json:"account"`
	TargetType string `json:"target_type"` // "Organization" or "User"
	HTMLURL    string `json:"html_url"`
}

// AppManifest is what the browser posts to GitHub to create the App.
type AppManifest struct {
	// PostURL is the GitHub page the browser submits the form to (field "manifest", plus the
	// state as a query parameter already included in the URL).
	PostURL  string         `json:"post_url"`
	Manifest map[string]any `json:"manifest"`
	State    string         `json:"state"`
}

// AppCredentials is the result of exchanging a manifest code.
type AppCredentials struct {
	AppID      int64
	Slug       string
	ClientID   string
	HTMLURL    string
	PrivateKey string // PEM
}
