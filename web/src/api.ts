import { useCallback, useEffect, useState } from "react";
import { captureError } from "./telemetry";
import type { Page } from "./types";

let csrfToken = "";
export function setCSRF(value: string) {
  csrfToken = value;
}
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(
  url: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(url, {
    method,
    credentials: "same-origin",
    signal,
    headers: {
      "Content-Type": "application/json",
      ...(method !== "GET" ? { "X-CSRF-Token": csrfToken } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).catch((error: unknown) => {
    if (signal?.aborted) throw error;
    throw new Error(
      "We could not reach the server. Check your connection and try again.",
    );
  });
  if (!response.ok) {
    let message =
      response.status === 401
        ? "Your session has ended. Sign in again to continue."
        : "That request could not be completed. Please try again.";
    try {
      const result = await response.json();
      message = result.error?.message || message;
    } catch {
      /* Preserve the HTTP fallback when a proxy returns a non-JSON error. */
    }
    throw new APIError(response.status, message);
  }
  return response.status === 204
    ? (undefined as T)
    : (response.json() as Promise<T>);
}
export function useResource<T>(url: string | null) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<Error>();
  const [loading, setLoading] = useState(true);
  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  useEffect(() => {
    const controller = new AbortController();
    setData(undefined);
    setError(undefined);
    setLoading(!!url);
    if (url) {
      api<T>(url, "GET", undefined, controller.signal)
        .then((result) => {
          if (!controller.signal.aborted) setData(result);
        })
        .catch((err: Error) => {
          if (!controller.signal.aborted) {
            setError(err);
            if (!(err instanceof APIError) || err.status >= 500)
              captureError("request.load");
          }
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false);
        });
    }
    return () => controller.abort();
  }, [url, version]);
  return { data, error, loading, reload };
}
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error>();
  const [success, setSuccess] = useState("");
  async function run(
    action: () => Promise<void>,
    message = "Saved.",
    operation = "admin.update",
  ) {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    setSuccess("");
    try {
      await action();
      setSuccess(message);
    } catch (err) {
      setError(
        err instanceof Error
          ? err
          : new Error("Something went wrong. Please try again."),
      );
      if (!(err instanceof APIError) || err.status >= 500)
        captureError(operation);
    } finally {
      setBusy(false);
    }
  }
  return { busy, error, success, run };
}

export function usePageTail<T>(
  url: string,
  initial: T[] | undefined,
  total = 0,
) {
  const [extra, setExtra] = useState<T[]>([]);
  const action = useAction();
  useEffect(() => setExtra([]), [url, initial]);
  const items = [...(initial ?? []), ...extra];
  function loadMore() {
    void action.run(
      async () => {
        const page = await api<Page<T>>(
          `${url}?limit=100&offset=${items.length}`,
        );
        setExtra((previous) => [...previous, ...page.items]);
      },
      "",
      "request.load",
    );
  }
  return {
    items,
    hasMore: items.length < total,
    loadMore,
    busy: action.busy,
    error: action.error,
  };
}
