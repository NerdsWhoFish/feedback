package api

import (
	"context"
	"net/http"

	"github.com/NerdsWhoFish/feedback/internal/auth"
)

func (s *Server) commentRows(ctx context.Context, id string, includeInternal, public bool, limit, offset int) ([]map[string]any, error) {
	if public {
		return maps(ctx, s.db, `SELECT body,created_at FROM comments WHERE request_id=$1 AND NOT internal ORDER BY created_at,id LIMIT $2 OFFSET $3`, id, limit, offset)
	}
	return maps(ctx, s.db, `SELECT c.id::text,c.body,c.internal,COALESCE(u.name,k.name,'Staff') AS author_name,c.created_at FROM comments c LEFT JOIN users u ON u.id=c.author_id LEFT JOIN api_keys k ON k.id=c.key_id WHERE c.request_id=$1 AND ($2 OR NOT c.internal) ORDER BY c.created_at,c.id LIMIT $3 OFFSET $4`, id, includeInternal, limit, offset)
}
func (s *Server) historyRows(ctx context.Context, id string, public bool, limit, offset int) ([]map[string]any, error) {
	if public {
		return maps(ctx, s.db, `SELECT from_status,to_status,created_at FROM status_history WHERE request_id=$1 ORDER BY created_at,id LIMIT $2 OFFSET $3`, id, limit, offset)
	}
	return maps(ctx, s.db, `SELECT h.id::text,h.from_status,h.to_status,COALESCE(u.name,k.name,'Staff') AS actor_name,h.created_at FROM status_history h LEFT JOIN users u ON u.id=h.actor_id LEFT JOIN api_keys k ON k.id=h.key_id WHERE h.request_id=$1 ORDER BY h.created_at,h.id LIMIT $2 OFFSET $3`, id, limit, offset)
}
func (s *Server) detailTotals(ctx context.Context, id string, includeInternal bool, comments, history *int64) error {
	return s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM comments WHERE request_id=$1 AND ($2 OR NOT internal)),(SELECT count(*) FROM status_history WHERE request_id=$1)`, id, includeInternal).Scan(comments, history)
}
func (s *Server) publicIdentity(r *http.Request) (string, string, string, error) {
	if len(r.PathValue("token")) != 64 {
		return "", "", "", missing
	}
	var id, title, status string
	err := s.db.QueryRow(r.Context(), `SELECT r.id::text,r.title,r.status FROM requests r JOIN projects p ON p.id=r.project_id WHERE r.public_token=$1 AND p.public_status_page`, r.PathValue("token")).Scan(&id, &title, &status)
	return id, title, status, err
}
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) error {
	return s.activityPage(w, r, false, false)
}
func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) error {
	return s.activityPage(w, r, false, true)
}
func (s *Server) publicComments(w http.ResponseWriter, r *http.Request) error {
	return s.activityPage(w, r, true, false)
}
func (s *Server) publicHistory(w http.ResponseWriter, r *http.Request) error {
	return s.activityPage(w, r, true, true)
}
func (s *Server) activityPage(w http.ResponseWriter, r *http.Request, public, history bool) error {
	var id string
	var err error
	if public {
		id, _, _, err = s.publicIdentity(r)
	} else {
		var v request
		v, err = s.loadRequest(r)
		id = v.ID
	}
	if err != nil {
		return err
	}
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	var items []map[string]any
	if history {
		items, err = s.historyRows(r.Context(), id, public, limit, offset)
	} else {
		items, err = s.commentRows(r.Context(), id, staff(auth.FromContext(r.Context())), public, limit, offset)
	}
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
