package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// DefaultHomepageURL is the App's homepage in the manifest. GitHub requires one.
const DefaultHomepageURL = "https://github.com/robertjndw/oakestra-github-runner"

// Apps implements core.GitHubApps against the GitHub REST API.
type Apps struct {
	httpClient *http.Client

	// APIBaseURL overrides the REST API base that is otherwise derived from the config URL.
	// Tests point it at an httptest server.
	APIBaseURL string
	// HomepageURL is the App homepage put in the manifest.
	HomepageURL string

	now func() time.Time
}

var _ core.GitHubApps = (*Apps)(nil)

// NewApps returns an Apps using httpClient, or a client with sane timeouts when it is nil.
func NewApps(httpClient *http.Client) *Apps {
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	return &Apps{httpClient: httpClient, HomepageURL: DefaultHomepageURL, now: time.Now}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          10,
		},
	}
}

func (a *Apps) apiBase(c *ConfigURL) string {
	if a.APIBaseURL != "" {
		return strings.TrimRight(a.APIBaseURL, "/")
	}
	return c.APIBaseURL()
}

// apiError is a non-2xx answer from the REST API.
type apiError struct {
	Status  int
	Message string
	// Scopes is the X-OAuth-Scopes header, only sent for classic PATs and OAuth tokens.
	Scopes    string
	HasScopes bool
	SSO       string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("GitHub API returned %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("GitHub API returned %d", e.Status)
}

type response struct {
	header http.Header
}

// do sends a request and decodes a JSON body into out. A non-2xx status becomes *apiError.
func (a *Apps) do(ctx context.Context, method, rawURL, auth string, body io.Reader, out any) (response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "oakestra-github-runner")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("calling GitHub: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return response{}, fmt.Errorf("reading GitHub response: %w", err)
	}
	r := response{header: resp.Header}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &apiError{Status: resp.StatusCode, SSO: resp.Header.Get("X-GitHub-SSO")}
		e.Scopes, e.HasScopes = headerValue(resp.Header, "X-OAuth-Scopes")
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &m) == nil {
			e.Message = truncate(m.Message, maxEchoedMessage)
		}
		return r, e
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return r, fmt.Errorf("decoding GitHub response: %w", err)
		}
	}
	return r, nil
}

// maxEchoedMessage bounds how much of an upstream error message ends up in our own errors,
// which reach the dashboard. The upstream can be any host an admin typed in.
const maxEchoedMessage = 300

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func headerValue(h http.Header, key string) (string, bool) {
	v, ok := h[http.CanonicalHeaderKey(key)]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

func statusOf(err error) int {
	var e *apiError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// ownerType asks the API whether owner is an "Organization" or a "User". The endpoint works
// without authentication, which matters because the manifest flow runs before we have any
// credential.
func (a *Apps) ownerType(ctx context.Context, c ConfigURL) (string, error) {
	var u struct {
		Type string `json:"type"`
	}
	_, err := a.do(ctx, http.MethodGet, a.apiBase(&c)+"/users/"+url.PathEscape(c.Owner), "", nil, &u)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return "", fmt.Errorf("GitHub account %q does not exist", c.Owner)
		}
		return "", fmt.Errorf("looking up GitHub account %q: %w", c.Owner, err)
	}
	return u.Type, nil
}

