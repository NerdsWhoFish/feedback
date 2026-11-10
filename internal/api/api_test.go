package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/NerdsWhoFish/feedback/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	s                                       *Server
	db                                      *pgxpool.Pool
	mux                                     *http.ServeMux
	admin, member, outsider, app, assistant auth.Principal
	project, other                          string
}

func testFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	suffix, err := token()
	if err != nil {
		t.Fatal(err)
	}
	schema := "api_test_" + suffix[:12]
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); base.Close() })
	if err = store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, s: New(db, slog.New(slog.NewTextHandler(io.Discard, nil))), mux: http.NewServeMux()}
	f.s.Register(f.mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := auth.FromContext(r.Context())
			if !p.Browser() && p.KeyID == "" {
				write(w, 401, map[string]any{"error": map[string]string{"code": "unauthenticated"}})
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	user := func(email, role string) auth.Principal {
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO users(email,name,role) VALUES($1,$2,$3) RETURNING id::text`, email, email, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return auth.Principal{UserID: id, Email: email, Name: email, Role: role}
	}
	f.admin = user("admin@example.com", "admin")
	f.member = user("member@example.com", "member")
	f.outsider = user("other@example.com", "member")
	for _, dest := range []struct {
		name string
		id   *string
	}{{"alpha", &f.project}, {"beta", &f.other}} {
		if err := db.QueryRow(ctx, `INSERT INTO projects(slug,name) VALUES($1,$1) RETURNING id::text`, dest.name).Scan(dest.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(ctx, `INSERT INTO memberships(project_id,email) VALUES($1,$2),($3,$4)`, f.project, f.member.Email, f.other, f.outsider.Email); err != nil {
		t.Fatal(err)
	}
	key := func(kind, app string) auth.Principal {
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO api_keys(name,key_type,app_name,key_hash) VALUES($1,$1,$2,$3) RETURNING id::text`, kind, app, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO key_projects(key_id,project_id) VALUES($1,$2)`, id, f.project); err != nil {
			t.Fatal(err)
		}
		return auth.Principal{KeyID: id, KeyType: kind, AppName: app}
	}
	f.app = key("app", "test-app")
	f.assistant = key("assistant", "")
	return f
}
func (f *fixture) call(t *testing.T, p auth.Principal, method, target string, body any) (int, map[string]any) {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	result := map[string]any{}
	if w.Body.Len() > 0 {
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("response %d invalid JSON: %s", w.Code, w.Body.String())
		}
	}
	return w.Code, result
}
func expectStatus(t *testing.T, want, got int, body any) {
	t.Helper()
	if want != got {
		t.Fatalf("status %d, want %d; body=%v", got, want, body)
	}
}
func (f *fixture) request(t *testing.T, p auth.Principal, project string, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"title": "A better fish finder", "description": "Make it useful."}
	for k, v := range extra {
		body[k] = v
	}
	status, result := f.call(t, p, "POST", "/api/v1/projects/"+project+"/requests", body)
	expectStatus(t, 201, status, result)
	return result
}

func TestAnonymousAndProjectIsolation(t *testing.T) {
	f := testFixture(t)
	for _, route := range []string{"/api/v1/projects", "/api/v1/projects/" + f.project + "/requests", "/api/v1/keys"} {
		status, body := f.call(t, auth.Principal{}, "GET", route, nil)
		expectStatus(t, 401, status, body)
	}
	own := f.request(t, f.member, f.project, nil)
	id := own["id"].(string)
	for _, test := range []struct {
		method, target string
		body           any
	}{{"GET", "/api/v1/requests/" + id, nil}, {"POST", "/api/v1/requests/" + id + "/comments", map[string]any{"body": "leak"}}, {"PUT", "/api/v1/requests/" + id + "/vote", nil}, {"GET", "/api/v1/projects/" + f.project + "/requests", nil}, {"GET", "/api/v1/projects/" + f.project + "/changelog", nil}, {"POST", "/api/v1/projects/" + f.project + "/requests", map[string]any{"title": "leak"}}} {
		status, body := f.call(t, f.outsider, test.method, test.target, test.body)
		expectStatus(t, 404, status, body)
	}
	status, body := f.call(t, f.member, "GET", "/api/v1/projects", nil)
	expectStatus(t, 200, status, body)
	items := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != f.project {
		t.Fatalf("unexpected project visibility %v", items)
	}
	status, body = f.call(t, f.admin, "GET", "/api/v1/projects", nil)
	expectStatus(t, 200, status, body)
	if len(body["items"].([]any)) != 2 {
		t.Fatal("admin cannot see all projects")
	}
	noAccess := f.member
	noAccess.Email = "none@example.com"
	status, body = f.call(t, noAccess, "GET", "/api/v1/projects", nil)
	expectStatus(t, 200, status, body)
	if len(body["items"].([]any)) != 0 {
		t.Fatal("uninvited browser sees projects")
	}
}
func TestAssistantCannotSelfApprove(t *testing.T) {
	f := testFixture(t)
	created := f.request(t, f.member, f.project, nil)
	route := "/api/v1/requests/" + created["id"].(string) + "/status"
	for _, status := range []string{"approved_for_build", "in_progress", "shipped", "declined", "planned", "under_review", "open"} {
		code, body := f.call(t, f.assistant, "POST", route, map[string]string{"status": status})
		expectStatus(t, 403, code, body)
	}
	code, body := f.call(t, f.member, "POST", route, map[string]string{"status": "approved_for_build"})
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.app, "POST", route, map[string]string{"status": "approved_for_build"})
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.admin, "POST", route, map[string]string{"status": "in_progress"})
	expectStatus(t, 200, code, body)
	for _, status := range []string{"shipped", "approved_for_build"} {
		code, body = f.call(t, f.assistant, "POST", route, map[string]string{"status": status})
		expectStatus(t, 403, code, body)
	}
	code, body = f.call(t, f.admin, "POST", route, map[string]string{"status": "approved_for_build"})
	expectStatus(t, 200, code, body)
	for _, status := range []string{"in_progress", "approved_for_build", "in_progress", "shipped"} {
		code, body = f.call(t, f.assistant, "POST", route, map[string]string{"status": status})
		expectStatus(t, 200, code, body)
	}
	code, body = f.call(t, f.assistant, "POST", route, map[string]string{"status": "approved_for_build"})
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.admin, "GET", strings.TrimSuffix(route, "/status"), nil)
	expectStatus(t, 200, code, body)
	if len(body["history"].([]any)) != 6 {
		t.Fatalf("unexpected audit history: %v", body["history"])
	}
	for _, endpoint := range []string{"/api/v1/keys", "/api/v1/projects/" + f.project + "/memberships", "/api/v1/projects/" + f.project + "/webhooks"} {
		code, body = f.call(t, f.assistant, "GET", endpoint, nil)
		expectStatus(t, 403, code, body)
	}
	code, body = f.call(t, f.assistant, "POST", "/api/v1/projects/"+f.project+"/requests", map[string]string{"title": "unauthorized"})
	expectStatus(t, 403, code, body)
}
func TestAppScopeAndUntrustedIdentity(t *testing.T) {
	f := testFixture(t)
	user := map[string]any{"end_user": map[string]string{"external_id": "123", "email": f.admin.Email, "name": "Fake admin"}}
	created := f.request(t, f.app, f.project, user)
	id := created["id"].(string)
	if _, ok := created["public_token"]; ok {
		t.Fatal("app received public capability")
	}
	var userID, role string
	if err := f.db.QueryRow(context.Background(), `SELECT u.id::text,u.role FROM users u JOIN identities i ON i.user_id=u.id WHERE i.issuer='app:test-app' AND i.subject='123'`).Scan(&userID, &role); err != nil {
		t.Fatal(err)
	}
	if userID == f.admin.UserID || role != "member" {
		t.Fatal("app asserted email inherited browser identity")
	}
	code, body := f.call(t, f.app, "GET", "/api/v1/projects/"+f.other+"/requests?external_id=123", nil)
	expectStatus(t, 404, code, body)
	code, body = f.call(t, f.app, "GET", "/api/v1/requests/"+id, nil)
	expectStatus(t, 400, code, body)
	code, body = f.call(t, f.app, "GET", "/api/v1/requests/"+id+"?external_id=456", nil)
	expectStatus(t, 404, code, body)
	code, body = f.call(t, f.app, "GET", "/api/v1/requests/"+id+"?external_id=123", nil)
	expectStatus(t, 200, code, body)
	f.request(t, f.app, f.project, map[string]any{"end_user": map[string]string{"external_id": "456", "email": f.admin.Email, "name": "Another person"}})
	code, body = f.call(t, f.app, "GET", "/api/v1/projects/"+f.project+"/requests?external_id=123", nil)
	expectStatus(t, 200, code, body)
	if len(body["items"].([]any)) != 1 {
		t.Fatal("app can see another user's requests")
	}
	for _, endpoint := range []string{"/api/v1/requests/" + id + "/comments", "/api/v1/requests/" + id + "/status", "/api/v1/projects/" + f.project + "/changelog"} {
		code, body = f.call(t, f.app, "POST", endpoint, map[string]string{"body": "nope"})
		expectStatus(t, 403, code, body)
	}
	forged := f.app
	forged.Email = f.admin.Email
	forged.Role = "admin"
	code, body = f.call(t, forged, "GET", "/api/v1/keys", nil)
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.member, "POST", "/api/v1/projects/"+f.project+"/requests", map[string]any{"title": "No impersonation", "end_user": user["end_user"]})
	expectStatus(t, 400, code, body)
}
func TestPublicPrivacyCommentsVotesAndChangelog(t *testing.T) {
	f := testFixture(t)
	created := f.request(t, f.member, f.project, nil)
	id := created["id"].(string)
	publicURL := "/api/v1/public/requests/" + created["public_token"].(string)
	code, body := f.call(t, auth.Principal{}, "GET", publicURL, nil)
	expectStatus(t, 404, code, body)
	code, body = f.call(t, f.admin, "PATCH", "/api/v1/projects/"+f.project, map[string]bool{"public_status_page": true})
	expectStatus(t, 200, code, body)
	for _, entry := range []struct {
		p        auth.Principal
		internal bool
		body     string
	}{{f.admin, true, "internal secret"}, {f.member, false, "public note"}} {
		code, body = f.call(t, entry.p, "POST", "/api/v1/requests/"+id+"/comments", map[string]any{"body": entry.body, "internal": entry.internal})
		expectStatus(t, 201, code, body)
	}
	code, body = f.call(t, f.member, "POST", "/api/v1/requests/"+id+"/comments", map[string]any{"body": "hide this", "internal": true})
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.member, "GET", "/api/v1/requests/"+id, nil)
	expectStatus(t, 200, code, body)
	if len(body["comments"].([]any)) != 1 {
		t.Fatal("member received internal comments")
	}
	code, body = f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "shipped"})
	expectStatus(t, 200, code, body)
	code, body = f.call(t, auth.Principal{}, "GET", publicURL, nil)
	expectStatus(t, 200, code, body)
	encoded, _ := json.Marshal(body)
	for _, secret := range []string{f.member.Email, f.admin.Email, "author", "actor", "internal secret", "description", "source", id, "public_token"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("public output leaked %q: %s", secret, encoded)
		}
	}
	for range 2 {
		code, body = f.call(t, f.member, "PUT", "/api/v1/requests/"+id+"/vote", nil)
		expectStatus(t, 200, code, body)
		if body["votes"] != float64(1) {
			t.Fatal("duplicate vote counted")
		}
	}
	code, body = f.call(t, f.member, "DELETE", "/api/v1/requests/"+id+"/vote", nil)
	expectStatus(t, 200, code, body)
	if body["votes"] != float64(0) {
		t.Fatal("vote not removed")
	}
	code, body = f.call(t, f.assistant, "POST", "/api/v1/projects/"+f.project+"/changelog", map[string]any{"title": "Delivered", "body": "Useful change", "request_ids": []string{id}})
	expectStatus(t, 201, code, body)
	wrong := f.request(t, f.outsider, f.other, nil)
	code, body = f.call(t, f.admin, "POST", "/api/v1/projects/"+f.project+"/changelog", map[string]any{"title": "Wrong project", "body": "No", "request_ids": []string{wrong["id"].(string)}})
	expectStatus(t, 400, code, body)
	code, body = f.call(t, f.admin, "PATCH", "/api/v1/projects/"+f.project, map[string]bool{"public_status_page": false})
	expectStatus(t, 200, code, body)
	code, body = f.call(t, auth.Principal{}, "GET", publicURL, nil)
	expectStatus(t, 404, code, body)
}
func TestKeyHashingPaginationAndValidation(t *testing.T) {
	f := testFixture(t)
	code, body := f.call(t, f.admin, "POST", "/api/v1/keys", map[string]any{"name": "new app", "key_type": "app", "app_name": "angler", "project_ids": []string{f.project}})
	expectStatus(t, 201, code, body)
	raw := body["token"].(string)
	id := body["id"].(string)
	var stored string
	if err := f.db.QueryRow(context.Background(), `SELECT key_hash FROM api_keys WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == raw || stored != hash(raw) {
		t.Fatal("key not hashed correctly")
	}
	code, body = f.call(t, f.admin, "GET", "/api/v1/keys", nil)
	expectStatus(t, 200, code, body)
	encoded, _ := json.Marshal(body)
	if bytes.Contains(encoded, []byte(raw)) || bytes.Contains(encoded, []byte(stored)) {
		t.Fatal("key read exposed credential")
	}
	code, body = f.call(t, f.admin, "DELETE", "/api/v1/keys/"+id, nil)
	expectStatus(t, 204, code, body)
	p := auth.Principal{KeyID: id, KeyType: "app", AppName: "angler"}
	code, body = f.call(t, p, "GET", "/api/v1/projects/"+f.project+"/requests?external_id=123", nil)
	expectStatus(t, 404, code, body)
	for _, query := range []string{"limit=0", "limit=101", "offset=-1", "limit=wat", "status=wat"} {
		code, body = f.call(t, f.member, "GET", "/api/v1/projects/"+f.project+"/requests?"+query, nil)
		expectStatus(t, 400, code, body)
	}
	for i := range 3 {
		f.request(t, f.member, f.project, map[string]any{"title": strings.Repeat("x", i+1)})
	}
	code, body = f.call(t, f.member, "GET", "/api/v1/projects/"+f.project+"/requests?limit=2&offset=2", nil)
	expectStatus(t, 200, code, body)
	if len(body["items"].([]any)) != 1 {
		t.Fatal("pagination failed")
	}
}

func TestActivityPaginationAndUUIDSlugRejection(t *testing.T) {
	f := testFixture(t)
	created := f.request(t, f.member, f.project, nil)
	id := created["id"].(string)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `INSERT INTO comments(request_id,author_id,body,internal) SELECT $1,$2,'visible comment',false FROM generate_series(1,105)`, id, f.member.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO comments(request_id,author_id,body,internal) VALUES($1,$2,'internal comment',true)`, id, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO status_history(request_id,actor_id,from_status,to_status) SELECT $1,$2,'open','planned' FROM generate_series(1,105)`, id, f.admin.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE projects SET public_status_page=true WHERE id=$1`, f.project); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		p     auth.Principal
		route string
	}{{f.member, "/api/v1/requests/" + id}, {auth.Principal{}, "/api/v1/public/requests/" + created["public_token"].(string)}} {
		code, body := f.call(t, entry.p, "GET", entry.route, nil)
		expectStatus(t, 200, code, body)
		for _, field := range []string{"comments", "history"} {
			if len(body[field].([]any)) != 100 || body[field+"_total"] != float64(105) {
				t.Fatalf("unbounded detail or incorrect count: %s", field)
			}
			code, page := f.call(t, entry.p, "GET", entry.route+"/"+field+"?limit=100&offset=100", nil)
			expectStatus(t, 200, code, page)
			if len(page["items"].([]any)) != 5 {
				t.Fatalf("missing tail of %s", field)
			}
		}
	}
	for _, field := range []string{"comments", "history"} {
		code, body := f.call(t, f.outsider, "GET", "/api/v1/requests/"+id+"/"+field, nil)
		expectStatus(t, 404, code, body)
	}
	code, body := f.call(t, f.admin, "POST", "/api/v1/projects", map[string]any{"slug": f.project, "name": "Ambiguous project"})
	expectStatus(t, 400, code, body)
	code, body = f.call(t, f.admin, "PATCH", "/api/v1/projects/"+f.other, map[string]any{"slug": f.project})
	expectStatus(t, 400, code, body)
}
