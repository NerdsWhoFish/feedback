package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/jackc/pgx/v5"
)

type request struct {
	ID              string           `json:"id"`
	ProjectID       string           `json:"project_id"`
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	Status          string           `json:"status"`
	Source          string           `json:"source"`
	Votes           int64            `json:"votes"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	PublicToken     string           `json:"public_token,omitempty"`
	AuthorID        string           `json:"-"`
	BuildAuthorized bool             `json:"-"`
	Comments        []map[string]any `json:"comments,omitempty"`
	History         []map[string]any `json:"history,omitempty"`
	CommentsTotal   int64            `json:"comments_total"`
	HistoryTotal    int64            `json:"history_total"`
}

const requestFields = `r.id::text,r.project_id::text,r.title,r.description,r.status,r.source,(SELECT count(*) FROM votes v WHERE v.request_id=r.id),r.created_at,r.updated_at,r.public_token,r.author_id::text,r.build_authorized`

func scanRequest(row pgx.Row) (request, error) {
	var v request
	err := row.Scan(&v.ID, &v.ProjectID, &v.Title, &v.Description, &v.Status, &v.Source, &v.Votes, &v.CreatedAt, &v.UpdatedAt, &v.PublicToken, &v.AuthorID, &v.BuildAuthorized)
	return v, err
}
func visibleRequest(v request, p auth.Principal) request {
	if !p.Admin() && (!p.Browser() || p.UserID != v.AuthorID) {
		v.PublicToken = ""
	}
	return v
}
func validStatus(v string) bool {
	switch v {
	case "open", "under_review", "planned", "approved_for_build", "in_progress", "shipped", "declined":
		return true
	}
	return false
}
func (s *Server) loadRequest(r *http.Request) (request, error) {
	if !idPattern.MatchString(r.PathValue("id")) {
		return request{}, missing
	}
	v, err := scanRequest(s.db.QueryRow(r.Context(), `SELECT `+requestFields+` FROM requests r WHERE r.id=$1`, r.PathValue("id")))
	if err != nil {
		return v, err
	}
	p := auth.FromContext(r.Context())
	if _, err = s.projectAccess(r.Context(), p, v.ProjectID); err != nil {
		return v, err
	}
	if p.KeyType == "app" {
		ext := r.URL.Query().Get("external_id")
		if !validText(ext, 255) {
			return v, invalid("App reads require external_id.")
		}
		var own bool
		err = s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM identities WHERE issuer=$1 AND subject=$2 AND user_id=$3)`, "app:"+p.AppName, ext, v.AuthorID).Scan(&own)
		if err != nil {
			return v, err
		}
		if !own {
			return v, missing
		}
	}
	return v, nil
}
func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) error {
	id, err := s.project(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	status := r.URL.Query().Get("status")
	if status != "" && !validStatus(status) {
		return invalid("Unknown request status.")
	}
	ext := r.URL.Query().Get("external_id")
	if p.KeyType == "app" && !validText(ext, 255) {
		return invalid("App reads require external_id.")
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+requestFields+` FROM requests r WHERE r.project_id=$1 AND ($2='' OR r.status=$2) AND ($3<>'app' OR EXISTS(SELECT 1 FROM identities i WHERE i.user_id=r.author_id AND i.issuer=$4 AND i.subject=$5)) ORDER BY r.created_at DESC,r.id LIMIT $6 OFFSET $7`, id, status, p.KeyType, "app:"+p.AppName, ext, limit, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []request{}
	for rows.Next() {
		v, err := scanRequest(rows)
		if err != nil {
			return err
		}
		items = append(items, visibleRequest(v, p))
	}
	if err = rows.Err(); err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}

type endUser struct {
	ExternalID string `json:"external_id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
}

func appUser(ctx context.Context, tx pgx.Tx, app string, user endUser) (string, error) {
	// Serialize identity creation so concurrent submissions cannot orphan users.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "app:"+app+":"+user.ExternalID); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT user_id::text FROM identities WHERE issuer=$1 AND subject=$2`, "app:"+app, user.ExternalID).Scan(&id)
	switch err {
	case pgx.ErrNoRows:
		err = tx.QueryRow(ctx, `INSERT INTO users(email,name) VALUES($1,$2) RETURNING id::text`, user.Email, user.Name).Scan(&id)
		if err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, "app:"+app, user.ExternalID, id)
	case nil:
		_, err = tx.Exec(ctx, `UPDATE users SET email=$2,name=$3 WHERE id=$1`, id, user.Email, user.Name)
	}
	return id, err
}
func (s *Server) createRequest(w http.ResponseWriter, r *http.Request) error {
	id, err := s.project(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	if !p.Browser() && p.KeyType != "app" {
		return denied
	}
	var in struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		EndUser     *endUser `json:"end_user,omitempty"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	in.Title = strings.TrimSpace(in.Title)
	if !validText(in.Title, 200) || len(in.Description) > 20000 {
		return invalid("Title is required and limited to 200 characters; description is limited to 20000 characters.")
	}
	if p.Browser() && in.EndUser != nil {
		return invalid("Browser submissions cannot assert another user.")
	}
	if p.KeyType == "app" {
		if in.EndUser == nil || !validText(in.EndUser.ExternalID, 255) || !validText(in.EndUser.Name, 150) {
			return invalid("App submissions require external_id, email, and name.")
		}
		in.EndUser.Email, err = email(in.EndUser.Email)
		if err != nil {
			return err
		}
	}
	publicToken, err := token()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	author, source := p.UserID, "portal"
	if p.KeyType == "app" {
		source = "app:" + p.AppName
		author, err = appUser(r.Context(), tx, p.AppName, *in.EndUser)
		if err != nil {
			return err
		}
	}
	var requestID string
	err = tx.QueryRow(r.Context(), `INSERT INTO requests(project_id,title,description,author_id,source,public_token) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, id, in.Title, in.Description, author, source, publicToken).Scan(&requestID)
	if err != nil {
		return err
	}
	if err = s.enqueue(r.Context(), tx, id, "request_created", map[string]any{"request_id": requestID, "status": "open"}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	v, err := scanRequest(s.db.QueryRow(r.Context(), `SELECT `+requestFields+` FROM requests r WHERE r.id=$1`, requestID))
	if err != nil {
		return err
	}
	write(w, 201, visibleRequest(v, p))
	return nil
}
func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) error {
	v, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	v = visibleRequest(v, p)
	v.Comments, err = s.commentRows(r.Context(), v.ID, staff(p), false, 100, 0)
	if err != nil {
		return err
	}
	v.History, err = s.historyRows(r.Context(), v.ID, false, 100, 0)
	if err != nil {
		return err
	}
	if err = s.detailTotals(r.Context(), v.ID, staff(p), &v.CommentsTotal, &v.HistoryTotal); err != nil {
		return err
	}
	write(w, 200, v)
	return nil
}
func (s *Server) addComment(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !p.Browser() && p.KeyType != "assistant" {
		return denied
	}
	v, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	var in struct {
		Body     string `json:"body"`
		Internal bool   `json:"internal"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	if !validText(in.Body, 10000) {
		return invalid("Comment must contain 1 to 10000 characters.")
	}
	if in.Internal && !staff(p) {
		return denied
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	uid, kid := actor(p)
	items, err := maps(r.Context(), tx, `INSERT INTO comments(request_id,author_id,key_id,body,internal) VALUES($1,$2,$3,$4,$5) RETURNING id::text,body,internal,created_at`, v.ID, uid, kid, in.Body, in.Internal)
	if err != nil {
		return err
	}
	if !in.Internal {
		if err = s.enqueue(r.Context(), tx, v.ProjectID, "comment_created", map[string]any{"request_id": v.ID, "comment_id": items[0]["id"]}); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	items[0]["author_name"] = p.Name
	if p.KeyType == "assistant" {
		items[0]["author_name"] = "Assistant"
	}
	write(w, 201, items[0])
	return nil
}
func assistantTransition(from, to string, authorized bool) bool {
	return authorized && (from == "approved_for_build" && to == "in_progress" || from == "in_progress" && (to == "shipped" || to == "approved_for_build"))
}
func (s *Server) changeStatus(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !staff(p) {
		return denied
	}
	v, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	var in struct {
		Status string `json:"status"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	if !validStatus(in.Status) {
		return invalid("Unknown request status.")
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var from string
	var authorized bool
	if err = tx.QueryRow(r.Context(), `SELECT status,build_authorized FROM requests WHERE id=$1 FOR UPDATE`, v.ID).Scan(&from, &authorized); err != nil {
		return err
	}
	if !p.Admin() && !assistantTransition(from, in.Status, authorized) {
		return denied
	}
	if from == in.Status {
		write(w, 200, visibleRequest(v, p))
		return nil
	}
	if p.Admin() && in.Status == "approved_for_build" {
		authorized = true
	}
	if in.Status != "approved_for_build" && in.Status != "in_progress" {
		authorized = false
	}
	if _, err = tx.Exec(r.Context(), `UPDATE requests SET status=$2,build_authorized=$3,updated_at=now() WHERE id=$1`, v.ID, in.Status, authorized); err != nil {
		return err
	}
	uid, kid := actor(p)
	if _, err = tx.Exec(r.Context(), `INSERT INTO status_history(request_id,from_status,to_status,actor_id,key_id) VALUES($1,$2,$3,$4,$5)`, v.ID, from, in.Status, uid, kid); err != nil {
		return err
	}
	if err = s.enqueue(r.Context(), tx, v.ProjectID, "status_changed", map[string]any{"request_id": v.ID, "from_status": from, "status": in.Status}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	v, err = scanRequest(s.db.QueryRow(r.Context(), `SELECT `+requestFields+` FROM requests r WHERE r.id=$1`, v.ID))
	if err != nil {
		return err
	}
	write(w, 200, visibleRequest(v, p))
	return nil
}
func (s *Server) vote(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if !p.Browser() {
		return denied
	}
	v, err := s.loadRequest(r)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPut {
		_, err = s.db.Exec(r.Context(), `INSERT INTO votes(request_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, v.ID, p.UserID)
	} else {
		_, err = s.db.Exec(r.Context(), `DELETE FROM votes WHERE request_id=$1 AND user_id=$2`, v.ID, p.UserID)
	}
	if err != nil {
		return err
	}
	var count int64
	if err = s.db.QueryRow(r.Context(), `SELECT count(*) FROM votes WHERE request_id=$1`, v.ID).Scan(&count); err != nil {
		return err
	}
	write(w, 200, map[string]any{"votes": count})
	return nil
}
func (s *Server) publicRequest(w http.ResponseWriter, r *http.Request) error {
	id, title, status, err := s.publicIdentity(r)
	if err != nil {
		return err
	}
	comments, err := s.commentRows(r.Context(), id, false, true, 100, 0)
	if err != nil {
		return err
	}
	history, err := s.historyRows(r.Context(), id, true, 100, 0)
	if err != nil {
		return err
	}
	var commentsTotal, historyTotal int64
	if err = s.detailTotals(r.Context(), id, false, &commentsTotal, &historyTotal); err != nil {
		return err
	}
	write(w, 200, map[string]any{"title": title, "status": status, "comments": comments, "history": history, "comments_total": commentsTotal, "history_total": historyTotal})
	return nil
}
