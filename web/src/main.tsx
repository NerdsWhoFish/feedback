import { Component, StrictMode } from "react";
import type { ReactNode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource/dm-sans/latin-400.css";
import "@fontsource/dm-sans/latin-500.css";
import "@fontsource/dm-sans/latin-600.css";
import "@fontsource/dm-sans/latin-700.css";
import "@fontsource/space-mono/latin-400.css";
import { App } from "./App";
import { captureError, startTelemetry } from "./telemetry";
import "./style.css";

class ErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch() {
    captureError("app.render");
  }
  render() {
    return this.state.failed ? (
      <main className="shell">
        <h1>Something went wrong.</h1>
        <p>Your saved requests are safe. Reload the page to try again.</p>
        <a className="button" href="/">
          Reload feedback
        </a>
      </main>
    ) : (
      this.props.children
    );
  }
}

void startTelemetry().finally(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ErrorBoundary>
        <App />
      </ErrorBoundary>
    </StrictMode>,
  );
});
