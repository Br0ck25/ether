import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "/ether/",
  build: {
    outDir: "../internal/admin/dist",
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: { "/ether/api": "http://localhost:32400" },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
