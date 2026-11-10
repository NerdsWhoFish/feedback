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
	items, err := maps(r.Context(), s.db, `SELECT c.id::text,c.title,c.body,c.created_at,c.published_at,ARRAY(SELECT cr.request_id::text FROM changelog_requests cr WHERE cr.changelog_id=c.id ORDER BY cr.request_id) AS request_ids FROM changelog c WHERE c.project_id=$1 AND ($4 OR c.published_at IS NOT NULL) ORDER BY c.created_at DESC,c.id LIMIT $2 OFFSET $3`, id, limit, offset, staff(auth.FromContext(r.Context())))
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
		Published  bool     `json:"published"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	if in.Published && !p.Admin() {
		return denied
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
	items, err := maps(r.Context(), tx, `INSERT INTO changelog(project_id,title,body,published_at) VALUES($1,$2,$3,CASE WHEN $4 THEN now() END) RETURNING id::text,title,body,created_at,published_at`, id, in.Title, in.Body, in.Published)
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

func (s *Server) updateChangelog(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	if !idPattern.MatchString(r.PathValue("id")) {
		return missing
	}
	var in struct {
		Title     *string `json:"title"`
		Body      *string `json:"body"`
		Published *bool   `json:"published"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	if (in.Title != nil && !validText(*in.Title, 200)) || (in.Body != nil && !validText(*in.Body, 20000)) {
		return invalid("Changelog needs a title up to 200 and body up to 20000 characters.")
	}
	items, err := maps(r.Context(), s.db, `UPDATE changelog c SET title=coalesce($3,title),body=coalesce($4,body),published_at=CASE WHEN $5::boolean IS NULL THEN published_at WHEN $5 THEN coalesce(published_at,now()) ELSE NULL END WHERE id=$1 AND project_id=$2 RETURNING id::text,title,body,created_at,published_at,ARRAY(SELECT cr.request_id::text FROM changelog_requests cr WHERE cr.changelog_id=c.id ORDER BY cr.request_id) AS request_ids`, r.PathValue("id"), id, in.Title, in.Body, in.Published)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return missing
	}
	write(w, 200, items[0])
	return nil
}
