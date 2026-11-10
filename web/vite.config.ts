import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

// The dev server proxies the API, WebDAV, and WebSockets to a local
// `lydiabox up`. changeOrigin stays false so the forwarded Host matches the
// browser Origin, which the server requires for cookie-authenticated writes.
const backend = process.env.LYDIABOX_DEV_BACKEND ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": { target: backend, ws: true, changeOrigin: false },
      "/webdav": { target: backend, changeOrigin: false },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 900,
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
