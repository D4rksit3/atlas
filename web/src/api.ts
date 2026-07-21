// Tipos espejo de pkg/api/types.go y el cliente de la API del control plane.
// Si cambias el contrato en Go, actualiza estos tipos.

export type Provider = "onprem" | "aws" | "oci";

export interface Node {
  name: string;
  role: "control-plane" | "worker" | string;
  ready: boolean;
}

export interface Placement {
  node: string;
  pods: number;
}

export interface Workload {
  name: string;
  namespace: string;
  kind: string;
  replicas: number;
  placement?: Placement[] | null;
}

export interface Link {
  from: string;
  to: string;
}

export interface Snapshot {
  nodes: Node[] | null;
  workloads: Workload[] | null;
  links: Link[] | null;
}

export interface ClusterView {
  clusterId: string;
  name: string;
  provider: Provider;
  online: boolean;
  lastSeen: string;
  agentVersion: string;
  snapshot: Snapshot;
}

export interface Topology {
  clusters: ClusterView[] | null;
  generatedAt: string;
}

/** Descarga la topología agregada desde el control plane. */
export async function fetchTopology(): Promise<Topology> {
  const res = await fetch("/v1/topology");
  if (!res.ok) throw new Error(`topology HTTP ${res.status}`);
  return (await res.json()) as Topology;
}

// ---- Acciones: la GUI ordena, el agente ejecuta ----

export type ActionKind = "scale" | "restart";
export type ActionStatus = "pending" | "dispatched" | "done" | "error";

export interface ActionRequest {
  kind: ActionKind;
  namespace: string;
  workload: string;
  workloadKind: string;
  replicas: number;
}

export interface Action {
  id: string;
  kind: ActionKind;
  namespace: string;
  workload: string;
  workloadKind: string;
  replicas: number;
  status: ActionStatus;
  error?: string;
  createdAt: string;
  updatedAt: string;
}

/** Encola una acción sobre una carga de un clúster. */
export async function postAction(
  clusterId: string,
  req: ActionRequest,
): Promise<Action> {
  const res = await fetch(`/v1/clusters/${encodeURIComponent(clusterId)}/actions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
  return (await res.json()) as Action;
}

/** Lista las acciones recientes de un clúster (para ver su estado). */
export async function fetchActions(clusterId: string): Promise<Action[]> {
  const res = await fetch(`/v1/clusters/${encodeURIComponent(clusterId)}/actions`);
  if (!res.ok) throw new Error(`actions HTTP ${res.status}`);
  return (await res.json()) as Action[];
}
