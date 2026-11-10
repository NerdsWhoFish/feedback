package api

import (
	"net/http"
	"strings"

	"github.com/NerdsWhoFish/feedback/internal/auth"
)

const projectFields = `id::text,slug,name,description,public_status_page`

type projectInput struct {
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	PublicStatusPage bool   `json:"public_status_page"`
}

func (p projectInput) valid() bool {
	return validSlug(p.Slug) && validText(p.Name, 150) && len(p.Description) <= 5000
}
func validSlug(slug string) bool {
	return slugPattern.MatchString(slug) && !idPattern.MatchString(slug)
}
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `SELECT `+projectFields+` FROM projects p WHERE $1 OR ($2<>'' AND EXISTS(SELECT 1 FROM memberships m WHERE m.project_id=p.id AND m.email=$2)) OR ($3<>'' AND EXISTS(SELECT 1 FROM key_projects k WHERE k.project_id=p.id AND k.key_id::text=$3)) ORDER BY name,id LIMIT $4 OFFSET $5`, p.Admin(), browserEmail(p), p.KeyID, limit, offset)
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	var in projectInput
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if !in.valid() {
		return invalid("Use a lowercase project slug, a name up to 150 characters, and a description up to 5000 characters.")
	}
	rows, err := maps(r.Context(), s.db, `INSERT INTO projects(slug,name,description,public_status_page) VALUES($1,$2,$3,$4) RETURNING `+projectFields, in.Slug, strings.TrimSpace(in.Name), in.Description, in.PublicStatusPage)
	if err != nil {
		return err
	}
	write(w, 201, rows[0])
	return nil
}
func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	var in struct {
		Slug             *string `json:"slug"`
		Name             *string `json:"name"`
		Description      *string `json:"description"`
		PublicStatusPage *bool   `json:"public_status_page"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	if in.Slug != nil && !validSlug(*in.Slug) || in.Name != nil && !validText(*in.Name, 150) || in.Description != nil && len(*in.Description) > 5000 {
		return invalid("Invalid project fields.")
	}
	rows, err := maps(r.Context(), s.db, `UPDATE projects SET slug=COALESCE($2,slug),name=COALESCE($3,name),description=COALESCE($4,description),public_status_page=COALESCE($5,public_status_page) WHERE id=$1 RETURNING `+projectFields, id, in.Slug, in.Name, in.Description, in.PublicStatusPage)
	if err != nil {
		return err
	}
	write(w, 200, rows[0])
	return nil
}
func (s *Server) listMemberships(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `SELECT email FROM memberships WHERE project_id=$1 ORDER BY email LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
func (s *Server) addMembership(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	var in struct {
		Email string `json:"email"`
	}
	if err = decode(w, r, &in); err != nil {
		return err
	}
	in.Email, err = email(in.Email)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(r.Context(), `INSERT INTO memberships(project_id,email) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, in.Email)
	if err != nil {
		return err
	}
	write(w, 201, in)
	return nil
}
func (s *Server) deleteMembership(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id, err := s.project(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(r.Context(), `DELETE FROM memberships WHERE project_id=$1 AND email=$2`, id, strings.ToLower(r.PathValue("email")))
	if err != nil {
		return err
	}
	write(w, 204, nil)
	return nil
}
