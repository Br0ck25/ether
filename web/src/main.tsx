import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import "./styles.css";

async function start() {
  // `vite --mode mock` runs the UI against the in-browser fake backend.
  // MODE is a compile-time constant, so production builds drop this branch
  // and never emit the mock chunk.
  if (import.meta.env.MODE === "mock") {
    const { installMockBackend } = await import("./mock/install");
    installMockBackend();
  }
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <BrowserRouter basename="/ether">
        <App />
      </BrowserRouter>
    </StrictMode>,
  );
}

void start();
