import { useState } from "react";
import { api, useAction, useResource } from "./api";
import { DateLabel, Empty, Field, Loading, Notice, Pagination } from "./ui";
import type { ChangelogEntry, Page, Project, RequestItem } from "./types";

export function Changelog({
  project,
  admin,
}: {
  project: Project;
  admin: boolean;
}) {
  const [offset, setOffset] = useState(0);
  const [editing, setEditing] = useState<ChangelogEntry | "new" | null>(null);
  const [success, setSuccess] = useState("");
  const resource = useResource<Page<ChangelogEntry>>(
    `/api/v1/projects/${project.id}/changelog?limit=20&offset=${offset}`,
  );
  return (
    <section>
      <div className="section-heading">
        <h2>Changelog</h2>
        {admin && !editing && (
          <button className="primary" onClick={() => setEditing("new")}>
            + Add update
          </button>
        )}
      </div>
      {admin && (
        <p className="hint">
          Shipping a request creates a draft here. Review it, then publish when
          it is ready for members.
        </p>
      )}
      {editing && (
        <EntryEditor
          key={editing === "new" ? "new" : editing.id}
          project={project}
          entry={editing === "new" ? undefined : editing}
          onCancel={() => setEditing(null)}
          onSaved={(published) => {
            setEditing(null);
            setSuccess(
              published
                ? "Update published."
                : "Draft saved. Only staff can see it.",
            );
            setOffset(0);
            resource.reload();
          }}
        />
      )}
      <Notice error={resource.error} success={success} />
      {resource.loading ? (
        <Loading />
      ) : resource.data?.items.length ? (
        <div className="changelog-list">
          {resource.data.items.map((entry) => (
            <article className="changelog-entry" key={entry.id}>
              <div>
                <span className="eyebrow">
                  {entry.published_at ? "PUBLISHED" : "DRAFT"}
                </span>
                <DateLabel value={entry.published_at ?? entry.created_at} />
                {!entry.published_at && (
                  <span className="hint">Staff only</span>
                )}
                {admin && (
                  <button
                    className="small"
                    disabled={editing !== null}
                    onClick={() => {
                      setSuccess("");
                      setEditing(entry);
                    }}
                  >
                    {entry.published_at ? "Edit update" : "Review draft"}
                  </button>
                )}
              </div>
              <section>
                <h3>{entry.title}</h3>
                <p className="prose">{entry.body}</p>
                {entry.request_ids?.length > 0 && (
                  <div className="linked-requests">
                    {entry.request_ids.map((id, index) => (
                      <a
                        key={id}
                        href={`#${new URLSearchParams({ project: project.id, view: "requests", request: id })}`}
                      >
                        Related request {index + 1} ↗
                      </a>
                    ))}
                  </div>
                )}
              </section>
            </article>
          ))}
        </div>
      ) : (
        !resource.error && (
          <Empty title="No updates yet">
            {admin
              ? "Select Add update to create a changelog entry."
              : "Published updates will appear here."}
          </Empty>
        )
      )}
      <Pagination
        offset={offset}
        count={resource.data?.items.length ?? 0}
        onChange={setOffset}
      />
    </section>
  );
}

function EntryEditor({
  project,
  entry,
  onCancel,
  onSaved,
}: {
  project: Project;
  entry?: ChangelogEntry;
  onCancel: () => void;
  onSaved: (published: boolean) => void;
}) {
  const shipped = useResource<Page<RequestItem>>(
    entry
      ? null
      : `/api/v1/projects/${project.id}/requests?status=shipped&limit=100`,
  );
  const [title, setTitle] = useState(entry?.title ?? "");
  const [body, setBody] = useState(entry?.body ?? "");
  const [links, setLinks] = useState<string[]>([]);
  const action = useAction();
  return (
    <form
      className="panel form-panel"
      onSubmit={(event) => {
        event.preventDefault();
        const submitter = (event.nativeEvent as SubmitEvent)
          .submitter as HTMLButtonElement | null;
        const published =
          !!entry?.published_at || submitter?.value === "publish";
        void action.run(async () => {
          const base = `/api/v1/projects/${project.id}/changelog`;
          await api(
            entry ? `${base}/${entry.id}` : base,
            entry ? "PATCH" : "POST",
            {
              title,
              body,
              published,
              ...(!entry ? { request_ids: links } : {}),
            },
          );
          onSaved(published);
        });
      }}
    >
      <h3>
        {entry?.published_at
          ? "Edit release update"
          : "Prepare a release update"}
      </h3>
      <Field label="Title">
        <input
          autoFocus
          required
          maxLength={200}
          value={title}
          onChange={(event) => setTitle(event.target.value)}
        />
      </Field>
      <Field label="What changed?">
        <textarea
          required
          maxLength={20000}
          rows={5}
          value={body}
          onChange={(event) => setBody(event.target.value)}
        />
      </Field>
      {!entry && (
        <fieldset>
          <legend>Link shipped requests</legend>
          <Notice error={shipped.error} />
          {shipped.data?.items.map((request) => (
            <label className="checkbox" key={request.id}>
              <input
                type="checkbox"
                checked={links.includes(request.id)}
                onChange={(event) =>
                  setLinks(
                    event.target.checked
                      ? [...links, request.id]
                      : links.filter((id) => id !== request.id),
                  )
                }
              />
              {request.title}
            </label>
          ))}
          {!shipped.loading && !shipped.data?.items.length && (
            <p className="hint">No shipped requests to link yet.</p>
          )}
        </fieldset>
      )}
      <Notice error={action.error} />
      <div className="form-actions">
        <button disabled={action.busy} value="save">
          {action.busy
            ? "Saving..."
            : entry?.published_at
              ? "Save changes"
              : "Save draft"}
        </button>
        {!entry?.published_at && (
          <button className="primary" disabled={action.busy} value="publish">
            Publish update
          </button>
        )}
        <button
          type="button"
          className="quiet"
          disabled={action.busy}
          onClick={onCancel}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}
