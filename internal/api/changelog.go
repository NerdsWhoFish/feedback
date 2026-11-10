package api

import (
	"net/http"

	"github.com/NerdsWhoFish/feedback/internal/auth"
)

func (s *Server) listChangelog(w http.ResponseWriter, r *http.Request) error {
	id, err := s.project(r)
	if err != nil {
		return err
	}
	if auth.FromContext(r.Context()).KeyType == "app" {
		return denied
	}
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `SELECT c.id::text,c.title,c.body,c.created_at,ARRAY(SELECT cr.request_id::text FROM changelog_requests cr WHERE cr.changelog_id=c.id ORDER BY cr.request_id) AS request_ids FROM changelog c WHERE c.project_id=$1 ORDER BY c.created_at DESC,c.id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
func (s *Server) addChangelog(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !staff(p) {
		return denied
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	var in struct {
		Title      string   `json:"title"`
		Body       string   `json:"body"`
		RequestIDs []string `json:"request_ids"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	if !validText(in.Title, 200) || !validText(in.Body, 20000) || len(in.RequestIDs) > 100 {
		return invalid("Changelog needs a title and body, and supports up to 100 shipped requests.")
	}
	for _, rid := range in.RequestIDs {
		if !idPattern.MatchString(rid) {
			return invalid("Request IDs must be UUIDs.")
		}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	for _, rid := range in.RequestIDs {
		var exists bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM requests WHERE id=$1 AND project_id=$2 AND status='shipped')`, rid, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return invalid("Linked requests must be shipped and belong to this project.")
		}
	}
	items, err := maps(r.Context(), tx, `INSERT INTO changelog(project_id,title,body) VALUES($1,$2,$3) RETURNING id::text,title,body,created_at`, id, in.Title, in.Body)
	if err != nil {
		return err
	}
	for _, rid := range in.RequestIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO changelog_requests(changelog_id,request_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, items[0]["id"], rid); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	if in.RequestIDs == nil {
		in.RequestIDs = []string{}
	}
	items[0]["request_ids"] = in.RequestIDs
	write(w, 201, items[0])
	return nil
}
