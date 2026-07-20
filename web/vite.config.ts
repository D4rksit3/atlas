import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Durante el desarrollo, la GUI corre en :5173 y el control plane en :8080.
// El proxy evita problemas de CORS: /v1/* y /healthz se redirigen al backend.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/v1": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
});
