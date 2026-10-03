import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In development the Go server runs on :8088; proxying keeps the browser
// on one origin, so no CORS configuration is needed locally.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: { "/v1": "http://localhost:8088", "/healthz": "http://localhost:8088" },
  },
  test: { environment: "jsdom", setupFiles: ["./src/test-setup.ts"] },
});
