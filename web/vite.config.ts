import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/marstack.govern.v1.CatalogService": "http://localhost:8080",
      "/v1/events": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
});
