package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/NerdsWhoFish/feedback/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

type Server struct {
	db         *pgxpool.Pool
	log        *slog.Logger
	webhookKey []byte
	outgoing   *outgoingConfig
	email      *emailConfig
	metrics    *telemetry.Metrics
}

func New(db *pgxpool.Pool, logger *slog.Logger) *Server { return &Server{db: db, log: logger} }

type handler func(http.ResponseWriter, *http.Request) error
type apiError struct {
	status        int
	code, message string
}

func (e apiError) Error() string      { return e.code }
func invalid(message string) apiError { return apiError{400, "invalid_input", message} }

var denied = apiError{403, "forbidden", "This operation is not permitted."}
var missing = apiError{404, "not_found", "The resource was not found."}

func (s *Server) wrap(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("feedback/api").Start(r.Context(), r.Pattern)
		defer span.End()
		r = r.WithContext(ctx)
		if err := h(w, r); err != nil {
			e := apiError{500, "internal_error", "The operation could not be completed."}
			var ae apiError
			var pe *pgconn.PgError
			switch {
			case errors.As(err, &ae):
				e = ae
			case errors.Is(err, pgx.ErrNoRows):
				e = missing
			case errors.As(err, &pe):
				switch pe.Code {
				case "23505":
					e = apiError{409, "conflict", "This resource already exists."}
				case "23503", "23514":
					e = invalid("A referenced resource or field is invalid.")
				}
			}
			if e.status >= 500 {
				span.SetStatus(codes.Error, "operation_failed")
				s.log.ErrorContext(ctx, "api operation failed", "operation", r.Pattern)
			}
			write(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.message}})
		}
	})
}
func (s *Server) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	routes := map[string]handler{
		"GET /api/v1/projects": s.listProjects, "POST /api/v1/projects": s.createProject, "PATCH /api/v1/projects/{project}": s.updateProject,
		"GET /api/v1/projects/{project}/requests": s.listRequests, "POST /api/v1/projects/{project}/requests": s.createRequest,
		"GET /api/v1/requests/{id}": s.getRequest, "POST /api/v1/requests/{id}/comments": s.addComment, "POST /api/v1/requests/{id}/status": s.changeStatus,
		"GET /api/v1/requests/{id}/comments": s.listComments, "GET /api/v1/requests/{id}/history": s.listHistory,
		"PUT /api/v1/requests/{id}/vote": s.vote, "DELETE /api/v1/requests/{id}/vote": s.vote,
		"GET /api/v1/projects/{project}/changelog": s.listChangelog, "POST /api/v1/projects/{project}/changelog": s.addChangelog,
		"PATCH /api/v1/projects/{project}/changelog/{id}": s.updateChangelog,
		"GET /api/v1/me/notifications":                    s.emailPreferences, "PUT /api/v1/me/notifications": s.emailPreferences,
		"GET /api/v1/requests/{id}/subscription": s.subscription, "PUT /api/v1/requests/{id}/subscription": s.subscription, "DELETE /api/v1/requests/{id}/subscription": s.subscription,
		"GET /api/v1/projects/{project}/memberships": s.listMemberships, "POST /api/v1/projects/{project}/memberships": s.addMembership, "DELETE /api/v1/projects/{project}/memberships/{email}": s.deleteMembership,
		"GET /api/v1/keys": s.listKeys, "POST /api/v1/keys": s.createKey, "DELETE /api/v1/keys/{id}": s.revokeKey,
		"GET /api/v1/projects/{project}/webhooks": s.listWebhooks, "POST /api/v1/projects/{project}/webhooks": s.createWebhook, "DELETE /api/v1/projects/{project}/webhooks/{id}": s.deleteWebhook, "GET /api/v1/projects/{project}/deliveries": s.listDeliveries,
	}
	for pattern, h := range routes {
		mux.Handle(pattern, require(s.wrap(h)))
	}
	mux.Handle("/mcp", require(s.MCP()))
	mux.Handle("GET /api/v1/email/unsubscribe/{token}", s.wrap(s.emailUnsubscribe))
	mux.Handle("POST /api/v1/email/unsubscribe/{token}", s.wrap(s.emailUnsubscribe))
	mux.Handle("GET /api/v1/public/requests/{token}", s.wrap(s.publicRequest))
	mux.Handle("GET /api/v1/public/requests/{token}/comments", s.wrap(s.publicComments))
	mux.Handle("GET /api/v1/public/requests/{token}/history", s.wrap(s.publicHistory))
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		return apiError{415, "unsupported_media_type", "Use application/json."}
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return invalid("Invalid JSON body.")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return invalid("Expected one JSON object.")
	}
	return nil
}
func pagination(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, invalid("Limit must be between 1 and 100.")
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 || offset > 1000000 {
			return 0, 0, invalid("Offset must be between 0 and 1000000.")
		}
	}
	return limit, offset, nil
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func maps(ctx context.Context, q querier, sql string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil {
		items = []map[string]any{}
	}
	return items, err
}
func page(w http.ResponseWriter, items any, limit, offset int) {
	write(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset})
}
func token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func hash(v string) string { b := sha256.Sum256([]byte(v)); return hex.EncodeToString(b[:]) }

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var idPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func validText(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }
func email(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || len(v) > 254 {
		return "", invalid("Enter a valid email address.")
	}
	return v, nil
}
func admin(r *http.Request) error {
	if !auth.FromContext(r.Context()).Admin() {
		return denied
	}
	return nil
}
func (s *Server) project(r *http.Request) (string, error) {
	return s.projectAccess(r.Context(), auth.FromContext(r.Context()), r.PathValue("project"))
}
func (s *Server) projectAccess(ctx context.Context, p auth.Principal, project string) (string, error) {
	if !p.Browser() && p.KeyID == "" {
		return "", apiError{401, "unauthenticated", "Sign in to continue."}
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT p.id::text FROM projects p WHERE (p.id::text=$1 OR p.slug=$1) AND ($2 OR ($3<>'' AND EXISTS(SELECT 1 FROM memberships m WHERE m.project_id=p.id AND m.email=$3)) OR ($4<>'' AND EXISTS(SELECT 1 FROM key_projects k JOIN api_keys a ON a.id=k.key_id WHERE k.project_id=p.id AND k.key_id::text=$4 AND a.revoked_at IS NULL)))`, project, p.Admin(), browserEmail(p), p.KeyID).Scan(&id)
	return id, err
}
func browserEmail(p auth.Principal) string {
	if p.Browser() {
		return strings.ToLower(p.Email)
	}
	return ""
}
func actor(p auth.Principal) (any, any) {
	if p.KeyID != "" {
		return nil, p.KeyID
	}
	return p.UserID, nil
}
func staff(p auth.Principal) bool { return p.Admin() || p.KeyType == "assistant" }
