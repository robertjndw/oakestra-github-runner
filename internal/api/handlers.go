package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

const (
	defaultJobLimit = 50
	maxJobLimit     = 200
	maxNameLen      = 100
)

func (s *server) getStatus(c *gin.Context) {
	ctx := c.Request.Context()
	conns, err := s.Store.ListConnections(ctx)
	if err != nil {
		s.internalError(c, err)
		return
	}
	pools, err := s.Store.ListPools(ctx)
	if err != nil {
		s.internalError(c, err)
		return
	}
	runners, err := s.Store.ListRunners(ctx, "")
	if err != nil {
		s.internalError(c, err)
		return
	}

	st := core.Status{Version: s.Version, RunnerImage: s.RunnerImage, Oakestra: s.Oakestra.Status(ctx)}
	st.Connections.Total = len(conns)
	for _, conn := range conns {
		if conn.Status == core.ConnectionReady {
			st.Connections.Ready++
		}
	}
	st.Pools.Total = len(pools)
	for _, p := range pools {
		if p.Enabled {
			st.Pools.Enabled++
		}
	}
	for _, rn := range runners {
		switch rn.State {
		case core.RunnerPending:
			st.Runners.Pending++
		case core.RunnerIdle:
			st.Runners.Idle++
		case core.RunnerBusy:
			st.Runners.Busy++
		}
	}
	writeJSON(c, http.StatusOK, st)
}

func (s *server) putCredentials(c *gin.Context) {
	if s.EnvCredentials {
		writeError(c, http.StatusConflict, codeConflict, "the Oakestra credentials are set through environment variables and cannot be changed here")
		return
	}
	var in struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		Organization string `json:"organization"`
	}
	if !decodeBody(c, &in) {
		return
	}
	var problems []string
	if strings.TrimSpace(in.Username) == "" {
		problems = append(problems, "username is required")
	}
	if in.Password == "" {
		problems = append(problems, "password is required")
	}
	if len(problems) > 0 {
		writeValidation(c, problems)
		return
	}
	creds := core.OakestraCredentials{
		Username:     strings.TrimSpace(in.Username),
		Password:     in.Password,
		Organization: strings.TrimSpace(in.Organization),
	}
	if err := s.Store.SetOakestraCredentials(c.Request.Context(), creds); err != nil {
		s.internalError(c, err)
		return
	}
	s.Oakestra.CredentialsChanged()
	s.Controller.Reload()
	writeJSON(c, http.StatusOK, s.Oakestra.Status(c.Request.Context()))
}

func (s *server) listClusters(c *gin.Context) {
	clusters, err := s.Oakestra.ListClusters(c.Request.Context())
	if errors.Is(err, core.ErrNoCredentials) {
		writeError(c, http.StatusConflict, codeConflict, "the Oakestra service user is not configured yet: ask an admin to set it up in the addon settings")
		return
	}
	if err != nil {
		s.log.Warn("listing clusters failed", "error", err)
		writeError(c, http.StatusBadGateway, codeUpstream, "could not list clusters: "+err.Error())
		return
	}
	if clusters == nil {
		clusters = []core.Cluster{}
	}
	writeJSON(c, http.StatusOK, clusters)
}

func writeValidation(c *gin.Context, problems []string) {
	writeError(c, http.StatusBadRequest, codeValidation, strings.Join(problems, "; "))
}

// parseConfigURL accepts https://<host>/<owner> and https://<host>/<owner>/<repo> and returns
// the normalized URL (no trailing slash) and the owner.
func parseConfigURL(raw string) (normalized, owner string, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", errors.New("config_url must be an https URL like https://github.com/<owner>[/<repo>]")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", "", errors.New("config_url must not contain credentials, a query or a fragment")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || slicesContainsEmpty(parts) {
		return "", "", errors.New("config_url must have the form https://<host>/<owner> or https://<host>/<owner>/<repo>")
	}
	return "https://" + u.Host + "/" + strings.Join(parts, "/"), parts[0], nil
}

func slicesContainsEmpty(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return true
		}
	}
	return false
}

func validateConnectionName(name string) string {
	switch {
	case name == "":
		return "name is required"
	case len(name) > maxNameLen:
		return fmt.Sprintf("name must be at most %d characters", maxNameLen)
	}
	return ""
}

