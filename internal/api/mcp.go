package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpListInput struct {
	Project string `json:"project" jsonschema:"Project UUID or slug within the assistant key scope"`
	Status  string `json:"status,omitempty" jsonschema:"Optional request status filter"`
	Query   string `json:"q,omitempty" jsonschema:"Case insensitive literal search in title and description"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Page size from 1 to 100; defaults to 50"`
	Offset  int    `json:"offset,omitempty" jsonschema:"Pagination offset"`
}
type mcpRequestInput struct {
	ID string `json:"id" jsonschema:"Request UUID"`
}
type mcpActivityInput struct {
	ID     string `json:"id" jsonschema:"Request UUID"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}
type mcpCommentInput struct {
	ID       string `json:"id"`
	Body     string `json:"body"`
	Internal bool   `json:"internal,omitempty"`
}
type mcpStatusInput struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type mcpChangelogInput struct {
	Project    string   `json:"project"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	RequestIDs []string `json:"request_ids,omitempty"`
}

// Reusing REST handlers keeps both transports on the same authorization path.
func (s *Server) MCP() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "feedback", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list_requests", Description: "List or search requests in a scoped project, optionally filtered by status."}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListInput) (*mcp.CallToolResult, any, error) {
		query := mcpPage(in.Limit, in.Offset)
		query.Set("status", in.Status)
		query.Set("q", in.Query)
		return s.mcpCall(ctx, s.listRequests, "GET /api/v1/projects/{project}/requests", map[string]string{"project": in.Project}, query, nil)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_request", Description: "Get a scoped request with the first 100 comments and history entries and total counts."}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRequestInput) (*mcp.CallToolResult, any, error) {
		return s.mcpCall(ctx, s.getRequest, "GET /api/v1/requests/{id}", map[string]string{"id": in.ID}, nil, nil)
	})
	for _, activity := range []struct {
		name, pattern string
		h             handler
	}{
		{"list_comments", "GET /api/v1/requests/{id}/comments", s.listComments},
		{"list_history", "GET /api/v1/requests/{id}/history", s.listHistory},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: activity.name, Description: "Page through request activity, including entries beyond the detail response."}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpActivityInput) (*mcp.CallToolResult, any, error) {
			return s.mcpCall(ctx, activity.h, activity.pattern, map[string]string{"id": in.ID}, mcpPage(in.Limit, in.Offset), nil)
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "add_comment", Description: "Add a public or internal comment to a scoped request."}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCommentInput) (*mcp.CallToolResult, any, error) {
		return s.mcpCall(ctx, s.addComment, "POST /api/v1/requests/{id}/comments", map[string]string{"id": in.ID}, nil, map[string]any{"body": in.Body, "internal": in.Internal})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "move_status", Description: "Move approved_for_build to in_progress, or authorized in_progress to shipped or back to approved_for_build. Cannot grant approval."}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpStatusInput) (*mcp.CallToolResult, any, error) {
		return s.mcpCall(ctx, s.changeStatus, "POST /api/v1/requests/{id}/status", map[string]string{"id": in.ID}, nil, map[string]string{"status": in.Status})
	})
	mcp.AddTool(server, &mcp.Tool{Name: "add_changelog", Description: "Create a draft changelog entry linked to shipped requests. Only an administrator can publish."}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpChangelogInput) (*mcp.CallToolResult, any, error) {
		return s.mcpCall(ctx, s.addChangelog, "POST /api/v1/projects/{project}/changelog", map[string]string{"project": in.Project}, nil, map[string]any{"title": in.Title, "body": in.Body, "request_ids": in.RequestIDs})
	})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 128 << 10, PropagateRequestCancellation: true})
	protected := http.NewCrossOriginProtection().Handler(transport)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := auth.FromContext(r.Context()); p.KeyType != "assistant" || p.KeyID == "" {
			write(w, 403, map[string]any{"error": map[string]string{"code": "forbidden", "message": "An assistant key is required."}})
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func mcpPage(limit, offset int) url.Values {
	v := url.Values{"offset": {strconv.Itoa(offset)}}
	if limit != 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	return v
}

type mcpResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *mcpResponse) Header() http.Header            { return w.header }
func (w *mcpResponse) WriteHeader(status int)         { w.status = status }
func (w *mcpResponse) Write(body []byte) (int, error) { return w.body.Write(body) }

func (s *Server) mcpCall(ctx context.Context, h handler, pattern string, pathValues map[string]string, query url.Values, body any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	method, _, _ := strings.Cut(pattern, " ")
	r, err := http.NewRequestWithContext(ctx, method, "http://feedback.internal/", bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	r.Pattern = pattern
	r.Header.Set("Content-Type", "application/json")
	r.URL.RawQuery = query.Encode()
	for name, value := range pathValues {
		r.SetPathValue(name, value)
	}
	w := &mcpResponse{header: make(http.Header), status: 200}
	s.wrap(h).ServeHTTP(w, r)
	return &mcp.CallToolResult{IsError: w.status >= 400, Content: []mcp.Content{&mcp.TextContent{Text: w.body.String()}}}, nil, nil
}
