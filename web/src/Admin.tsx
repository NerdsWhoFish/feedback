import { useState } from "react";
import { api, useAction, useResource } from "./api";
import { DateLabel, Empty, Field, Loading, Notice, Pagination } from "./ui";
import type {
  APIKey,
  Delivery,
  Membership,
  Page,
  Project,
  Webhook,
} from "./types";

type AdminTab = "projects" | "members" | "keys" | "webhooks";
export function Admin({
  project,
  projects,
  onProjectsChanged,
}: {
  project?: Project;
  projects: Project[];
  onProjectsChanged: () => void;
}) {
  const [tab, setTab] = useState<AdminTab>("projects");
  const tabs: { id: AdminTab; label: string }[] = [
    { id: "projects", label: "Projects" },
    { id: "members", label: "Memberships" },
    { id: "keys", label: "API keys" },
    { id: "webhooks", label: "Webhooks" },
  ];
  return (
    <section>
      <div className="project-heading">
        <h1>Administration</h1>
      </div>
      <nav className="tabs" aria-label="Administration">
        {tabs.map((item) => (
          <button
            key={item.id}
            className={tab === item.id ? "active" : ""}
            aria-current={tab === item.id ? "page" : undefined}
            onClick={() => setTab(item.id)}
          >
            {item.label}
          </button>
        ))}
      </nav>
      {tab === "projects" ? (
        <Projects
          key={project?.id}
          project={project}
          onSaved={onProjectsChanged}
        />
      ) : tab === "keys" ? (
        <Keys projects={projects} />
      ) : project ? (
        tab === "members" ? (
          <Members key={project.id} project={project} />
        ) : (
          <Webhooks key={project.id} project={project} />
        )
      ) : (
        <Empty title="Choose a project first">
          Create a project before adding members or connecting webhooks.
        </Empty>
      )}
    </section>
  );
}

function Projects({
  project,
  onSaved,
}: {
  project?: Project;
  onSaved: () => void;
}) {
  const [creating, setCreating] = useState(!project);
  return (
    <>
      <div className="section-heading">
        <h2>{creating ? "New project" : project?.name}</h2>
        {project && (
          <button onClick={() => setCreating((v) => !v)}>
            {creating ? "Edit current project" : "+ New project"}
          </button>
        )}
      </div>
      <ProjectForm
        key={creating ? "new" : project?.id}
        project={creating ? undefined : project}
        onSaved={() => {
          onSaved();
          if (!project) setCreating(true);
        }}
      />
    </>
  );
}

function ProjectForm({
  project,
  onSaved,
}: {
  project?: Project;
  onSaved: () => void;
}) {
  const [name, setName] = useState(project?.name ?? "");
  const [slug, setSlug] = useState(project?.slug ?? "");
  const [description, setDescription] = useState(project?.description ?? "");
  const [publicStatus, setPublicStatus] = useState(
    project?.public_status_page ?? false,
  );
  const action = useAction();
  return (
    <form
      className="panel form-panel"
      onSubmit={(event) => {
        event.preventDefault();
        void action.run(
          async () => {
            await api(
              project ? `/api/v1/projects/${project.id}` : "/api/v1/projects",
              project ? "PATCH" : "POST",
              { name, slug, description, public_status_page: publicStatus },
            );
            onSaved();
            if (!project) {
              setName("");
              setSlug("");
              setDescription("");
              setPublicStatus(false);
            }
          },
          project
            ? "Project updated."
            : "Project created. Select it in the project picker.",
        );
      }}
    >
      <Field label="Project name">
        <input
          required
          maxLength={120}
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </Field>
      <Field
        label="Project slug"
        hint="Lowercase letters, numbers, and hyphens."
      >
        <input
          required
          pattern="[a-z0-9]+(-[a-z0-9]+)*"
          maxLength={63}
          value={slug}
          onChange={(event) => setSlug(event.target.value)}
        />
      </Field>
      <Field label="Description">
        <textarea
          rows={3}
          maxLength={2000}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>
      <label className="checkbox">
        <input
          type="checkbox"
          checked={publicStatus}
          onChange={(event) => setPublicStatus(event.target.checked)}
        />
        Enable shareable public status pages
      </label>
      <p className="hint">
        Anyone with a request's private link can see its title, status, public
        comments, and progress. Author details and staff comments stay private.
      </p>
      <Notice error={action.error} success={action.success} />
      <button className="primary" disabled={action.busy}>
        {action.busy
          ? "Saving..."
          : project
            ? "Save project"
            : "Create project"}
      </button>
    </form>
  );
}

