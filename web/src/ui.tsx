import type { ReactNode } from "react";
import { APIError } from "./api";
import { statusLabels, type Status } from "./types";

export function StatusBadge({ status }: { status: Status }) {
  return (
    <span className={`status status-${status}`}>
      {statusLabels[status] ?? status}
    </span>
  );
}
export function DateLabel({ value }: { value: string }) {
  return (
    <time dateTime={value}>
      {new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
      }).format(new Date(value))}
    </time>
  );
}
export function Notice({
  error,
  success,
}: {
  error?: Error;
  success?: string;
}) {
  if (error)
    return (
      <div className="notice error" role="alert">
        {error.message}{" "}
        {error instanceof APIError && error.status === 401 && (
          <a href="/auth/login">Sign in</a>
        )}
      </div>
    );
  return success ? (
    <div className="notice success" role="status">
      {success}
    </div>
  ) : null;
}
export function Empty({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="empty">
      <span className="empty-mark" aria-hidden="true">
        ↗
      </span>
      <h3>{title}</h3>
      <p>{children}</p>
    </div>
  );
}
export function Loading() {
  return (
    <div className="loading" role="status">
      Loading<span aria-hidden="true">...</span>
    </div>
  );
}
export function Pagination({
  offset,
  count,
  onChange,
  limit = 20,
}: {
  offset: number;
  count: number;
  onChange: (offset: number) => void;
  limit?: number;
}) {
  if (offset === 0 && count < limit) return null;
  return (
    <nav className="pagination" aria-label="Pagination">
      <button
        disabled={offset === 0}
        onClick={() => onChange(Math.max(0, offset - limit))}
      >
        ← Previous
      </button>
      <span>Page {Math.floor(offset / limit) + 1}</span>
      <button disabled={count < limit} onClick={() => onChange(offset + limit)}>
        Next →
      </button>
    </nav>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint && <small>{hint}</small>}
    </label>
  );
}
export function Brand() {
  return (
    <a className="brand" href="/">
      <span className="brand-mark" aria-hidden="true">
        ↗
      </span>
      <span>
        NERDS WHO FISH<span className="brand-sub">THE FEEDBACK NOTEBOOK</span>
      </span>
    </a>
  );
}
