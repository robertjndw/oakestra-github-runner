// Package config reads the addon's configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// Auth modes for GHR_AUTH.
const (
	AuthJWT = "jwt"
	// AuthOff disables API authentication. Only meant for local development.
	AuthOff = "off"
)

type Config struct {
	Listen        string
	DataDir       string
	EncryptionKey string
	// Auth is AuthJWT or AuthOff.
	Auth        string
	RunnerImage string
	LogLevel    slog.Level

	OakestraAPIURL       string
	OakestraUsername     string
	OakestraPassword     string
	OakestraOrganization string

	// JWTPublicKey is a PEM key with real newlines, already unescaped from "\n" sequences.
	JWTPublicKey    string
	JWTPublicKeyURL string
	JWTGeneratorURL string
}

// HasEnvCredentials reports whether the Oakestra service user comes from the environment. In
// that case the UI must not offer to change it.
func (c Config) HasEnvCredentials() bool {
	return c.OakestraUsername != "" && c.OakestraPassword != ""
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(get func(string) string) (Config, error) {
	str := func(key, def string) string {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Listen:               str("GHR_LISTEN", ":80"),
		DataDir:              str("GHR_DATA_DIR", "/data"),
		EncryptionKey:        strings.TrimSpace(get("GHR_ENCRYPTION_KEY")),
		Auth:                 strings.ToLower(str("GHR_AUTH", AuthJWT)),
		RunnerImage:          str("GHR_RUNNER_IMAGE", "ghcr.io/actions/actions-runner:latest"),
		OakestraAPIURL:       strings.TrimRight(str("OAKESTRA_API_URL", "http://system_manager:10000"), "/"),
		OakestraUsername:     strings.TrimSpace(get("OAKESTRA_USERNAME")),
		OakestraPassword:     get("OAKESTRA_PASSWORD"),
		OakestraOrganization: strings.TrimSpace(get("OAKESTRA_ORGANIZATION")),
		JWTPublicKeyURL:      strings.TrimSpace(get("JWT_PUBLIC_KEY_URL")),
		JWTGeneratorURL:      strings.TrimRight(str("JWT_GENERATOR_URL", "http://jwt_generator:10011"), "/"),
	}

	var errs []error
	if c.Auth != AuthJWT && c.Auth != AuthOff {
		errs = append(errs, fmt.Errorf("GHR_AUTH must be %q or %q, got %q", AuthJWT, AuthOff, c.Auth))
	}
	if err := c.LogLevel.UnmarshalText([]byte(str("GHR_LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("GHR_LOG_LEVEL: %w", err))
	}
	if (c.OakestraUsername == "") != (c.OakestraPassword == "") {
		errs = append(errs, errors.New("OAKESTRA_USERNAME and OAKESTRA_PASSWORD must be set together"))
	}
	for _, u := range []struct{ name, value string }{
		{"OAKESTRA_API_URL", c.OakestraAPIURL},
		{"JWT_GENERATOR_URL", c.JWTGeneratorURL},
	} {
		if err := checkHTTPURL(u.value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u.name, err))
		}
	}
	if c.JWTPublicKeyURL != "" {
		if err := checkHTTPURL(c.JWTPublicKeyURL); err != nil {
			errs = append(errs, fmt.Errorf("JWT_PUBLIC_KEY_URL: %w", err))
		}
	}

	if key := strings.TrimSpace(get("JWT_PUBLIC_KEY")); key != "" {
		// Compose files and secrets managers often flatten the PEM onto one line.
		if !strings.Contains(key, "\n") {
			key = strings.ReplaceAll(key, `\n`, "\n")
		}
		c.JWTPublicKey = key
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an absolute http(s) URL", raw)
	}
	return nil
}
