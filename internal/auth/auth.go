package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/oauth2"
)

type Auth struct {
	db           *pgxpool.Pool
	config       config.Config
	provider     *oidc.Provider
	oauth        oauth2.Config
	client       *http.Client
	limiter      *Limiter
	loginLimiter *Limiter
}

type providerTransport struct {
	local bool
	base  http.RoundTripper
}

func (t providerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !config.ValidEndpoint(r.URL.String(), t.local) {
		return nil, errors.New("insecure OIDC endpoint rejected")
	}
	return t.base.RoundTrip(r)
}

func New(ctx context.Context, db *pgxpool.Pool, c config.Config) (*Auth, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	a := &Auth{db: db, config: c, client: &http.Client{Transport: providerTransport{local: c.Environment == "local", base: transport}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, limiter: NewLimiter(c.KeyRPM), loginLimiter: NewLimiter(20)}
	if c.OIDCIssuer == "" {
		return a, nil
	}
	ctx = oidc.ClientContext(ctx, a.client)
	provider, err := oidc.NewProvider(ctx, c.OIDCIssuer)
	if err != nil {
		if c.OIDCAuthURL == "" || c.OIDCTokenURL == "" || c.OIDCUserInfoURL == "" || c.OIDCJWKSURL == "" {
			return nil, errors.New("OIDC discovery failed; all explicit endpoints including JWKS are required for fallback")
		}
		provider = (&oidc.ProviderConfig{IssuerURL: c.OIDCIssuer, AuthURL: c.OIDCAuthURL, TokenURL: c.OIDCTokenURL, UserInfoURL: c.OIDCUserInfoURL, JWKSURL: c.OIDCJWKSURL, Algorithms: []string{"RS256", "ES256"}}).NewProvider(ctx)
	}
	if !config.ValidEndpoint(provider.Endpoint().AuthURL, c.Environment == "local") || !config.ValidEndpoint(provider.Endpoint().TokenURL, c.Environment == "local") || !config.ValidEndpoint(provider.UserInfoEndpoint(), c.Environment == "local") {
		return nil, errors.New("OIDC discovery returned insecure endpoints")
	}
	a.provider = provider
	a.oauth = oauth2.Config{ClientID: c.OIDCClientID, ClientSecret: c.OIDCClientSecret, Endpoint: provider.Endpoint(), RedirectURL: c.OIDCRedirectURL, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	return a, nil
}

func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.Handle("POST /auth/logout", a.Require(http.HandlerFunc(a.logout)))
	mux.Handle("GET /api/v1/me", a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if !p.Browser() {
			fail(w, 403, "forbidden", "Browser session required")
			return
		}
		write(w, 200, p)
	})))
}

