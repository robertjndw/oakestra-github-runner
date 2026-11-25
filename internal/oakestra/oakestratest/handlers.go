package oakestratest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9]{1,32}$`)

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/auth/login", s.login)
	m.HandleFunc("POST /api/auth/refresh", s.refresh)
	m.HandleFunc("GET /api/clusters/{$}", s.listClusters)
	m.HandleFunc("GET /api/clusters", s.listClusters)
	m.HandleFunc("GET /api/user/{name}", s.authed(s.getUser))
	m.HandleFunc("POST /api/application/{$}", s.authed(s.createApplication))
	m.HandleFunc("GET /api/applications/{uid}", s.authed(s.listUserApplications))
	m.HandleFunc("POST /api/service/{$}", s.authed(s.createService))
	m.HandleFunc("GET /api/service/{id}", s.authed(s.getService))
	m.HandleFunc("DELETE /api/service/{id}", s.authed(s.deleteService))
	m.HandleFunc("POST /api/service/{id}/instance", s.authed(s.deployInstance))
	m.HandleFunc("GET /api/services/{appid}", s.authed(s.listAppServices))
	s.mux = m
}

type userHandler func(w http.ResponseWriter, r *http.Request, user string)

// authed answers 401 {"msg": ...} without a valid access token, like flask-jwt-extended.
func (s *Server) authed(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.authenticate(r, false)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"msg": "invalid or missing token"})
			return
		}
		h(w, r, user)
	}
}

func (s *Server) authenticate(r *http.Request, refresh bool) (string, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tok]
	if !ok || t.refresh != refresh || !s.opts.Now().Before(t.exp) {
		return "", false
	}
	return t.user, true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username         string `json:"username"`
		Password         string `json:"password"`
		OrganizationName string `json:"organization_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		message(w, http.StatusForbidden, "No credentials provided")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emptyLogin {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	ok := req.Username != "" && req.Password != ""
	if len(s.opts.Users) > 0 {
		pw, known := s.opts.Users[req.Username]
		ok = known && pw == req.Password
	}
	if !ok {
		message(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.logins++
	org := orgID(req.OrganizationName)
	writeJSON(w, http.StatusOK, map[string]string{
		"token":         s.newTokenLocked(req.Username, org, false),
		"refresh_token": s.newTokenLocked(req.Username, org, true),
	})
}

// refresh answers only a new access token, no new refresh token.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	user, ok := s.authenticate(r, true)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"msg": "invalid or missing token"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	// The organization of the original login is not tracked per user; the fake serves one.
	writeJSON(w, http.StatusOK, map[string]string{"token": s.newTokenLocked(user, s.orgOfLocked(user), false)})
}

// orgOfLocked finds the organization claim of the user's newest refresh token.
func (s *Server) orgOfLocked(user string) string {
	for tok, t := range s.tokens {
		if t.user == user && t.refresh {
			if org := claimOrg(tok); org != "" {
				return org
			}
		}
	}
	return orgID("")
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request, user string) {
	if r.PathValue("name") != user {
		message(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"_id": userID(user), "name": user})
}

// --- clusters ---