func (s *server) listConnections(c *gin.Context) {
	conns, err := s.Store.ListConnections(c.Request.Context())
	if err != nil {
		s.internalError(c, err)
		return
	}
	if conns == nil {
		conns = []core.Connection{}
	}
	writeJSON(c, http.StatusOK, conns)
}

func (s *server) createConnection(c *gin.Context) {
	var in struct {
		Name      string `json:"name"`
		ConfigURL string `json:"config_url"`
		Token     string `json:"token"`
	}
	if !decodeBody(c, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	var problems []string
	if p := validateConnectionName(name); p != "" {
		problems = append(problems, p)
	}
	configURL, _, err := parseConfigURL(in.ConfigURL)
	if err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(in.Token) == "" {
		problems = append(problems, "token is required")
	}
	if len(problems) > 0 {
		writeValidation(c, problems)
		return
	}
	token := strings.TrimSpace(in.Token)
	if err := s.GitHub.ValidatePAT(c.Request.Context(), configURL, token); err != nil {
		writeError(c, http.StatusBadRequest, codeValidation, "GitHub rejected the token: "+err.Error())
		return
	}
	conn := core.Connection{
		Name:      name,
		ConfigURL: configURL,
		AuthKind:  core.AuthPAT,
		Status:    core.ConnectionReady,
		Secret:    token,
	}
	if err := s.Store.CreateConnection(c.Request.Context(), &conn); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	writeJSON(c, http.StatusCreated, conn)
}

func (s *server) deleteConnection(c *gin.Context) {
	if err := s.Store.DeleteConnection(c.Request.Context(), c.Param("id")); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	c.Status(http.StatusNoContent)
}

func newState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *server) appManifest(c *gin.Context) {
	var in struct {
		Name        string `json:"name"`
		ConfigURL   string `json:"config_url"`
		RedirectURL string `json:"redirect_url"`
	}
	if !decodeBody(c, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	var problems []string
	if p := validateConnectionName(name); p != "" {
		problems = append(problems, p)
	}
	configURL, _, err := parseConfigURL(in.ConfigURL)
	if err != nil {
		problems = append(problems, err.Error())
	}
	if ru, err := url.Parse(in.RedirectURL); err != nil || (ru.Scheme != "http" && ru.Scheme != "https") || ru.Host == "" {
		problems = append(problems, "redirect_url must be an absolute http(s) URL")
	}
	if len(problems) > 0 {
		writeValidation(c, problems)
		return
	}
	state, err := newState()
	if err != nil {
		s.internalError(c, err)
		return
	}
	manifest, err := s.GitHub.Manifest(c.Request.Context(), name, configURL, in.RedirectURL, state)
	if err != nil {
		s.log.Warn("building app manifest failed", "error", err)
		writeError(c, http.StatusBadGateway, codeUpstream, "could not build the GitHub App manifest: "+err.Error())
		return
	}
	pending := core.Connection{Name: name, ConfigURL: configURL, AuthKind: core.AuthApp}
	if err := s.Store.SaveManifestState(c.Request.Context(), state, pending); err != nil {
		s.internalError(c, err)
		return
	}
	manifest.State = state
	writeJSON(c, http.StatusOK, manifest)
}

func (s *server) appManifestComplete(c *gin.Context) {
	var in struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if !decodeBody(c, &in) {
		return
	}
	if in.State == "" || in.Code == "" {
		writeValidation(c, []string{"state and code are required"})
		return
	}
	pending, err := s.Store.TakeManifestState(c.Request.Context(), in.State)
	if errors.Is(err, core.ErrNotFound) {
		writeError(c, http.StatusBadRequest, codeValidation, "unknown or expired state, start the GitHub App setup again")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	creds, err := s.GitHub.ExchangeManifestCode(c.Request.Context(), pending.ConfigURL, in.Code)
	if err != nil {
		s.log.Warn("exchanging manifest code failed", "error", err)
		writeError(c, http.StatusBadGateway, codeUpstream, "GitHub did not accept the manifest code: "+err.Error())
		return
	}
	conn := core.Connection{
		Name:        pending.Name,
		ConfigURL:   pending.ConfigURL,
		AuthKind:    core.AuthApp,
		Status:      core.ConnectionPendingInstallation,
		AppID:       creds.AppID,
		AppSlug:     creds.Slug,
		AppClientID: creds.ClientID,
		AppHTMLURL:  creds.HTMLURL,
		Secret:      creds.PrivateKey,
	}
	if err := s.Store.CreateConnection(c.Request.Context(), &conn); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	writeJSON(c, http.StatusCreated, conn)
}

// appConnection loads a connection that must be a GitHub App. It writes the error response
// itself and reports false on failure.
func (s *server) appConnection(c *gin.Context) (core.Connection, bool) {
	conn, err := s.Store.GetConnection(c.Request.Context(), c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return core.Connection{}, false
	}
	if conn.AuthKind != core.AuthApp || conn.AppID == 0 {
		writeValidation(c, []string{"this connection is not a GitHub App connection"})
		return core.Connection{}, false
	}
	return conn, true
}

func (s *server) listInstallations(c *gin.Context) {
	conn, ok := s.appConnection(c)
	if !ok {
		return
	}
	list, err := s.GitHub.ListInstallations(c.Request.Context(), conn.ConfigURL, conn.AppID, conn.Secret)
	if err != nil {
		s.log.Warn("listing installations failed", "connection", conn.ID, "error", err)
		writeError(c, http.StatusBadGateway, codeUpstream, "could not list installations: "+err.Error())
		return
	}
	if list == nil {
		list = []core.Installation{}
	}
	writeJSON(c, http.StatusOK, list)
}

func (s *server) setInstallation(c *gin.Context) {
	var in struct {
		InstallationID int64 `json:"installation_id"`
	}
	if !decodeBody(c, &in) {
		return
	}
	if in.InstallationID <= 0 {
		writeValidation(c, []string{"installation_id is required"})
		return
	}
	conn, ok := s.appConnection(c)
	if !ok {
		return
	}
	list, err := s.GitHub.ListInstallations(c.Request.Context(), conn.ConfigURL, conn.AppID, conn.Secret)
	if err != nil {
		s.log.Warn("listing installations failed", "connection", conn.ID, "error", err)
		writeError(c, http.StatusBadGateway, codeUpstream, "could not list installations: "+err.Error())
		return
	}
	var found *core.Installation
	for i := range list {
		if list[i].ID == in.InstallationID {
			found = &list[i]
			break
		}
	}
	if found == nil {
		writeValidation(c, []string{"the App has no installation with this id"})
		return
	}
	_, owner, err := parseConfigURL(conn.ConfigURL)
	if err != nil {
		s.internalError(c, fmt.Errorf("stored config_url of connection %s: %w", conn.ID, err))
		return
	}
	if !strings.EqualFold(found.Account, owner) {
		writeValidation(c, []string{fmt.Sprintf("the installation belongs to %q but the connection targets %q", found.Account, owner)})
		return
	}
	conn.InstallationID = found.ID
	conn.Status = core.ConnectionReady
	if err := s.Store.UpdateConnection(c.Request.Context(), &conn); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	writeJSON(c, http.StatusOK, conn)
}

var poolNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// poolInput lists every field a client may send. The read-only ones are accepted so a client
// can send back what it fetched, but they are discarded.
type poolInput struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	VCPUs        int    `json:"vcpus"`
	MemoryMB     int    `json:"memory_mb"`
	Arch         string `json:"arch"`
	MaxRunners   int    `json:"max_runners"`
	Cluster      string `json:"cluster"`
	Node         string `json:"node"`
	RunnerGroup  string `json:"runner_group"`
	Image        string `json:"image"`
	Enabled      *bool  `json:"enabled"`

	ID         any `json:"id"`
	ScaleSetID any `json:"scale_set_id"`
	LastError  any `json:"last_error"`
	CreatedAt  any `json:"created_at"`
	UpdatedAt  any `json:"updated_at"`
}

// validatePool returns every problem found, so the UI can show them at once.
func validatePool(in poolInput, connectionExists bool) []string {
	var p []string
	switch {
	case in.Name == "":
		p = append(p, "name is required")
	case !poolNameRe.MatchString(in.Name):
		p = append(p, "name must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	if in.VCPUs < 1 || in.VCPUs > 256 {
		p = append(p, "vcpus must be between 1 and 256")
	}
	if in.MemoryMB < 256 || in.MemoryMB > 1048576 {
		p = append(p, "memory_mb must be between 256 and 1048576")
	}
	if in.MaxRunners < 1 || in.MaxRunners > 100 {
		p = append(p, "max_runners must be between 1 and 100")
	}
	if in.Arch != "" && in.Arch != "amd64" && in.Arch != "arm64" {
		p = append(p, `arch must be empty, "amd64" or "arm64"`)
	}
	if in.Node != "" && in.Cluster == "" {
		p = append(p, "node requires cluster")
	}
	// Oakestra's scheduler does not filter by architecture, so only a pinned pool can promise one.
	if in.Arch != "" && in.Cluster == "" {
		p = append(p, "arch requires cluster, because Oakestra does not schedule by architecture")
	}
	if strings.ContainsAny(in.Image, " \t\r\n") {
		p = append(p, "image must not contain whitespace")
	}
	switch {
	case in.ConnectionID == "":
		p = append(p, "connection_id is required")
	case !connectionExists:
		p = append(p, "connection_id does not exist")
	}
	return p
}

func (in poolInput) apply(p *core.Pool) {
	p.ConnectionID = in.ConnectionID
	p.Name = in.Name
	p.VCPUs = in.VCPUs
	p.MemoryMB = in.MemoryMB
	p.Arch = in.Arch
	p.MaxRunners = in.MaxRunners
	p.Cluster = in.Cluster
	p.Node = in.Node
	p.RunnerGroup = in.RunnerGroup
	p.Image = in.Image
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
}

// readPoolInput decodes and validates a pool body. It writes the error response itself.
func (s *server) readPoolInput(c *gin.Context) (poolInput, bool) {
	var in poolInput
	if !decodeBody(c, &in) {
		return in, false
	}
	in.Name = strings.TrimSpace(in.Name)
	exists := false
	if in.ConnectionID != "" {
		_, err := s.Store.GetConnection(c.Request.Context(), in.ConnectionID)
		switch {
		case err == nil:
			exists = true
		case !errors.Is(err, core.ErrNotFound):
			s.internalError(c, err)
			return in, false
		}
	}
	if problems := validatePool(in, exists); len(problems) > 0 {
		writeValidation(c, problems)
		return in, false
	}
	return in, true
}

func (s *server) listPools(c *gin.Context) {
	pools, err := s.Store.ListPools(c.Request.Context())
	if err != nil {
		s.internalError(c, err)
		return
	}
	if pools == nil {
		pools = []core.Pool{}
	}
	writeJSON(c, http.StatusOK, pools)
}

func (s *server) getPool(c *gin.Context) {
	p, err := s.Store.GetPool(c.Request.Context(), c.Param("id"))
	if err != nil {
		s.storeError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, p)
}

func (s *server) createPool(c *gin.Context) {
	in, ok := s.readPoolInput(c)
	if !ok {
		return
	}
	pool := core.Pool{Enabled: true}
	in.apply(&pool)
	if err := s.Store.CreatePool(c.Request.Context(), &pool); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	writeJSON(c, http.StatusCreated, pool)
}

func (s *server) updatePool(c *gin.Context) {
	id := c.Param("id")
	pool, err := s.Store.GetPool(c.Request.Context(), id)
	if err != nil {
		s.storeError(c, err)
		return
	}
	in, ok := s.readPoolInput(c)
	if !ok {
		return
	}
	// Starting from the stored pool keeps ID, ScaleSetID, LastError and CreatedAt out of the
	// client's reach.
	in.apply(&pool)
	if err := s.Store.UpdatePool(c.Request.Context(), &pool); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	writeJSON(c, http.StatusOK, pool)
}

func (s *server) deletePool(c *gin.Context) {
	if err := s.Store.DeletePool(c.Request.Context(), c.Param("id")); err != nil {
		s.storeError(c, err)
		return
	}
	s.Controller.Reload()
	c.Status(http.StatusNoContent)
}

func (s *server) listRunners(c *gin.Context) {
	runners, err := s.Store.ListRunners(c.Request.Context(), c.Query("pool_id"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	if runners == nil {
		runners = []core.Runner{}
	}
	writeJSON(c, http.StatusOK, runners)
}

func (s *server) listJobs(c *gin.Context) {
	limit := defaultJobLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeValidation(c, []string{"limit must be a positive integer"})
			return
		}
		limit = min(n, maxJobLimit)
	}
	jobs, next, err := s.Store.ListJobs(c.Request.Context(), core.JobFilter{
		PoolID: c.Query("pool_id"),
		Result: c.Query("result"),
		Before: c.Query("before"),
		Limit:  limit,
	})
	if err != nil {
		s.internalError(c, err)
		return
	}
	if jobs == nil {
		jobs = []core.Job{}
	}
	writeJSON(c, http.StatusOK, struct {
		Items []core.Job `json:"items"`
		Next  string     `json:"next"`
	}{jobs, next})
}