// Manifest builds the App manifest. The App is private, has its webhook switched off (the
// controller long-polls, nothing calls us), and asks only for what scale sets need: managing
// self-hosted runners at the level of the config URL.
func (a *Apps) Manifest(ctx context.Context, name, configURL, redirectURL, state string) (core.AppManifest, error) {
	c, err := ParseConfigURL(configURL)
	if err != nil {
		return core.AppManifest{}, err
	}
	if state == "" {
		return core.AppManifest{}, errors.New("state must not be empty")
	}
	ru, err := url.Parse(redirectURL)
	if err != nil || (ru.Scheme != "https" && ru.Scheme != "http") || ru.Host == "" {
		return core.AppManifest{}, fmt.Errorf("redirect URL %q must be an absolute http(s) URL", redirectURL)
	}
	// GitHub limits App names to 34 characters and rejects the manifest otherwise, after the
	// user already landed on github.com. Failing here gives a better message.
	if len([]rune(name)) > 34 {
		return core.AppManifest{}, fmt.Errorf("App name %q is longer than 34 characters", name)
	}

	typ, err := a.ownerType(ctx, c)
	if err != nil {
		return core.AppManifest{}, err
	}
	isOrg := typ == "Organization"
	if !c.IsRepo() && !isOrg {
		return core.AppManifest{}, fmt.Errorf("%q is a personal account; organization-level runners need an organization, use a repository URL instead", c.Owner)
	}

	perms := map[string]string{"metadata": "read"}
	if c.IsRepo() {
		perms["administration"] = "write"
	} else {
		perms["organization_self_hosted_runners"] = "write"
	}
	manifest := map[string]any{
		"url": a.HomepageURL,
		// GitHub requires hook_attributes.url whenever hook_attributes is present, and without
		// hook_attributes the form defaults to an active webhook. An inactive hook pointing at
		// the dashboard is the documented way to say "no webhook"; it is never called.
		"hook_attributes": map[string]any{
			"url":    redirectURL,
			"active": false,
		},
		"redirect_url":        redirectURL,
		"public":              false,
		"default_permissions": perms,
		"default_events":      []string{},
		"description":         "Runs GitHub Actions jobs on an Oakestra cluster through runner scale sets.",
	}
	if name != "" {
		manifest["name"] = name
	}

	post := c.WebBaseURL() + "/settings/apps/new"
	if isOrg {
		post = c.WebBaseURL() + "/organizations/" + url.PathEscape(c.Owner) + "/settings/apps/new"
	}
	post += "?state=" + url.QueryEscape(state)
	return core.AppManifest{PostURL: post, Manifest: manifest, State: state}, nil
}

var manifestCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// ExchangeManifestCode converts the code GitHub appended to the redirect. Codes are single-use
// and expire one hour after the manifest was submitted.
func (a *Apps) ExchangeManifestCode(ctx context.Context, configURL, code string) (core.AppCredentials, error) {
	c, err := ParseConfigURL(configURL)
	if err != nil {
		return core.AppCredentials{}, err
	}
	if !manifestCodePattern.MatchString(code) {
		return core.AppCredentials{}, errors.New("invalid manifest code")
	}
	var out struct {
		ID       int64  `json:"id"`
		Slug     string `json:"slug"`
		ClientID string `json:"client_id"`
		HTMLURL  string `json:"html_url"`
		PEM      string `json:"pem"`
	}
	_, err = a.do(ctx, http.MethodPost, a.apiBase(&c)+"/app-manifests/"+code+"/conversions", "", nil, &out)
	if err != nil {
		switch statusOf(err) {
		case http.StatusNotFound, http.StatusUnprocessableEntity:
			return core.AppCredentials{}, fmt.Errorf("GitHub did not accept the manifest code; it is single-use and expires after one hour, start the App creation again: %w", err)
		}
		return core.AppCredentials{}, fmt.Errorf("exchanging manifest code: %w", err)
	}
	if out.ID == 0 || out.PEM == "" {
		return core.AppCredentials{}, errors.New("GitHub returned an incomplete App registration")
	}
	return core.AppCredentials{AppID: out.ID, Slug: out.Slug, ClientID: out.ClientID, HTMLURL: out.HTMLURL, PrivateKey: out.PEM}, nil
}

// appJWT signs the short-lived JWT that authenticates as the App itself. iat is backdated a
// minute against clock drift, and exp stays under GitHub's ten-minute limit.
func (a *Apps) appJWT(appID int64, privateKeyPEM string) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", errors.New("the App private key is not a valid RSA PEM key")
	}
	now := a.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
	})
	return tok.SignedString(key)
}

