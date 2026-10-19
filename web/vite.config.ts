import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { getThemeScript } from "@braedonsaunders/appkit-ui/theme-script";
import type { Plugin } from "vite";
import { theme } from "./src/theme";

// AppKit's pre-paint theme script goes first in <head>, so the stored (or
// default dark) theme class is on <html> before anything paints.
const themeScript: Plugin = {
  name: "ether-theme-script",
  transformIndexHtml: () => [{ tag: "script", children: getThemeScript(theme), injectTo: "head-prepend" }],
};

// The production bundle is embedded into the Go binary from
// ../internal/admin/dist. `public/.gitkeep` is copied into the output so the
// directory stays in git (and go:embed has something to embed) even after a
// clean build wipes stale fingerprinted assets.
//
// `vite --mode mock` serves the UI against the in-browser fake backend in
// src/mock, for working on screens before the server implements them. The
// mock is only reachable behind `import.meta.env.MODE === "mock"`, which is a
// compile-time constant, so it never ships in the production bundle.
export default defineConfig(({ mode }) => ({
  plugins: [react(), tailwindcss(), themeScript],
  base: "/ether/",
  build: {
    outDir: "../internal/admin/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 700,
  },
  server: {
    port: 5173,
    // Mock mode serves the API in the browser; never touch a real server then.
    proxy: mode === "mock" ? undefined : { "/ether/api": process.env.ETHER_API ?? "http://localhost:32400" },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    css: false,
    testTimeout: 15000,
  },
}));
