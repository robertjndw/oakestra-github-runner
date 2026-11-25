package oakestra

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// Access tokens live 10 minutes and refresh tokens about 30 days. These are only fallbacks for
// tokens whose exp claim cannot be read.
const (
	fallbackAccessTTL  = 10 * time.Minute
	fallbackRefreshTTL = 30 * 24 * time.Hour
	// refreshMargin renews the access token this long before it expires, so a slow request does
	// not race the expiry.
	refreshMargin = 2 * time.Minute
)

// session is what a request needs from the login state.
type session struct {
	token string
	// org is the organization id from the JWT; the SLA's customerID must be exactly this.
	org  string
	user string
}

type tokenState struct {
	access     string
	accessExp  time.Time
	refresh    string
	refreshExp time.Time
	org, user  string
}

// authCall is one login or refresh in flight. Concurrent callers share it instead of each
// hitting the system manager.
type authCall struct {
	done chan struct{}
	sess session
	err  error
}

// session returns a usable access token. stale is the token a request was just rejected with
// (or ""). A cached token equal to stale is never reused and forces a full login, because the
// server rejected a token our clock still considered valid (restart with a new secret, skew).
func (c *Client) session(ctx context.Context, stale string) (session, error) {
	c.mu.Lock()
	st := c.tokens
	if st.access != "" && st.access != stale && c.now().Before(st.accessExp.Add(-refreshMargin)) {
		s := session{token: st.access, org: st.org, user: st.user}
		c.mu.Unlock()
		return s, nil
	}
	call := c.inflight
	if call == nil {
		call = &authCall{done: make(chan struct{})}
		c.inflight = call
		refresh := ""
		if stale == "" && st.refresh != "" && c.now().Before(st.refreshExp.Add(-refreshMargin)) {
			refresh = st.refresh
		}
		// Detached from the first caller's context: the others wait on this result and must not
		// fail because the first one gave up.
		go c.authenticate(context.WithoutCancel(ctx), call, refresh, c.gen)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.sess, call.err
	case <-ctx.Done():
		return session{}, ctx.Err()
	}
}

func (c *Client) authenticate(ctx context.Context, call *authCall, refresh string, gen uint64) {
	var (
		resp loginResponse
		err  error
	)
	if refresh != "" {
		resp, err = c.refreshAccess(ctx, refresh)
		if err != nil {
			c.log.Debug("oakestra: token refresh failed, logging in again", "err", err)
		} else {
			resp.RefreshToken = refresh
		}
	}
	if refresh == "" || err != nil {
		resp, err = c.login(ctx)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight == call {
		c.inflight = nil
	}
	if err != nil {
		if gen == c.gen {
			c.tokens = tokenState{}
		}
		call.err = err
		close(call.done)
		return
	}
	now := c.now()
	st := tokenState{access: resp.Token}
	var claims tokenClaims
	claims, st.accessExp = parseToken(resp.Token, now, fallbackAccessTTL)
	st.org, st.user = claims.org, claims.user
	if resp.RefreshToken != "" {
		st.refresh = resp.RefreshToken
		if resp.RefreshToken == refresh {
			// The refresh endpoint returns no new refresh token, so keep its known expiry.
			st.refreshExp = c.tokens.refreshExp
		} else {
			_, st.refreshExp = parseToken(resp.RefreshToken, now, fallbackRefreshTTL)
		}
	}
	// A result that belongs to credentials an admin has since replaced is handed to the waiters
	// but not cached.
	if gen == c.gen {
		c.tokens = st
	}
	call.sess = session{token: resp.Token, org: st.org, user: st.user}
	close(call.done)
}

func (c *Client) login(ctx context.Context) (loginResponse, error) {
	if c.cfg.Credentials == nil {
		return loginResponse{}, core.ErrNoCredentials
	}
	creds, _, err := c.cfg.Credentials(ctx)
	if err != nil {
		return loginResponse{}, err
	}
	body := loginRequest{Username: creds.Username, Password: creds.Password, OrganizationName: creds.Organization}
	var resp loginResponse
	if err := c.send(ctx, "login", http.MethodPost, "/api/auth/login", "", body, &resp); err != nil {
		return loginResponse{}, err
	}
	if resp.Token == "" {
		// Some credential problems come back as 200 with an empty object.
		return loginResponse{}, &APIError{Op: "login", Status: http.StatusUnauthorized, Message: "no token in login response"}
	}
	return resp, nil
}

func (c *Client) refreshAccess(ctx context.Context, refresh string) (loginResponse, error) {
	var resp loginResponse
	if err := c.send(ctx, "refresh token", http.MethodPost, "/api/auth/refresh", refresh, nil, &resp); err != nil {
		return loginResponse{}, err
	}
	if resp.Token == "" {
		return loginResponse{}, errors.New("oakestra: refresh token: no token in response")
	}
	return resp, nil
}

type tokenClaims struct {
	org, user string
}

// parseToken reads exp, organization and sub from a JWT without verifying it. The server does
// the verifying; we only need to know when to renew and which organization to put in the SLA.
func parseToken(tok string, now time.Time, fallback time.Duration) (tokenClaims, time.Time) {
	var mc jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(tok, &mc); err != nil {
		return tokenClaims{}, now.Add(fallback)
	}
	var out tokenClaims
	out.org, _ = mc["organization"].(string)
	out.user, _ = mc.GetSubject()
	exp := now.Add(fallback)
	if t, err := mc.GetExpirationTime(); err == nil && t != nil {
		exp = t.Time
	}
	return out, exp
}

func authErr(op string, err error) error {
	return fmt.Errorf("oakestra: %s: authenticate: %w", op, err)
}
