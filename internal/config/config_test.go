package config

import (
	"log/slog"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":80" || c.DataDir != "/data" || c.Auth != AuthJWT ||
		c.RunnerImage != "ghcr.io/actions/actions-runner:latest" || c.LogLevel != slog.LevelInfo ||
		c.OakestraAPIURL != "http://system_manager:10000" || c.JWTGeneratorURL != "http://jwt_generator:10011" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.HasEnvCredentials() {
		t.Fatal("no credentials expected")
	}
}

func TestOverrides(t *testing.T) {
	c, err := load(env(map[string]string{
		"GHR_LISTEN":         ":8080",
		"GHR_AUTH":           "OFF",
		"GHR_LOG_LEVEL":      "debug",
		"OAKESTRA_API_URL":   "http://sm:1/",
		"OAKESTRA_USERNAME":  "svc",
		"OAKESTRA_PASSWORD":  "pw",
		"JWT_PUBLIC_KEY":     `-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----`,
		"JWT_PUBLIC_KEY_URL": "https://root/key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.Auth != AuthOff || c.LogLevel != slog.LevelDebug || c.OakestraAPIURL != "http://sm:1" {
		t.Fatalf("unexpected: %+v", c)
	}
	if !c.HasEnvCredentials() {
		t.Fatal("expected env credentials")
	}
	if !strings.Contains(c.JWTPublicKey, "\nabc\n") {
		t.Fatalf("escaped newlines not expanded: %q", c.JWTPublicKey)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"GHR_AUTH":                {"GHR_AUTH": "maybe"},
		"GHR_LOG_LEVEL":           {"GHR_LOG_LEVEL": "loud"},
		"set together":            {"OAKESTRA_USERNAME": "svc"},
		"OAKESTRA_API_URL":        {"OAKESTRA_API_URL": "system_manager:10000"},
		"JWT_PUBLIC_KEY_URL":      {"JWT_PUBLIC_KEY_URL": "ftp://x"},
		"set together (password)": {"OAKESTRA_PASSWORD": "pw"},
	}
	for want, m := range cases {
		_, err := load(env(m))
		if err == nil || !strings.Contains(err.Error(), strings.Split(want, " (")[0]) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}
