package api

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// AdminRole is the role Oakestra gives platform admins.
const AdminRole = "Admin"

var (
	ErrInvalidToken = errors.New("invalid token")
	// ErrNoKey means no verification key has been loaded yet. The API answers 503 for it.
	ErrNoKey = errors.New("no verification key loaded")
)

// Claims are the parts of an Oakestra access token the API uses.
type Claims struct {
	Subject      string
	Roles        []string
	Organization string
}

func (c Claims) IsAdmin() bool { return slices.Contains(c.Roles, AdminRole) }

// TokenVerifier validates the bearer token of a request. Implementations return an error
// wrapping ErrNoKey while they cannot verify anything yet and ErrInvalidToken for bad tokens.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (Claims, error)
}

// JWTOptions selects where the Oakestra public key comes from, in order of precedence:
// PublicKeyPEM, PublicKeyURL, then the jwt_generator at GeneratorURL.
type JWTOptions struct {
	PublicKeyPEM string
	// PublicKeyURL answers like jwt_generator's GET /key: {"public_key": "<PEM>"}.
	PublicKeyURL string
	// GeneratorURL is the base URL of jwt_generator, the key lives at <GeneratorURL>/key.
	GeneratorURL string

	Client *http.Client
	Logger *slog.Logger
	// RetryInterval is the pause between failed initial key loads (default 5s).
	RetryInterval time.Duration
	// MinRefetchInterval limits how often a failing token may trigger a refetch of the
	// generator key (default 10s).
	MinRefetchInterval time.Duration
	// Leeway is the accepted clock skew (default 30s).
	Leeway time.Duration
}

// JWTVerifier verifies RS256 access tokens issued by Oakestra's jwt_generator.
type JWTVerifier struct {
	key     atomic.Pointer[rsa.PublicKey]
	url     string
	refresh bool
	static  bool
	client  *http.Client
	log     *slog.Logger
	retry   time.Duration
	minGap  time.Duration
	leeway  time.Duration

	mu          sync.Mutex
	lastRefetch time.Time
}

// NewJWTVerifier fails only when a configured static key does not parse. Call Start to load
// keys that come from the network.
func NewJWTVerifier(o JWTOptions) (*JWTVerifier, error) {
	v := &JWTVerifier{
		client: o.Client,
		log:    o.Logger,
		retry:  o.RetryInterval,
		minGap: o.MinRefetchInterval,
		leeway: o.Leeway,
	}
	if v.client == nil {
		v.client = &http.Client{Timeout: 5 * time.Second}
	}
	if v.log == nil {
		v.log = slog.Default()
	}
	if v.retry <= 0 {
		v.retry = 5 * time.Second
	}
	if v.minGap <= 0 {
		v.minGap = 10 * time.Second
	}
	if v.leeway <= 0 {
		v.leeway = 30 * time.Second
	}

	switch {
	case strings.TrimSpace(o.PublicKeyPEM) != "":
		pemKey := o.PublicKeyPEM
		if !strings.Contains(pemKey, "\n") {
			pemKey = strings.ReplaceAll(pemKey, `\n`, "\n")
		}
		key, err := parsePublicKeyPEM([]byte(pemKey))
		if err != nil {
			return nil, fmt.Errorf("JWT_PUBLIC_KEY: %w", err)
		}
		v.key.Store(key)
		v.static = true
	case strings.TrimSpace(o.PublicKeyURL) != "":
		// A key fetched from another host over plain HTTP is trusted once. Refetching on every
		// bad token would let anyone on the path trigger a fetch and answer it with their key.
		v.url = strings.TrimSpace(o.PublicKeyURL)
	default:
		// jwt_generator makes a new key pair on every restart, so tokens signed by a newer key
		// are expected and a refetch is the right reaction. It sits on the local network.
		v.url = strings.TrimRight(o.GeneratorURL, "/") + "/key"
		v.refresh = true
	}
	return v, nil
}

// SetKey installs a key directly.
func (v *JWTVerifier) SetKey(k *rsa.PublicKey) { v.key.Store(k) }