var linkNextPattern = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// ListInstallations returns every installation of the App, following Link pagination.
func (a *Apps) ListInstallations(ctx context.Context, configURL string, appID int64, privateKeyPEM string) ([]core.Installation, error) {
	c, err := ParseConfigURL(configURL)
	if err != nil {
		return nil, err
	}
	if appID == 0 {
		return nil, errors.New("App id is missing")
	}
	token, err := a.appJWT(appID, privateKeyPEM)
	if err != nil {
		return nil, err
	}
	next := a.apiBase(&c) + "/app/installations?per_page=100"
	out := []core.Installation{}
	for page := 0; next != "" && page < 100; page++ {
		var items []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
			TargetType string `json:"target_type"`
			HTMLURL    string `json:"html_url"`
		}
		resp, err := a.do(ctx, http.MethodGet, next, "Bearer "+token, nil, &items)
		if err != nil {
			if statusOf(err) == http.StatusUnauthorized {
				return nil, fmt.Errorf("GitHub rejected the App credentials (wrong App id or private key, or the App was deleted): %w", err)
			}
			return nil, fmt.Errorf("listing App installations: %w", err)
		}
		for _, it := range items {
			out = append(out, core.Installation{ID: it.ID, Account: it.Account.Login, TargetType: it.TargetType, HTMLURL: it.HTMLURL})
		}
		next = ""
		if m := linkNextPattern.FindStringSubmatch(resp.header.Get("Link")); m != nil {
			// The App JWT goes with every page, so only follow links to the API we started on.
			if !sameOrigin(m[1], a.apiBase(&c)) {
				return nil, errors.New("GitHub returned a pagination link to another host")
			}
			next = m[1]
		}
	}
	return out, nil
}

// ValidatePAT checks that token can reach the org or repo and can manage its self-hosted
// runners, which is what creating a scale set and generating JIT configs needs.
func (a *Apps) ValidatePAT(ctx context.Context, configURL, token string) error {
	c, err := ParseConfigURL(configURL)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("the token is empty")
	}
	base := a.apiBase(&c)
	auth := "Bearer " + token

	target, runners, scope := "/orgs/"+url.PathEscape(c.Owner), "/orgs/"+url.PathEscape(c.Owner)+"/actions/runners?per_page=1", "admin:org"
	what := fmt.Sprintf("organization %q", c.Owner)
	if c.IsRepo() {
		target = "/repos/" + url.PathEscape(c.Owner) + "/" + url.PathEscape(c.Repo)
		runners = target + "/actions/runners?per_page=1"
		scope = "repo"
		what = fmt.Sprintf("repository %s/%s", c.Owner, c.Repo)
	}

	if _, err := a.do(ctx, http.MethodGet, base+target, auth, nil, nil); err != nil {
		var e *apiError
		if !errors.As(err, &e) {
			return err
		}
		switch {
		case e.Status == http.StatusUnauthorized:
			return errors.New("GitHub rejected the token: it is wrong, expired or revoked")
		case e.SSO != "":
			return fmt.Errorf("the token must be authorized for SAML single sign-on to access %s; authorize it in GitHub's token settings", what)
		case e.Status == http.StatusNotFound:
			return fmt.Errorf("%s does not exist or the token has no access to it", what)
		case e.Status == http.StatusForbidden:
			return fmt.Errorf("the token is not allowed to access %s: %s", what, e.Message)
		}
		return fmt.Errorf("checking access to %s: %w", what, err)
	}

	if _, err := a.do(ctx, http.MethodGet, base+runners, auth, nil, nil); err != nil {
		var e *apiError
		if !errors.As(err, &e) {
			return err
		}
		if e.Status == http.StatusForbidden || e.Status == http.StatusNotFound {
			// Classic PATs report their scopes, fine-grained tokens do not.
			if e.HasScopes && !hasScope(e.Scopes, scope) {
				return fmt.Errorf("the token can see %s but lacks the %q scope needed to manage self-hosted runners", what, scope)
			}
			if c.IsRepo() {
				return fmt.Errorf("the token can see %s but cannot manage its self-hosted runners; it needs admin access (classic: \"repo\" scope, fine-grained: Administration read and write)", what)
			}
			return fmt.Errorf("the token can see %s but cannot manage its self-hosted runners; it needs organization owner rights (classic: \"admin:org\" scope, fine-grained: Self-hosted runners read and write)", what)
		}
		return fmt.Errorf("checking runner access on %s: %w", what, err)
	}
	return nil
}

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host)
}

func hasScope(header, want string) bool {
	for s := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}