func (a *Auth) cookieName() string {
	if a.config.CookieSecure {
		return "__Host-feedback"
	}
	return "feedback_local"
}
func (a *Auth) stateCookieName() string {
	if a.config.CookieSecure {
		return "__Host-feedback_state"
	}
	return "feedback_state_local"
}
func (a *Auth) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, Secure: a.config.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if a.provider == nil {
		fail(w, 503, "auth_unavailable", "OIDC is not configured")
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.loginLimiter.Allow(host) {
		fail(w, 429, "rate_limited", "Too many login attempts")
		return
	}
	token, nonce, verifier := RandomToken(), RandomToken(), oauth2.GenerateVerifier()
	if _, err := a.db.Exec(r.Context(), `INSERT INTO oidc_states(token_hash,nonce,verifier,expires_at) VALUES($1,$2,$3,now()+interval '10 minutes')`, Hash(token), nonce, verifier); err != nil {
		fail(w, 503, "unavailable", "Login temporarily unavailable")
		return
	}
	a.cookie(w, a.stateCookieName(), token, 600)
	http.Redirect(w, r, a.oauth.AuthCodeURL(token, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	if a.provider == nil {
		fail(w, 503, "auth_unavailable", "OIDC is not configured")
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(a.stateCookieName())
	if err != nil || len(state) != 43 || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		fail(w, 400, "invalid_login", "Invalid or expired login")
		return
	}
	a.cookie(w, a.stateCookieName(), "", -1)
	var nonce, verifier string
	err = a.db.QueryRow(r.Context(), `DELETE FROM oidc_states WHERE token_hash=$1 AND expires_at>now() RETURNING nonce,verifier`, Hash(state)).Scan(&nonce, &verifier)
	if err != nil || r.URL.Query().Get("code") == "" {
		fail(w, 400, "invalid_login", "Invalid or expired login")
		return
	}
	ctx, span := otel.Tracer("feedback/auth").Start(r.Context(), "auth.exchange")
	defer span.End()
	ctx = oidc.ClientContext(ctx, a.client)
	token, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		fail(w, 401, "invalid_login", "Identity provider rejected login")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		fail(w, 401, "invalid_login", "Identity token missing")
		return
	}
	id, err := a.provider.Verifier(&oidc.Config{ClientID: a.config.OIDCClientID}).Verify(ctx, raw)
	if err != nil || id.Nonce != nonce || id.Subject == "" {
		fail(w, 401, "invalid_login", "Identity token rejected")
		return
	}
	var claims struct {
		Email           string `json:"email"`
		Name            string `json:"name"`
		Verified        bool   `json:"email_verified"`
		AuthorizedParty string `json:"azp"`
	}
	if err = id.Claims(&claims); err != nil || (len(id.Audience) > 1 && claims.AuthorizedParty != a.config.OIDCClientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != a.config.OIDCClientID) {
		fail(w, 401, "invalid_login", "Identity claims rejected")
		return
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(token.AccessToken) != nil {
		fail(w, 401, "invalid_login", "Access token rejected")
		return
	}
	info, err := a.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil || info.Subject != id.Subject {
		fail(w, 401, "invalid_login", "Identity information rejected")
		return
	}
	if claims.Email == "" {
		claims.Email = info.Email
		claims.Verified = info.EmailVerified
	}
	if info.Email != "" && !strings.EqualFold(info.Email, claims.Email) {
		fail(w, 401, "invalid_login", "Identity email mismatch")
		return
	}
	if claims.Name == "" {
		var profile struct {
			Name string `json:"name"`
		}
		_ = info.Claims(&profile)
		claims.Name = profile.Name
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if !claims.Verified || email == "" || len(email) > 254 || !strings.Contains(email, "@") || len(claims.Name) > 200 {
		fail(w, 403, "unverified_email", "A verified email is required")
		return
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		fail(w, 503, "unavailable", "Login temporarily unavailable")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize first logins so concurrent callbacks cannot duplicate users.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, a.config.OIDCIssuer+":"+id.Subject)
	if err != nil {
		fail(w, 503, "unavailable", "Login temporarily unavailable")
		return
	}
	var userID string
	err = tx.QueryRow(ctx, `SELECT user_id FROM identities WHERE issuer=$1 AND subject=$2`, a.config.OIDCIssuer, id.Subject).Scan(&userID)
	role := "member"
	if a.config.AdminEmails[email] {
		role = "admin"
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO users(email,name,role) VALUES($1,$2,$3) RETURNING id`, email, claims.Name, role).Scan(&userID)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, a.config.OIDCIssuer, id.Subject, userID)
		}
	} else if err == nil {
		_, err = tx.Exec(ctx, `UPDATE users SET email=$1,name=$2,role=$3 WHERE id=$4`, email, claims.Name, role, userID)
	}
	if err != nil {
		fail(w, 503, "unavailable", "Login temporarily unavailable")
		return
	}
	session, csrf := RandomToken(), RandomToken()
	_, err = tx.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, Hash(session), userID, csrf, time.Now().Add(a.config.SessionTTL))
	if err != nil || tx.Commit(ctx) != nil {
		fail(w, 503, "unavailable", "Login temporarily unavailable")
		return
	}
	a.cookie(w, a.cookieName(), session, int(a.config.SessionTTL.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Principal
		authorization := r.Header.Get("Authorization")
		if authorization != "" {
			if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > 256 {
				fail(w, 401, "unauthorized", "Valid authentication required")
				return
			}
			value := strings.TrimPrefix(authorization, "Bearer ")
			err := a.db.QueryRow(r.Context(), `SELECT id,key_type,app_name FROM api_keys WHERE key_hash=$1 AND revoked_at IS NULL`, Hash(value)).Scan(&p.KeyID, &p.KeyType, &p.AppName)
			if err != nil {
				fail(w, 401, "unauthorized", "Valid authentication required")
				return
			}
			if !a.limiter.Allow(p.KeyID) {
				w.Header().Set("Retry-After", "60")
				fail(w, 429, "rate_limited", "API key rate limit exceeded")
				return
			}
		} else {
			cookie, err := r.Cookie(a.cookieName())
			if err != nil {
				fail(w, 401, "unauthorized", "Sign in required")
				return
			}
			err = a.db.QueryRow(r.Context(), `SELECT u.id,u.email,u.name,s.csrf_token FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, Hash(cookie.Value)).Scan(&p.UserID, &p.Email, &p.Name, &p.CSRF)
			if err != nil {
				fail(w, 401, "unauthorized", "Session expired or invalid")
				return
			}
			p.Role = "member"
			if a.config.AdminEmails[p.Email] {
				p.Role = "admin"
			}
			if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
				if r.Header.Get("Origin") != a.config.BaseURL || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRF)) != 1 {
					fail(w, 403, "csrf_failed", "Refresh the page and try again")
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if !FromContext(r.Context()).Browser() {
		fail(w, 403, "forbidden", "Browser session required")
		return
	}
	c, _ := r.Cookie(a.cookieName())
	if _, err := a.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, Hash(c.Value)); err != nil {
		fail(w, 503, "unavailable", "Logout temporarily unavailable")
		return
	}
	a.cookie(w, a.cookieName(), "", -1)
	w.WriteHeader(204)
}

func (a *Auth) Cleanup(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobCtx, span := otel.Tracer("feedback/auth").Start(ctx, "auth.cleanup")
			_, sessionErr := a.db.Exec(jobCtx, `DELETE FROM sessions WHERE expires_at<now()`)
			_, stateErr := a.db.Exec(jobCtx, `DELETE FROM oidc_states WHERE expires_at<now()`)
			if (sessionErr != nil || stateErr != nil) && ctx.Err() == nil {
				span.SetStatus(codes.Error, "cleanup failed")
				slog.ErrorContext(jobCtx, "authentication cleanup failed")
			}
			span.End()
		}
	}
}
func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func Hash(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type bucket struct {
	tokens float64
	last   time.Time
}
type Limiter struct {
	mu      sync.Mutex
	rpm     int
	entries map[string]bucket
}

func NewLimiter(rpm int) *Limiter {
	if rpm < 1 {
		rpm = 120
	}
	return &Limiter{rpm: rpm, entries: map[string]bucket{}}
}
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= 10000 {
			for k, v := range l.entries {
				if now.Sub(v.last) > time.Minute {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= 10000 {
				return false
			}
		}
		b = bucket{float64(l.rpm), now}
	}
	b.tokens = min(float64(l.rpm), b.tokens+now.Sub(b.last).Minutes()*float64(l.rpm))
	b.last = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.entries[key] = b
	return allowed
}
