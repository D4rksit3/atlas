// Set de iconos propio (línea, 24x24) — el mismo lenguaje visual del mapa.
import type { Provider } from "./api";

export type IconKey =
  | "controlplane"
  | "cluster"
  | "server"
  | "agent"
  | "console"
  | "workload"
  | "database"
  | "gitops";

const PATHS: Record<IconKey, string> = {
  controlplane:
    "M12 2 3 6.5v11L12 22l9-4.5v-11L12 2ZM3 6.5l9 4.5 9-4.5M12 11v11",
  cluster:
    "M12 3.2 4.6 7.2v7.6L12 18.8l7.4-4V7.2ZM12 4v2.6M12 15.4V18M6 8.2l2.2 1.2M15.8 12.4 18 13.6",
  server:
    "M4.5 5h15v6h-15zM4.5 13h15v6h-15zM7.5 8h.01M7.5 16h.01M11 8h5.5M11 16h5.5",
  agent:
    "M6.5 11h11v8.5h-11zM12 11V8.4M9.2 6.6a4 4 0 0 1 5.6 0M7.2 4.8a7 7 0 0 1 9.6 0",
  console: "M3 5h18v14H3zM3 9.5h18M6 7.2h.01M8.6 7.2h.01",
  // Deployment: capas apiladas (réplicas).
  workload:
    "M12 3 3.5 7.2 12 11.4l8.5-4.2ZM3.5 12 12 16.2 20.5 12M3.5 16.8 12 21l8.5-4.2",
  // StatefulSet / almacén: cilindro.
  database:
    "M12 3c3.9 0 6.5 1.1 6.5 2.5S15.9 8 12 8 5.5 6.9 5.5 5.5 8.1 3 12 3ZM5.5 5.5v13c0 1.4 2.6 2.5 6.5 2.5s6.5-1.1 6.5-2.5v-13M5.5 12c0 1.4 2.6 2.5 6.5 2.5s6.5-1.1 6.5-2.5",
  // GitOps: ramas de git (dos nodos y una bifurcación).
  gitops:
    "M6.5 3a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5ZM6.5 8v8M6.5 16a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5ZM17.5 6a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5ZM17.5 11c0 3-2 4-5 4.5",
};

export function Icon({ name, size = 22 }: { name: IconKey; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.9}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d={PATHS[name]} />
    </svg>
  );
}

/** Color de acento por provider (coincide con el diagrama de arquitectura). */
export const providerColor: Record<Provider, string> = {
  onprem: "#5A6577",
  aws: "#12B5A5",
  oci: "#E7476B",
};

export const providerLabel: Record<Provider, string> = {
  onprem: "On-premises",
  aws: "AWS",
  oci: "OCI",
};
