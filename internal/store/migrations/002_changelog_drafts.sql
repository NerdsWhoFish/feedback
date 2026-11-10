ALTER TABLE changelog ADD COLUMN published_at timestamptz;
UPDATE changelog SET published_at=created_at;
ALTER TABLE changelog ADD COLUMN automatic_request_id uuid UNIQUE REFERENCES requests(id);
CREATE INDEX changelog_project_created ON changelog(project_id,created_at DESC,id);
