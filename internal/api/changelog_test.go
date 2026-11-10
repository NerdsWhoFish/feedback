package api

import (
	"context"
	"testing"

	"github.com/NerdsWhoFish/feedback/internal/store"
)

func TestDraftLifecycleAndIsolation(t *testing.T) {
	f := testFixture(t)
	id := f.request(t, f.member, f.project, nil)["id"].(string)
	statusRoute := "/api/v1/requests/" + id + "/status"
	listRoute := "/api/v1/projects/" + f.project + "/changelog"
	code, body := f.call(t, f.admin, "POST", statusRoute, map[string]string{"status": "shipped"})
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.member, "GET", listRoute, nil)
	expectStatus(t, 200, code, body)
	if len(body["items"].([]any)) != 0 {
		t.Fatal("member sees unpublished draft")
	}
	code, body = f.call(t, f.assistant, "GET", listRoute, nil)
	expectStatus(t, 200, code, body)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want one automatic draft: %v", items)
	}
	draft := items[0].(map[string]any)
	if draft["published_at"] != nil || draft["request_ids"].([]any)[0] != id {
		t.Fatal("draft not linked or published automatically")
	}
	editRoute := listRoute + "/" + draft["id"].(string)
	for _, p := range []struct {
		name  string
		route string
	}{{"scope", "/api/v1/projects/" + f.other + "/changelog/" + draft["id"].(string)}} {
		code, body = f.call(t, f.admin, "PATCH", p.route, map[string]any{"published": true})
		expectStatus(t, 404, code, body)
	}
	code, body = f.call(t, f.assistant, "PATCH", editRoute, map[string]any{"published": true})
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.assistant, "POST", listRoute, map[string]any{"title": "Denied", "body": "No", "published": true})
	expectStatus(t, 403, code, body)
	code, body = f.call(t, f.admin, "PATCH", editRoute, map[string]any{"title": "Reviewed title", "body": "Reviewed body", "published": true})
	expectStatus(t, 200, code, body)
	if body["published_at"] == nil {
		t.Fatal("publish missing timestamp")
	}
	for _, status := range []string{"shipped", "planned", "shipped"} {
		code, body = f.call(t, f.admin, "POST", statusRoute, map[string]string{"status": status})
		expectStatus(t, 200, code, body)
	}
	code, body = f.call(t, f.member, "GET", listRoute, nil)
	expectStatus(t, 200, code, body)
	items = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["body"] != "Reviewed body" {
		t.Fatal("re-shipping duplicates or overwrites reviewed entry")
	}
	code, body = f.call(t, f.admin, "PATCH", editRoute, map[string]any{"published": false})
	expectStatus(t, 200, code, body)
	code, body = f.call(t, f.member, "GET", listRoute, nil)
	expectStatus(t, 200, code, body)
	if len(body["items"].([]any)) != 0 {
		t.Fatal("unpublished entry visible")
	}
}

func TestShipDraftRollsBackWithStatus(t *testing.T) {
	f := testFixture(t)
	id := f.request(t, f.member, f.project, nil)["id"].(string)
	_, err := f.db.Exec(context.Background(), `ALTER TABLE changelog ADD CONSTRAINT test_reject_draft CHECK(title='unreachable')`)
	if err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "shipped"})
	expectStatus(t, 400, code, body)
	var status string
	var count int
	if err = f.db.QueryRow(context.Background(), `SELECT status,(SELECT count(*) FROM status_history WHERE request_id=$1) FROM requests WHERE id=$1`, id).Scan(&status, &count); err != nil {
		t.Fatal(err)
	}
	if status != "open" || count != 0 {
		t.Fatal("status committed without its draft")
	}
}

func TestChangelogMigrationPreservesPublishedEntries(t *testing.T) {
	f := testFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `ALTER TABLE changelog DROP COLUMN published_at, DROP COLUMN automatic_request_id; DELETE FROM schema_migrations WHERE name='002_changelog_drafts.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO changelog(project_id,title,body) VALUES($1,'Existing release','Already public')`, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `DROP INDEX changelog_project_created`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	code, body := f.call(t, f.member, "GET", "/api/v1/projects/"+f.project+"/changelog", nil)
	expectStatus(t, 200, code, body)
	items := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["published_at"] == nil {
		t.Fatal("migration hid an existing published entry")
	}
	if err := store.Migrate(ctx, f.db); err != nil {
		t.Fatal("migrations are not repeatable", err)
	}
}
