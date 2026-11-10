package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ key string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPTransportAndAuthorization(t *testing.T) {
	f := testFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, err := auth.New(ctx, f.db, config.Config{Environment: "local", KeyRPM: 1000})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.s.Register(mux, a.Require)
	server := httptest.NewServer(mux)
	defer server.Close()
	for key, id := range map[string]string{"mcp-test-assistant": f.assistant.KeyID, "mcp-test-app": f.app.KeyID} {
		if _, err = f.db.Exec(ctx, `UPDATE api_keys SET key_hash=$2 WHERE id=$1`, id, auth.Hash(key)); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		key  string
		want int
	}{{"", 401}, {"bad-key", 401}, {"mcp-test-app", 403}} {
		r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if test.key != "" {
			r.Header.Set("Authorization", "Bearer "+test.key)
		}
		r.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != test.want {
			t.Fatalf("key scope status %d want %d", res.StatusCode, test.want)
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{"mcp-test-assistant"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"list_requests": true, "get_request": true, "list_comments": true, "list_history": true, "add_comment": true, "move_status": true, "add_changelog": true}
	if len(listed.Tools) != len(allowed) {
		t.Fatalf("unexpected tools: %v", listed.Tools)
	}
	for _, tool := range listed.Tools {
		if !allowed[tool.Name] {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
	}
	call := func(name string, args map[string]any, wantError bool) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("%s error=%v result=%v", name, result.IsError, result.Content)
		}
		var body map[string]any
		if len(result.Content) == 0 {
			t.Fatal("empty tool response")
		}
		if err = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	id := f.request(t, f.member, f.project, map[string]any{"title": "Exact Search Needle"})["id"].(string)
	other := f.request(t, f.outsider, f.other, nil)["id"].(string)
	for _, status := range []string{"approved_for_build", "in_progress", "shipped"} {
		call("move_status", map[string]any{"id": id, "status": status}, true)
	}
	call("get_request", map[string]any{"id": other}, true)
	call("list_requests", map[string]any{"project": f.other}, true)
	call("list_requests", map[string]any{"project": f.project, "limit": 101}, true)
	call("list_requests", map[string]any{"project": f.project, "status": "bad"}, true)
	found := call("list_requests", map[string]any{"project": "alpha", "q": "search NEEDLE", "status": "open"}, false)
	if len(found["items"].([]any)) != 1 {
		t.Fatal("search did not filter")
	}
	found = call("list_requests", map[string]any{"project": "alpha", "q": "%"}, false)
	if len(found["items"].([]any)) != 0 {
		t.Fatal("literal search treated percent as wildcard")
	}
	call("add_comment", map[string]any{"id": id, "body": "Assistant note", "internal": true}, false)
	code, body := f.call(t, f.admin, "POST", "/api/v1/requests/"+id+"/status", map[string]string{"status": "approved_for_build"})
	expectStatus(t, 200, code, body)
	for _, status := range []string{"in_progress", "approved_for_build", "in_progress", "shipped"} {
		call("move_status", map[string]any{"id": id, "status": status}, false)
	}
	call("move_status", map[string]any{"id": id, "status": "approved_for_build"}, true)
	detail := call("get_request", map[string]any{"id": id}, false)
	if len(detail["comments"].([]any)) != 1 || len(detail["history"].([]any)) != 5 {
		t.Fatal("detail missing activity")
	}
	call("list_comments", map[string]any{"id": id, "limit": 1}, false)
	call("list_history", map[string]any{"id": id, "offset": 1}, false)
	draft := call("add_changelog", map[string]any{"project": f.project, "title": "Draft", "body": "For review", "request_ids": []string{id}}, false)
	if draft["published_at"] != nil {
		t.Fatal("assistant published a draft")
	}
	if _, err = f.db.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, f.assistant.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = session.ListTools(ctx, nil); err == nil {
		t.Fatal("revoked key retained MCP access")
	}
}
