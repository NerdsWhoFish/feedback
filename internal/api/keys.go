package api

import "net/http"

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	limit, offset, err := pagination(r)
	if err != nil {
		return err
	}
	items, err := maps(r.Context(), s.db, `SELECT k.id::text,k.name,k.key_type,k.app_name,k.created_at,k.revoked_at,ARRAY(SELECT p.project_id::text FROM key_projects p WHERE p.key_id=k.id ORDER BY p.project_id) AS project_ids FROM api_keys k ORDER BY k.created_at DESC,k.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return err
	}
	page(w, items, limit, offset)
	return nil
}
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	var in struct {
		Name       string   `json:"name"`
		KeyType    string   `json:"key_type"`
		AppName    string   `json:"app_name"`
		ProjectIDs []string `json:"project_ids"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if !validText(in.Name, 150) || (in.KeyType != "app" && in.KeyType != "assistant") || len(in.ProjectIDs) < 1 || len(in.ProjectIDs) > 100 {
		return invalid("A key needs a name, an app or assistant type, and 1 to 100 projects.")
	}
	if in.KeyType == "app" && !slugPattern.MatchString(in.AppName) {
		return invalid("An app key needs a lowercase app name.")
	}
	if in.KeyType == "assistant" && in.AppName != "" {
		return invalid("Assistant keys cannot specify an app name.")
	}
	for _, id := range in.ProjectIDs {
		if !idPattern.MatchString(id) {
			return invalid("Project IDs must be UUIDs.")
		}
	}
	raw, err := token()
	if err != nil {
		return err
	}
	raw = "nwf_" + raw
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	items, err := maps(r.Context(), tx, `INSERT INTO api_keys(name,key_type,app_name,key_hash) VALUES($1,$2,$3,$4) RETURNING id::text,name,key_type,app_name,created_at,revoked_at`, in.Name, in.KeyType, in.AppName, hash(raw))
	if err != nil {
		return err
	}
	for _, id := range in.ProjectIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO key_projects(key_id,project_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, items[0]["id"], id); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	items[0]["token"] = raw
	items[0]["project_ids"] = in.ProjectIDs
	write(w, 201, items[0])
	return nil
}
func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) error {
	if err := admin(r); err != nil {
		return err
	}
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		return missing
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return missing
	}
	write(w, 204, nil)
	return nil
}
