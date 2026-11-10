import { initializeTelemetry } from "@nerdswhofish/browser-telemetry";

let telemetry: ReturnType<typeof initializeTelemetry> | undefined;
export async function startTelemetry() {
  try {
    const response = await fetch("/api/v1/config", {
      signal: AbortSignal.timeout(3000),
    });
    if (!response.ok) return;
    const config = await response.json();
    if (!config.telemetry_enabled) return;
    telemetry = initializeTelemetry({
      url: new URL("/api/v1/telemetry", location.origin).href,
      local: true,
      app: {
        name: "feedback",
        version: config.version,
        environment: config.environment,
      },
      routes: [
        "/",
        "/api/v1/config",
        "/api/v1/me",
        "/api/v1/projects",
        "/api/v1/keys",
        "/api/v1/telemetry",
      ],
      assets: Array.from(document.scripts)
        .map((script) => new URL(script.src || location.href).pathname)
        .filter((path) => path.startsWith("/assets/")),
      operations: [
        "request.load",
        "request.submit",
        "comment.submit",
        "vote.update",
        "status.update",
        "admin.update",
        "app.render",
      ],
    });
  } catch {
    /* Telemetry must not prevent access to feedback. */
  }
}
export function captureError(operation: string) {
  telemetry?.captureError(new Error("Operation failed"), operation);
}
