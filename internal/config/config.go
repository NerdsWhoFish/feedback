package config

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address, BaseURL, DatabaseURL, Environment                  string
	OIDCIssuer, OIDCClientID, OIDCClientSecret, OIDCRedirectURL string
	OIDCAuthURL, OIDCTokenURL, OIDCUserInfoURL, OIDCJWKSURL     string
	AdminEmails                                                 map[string]bool
	CookieSecure                                                bool
	SessionTTL                                                  time.Duration
	KeyRPM                                                      int
	WebhookKey                                                  []byte
	FaroURL                                                     string
}

func Load() (Config, error) {
	c := Config{Address: env("HTTP_ADDR", ":8080"), BaseURL: env("BASE_URL", "http://localhost:8080"), DatabaseURL: os.Getenv("DATABASE_URL"), Environment: env("ENVIRONMENT", "local"),
		OIDCIssuer: os.Getenv("OIDC_ISSUER"), OIDCClientID: os.Getenv("OIDC_CLIENT_ID"), OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), OIDCRedirectURL: os.Getenv("OIDC_REDIRECT_URL"),
		OIDCAuthURL: os.Getenv("OIDC_AUTH_URL"), OIDCTokenURL: os.Getenv("OIDC_TOKEN_URL"), OIDCUserInfoURL: os.Getenv("OIDC_USERINFO_URL"), OIDCJWKSURL: os.Getenv("OIDC_JWKS_URL"),
		AdminEmails: map[string]bool{}, SessionTTL: 12 * time.Hour, KeyRPM: 120, FaroURL: os.Getenv("FARO_COLLECTOR_URL")}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("BASE_URL must be an origin")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	c.CookieSecure = u.Scheme == "https"
	if !c.CookieSecure && (u.Scheme != "http" || !loopback(u.Hostname()) || c.Environment != "local") {
		return c, errors.New("BASE_URL requires HTTPS outside local loopback development")
	}
	if c.Environment != "local" && c.Environment != "production" && c.Environment != "staging" && c.Environment != "test" {
		return c, errors.New("invalid ENVIRONMENT")
	}
	for _, s := range strings.Split(os.Getenv("ADMIN_EMAILS"), ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			c.AdminEmails[s] = true
		}
	}
	if c.OIDCRedirectURL == "" {
		c.OIDCRedirectURL = c.BaseURL + "/auth/callback"
	}
	if c.OIDCRedirectURL != c.BaseURL+"/auth/callback" {
		return c, errors.New("OIDC_REDIRECT_URL must equal BASE_URL/auth/callback")
	}
	enabled := c.OIDCIssuer != "" || c.OIDCClientID != "" || c.OIDCClientSecret != ""
	if enabled && (c.OIDCIssuer == "" || c.OIDCClientID == "" || c.OIDCClientSecret == "") {
		return c, errors.New("OIDC issuer, client ID and secret must be configured together")
	}
	if c.Environment != "local" && (!enabled || len(c.AdminEmails) == 0) {
		return c, errors.New("non-local environments require OIDC and ADMIN_EMAILS")
	}
	for _, value := range []string{c.OIDCIssuer, c.OIDCAuthURL, c.OIDCTokenURL, c.OIDCUserInfoURL, c.OIDCJWKSURL, c.FaroURL} {
		if value != "" && !ValidEndpoint(value, c.Environment == "local") {
			return c, errors.New("invalid HTTPS authentication or telemetry endpoint")
		}
	}
	if value := os.Getenv("SESSION_TTL"); value != "" {
		c.SessionTTL, err = time.ParseDuration(value)
		if err != nil || c.SessionTTL < time.Minute || c.SessionTTL > 7*24*time.Hour {
			return c, errors.New("SESSION_TTL must be between 1m and 168h")
		}
	}
	if value := os.Getenv("KEY_RATE_LIMIT_RPM"); value != "" {
		c.KeyRPM, err = strconv.Atoi(value)
		if err != nil || c.KeyRPM < 1 || c.KeyRPM > 10000 {
			return c, errors.New("KEY_RATE_LIMIT_RPM must be between 1 and 10000")
		}
	}
	if value := os.Getenv("WEBHOOK_ENCRYPTION_KEY"); value != "" {
		c.WebhookKey, err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(c.WebhookKey) != 32 {
			return c, errors.New("WEBHOOK_ENCRYPTION_KEY must be base64 encoded 32 bytes")
		}
	}
	if c.Environment != "local" && len(c.WebhookKey) != 32 {
		return c, errors.New("WEBHOOK_ENCRYPTION_KEY is required outside local development")
	}
	return c, nil
}

func ValidEndpoint(raw string, local bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || (local && u.Scheme == "http" && loopback(u.Hostname())))
}
func loopback(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }
func env(name, fallback string) string {
	if s := os.Getenv(name); s != "" {
		return s
	}
	return fallback
}
