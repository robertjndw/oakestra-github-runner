// Package api serves the addon's JSON API and the dashboard bundle.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeConflict     = "conflict"
	codeValidation   = "validation_failed"
	codeUpstream     = "upstream_error"
	codeInternal     = "internal_error"
	codeMethod       = "method_not_allowed"
	// codeUnavailable is not in the documented error codes. It accompanies the 503 sent while
	// no verification key is loaded.
	codeUnavailable = "unavailable"
)

const maxBodyBytes = 1 << 20

type Deps struct {
	Store      core.Store
	Oakestra   core.Oakestra
	GitHub     core.GitHubApps
	Controller core.PoolController
	// Verifier is nil only when authentication is switched off.
	Verifier       TokenVerifier
	EnvCredentials bool
	Version        string
	RunnerImage    string
	// UI holds the dashboard bundle. Nil serves 404 for everything outside /api.
	UI     fs.FS
	Logger *slog.Logger
}

type server struct {
	Deps
	verifier TokenVerifier
	log      *slog.Logger
}

// New builds the HTTP handler for the whole addon. The returned engine is only a router; the
// caller's http.Server owns timeouts and shutdown.
func New(deps Deps) http.Handler {
	s := &server{Deps: deps, verifier: deps.Verifier, log: deps.Logger}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.verifier == nil {
		s.log.Warn("API authentication is DISABLED (GHR_AUTH=off): every request is treated as an admin. Use this for local development only")
	}

	// Release mode keeps gin from printing its banner and route table to stdout.
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Redirects would answer a POST with a 301/307 that clients follow as a GET without the
	// body, so a stray slash must be a plain 404.
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = true
	r.Use(s.recoverPanics, s.logRequests)

	get := func(g gin.IRoutes, p string, h ...gin.HandlerFunc) {
		g.GET(p, h...)
		// net/http answered HEAD for every GET route, keep that.
		g.HEAD(p, h...)
	}

	get(r, "/healthz", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(c.Writer, "ok\n")
	})

	api := r.Group("/api", s.authenticate)
	get(api, "/status", s.getStatus)
	api.PUT("/oakestra-credentials", s.putCredentials)
	get(api, "/clusters", s.listClusters)

	get(api, "/connections", s.listConnections)
	api.POST("/connections", s.createConnection)
	api.DELETE("/connections/:id", s.deleteConnection)
	api.POST("/github/app-manifest", s.appManifest)
	api.POST("/github/app-manifest/complete", s.appManifestComplete)
	get(api, "/connections/:id/installations", s.listInstallations)
	api.PUT("/connections/:id/installation", s.setInstallation)

	get(api, "/pools", s.listPools)
	api.POST("/pools", s.createPool)
	get(api, "/pools/:id", s.getPool)
	api.PUT("/pools/:id", s.updatePool)
	api.DELETE("/pools/:id", s.deletePool)

	get(api, "/runners", s.listRunners)
	get(api, "/jobs", s.listJobs)

	// Unmatched requests never reach the group middleware, so the API ones authenticate here
	// to keep answering 401 before revealing which paths exist.
	allowed := allowedMethods(r.Routes())
	r.NoRoute(func(c *gin.Context) {
		if !isAPIPath(c.Request.URL.Path) {
			s.serveUI(c)
			return
		}
		if s.authenticate(c); !c.IsAborted() {
			writeError(c, http.StatusNotFound, codeNotFound, "no such endpoint")
		}
	})
	r.NoMethod(func(c *gin.Context) {
		c.Header("Allow", allowed(c.Request.URL.Path))
		if !isAPIPath(c.Request.URL.Path) {
			http.Error(c.Writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.authenticate(c); !c.IsAborted() {
			writeError(c, http.StatusMethodNotAllowed, codeMethod, "method not allowed")
		}
	})
	return r
}

func isAPIPath(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

// allowedMethods returns a lookup for the Allow header of a 405. gin does not provide one, and
// net/http's mux, which this replaced, did.
func allowedMethods(routes gin.RoutesInfo) func(string) string {
	return func(p string) string {
		segs := strings.Split(p, "/")
		var methods []string
		for _, rt := range routes {
			rs := strings.Split(rt.Path, "/")
			if len(rs) != len(segs) {
				continue
			}
			match := true
			for i, seg := range rs {
				if strings.HasPrefix(seg, ":") {
					match = segs[i] != ""
				} else {
					match = seg == segs[i]
				}
				if !match {
					break
				}
			}
			if match && !slices.Contains(methods, rt.Method) {
				methods = append(methods, rt.Method)
			}
		}
		slices.Sort(methods)
		return strings.Join(methods, ", ")
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(c *gin.Context, status int, v any) {
	c.Header("Content-Type", "application/json")
	c.Status(status)
	_ = json.NewEncoder(c.Writer).Encode(v)
}

func writeError(c *gin.Context, status int, code, msg string) {
	writeJSON(c, status, apiError{Code: code, Message: msg})
}

func (s *server) internalError(c *gin.Context, err error) {
	s.log.Error("request failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	writeError(c, http.StatusInternalServerError, codeInternal, "internal error")
}

// storeError maps the store's sentinel errors to HTTP responses.
func (s *server) storeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		writeError(c, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, core.ErrConflict):
		writeError(c, http.StatusConflict, codeConflict, conflictMessage(err))
	default:
		s.internalError(c, err)
	}
}

// conflictMessage prefers the store's own wording ("pool name already in use") over the bare
// sentinel text.
func conflictMessage(err error) string {
	msg := strings.TrimSpace(strings.TrimSuffix(err.Error(), core.ErrConflict.Error()))
	msg = strings.TrimRight(msg, ": ")
	if msg == "" {
		return "conflict"
	}
	return msg
}

// decodeBody reads one JSON object strictly. gin's ShouldBindJSON is avoided on purpose: it
// tolerates unknown fields and trailing data. It writes the 400 itself and reports false.
func decodeBody(c *gin.Context, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		// A second value after the object is as malformed as garbage.
		if _, err2 := dec.Token(); err2 != io.EOF {
			err = errors.New("unexpected data after the JSON object")
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		msg := "invalid JSON body: " + err.Error()
		if errors.As(err, &tooBig) {
			msg = "request body too large"
		}
		writeError(c, http.StatusBadRequest, codeValidation, msg)
		return false
	}
	return true
}

// logRequests logs the path only: the query of the manifest callback carries a one-time code,
// and headers carry the bearer token.
func (s *server) logRequests(c *gin.Context) {
	start := time.Now()
	c.Next()
	attrs := []any{"method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(), "duration", time.Since(start)}
	if claims, ok := claimsFrom(c); ok {
		attrs = append(attrs, "user", claims.Subject)
	}
	s.log.Info("request", attrs...)
}

// recoverPanics replaces gin.Recovery, which prints request headers (and the bearer token)
// to stderr.
func (s *server) recoverPanics(c *gin.Context) {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		if rec == http.ErrAbortHandler {
			panic(rec)
		}
		s.log.Error("panic in handler", "method", c.Request.Method, "path", c.Request.URL.Path, "panic", rec, "stack", string(debug.Stack()))
		// Headers may already be out; the body is then appended to a response that is lost anyway.
		writeError(c, http.StatusInternalServerError, codeInternal, "internal error")
		c.Abort()
	}()
	c.Next()
}

// serveUI serves concrete files only. The dashboard loads mf-manifest.json, remoteEntry.js and
// hashed chunks by exact name, so an SPA fallback to index.html would only hide mistakes.
func (s *server) serveUI(c *gin.Context) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Header("Allow", "GET, HEAD")
		http.Error(c.Writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Anything but the canonical form ("/../x", "//x", "/a/./b") is refused instead of cleaned,
	// so one file has one URL.
	p := c.Request.URL.Path
	name := strings.TrimPrefix(p, "/")
	if s.UI == nil || name == "" || path.Clean(p) != p || !fs.ValidPath(name) {
		http.NotFound(c.Writer, c.Request)
		return
	}
	info, err := fs.Stat(s.UI, name)
	if err != nil || info.IsDir() {
		http.NotFound(c.Writer, c.Request)
		return
	}
	data, err := fs.ReadFile(s.UI, name)
	if err != nil {
		http.NotFound(c.Writer, c.Request)
		return
	}
	switch base := path.Base(name); {
	case base == "mf-manifest.json" || base == "remoteEntry.js":
		c.Header("Cache-Control", "no-cache")
	case strings.HasPrefix(name, "assets/"):
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), bytes.NewReader(data))
}
