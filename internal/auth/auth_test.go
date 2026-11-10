package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/NerdsWhoFish/feedback/internal/store"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"
)

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for real PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "auth_" + Hash(RandomToken())[:16]
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	if err = store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixture(t *testing.T) (*Auth, *pgxpool.Pool, string) {
	t.Helper()
	db := database(t)
	c := config.Config{BaseURL: "https://feedback.example", CookieSecure: true, AdminEmails: map[string]bool{"admin@example.com": true}, SessionTTL: time.Hour, KeyRPM: 2}
	a, err := New(context.Background(), db, c)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = db.QueryRow(context.Background(), `INSERT INTO users(email,name,role) VALUES('member@example.com','Member','admin') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf',now()+interval '1 hour')`, Hash("session"), id)
	if err != nil {
		t.Fatal(err)
	}
	return a, db, id
}

func TestAuthenticationAndCSRF(t *testing.T) {
	a, db, _ := fixture(t)
	h := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if p.Admin() {
			t.Error("stored role elevated a non-admin email")
		}
		w.WriteHeader(204)
	}))
	cases := []struct {
		name, method, cookie, origin, csrf, authorization string
		want                                              int
	}{
		{name: "anonymous", method: "GET", want: 401},
		{name: "valid session", method: "GET", cookie: "session", want: 204},
		{name: "missing CSRF", method: "POST", cookie: "session", origin: a.config.BaseURL, want: 403},
		{name: "wrong origin", method: "POST", cookie: "session", origin: "https://attacker.example", csrf: "csrf", want: 403},
		{name: "missing origin", method: "POST", cookie: "session", csrf: "csrf", want: 403},
		{name: "valid write", method: "POST", cookie: "session", origin: a.config.BaseURL, csrf: "csrf", want: 204},
		{name: "invalid bearer cannot fall back", method: "GET", cookie: "session", authorization: "Bearer invalid", want: 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/v1/projects", nil)
			r.Header.Set("Remote-User", "admin@example.com")
			r.Header.Set("Remote-Email", "admin@example.com")
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: a.cookieName(), Value: tc.cookie})
			}
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Authorization", tc.authorization)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
	_, err := db.Exec(context.Background(), `UPDATE sessions SET expires_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: a.cookieName(), Value: "session"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestKeysRevocationRateLimitAndCookieFlags(t *testing.T) {
	a, db, _ := fixture(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO api_keys(name,key_type,app_name,key_hash) VALUES('Test','app','client',$1)`, Hash("app-token"))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if p.Browser() || p.Admin() || p.KeyType != "app" {
			t.Fatal("key authenticated as browser")
		}
		w.WriteHeader(204)
	}))
	for _, want := range []int{204, 204, 429} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Authorization", "Bearer app-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("got %d want %d", w.Code, want)
		}
	}
	_, err = db.Exec(ctx, `UPDATE api_keys SET revoked_at=now()`)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer app-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked key accepted")
	}
	w = httptest.NewRecorder()
	a.cookie(w, a.cookieName(), "test", 3600)
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || !strings.HasPrefix(c.Name, "__Host-") {
		t.Fatalf("unsafe cookie: %+v", c)
	}
}

type oidcFixture struct {
	server                 *httptest.Server
	nonce, challenge, mode string
	key                    *rsa.PrivateKey
}