// Start loads the key in the background, retrying until it succeeds or ctx ends, because
// jwt_generator may come up after this addon. It does nothing for a static key.
func (v *JWTVerifier) Start(ctx context.Context) {
	if v.static {
		return
	}
	go func() {
		for {
			key, err := v.fetch(ctx)
			if err == nil {
				v.key.Store(key)
				v.log.Info("JWT public key loaded")
				return
			}
			v.log.Warn("fetching JWT public key failed, retrying", "url", v.url, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(v.retry):
			}
		}
	}()
}

func (v *JWTVerifier) fetch(ctx context.Context) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	var body struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return parsePublicKeyPEM([]byte(body.PublicKey))
}

type tokenClaims struct {
	jwt.RegisteredClaims
	Type         string   `json:"type"`
	Roles        []string `json:"roles"`
	Organization string   `json:"organization"`
}

func (v *JWTVerifier) parse(raw string, key *rsa.PublicKey) (Claims, error) {
	parser := jwt.NewParser(
		// Pinning the method is what stops alg=none and HS256 signed with the public key.
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(v.leeway),
	)
	var tc tokenClaims
	if _, err := parser.ParseWithClaims(raw, &tc, func(*jwt.Token) (any, error) { return key, nil }); err != nil {
		return Claims{}, err
	}
	if tc.Type != "access" {
		return Claims{}, errors.New("only access tokens are allowed")
	}
	if tc.Subject == "" {
		return Claims{}, errors.New("missing subject")
	}
	return Claims{Subject: tc.Subject, Roles: tc.Roles, Organization: tc.Organization}, nil
}

func (v *JWTVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	key := v.key.Load()
	if key == nil {
		return Claims{}, ErrNoKey
	}
	c, err := v.parse(raw, key)
	if err != nil && v.refresh && errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		if fresh := v.refetch(ctx, key); fresh != nil {
			c, err = v.parse(raw, fresh)
		}
	}
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return c, nil
}

// refetch returns a key different from stale, or nil when the generator was asked too recently,
// is unreachable or still serves the same key.
func (v *JWTVerifier) refetch(ctx context.Context, stale *rsa.PublicKey) *rsa.PublicKey {
	v.mu.Lock()
	defer v.mu.Unlock()
	// Another request may have refreshed the key while this one waited for the lock.
	if cur := v.key.Load(); cur != stale {
		return cur
	}
	if time.Since(v.lastRefetch) < v.minGap {
		return nil
	}
	v.lastRefetch = time.Now()
	key, err := v.fetch(ctx)
	if err != nil {
		v.log.Warn("refetching JWT public key failed", "error", err)
		return nil
	}
	if key.Equal(stale) {
		return nil
	}
	v.key.Store(key)
	v.log.Info("JWT public key rotated")
	return key
}

func parsePublicKeyPEM(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("expected an RSA key, got %T", key)
		}
		return rsaKey, nil
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unexpected PEM block %q", block.Type)
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// claimsKey is the gin context key authenticate stores the verified Claims under.
const claimsKey = "ghr.claims"

func claimsFrom(c *gin.Context) (Claims, bool) {
	v, ok := c.Get(claimsKey)
	if !ok {
		return Claims{}, false
	}
	claims, ok := v.(Claims)
	return claims, ok
}

// authenticate guards the /api routes: any valid token may read, everything else needs Admin.
func (s *server) authenticate(c *gin.Context) {
	if s.verifier == nil {
		c.Set(claimsKey, Claims{Subject: "anonymous", Roles: []string{AdminRole}})
		return
	}
	raw, ok := bearerToken(c.Request)
	if !ok {
		c.Header("WWW-Authenticate", "Bearer")
		writeError(c, http.StatusUnauthorized, codeUnauthorized, "missing bearer token")
		c.Abort()
		return
	}
	claims, err := s.verifier.Verify(c.Request.Context(), raw)
	switch {
	case errors.Is(err, ErrNoKey):
		c.Header("Retry-After", "5")
		writeError(c, http.StatusServiceUnavailable, codeUnavailable, "token verification key is not available yet")
		c.Abort()
		return
	case err != nil:
		c.Header("WWW-Authenticate", "Bearer")
		writeError(c, http.StatusUnauthorized, codeUnauthorized, "invalid or expired token")
		c.Abort()
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && !claims.IsAdmin() {
		writeError(c, http.StatusForbidden, codeForbidden, "the Admin role is required")
		c.Abort()
		return
	}
	c.Set(claimsKey, claims)
}