function Members({ project }: { project: Project }) {
  const [offset, setOffset] = useState(0);
  const [email, setEmail] = useState("");
  const action = useAction();
  const resource = useResource<Page<Membership>>(
    `/api/v1/projects/${project.id}/memberships?limit=20&offset=${offset}`,
  );
  return (
    <>
      <h2>People in {project.name}</h2>
      <p className="muted">
        Membership grants access to this project's requests and changelog.
        Administrators already see every project.
      </p>
      <form
        className="inline-form panel"
        onSubmit={(event) => {
          event.preventDefault();
          void action.run(async () => {
            await api(`/api/v1/projects/${project.id}/memberships`, "POST", {
              email,
            });
            setEmail("");
            resource.reload();
          }, "Membership added. The person can now sign in with this address.");
        }}
      >
        <Field label="Email">
          <input
            type="email"
            required
            maxLength={254}
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder="person@example.com"
          />
        </Field>
        <button className="primary" disabled={action.busy}>
          Add member
        </button>
      </form>
      <Notice error={action.error ?? resource.error} success={action.success} />
      {resource.loading ? (
        <Loading />
      ) : resource.data?.items.length ? (
        <ul className="admin-list">
          {resource.data.items.map((member) => (
            <li key={member.email}>
              <strong>{member.email}</strong>
              <button
                className="danger quiet"
                disabled={action.busy}
                onClick={() => {
                  if (
                    window.confirm(
                      `Remove ${member.email} from ${project.name}?`,
                    )
                  )
                    void action.run(async () => {
                      await api(
                        `/api/v1/projects/${project.id}/memberships/${encodeURIComponent(member.email)}`,
                        "DELETE",
                      );
                      resource.reload();
                    }, "Membership removed.");
                }}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <Empty title="No members yet">
          Add the email for each person who should have access.
        </Empty>
      )}
      <Pagination
        offset={offset}
        count={resource.data?.items.length ?? 0}
        onChange={setOffset}
      />
    </>
  );
}

function Secret({
  value,
  label,
  onDismiss,
}: {
  value: string;
  label: string;
  onDismiss: () => void;
}) {
  const action = useAction();
  return (
    <section className="secret panel" aria-label={label}>
      <h3>{label}</h3>
      <p>Copy this now. It will not be shown again.</p>
      <code>{value}</code>
      <div className="button-row">
        <button
          onClick={() =>
            void action.run(async () => {
              await navigator.clipboard.writeText(value);
            }, "Copied.")
          }
        >
          Copy
        </button>
        <button onClick={onDismiss}>I have saved it</button>
      </div>
      <Notice error={action.error} success={action.success} />
    </section>
  );
}

function Keys({ projects }: { projects: Project[] }) {
  const [offset, setOffset] = useState(0);
  const resource = useResource<Page<APIKey>>(
    `/api/v1/keys?limit=20&offset=${offset}`,
  );
  const [name, setName] = useState("");
  const [type, setType] = useState<"app" | "assistant">("app");
  const [appName, setAppName] = useState("");
  const [projectIDs, setProjectIDs] = useState<string[]>([]);
  const [token, setToken] = useState("");
  const action = useAction();
  return (
    <>
      <h2>API keys</h2>
      <p className="muted">
        Application keys submit on behalf of their users. Assistant keys work
        only on approved requests. Both are limited to the projects you select.
      </p>
      {token && (
        <Secret
          value={token}
          label="Your new API key"
          onDismiss={() => setToken("")}
        />
      )}
      <form
        className="panel form-panel"
        onSubmit={(event) => {
          event.preventDefault();
          void action.run(async () => {
            const result = await api<APIKey & { token: string }>(
              "/api/v1/keys",
              "POST",
              {
                name,
                key_type: type,
                app_name: type === "app" ? appName : "",
                project_ids: projectIDs,
              },
            );
            setToken(result.token);
            setName("");
            setAppName("");
            setProjectIDs([]);
            resource.reload();
          }, "Key created. Copy it from the box above.");
        }}
      >
        <h3>Create an API key</h3>
        <div className="form-grid">
          <Field label="Key name">
            <input
              required
              maxLength={120}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="Customer portal production"
            />
          </Field>
          <Field label="Key type">
            <select
              value={type}
              onChange={(event) =>
                setType(event.target.value as "app" | "assistant")
              }
            >
              <option value="app">Application</option>
              <option value="assistant">Assistant</option>
            </select>
          </Field>
        </div>
        {type === "app" && (
          <Field
            label="Application name"
            hint="A stable lowercase slug. Use the same name when rotating a key to retain user attribution."
          >
            <input
              required
              pattern="[a-z0-9]+(-[a-z0-9]+)*"
              maxLength={63}
              value={appName}
              onChange={(event) => setAppName(event.target.value)}
              placeholder="my-app"
            />
          </Field>
        )}
        <fieldset>
          <legend>Allowed projects</legend>
          {projects.map((project) => (
            <label className="checkbox" key={project.id}>
              <input
                type="checkbox"
                checked={projectIDs.includes(project.id)}
                onChange={(event) =>
                  setProjectIDs(
                    event.target.checked
                      ? [...projectIDs, project.id]
                      : projectIDs.filter((id) => id !== project.id),
                  )
                }
              />
              {project.name}
            </label>
          ))}
          {!projects.length && <p className="hint">Create a project first.</p>}
        </fieldset>
        <button
          className="primary"
          disabled={action.busy || !projectIDs.length}
        >
          {action.busy ? "Creating..." : "Create key"}
        </button>
      </form>
      <Notice error={action.error ?? resource.error} success={action.success} />
      {resource.loading ? (
        <Loading />
      ) : resource.data?.items.length ? (
        <ul className="admin-list">
          {resource.data.items.map((key) => (
            <li key={key.id}>
              <div>
                <strong>{key.name}</strong>
                <p className="hint">
                  {key.key_type === "app"
                    ? `Application: ${key.app_name}`
                    : "Assistant"}{" "}
                  · <DateLabel value={key.created_at} />
                  {key.revoked_at ? " · Revoked" : ""}
                </p>
                <p className="hint">
                  {key.project_ids
                    ?.map(
                      (id) =>
                        projects.find((project) => project.id === id)?.name ??
                        "Project",
                    )
                    .join(", ")}
                </p>
              </div>
              <button
                className="danger quiet"
                disabled={action.busy || !!key.revoked_at}
                onClick={() => {
                  if (
                    window.confirm(
                      `Revoke ${key.name}? Applications using this key will immediately lose access.`,
                    )
                  )
                    void action.run(async () => {
                      await api(`/api/v1/keys/${key.id}`, "DELETE");
                      resource.reload();
                    }, "Key revoked.");
                }}
              >
                Revoke
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <Empty title="No keys created yet">
          Create a key to connect an application or assistant.
        </Empty>
      )}
      <Pagination
        offset={offset}
        count={resource.data?.items.length ?? 0}
        onChange={setOffset}
      />
    </>
  );
}

const webhookEvents = ["request_created", "status_changed", "comment_created"];
function Webhooks({ project }: { project: Project }) {
  const [offset, setOffset] = useState(0);
  const resource = useResource<Page<Webhook>>(
    `/api/v1/projects/${project.id}/webhooks?limit=20&offset=${offset}`,
  );
  const [url, setURL] = useState("");
  const [events, setEvents] = useState<string[]>(["status_changed"]);
  const [secret, setSecret] = useState("");
  const action = useAction();
  return (
    <>
      <h2>Webhooks</h2>
      <p className="muted">
        Send signed events to a public HTTPS endpoint. Failed deliveries retry
        automatically.
      </p>
      {secret && (
        <Secret
          value={secret}
          label="Your webhook signing secret"
          onDismiss={() => setSecret("")}
        />
      )}
      <form
        className="panel form-panel"
        onSubmit={(event) => {
          event.preventDefault();
          void action.run(async () => {
            const result = await api<Webhook & { secret: string }>(
              `/api/v1/projects/${project.id}/webhooks`,
              "POST",
              { url, events },
            );
            setSecret(result.secret);
            setURL("");
            resource.reload();
          }, "Webhook created. Save the signing secret above.");
        }}
      >
        <Field label="Delivery URL">
          <input
            type="url"
            required
            pattern="https://.*"
            maxLength={2048}
            value={url}
            onChange={(event) => setURL(event.target.value)}
            placeholder="https://your-app.com/webhooks/feedback"
          />
        </Field>
        <fieldset>
          <legend>Send these events</legend>
          {webhookEvents.map((eventName) => (
            <label className="checkbox" key={eventName}>
              <input
                type="checkbox"
                checked={events.includes(eventName)}
                onChange={(event) =>
                  setEvents(
                    event.target.checked
                      ? [...events, eventName]
                      : events.filter((item) => item !== eventName),
                  )
                }
              />
              {eventName.replaceAll("_", " ")}
            </label>
          ))}
        </fieldset>
        <button className="primary" disabled={action.busy || !events.length}>
          {action.busy ? "Creating..." : "Create webhook"}
        </button>
      </form>
      <Notice error={action.error ?? resource.error} success={action.success} />
      {resource.loading ? (
        <Loading />
      ) : resource.data?.items.length ? (
        <ul className="admin-list">
          {resource.data.items.map((webhook) => (
            <li key={webhook.id}>
              <div>
                <strong>{webhook.url}</strong>
                <p className="hint">
                  {webhook.events.join(", ")}
                  {webhook.disabled_at ? " · Removed" : ""}
                </p>
              </div>
              <button
                className="danger quiet"
                disabled={action.busy || !!webhook.disabled_at}
                onClick={() => {
                  if (
                    window.confirm(
                      "Remove this webhook and stop future deliveries?",
                    )
                  )
                    void action.run(async () => {
                      await api(
                        `/api/v1/projects/${project.id}/webhooks/${webhook.id}`,
                        "DELETE",
                      );
                      resource.reload();
                    }, "Webhook removed.");
                }}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted">No webhooks connected.</p>
      )}
      <Pagination
        offset={offset}
        count={resource.data?.items.length ?? 0}
        onChange={setOffset}
      />
      <Deliveries projectID={project.id} />
    </>
  );
}

function Deliveries({ projectID }: { projectID: string }) {
  const [offset, setOffset] = useState(0);
  const resource = useResource<Page<Delivery>>(
    `/api/v1/projects/${projectID}/deliveries?limit=20&offset=${offset}`,
  );
  return (
    <section className="delivery-section">
      <div className="section-heading">
        <h3>Delivery log</h3>
        <button disabled={resource.loading} onClick={resource.reload}>
          Refresh
        </button>
      </div>
      <Notice error={resource.error} />
      {resource.loading ? (
        <Loading />
      ) : resource.data?.items.length ? (
        <ul className="admin-list">
          {resource.data.items.map((delivery) => (
            <li key={delivery.id}>
              <div>
                <strong>
                  {delivery.event_type ?? delivery.event ?? "Project event"}
                </strong>
                <p className="hint">
                  <DateLabel value={delivery.created_at} /> ·{" "}
                  {delivery.attempts} attempt
                  {delivery.attempts === 1 ? "" : "s"}
                </p>
              </div>
              <span className="tag">
                {delivery.last_error === "delivered" || delivery.delivered_at
                  ? "Delivered"
                  : (delivery.status ??
                    (delivery.completed_at ? "Failed" : "Pending"))}
                {(delivery.status_code ?? delivery.last_status)
                  ? ` · HTTP ${delivery.status_code ?? delivery.last_status}`
                  : ""}
              </span>
              {!!delivery.delivery_log?.length && (
                <details className="delivery-attempts">
                  <summary>Attempts</summary>
                  <ol>
                    {delivery.delivery_log.map((attempt) => (
                      <li key={attempt.attempt}>
                        Attempt {attempt.attempt}:{" "}
                        {attempt.outcome.replaceAll("_", " ")}
                        {attempt.status
                          ? ` (HTTP ${attempt.status})`
                          : ""} · <DateLabel value={attempt.created_at} />
                      </li>
                    ))}
                  </ol>
                </details>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted">
          Deliveries appear here when your project has a new event.
        </p>
      )}
      <Pagination
        offset={offset}
        count={resource.data?.items.length ?? 0}
        onChange={setOffset}
      />
    </section>
  );
}
