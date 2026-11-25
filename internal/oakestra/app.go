package oakestra

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// withApp runs fn with the runner application's id. If fn fails with 404 the application was
// deleted behind our back, so the cached id is dropped and fn runs once more against a
// recreated one.
func (c *Client) withApp(ctx context.Context, fn func(sess session, appID string) error) error {
	for attempt := 0; ; attempt++ {
		sess, err := c.session(ctx, "")
		if err != nil {
			return authErr("application", err)
		}
		appID, err := c.ensureApp(ctx, sess)
		if err != nil {
			return err
		}
		err = fn(sess, appID)
		if err != nil && attempt == 0 && statusOf(err) == http.StatusNotFound {
			c.forgetApp(appID)
			continue
		}
		return err
	}
}

func (c *Client) forgetApp(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appID == id {
		c.appID = ""
	}
}

func (c *Client) cachedApp() (string, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appID, c.gen
}

func (c *Client) ensureApp(ctx context.Context, sess session) (string, error) {
	if !nameRE.MatchString(c.cfg.AppName) || !nameRE.MatchString(c.cfg.Namespace) {
		return "", fmt.Errorf("oakestra: application name %q and namespace %q must match %s", c.cfg.AppName, c.cfg.Namespace, nameRE)
	}
	if id, _ := c.cachedApp(); id != "" {
		return id, nil
	}
	c.appCreate.Lock()
	defer c.appCreate.Unlock()
	id, gen := c.cachedApp()
	if id != "" {
		return id, nil
	}
	if sess.org == "" || sess.user == "" {
		return "", errors.New("oakestra: access token has no organization or subject claim")
	}
	id, err := c.createApp(ctx, sess)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.gen == gen {
		c.appID = id
	}
	c.mu.Unlock()
	return id, nil
}

func (c *Client) createApp(ctx context.Context, sess session) (string, error) {
	body := sla{
		SLAVersion: slaVersion,
		CustomerID: sess.org,
		Applications: []slaApplication{{
			ApplicationName:      c.cfg.AppName,
			ApplicationNamespace: c.cfg.Namespace,
			ApplicationDesc:      "GitHub Actions runners managed by oakestra-github-runner",
			Microservices:        []slaMicroservice{},
		}},
	}
	var apps []applicationInfo
	err := c.call(ctx, "create application", http.MethodPost, "/api/application/", body, &apps)
	if statusOf(err) == http.StatusConflict {
		// Exists already, for example created by an earlier run of the backend.
		apps, err = c.userApps(ctx, sess.user)
	}
	if err != nil {
		return "", err
	}
	for _, a := range apps {
		if a.ApplicationName == c.cfg.AppName && a.ApplicationNamespace == c.cfg.Namespace && a.ApplicationID != "" {
			return a.ApplicationID, nil
		}
	}
	return "", fmt.Errorf("oakestra: application %s.%s missing from the response", c.cfg.AppName, c.cfg.Namespace)
}

// userApps lists the user's applications. The endpoint takes the user's document id and
// answers 401 for anything else, including the user name, so the id is looked up first.
func (c *Client) userApps(ctx context.Context, username string) ([]applicationInfo, error) {
	var user struct {
		ID flexString `json:"_id"`
	}
	if err := c.call(ctx, "get user", http.MethodGet, "/api/user/"+url.PathEscape(username), nil, &user); err != nil {
		return nil, err
	}
	if user.ID == "" {
		return nil, fmt.Errorf("oakestra: get user %s: no _id in response", username)
	}
	var apps []applicationInfo
	if err := c.call(ctx, "list applications", http.MethodGet, "/api/applications/"+url.PathEscape(string(user.ID)), nil, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}