func provider(t *testing.T) *oidcFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcFixture{key: key}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if f.mode == "fallback" {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "userinfo_endpoint": f.server.URL + "/userinfo", "jwks_uri": f.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			_ = r.ParseForm()
			h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(h[:]) != f.challenge {
				t.Error("PKCE verifier missing or incorrect")
				w.WriteHeader(400)
				return
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: f.key, KeyID: "test"}}, nil)
			if err != nil {
				t.Error(err)
				return
			}
			claims := map[string]any{"iss": f.server.URL, "sub": "browser-user", "aud": "feedback-test", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": f.nonce, "email": "admin@example.com", "name": "Admin", "email_verified": true}
			switch f.mode {
			case "nonce":
				claims["nonce"] = "wrong"
			case "audience":
				claims["aud"] = "other-client"
			case "issuer":
				claims["iss"] = "https://attacker.example"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "unverified":
				claims["email_verified"] = false
			case "azp":
				claims["aud"] = []string{"feedback-test", "other"}
				claims["azp"] = "other"
			}
			payload, _ := json.Marshal(claims)
			signed, err := signer.Sign(payload)
			if err != nil {
				t.Error(err)
				return
			}
			token, err := signed.CompactSerialize()
			if err != nil {
				t.Error(err)
				return
			}
			if f.mode == "signature" {
				token = token[:len(token)-10] + "AAAAAAAAAA"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "provider-access", "token_type": "Bearer", "id_token": token, "expires_in": 3600})
		case "/userinfo":
			sub := "browser-user"
			if f.mode == "subject" {
				sub = "other-user"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": sub, "email": "admin@example.com", "name": "Admin", "email_verified": true})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func TestOIDCFullFlowAndRejection(t *testing.T) {
	db := database(t)
	f := provider(t)
	for _, mode := range []string{"valid", "nonce", "audience", "issuer", "expired", "unverified", "azp", "signature", "subject", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			f.mode = mode
			c := config.Config{BaseURL: "http://localhost:8080", Environment: "local", OIDCIssuer: f.server.URL, OIDCClientID: "feedback-test", OIDCClientSecret: "test-only", OIDCRedirectURL: "http://localhost:8080/auth/callback", AdminEmails: map[string]bool{"admin@example.com": true}, SessionTTL: time.Hour, OIDCAuthURL: f.server.URL + "/authorize", OIDCTokenURL: f.server.URL + "/token", OIDCUserInfoURL: f.server.URL + "/userinfo", OIDCJWKSURL: f.server.URL + "/jwks"}
			a, err := New(context.Background(), db, c)
			if err != nil {
				t.Fatal(err)
			}
			login := httptest.NewRecorder()
			a.login(login, httptest.NewRequest("GET", "/auth/login", nil))
			if login.Code != 302 {
				t.Fatal(login.Body.String())
			}
			location, err := url.Parse(login.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			q := location.Query()
			f.nonce = q.Get("nonce")
			f.challenge = q.Get("code_challenge")
			if q.Get("code_challenge_method") != "S256" || len(f.nonce) < 32 {
				t.Fatal("missing PKCE or nonce")
			}
			request := func() *http.Request {
				r := httptest.NewRequest("GET", "/auth/callback?state="+q.Get("state")+"&code=provider-code", nil)
				r.AddCookie(login.Result().Cookies()[0])
				return r
			}
			callback := httptest.NewRecorder()
			a.callback(callback, request())
			valid := mode == "valid" || mode == "fallback"
			if valid {
				if callback.Code != 303 {
					t.Fatalf("login failed: %d %s", callback.Code, callback.Body.String())
				}
				var session *http.Cookie
				for _, c := range callback.Result().Cookies() {
					if c.Name == a.cookieName() {
						session = c
					}
				}
				if session == nil {
					t.Fatal("no session")
				}
				me := httptest.NewRecorder()
				r := httptest.NewRequest("GET", "/", nil)
				r.AddCookie(session)
				a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !FromContext(r.Context()).Admin() {
						t.Error("admin email did not confer admin")
					}
					w.WriteHeader(204)
				})).ServeHTTP(me, r)
				if me.Code != 204 {
					t.Fatal("session failed")
				}
			} else if callback.Code < 400 {
				t.Fatalf("accepted invalid %s", mode)
			}
			replay := httptest.NewRecorder()
			a.callback(replay, request())
			if replay.Code != 400 {
				t.Fatal("state replay accepted")
			}
		})
	}
	var users int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("identity not stable across logins: count=%d err=%v", users, err)
	}
}

func TestOIDCStateBindingAndIdentityIsolation(t *testing.T) {
	db := database(t)
	f := provider(t)
	c := config.Config{BaseURL: "http://localhost:8080", Environment: "local", OIDCIssuer: f.server.URL, OIDCClientID: "feedback-test", OIDCClientSecret: "test-only", OIDCRedirectURL: "http://localhost:8080/auth/callback", SessionTTL: time.Hour}
	a, err := New(context.Background(), db, c)
	if err != nil {
		t.Fatal(err)
	}
	var appUser string
	if err = db.QueryRow(context.Background(), `INSERT INTO users(email,name) VALUES('admin@example.com','App assertion') RETURNING id`).Scan(&appUser); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(context.Background(), `INSERT INTO identities(issuer,subject,user_id) VALUES('app:client','browser-user',$1)`, appUser); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	a.login(login, httptest.NewRequest("GET", "/auth/login", nil))
	location, _ := url.Parse(login.Header().Get("Location"))
	q := location.Query()
	f.nonce = q.Get("nonce")
	f.challenge = q.Get("code_challenge")
	callbackURL := fmt.Sprintf("/auth/callback?state=%s&code=test", q.Get("state"))
	missing := httptest.NewRecorder()
	a.callback(missing, httptest.NewRequest("GET", callbackURL, nil))
	if missing.Code != 400 {
		t.Fatal("callback without browser binding accepted")
	}
	r := httptest.NewRequest("GET", callbackURL, nil)
	r.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	a.callback(w, r)
	if w.Code != 303 {
		t.Fatal(w.Body.String())
	}
	var browserUser string
	if err = db.QueryRow(context.Background(), `SELECT user_id FROM identities WHERE issuer=$1`, f.server.URL).Scan(&browserUser); err != nil {
		t.Fatal(err)
	}
	if browserUser == appUser {
		t.Fatal("browser identity linked to app assertion by email")
	}
}
