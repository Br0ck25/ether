import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

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
  plugins: [react(), tailwindcss()],
  base: "/ether/",
  build: {
    outDir: "../internal/admin/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 700,
    rollupOptions: {
      onwarn(warning, warn) {
        // AppKit's published modules carry React Server Component
        // directives; they are meaningless in a client-only SPA.
        if (warning.code === "MODULE_LEVEL_DIRECTIVE") return;
        warn(warning);
      },
    },
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
