import { useState } from "react";
import { api, useAction, usePageTail, useResource } from "./api";
import { RequestSubscription } from "./Notifications";
import {
  DateLabel,
  Empty,
  Field,
  Loading,
  Notice,
  Pagination,
  StatusBadge,
} from "./ui";
import {
  statuses,
  statusLabels,
  type Page,
  type Project,
  type RequestItem,
  type Status,
} from "./types";

export function Requests({
  project,
  onOpen,
}: {
  project: Project;
  onOpen: (id: string) => void;
}) {
  const [status, setStatus] = useState("");
  const [offset, setOffset] = useState(0);
  const [creating, setCreating] = useState(false);
  const resource = useResource<Page<RequestItem>>(
    `/api/v1/projects/${project.id}/requests?limit=20&offset=${offset}&status=${status}`,
  );
  return (
    <section>
      <div className="section-heading">
        <h2>Feature requests</h2>
        <button className="primary" onClick={() => setCreating((v) => !v)}>
          {creating ? "Close form" : "+ New request"}
        </button>
      </div>
      {creating && (
        <SubmitRequest
          projectID={project.id}
          onCreated={(request) => {
            setCreating(false);
            onOpen(request.id);
          }}
        />
      )}
      <div className="list-toolbar">
        <label>
          Show{" "}
          <select
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              setOffset(0);
            }}
          >
            <option value="">All statuses</option>
            {statuses.map((value) => (
              <option key={value} value={value}>
                {statusLabels[value]}
              </option>
            ))}
          </select>
        </label>
      </div>
      <Notice error={resource.error} />
      {resource.loading ? (
        <Loading />
      ) : resource.data?.items.length ? (
        <div className="request-list">
          {resource.data.items.map((request, index) => (
            <a
              className="request-row"
              key={request.id}
              href={`#${new URLSearchParams({ project: project.id, view: "requests", request: request.id })}`}
            >
              <span className="request-index" aria-hidden="true">
                {String(offset + index + 1).padStart(2, "0")}
              </span>
              <span className="request-info">
                <span className="request-title">{request.title}</span>
                <span className="request-excerpt">{request.description}</span>
                <span className="request-date">
                  <DateLabel value={request.created_at} />
                </span>
              </span>
              <span className="request-meta">
                <StatusBadge status={request.status} />
                <span className="vote-count">
                  <span aria-hidden="true">↑</span> {request.votes}{" "}
                  <span className="sr-only">votes</span>
                </span>
              </span>
              <span className="request-arrow" aria-hidden="true">
                ↗
              </span>
            </a>
          ))}
        </div>
      ) : (
        !resource.error && (
          <Empty
            title={
              status
                ? "No requests with this status"
                : "No requests yet"
            }
          >
            {status
              ? "Try a different filter to see more requests."
              : "Select New request to create a request for this project."}
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

function SubmitRequest({
  projectID,
  onCreated,
}: {
  projectID: string;
  onCreated: (request: RequestItem) => void;
}) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const action = useAction();
  return (
    <form
      className="panel form-panel"
      onSubmit={(event) => {
        event.preventDefault();
        void action.run(
          async () => {
            const request = await api<RequestItem>(
              `/api/v1/projects/${projectID}/requests`,
              "POST",
              { title, description },
            );
            onCreated(request);
          },
          "Request submitted.",
          "request.submit",
        );
      }}
    >
      <h3>New request</h3>
      <Field label="Request title">
        <input
          autoFocus
          required
          maxLength={200}
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          placeholder="A short, specific summary"
        />
      </Field>
      <Field
        label="What do you need, and why?"
        hint="Describe the problem, who it affects, and what a useful outcome looks like."
      >
        <textarea
          required
          maxLength={10000}
          rows={5}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </Field>
      <Notice error={action.error} />
      <button className="primary" disabled={action.busy}>
        {action.busy ? "Submitting..." : "Submit request"}
      </button>
    </form>
  );
}

export function RequestDetail({
  id,
  project,
  admin,
  onBack,
}: {
  id: string;
  project: Project;
  admin: boolean;
  onBack: () => void;
}) {
  const resource = useResource<RequestItem>(`/api/v1/requests/${id}`);
  const action = useAction();
  const [body, setBody] = useState("");
  const [internal, setInternal] = useState(false);
  const [nextStatus, setNextStatus] = useState<Status | "">("");
  const [copied, setCopied] = useState(false);
  const request = resource.data;
  const comments = usePageTail(
    `/api/v1/requests/${id}/comments`,
    request?.comments,
    request?.comments_total,
  );
  const history = usePageTail(
    `/api/v1/requests/${id}/history`,
    request?.history,
    request?.history_total,
  );
  const publicURL =
    request?.public_token && project.public_status_page
      ? `${location.origin}/s/${request.public_token}`
      : "";
  return (
    <section>
      <button className="back-link" onClick={onBack}>
        ← All requests
      </button>
      <Notice error={resource.error} />
      {resource.loading ? (
        <Loading />
      ) : (
        request && (
          <>
            <div className="detail-heading">
              <div>
                <StatusBadge status={request.status} />
                <h2>{request.title}</h2>
                <p className="muted">
                  Opened <DateLabel value={request.created_at} /> ·{" "}
                  {request.source.startsWith("app:")
                    ? `Via ${request.source.slice(4)}`
                    : request.source === "assistant"
                      ? "Via assistant"
                      : "Via the portal"}
                </p>
              </div>
              <div className="vote-actions">
                <strong>{request.votes}</strong>
                <span>votes</span>
                <button
                  disabled={action.busy}
                  onClick={() =>
                    void action.run(
                      async () => {
                        await api(`/api/v1/requests/${id}/vote`, "PUT");
                        resource.reload();
                      },
                      "Your vote is counted.",
                      "vote.update",
                    )
                  }
                >
                  ↑ Vote
                </button>
                <button
                  className="quiet small"
                  disabled={action.busy}
                  onClick={() =>
                    void action.run(
                      async () => {
                        await api(`/api/v1/requests/${id}/vote`, "DELETE");
                        resource.reload();
                      },
                      "Your vote was removed.",
                      "vote.update",
                    )
                  }
                >
                  Remove vote
                </button>
              </div>
            </div>
            <p className="prose request-description">{request.description}</p>
            <RequestSubscription key={`${id}:${request.votes}`} id={id} />
            {publicURL && (
              <div className="share-link">
                <div>
                  <span className="eyebrow">PUBLIC STATUS LINK</span>
                  <a href={publicURL} target="_blank" rel="noreferrer">
                    Open status page ↗
                  </a>
                </div>
                <button
                  onClick={() =>
                    void action.run(async () => {
                      await navigator.clipboard.writeText(publicURL);
                      setCopied(true);
                    }, "Status link copied.")
                  }
                >
                  {copied ? "Copied" : "Copy link"}
                </button>
              </div>
            )}
            <Notice error={action.error} success={action.success} />
            {admin && (
              <form
                className="panel status-editor"
                onSubmit={(event) => {
                  event.preventDefault();
                  if (
                    nextStatus === "approved_for_build" &&
                    !window.confirm(
                      "Approve this request for implementation? This authorizes the assistant to begin work.",
                    )
                  )
                    return;
                  void action.run(
                    async () => {
                      await api(`/api/v1/requests/${id}/status`, "POST", {
                        status: nextStatus,
                      });
                      setNextStatus("");
                      resource.reload();
                    },
                    "Status updated.",
                    "status.update",
                  );
                }}
              >
                <div>
                  <h3>Request status</h3>
                  <p className="hint">
                    Only an administrator can authorize a build.
                  </p>
                </div>
                <Field label="New status">
                  <select
                    required
                    value={nextStatus}
                    onChange={(event) =>
                      setNextStatus(event.target.value as Status)
                    }
                  >
                    <option value="">Choose a status</option>
                    {statuses
                      .filter((value) => value !== request.status)
                      .map((value) => (
                        <option key={value} value={value}>
                          {statusLabels[value]}
                        </option>
                      ))}
                  </select>
                </Field>
                <button disabled={action.busy || !nextStatus}>
                  Update status
                </button>
              </form>
            )}
            <div className="detail-columns">
              <section>
                <h3>Comments</h3>
                {comments.items.length ? (
                  comments.items.map((comment) => (
                    <article
                      className={`comment ${comment.internal ? "internal" : ""}`}
                      key={comment.id}
                    >
                      <div className="comment-meta">
                        <strong>{comment.author_name || "Project team"}</strong>
                        {comment.internal && (
                          <span className="tag">Staff only</span>
                        )}
                        <DateLabel value={comment.created_at} />
                      </div>
                      <p className="prose">{comment.body}</p>
                    </article>
                  ))
                ) : (
                  <p className="muted">
                    No comments yet. Add context or ask a question.
                  </p>
                )}
                <Notice error={comments.error} />
                {comments.hasMore && (
                  <button disabled={comments.busy} onClick={comments.loadMore}>
                    {comments.busy ? "Loading..." : "Load more comments"}
                  </button>
                )}
                <form
                  className="comment-form"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void action.run(
                      async () => {
                        await api(`/api/v1/requests/${id}/comments`, "POST", {
                          body,
                          internal,
                        });
                        setBody("");
                        resource.reload();
                      },
                      "Comment posted.",
                      "comment.submit",
                    );
                  }}
                >
                  <Field label="Add a comment">
                    <textarea
                      required
                      maxLength={10000}
                      rows={4}
                      value={body}
                      onChange={(event) => setBody(event.target.value)}
                      placeholder="A useful detail, question, or update..."
                    />
                  </Field>
                  {admin && (
                    <label className="checkbox">
                      <input
                        type="checkbox"
                        checked={internal}
                        onChange={(event) => setInternal(event.target.checked)}
                      />
                      Staff only
                    </label>
                  )}
                  <button className="primary" disabled={action.busy}>
                    {action.busy ? "Posting..." : "Post comment"}
                  </button>
                </form>
              </section>
              <aside className="history">
                <h3>Progress</h3>
                <ol className="timeline">
                  {history.items.map((entry) => (
                    <li key={entry.id}>
                      <StatusBadge status={entry.to_status} />
                      <span className="muted">
                        {entry.actor_name || "Project team"}
                      </span>
                      <DateLabel value={entry.created_at} />
                    </li>
                  ))}
                </ol>
                <Notice error={history.error} />
                {history.hasMore && (
                  <button disabled={history.busy} onClick={history.loadMore}>
                    {history.busy ? "Loading..." : "Load more history"}
                  </button>
                )}
              </aside>
            </div>
          </>
        )
      )}
    </section>
  );
}
