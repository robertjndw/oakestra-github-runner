package core

import "context"

// Store persists configuration and history. Implemented by internal/store on SQLite.
//
// Get and Delete methods return ErrNotFound for unknown ids. Create methods fill in ID (when
// empty) and the timestamps on the value they are given.
type Store interface {
	ListConnections(ctx context.Context) ([]Connection, error)
	GetConnection(ctx context.Context, id string) (Connection, error)
	CreateConnection(ctx context.Context, c *Connection) error
	UpdateConnection(ctx context.Context, c *Connection) error
	// DeleteConnection returns ErrConflict while pools reference the connection.
	DeleteConnection(ctx context.Context, id string) error

	ListPools(ctx context.Context) ([]Pool, error)
	GetPool(ctx context.Context, id string) (Pool, error)
	// CreatePool and UpdatePool return ErrConflict when another pool of the same connection
	// already uses the name. UpdatePool does not touch ScaleSetID and LastError.
	CreatePool(ctx context.Context, p *Pool) error
	UpdatePool(ctx context.Context, p *Pool) error
	// SetPoolRuntime records what the controller learned about the pool's scale set.
	SetPoolRuntime(ctx context.Context, id string, scaleSetID int, lastError string) error
	DeletePool(ctx context.Context, id string) error

	// ListRunners returns runners that are not removed. An empty poolID means all pools.
	ListRunners(ctx context.Context, poolID string) ([]Runner, error)
	GetRunner(ctx context.Context, name string) (Runner, error)
	CreateRunner(ctx context.Context, r *Runner) error
	UpdateRunner(ctx context.Context, r *Runner) error

	// UpsertJob inserts the job or merges into the existing row with the same pool and request
	// id. Zero-valued fields of j never overwrite stored values, so JobStarted and JobCompleted
	// can arrive in any order and more than once.
	UpsertJob(ctx context.Context, j *Job) error
	// ListJobs returns a page of jobs, newest first, and the cursor for the next page (empty
	// when there is none).
	ListJobs(ctx context.Context, f JobFilter) (jobs []Job, next string, err error)

	// OakestraCredentials returns ErrNotFound when none are stored.
	OakestraCredentials(ctx context.Context) (OakestraCredentials, error)
	SetOakestraCredentials(ctx context.Context, c OakestraCredentials) error

	// SaveManifestState and TakeManifestState keep the pending GitHub App manifest request
	// between the redirect to GitHub and the callback. Take deletes the state and returns
	// ErrNotFound for unknown or expired (older than one hour) states.
	SaveManifestState(ctx context.Context, state string, pending Connection) error
	TakeManifestState(ctx context.Context, state string) (Connection, error)

	// Prune deletes removed runners and finished jobs older than the retention period.
	Prune(ctx context.Context) error

	Close() error
}

// Oakestra is the system manager as the controller sees it. Implemented by internal/oakestra.
// All runner services live in one application owned by the service user.
type Oakestra interface {
	// CreateRunner creates the service for one runner and deploys its single instance. It
	// returns the Oakestra service id. Returns ErrNoCredentials when no service user is set.
	CreateRunner(ctx context.Context, spec RunnerSpec) (serviceID string, err error)
	// GetRunner returns ErrNotFound when the service is gone.
	GetRunner(ctx context.Context, serviceID string) (ServiceInstance, error)
	// ListRunners lists every service in the runner application, used to find orphans.
	ListRunners(ctx context.Context) ([]ServiceInstance, error)
	// DeleteRunner deletes the service and its instance. Deleting a missing service is not an
	// error.
	DeleteRunner(ctx context.Context, serviceID string) error
	ListClusters(ctx context.Context) ([]Cluster, error)
	// Status reports the login state. It may try to log in.
	Status(ctx context.Context) OakestraStatus
	// CredentialsChanged drops cached tokens after an admin stored new credentials.
	CredentialsChanged()
}

// GitHubApps covers the GitHub App setup calls the API needs. Implemented by internal/github.
type GitHubApps interface {
	// Manifest builds the App manifest for configURL. redirectURL is the dashboard page GitHub
	// sends the browser back to with ?code=...&state=... It takes a context because it may have
	// to ask GitHub whether the owner is an organization or a user.
	Manifest(ctx context.Context, name, configURL, redirectURL, state string) (AppManifest, error)
	// ExchangeManifestCode turns the code from the redirect into the App's credentials.
	// configURL is the connection's URL; it selects the GitHub host (github.com, ghe.com or a
	// GitHub Enterprise Server) whose API is called.
	ExchangeManifestCode(ctx context.Context, configURL, code string) (AppCredentials, error)
	// ListInstallations lists the App's installations on the host of configURL.
	ListInstallations(ctx context.Context, configURL string, appID int64, privateKeyPEM string) ([]Installation, error)
	// ValidatePAT checks that the token can see configURL. It returns a descriptive error
	// otherwise.
	ValidatePAT(ctx context.Context, configURL, token string) error
}

// PoolController is the running controller as the API sees it. Implemented by
// internal/controller.
type PoolController interface {
	// Reload makes the controller re-read connections and pools from the store and start, stop
	// or restart listeners to match. It returns quickly; work happens in the background.
	Reload()
}

/*
HTTP API, served under /api (the dashboard reaches it at /addon-assets/<addon id>/api).

Every route needs "Authorization: Bearer <Oakestra JWT>". Reads need any valid token, writes
(everything except GET) need the Admin role. Bodies and responses are JSON, using the json tags
of the types in this package. Errors are {"code": "...", "message": "..."} with codes
unauthorized (401), forbidden (403), not_found (404), conflict (409), validation_failed (400),
method_not_allowed (405), upstream_error (502), unavailable (503) and internal_error (500).

	GET    /api/status                         -> Status
	PUT    /api/oakestra-credentials           {username, password, organization?} -> OakestraStatus
	                                           409 conflict when credentials come from env
	GET    /api/clusters                       -> []Cluster

	GET    /api/connections                    -> []Connection
	POST   /api/connections                    {name, config_url, token} -> Connection (PAT, ready)
	DELETE /api/connections/{id}               -> 204, 409 while pools use it
	POST   /api/github/app-manifest            {name, config_url, redirect_url} -> AppManifest
	POST   /api/github/app-manifest/complete   {state, code} -> Connection (pending_installation)
	GET    /api/connections/{id}/installations -> []Installation
	PUT    /api/connections/{id}/installation  {installation_id} -> Connection (ready)

	GET    /api/pools                          -> []Pool
	POST   /api/pools                          Pool without id/runtime fields -> Pool
	GET    /api/pools/{id}                     -> Pool
	PUT    /api/pools/{id}                     -> Pool
	DELETE /api/pools/{id}                     -> 204

	GET    /api/runners[?pool_id=]             -> []Runner (not removed)
	GET    /api/jobs[?pool_id=&result=&before=&limit=] -> {"items": []Job, "next": "<cursor or empty>"}

Lists are never null: an empty list is [].
*/

// Status is the response of GET /api/status.
type Status struct {
	Version     string         `json:"version"`
	RunnerImage string         `json:"runner_image"`
	Oakestra    OakestraStatus `json:"oakestra"`
	Connections struct {
		Total int `json:"total"`
		Ready int `json:"ready"`
	} `json:"connections"`
	Pools struct {
		Total   int `json:"total"`
		Enabled int `json:"enabled"`
	} `json:"pools"`
	Runners struct {
		Pending int `json:"pending"`
		Idle    int `json:"idle"`
		Busy    int `json:"busy"`
	} `json:"runners"`
}
