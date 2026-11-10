import { useEffect, useState } from "react";
import {
  api,
  APIError,
  setCSRF,
  useAction,
  usePageTail,
  useResource,
} from "./api";
import { Admin } from "./Admin";
import { Changelog } from "./Changelog";
import { Requests, RequestDetail } from "./Requests";
import { Brand, DateLabel, Empty, Loading, Notice, StatusBadge } from "./ui";
import type { Page, Project, PublicRequest, User } from "./types";

function useNavigation() {
  const [hash, setHash] = useState(location.hash);
  useEffect(() => {
    const update = () => setHash(location.hash);
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, []);
  const params = new URLSearchParams(hash.slice(1));
  function navigate(project: string, view = "requests", request?: string) {
    const next = new URLSearchParams({ project, view });
    if (request) next.set("request", request);
    location.hash = next.toString();
  }
  return {
    projectID: params.get("project") ?? "",
    view: params.get("view") ?? "requests",
    requestID: params.get("request"),
    navigate,
  };
}

export function App() {
  const publicToken = /^\/s\/([A-Za-z0-9_-]+)\/?$/.exec(location.pathname)?.[1];
  return publicToken ? <PublicStatus token={publicToken} /> : <Portal />;
}

function Portal() {
  const session = useResource<User>("/api/v1/me");
  const logout = useAction();
  useEffect(() => {
    setCSRF(session.data?.csrf_token ?? "");
  }, [session.data]);
  if (session.loading)
    return (
      <main className="shell">
        <Brand />
        <Loading />
      </main>
    );
  if (
    session.error &&
    (!(session.error instanceof APIError) || session.error.status !== 401)
  )
    return (
      <main className="shell">
        <Brand />
        <Notice error={session.error} />
        <button onClick={session.reload}>Try again</button>
      </main>
    );
  if (!session.data) return <Landing />;
  return (
    <>
      <header className="topbar">
        <Brand />
        <div className="account">
          <span>{session.data.name || session.data.email}</span>
          <button
            className="quiet"
            disabled={logout.busy}
            onClick={() =>
              void logout.run(async () => {
                await api("/auth/logout", "POST");
                setCSRF("");
                session.reload();
              }, "Signed out.")
            }
          >
            Sign out
          </button>
        </div>
      </header>
      <Notice error={logout.error} />
      <Workspace user={session.data} />
    </>
  );
}

function Landing() {
  return (
    <div className="landing">
      <header className="topbar">
        <Brand />
        <span className="eyebrow">A LITTLE INPUT. A BETTER PRODUCT.</span>
      </header>
      <main className="landing-grid">
        <section className="landing-copy">
          <p className="eyebrow">GOOD IDEAS START A CONVERSATION</p>
          <h1>
            What should
            <br />
            we build <em>next?</em>
          </h1>
          <p className="lede">
            A place for your ideas, the small frustrations, and the things that
            would make a real difference.
          </p>
          <a className="button primary large" href="/auth/login">
            Sign in to share an idea <span aria-hidden="true">↗</span>
          </a>
          <p className="hint">
            Use the Google account invited to your project.
          </p>
        </section>
        <aside className="notebook" aria-label="How feedback works">
          <div className="notebook-top">
            <span>THE PATH FROM IDEA TO REALITY</span>
            <span aria-hidden="true">01 / 03</span>
          </div>
          <ol>
            <li>
              <span className="step-number">01</span>
              <div>
                <h2>Put it on the page.</h2>
                <p>Tell us what you need and why it matters.</p>
              </div>
            </li>
            <li>
              <span className="step-number">02</span>
              <div>
                <h2>Find the right direction.</h2>
                <p>Discuss, vote, and follow the decisions.</p>
              </div>
            </li>
            <li>
              <span className="step-number">03</span>
              <div>
                <h2>See it out in the world.</h2>
                <p>Track progress from first thought to shipped.</p>
              </div>
            </li>
          </ol>
          <p className="notebook-foot">
            YOUR EXPERIENCE SHAPES WHAT COMES NEXT.
          </p>
        </aside>
      </main>
      <footer>Made with care by Nerds Who Fish.</footer>
    </div>
  );
}

function Workspace({ user }: { user: User }) {
  const projects = useResource<Page<Project>>("/api/v1/projects?limit=100");
  const nav = useNavigation();
  const selected =
    projects.data?.items.find((project) => project.id === nav.projectID) ??
    projects.data?.items[0];
  const admin = user.role === "admin";
  return (
    <main className="workspace">
      <aside className="sidebar">
        <p className="eyebrow">YOUR PROJECTS</p>
        {projects.loading ? (
          <Loading />
        ) : (
          <label className="field">
            <span className="sr-only">Current project</span>
            <select
              value={selected?.id ?? ""}
              onChange={(event) => nav.navigate(event.target.value)}
            >
              {!selected && <option value="">No projects yet</option>}
              {projects.data?.items.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <nav aria-label="Project navigation">
          <button
            className={nav.view === "requests" ? "nav-link active" : "nav-link"}
            onClick={() => nav.navigate(selected?.id ?? "")}
          >
            <span aria-hidden="true">↗</span> Requests
          </button>
          <button
            className={
              nav.view === "changelog" ? "nav-link active" : "nav-link"
            }
            onClick={() => nav.navigate(selected?.id ?? "", "changelog")}
          >
            <span aria-hidden="true">◷</span> Changelog
          </button>
          {admin && (
            <button
              className={nav.view === "admin" ? "nav-link active" : "nav-link"}
              onClick={() => nav.navigate(selected?.id ?? "", "admin")}
            >
              <span aria-hidden="true">⚙</span> Administration
            </button>
          )}
        </nav>
        <div className="sidebar-note">
          <span className="eyebrow">A WORK IN PROGRESS</span>
          <p>Good products get better through honest conversations.</p>
        </div>
      </aside>
      <div className="content">
        <Notice error={projects.error} />
        {nav.view === "admin" && admin ? (
          <Admin
            project={selected}
            projects={projects.data?.items ?? []}
            onProjectsChanged={projects.reload}
          />
        ) : selected ? (
          <>
            <div className="project-heading">
              <p className="eyebrow">{selected.slug} / FEEDBACK</p>
              <h1>{selected.name}</h1>
              {selected.description && <p>{selected.description}</p>}
            </div>
            {nav.view === "changelog" ? (
              <Changelog key={selected.id} project={selected} admin={admin} />
            ) : nav.requestID ? (
              <RequestDetail
                key={nav.requestID}
                id={nav.requestID}
                project={selected}
                admin={admin}
                onBack={() => nav.navigate(selected.id)}
              />
            ) : (
              <Requests
                key={selected.id}
                project={selected}
                onOpen={(id) => nav.navigate(selected.id, "requests", id)}
              />
            )}
          </>
        ) : (
          !projects.loading && (
            <Empty
              title={
                admin ? "Start with a project" : "No projects assigned yet"
              }
            >
              {admin
                ? "Open Administration to create the first project and invite its members."
                : "You are signed in, but your account has not been invited to a project. Ask the project owner to add your email address."}
            </Empty>
          )
        )}
      </div>
    </main>
  );
}

function PublicStatus({ token }: { token: string }) {
  const request = useResource<PublicRequest>(
    `/api/v1/public/requests/${encodeURIComponent(token)}`,
  );
  const comments = usePageTail(
    `/api/v1/public/requests/${encodeURIComponent(token)}/comments`,
    request.data?.comments,
    request.data?.comments_total,
  );
  const history = usePageTail(
    `/api/v1/public/requests/${encodeURIComponent(token)}/history`,
    request.data?.history,
    request.data?.history_total,
  );
  return (
    <>
      <header className="topbar">
        <Brand />
        <span className="eyebrow">REQUEST STATUS</span>
      </header>
      <main className="public-content">
        {request.loading ? (
          <Loading />
        ) : request.error ? (
          <Empty title="This status page is unavailable">
            The link may be incorrect, or the project owner may have disabled
            public status pages.
          </Empty>
        ) : (
          request.data && (
            <>
              <p className="eyebrow">FROM THE FEEDBACK NOTEBOOK</p>
              <h1>{request.data.title}</h1>
              <StatusBadge status={request.data.status} />
              <section className="panel">
                <h2>Updates</h2>
                {comments.items.length ? (
                  comments.items.map((comment, i) => (
                    <article className="comment" key={comment.id ?? i}>
                      <DateLabel value={comment.created_at} />
                      <p className="prose">{comment.body}</p>
                    </article>
                  ))
                ) : (
                  <p className="muted">No public updates yet.</p>
                )}
                <Notice error={comments.error} />
                {comments.hasMore && (
                  <button disabled={comments.busy} onClick={comments.loadMore}>
                    {comments.busy ? "Loading..." : "Load more updates"}
                  </button>
                )}
              </section>
              <section className="panel">
                <h2>Progress</h2>
                <ol className="timeline">
                  {history.items.map((entry, i) => (
                    <li key={entry.id ?? i}>
                      <StatusBadge status={entry.to_status} />
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
              </section>
            </>
          )
        )}
      </main>
    </>
  );
}
