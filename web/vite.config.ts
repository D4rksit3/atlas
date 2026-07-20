import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// La GUI corre en :5173 y habla con el control plane vía proxy (evita CORS).
// El destino se toma de ATLAS_CONTROL_PLANE para poder apuntar a un puerto
// dinámico (lo usa scripts/dev.sh). Por defecto: http://localhost:8080.
const controlPlane = process.env.ATLAS_CONTROL_PLANE || "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/v1": controlPlane,
      "/healthz": controlPlane,
    },
  },
});