func (s *Server) listClusters(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	specs := slices.Clone(s.clusters)
	s.mu.Unlock()
	out := make([]map[string]any, 0, len(specs))
	for i, c := range specs {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("%024x", i+1)
		}
		perArch := map[string]any{}
		for _, a := range c.Architectures {
			perArch[a] = map[string]any{"active_nodes": 1, "vcpus": 1, "memory": 1}
		}
		out = append(out, map[string]any{
			"_id":                          id,
			"candidate_name":               c.Name,
			"candidate_location":           c.Location,
			"cluster_name":                 c.Name,
			"cluster_location":             c.Location,
			"active":                       true,
			"active_nodes":                 c.ActiveNodes,
			"vcpus":                        c.VCPUs,
			"memory":                       c.MemoryMB,
			"aggregation_per_architecture": perArch,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- applications ---

type slaDoc struct {
	SLAVersion   string `json:"sla_version"`
	CustomerID   string `json:"customerID"`
	Applications []struct {
		ApplicationID string          `json:"applicationID"`
		Name          string          `json:"application_name"`
		Namespace     string          `json:"application_namespace"`
		Microservices *[]Microservice `json:"microservices"`
	} `json:"applications"`
}

func (s *Server) createApplication(w http.ResponseWriter, r *http.Request, user string) {
	var doc slaDoc
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil || len(doc.Applications) == 0 {
		message(w, http.StatusUnprocessableEntity, "invalid SLA")
		return
	}
	for _, a := range doc.Applications {
		if !nameRE.MatchString(a.Name) || !nameRE.MatchString(a.Namespace) {
			message(w, http.StatusUnprocessableEntity, "application name or namespace does not match '^[a-zA-Z0-9]{1,32}$'")
			return
		}
		// register_app calls len() on the list; null is a 500 there.
		if a.Microservices == nil {
			message(w, http.StatusInternalServerError, "microservices missing")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range doc.Applications {
		if s.appByNameLocked(user, a.Name, a.Namespace) != nil {
			message(w, http.StatusConflict, "An application with the same name and namespace exists already")
			return
		}
	}
	for _, a := range doc.Applications {
		id := s.newIDLocked()
		app := &Application{ID: id, Name: a.Name, Namespace: a.Namespace, Owner: user}
		s.apps[id] = app
		s.appOrder = append(s.appOrder, id)
		for _, ms := range *a.Microservices {
			s.insertServiceLocked(app, ms)
		}
	}
	writeJSON(w, http.StatusOK, s.userAppsLocked(user))
}

func (s *Server) appByNameLocked(user, name, ns string) *Application {
	for _, id := range s.appOrder {
		a := s.apps[id]
		if a.Owner == user && a.Name == name && a.Namespace == ns {
			return a
		}
	}
	return nil
}

// userAppsLocked renders the user's applications. Apps are only visible to their creator.
func (s *Server) userAppsLocked(user string) []map[string]any {
	out := []map[string]any{}
	for _, id := range s.appOrder {
		if a := s.apps[id]; a.Owner == user {
			out = append(out, map[string]any{
				"applicationID":         a.ID,
				"application_name":      a.Name,
				"application_namespace": a.Namespace,
				"microservices":         append([]string{}, a.Services...),
				"userId":                userID(user),
			})
		}
	}
	return out
}

// listUserApplications wants the user's document id; the user name gives 401.
func (s *Server) listUserApplications(w http.ResponseWriter, r *http.Request, user string) {
	if r.PathValue("uid") != userID(user) {
		message(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	s.mu.Lock()
	out := s.userAppsLocked(user)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// --- services ---

func (s *Server) createService(w http.ResponseWriter, r *http.Request, user string) {
	var doc slaDoc
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil || len(doc.Applications) == 0 || doc.Applications[0].Microservices == nil {
		message(w, http.StatusBadRequest, "The given SLA was not formatted correctly")
		return
	}
	in := doc.Applications[0]
	for _, ms := range *in.Microservices {
		if !nameRE.MatchString(ms.Name) || !nameRE.MatchString(ms.Namespace) {
			message(w, http.StatusUnprocessableEntity, "microservice name or namespace does not match '^[a-zA-Z0-9]{1,32}$'")
			return
		}
		if ms.Virtualization == "" || ms.Code == "" {
			message(w, http.StatusUnprocessableEntity, "virtualization and code are required")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[in.ApplicationID]
	if !ok || app.Owner != user {
		message(w, http.StatusNotFound, "application not found")
		return
	}
	deployed := []map[string]any{}
	failed := []map[string]any{}
	lastID := ""
	for _, ms := range *in.Microservices {
		if s.failCreate != "" {
			failed = append(failed, map[string]any{"service_name": ms.Name, "message": s.failCreate, "status": 500})
			continue
		}
		lastID = s.insertServiceLocked(app, ms)
		s.services[lastID].CustomerID = doc.CustomerID
		deployed = append(deployed, map[string]any{"service_name": ms.Name, "status": 200})
	}
	res := map[string]any{"deployed_services": deployed, "failed_services": failed}
	if !s.omitJobID {
		// str(None) when nothing was inserted.
		if lastID == "" {
			res["job_id"] = "None"
		} else {
			res["job_id"] = lastID
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) insertServiceLocked(app *Application, ms Microservice) string {
	id := s.newIDLocked()
	s.services[id] = &Service{ID: id, ApplicationID: app.ID, Owner: app.Owner, SLA: ms, nextInstNum: 0}
	s.svcOrder = append(s.svcOrder, id)
	app.Services = append(app.Services, id)
	return id
}

func (s *Server) ownedLocked(user, id string) *Service {
	svc, ok := s.services[id]
	if !ok || svc.Owner != user {
		return nil
	}
	return svc
}

func (s *Server) serviceDocLocked(svc *Service) map[string]any {
	app := s.apps[svc.ApplicationID]
	instances := svc.Instances
	if instances == nil {
		instances = []Instance{}
	}
	return map[string]any{
		"_id":                              svc.ID,
		"microserviceID":                   svc.ID,
		"applicationID":                    svc.ApplicationID,
		"app_name":                         app.Name,
		"app_ns":                           app.Namespace,
		"microservice_name":                svc.SLA.Name,
		"microservice_namespace":           svc.SLA.Namespace,
		"service_name":                     svc.SLA.Name,
		"service_ns":                       svc.SLA.Namespace,
		"image":                            svc.SLA.Code,
		"cmd":                              svc.SLA.Cmd,
		"environment":                      svc.SLA.Environment,
		"one_shot":                         svc.SLA.OneShot,
		"instance_list":                    instances,
		"next_instance_progressive_number": svc.nextInstNum,
	}
}

// getService answers 404 "not found" for unknown or not-owned services.
func (s *Server) getService(w http.ResponseWriter, r *http.Request, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc := s.ownedLocked(user, r.PathValue("id"))
	if svc == nil {
		message(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, s.serviceDocLocked(svc))
}

func (s *Server) listAppServices(w http.ResponseWriter, r *http.Request, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[r.PathValue("appid")]
	if !ok || app.Owner != user {
		message(w, http.StatusNotFound, "app not found")
		return
	}
	out := []map[string]any{}
	for _, id := range app.Services {
		if svc, ok := s.services[id]; ok {
			out = append(out, s.serviceDocLocked(svc))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteService answers 500 "Job not deleted" for unknown or not-owned services.
func (s *Server) deleteService(w http.ResponseWriter, r *http.Request, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownedLocked(user, r.PathValue("id")) == nil {
		message(w, http.StatusInternalServerError, "Job not deleted")
		return
	}
	s.deleteServiceLocked(r.PathValue("id"))
	message(w, http.StatusOK, "Job deleted")
}

func (s *Server) deleteServiceLocked(id string) {
	svc, ok := s.services[id]
	if !ok {
		return
	}
	if app, ok := s.apps[svc.ApplicationID]; ok {
		app.Services = slices.DeleteFunc(app.Services, func(x string) bool { return x == id })
	}
	delete(s.services, id)
	s.svcOrder = slices.DeleteFunc(s.svcOrder, func(x string) bool { return x == id })
}

// deployInstance always answers ok, even for unknown services.
func (s *Server) deployInstance(w http.ResponseWriter, r *http.Request, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if svc := s.ownedLocked(user, r.PathValue("id")); svc != nil && !s.noInstance {
		svc.nextInstNum++
		svc.Instances = append(svc.Instances, Instance{InstanceNumber: svc.nextInstNum - 1, Status: StatusRequested})
	}
	message(w, http.StatusOK, "ok")
}
