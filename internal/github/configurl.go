// Package github talks to the GitHub REST API for App setup and PAT validation, and builds the
// runner scale set clients the controller uses.
package github

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ConfigURL is a parsed scale set registration URL: an organization or a repository.
type ConfigURL struct {
	Scheme string
	Host   string
	Owner  string
	// Repo is empty for organization URLs.
	Repo string
}

var (
	// GitHub logins allow alphanumerics and single hyphens. We are a bit more lenient (no rule
	// about consecutive hyphens) and let GitHub reject the rest.
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ErrInvalidConfigURL is wrapped by every ParseConfigURL error.
var ErrInvalidConfigURL = errors.New("invalid GitHub URL")

// ParseConfigURL parses https://<host>/<org> or https://<host>/<owner>/<repo>. Enterprise URLs
// (/enterprises/<name>) are rejected: this addon registers scale sets only at organization or
// repository level.
func ParseConfigURL(raw string) (ConfigURL, error) {
	invalid := func(reason string) (ConfigURL, error) {
		shown := raw
		if len(shown) > 200 {
			shown = shown[:200] + "..."
		}
		return ConfigURL{}, fmt.Errorf("%w %q: %s", ErrInvalidConfigURL, shown, reason)
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return invalid("it is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return invalid(err.Error())
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return invalid("it must start with https://")
	}
	if u.Host == "" {
		return invalid("it has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalid("it must not contain credentials, a query or a fragment")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	c := ConfigURL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}
	switch {
	case len(parts) == 1 && parts[0] != "":
		c.Owner = parts[0]
	case len(parts) == 2:
		if strings.EqualFold(parts[0], "enterprises") {
			return invalid("enterprise-level runners are not supported, use an organization or repository URL")
		}
		c.Owner, c.Repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	default:
		return invalid("expected https://github.com/<org> or https://github.com/<owner>/<repo>")
	}
	if !ownerPattern.MatchString(c.Owner) {
		return invalid(fmt.Sprintf("%q is not a valid GitHub account name", c.Owner))
	}
	if c.Repo != "" && (!repoPattern.MatchString(c.Repo) || c.Repo == "." || c.Repo == "..") {
		return invalid(fmt.Sprintf("%q is not a valid repository name", c.Repo))
	}
	return c, nil
}

// IsRepo reports whether the URL points at a repository rather than an organization.
func (c ConfigURL) IsRepo() bool { return c.Repo != "" }

// String returns the normalized URL.
func (c ConfigURL) String() string {
	if c.IsRepo() {
		return fmt.Sprintf("%s://%s/%s/%s", c.Scheme, c.Host, c.Owner, c.Repo)
	}
	return fmt.Sprintf("%s://%s/%s", c.Scheme, c.Host, c.Owner)
}

// WebBaseURL is the browser-facing base, e.g. https://github.com.
func (c ConfigURL) WebBaseURL() string {
	return c.Scheme + "://" + c.Host
}

// APIBaseURL returns the REST API base for the host. The hosted rules mirror the scaleset
// client's own (github.com, www.github.com and *.ghe.com use api.<host>), so this package and
// the scale set client never disagree about which server a connection lives on.
func (c ConfigURL) APIBaseURL() string {
	switch {
	case c.Host == "github.com" || c.Host == "www.github.com":
		return "https://api.github.com"
	case strings.HasSuffix(c.Host, ".ghe.com"):
		return "https://api." + c.Host
	default:
		return c.Scheme + "://" + c.Host + "/api/v3"
	}
}
